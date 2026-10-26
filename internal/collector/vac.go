// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

// VACStore is what the VAC block needs of the database (*stats.Store).
type VACStore interface {
	HasRunningBan(ctx context.Context, game, uniqueID, banType string) (bool, error)
	RecordBan(ctx context.Context, game, uniqueID string, b stats.Ban, now time.Time) (int, error)
	RunningBans(ctx context.Context, banType string) ([]stats.PlayerBan, error)
	CheckedBan(ctx context.Context, id int64, now time.Time) error
	EndBan(ctx context.Context, b stats.PlayerBan, now time.Time) (bool, error)
}

// VACLookup returns the bans of up to steam.MaxIDs players by SteamID64
// (steam.Client.PlayerBans).
type VACLookup func(ctx context.Context, key string, ids []string) (map[string]steam.PlayerBans, error)

// VACNotifier is told about every connect of a player (Processor.VAC).
type VACNotifier interface {
	Connected(srv stats.Server, userID int, uniqueID string)
}

const (
	// VACQueue is how many connects wait for their check; more are dropped
	// (not blocked).
	VACQueue = 256
	// VACCacheFor is how long an answer of the Steam Web API is used, with
	// and without a ban.
	VACCacheFor = 24 * time.Hour
	// VACCacheMax caps the remembered answers.
	VACCacheMax = 20000
	// VACGamesEvery is how often the rankings' switches (the settings,
	// reloaded every minute) are compared with the last ones.
	VACGamesEvery = time.Minute
	// VACRecheckEvery is how often the running VAC records are asked again
	// (a ban Steam removed ends; the first time one VACRecheckEvery after
	// the start).
	VACRecheckEvery = 24 * time.Hour
	// VACKickReason is the kick message.
	VACKickReason = "VAC ban on record"
)

// vacRecheckPause is the pause between two requests of the re-check
// (variable for tests).
var vacRecheckPause = time.Second

// VACBlock keeps players with a VAC ban on record out of the rankings
// that switch it on (hlstats_Options gamewe_block_vac_<game>): at every
// connect the player's bans are read from the Steam Web API (ISteamUser/GetPlayerBans,
// key hlstats_Options gamewe_steam_api_key, answers cached VACCacheFor);
// a VAC ban is recorded in hlstats_Players_Bans (type vac, out of the
// ranking) and the player is kicked over RCON. Without a key, with an
// unreachable API or a ranking that does not switch it on, nothing happens
// (no request either). When a ranking switches it off, its VAC records end
// and their players are back in the ranking.
type VACBlock struct {
	Store  VACStore
	Set    *settings.Live
	Lookup VACLookup
	Reply  Replier
	Log    *slog.Logger
	Now    func() time.Time // tests

	once    sync.Once
	queue   chan vacItem
	mu      sync.Mutex
	games   map[string]bool      // rankings with the block on; nil until read
	cache   map[string]vacAnswer // SteamID64 -> answer
	noKeyAt time.Time            // last "no key" warning
}

type vacItem struct {
	srv      stats.Server
	userID   int
	uniqueID string
	id64     string
}

type vacAnswer struct {
	vac  bool
	bans int
	at   time.Time
}

func (w *VACBlock) init() {
	w.once.Do(func() {
		w.queue = make(chan vacItem, VACQueue)
		w.cache = map[string]vacAnswer{}
	})
}

func (w *VACBlock) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// on reports whether the ranking switches the block on.
func (w *VACBlock) on(game string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.games[game]
}

// Connected is called by the processors for every connect of a player;
// it never blocks. Bots and ids that are no Steam account are skipped.
func (w *VACBlock) Connected(srv stats.Server, userID int, uniqueID string) {
	if !w.on(srv.Game) || stats.IsBot(uniqueID) {
		return
	}
	id64, ok := steam.ID64(uniqueID)
	if !ok {
		return
	}
	w.init()
	select {
	case w.queue <- vacItem{srv, userID, uniqueID, id64}:
	default:
		w.Log.Warn("vac: check queue full, connect not checked", "game", srv.Game)
	}
}

