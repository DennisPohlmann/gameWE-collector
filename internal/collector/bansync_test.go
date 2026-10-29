// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/sourcebans"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

// fakeSyncStore keeps hlstats_Bans in maps: steam rows by account,
// SourceBans rows by account.
type fakeSyncStore struct {
	accounts []stats.BanAccount
	checked  map[uint64]int64
	steam    map[uint64]stats.SteamBan
	sb       map[uint64]stats.SourceBan
	players  map[uint64][]stats.BanPlayer
	running  []stats.PlayerBan
	ended    []int64
	options  map[string]string
	saves    int
}

func newFakeSyncStore() *fakeSyncStore {
	return &fakeSyncStore{checked: map[uint64]int64{}, steam: map[uint64]stats.SteamBan{}, sb: map[uint64]stats.SourceBan{}, options: map[string]string{}}
}

func (f *fakeSyncStore) BanAccounts(context.Context) ([]stats.BanAccount, error) { return f.accounts, nil }

func (f *fakeSyncStore) SteamChecked(context.Context) (map[uint64]int64, error) {
	return copyChecked(f.checked), nil
}

func copyChecked(m map[uint64]int64) map[uint64]int64 {
	out := map[uint64]int64{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (f *fakeSyncStore) SaveSteamBans(_ context.Context, bans []stats.SteamBan, now time.Time) error {
	f.saves++
	for _, b := range bans {
		f.steam[b.SteamID64] = b
		f.checked[b.SteamID64] = now.Unix()
	}
	return nil
}

func (f *fakeSyncStore) SaveSourceBans(_ context.Context, bans []stats.SourceBan, _ time.Time) (int, error) {
	keep := map[uint64]stats.SourceBan{}
	for _, b := range bans {
		keep[b.SteamID64] = b
	}
	gone := 0
	for id := range f.sb {
		if _, ok := keep[id]; !ok {
			gone++
		}
	}
	f.sb = keep
	return gone, nil
}

func (f *fakeSyncStore) DeleteExpiredSourceBans(_ context.Context, now time.Time) (int64, error) {
	var n int64
	for id, b := range f.sb {
		if !b.Ends.IsZero() && !b.Ends.After(now) {
			delete(f.sb, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeSyncStore) SyncBanPlayers(_ context.Context, p map[uint64][]stats.BanPlayer) (int, int, error) {
	f.players = p
	return 0, 0, nil
}

func (f *fakeSyncStore) BanCounts(context.Context) (int, int, int, error) {
	banned := 0
	for _, b := range f.steam {
		if b.Banned() {
			banned++
		}
	}
	return len(f.steam), banned, len(f.sb), nil
}

func (f *fakeSyncStore) RunningBans(context.Context, string) ([]stats.PlayerBan, error) {
	var out []stats.PlayerBan
	for _, r := range f.running {
		if !slices.Contains(f.ended, r.ID) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeSyncStore) EndBan(_ context.Context, b stats.PlayerBan, _ time.Time) (bool, error) {
	f.ended = append(f.ended, b.ID)
	return b.Hid, nil
}

func (f *fakeSyncStore) SetOption(_ context.Context, k, v string) error {
	f.options[k] = v
	return nil
}

type syncClock struct{ t time.Time }

func (c *syncClock) now() time.Time { return c.t }

func (c *syncClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.t = c.t.Add(d)
	return nil
}

func banSync(store *fakeSyncStore, clock *syncClock, key, sbURL string) *BanSync {
	set := settings.NewLive(settings.Defaults())
	s := settings.Defaults()
	s.SteamAPIKey = settings.Secret(key)
	s.SourceBansURL = sbURL
	set.Set(s)
	return &BanSync{Store: store, Set: set, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Loc: time.UTC,
		Config: BanSyncConfig{Enabled: true, Recheck: 24 * time.Hour, Pause: time.Second, SourceBans: true, SourceBansEvery: 30 * time.Minute},
		Now:    clock.now, Sleep: clock.sleep}
}

const id64Base = 76561197960265728

// account returns the SteamID64 and the "X:Y" unique id of account n.
func account(n uint64) (uint64, string) {
	return id64Base + n, strconv.FormatUint(n%2, 10) + ":" + strconv.FormatUint(n/2, 10)
}

func TestID64Spellings(t *testing.T) {
	want := uint64(76561197960265739) // 1:5
	for _, s := range []string{"1:5", "STEAM_0:1:5", "STEAM_1:1:5", "[U:1:11]", "U:1:11", "76561197960265739"} {
		if got, ok := ID64(s); !ok || got != want {
			t.Errorf("%s: %d %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "BOT", "BOT:Ann", "STEAM_ID_LAN", "1:x", "[U:1:0]"} {
		if got, ok := ID64(s); ok {
			t.Errorf("%s accepted as %d", s, got)
		}
	}
}

func TestDueAccounts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	players := map[uint64][]stats.BanPlayer{1: nil, 2: nil, 3: nil, 4: nil, 5: nil}
	checked := map[uint64]int64{
		2: now.Add(-25 * time.Hour).Unix(), // due
		3: now.Add(-time.Hour).Unix(),      // fresh answer
		4: now.Add(-48 * time.Hour).Unix(), // due, older
	}
	got := DueAccounts(players, checked, now, 24*time.Hour)
	if want := []uint64{1, 5, 4, 2}; !slices.Equal(got, want) {
		t.Errorf("due %v, want %v (never asked first, then oldest)", got, want)
	}
}

// 250 accounts: three requests (100, 100, 50) with the pause between them;
// every account asked gets a row, banned or not; a second slice asks none.
func TestBanSyncSteamBatches(t *testing.T) {
	store := newFakeSyncStore()
	for n := uint64(1); n <= 250; n++ {
		_, uid := account(n)
		store.accounts = append(store.accounts, stats.BanAccount{PlayerID: int64(n), Game: "css", UniqueID: uid})
	}
	// The same account in a second ranking and in another spelling.
	_, uid := account(7)
	store.accounts = append(store.accounts, stats.BanAccount{PlayerID: 1007, Game: "csgo", UniqueID: "STEAM_1:" + uid})
	clock := &syncClock{t: time.Unix(1_800_000_000, 0)}
	var sizes []int
	var at []time.Time
	w := banSync(store, clock, "KEY", "")
	w.Steam = func(_ context.Context, key string, ids []string) (map[string]steam.PlayerBans, error) {
		if key != "KEY" || len(ids) > steam.MaxIDs {
			t.Fatalf("request with key %q and %d ids", key, len(ids))
		}
		sizes = append(sizes, len(ids))
		at = append(at, clock.t)
		id7, _ := account(7)
		return map[string]steam.PlayerBans{strconv.FormatUint(id7, 10): {SteamID: strconv.FormatUint(id7, 10), VACBanned: true, NumberOfVACBans: 1, DaysSinceLastBan: 3}}, nil
	}
	ctx := context.Background()
	if err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Due() != 250 {
		t.Fatalf("%d due, want 250 (one per account)", w.Due())
	}
	if err := w.SteamSlice(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sizes, []int{100, 100, 50}) {
		t.Errorf("request sizes %v", sizes)
	}
	for i := 1; i < len(at); i++ {
		if d := at[i].Sub(at[i-1]); d < time.Second {
			t.Errorf("only %v between requests", d)
		}
	}
	if len(store.steam) != 250 || w.Due() != 0 {
		t.Errorf("%d rows, %d due", len(store.steam), w.Due())
	}
	id7, _ := account(7)
	if !store.steam[id7].Banned() {
		t.Error("VAC ban not stored")
	}
	if got := store.players[id7]; len(got) != 2 {
		t.Errorf("players of the account: %+v (both rankings)", got)
	}
	sizes = nil
	if err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.SteamSlice(ctx, time.Hour); err != nil || len(sizes) != 0 {
		t.Errorf("asked again within a day: %v %v", sizes, err)
	}
	clock.t = clock.t.Add(25 * time.Hour)
	if err := w.Scan(ctx); err != nil || w.Due() != 250 {
		t.Errorf("after a day %d due, %v", w.Due(), err)
	}
}

// The time limit of a slice stops between requests; the rest stays queued.
func TestBanSyncSteamSliceLimit(t *testing.T) {
	store := newFakeSyncStore()
	for n := uint64(1); n <= 1000; n++ {
		_, uid := account(n)
		store.accounts = append(store.accounts, stats.BanAccount{PlayerID: int64(n), Game: "css", UniqueID: uid})
	}
	clock := &syncClock{t: time.Unix(1_800_000_000, 0)}
	w := banSync(store, clock, "KEY", "")
	calls := 0
	w.Steam = func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
		calls++
		return nil, nil
	}
	ctx := context.Background()
	w.Scan(ctx)
	if err := w.SteamSlice(ctx, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || w.Due() != 600 {
		t.Errorf("%d calls, %d due; want 4 and 600", calls, w.Due())
	}
}

// HTTP 429 and 5xx double the pause and keep the accounts queued; success
// resets it. A refused key empties the queue until the next scan. Without
// a key nothing is asked.
func TestBanSyncSteamBackoff(t *testing.T) {
	store := newFakeSyncStore()
	for n := uint64(1); n <= 150; n++ {
		_, uid := account(n)
		store.accounts = append(store.accounts, stats.BanAccount{PlayerID: int64(n), Game: "css", UniqueID: uid})
	}
	clock := &syncClock{t: time.Unix(1_800_000_000, 0)}
	w := banSync(store, clock, "KEY", "")
	errs := []error{&steam.StatusError{Code: 429}, &steam.StatusError{Code: 503}, nil, nil}
	var at []time.Time
	w.Steam = func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
		at = append(at, clock.t)
		err := errs[0]
		errs = errs[1:]
		return nil, err
	}
	ctx := context.Background()
	w.Scan(ctx)
	if err := w.SteamSlice(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(at) != 4 || w.Due() != 0 || len(store.steam) != 150 {
		t.Fatalf("%d requests, %d due, %d rows", len(at), w.Due(), len(store.steam))
	}
	if d1, d2, d3 := at[1].Sub(at[0]), at[2].Sub(at[1]), at[3].Sub(at[2]); d1 != 2*time.Second || d2 != 4*time.Second || d3 != time.Second {
		t.Errorf("pauses %v %v %v, want 2s 4s 1s", d1, d2, d3)
	}

	// Refused key.
	store2 := newFakeSyncStore()
	store2.accounts = store.accounts
	w2 := banSync(store2, clock, "KEY", "")
	calls := 0
	w2.Steam = func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
		calls++
		return nil, steam.ErrKey
	}
	w2.Scan(ctx)
	if err := w2.SteamSlice(ctx, time.Hour); err != nil || calls != 1 || w2.Due() != 0 || len(store2.steam) != 0 {
		t.Errorf("refused key: %d calls, %d due, %v", calls, w2.Due(), err)
	}

	// Persistent failures end the slice after BanSyncFailures requests.
	w3 := banSync(newFakeSyncStore(), clock, "KEY", "")
	w3.Store.(*fakeSyncStore).accounts = store.accounts
	calls = 0
	w3.Steam = func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
		calls++
		return nil, errors.New("network down")
	}
	w3.Scan(ctx)
	if err := w3.SteamSlice(ctx, 24*time.Hour); err != nil || calls != BanSyncFailures || w3.Due() != 150 {
		t.Errorf("failures: %d calls, %d due, %v", calls, w3.Due(), err)
	}

	// No key: no request.
	w4 := banSync(newFakeSyncStore(), clock, "", "")
	w4.Store.(*fakeSyncStore).accounts = store.accounts
	w4.Steam = func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
		t.Fatal("asked without a key")
		return nil, nil
	}
	w4.Scan(ctx)
	if err := w4.SteamSlice(ctx, time.Hour); err != nil {
		t.Error(err)
	}
}

func TestRunningSourceBans(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	list := []sourcebans.Ban{
		{Steam: "1:5", Reason: "old", Length: "1 hr", Date: "01.01.2026, 10:00", State: sourcebans.Expired},
		{Steam: "1:5", Reason: "Cheating", Length: "2 wk", Date: "26.10.2026, 17:48", State: sourcebans.Active, Admin: "Ann"},
		{Steam: "0:6", Reason: "Lifted", Length: "Permanent", Date: "20.10.2026, 10:00", State: sourcebans.Unbanned},
		{Steam: "0:7", Reason: "Spam", Length: "Permanent", Date: "20.10.2026, 10:00", State: sourcebans.Permanent},
		{Steam: "0:8", Reason: "Ran out", Length: "1 d", Date: "20.10.2026, 10:00", State: sourcebans.Active}, // the list does not know yet
		{Steam: "76561197960265745", Reason: "By ID64", Length: "1 wk", Date: "2026-10-28T10:00:00Z", State: sourcebans.Active},
		{Steam: "BOT", State: sourcebans.Active},
	}
	got := RunningSourceBans(list, time.UTC, now)
	if len(got) != 3 {
		t.Fatalf("%d running: %+v", len(got), got)
	}
	cheat := got[0]
	if id, _ := ID64("1:5"); cheat.SteamID64 != id || cheat.Reason != "Cheating" || cheat.Admin != "Ann" || cheat.LengthMinutes != 20160 ||
		!cheat.Ends.Equal(time.Date(2026, 11, 9, 17, 48, 0, 0, time.UTC)) {
		t.Errorf("running ban %+v", cheat)
	}
	if perm := got[1]; perm.Reason != "Spam" || !perm.Ends.IsZero() || perm.LengthMinutes != 0 {
		t.Errorf("permanent ban %+v", perm)
	}
	if got[2].SteamID64 != 76561197960265745 || got[2].Created.IsZero() {
		t.Errorf("ban by SteamID64 %+v", got[2])
	}
}

// A completely read list replaces the SourceBans rows (lifted bans go);
// a failed read changes nothing. A player's record whose ban left the list
// ends (back in the ranking when the record took the player out).
func TestBanSyncSourceBans(t *testing.T) {
	store := newFakeSyncStore()
	id5, _ := ID64("1:5")
	id6, _ := ID64("0:6")
	store.sb[id6] = stats.SourceBan{SteamID64: id6, Reason: "was banned"}
	store.running = []stats.PlayerBan{
		{ID: 1, PlayerID: 10, Game: "css", UniqueID: "1:5", Hid: true},
		{ID: 2, PlayerID: 11, Game: "css", UniqueID: "0:6", Hid: true},
	}
	clock := &syncClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	w := banSync(store, clock, "", "bans.example.org")
	var listErr error
	w.List = func(_ context.Context, base string, _ time.Duration) ([]sourcebans.Ban, error) {
		if base != "https://bans.example.org/" {
			t.Errorf("base %q", base)
		}
		if listErr != nil {
			return nil, listErr
		}
		return []sourcebans.Ban{
			{Steam: "1:5", Reason: "Cheating", Length: "Permanent", Date: "26.10.2026, 17:48", State: sourcebans.Permanent},
			{Steam: "0:6", Reason: "was banned", Length: "Permanent", Date: "20.10.2026, 10:00", State: sourcebans.Unbanned},
		}, nil
	}
	ctx := context.Background()
	listErr = errors.New("HTTP 500")
	if err := w.SyncSourceBans(ctx); err == nil {
		t.Error("failed list without error")
	}
	if _, ok := store.sb[id6]; !ok || len(store.ended) != 0 {
		t.Fatalf("failed list changed rows: %+v ended %v", store.sb, store.ended)
	}
	listErr = nil
	if err := w.SyncSourceBans(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.sb[id5]; !ok || len(store.sb) != 1 {
		t.Errorf("rows %+v: want only the running ban of 1:5", store.sb)
	}
	if !slices.Equal(store.ended, []int64{2}) {
		t.Errorf("ended records %v, want the unbanned player's", store.ended)
	}
	var plan BansPlan
	w.writePlan(ctx)
	if err := json.Unmarshal([]byte(store.options[BansPlanKey]), &plan); err != nil || !plan.SourceBans.Enabled || plan.SourceBans.Bans != 1 ||
		plan.SourceBans.Last != clock.t.Unix() || plan.SourceBans.Error != "" || plan.Steam.Enabled {
		t.Errorf("plan %+v %v", plan, err)
	}
}

// Expired bans go every tick, also while the list cannot be read, and the
// expired record of a player ends.
func TestBanSyncExpiry(t *testing.T) {
	store := newFakeSyncStore()
	clock := &syncClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	idA, _ := ID64("1:5")
	idB, _ := ID64("0:6")
	store.sb[idA] = stats.SourceBan{SteamID64: idA, Ends: clock.t.Add(30 * time.Second)}
	store.sb[idB] = stats.SourceBan{SteamID64: idB} // permanent
	store.running = []stats.PlayerBan{{ID: 1, PlayerID: 10, Game: "css", UniqueID: "1:5", EndsAt: clock.t.Add(30 * time.Second).Unix(), Hid: true}}
	w := banSync(store, clock, "", "bans.example.org")
	w.List = func(context.Context, string, time.Duration) ([]sourcebans.Ban, error) { return nil, errors.New("unreachable") }
	ctx := context.Background()
	w.Step(ctx)
	if len(store.sb) != 2 || len(store.ended) != 0 {
		t.Fatalf("too early: %+v %v", store.sb, store.ended)
	}
	clock.t = clock.t.Add(BanSyncTick)
	w.Step(ctx)
	if _, ok := store.sb[idA]; ok || len(store.sb) != 1 || !slices.Equal(store.ended, []int64{1}) {
		t.Errorf("after the end: %+v ended %v", store.sb, store.ended)
	}
}

// Switched off, the job only writes that to the plan.
func TestBanSyncOff(t *testing.T) {
	store := newFakeSyncStore()
	clock := &syncClock{t: time.Unix(1_800_000_000, 0)}
	w := banSync(store, clock, "KEY", "bans.example.org")
	w.Config.Enabled = false
	w.Steam = func(context.Context, string, []string) (map[string]steam.PlayerBans, error) {
		t.Fatal("asked while off")
		return nil, nil
	}
	w.Run(context.Background())
	var plan BansPlan
	if err := json.Unmarshal([]byte(store.options[BansPlanKey]), &plan); err != nil || plan.Enabled || plan.Steam.Enabled || plan.SourceBans.Enabled {
		t.Errorf("plan %+v %v", plan, err)
	}
}
