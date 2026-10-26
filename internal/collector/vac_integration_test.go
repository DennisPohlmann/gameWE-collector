// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

// gamewe_block_vac_<game>: a connecting player with a VAC ban on record is
// registered, recorded (type vac, out of the ranking) and kicked; after a
// restart the record kicks without asking Steam; switching the ranking off
// ends the record and puts the player back. The Steam Web API is mocked.
func TestIntegrationVACBlock(t *testing.T) {
	db := testDB(t)
	// The switch and the key as the web admin stores them (not a real key).
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_block_vac_css', '1'),
		('gamewe_block_vac_tf', '0'), ('gamewe_steam_api_key', '0123456789ABCDEF0123456789ABCDEF')`)); err != nil {
		t.Fatal(err)
	}
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	ctx := context.Background()
	load := func() {
		t.Helper()
		opts, err := store.Options(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := settings.FromOptions(opts)
		set.Set(s)
	}
	load()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	asked := 0
	lookup := func(_ context.Context, _ string, ids []string) (map[string]steam.PlayerBans, error) {
		asked++
		out := map[string]steam.PlayerBans{}
		for _, id := range ids {
			if id == "76561197960265739" { // 1:5
				out[id] = steam.PlayerBans{SteamID: id, VACBanned: true, NumberOfVACBans: 1}
			} else {
				out[id] = steam.PlayerBans{SteamID: id}
			}
		}
		return out, nil
	}
	rec := &recorder{}
	srv := stats.Server{ID: 1, Address: "198.51.100.1:27015", Game: "css", GameID: 1, Base: "css", Engine: games.Source, RconPassword: "pw"}
	w := &VACBlock{Store: store, Set: set, Lookup: lookup, Reply: rec, Log: log}
	w.init()
	w.refresh(ctx)
	if !w.on("css") || w.on("tf") {
		t.Fatalf("switches %v", w.games)
	}
	p := NewProcessor(store, set, srv, rec, log)
	p.VAC = w
	now := time.Now()
	for _, l := range []string{
		`"Cheater<7><STEAM_1:1:5><>" connected, address "192.0.2.9:27005"`,
		`"Clean<8><STEAM_1:0:6><>" connected, address "192.0.2.10:27005"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	for len(w.queue) > 0 {
		w.handle(ctx, <-w.queue)
	}
	hide := func(uid string) int {
		t.Helper()
		return count(t, db, `SELECT p.hideranking FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId
			WHERE u.uniqueId = ? AND u.game = 'css'`, uid)
	}
	if hide("1:5") != 2 || hide("0:6") != 0 {
		t.Errorf("hideranking %d %d, want 2 0", hide("1:5"), hide("0:6"))
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE unique_id = '1:5' AND game = 'css' AND ban_type = 'vac'
		AND active = 1 AND hid = 1 AND reason = 'VAC ban on record (1)'`); n != 1 {
		t.Errorf("%d vac records, want 1", n)
	}
	if got := rec.get(srv.Address); len(got) != 1 || got[0] != `kickid 7 "VAC ban on record"` {
		t.Errorf("sent %q", got)
	}
	// Restart: no cache, Steam unreachable; the record still kicks.
	w2 := &VACBlock{Store: store, Set: set, Reply: rec, Log: log,
		Lookup: func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
			return nil, errors.New("unreachable")
		}}
	w2.init()
	w2.refresh(ctx)
	w2.Connected(srv, 9, "1:5")
	w2.handle(ctx, <-w2.queue)
	if got := rec.get(srv.Address); len(got) != 2 || got[1] != `kickid 9 "VAC ban on record"` {
		t.Errorf("after the restart: sent %q", got)
	}
	// Switched off: the record ends, the player is back.
	if _, err := db.Exec(db.Q(`UPDATE hlstats_Options SET value = '0' WHERE keyname = 'gamewe_block_vac_css'`)); err != nil {
		t.Fatal(err)
	}
	load()
	w2.refresh(ctx)
	if hide("1:5") != 0 || count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE ban_type = 'vac' AND active = 1`) != 0 {
		t.Errorf("switched off: hideranking %d", hide("1:5"))
	}
	if held, err := store.HasRunningBan(ctx, "css", "1:5", stats.BanTypeVAC); err != nil || held {
		t.Errorf("running after the end: %v %v", held, err)
	}
	if asked != 2 {
		t.Errorf("Steam asked %d times, want 2 (one per new player)", asked)
	}
}