// Run reads the switches (now and every VACGamesEvery), checks the
// connects and asks the running records again every VACRecheckEvery
// until ctx ends.
func (w *VACBlock) Run(ctx context.Context) {
	w.init()
	w.refresh(ctx)
	g := time.NewTicker(VACGamesEvery)
	defer g.Stop()
	r := time.NewTicker(VACRecheckEvery)
	defer r.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-w.queue:
			w.handle(ctx, it)
		case <-g.C:
			w.refresh(ctx)
		case <-r.C:
			if err := w.Recheck(ctx); err != nil && ctx.Err() == nil {
				w.Log.Error("vac: checking the running records failed", "err", err)
			}
		}
	}
}

// refresh takes the switches from the settings; when a ranking switched
// off (or at the start) the VAC records of rankings without the switch end.
func (w *VACBlock) refresh(ctx context.Context) {
	games := w.Set.Get().VACBlockGames()
	w.mu.Lock()
	first, off := w.games == nil, false
	for g := range w.games {
		off = off || !games[g]
	}
	w.games = games
	w.mu.Unlock()
	if first || off {
		if err := w.EndSwitchedOff(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("vac: ending the records of rankings without the switch failed", "err", err)
		}
	}
}

// EndSwitchedOff ends the running VAC records of rankings that do not
// switch the block on (the players are back in the ranking when the
// record had taken them out).
func (w *VACBlock) EndSwitchedOff(ctx context.Context) error {
	running, err := w.Store.RunningBans(ctx, stats.BanTypeVAC)
	if err != nil {
		return err
	}
	for _, b := range running {
		if !w.on(b.Game) {
			w.end(ctx, b, w.now(), "switched off for the ranking")
		}
	}
	return nil
}

func (w *VACBlock) key(now time.Time) string {
	k := string(w.Set.Get().SteamAPIKey)
	if k == "" {
		w.mu.Lock()
		if now.Sub(w.noKeyAt) >= time.Hour {
			w.noKeyAt = now
			w.Log.Warn("vac: no valid Steam Web API key (web admin: Settings → Steam), players are not checked")
		}
		w.mu.Unlock()
	}
	return k
}

func (w *VACBlock) cached(id64 string, now time.Time) (vacAnswer, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a, ok := w.cache[id64]
	if ok && now.Sub(a.at) >= VACCacheFor {
		delete(w.cache, id64)
		ok = false
	}
	return a, ok
}

func (w *VACBlock) remember(id64 string, a vacAnswer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.cache) >= VACCacheMax {
		for k, v := range w.cache {
			if a.at.Sub(v.at) >= VACCacheFor {
				delete(w.cache, k)
			}
		}
		if len(w.cache) >= VACCacheMax {
			return
		}
	}
	w.cache[id64] = a
}

func (w *VACBlock) forget(id64 string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.cache, id64)
}

func (w *VACBlock) handle(ctx context.Context, it vacItem) {
	if !w.on(it.srv.Game) {
		return // switched off meanwhile
	}
	now := w.now()
	if a, ok := w.cached(it.id64, now); ok {
		if a.vac {
			w.block(ctx, it, a.bans, now)
		}
		return
	}
	// Recorded before (a restart forgot the answer): kick without asking.
	if held, err := w.Store.HasRunningBan(ctx, it.srv.Game, it.uniqueID, stats.BanTypeVAC); err != nil {
		w.Log.Error("vac: reading the records failed", "game", it.srv.Game, "err", err)
	} else if held {
		w.kick(it)
		return
	}
	key := w.key(now)
	if key == "" {
		return
	}
	res, err := w.Lookup(ctx, key, []string{it.id64})
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Warn("vac: Steam Web API failed, player not checked", "game", it.srv.Game, "err", err)
		}
		return
	}
	b := res[it.id64] // missing: unknown to Steam, no ban
	a := vacAnswer{vac: b.VAC(), bans: b.NumberOfVACBans, at: now}
	w.remember(it.id64, a)
	if a.vac {
		w.block(ctx, it, a.bans, now)
	}
}

