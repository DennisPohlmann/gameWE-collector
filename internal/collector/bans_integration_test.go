// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// SourceBans bans in hlstats_Players_Bans: a ban takes a ranked player out
// of the ranking and its end puts the player back; players hidden by an
// admin, inactive players and a VAC ban that came on top are handled.
func TestIntegrationPlayerBans(t *testing.T) {
	db := testDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(db.Q(identityInsert(db, q)), args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO hlstats_Players (playerId, game, game_id, skill, hideranking) VALUES
		(21, 'css', 1, 1000, 0), (22, 'css', 1, 1000, 1), (24, 'css', 1, 1000, 0)`)
	exec(`INSERT INTO hlstats_PlayersInactive (playerId, game, game_id, skill, hideranking) VALUES (23, 'css', 1, 1000, 0)`)
	exec(`INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game) VALUES
		(21, '1:21', 'css'), (22, '1:22', 'css'), (23, '1:23', 'css'), (24, '1:24', 'css')`)
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	hide := func(id int) int {
		t.Helper()
		return count(t, db, `SELECT hideranking FROM hlstats_Players WHERE playerId = ? UNION ALL
			SELECT hideranking FROM hlstats_PlayersInactive WHERE playerId = ?`, id, id)
	}
	ban := stats.Ban{Type: stats.BanTypeSourceBans, Reason: "Cheating", LengthMinutes: 60,
		BannedAt: now.Add(-10 * time.Minute), EndsAt: now.Add(50 * time.Minute)}
	for _, uid := range []string{"1:21", "1:22", "1:23", "1:24", "1:99"} {
		n, err := store.RecordBan(ctx, "css", uid, ban, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]int{"1:21": 1, "1:23": 1, "1:24": 1}[uid]; n != want {
			t.Errorf("%s: %d taken out of the ranking, want %d", uid, n, want)
		}
	}
	// The same ban seen again: no second row.
	if _, err := store.RecordBan(ctx, "css", "1:21", ban, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE active = 1`); n != 4 {
		t.Errorf("%d running bans, want 4 (one per known player)", n)
	}
	if hide(21) != 2 || hide(22) != 1 || hide(23) != 2 || hide(24) != 2 {
		t.Errorf("hideranking 21..24: %d %d %d %d, want 2 1 2 2", hide(21), hide(22), hide(23), hide(24))
	}
	// 24 gets a VAC kick on top: its ban must not put it back.
	if err := store.HideBanned(ctx, stats.Ev{Srv: stats.Server{Game: "css"}}, "1:24"); err != nil {
		t.Fatal(err)
	}
	running, err := store.RunningBans(ctx, stats.BanTypeSourceBans)
	if err != nil || len(running) != 4 {
		t.Fatalf("running %+v %v", running, err)
	}
	backs := map[int64]bool{}
	for _, b := range running {
		back, err := store.EndBan(ctx, b, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		backs[b.PlayerID] = back
	}
	if !backs[21] || backs[22] || !backs[23] || backs[24] {
		t.Errorf("back in the ranking %v, want 21 and 23", backs)
	}
	if hide(21) != 0 || hide(22) != 1 || hide(23) != 0 || hide(24) != 2 {
		t.Errorf("after the end 21..24: %d %d %d %d, want 0 1 0 2", hide(21), hide(22), hide(23), hide(24))
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE active = 0 AND ended_at > 0`); n != 4 {
		t.Errorf("%d ended bans, want 4", n)
	}
	// Banned again with the same start (lifted by mistake): the row runs again.
	if _, err := store.RecordBan(ctx, "css", "1:21", ban, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE player_id = 21`); n != 1 || hide(21) != 2 {
		t.Errorf("rebanned: %d rows, hideranking %d, want 1 row, 2", n, hide(21))
	}
	// A ban without a date reuses the running row instead of adding one.
	if _, err := store.RecordBan(ctx, "css", "1:21", stats.Ban{Type: stats.BanTypeSourceBans, Reason: "x"}, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE player_id = 21`); n != 1 {
		t.Errorf("%d rows for 21, want 1", n)
	}
}

// The fixes of the security audit: a second running ban keeps the player out
// of the ranking whichever ends first, and a unique id over the column's 64
// characters is found again instead of looping on insert and delete.
func TestIntegrationBansAndLongIDs(t *testing.T) {
	db := testDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(db.Q(identityInsert(db, q)), args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO hlstats_Players (playerId, game, game_id, skill, hideranking) VALUES (31, 'css', 1, 1000, 0)`)
	exec(`INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game) VALUES (31, '1:31', 'css')`)
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	for i, kind := range []string{stats.BanTypeSourceBans, stats.BanTypeVAC} {
		if _, err := store.RecordBan(ctx, "css", "1:31", stats.Ban{Type: kind, Reason: "x", BannedAt: now.Add(time.Duration(i) * time.Minute)}, now); err != nil {
			t.Fatal(err)
		}
	}
	var mine []stats.PlayerBan
	for _, kind := range []string{stats.BanTypeSourceBans, stats.BanTypeVAC} {
		running, err := store.RunningBans(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range running {
			if b.PlayerID == 31 {
				mine = append(mine, b)
			}
		}
	}
	if len(mine) != 2 {
		t.Fatalf("running bans: %+v", mine)
	}
	hide := func() int {
		return count(t, db, `SELECT hideranking FROM hlstats_Players WHERE playerId = ?`, 31)
	}
	for i := range mine {
		if !mine[i].Hid {
			continue
		}
		back, err := store.EndBan(ctx, mine[i], now)
		if err != nil || back || hide() != 2 {
			t.Fatalf("first end: back %v, %v, hideranking %d", back, err, hide())
		}
		other := mine[1-i]
		other.Hid = true
		back, err = store.EndBan(ctx, other, now)
		if err != nil || !back || hide() != 0 {
			t.Fatalf("last end: back %v, %v, hideranking %d", back, err, hide())
		}
	}

	ev := stats.Ev{Srv: stats.Server{ID: 1, Game: "css"}, Time: now}
	long := "1:" + strings.Repeat("7", 70)
	id1, _, err := store.Player(ctx, ev, long, "n")
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := store.Player(ctx, ev, long, "n")
	if err != nil || id1 != id2 {
		t.Errorf("long id: %d then %d, %v", id1, id2, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE game = 'css' AND uniqueId LIKE '1:777%'`); n != 1 {
		t.Errorf("long id rows: %d", n)
	}
}
