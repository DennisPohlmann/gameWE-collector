// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/sourcebans"
	"gamewe-collector/internal/stats"
)

// BanStore is what the ban watcher needs of the database (*stats.Store).
type BanStore interface {
	RecordBan(ctx context.Context, game, uniqueID string, b stats.Ban, now time.Time) (int, error)
	RunningBans(ctx context.Context, banType string) ([]stats.PlayerBan, error)
	CheckedBan(ctx context.Context, id int64, now time.Time) error
	EndBan(ctx context.Context, b stats.PlayerBan, now time.Time) (bool, error)
}

// BanLookup returns the bans the ban list shows for a Steam ID
// (sourcebans.Client.Lookup).
type BanLookup func(ctx context.Context, base, uniqueID string) ([]sourcebans.Ban, error)

// BanNotifier is told about disconnects with a reason (Processor.Bans).
type BanNotifier interface {
	Notice(game, uniqueID, reason string)
}

const (
	// BanQueue is how many noticed kicks wait for their lookup; more are
	// dropped (a flood of fake disconnect lines cannot pile up).
	BanQueue = 256
	// BanLookupEvery is the shortest time between two lookups of the same
	// player (a banned player retrying to join).
	BanLookupEvery = 5 * time.Minute
	// BanSeenMax caps the remembered lookups.
	BanSeenMax = 10000
)

// banCheckPause is the pause between the lookups of the check of the
// running bans, so the ban list is not hammered (variable for tests).
var banCheckPause = time.Second

// BanWatch records SourceBans bans (features.sourcebans): a player kicked
// with a reason that names the ban list (SourceBans' kick message, in any
// language) or one of Phrases is looked up on the list
// (hlstats_Options gamewe_sourcebans_url); a running ban is stored in
// hlstats_Players_Bans and takes the player out of the ranking. Every
// Interval the running bans are checked again: an expired or lifted ban
// ends and the player is back in the ranking. Without an address it does
// nothing.
type BanWatch struct {
	Store    BanStore
	Set      *settings.Live
	Lookup   BanLookup
	Log      *slog.Logger
	Loc      *time.Location // time zone of the dates on the ban list
	Phrases  []string
	Interval time.Duration
	Now      func() time.Time // tests

	once  sync.Once
	queue chan banItem
	mu    sync.Mutex
	seen  map[string]time.Time // game|unique id -> last lookup
}

type banItem struct{ game, uniqueID string }

func (w *BanWatch) init() {
	w.once.Do(func() {
		w.queue = make(chan banItem, BanQueue)
		w.seen = map[string]time.Time{}
	})
}

func (w *BanWatch) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// base is the ban list address of the setting ("" when none).
func (w *BanWatch) base() string { return sourcebans.Base(w.Set.Get().SourceBansURL) }

// Notice is called by the processors for every disconnect with a reason;
// it never blocks.
func (w *BanWatch) Notice(game, uniqueID, reason string) {
	if reason == "" {
		return
	}
	base := w.base()
	if base == "" || !sourcebans.Matches(reason, sourcebans.Host(base), w.Phrases) {
		return
	}
	if _, ok := sourcebans.Account(uniqueID); !ok {
		return
	}
	w.init()
	select {
	case w.queue <- banItem{game, uniqueID}:
	default:
		w.Log.Warn("sourcebans: lookup queue full, kick not checked", "game", game)
	}
}

// Run looks the noticed kicks up and checks the running bans every
// Interval (the first time one Interval after the start) until ctx ends.
func (w *BanWatch) Run(ctx context.Context) {
	w.init()
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-w.queue:
			w.handle(ctx, it)
		case <-t.C:
			if err := w.Check(ctx); err != nil && ctx.Err() == nil {
				w.Log.Error("sourcebans: checking the running bans failed", "err", err)
			}
		}
	}
}

// due reports whether the player may be looked up now and remembers it.
func (w *BanWatch) due(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.seen[key]; ok && now.Sub(last) < BanLookupEvery {
		return false
	}
	if len(w.seen) >= BanSeenMax {
		for k, t := range w.seen {
			if now.Sub(t) >= BanLookupEvery {
				delete(w.seen, k)
			}
		}
		if len(w.seen) >= BanSeenMax {
			return false
		}
	}
	w.seen[key] = now
	return true
}

