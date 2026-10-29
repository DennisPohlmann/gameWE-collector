// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/sourcebans"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

// BanSyncStore is what the bans-sync job needs of the database
// (*stats.Store).
type BanSyncStore interface {
	BanAccounts(ctx context.Context) ([]stats.BanAccount, error)
	SteamChecked(ctx context.Context) (map[uint64]int64, error)
	SaveSteamBans(ctx context.Context, bans []stats.SteamBan, now time.Time) error
	SaveSourceBans(ctx context.Context, bans []stats.SourceBan, now time.Time) (int, error)
	DeleteExpiredSourceBans(ctx context.Context, now time.Time) (int64, error)
	SyncBanPlayers(ctx context.Context, players map[uint64][]stats.BanPlayer) (added, removed int, err error)
	BanCounts(ctx context.Context) (checked, steamBanned, sourceBans int, err error)
	RunningBans(ctx context.Context, banType string) ([]stats.PlayerBan, error)
	EndBan(ctx context.Context, b stats.PlayerBan, now time.Time) (bool, error)
	SetOption(ctx context.Context, key, value string) error
}

// BanList reads the whole SourceBans list (sourcebans.Client.List).
type BanList func(ctx context.Context, base string, pause time.Duration) ([]sourcebans.Ban, error)

// BanSyncConfig is features.bans_sync, with the SourceBans part of
// features.sourcebans.
type BanSyncConfig struct {
	Enabled bool
	// Recheck is how long an answer of Steam is used before the account is
	// asked again.
	Recheck time.Duration
	// Pause is the pause between two requests to the Steam Web API (one
	// request asks for steam.MaxIDs accounts).
	Pause time.Duration
	// SourceBans reads the SourceBans list every SourceBansEvery
	// (features.sourcebans enabled and an address set).
	SourceBans      bool
	SourceBansEvery time.Duration
}

const (
	// BanSyncTick is how often the job looks for work: expired SourceBans
	// bans, a due list, accounts due at Steam.
	BanSyncTick = time.Minute
	// BanSyncScan is how often the players' Steam IDs are read again (new
	// players, which are asked first) and hlstats_Bans_Players is updated.
	BanSyncScan = 10 * time.Minute
	// BanSyncSlice is the longest the job asks Steam in one go before it
	// looks at the other work again.
	BanSyncSlice = 50 * time.Second
	// BanSyncMaxPause caps the pause after errors of the Steam Web API.
	BanSyncMaxPause = 10 * time.Minute
	// BanSyncFailures is how many failed Steam requests in a row end one
	// SteamSlice (the accounts stay queued).
	BanSyncFailures = 5
	// sbPagePause is the pause between two pages of the SourceBans list.
	sbPagePause = 500 * time.Millisecond
)

// BanSync is the bans-sync job (features.bans_sync): it keeps hlstats_Bans
// filled with the bans of every known Steam account, active and inactive
// players of every ranking, so the website reads them from the database
// instead of asking Steam and SourceBans itself.
//
//   - Steam: GetPlayerBans for every account (steam.MaxIDs per request, one
//     request per Pause, longer after HTTP 429 or 5xx), accounts never asked
//     first, then the oldest answer; each is asked again after Recheck.
//     Without an API key (hlstats_Options gamewe_steam_api_key) nothing is
//     asked.
//   - SourceBans: every SourceBansEvery the whole public list
//     (hlstats_Options gamewe_sourcebans_url); the running bans are stored,
//     the rows of bans the list no longer shows as running are deleted.
//     Independently of the list, every BanSyncTick, bans whose end has
//     passed are deleted. When a player's last running SourceBans record
//     (hlstats_Players_Bans) ends this way, the player is back in the
//     ranking if the record took the player out (hideranking 2 to 0; a hide
//     by an admin stays).
//
// The players of a banned account are written to hlstats_Bans_Players
// (every spelling of the Steam ID). The job's state is written to
// hlstats_Options gamewe_next_bans (BansPlan) for the web admin.
type BanSync struct {
	Store  BanSyncStore
	Set    *settings.Live
	Steam  VACLookup
	List   BanList
	Log    *slog.Logger
	Loc    *time.Location // time zone of the dates on the ban list
	Config BanSyncConfig
	Now    func() time.Time // tests
	// Sleep waits d or until ctx ends (tests); nil: a timer.
	Sleep func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	players  map[uint64][]stats.BanPlayer // SteamID64 -> players
	queue    []uint64                     // accounts due at Steam, first first
	lastScan time.Time
	lastSB   time.Time
	pause    time.Duration // current pause between Steam requests
	dirty    bool          // banned accounts without their players yet
	noKeyAt  time.Time
	plan     BansPlan
	written  string
}

