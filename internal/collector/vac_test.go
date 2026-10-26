// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

const testSteamKey = "0123456789ABCDEF0123456789ABCDEF" // not a real key

type fakeVACStore struct {
	fakeBanStore
	held map[string]bool // game|unique id with a running vac record
	uids []string        // recorded unique ids
}

func (f *fakeVACStore) HasRunningBan(_ context.Context, game, uid, typ string) (bool, error) {
	return typ == stats.BanTypeVAC && f.held[game+"|"+uid], nil
}

func (f *fakeVACStore) RecordBan(ctx context.Context, game, uid string, b stats.Ban, now time.Time) (int, error) {
	f.mu.Lock()
	f.uids = append(f.uids, game+"|"+uid)
	f.mu.Unlock()
	return f.fakeBanStore.RecordBan(ctx, game, uid, b, now)
}

type vacTest struct {
	w     *VACBlock
	store *fakeVACStore
	reply *recorder
	asked [][]string
	log   *bytes.Buffer
	ans   map[string]steam.PlayerBans
	err   error
	now   time.Time
}

func newVACTest(t *testing.T, key string) *vacTest {
	t.Helper()
	vt := &vacTest{store: &fakeVACStore{held: map[string]bool{}},
		reply: &recorder{}, log: &bytes.Buffer{}, ans: map[string]steam.PlayerBans{},
		now: time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)}
	s := settings.Defaults()
	s.SteamAPIKey = settings.Secret(key)
	s.VACBlocked = map[string]bool{"css": true, "tf": false}
	var mu sync.Mutex
	lookup := func(_ context.Context, k string, ids []string) (map[string]steam.PlayerBans, error) {
		mu.Lock()
		defer mu.Unlock()
		if k != key {
			t.Errorf("lookup with key %q", k)
		}
		vt.asked = append(vt.asked, ids)
		if vt.err != nil {
			return nil, vt.err
		}
		out := map[string]steam.PlayerBans{}
		for _, id := range ids {
			if b, ok := vt.ans[id]; ok {
				out[id] = b
			}
		}
		return out, nil
	}
	vt.w = &VACBlock{Store: vt.store, Set: settings.NewLive(s), Lookup: lookup, Reply: vt.reply,
		Log: slog.New(slog.NewTextHandler(vt.log, nil)), Now: func() time.Time { return vt.now }}
	vt.w.init()
	vt.w.refresh(context.Background())
	return vt
}

var vacSrv = stats.Server{Address: "192.0.2.1:27015", Game: "css", Engine: games.Source, RconPassword: "pw"}

// drain handles the queued connects.
func (vt *vacTest) drain() {
	for len(vt.w.queue) > 0 {
		vt.w.handle(context.Background(), <-vt.w.queue)
	}
}

// 1:5 is STEAM_0:1:5, SteamID64 76561197960265739.
const vacID64 = "76561197960265739"

func TestVACBlockKicksAndRecords(t *testing.T) {
	vt := newVACTest(t, testSteamKey)
	vt.ans[vacID64] = steam.PlayerBans{SteamID: vacID64, VACBanned: true, NumberOfVACBans: 2}
	vt.w.Connected(vacSrv, 7, "1:5")
	vt.w.Connected(vacSrv, 8, "BOT:Ann")      // bot
	vt.w.Connected(vacSrv, 9, "STEAM_ID_LAN") // no account
	other := vacSrv
	other.Game = "tf" // switched off: not even queued
	vt.w.Connected(other, 10, "1:5")
	if len(vt.w.queue) != 1 {
		t.Fatalf("%d queued, want 1", len(vt.w.queue))
	}
	vt.drain()
	if len(vt.asked) != 1 || len(vt.asked[0]) != 1 || vt.asked[0][0] != vacID64 {
		t.Fatalf("asked %v", vt.asked)
	}
	if len(vt.store.recorded) != 1 || vt.store.recorded[0].Type != stats.BanTypeVAC || vt.store.recorded[0].Reason != "VAC ban on record (2)" ||
		vt.store.uids[0] != "css|1:5" {
		t.Errorf("recorded %+v %v", vt.store.recorded, vt.store.uids)
	}
	if got := vt.reply.get(vacSrv.Address); len(got) != 1 || got[0] != `kickid 7 "VAC ban on record"` {
		t.Errorf("sent %q", got)
	}
	// Reconnect: from the cache, no second request.
	vt.w.Connected(vacSrv, 11, "1:5")
	vt.drain()
	if len(vt.asked) != 1 || len(vt.reply.get(vacSrv.Address)) != 2 {
		t.Errorf("reconnect: asked %v, sent %q", vt.asked, vt.reply.get(vacSrv.Address))
	}
	// After a day the API is asked again.
	vt.now = vt.now.Add(VACCacheFor)
	vt.w.Connected(vacSrv, 12, "1:5")
	vt.drain()
	if len(vt.asked) != 2 {
		t.Errorf("after a day: asked %v", vt.asked)
	}
	if strings.Contains(vt.log.String(), testSteamKey) {
		t.Error("key in the log")
	}
}

