// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/stats"
)

// resetFixture: a css server with two players and a tf server with one,
// some kills on both. It returns the hub (still running) and its store.
func resetFixture(t *testing.T, db *database.DB) (*Hub, *stats.Store, func(addr string, lines ...string)) {
	t.Helper()
	css, tf := "198.51.100.60:27015", "198.51.100.61:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{css, "css"}, {tf, "tf"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	feed := func(addr string, lines ...string) {
		t.Helper()
		for _, l := range lines {
			if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
	}
	feed(css,
		`"A<2><STEAM_1:0:101><CT>" killed "B<3><STEAM_1:0:102><TERRORIST>" with "weapon1"`,
		`"B<3><STEAM_1:0:102><TERRORIST>" killed "A<2><STEAM_1:0:101><CT>" with "weapon1" (headshot)`,
		`"A<2><STEAM_1:0:101><CT>" triggered "Planted_The_Bomb"`)
	feed(tf, `"T<2><[U:1:203]><Red>" killed "U<3><[U:1:204]><Blue>" with "scattergun"`)
	return hub, hub.store.(*stats.Store), feed
}

func flush(t *testing.T, hub *Hub) {
	t.Helper()
	// a pause of a game without servers returns at once; one of each game
	// waits until the queued lines are processed
	for _, g := range []string{"css", "tf"} {
		resume, _, err := hub.PauseGame(context.Background(), g)
		resume()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func q(t *testing.T, db *database.DB, sql string, args ...any) int {
	t.Helper()
	return count(t, db, sql, args...)
}

// stats keeps the players of the game with the start skill and no
// counters, deletes its events; the other game is untouched; the dry run
// before changes nothing.
func TestIntegrationResetStats(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	hub.Close(context.Background())
	before := q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`)
	counts, err := store.ResetCounts(context.Background(), "css", stats.ResetStats)
	if err != nil {
		t.Fatal(err)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`) != before || !strings.Contains(stats.FormatCounts(counts), "Events_Frags 2") {
		t.Fatalf("dry run: %s", stats.FormatCounts(counts))
	}
	if _, err := store.Reset(context.Background(), "css", stats.ResetStats); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"css players kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css'`), 2},
		{"css players reset", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css' AND (skill <> 1000 OR kills <> 0 OR deaths <> 0 OR headshots <> 0)`), 0},
		{"css frags gone", q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE game_id = 1`), 0},
		{"css actions gone", q(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions WHERE game_id = 1`), 0},
		{"css history gone", q(t, db, `SELECT COUNT(*) FROM hlstats_Players_History WHERE game = 'css'`), 0},
		{"css unique ids kept", q(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE game = 'css'`), 2},
		{"css server counters", q(t, db, `SELECT kills FROM hlstats_Servers WHERE game = 'css'`), 0},
		{"tf frag kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE game_id = 2`), 1},
		{"tf player kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'tf' AND kills = 1`), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	if _, err := store.Reset(context.Background(), "nope", stats.ResetStats); err == nil {
		t.Error("unknown game reset")
	}
	if _, err := store.Reset(context.Background(), "css' OR '1'='1", stats.ResetAll); err == nil {
		t.Error("game code with SQL accepted")
	}
}

// all deletes the players of the game only.
func TestIntegrationResetAll(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	hub.Close(context.Background())
	if _, err := store.Reset(context.Background(), "css", stats.ResetAll); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"css players", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css'`), 0},
		{"css unique ids", q(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE game = 'css'`), 0},
		{"names of css players", q(t, db, `SELECT COUNT(*) FROM hlstats_PlayerNames n WHERE NOT EXISTS (SELECT 1 FROM hlstats_Players p WHERE p.playerId = n.playerId)`), 0},
		{"tf players", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'tf'`), 2},
		{"tf frag", q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE game_id = 2`), 1},
		{"games kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Games`), 2},
		{"actions kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Actions WHERE game = 'css'`), 3},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// The running collector does a queued reset while lines keep coming: no
// line written afterwards uses an id of before; a second job for the game
// is refused; a job left running by a stopped collector is completed.
func TestIntegrationResetJob(t *testing.T) {
	db := testDB(t)
	hub, store, feed := resetFixture(t, db)
	flush(t, hub)
	css := "198.51.100.60:27015"
	ctx := context.Background()
	first, err := store.AddJob(ctx, stats.JobReset, "css", stats.ResetAll, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, _ := store.AddJob(ctx, stats.JobReset, "css", stats.ResetStats, "test", time.Now())
	jobs := &Jobs{Store: store, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // lines during the reset
		defer wg.Done()
		for i := 0; i < 50; i++ {
			feed(css, fmt.Sprintf(`"A<2><STEAM_1:0:101><CT>" killed "B<3><STEAM_1:0:102><TERRORIST>" with "weapon1" (round %d)`, i))
		}
	}()
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	feed(css, `"A<2><STEAM_1:0:101><CT>" killed "B<3><STEAM_1:0:102><TERRORIST>" with "weapon1"`)
	hub.Close(ctx)
	j1, _ := store.Job(ctx, first)
	j2, _ := store.Job(ctx, second)
	if j1.Status != stats.JobDone || !strings.Contains(j1.Result, "Players") {
		t.Errorf("first job %+v", j1)
	}
	if j2.Status != stats.JobFailed || !strings.Contains(j2.Result, "refused") {
		t.Errorf("second job %+v", j2)
	}
	for _, c := range []struct {
		name string
		sql  string
	}{
		{"frags of unknown killers", `SELECT COUNT(*) FROM hlstats_Events_Frags f WHERE NOT EXISTS (SELECT 1 FROM hlstats_Players p WHERE p.playerId = f.killerId)`},
		{"frags of unknown victims", `SELECT COUNT(*) FROM hlstats_Events_Frags f WHERE NOT EXISTS (SELECT 1 FROM hlstats_Players p WHERE p.playerId = f.victimId)`},
		{"names of unknown players", `SELECT COUNT(*) FROM hlstats_PlayerNames n WHERE NOT EXISTS (SELECT 1 FROM hlstats_Players p WHERE p.playerId = n.playerId)`},
		{"unique ids of unknown players", `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds u WHERE NOT EXISTS (SELECT 1 FROM hlstats_Players p WHERE p.playerId = u.playerId)`},
	} {
		if n := q(t, db, c.sql); n != 0 {
			t.Errorf("%s: %d", c.name, n)
		}
	}
	if n := q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css'`); n != 2 {
		t.Errorf("%d css players after the reset and new lines, want 2 (new ids)", n)
	}

	// A collector stopped during a reset left the job running.
	third, _ := store.AddJob(ctx, stats.JobReset, "tf", stats.ResetStats, "test", time.Now())
	if ok, err := store.ClaimJob(ctx, third, stats.JobPending, time.Now()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := (&Jobs{Store: store, Log: jobs.Log}).Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := store.Job(ctx, third); j.Status != stats.JobDone {
		t.Errorf("recovered job %+v", j)
	}
	if n := q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'tf' AND kills = 0`); n != 2 {
		t.Errorf("tf players after the recovered reset: %d with 0 kills, want 2", n)
	}
	// the heartbeat
	if beat, err := store.LastHeartbeat(ctx); err != nil || time.Since(beat) > time.Minute {
		t.Errorf("heartbeat %v %v", beat, err)
	}
}