func (w *BanWatch) handle(ctx context.Context, it banItem) {
	now := w.now()
	if !w.due(it.game+"|"+it.uniqueID, now) {
		return
	}
	base := w.base()
	if base == "" {
		return
	}
	bans, err := w.Lookup(ctx, base, it.uniqueID)
	if err != nil {
		w.Log.Warn("sourcebans: lookup failed", "game", it.game, "err", err)
		return
	}
	b, ok := w.running(bans, now)
	if !ok {
		w.Log.Info("sourcebans: kicked player has no running ban on the list", "game", it.game, "unique_id", it.uniqueID)
		return
	}
	hidden, err := w.Store.RecordBan(ctx, it.game, it.uniqueID, b, now)
	if err != nil {
		w.Log.Error("sourcebans: storing the ban failed", "game", it.game, "err", err)
		return
	}
	w.Log.Info("sourcebans: ban recorded", "game", it.game, "unique_id", it.uniqueID, "length_minutes", b.LengthMinutes,
		"taken_out_of_ranking", hidden)
}

// running picks the newest running ban of the list and converts it.
func (w *BanWatch) running(bans []sourcebans.Ban, now time.Time) (stats.Ban, bool) {
	var best stats.Ban
	found := false
	for _, b := range bans {
		if !b.State.Running() {
			continue
		}
		loc := w.Loc
		if loc == nil {
			loc = time.Local
		}
		sb := stats.Ban{Type: stats.BanTypeSourceBans, Reason: b.Reason}
		if t, ok := sourcebans.ParseDate(b.Date, loc); ok && !t.After(now.Add(time.Hour)) {
			sb.BannedAt = t
		}
		if m, ok := sourcebans.LengthMinutes(b.Length); ok && m > 0 {
			sb.LengthMinutes = m
			if !sb.BannedAt.IsZero() {
				sb.EndsAt = sb.BannedAt.Add(time.Duration(m) * time.Minute)
				if !sb.EndsAt.After(now) {
					continue // ran out, the list does not know yet
				}
			}
		}
		if !found || sb.BannedAt.After(best.BannedAt) {
			best, found = sb, true
		}
	}
	return best, found
}

// Check goes through the running bans: an end time that has passed ends the
// ban; the others are looked up again (one Steam ID per banCheckPause), and
// a ban the list no longer shows as running (expired, lifted, deleted) ends
// too. When the list cannot be read nothing changes.
func (w *BanWatch) Check(ctx context.Context) error {
	running, err := w.Store.RunningBans(ctx, stats.BanTypeSourceBans)
	if err != nil {
		return err
	}
	base := w.base()
	looked := map[string]*[]sourcebans.Ban{} // unique id -> bans, nil: failed
	for i, b := range running {
		now := w.now()
		if b.EndsAt > 0 && b.EndsAt <= now.Unix() {
			w.end(ctx, b, now, "expired")
			continue
		}
		if base == "" {
			continue // no list: only end times are checked
		}
		res, ok := looked[b.UniqueID]
		if !ok {
			if i > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(banCheckPause):
				}
			}
			bans, err := w.Lookup(ctx, base, b.UniqueID)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				w.Log.Warn("sourcebans: lookup of a running ban failed, unchanged", "game", b.Game, "err", err)
			} else {
				res = &bans
			}
			looked[b.UniqueID] = res
		}
		if res == nil {
			continue
		}
		if _, still := w.running(*res, now); still {
			if err := w.Store.CheckedBan(ctx, b.ID, now); err != nil {
				return err
			}
			continue
		}
		w.end(ctx, b, now, "lifted or expired on the ban list")
	}
	return nil
}

func (w *BanWatch) end(ctx context.Context, b stats.PlayerBan, now time.Time, why string) {
	back, err := w.Store.EndBan(ctx, b, now)
	if err != nil {
		w.Log.Error("sourcebans: ending a ban failed", "game", b.Game, "err", err)
		return
	}
	w.Log.Info("sourcebans: ban ended", "game", b.Game, "unique_id", b.UniqueID, "why", why, "back_in_ranking", back)
}
