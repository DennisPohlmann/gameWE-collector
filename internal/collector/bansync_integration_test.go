// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/sourcebans"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

// The bans-sync job against the database: Steam rows for every account of
// active and inactive players (also in another spelling), the SourceBans
// rows of the list (lifted and expired ones deleted, also without the
// list), hlstats_Bans_Players, and a player whose SourceBans record the
// list no longer shows is back in the ranking (not one an admin hid).
func TestIntegrationBanSync(t *testing.T) {
	db := testDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(db.Q(q), args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO hlstats_Players (playerId, game, game_id, skill, hideranking) VALUES
		(31, 'css', 1, 1000, 0), (32, 'css', 1, 1000, 1), (34, 'csgo', 1, 1000, 0)`)
	exec(`INSERT INTO hlstats_PlayersInactive (playerId, game, game_id, skill, hideranking) VALUES (33, 'css', 1, 1000, 0)`)
	exec(`INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game) VALUES
		(31, '1:31', 'css'), (32, '1:32', 'css'), (33, '1:33', 'css'), (34, 'STEAM_1:1:31', 'csgo'), (35, 'BOT:x', 'css')`)
	set := settings.NewLive(settings.Defaults())
	s := settings.Defaults()
	s.SteamAPIKey = "KEY"
	s.SourceBansURL = "bans.example.org"
	set.Set(s)
	store := stats.NewStore(db, set)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	clock := &syncClock{t: now}
	id := func(uid string) uint64 { v, _ := ID64(uid); return v }
	banDay := now.Truncate(24 * time.Hour).Add(-10 * 24 * time.Hour) // the VAC ban of 1:33
	str := func(uid string) string { return strconv.FormatUint(id(uid), 10) }

	// Two SourceBans bans recorded from kicks: 31 taken out of the ranking,
	// 32 hidden by an admin before.
	for _, uid := range []string{"1:31", "1:32"} {
		if _, err := store.RecordBan(ctx, "css", uid, stats.Ban{Type: stats.BanTypeSourceBans, Reason: "Cheating"}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	list := []sourcebans.Ban{
		{Steam: "1:31", Reason: "Cheating", Length: "Permanent", Date: "2026-10-20T10:00:00Z", State: sourcebans.Permanent, Admin: "Ann"},
		{Steam: "1:32", Reason: "Spam", Length: "1 hr", Date: "2026-10-29T11:30:00Z", State: sourcebans.Active},
		{Steam: "1:99", Reason: "Unknown player", Length: "Permanent", Date: "2026-10-01T10:00:00Z", State: sourcebans.Permanent},
	}
	w := &BanSync{Store: store, Set: set, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Loc: time.UTC, Now: clock.now, Sleep: clock.sleep,
		Config: BanSyncConfig{Enabled: true, Recheck: 24 * time.Hour, Pause: time.Second, SourceBans: true, SourceBansEvery: 30 * time.Minute},
		List: func(context.Context, string, time.Duration) ([]sourcebans.Ban, error) { return list, nil },
		Steam: func(_ context.Context, _ string, ids []string) (map[string]steam.PlayerBans, error) {
			out := map[string]steam.PlayerBans{}
			for _, i := range ids {
				if i == str("1:33") {
					days := int(clock.t.Truncate(24*time.Hour).Sub(banDay) / (24 * time.Hour))
					out[i] = steam.PlayerBans{SteamID: i, VACBanned: true, NumberOfVACBans: 2, DaysSinceLastBan: days, EconomyBan: "none"}
				}
			}
			return out, nil
		}}
	w.Step(ctx)

	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'steam'`); n != 3 {
		t.Errorf("%d steam rows, want 3 (1:31 with both spellings, 1:32, 1:33; no bot)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'steam' AND banned = 1 AND vac_bans = 2 AND steamid64 = ?`, id("1:33")); n != 1 {
		t.Error("the inactive player's VAC ban is missing")
	}
	wantDay := now.Truncate(24*time.Hour).Unix() - 10*86400
	if n := count(t, db, `SELECT last_ban FROM hlstats_Bans WHERE source = 'steam' AND steamid64 = ?`, id("1:33")); int64(n) != wantDay {
		t.Errorf("last_ban %d, want %d", n, wantDay)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'sourcebans'`); n != 3 {
		t.Errorf("%d SourceBans rows, want 3", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'sourcebans' AND steamid64 = ? AND sb_ends IS NULL AND sb_admin = 'Ann' AND playerId = 31`, id("1:31")); n != 1 {
		t.Error("the permanent ban of 1:31 is not stored as such")
	}
	// The players of the banned accounts: 1:31 in css and (as STEAM_1:1:31)
	// in csgo, 1:32, 1:33; none for 1:99.
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans_Players`); n != 4 {
		t.Errorf("%d player rows, want 4", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans_Players WHERE steamid64 = ? AND game = 'csgo' AND playerId = 34`, id("1:31")); n != 1 {
		t.Error("the csgo player of 1:31 is missing")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE steamid64 = ? AND playerId IS NULL`, id("1:99")); n != 1 {
		t.Error("a ban of an unknown account has a player")
	}
	// Seen again unchanged: updated_at stays, last_checked moves.
	clock.t = now.Add(25 * time.Hour)
	list = list[:2]
	list[1].Date = "2026-10-30T12:30:00Z" // 32 banned again for an hour
	w.lastSB = time.Time{}
	w.Step(ctx)
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'steam' AND updated_at = ? AND last_checked = ?`,
		now.Unix(), clock.t.Unix()); n != 3 {
		t.Errorf("%d steam rows checked again without a change, want 3", n)
	}
	// 1:99 left the list: deleted.
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE steamid64 = ?`, id("1:99")); n != 0 {
		t.Error("the ban that left the list is still there")
	}

	// 1:31 is lifted: its row goes, its record ends and the player is back
	// in the ranking.
	list = list[1:]
	clock.t = clock.t.Add(time.Minute)
	w.lastSB = time.Time{}
	w.Step(ctx)
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'sourcebans' AND steamid64 = ?`, id("1:31")); n != 0 {
		t.Error("the lifted ban is still there")
	}
	if n := count(t, db, `SELECT hideranking FROM hlstats_Players WHERE playerId = 31`); n != 0 {
		t.Errorf("hideranking of 31: %d, want 0 (back in the ranking)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE player_id = 31 AND active = 1`); n != 0 {
		t.Error("the record of the lifted ban still runs")
	}

	// The list cannot be read; 32's ban ends after an hour and goes all the
	// same; the admin's hide of 32 stays.
	w.List = func(context.Context, string, time.Duration) ([]sourcebans.Ban, error) { return nil, sourcebans.ErrUnknownTemplate }
	clock.t = time.Date(2026, 10, 30, 13, 31, 0, 0, time.UTC)
	w.lastSB = time.Time{}
	w.Step(ctx)
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans WHERE source = 'sourcebans'`); n != 0 {
		t.Errorf("%d SourceBans rows after the end, want 0", n)
	}
	if n := count(t, db, `SELECT hideranking FROM hlstats_Players WHERE playerId = 32`); n != 1 {
		t.Errorf("hideranking of 32: %d, want 1 (hidden by an admin)", n)
	}
	// Only the inactive player's VAC ban is left, with its player.
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Bans_Players`); n != 1 {
		t.Errorf("%d player rows, want 1", n)
	}
	var plan string
	if err := db.QueryRow(db.Q(`SELECT value FROM hlstats_Options WHERE keyname = ?`), BansPlanKey).Scan(&plan); err != nil || plan == "" {
		t.Errorf("plan %q %v", plan, err)
	}
}