// BansPlan is the state of the bans-sync job in hlstats_Options
// gamewe_next_bans (JSON), for the web admin's job plan.
type BansPlan struct {
	Enabled    bool            `json:"enabled"`
	Steam      BansPlanSteam   `json:"steam"`
	SourceBans BansPlanSourceB `json:"sourcebans"`
}

// BansPlanSteam is the Steam part of BansPlan. Next is when the next
// account is asked (0: none due before the next scan).
type BansPlanSteam struct {
	Enabled bool   `json:"enabled"` // an API key is set
	Recheck int64  `json:"recheck"` // seconds
	Due     int    `json:"due"`
	Checked int    `json:"checked"`
	Banned  int    `json:"banned"`
	Last    int64  `json:"last,omitempty"`
	Next    int64  `json:"next,omitempty"`
	Error   string `json:"error,omitempty"`
}

// BansPlanSourceB is the SourceBans part of BansPlan.
type BansPlanSourceB struct {
	Enabled  bool   `json:"enabled"` // features.sourcebans and an address
	Interval int64  `json:"interval"`
	Bans     int    `json:"bans"`
	Last     int64  `json:"last,omitempty"`
	Next     int64  `json:"next,omitempty"`
	Error    string `json:"error,omitempty"`
}

// BansPlanKey is the hlstats_Options key of BansPlan.
const BansPlanKey = "gamewe_next_bans"

func (w *BanSync) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *BanSync) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run works until ctx ends. Switched off, it only writes that to the plan.
func (w *BanSync) Run(ctx context.Context) {
	if !w.Config.Enabled {
		w.writePlan(ctx)
		return
	}
	for {
		w.Step(ctx)
		if w.sleep(ctx, BanSyncTick) != nil {
			return
		}
	}
}

// Step does what is due: the expiry cleanup, the SourceBans list, the scan
// of the players' Steam IDs and up to BanSyncSlice of Steam requests.
func (w *BanSync) Step(ctx context.Context) {
	now := w.now()
	w.Expire(ctx, now)
	rescan := now.Sub(w.lastScan) >= BanSyncScan
	if w.sourceBansOn() && now.Sub(w.lastSB) >= w.Config.SourceBansEvery {
		w.lastSB = now
		if err := w.SyncSourceBans(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("bans-sync: SourceBans list not read, bans unchanged", "err", err)
		}
		rescan = true // new bans need their players
	}
	if rescan {
		if err := w.Scan(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("bans-sync: reading the players' Steam IDs failed", "err", err)
		}
	}
	if err := w.SteamSlice(ctx, BanSyncSlice); err != nil && ctx.Err() == nil {
		w.Log.Error("bans-sync: storing Steam bans failed", "err", err)
	}
	if err := w.Players(ctx); err != nil && ctx.Err() == nil {
		w.Log.Error("bans-sync: writing the players of the banned accounts failed", "err", err)
	}
	w.writePlan(ctx)
}

// Players writes hlstats_Bans_Players again (with the Steam IDs of the
// last scan) when Steam reported new bans since.
func (w *BanSync) Players(ctx context.Context) error {
	w.mu.Lock()
	dirty, players := w.dirty, w.players
	w.dirty = false
	w.mu.Unlock()
	if !dirty {
		return nil
	}
	_, _, err := w.Store.SyncBanPlayers(ctx, players)
	if err != nil {
		w.mu.Lock()
		w.dirty = true
		w.mu.Unlock()
	}
	return err
}

func (w *BanSync) sourceBansOn() bool {
	return w.Config.SourceBans && sourcebans.Base(w.Set.Get().SourceBansURL) != ""
}

// Expire deletes the SourceBans rows whose end has passed and ends the
// running SourceBans records (hlstats_Players_Bans) whose end has passed.
func (w *BanSync) Expire(ctx context.Context, now time.Time) {
	n, err := w.Store.DeleteExpiredSourceBans(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Error("bans-sync: deleting expired SourceBans bans failed", "err", err)
		}
		return
	}
	if n > 0 {
		w.Log.Info("bans-sync: expired SourceBans bans deleted", "count", n)
	}
	w.endRecords(ctx, now, nil)
}

