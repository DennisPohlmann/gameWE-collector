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

// player-stats keeps the players of the game with the start skill and no
// counters, deletes its events and keeps the server statistics; the other
// game is untouched; the dry run before changes nothing.
func TestIntegrationResetPlayerStats(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	hub.Close(context.Background())
	before := q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`)
	serverKills := q(t, db, `SELECT kills FROM hlstats_Servers WHERE game = 'css'`)
	if serverKills == 0 {
		t.Fatal("fixture: no server kills")
	}
	counts, err := store.ResetCounts(context.Background(), "css", stats.ResetPlayerStats)
	if err != nil {
		t.Fatal(err)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`) != before || !strings.Contains(stats.FormatCounts(counts), "Events_Frags 2") {
		t.Fatalf("dry run: %s", stats.FormatCounts(counts))
	}
	if strings.Contains(stats.FormatCounts(counts), "Servers") {
		t.Errorf("dry run touches the servers: %s", stats.FormatCounts(counts))
	}
	if _, err := store.Reset(context.Background(), "css", stats.ResetPlayerStats); err != nil {
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
		{"css server counters kept", q(t, db, `SELECT kills FROM hlstats_Servers WHERE game = 'css'`), serverKills},
		{"css weapon totals", q(t, db, `SELECT COALESCE(SUM(kills), 0) FROM hlstats_Weapons WHERE game = 'css'`), 0},
		{"tf frag kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE game_id = 2`), 1},
		{"tf player kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'tf' AND kills = 1`), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	if _, err := store.Reset(context.Background(), "nope", stats.ResetPlayerStats); err == nil {
		t.Error("unknown game reset")
	}
	if _, err := store.Reset(context.Background(), "css' OR '1'='1", stats.ResetAll); err == nil {
		t.Error("game code with SQL accepted")
	}
}

// all-stats resets the server statistics too and keeps the players; the
// old mode name stats (jobs of an older web) is player-stats.
func TestIntegrationResetAllStats(t *testing.T) {
	for _, mode := range []string{stats.ResetAllStats, stats.ResetStatsOld} {
		t.Run(mode, func(t *testing.T) {
			db := testDB(t)
			hub, store, _ := resetFixture(t, db)
			flush(t, hub)
			hub.Close(context.Background())
			if _, err := store.Reset(context.Background(), "css", mode); err != nil {
				t.Fatal(err)
			}
			serverKills := 0
			if mode == stats.ResetStatsOld { // = player-stats: the servers keep their kills
				serverKills = 2
			}
			for _, c := range []struct {
				name      string
				got, want int
			}{
				{"css players kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css'`), 2},
				{"css players reset", q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css' AND kills <> 0`), 0},
				{"css server counters", q(t, db, `SELECT kills FROM hlstats_Servers WHERE game = 'css'`), serverKills},
				{"tf server counters kept", q(t, db, `SELECT kills FROM hlstats_Servers WHERE game = 'tf'`), 1},
			} {
				if c.got != c.want {
					t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
				}
			}
		})
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
		{"css server counters", q(t, db, `SELECT kills FROM hlstats_Servers WHERE game = 'css'`), 0},
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
	second, _ := store.AddJob(ctx, stats.JobReset, "css", stats.ResetPlayerStats, "test", time.Now())
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
	third, _ := store.AddJob(ctx, stats.JobReset, "tf", stats.ResetStatsOld, "test", time.Now()) // queued by an older web
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

	// A template (a ranking without servers) is not reset, also when an
	// older web queued it.
	exec(t, db, `INSERT INTO hlstats_Games (id, code, name, realgame, hidden) VALUES (3, 'cstrike', 'Counter-Strike', 'cstrike', '0')`)
	for _, typ := range []string{stats.JobReset, stats.JobScheduledReset} {
		id, err := store.AddJob(ctx, typ, "cstrike", stats.ResetAll, "test", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := (&Jobs{Store: store, Log: jobs.Log}).Poll(ctx); err != nil {
			t.Fatal(err)
		}
		if j, _ := store.Job(ctx, id); j.Status != stats.JobFailed || !strings.Contains(j.Result, "template") {
			t.Errorf("%s job of a template %+v", typ, j)
		}
	}
}

// !gamewe_reset: one player's totals, weekly and monthly rows and history
// go, the events and the other players stay; banned players are not reset.
// The lookups of !weapons and !actions ignore case.
func TestIntegrationPlayerOwnReset(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	hub.Close(context.Background())
	ctx := context.Background()
	id := func(uid string) int {
		return q(t, db, `SELECT p.playerId FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = ? AND u.game = 'css'`, uid)
	}
	a, b := id("0:101"), id("0:102")
	frags := q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`)
	bKills := q(t, db, `SELECT kills FROM hlstats_Players WHERE playerId = ?`, b)
	if q(t, db, `SELECT kills FROM hlstats_Players WHERE playerId = ?`, a) == 0 {
		t.Fatal("fixture: player A has no kills")
	}
	for _, p := range []int{a, b} { // weapon rows come from the plugin's weaponstats lines
		if _, err := db.ExecContext(ctx, db.Q(`INSERT INTO hlstats_Players_Weapons (player_id, yearweek_id, game_id, weapon_id, kills) VALUES (?, 1, 1, 1, 3)`), p); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Unix(1792000000, 0)
	if ok, err := store.ResetPlayer(ctx, int64(a), 1000, at); err != nil || !ok {
		t.Fatalf("ResetPlayer: %v %v", ok, err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"skill", q(t, db, `SELECT skill FROM hlstats_Players WHERE playerId = ?`, a), 1000},
		{"kills", q(t, db, `SELECT kills + deaths + headshots FROM hlstats_Players WHERE playerId = ?`, a), 0},
		{"weapon rows", q(t, db, `SELECT COUNT(*) FROM hlstats_Players_Weapons WHERE player_id = ?`, a), 0},
		{"action rows", q(t, db, `SELECT COUNT(*) FROM hlstats_Players_Actions WHERE player_id = ?`, a), 0},
		{"history", q(t, db, `SELECT COUNT(*) FROM hlstats_Players_History WHERE playerId = ?`, a), 0},
		{"name counters", q(t, db, `SELECT COALESCE(SUM(kills), 0) FROM hlstats_PlayerNames WHERE playerId = ?`, a), 0},
		{"reset row", q(t, db, `SELECT COUNT(*) FROM hlstats_Players_Resets WHERE player_id = ? AND timestamp = 1792000000`, a), 1},
		{"events kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`), frags},
		{"other player kept", q(t, db, `SELECT kills FROM hlstats_Players WHERE playerId = ?`, b), bKills},
		{"other player's weapons kept", q(t, db, `SELECT COUNT(*) FROM hlstats_Players_Weapons WHERE player_id = ?`, b), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.name, c.got, c.want)
		}
	}
	if last, err := store.LastReset(ctx, int64(a)); err != nil || last != 1792000000 {
		t.Errorf("LastReset: %d %v", last, err)
	}
	if last, err := store.LastReset(ctx, int64(b)); err != nil || last != 0 {
		t.Errorf("LastReset without reset: %d %v", last, err)
	}
	if _, err := db.ExecContext(ctx, db.Q(`UPDATE hlstats_Players SET hideranking = 2 WHERE playerId = ?`), b); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ResetPlayer(ctx, int64(b), 1000, at); err != nil || ok || q(t, db, `SELECT kills FROM hlstats_Players WHERE playerId = ?`, b) != bKills {
		t.Errorf("banned player reset: %v %v", ok, err)
	}
	if code, err := store.WeaponCode(ctx, "css", "WEAPON1"); err != nil || code != "weapon1" {
		t.Errorf("WeaponCode: %q %v", code, err)
	}
	if _, err := store.WeaponCode(ctx, "css", "nope"); err != stats.ErrNotFound {
		t.Errorf("WeaponCode of an unknown weapon: %v", err)
	}
	if id, err := store.ActionID(ctx, "css", "planted_the_bomb"); err != nil || id <= 0 {
		t.Errorf("ActionID: %d %v", id, err)
	}
	if _, err := store.ActionID(ctx, "tf", "planted_the_bomb"); err != stats.ErrNotFound {
		t.Errorf("ActionID of another game: %v", err)
	}
}