// block records the VAC ban and kicks the player.
func (w *VACBlock) block(ctx context.Context, it vacItem, bans int, now time.Time) {
	reason := VACKickReason
	if bans > 0 {
		reason = fmt.Sprintf("%s (%d)", VACKickReason, bans)
	}
	hidden, err := w.Store.RecordBan(ctx, it.srv.Game, it.uniqueID, stats.Ban{Type: stats.BanTypeVAC, Reason: reason}, now)
	if err != nil {
		w.Log.Error("vac: storing the record failed", "game", it.srv.Game, "err", err)
	} else {
		w.Log.Info("vac: player with a VAC ban on record blocked", "game", it.srv.Game, "unique_id", it.uniqueID,
			"vac_bans", bans, "taken_out_of_ranking", hidden)
	}
	w.kick(it)
}

// kick sends the kick for the player's user id over RCON.
func (w *VACBlock) kick(it vacItem) {
	if it.userID <= 0 {
		return
	}
	if it.srv.RconPassword == "" {
		w.Log.Warn("vac: server has no rcon_password, player not kicked", "game", it.srv.Game, "server", it.srv.Address)
		return
	}
	w.Reply.Send(it.srv, KickCommand(it.srv.Engine, it.userID, VACKickReason))
}

// KickCommand is the console command that kicks the player with the user
// id (the number in the log's player tag) with the reason.
func KickCommand(engine games.Engine, userID int, reason string) string {
	r := `"` + Sanitize(reason) + `"`
	if engine == games.GoldSrc {
		return "kick #" + strconv.Itoa(userID) + " " + r
	}
	return "kickid " + strconv.Itoa(userID) + " " + r
}

// Recheck asks the Steam Web API again for the players of the running VAC
// records (steam.MaxIDs per request): a record whose ranking switched the
// block off or whose player Steam no longer shows with a VAC ban ends.
// When the API cannot be asked nothing changes.
func (w *VACBlock) Recheck(ctx context.Context) error {
	running, err := w.Store.RunningBans(ctx, stats.BanTypeVAC)
	if err != nil {
		return err
	}
	var ask []stats.PlayerBan
	for _, b := range running {
		if !w.on(b.Game) {
			w.end(ctx, b, w.now(), "switched off for the ranking")
			continue
		}
		ask = append(ask, b)
	}
	if len(ask) == 0 {
		return nil
	}
	key := w.key(w.now())
	if key == "" {
		return nil
	}
	for start := 0; start < len(ask); start += steam.MaxIDs {
		if start > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(vacRecheckPause):
			}
		}
		part := ask[start:min(start+steam.MaxIDs, len(ask))]
		var ids []string
		seen := map[string]bool{}
		for _, b := range part {
			if id, ok := steam.ID64(b.UniqueID); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		res, err := w.Lookup(ctx, key, ids)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			w.Log.Warn("vac: Steam Web API failed, running records unchanged", "err", err)
			continue
		}
		now := w.now()
		for _, b := range part {
			id, ok := steam.ID64(b.UniqueID)
			if !ok {
				continue
			}
			p, known := res[id]
			if known && !p.VAC() {
				w.forget(id)
				w.end(ctx, b, now, "Steam shows no VAC ban any more")
				continue
			}
			if err := w.Store.CheckedBan(ctx, b.ID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *VACBlock) end(ctx context.Context, b stats.PlayerBan, now time.Time, why string) {
	back, err := w.Store.EndBan(ctx, b, now)
	if err != nil {
		w.Log.Error("vac: ending a record failed", "game", b.Game, "err", err)
		return
	}
	w.Log.Info("vac: record ended", "game", b.Game, "unique_id", b.UniqueID, "why", why, "back_in_ranking", back)
}