// endRecords ends the running SourceBans records of hlstats_Players_Bans
// whose end has passed and, with running (the accounts of a completely
// read list), those whose account the list no longer shows as banned.
func (w *BanSync) endRecords(ctx context.Context, now time.Time, running map[uint64]bool) {
	recs, err := w.Store.RunningBans(ctx, stats.BanTypeSourceBans)
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Error("bans-sync: reading the SourceBans records failed", "err", err)
		}
		return
	}
	for _, r := range recs {
		why := ""
		switch id, ok := ID64(r.UniqueID); {
		case r.EndsAt > 0 && r.EndsAt <= now.Unix():
			why = "expired"
		case running != nil && ok && !running[id]:
			why = "no longer on the ban list"
		}
		if why == "" {
			continue
		}
		back, err := w.Store.EndBan(ctx, r, now)
		if err != nil {
			w.Log.Error("bans-sync: ending a SourceBans record failed", "game", r.Game, "err", err)
			continue
		}
		w.Log.Info("bans-sync: SourceBans record ended", "game", r.Game, "unique_id", r.UniqueID, "why", why, "back_in_ranking", back)
	}
}

// ID64 returns the SteamID64 of a Steam ID in any spelling the logs and
// the ban lists use: X:Y, STEAM_N:X:Y, [U:1:N], U:1:N or a SteamID64.
func ID64(id string) (uint64, bool) {
	acc, ok := sourcebans.Account(id)
	if !ok {
		return 0, false
	}
	s, ok := steam.ID64(acc)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// SyncSourceBans reads the whole SourceBans list and makes the SourceBans
// rows of hlstats_Bans its running bans. A list that could not be read
// completely changes nothing.
func (w *BanSync) SyncSourceBans(ctx context.Context) error {
	base := sourcebans.Base(w.Set.Get().SourceBansURL)
	if base == "" {
		return nil
	}
	list, err := w.List(ctx, base, sbPagePause)
	now := w.now()
	w.mu.Lock()
	w.plan.SourceBans.Last = now.Unix()
	w.plan.SourceBans.Error = ""
	if err != nil {
		w.plan.SourceBans.Error = err.Error()
	}
	w.mu.Unlock()
	if err != nil {
		return err
	}
	bans := RunningSourceBans(list, w.Loc, now)
	removed, err := w.Store.SaveSourceBans(ctx, bans, now)
	if err != nil {
		return err
	}
	running := map[uint64]bool{}
	for _, b := range bans {
		running[b.SteamID64] = true
	}
	w.endRecords(ctx, now, running)
	w.Log.Info("bans-sync: SourceBans list read", "listed", len(list), "running", len(bans), "deleted", removed)
	return nil
}

// RunningSourceBans picks the running bans of a list (permanent or active
// and not yet past their end), one per account: the newest.
func RunningSourceBans(list []sourcebans.Ban, loc *time.Location, now time.Time) []stats.SourceBan {
	if loc == nil {
		loc = time.Local
	}
	by := map[uint64]stats.SourceBan{}
	var order []uint64
	for _, b := range list {
		if !b.State.Running() {
			continue
		}
		id, ok := ID64(b.Steam)
		if !ok {
			continue
		}
		sb := stats.SourceBan{SteamID64: id, Reason: b.Reason, Admin: b.Admin}
		if t, ok := sourcebans.ParseDate(b.Date, loc); ok && !t.After(now.Add(time.Hour)) {
			sb.Created = t
		}
		if m, ok := sourcebans.LengthMinutes(b.Length); ok && m > 0 && b.State != sourcebans.Permanent {
			sb.LengthMinutes = m
			if !sb.Created.IsZero() {
				sb.Ends = sb.Created.Add(time.Duration(m) * time.Minute)
				if !sb.Ends.After(now) {
					continue // ran out, the list does not know yet
				}
			}
		}
		old, seen := by[id]
		if !seen {
			order = append(order, id)
		}
		if !seen || sb.Created.After(old.Created) {
			by[id] = sb
		}
	}
	out := make([]stats.SourceBan, 0, len(order))
	for _, id := range order {
		out = append(out, by[id])
	}
	return out
}

// Scan reads the Steam IDs of all players, updates hlstats_Bans_Players and
// queues the accounts due at Steam: never asked first, then the oldest
// answer older than Recheck.
func (w *BanSync) Scan(ctx context.Context) error { return w.scan(ctx, true) }

// Count queues the accounts due at Steam like Scan, without writing
// anything (gamewe bans --dry-run).
func (w *BanSync) Count(ctx context.Context) error { return w.scan(ctx, false) }

func (w *BanSync) scan(ctx context.Context, write bool) error {
	accs, err := w.Store.BanAccounts(ctx)
	if err != nil {
		return err
	}
	players := map[uint64][]stats.BanPlayer{}
	for _, a := range accs {
		id, ok := ID64(a.UniqueID)
		if !ok {
			continue
		}
		p := stats.BanPlayer{PlayerID: a.PlayerID, Game: a.Game}
		if !slices.Contains(players[id], p) {
			players[id] = append(players[id], p)
		}
	}
	if write {
		if _, _, err := w.Store.SyncBanPlayers(ctx, players); err != nil {
			return err
		}
	}
	checked, err := w.Store.SteamChecked(ctx)
	if err != nil {
		return err
	}
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.players = players
	w.lastScan = now
	if write {
		w.dirty = false
	}
	w.queue = DueAccounts(players, checked, now, w.Config.Recheck)
	return nil
}

// DueAccounts returns the accounts to ask at Steam: those never asked
// first (lowest SteamID64 first), then those whose answer is older than
// recheck, oldest first.
func DueAccounts(players map[uint64][]stats.BanPlayer, checked map[uint64]int64, now time.Time, recheck time.Duration) []uint64 {
	var fresh, old []uint64
	limit := now.Add(-recheck).Unix()
	for id := range players {
		at, ok := checked[id]
		switch {
		case !ok:
			fresh = append(fresh, id)
		case at <= limit:
			old = append(old, id)
		}
	}
	slices.Sort(fresh)
	slices.SortFunc(old, func(a, b uint64) int {
		if checked[a] != checked[b] {
			return int(checked[a] - checked[b])
		}
		if a < b {
			return -1
		}
		return 1
	})
	return append(fresh, old...)
}

// key is the Steam Web API key ("" with a warning once an hour).
func (w *BanSync) key(now time.Time) string {
	k := string(w.Set.Get().SteamAPIKey)
	if k == "" {
		w.mu.Lock()
		if now.Sub(w.noKeyAt) >= time.Hour {
			w.noKeyAt = now
			w.Log.Info("bans-sync: no Steam Web API key (web admin: Settings → Steam), Steam bans are not read")
		}
		w.mu.Unlock()
	}
	return k
}

// SteamSlice asks Steam for the queued accounts, steam.MaxIDs per request
// with the pause between two, for at most d. On HTTP 429 or a server error
// the pause doubles (up to BanSyncMaxPause) and the accounts stay queued;
// a refused key stops until the next scan, BanSyncFailures failures in a
// row until the next call.
func (w *BanSync) SteamSlice(ctx context.Context, d time.Duration) error {
	start := w.now()
	key := w.key(start)
	if key == "" {
		return nil
	}
	if w.pause < w.Config.Pause {
		w.pause = w.Config.Pause
	}
	failed := 0
	for first := true; ; first = false {
		w.mu.Lock()
		n := min(len(w.queue), steam.MaxIDs)
		part := slices.Clone(w.queue[:n])
		w.mu.Unlock()
		if n == 0 {
			return nil
		}
		if !first {
			if w.now().Add(w.pause).Sub(start) > d {
				return nil
			}
			if err := w.sleep(ctx, w.pause); err != nil {
				return err
			}
		}
		ids := make([]string, len(part))
		for i, id := range part {
			ids[i] = strconv.FormatUint(id, 10)
		}
		res, err := w.Steam(ctx, key, ids)
		now := w.now()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.mu.Lock()
			w.plan.Steam.Error = err.Error()
			w.mu.Unlock()
			if errors.Is(err, steam.ErrKey) {
				w.Log.Warn("bans-sync: Steam Web API refused the key, Steam bans are not read until the next scan", "err", err)
				w.mu.Lock()
				w.queue = nil
				w.mu.Unlock()
				return nil
			}
			w.pause = min(max(w.pause*2, time.Second), BanSyncMaxPause)
			if failed++; failed >= BanSyncFailures {
				return nil
			}
			if steam.Busy(err) {
				w.Log.Warn("bans-sync: Steam Web API busy, slowing down", "err", err, "pause", w.pause.String())
			} else {
				w.Log.Warn("bans-sync: Steam Web API failed, trying again", "err", err, "pause", w.pause.String())
			}
			continue
		}
		w.pause, failed = w.Config.Pause, 0
		bans := make([]stats.SteamBan, 0, len(part))
		for i, id := range part {
			b := res[ids[i]] // missing: unknown to Steam, no ban
			bans = append(bans, stats.SteamBan{SteamID64: id, VACBanned: b.VACBanned, VACBans: b.NumberOfVACBans,
				GameBans: b.NumberOfGameBans, CommunityBanned: b.CommunityBanned, EconomyBan: b.EconomyBan, DaysSinceLastBan: b.DaysSinceLastBan})
		}
		if err := w.Store.SaveSteamBans(ctx, bans, now); err != nil {
			return err
		}
		newBan := false
		for _, b := range bans {
			newBan = newBan || b.Banned()
		}
		w.mu.Lock()
		w.queue = w.queue[n:]
		w.plan.Steam.Last = now.Unix()
		w.plan.Steam.Error = ""
		w.mu.Unlock()
		if newBan {
			w.mu.Lock()
			w.dirty = true // the players of the banned accounts: Players
			w.mu.Unlock()
		}
	}
}

