// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/sourcebans"
	"gamewe-collector/internal/stats"
)

type fakeBanStore struct {
	mu       sync.Mutex
	recorded []stats.Ban
	running  []stats.PlayerBan
	checked  []int64
	ended    []int64
}

func (f *fakeBanStore) RecordBan(_ context.Context, game, uid string, b stats.Ban, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, b)
	return 1, nil
}

func (f *fakeBanStore) RunningBans(context.Context, string) ([]stats.PlayerBan, error) {
	return f.running, nil
}

func (f *fakeBanStore) CheckedBan(_ context.Context, id int64, _ time.Time) error {
	f.checked = append(f.checked, id)
	return nil
}

func (f *fakeBanStore) EndBan(_ context.Context, b stats.PlayerBan, _ time.Time) (bool, error) {
	f.ended = append(f.ended, b.ID)
	return b.Hid, nil
}

func banWatch(t *testing.T, store BanStore, lookup BanLookup, now time.Time) *BanWatch {
	t.Helper()
	set := settings.NewLive(settings.Defaults())
	s := settings.Defaults()
	s.SourceBansURL = "bans.example.org"
	set.Set(s)
	return &BanWatch{Store: store, Set: set, Lookup: lookup, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Loc: time.UTC, Interval: time.Hour, Now: func() time.Time { return now }}
}

func TestBanWatchNotice(t *testing.T) {
	now := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	var asked []string
	lookup := func(_ context.Context, base, uid string) ([]sourcebans.Ban, error) {
		asked = append(asked, base+" "+uid)
		return []sourcebans.Ban{
			{Steam: "1:5", Reason: "old", Length: "1 hr", Date: "01.01.2026, 10:00", State: sourcebans.Expired},
			{Steam: "1:5", Reason: "Cheating", Length: "2 wk", Date: "26.10.2026, 17:48", State: sourcebans.Active},
		}, nil
	}
	store := &fakeBanStore{}
	w := banWatch(t, store, lookup, now)
	w.Notice("css1", "1:5", "Disconnect by user.")                                                           // not a ban
	w.Notice("css1", "BOT", "You have been banned from this server. Access bans.example.org for more info.") // no Steam ID
	w.Notice("css1", "1:5", "Sie wurden von diesem Server gebannt, weitere Informationen auf bans.example.org.")
	w.Notice("css1", "1:5", "You have been banned from this server. Access bans.example.org for more info.")
	if len(w.queue) != 2 {
		t.Fatalf("%d queued, want 2", len(w.queue))
	}
	w.handle(context.Background(), <-w.queue)
	w.handle(context.Background(), <-w.queue) // the same player again: no second lookup
	if len(asked) != 1 || asked[0] != "https://bans.example.org/ 1:5" {
		t.Fatalf("lookups %v", asked)
	}
	if len(store.recorded) != 1 {
		t.Fatalf("recorded %+v", store.recorded)
	}
	b := store.recorded[0]
	if b.Reason != "Cheating" || b.LengthMinutes != 20160 || !b.BannedAt.Equal(time.Date(2026, 10, 26, 17, 48, 0, 0, time.UTC)) ||
		!b.EndsAt.Equal(b.BannedAt.Add(14*24*time.Hour)) || b.Type != stats.BanTypeSourceBans {
		t.Errorf("ban %+v", b)
	}
}

func TestBanWatchNoAddress(t *testing.T) {
	w := banWatch(t, &fakeBanStore{}, nil, time.Now())
	w.Set.Set(settings.Defaults())
	w.Notice("css1", "1:5", "You have been banned from this server. Access bans.example.org for more info.")
	if w.queue != nil && len(w.queue) != 0 {
		t.Error("queued without an address")
	}
}

func TestBanWatchQueueBounded(t *testing.T) {
	w := banWatch(t, &fakeBanStore{}, nil, time.Now())
	for i := 0; i < BanQueue+50; i++ {
		w.Notice("css1", "1:5", "banned, see bans.example.org")
	}
	if len(w.queue) != BanQueue {
		t.Errorf("queue %d, want %d", len(w.queue), BanQueue)
	}
}

func TestBanWatchCheck(t *testing.T) {
	now := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	pauseSave := banCheckPause
	banCheckPause = 0
	defer func() { banCheckPause = pauseSave }()
	store := &fakeBanStore{running: []stats.PlayerBan{
		{ID: 1, UniqueID: "1:1", EndsAt: now.Add(-time.Minute).Unix(), Hid: true}, // ran out
		{ID: 2, UniqueID: "1:2", Hid: true},                                       // still on the list
		{ID: 3, UniqueID: "1:3", Hid: true},                                       // lifted
		{ID: 4, UniqueID: "1:4", Hid: true},                                       // list unreachable
		{ID: 5, UniqueID: "1:2", Hid: false},                                      // same Steam ID, other game
	}}
	n := 0
	lookup := func(_ context.Context, _, uid string) ([]sourcebans.Ban, error) {
		n++
		switch uid {
		case "1:2":
			return []sourcebans.Ban{{Steam: uid, Length: "Permanent", State: sourcebans.Permanent}}, nil
		case "1:3":
			return []sourcebans.Ban{{Steam: uid, Length: "1 d", State: sourcebans.Unbanned}}, nil
		}
		return nil, errors.New("timeout")
	}
	w := banWatch(t, store, lookup, now)
	if err := w.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("%d lookups, want 3 (one per Steam ID, none for the expired ban)", n)
	}
	if want := []int64{1, 3}; len(store.ended) != 2 || store.ended[0] != want[0] || store.ended[1] != want[1] {
		t.Errorf("ended %v, want %v", store.ended, want)
	}
	if len(store.checked) != 2 || store.checked[0] != 2 || store.checked[1] != 5 {
		t.Errorf("checked %v, want [2 5]", store.checked)
	}
}

func TestBanWatchRunningIgnoresRunOut(t *testing.T) {
	now := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	w := banWatch(t, &fakeBanStore{}, nil, now)
	// The list still says "banned", but the hour is over.
	if _, ok := w.running([]sourcebans.Ban{{Length: "1 hr", Date: "26.10.2026, 10:00", State: sourcebans.Active}}, now); ok {
		t.Error("run out ban counted as running")
	}
	if b, ok := w.running([]sourcebans.Ban{{Length: "Permanent", Date: "?", State: sourcebans.Permanent}}, now); !ok || !b.BannedAt.IsZero() || !b.EndsAt.IsZero() {
		t.Errorf("permanent ban without a date: %+v %v", b, ok)
	}
}

type noticeRecorder struct{ got []string }

func (n *noticeRecorder) Notice(game, uniqueID, reason string) {
	n.got = append(n.got, game+" "+uniqueID+" "+reason)
}

// Disconnects with a reason go to the ban watcher, not those of bots, VAC
// kicks (hidden at once) or without a reason.
func TestProcessorNoticesBanKicks(t *testing.T) {
	p, _ := newTestProcessor(t)
	rec := &noticeRecorder{}
	p.Bans = rec
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"A<2><STEAM_1:1:5><CT>" disconnected (reason "You have been banned from this server. Access bans.example.org for more info.")`,
		`"B<3><STEAM_1:0:6><CT>" disconnected`,
		`"C<4><BOT><CT>" disconnected (reason "Kicked by Console")`,
		`"D<5><STEAM_1:0:7><CT>" disconnected (reason "VAC banned from secure server")`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.got) != 1 || !strings.HasSuffix(rec.got[0], "1:5 You have been banned from this server. Access bans.example.org for more info.") {
		t.Errorf("notices %q", rec.got)
	}
}