func TestVACBlockCleanPlayerCached(t *testing.T) {
	vt := newVACTest(t, testSteamKey)
	vt.ans[vacID64] = steam.PlayerBans{SteamID: vacID64, NumberOfGameBans: 1} // game ban only: not blocked
	vt.w.Connected(vacSrv, 7, "1:5")
	vt.w.Connected(vacSrv, 8, "STEAM_1:1:5")
	vt.drain()
	if len(vt.asked) != 1 || len(vt.store.recorded) != 0 || len(vt.reply.get(vacSrv.Address)) != 0 {
		t.Errorf("asked %v, recorded %+v, sent %q", vt.asked, vt.store.recorded, vt.reply.get(vacSrv.Address))
	}
}

// Without a key or with a failing API nobody is blocked, nothing cached.
func TestVACBlockFailOpen(t *testing.T) {
	vt := newVACTest(t, "")
	vt.w.Connected(vacSrv, 7, "1:5")
	vt.drain()
	if len(vt.asked) != 0 || len(vt.reply.get(vacSrv.Address)) != 0 || !strings.Contains(vt.log.String(), "no valid Steam Web API key") {
		t.Errorf("no key: asked %v, log %s", vt.asked, vt.log)
	}
	vt = newVACTest(t, testSteamKey)
	vt.err = errors.New("steam web api: HTTP 503")
	vt.ans[vacID64] = steam.PlayerBans{SteamID: vacID64, VACBanned: true}
	vt.w.Connected(vacSrv, 7, "1:5")
	vt.drain()
	if len(vt.store.recorded) != 0 || len(vt.reply.get(vacSrv.Address)) != 0 {
		t.Errorf("API error blocked: %+v %q", vt.store.recorded, vt.reply.get(vacSrv.Address))
	}
	vt.err = nil // not cached: the next connect asks again
	vt.w.Connected(vacSrv, 8, "1:5")
	vt.drain()
	if len(vt.asked) != 2 || len(vt.store.recorded) != 1 {
		t.Errorf("after the error: asked %v, recorded %+v", vt.asked, vt.store.recorded)
	}
}

// A recorded player (the collector restarted) is kicked without a request.
func TestVACBlockRecordedKickedAgain(t *testing.T) {
	vt := newVACTest(t, testSteamKey)
	vt.store.held["css|1:5"] = true
	vt.w.Connected(vacSrv, 7, "1:5")
	vt.drain()
	if len(vt.asked) != 0 || len(vt.reply.get(vacSrv.Address)) != 1 {
		t.Errorf("asked %v, sent %q", vt.asked, vt.reply.get(vacSrv.Address))
	}
}

func TestVACBlockNoRconPassword(t *testing.T) {
	vt := newVACTest(t, testSteamKey)
	vt.ans[vacID64] = steam.PlayerBans{SteamID: vacID64, NumberOfVACBans: 1}
	srv := vacSrv
	srv.RconPassword = ""
	vt.w.Connected(srv, 7, "1:5")
	vt.drain()
	if len(vt.store.recorded) != 1 || len(vt.reply.get(srv.Address)) != 0 {
		t.Errorf("recorded %+v, sent %q", vt.store.recorded, vt.reply.get(srv.Address))
	}
}