// Plan returns the job's state for the web admin.
func (w *BanSync) Plan(ctx context.Context) BansPlan {
	now := w.now()
	w.mu.Lock()
	p := w.plan
	p.Enabled = w.Config.Enabled
	p.Steam.Recheck = int64(w.Config.Recheck / time.Second)
	p.Steam.Due = len(w.queue)
	p.SourceBans.Interval = int64(w.Config.SourceBansEvery / time.Second)
	lastScan, lastSB := w.lastScan, w.lastSB
	w.mu.Unlock()
	p.Steam.Enabled = w.Set.Get().SteamAPIKey != ""
	p.SourceBans.Enabled = w.Config.SourceBans && sourcebans.Base(w.Set.Get().SourceBansURL) != ""
	if p.Enabled {
		if checked, banned, sb, err := w.Store.BanCounts(ctx); err == nil {
			p.Steam.Checked, p.Steam.Banned, p.SourceBans.Bans = checked, banned, sb
		}
		if p.Steam.Enabled {
			if p.Steam.Due > 0 {
				p.Steam.Next = now.Add(w.pause).Unix()
			} else if !lastScan.IsZero() {
				p.Steam.Next = lastScan.Add(BanSyncScan).Unix()
			}
		}
		if p.SourceBans.Enabled {
			p.SourceBans.Next = max(lastSB.Add(w.Config.SourceBansEvery).Unix(), now.Unix())
		}
	} else {
		p.Steam, p.SourceBans = BansPlanSteam{}, BansPlanSourceB{}
	}
	return p
}

// writePlan stores the plan when it changed.
func (w *BanSync) writePlan(ctx context.Context) {
	b, _ := json.Marshal(w.Plan(ctx))
	if string(b) == w.written {
		return
	}
	if err := w.Store.SetOption(ctx, BansPlanKey, string(b)); err != nil {
		if ctx.Err() == nil {
			w.Log.Error("bans-sync: writing the plan failed", "err", err)
		}
		return
	}
	w.written = string(b)
}

// Once does one complete pass (gamewe bans): the expiry cleanup, the
// SourceBans list (when switched on) and every due Steam account, without
// the time limit of the running job. It returns the errors of the Steam
// part only through the log; the SourceBans error is returned.
func (w *BanSync) Once(ctx context.Context) error {
	now := w.now()
	w.Expire(ctx, now)
	var sbErr error
	if w.sourceBansOn() {
		w.lastSB = now
		sbErr = w.SyncSourceBans(ctx)
	}
	if err := w.Scan(ctx); err != nil {
		return err
	}
	if err := w.SteamSlice(ctx, 24*time.Hour); err != nil {
		return err
	}
	if err := w.Players(ctx); err != nil {
		return err
	}
	return sbErr
}

// Due returns how many accounts are queued for Steam (after Scan).
func (w *BanSync) Due() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}