func TestKickCommand(t *testing.T) {
	if c := KickCommand(games.GoldSrc, 3, `bad"; quit`); c != `kick #3 "bad', quit"` {
		t.Errorf("goldsrc %q", c)
	}
	if c := KickCommand(games.Source2, 4, "x\ny"); c != `kickid 4 "xy"` {
		t.Errorf("source2 %q", c)
	}
}

// Switching a ranking off ends its records.
func TestVACBlockSwitchedOff(t *testing.T) {
	vt := newVACTest(t, testSteamKey)
	vt.store.running = []stats.PlayerBan{{ID: 1, Game: "css", UniqueID: "1:5", Hid: true}, {ID: 2, Game: "tf", UniqueID: "1:6", Hid: true}}
	vt.w.games = nil
	vt.w.refresh(context.Background()) // start: tf has no switch
	if len(vt.store.ended) != 1 || vt.store.ended[0] != 2 {
		t.Fatalf("ended %v, want [2]", vt.store.ended)
	}
	vt.w.refresh(context.Background()) // nothing switched off: nothing ends
	if len(vt.store.ended) != 1 {
		t.Fatalf("ended %v without a change", vt.store.ended)
	}
	s := vt.w.Set.Get()
	s.VACBlocked = map[string]bool{"css": false}
	vt.w.Set.Set(s)
	vt.w.refresh(context.Background())
	if len(vt.store.ended) != 3 || vt.w.on("css") {
		t.Errorf("ended %v after switching css off", vt.store.ended)
	}
	vt.w.Connected(vacSrv, 7, "1:5")
	if len(vt.w.queue) != 0 || len(vt.asked) != 0 {
		t.Error("switched off: connect checked")
	}
}

func TestVACBlockRecheck(t *testing.T) {
	vt := newVACTest(t, testSteamKey)
	vt.store.running = []stats.PlayerBan{
		{ID: 1, Game: "css", UniqueID: "1:5", Hid: true}, // still banned
		{ID: 2, Game: "css", UniqueID: "0:6", Hid: true}, // Steam removed the ban
		{ID: 3, Game: "css", UniqueID: "0:7", Hid: true}, // unknown to the answer: unchanged
	}
	vt.ans[vacID64] = steam.PlayerBans{SteamID: vacID64, VACBanned: true, NumberOfVACBans: 1}
	vt.ans["76561197960265740"] = steam.PlayerBans{SteamID: "76561197960265740"}
	if err := vt.w.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(vt.asked) != 1 || len(vt.asked[0]) != 3 {
		t.Errorf("asked %v", vt.asked)
	}
	if len(vt.store.ended) != 1 || vt.store.ended[0] != 2 || len(vt.store.checked) != 2 {
		t.Errorf("ended %v checked %v", vt.store.ended, vt.store.checked)
	}
	// API down: nothing ends.
	vt.err = errors.New("timeout")
	vt.store.ended, vt.store.checked = nil, nil
	if err := vt.w.Recheck(context.Background()); err != nil || len(vt.store.ended) != 0 || len(vt.store.checked) != 0 {
		t.Errorf("API down: %v ended %v checked %v", err, vt.store.ended, vt.store.checked)
	}
}

type vacRecorder struct{ got []string }

func (v *vacRecorder) Connected(srv stats.Server, userID int, uid string) {
	v.got = append(v.got, srv.Game+" "+uid)
}

// Connects go to the VAC block (bots not); the player is registered first.
func TestProcessorTellsVACConnects(t *testing.T) {
	p, _ := newTestProcessor(t)
	rec := &vacRecorder{}
	p.VAC = rec
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"A<2><STEAM_1:1:5><>" connected, address "192.0.2.9:27005"`,
		`"Bot<3><BOT><>" connected, address "none"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.got) != 1 || !strings.HasSuffix(rec.got[0], " 1:5") {
		t.Errorf("connects %q", rec.got)
	}
}
