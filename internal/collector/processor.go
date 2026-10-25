// SPDX-License-Identifier: GPL-3.0-or-later

// Package collector turns parsed log events into database updates and
// answers in-game commands.
//
// Every configured game server gets its own Processor, fed by its own
// goroutine (see Hub). A Processor only ever sees lines of its server, so
// player state, sessions and rounds can never mix between servers, even
// when two servers use the same userids.
package collector

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// Store is the database API the collector needs. *stats.Store implements
// it; it must be safe for concurrent use by several Processors.
type Store interface {
	Servers(ctx context.Context) ([]stats.Server, error)
	Options(ctx context.Context) (map[string]string, error)
	ResetLive(ctx context.Context, serverID int64) error
	MapID(ctx context.Context, game, name string) (int, error)
	MapStarted(ctx context.Context, ev stats.Ev, name string) (int, error)
	Player(ctx context.Context, ev stats.Ev, uniqueID, name string) (int64, int, error)
	Join(ctx context.Context, ev stats.Ev, p stats.Live) error
	Connect(ctx context.Context, ev stats.Ev, p stats.Live, ip netip.Addr) error
	Enter(ctx context.Context, ev stats.Ev, p stats.Live) error
	Leave(ctx context.Context, ev stats.Ev, p stats.Live, seconds int, logged bool) error
	Rename(ctx context.Context, ev stats.Ev, p stats.Live, newName string) error
	ChangeTeam(ctx context.Context, ev stats.Ev, p stats.Live, team string) error
	ChangeRole(ctx context.Context, ev stats.Ev, p stats.Live, role string) error
	AddPlayTime(ctx context.Context, ev stats.Ev, team string, teamSecs int, role string, roleSecs int) error
	RecordLocation(ctx context.Context, ev stats.Ev, playerID int64, typ, x, y int) error
	RecordKill(ctx context.Context, ev stats.Ev, k stats.Kill) (stats.KillResult, error)
	RecordSuicide(ctx context.Context, ev stats.Ev, p stats.Live, weapon string) (int, error)
	RecordWorldDeath(ctx context.Context, ev stats.Ev, p stats.Live, weapon string) error
	RecordPlayerAction(ctx context.Context, ev stats.Ev, p stats.Live, code string) (int, error)
	RecordPlayerPlayerAction(ctx context.Context, ev stats.Ev, p, victim stats.Live, code string, noSkill bool) (int, int, error)
	RecordRoundWin(ctx context.Context, ev stats.Ev, team, trigger string, winners, losers []stats.Live, survivors map[int64]bool) (int, error)
	AddWeaponStats(ctx context.Context, ev stats.Ev, p stats.Live, weapon string, w stats.WeaponStats) error
	SetActivePlayers(ctx context.Context, serverID int64, n int) error
	RecordChat(ctx context.Context, ev stats.Ev, p stats.Live, team bool, msg string) error
	RoundStart(ctx context.Context, ev stats.Ev) error
	HideBanned(ctx context.Context, ev stats.Ev, uniqueID string) error
	ClanFromName(ctx context.Context, ev stats.Ev, p stats.Live, name string) (int64, error)
	ClanTag(ctx context.Context, ev stats.Ev, p stats.Live, tag string) (int64, error)
	UpdateServerPlayers(ctx context.Context) error
	SetLocation(ctx context.Context, ev stats.Ev, p stats.Live, loc stats.Location) error
	ServerLocation(ctx context.Context, serverID int64, loc stats.Location) error
	RecordServerLoad(ctx context.Context, l stats.Load) error
	ServerStatus(ctx context.Context, serverID int64, hostname string, maxPlayers int, pings map[string]int) error
	ServerInfo(ctx context.Context, serverID int64, info stats.ServerInfo) error
	SetConnectType(ctx context.Context, playerID int64, t int) error
	RecordLatency(ctx context.Context, ev stats.Ev, p stats.Live, ping int) error
	SetLiveTeam(ctx context.Context, ev stats.Ev, p stats.Live) error
	KillAssist(ctx context.Context, ev stats.Ev, p stats.Live) error
	FragPositions(ctx context.Context, ev stats.Ev, killerID, victimID int64, kpos, vpos [3]int) error
	HideRanking(ctx context.Context, playerID int64) (int, error)
	SetHideRanking(ctx context.Context, playerID int64, from, to int) (int, error)
	PlayerStats(ctx context.Context, playerID int64) (stats.Player, error)
	Rank(ctx context.Context, game string, skill int) (rank, total int, err error)
	Next(ctx context.Context, game string, skill int) (stats.Player, error)
	Above(ctx context.Context, game string, skill, n int) ([]stats.Player, error)
	Top(ctx context.Context, game string, n int) ([]stats.Player, error)
}

// Processor handles the log lines of one game server. It is not safe for
// concurrent use.
type Processor struct {
	store Store
	set   *settings.Live
	cmds  Commands
	log   *slog.Logger
	srv   serverState

	geoCache map[netip.Addr]geoEntry

	// Geo looks up locations (nil: no GeoIP). Set before the first line.
	Geo GeoLookup
	// ServerStats asks the game server for its load (RCON "stats"); tests
	// replace it.
	ServerStats func(srv stats.Server, timeout time.Duration) (ServerStats, error)
}

// GeoLookup finds the location of an IP address (*geoip.DB).
type GeoLookup interface {
	Lookup(path string, ip netip.Addr) (stats.Location, bool)
}

// LoadInterval is how often a row of hlstats_server_load is written.
const LoadInterval = 10 * time.Minute

type serverState struct {
	info    stats.Server
	ready   bool
	mapID   int
	players map[string]*playerState // by Player.Key()

	// server load (hlstats_server_load)
	loadSince        time.Time // start of the current interval
	minPlay, maxPlay int       // players in the interval
	// Map change: the players connect again and the bots are added anew,
	// so for a while after it the count from before the change stands (see
	// online): no drop to 0, no old and new bots counted at once.
	heldCount   int
	activeCount int       // hlstats_Servers.act_players as last written
	lastLine    time.Time // last log line
	lastStatus  time.Time // last RCON status
	statusCount int       // players and bots listed by the last status

	// RCON status
	statusMap  string // map set by a status, not by a log line yet
	statusHost string // hostname and max players of the last status
	statusMax  int
	// Server info (features.server_info): the last answers of A2S_INFO and
	// of status, and what hlstats_Servers holds.
	a2sInfo, rconInfo, storedInfo stats.ServerInfo
	gone                          map[string]time.Time // players whose disconnect was logged, by key
	// Players whose connect is stored (connect line, or status for those
	// already on the server), by key, until their disconnect line. Kept over
	// map changes: the players stay connected, only their visits restart.
	connected map[string]bool

	// Duplicate lines: the game server sends every line twice when the
	// collector is registered twice (logaddress_list).
	lastRaw    string
	lastRawAt  time.Time
	lastTwin   bool   // lastRaw was the second copy of a line
	pairedLine string // the last line that came twice
	pairs      int    // different lines in a row that came twice
	unpaired   int    // lines in a row that came once
	doubled    bool   // the server sends everything twice: second copies are skipped
	dupWarned  time.Time
	// trimWarned: the last warning that more players were tracked than the
	// server has slots.
	trimWarned time.Time

	// pendingKill is a GoldSrc kill held until the server's next line: CS
	// 1.6 logs no headshots, the AMX plugin logs `triggered "headshot"`
	// right after the kill line. Recorded with the next line, at a tick or
	// at shutdown.
	pendingKill *heldKill
	mapAt       time.Time // last map change
	// linePositions: the server logs positions in its kill lines (CS:GO,
	// TF2, AMX plugin); the SourceMod plugin's "located on" kill lines
	// are then skipped.
	linePositions bool
	// pendingLoc is the plugin's "located on kill" line: it hooks
	// player_death before the engine logs the kill line, so the positions
	// go into the frag of the kill line that follows.
	pendingLoc *pendingLocation
	upSince    time.Time // first line after a silence of LoadInterval (uptime without RCON)
}

// playerState is a player on this server, with the counters of the
// current visit (for !session) and the current streaks.
type playerState struct {
	live                     stats.Live
	userID                   int
	since                    time.Time
	lastSeen                 time.Time // last line naming the player (idle timeout)
	lastTeamChange           time.Time
	teamSince, roleSince     time.Time // start of the time in live.Team / live.Role not yet stored
	kills, deaths, headshots int
	suicides, shots, hits    int
	skillDelta               int
	killStreak, deathStreak  int // kills in this life, deaths in a row
	bestKill, bestDeath      int // best streaks of the visit
	weaponKills              map[string]int
	startRank                int        // rank when the visit started, 0 if unknown
	ping                     int        // of the last status
	lastLatency              time.Time  // last ping sample (hlstats_Events_Latency)
	lastHide                 time.Time  // last !hideranking change (cooldown)
	dead                     bool       // died since the round started (survived rounds)
	missed                   int        // status polls in a row that did not list the player
	connectType              int        // stored this visit (cl_connectmethod)
	locatedIP                netip.Addr // the address whose location is stored this visit

	// Headshot actions: the plugin logs `triggered "headshot"` for every
	// headshot kill and the kill line says (headshot) as well. Whichever
	// comes first is rewarded, the other one is skipped.
	headshotLines, headshotKills int

	// Kill assists: CS:GO logs `assisted killing` and the plugin
	// `triggered "kill_assist" against` for the same kill. The victim keeps
	// the last counted assist, so the second line is not counted again.
	assistBy int64 // player id of the assister
	assistAt time.Time
}

type heldKill struct {
	ev stats.Ev
	e  logparse.Kill
}

// recordHeldKill records a held GoldSrc kill. Its headshot line was
// consumed here, so it is not left to pair with a later kill.
func (p *Processor) recordHeldKill(ctx context.Context, h *heldKill) error {
	if err := p.kill(ctx, h.ev, h.e); err != nil {
		return err
	}
	if st := p.srv.players[h.e.Killer.Key()]; st != nil && h.e.Headshot {
		st.headshotKills = 0
	}
	return nil
}

// flushKill records a held kill (tick, shutdown).
func (p *Processor) flushKill(ctx context.Context) error {
	h := p.srv.pendingKill
	if h == nil {
		return nil
	}
	p.srv.pendingKill = nil
	return p.recordHeldKill(ctx, h)
}

// MaxKillStreak is the highest kill_streak_<n> action (as HLstatsX).
const MaxKillStreak = 12

// teamSwitchSuicide is how long after a team change a suicide is ignored:
// CS:S logs a suicide when a living player changes teams (as HLstatsX).
const teamSwitchSuicide = 2 * time.Second

// VACBanReason is the disconnect reason of a VAC-banned player.
const VACBanReason = "VAC banned from secure server"

// NewProcessor handles the lines of server, a row of hlstats_Servers.
func NewProcessor(store Store, set *settings.Live, server stats.Server, reply Replier, log *slog.Logger) *Processor {
	return &Processor{
		store: store,
		set:   set,
		cmds:  Commands{set: set, store: store, reply: reply, log: log},
		log:   log.With("server", server.Address, "server_id", server.ID),
		srv:   serverState{info: server, players: map[string]*playerState{}, storedInfo: server.Info},

		geoCache: map[netip.Addr]geoEntry{},

		ServerStats: RconStats,
	}
}

// Label names the server in log output: "name ip:port". Only for the
// processor's own goroutine.
func (p *Processor) Label() string {
	return p.srv.info.Name + " " + p.srv.info.Address
}

// Update applies changed hlstats_Servers values (name, RCON password) that
// do not need a new processor.
func (p *Processor) Update(server stats.Server) {
	p.srv.info.Name, p.srv.info.RconPassword, p.srv.info.MaxPlayers = server.Name, server.RconPassword, server.MaxPlayers
}

func (p *Processor) winTrigger(trigger string) bool {
	for _, t := range p.set.Get().WinTriggers {
		if strings.EqualFold(t, trigger) {
			return true
		}
	}
	return false
}

// Handle processes one log line of this processor's server.
func (p *Processor) Handle(ctx context.Context, line string, now time.Time) error {
	if p.duplicate(line, now) {
		return nil
	}
	e, ok := logparse.ParseLine(line)
	if !ok {
		return nil
	}
	if err := p.init(ctx); err != nil {
		return err
	}
	if p.srv.lastLine.IsZero() || now.Sub(p.srv.lastLine) > LoadInterval {
		p.srv.upSince = now // the server was silent: probably restarted
	}
	p.srv.lastLine = now
	if pk := p.srv.pendingKill; pk != nil {
		// GoldSrc: the AMX plugin logs `triggered "headshot"` right after the
		// kill line of a headshot kill; it completes the held kill.
		p.srv.pendingKill = nil
		if t, ok := e.(logparse.PlayerTriggered); ok && t.Trigger == "headshot" && t.Player.Key() == pk.e.Killer.Key() {
			pk.e.Headshot = true
			return p.recordHeldKill(ctx, pk)
		}
		if err := p.recordHeldKill(ctx, pk); err != nil {
			return err
		}
	}
	ev := stats.Ev{Srv: p.srv.info, MapID: p.srv.mapID, Time: now}
	switch e := e.(type) {
	case logparse.MapStarted:
		return p.mapStarted(ctx, ev, e)
	case logparse.Connected:
		delete(p.srv.gone, e.Player.Key()) // a reconnect starts a new visit
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		var ip, stored netip.Addr
		if ap, err := netip.ParseAddrPort(e.Address); err == nil {
			ip = ap.Addr().Unmap()
		}
		if p.set.Get().StorePlayerIP {
			stored = ip
		}
		if err := p.store.Connect(ctx, ev, st.live, stored); err != nil {
			return err
		}
		p.markConnected(e.Player.Key())
		st.locatedIP = netip.Addr{} // a new connect: store the location again
		return p.locate(ctx, ev, st, ip)
	case logparse.Entered:
		delete(p.srv.gone, e.Player.Key())
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		return p.store.Enter(ctx, ev, st.live)
	case logparse.JoinedTeam:
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		if err := p.playTime(ctx, ev, st); err != nil {
			return err
		}
		st.live.Team = e.Team
		st.lastTeamChange = ev.Time
		if neutralTeam(e.Team) {
			return p.store.SetLiveTeam(ctx, ev, st.live) // spectator: shown live, no team event
		}
		return p.store.ChangeTeam(ctx, ev, st.live, e.Team)
	case logparse.NameChange:
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		if err := p.store.Rename(ctx, ev, st.live, e.NewName); err != nil {
			return err
		}
		st.live.Name = e.NewName
		_, err = p.store.ClanFromName(ctx, ev, st.live, e.NewName)
		return err
	case logparse.Disconnected:
		if e.Reason == VACBanReason && e.Player.Trackable() && !e.Player.IsBot() {
			if err := p.store.HideBanned(ctx, ev, UniqueID(e.Player)); err != nil {
				return err
			}
		}
		return p.disconnect(ctx, ev, e.Player)
	case logparse.Requested:
		var st *playerState
		if e.Player != nil {
			var err error
			if st, err = p.player(ctx, ev, *e.Player); st == nil || err != nil {
				return err
			}
		}
		return p.cmds.Query(ctx, &p.srv, st, e.Type, e.QueryID, now)
	case logparse.Located:
		return p.located(ctx, ev, e)
	case logparse.RoleChange:
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		if err := p.playTime(ctx, ev, st); err != nil {
			return err
		}
		st.live.Role = e.Role
		return p.store.ChangeRole(ctx, ev, st.live, e.Role)
	case logparse.Setup:
		if !strings.EqualFold(e.Name, "cl_connectmethod") || e.Player.IsBot() {
			return nil
		}
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		if t := stats.ConnectType(e.Value); t > 0 && t != st.connectType {
			st.connectType = t
			return p.store.SetConnectType(ctx, st.live.ID, t)
		}
	case logparse.KillAssist:
		st, err := p.player(ctx, ev, e.Assister)
		if st == nil || err != nil {
			return err
		}
		v, err := p.player(ctx, ev, e.Victim)
		if err != nil {
			return err
		}
		if err := p.killAssist(ctx, ev, st, v); err != nil {
			return err
		}
		if e.Flash {
			if err := p.playerAction(ctx, ev, st, "flash_assist"); err != nil {
				return err
			}
		}
		return p.playerAction(ctx, ev, st, "kill_assist") // rewarded if the game defines it
	case logparse.Banned:
		if e.Permanent && e.Player.Trackable() && !e.Player.IsBot() {
			return p.store.HideBanned(ctx, ev, UniqueID(e.Player))
		}
	case logparse.Kill:
		if p.srv.info.Engine == games.GoldSrc && !e.Headshot {
			p.srv.pendingKill = &heldKill{ev, e} // see pendingKill
			return nil
		}
		return p.kill(ctx, ev, e)
	case logparse.Suicide:
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		if err := p.lineLocations(ctx, ev, nil, st, nil, e.Pos); err != nil {
			return err
		}
		return p.suicide(ctx, ev, st, e.Weapon)
	case logparse.PlayerTriggered:
		return p.playerTrigger(ctx, ev, e)
	case logparse.WorldDeath:
		st, err := p.player(ctx, ev, e.Player)
		if st == nil || err != nil {
			return err
		}
		st.dead = true
		if err := p.endStreak(ctx, ev, st); err != nil {
			return err
		}
		st.deaths++
		st.deathStreak++
		return p.store.RecordWorldDeath(ctx, ev, st.live, e.Weapon)
	case logparse.Accolade:
		return p.accolade(ctx, ev, e)
	case logparse.TeamTriggered:
		if p.winTrigger(e.Trigger) {
			if err := p.roundWin(ctx, ev, e.Team, e.Trigger); err != nil {
				return err
			}
			return p.endStreaks(ctx, ev)
		}
	case logparse.WorldTriggered:
		if e.Winner != "" && p.winTrigger(e.Trigger) {
			if err := p.roundWin(ctx, ev, e.Winner, e.Trigger); err != nil {
				return err
			}
		}
		switch e.Trigger {
		case "Round_Start":
			for _, st := range p.srv.players {
				st.dead = false
			}
			return p.store.RoundStart(ctx, ev)
		case "Round_End", "Round_Win", "Mini_Round_Win":
			return p.endStreaks(ctx, ev)
		}
	case logparse.Say:
		return p.say(ctx, ev, e, now)
	}
	// TeamScored has no table in the HLstatsX schema and is ignored.
	return nil
}

// Discard forgets the players, streaks and held lines of the server without
// writing anything (a ranking reset follows: their ids are gone). The map
// and the server stay; players come back with their next line or status.
func (p *Processor) Discard() {
	srv := &p.srv
	srv.players = map[string]*playerState{}
	srv.gone, srv.connected, srv.pendingKill, srv.pendingLoc = nil, nil, nil, nil
	srv.minPlay, srv.maxPlay = 0, 0
	srv.heldCount, srv.activeCount, srv.statusCount = 0, 0, 0 // the reset clears act_players too
	srv.lastRaw, srv.lastTwin = "", false
}

// init clears the server's stale live rows and looks up its current map
// on first use.
func (p *Processor) init(ctx context.Context) error {
	srv := &p.srv
	if srv.ready {
		return nil
	}
	if err := p.store.ResetLive(ctx, srv.info.ID); err != nil {
		return err
	}
	mapID, err := p.store.MapID(ctx, srv.info.Game, srv.info.Map)
	if err != nil {
		return err
	}
	srv.mapID, srv.ready = mapID, true
	srv.loadSince = time.Now()
	p.log.Info("game server active", "name", srv.info.Name, "game", srv.info.Game, "map", srv.info.Map)
	if p.Geo != nil {
		if loc, ok := p.Geo.Lookup(p.set.Get().GeoIPDB, srv.info.AddrPort.Addr()); ok {
			return p.store.ServerLocation(ctx, srv.info.ID, loc)
		}
	}
	return nil
}

// mapRepeat is how long after a map change another line for the same map
// ("Loading map" then "Started map", or a status poll first) is the same
// change.
const mapRepeat = 2 * time.Minute

func (p *Processor) mapStarted(ctx context.Context, ev stats.Ev, e logparse.MapStarted) error {
	srv := &p.srv
	if e.Map == srv.info.Map && (e.Map == srv.statusMap || ev.Time.Sub(srv.mapAt) < mapRepeat) {
		srv.statusMap = "" // this change is stored already
		return nil
	}
	srv.statusMap = ""
	return p.changeMap(ctx, ev, e.Map)
}

// changeMap starts a new map: every visit ends (as at shutdown: connection
// time counted, no disconnect event) and the server's live view is cleared,
// because the players connect again (log lines, the next status poll). Bots
// of the old map would otherwise stay listed next to the new ones.
func (p *Processor) changeMap(ctx context.Context, ev stats.Ev, name string) error {
	if err := p.endStreaks(ctx, ev); err != nil {
		return err
	}
	held := p.online(ev.Time)
	if err := p.endVisits(ctx, ev); err != nil {
		return err
	}
	id, err := p.store.MapStarted(ctx, ev, name)
	if err != nil {
		return err
	}
	p.srv.mapID, p.srv.info.Map, p.srv.mapAt = id, name, ev.Time
	p.srv.heldCount = held
	p.counted(ev.Time)
	return p.setActive(ctx, ev.Time)
}

// endVisits ends the visits of all players on the server and clears its
// live rows and act_players.
func (p *Processor) endVisits(ctx context.Context, ev stats.Ev) error {
	for key, st := range p.srv.players {
		delete(p.srv.players, key)
		if err := p.leave(ctx, ev, st, false); err != nil {
			return err
		}
	}
	p.srv.activeCount = 0
	return p.store.ResetLive(ctx, p.srv.info.ID)
}

// locate stores the location of the player's address (connect line or
// RCON status) once per visit and address: hlstats_Players_Data.location_id
// and the live row's cli_location_id. Bots and addresses without a location
// (LAN, loopback, not in the database) get none.
func (p *Processor) locate(ctx context.Context, ev stats.Ev, st *playerState, ip netip.Addr) error {
	if p.Geo == nil || !ip.IsValid() || ip == st.locatedIP || stats.IsBot(st.live.UniqueID) {
		return nil
	}
	st.locatedIP = ip
	c, ok := p.geoCache[ip]
	if !ok || time.Since(c.at) > geoCacheTTL {
		c.loc, c.ok = p.Geo.Lookup(p.set.Get().GeoIPDB, ip)
		c.at = time.Now()
		if len(p.geoCache) >= geoCacheSize {
			clear(p.geoCache)
		}
		p.geoCache[ip] = c
	}
	if !c.ok {
		return nil
	}
	return p.store.SetLocation(ctx, ev, st.live, c.loc)
}

// The processor keeps the GeoIP answers of up to geoCacheSize addresses
// for geoCacheTTL (the database can be replaced while running).
const (
	geoCacheSize = 4096
	geoCacheTTL  = time.Hour
)

type geoEntry struct {
	loc stats.Location
	ok  bool
	at  time.Time
}

// LatencyInterval is how often a player's ping from RCON status is stored
// as a sample (hlstats_Events_Latency).
const LatencyInterval = time.Minute

// statusMisses is how many status polls in a row must miss a player
// before the visit ends: a player can leave between a poll and the
// disconnect line, or connect between the connect line and a poll.
const statusMisses = 2

// Status applies an RCON "status" answer, polled at time at
// (features.rcon_status): the current map, hostname and max players, and
// the players on the server. Players and bots it lists get a visit and a
// live row (also without any log line, e.g. after a collector restart);
// players it misses statusMisses times in a row are ended like idle
// players. Nothing is written when nothing changed.
func (p *Processor) Status(ctx context.Context, st Status, at time.Time) error {
	if err := p.init(ctx); err != nil {
		return err
	}
	srv := &p.srv
	srv.lastStatus = at
	if srv.upSince.IsZero() {
		srv.upSince = at
	}
	ev := stats.Ev{Srv: srv.info, MapID: srv.mapID, Time: at}
	if at.Before(srv.mapAt) {
		// Sent before the map change of a log line, answered after it (the
		// server is busy loading): the old map and its players. Using it
		// would bring back the bots of the old map, or the old map itself.
		return p.statusServer(ctx, st, nil)
	}
	if st.Map != "" && st.Map != srv.info.Map {
		if err := p.changeMap(ctx, ev, st.Map); err != nil {
			return err
		}
		srv.statusMap = st.Map
		ev.MapID, ev.Srv.Map = srv.mapID, st.Map
	}
	for key, t := range srv.gone {
		if at.Sub(t) > time.Minute {
			delete(srv.gone, key)
		}
	}
	listed := map[string]bool{}
	pings := map[string]int{}
	for _, sp := range st.Players {
		ref := logparse.Player{Name: sp.Name, UserID: sp.UserID, SteamID: sp.SteamID}
		if !ref.Trackable() {
			continue
		}
		key := ref.Key()
		listed[key] = true
		ps := srv.players[key]
		if ps == nil {
			if t, ok := srv.gone[key]; ok && !t.Before(at) {
				continue // disconnect logged after this poll
			}
			var connected int64
			if sp.Connected > 0 {
				connected = at.Add(-sp.Connected).Unix()
			}
			var err error
			if ps, err = p.join(ctx, ev, ref, connected); err != nil {
				return err
			}
			ps.ping = -1
		}
		ps.userID, ps.lastSeen, ps.missed = sp.UserID, at, 0
		if ps.ping != sp.Ping {
			ps.ping = sp.Ping
			pings[ps.live.UniqueID] = sp.Ping
		}
		if err := p.statusConnect(ctx, ev, key, ps, sp.Address); err != nil {
			return err
		}
		if err := p.locate(ctx, ev, ps, sp.Address); err != nil {
			return err
		}
		if sp.Ping > 0 && !stats.IsBot(ps.live.UniqueID) && at.Sub(ps.lastLatency) >= LatencyInterval {
			ps.lastLatency = at
			if err := p.store.RecordLatency(ctx, ev, ps.live, sp.Ping); err != nil {
				return err
			}
		}
	}
	left := 0
	for key, ps := range srv.players {
		if listed[key] {
			continue
		}
		misses := statusMisses
		if at.Sub(srv.mapAt) < mapRepeat {
			misses = 1 // right after a map change: who is not listed is gone
		}
		if ps.missed++; ps.missed < misses {
			continue
		}
		delete(srv.players, key)
		left++
		p.log.Debug("player no longer in status, visit ended", "player", ps.live.Name)
		if err := p.endStreak(ctx, ev, ps); err != nil {
			return err
		}
		if err := p.leave(ctx, ev, ps, false); err != nil {
			return err
		}
	}
	srv.statusCount = len(listed)
	p.counted(at)
	if err := p.setActive(ctx, at); err != nil {
		return err
	}
	return p.statusServer(ctx, st, pings)
}

// statusConnect stores the connect of a player whose "connected" line the
// collector never saw (it started, or the server was added, while the
// player was already on it), with the address from status: otherwise the
// player has no connection and no IP. Once until the player's disconnect
// line; bots and players without an address (loopback, LAN) are skipped.
func (p *Processor) statusConnect(ctx context.Context, ev stats.Ev, key string, ps *playerState, ip netip.Addr) error {
	if p.srv.connected[key] || !ip.IsValid() || stats.IsBot(ps.live.UniqueID) {
		return nil
	}
	var stored netip.Addr
	if p.set.Get().StorePlayerIP {
		stored = ip
	}
	if err := p.store.Connect(ctx, ev, ps.live, stored); err != nil {
		return err
	}
	p.markConnected(key)
	return nil
}

// markConnected notes that key's connect is stored. The set is bounded:
// when it is full it starts over (at worst a player gets a second connect
// from status).
func (p *Processor) markConnected(key string) {
	if p.srv.connected == nil || len(p.srv.connected) >= connectedSize {
		p.srv.connected = map[string]bool{}
	}
	p.srv.connected[key] = true
}

const connectedSize = 4096

// statusServer writes hostname, max players and pings of a status answer
// when they changed.
func (p *Processor) statusServer(ctx context.Context, st Status, pings map[string]int) error {
	srv := &p.srv
	host, maxPlayers := "", 0
	if st.Hostname != srv.statusHost {
		host, srv.statusHost = st.Hostname, st.Hostname
	}
	if st.MaxPlayers != srv.statusMax {
		maxPlayers, srv.statusMax = st.MaxPlayers, st.MaxPlayers
	}
	srv.rconInfo = st.Info
	if err := p.writeInfo(ctx); err != nil {
		return err
	}
	if host == "" && maxPlayers == 0 && len(pings) == 0 {
		return nil
	}
	return p.store.ServerStatus(ctx, srv.info.ID, host, maxPlayers, pings)
}

// ServerInfo applies an A2S_INFO answer (features.server_info).
func (p *Processor) ServerInfo(ctx context.Context, info stats.ServerInfo) error {
	p.srv.a2sInfo = info
	return p.writeInfo(ctx)
}

// writeInfo stores version, OS and VAC state when they changed: A2S_INFO
// first, status for what it does not tell; a value nobody reports any more
// stays as it is.
func (p *Processor) writeInfo(ctx context.Context) error {
	srv := &p.srv
	info := srv.a2sInfo.Or(srv.rconInfo).Or(srv.storedInfo)
	if info == srv.storedInfo {
		return nil
	}
	if err := p.store.ServerInfo(ctx, srv.info.ID, info); err != nil {
		return err
	}
	srv.storedInfo = info
	return nil
}

// Shutdown ends the visits of all players still on the server and clears
// the server's live view.
func (p *Processor) Shutdown(ctx context.Context, now time.Time) {
	if !p.srv.ready {
		return
	}
	if err := p.flushKill(ctx); err != nil {
		p.log.Warn("held kill", "err", err)
	}
	ev := stats.Ev{Srv: p.srv.info, MapID: p.srv.mapID, Time: now}
	if err := p.endVisits(ctx, ev); err != nil {
		p.log.Warn("end player visits", "err", err)
	}
}

// UniqueID turns a log SteamID into hlstats_PlayerUniqueIds.uniqueId, as
// HLstatsX does: the STEAM_x: prefix is removed (STEAM_1:0:123 becomes
// 0:123), Steam3 ids get the same form ([U:1:246] is 0:123), bots are
// BOT:<name> (see logparse.NormalizedID).
func UniqueID(ref logparse.Player) string {
	return ref.Key()
}

// player returns the state of a tracked player, registering the player on
// first sight. It returns nil for references that are not tracked
// (console, pending ids).
func (p *Processor) player(ctx context.Context, ev stats.Ev, ref logparse.Player) (*playerState, error) {
	if !ref.Trackable() {
		return nil, nil
	}
	key := ref.Key()
	if st := p.srv.players[key]; st != nil {
		st.userID = ref.UserID
		st.lastSeen = ev.Time
		st.missed = 0
		// Every line carries the team in the player tag: keep the live row's
		// team right also without a "joined team" line (bots, players first
		// seen in RCON status, which has no team).
		if ref.Team != "" && ref.Team != st.live.Team {
			if err := p.playTime(ctx, ev, st); err != nil {
				return nil, err
			}
			st.live.Team = ref.Team
			if err := p.store.SetLiveTeam(ctx, ev, st.live); err != nil {
				return nil, err
			}
		}
		return st, nil
	}
	if t, ok := p.srv.gone[key]; ok && ev.Time.Sub(t) < goneWindow {
		// A line after the logged disconnect (the plugin flushes its stats):
		// no new visit, the player is not on the server.
		return nil, nil
	}
	return p.join(ctx, ev, ref, 0)
}

// botIdle is the idle timeout of bots (at most gamewe_idle_timeout).
const botIdle = 3 * time.Minute

// duplicate reports whether line is the second copy of a line from a game
// server that sends everything twice (the collector registered twice with
// logaddress_add, e.g. by its public and its local address). Each line
// then arrives right after itself, timestamp included. Copies are skipped
// once three different lines in a row came twice (a single repeated line
// is a real event: the same chat line, a repeated command); three lines in
// a row that come once end it.
func (p *Processor) duplicate(line string, now time.Time) bool {
	srv := &p.srv
	twin := line != "" && line == srv.lastRaw && !srv.lastTwin && now.Sub(srv.lastRawAt) < time.Second
	if twin {
		if line != srv.pairedLine {
			srv.pairs++
			srv.pairedLine = line
		}
		srv.unpaired = 0
		if srv.pairs >= 3 && !srv.doubled {
			srv.doubled = true
			if now.Sub(srv.dupWarned) > time.Hour {
				srv.dupWarned = now
				p.log.Warn("every log line arrives twice: the collector is registered more than once on the game server (logaddress_list); the copies are skipped", "server", srv.info.Name)
			}
		}
	} else if srv.lastRaw != "" && !srv.lastTwin {
		if srv.unpaired++; srv.unpaired >= 3 {
			srv.doubled, srv.pairs = false, 0
		}
	}
	srv.lastTwin = twin
	srv.lastRaw, srv.lastRawAt = line, now
	return twin && srv.doubled
}

// goneWindow is how long after a logged disconnect lines of the player
// (other than connected / entered the game) start no new visit.
const goneWindow = time.Minute

// maxPlayers is the server's slot count: of the last RCON status, else
// hlstats_Servers.max_players; 0 if unknown.
func (p *Processor) maxPlayers() int {
	if p.srv.statusMax > 0 {
		return p.srv.statusMax
	}
	return p.srv.info.MaxPlayers
}

// trim keeps the tracked players within the server's slots: more cannot be
// on it, so the extra ones are visits whose end was lost (bots kicked by
// bot_quota without a disconnect line, a lost map change). The players
// without a line for the longest time end first.
func (p *Processor) trim(ctx context.Context, ev stats.Ev) error {
	limit := p.maxPlayers()
	n := len(p.srv.players)
	if limit <= 0 || n <= limit {
		return nil
	}
	keys := make([]string, 0, n)
	for k := range p.srv.players {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := p.srv.players[keys[i]], p.srv.players[keys[j]]
		if !a.lastSeen.Equal(b.lastSeen) {
			return a.lastSeen.Before(b.lastSeen)
		}
		return keys[i] < keys[j]
	})
	bots := 0
	for _, k := range keys[:n-limit] {
		if strings.HasPrefix(k, "BOT:") {
			bots++
		}
	}
	if ev.Time.Sub(p.srv.trimWarned) > time.Hour {
		p.srv.trimWarned = ev.Time
		p.log.Warn("more players tracked than the server has slots, the longest silent visits end", "server", p.srv.info.Name,
			"tracked", n, "max_players", limit, "ended", n-limit, "bots", bots)
	}
	for _, k := range keys[:n-limit] {
		st := p.srv.players[k]
		delete(p.srv.players, k)
		if err := p.leave(ctx, ev, st, false); err != nil {
			return err
		}
	}
	return nil
}

// join starts the visit of a player the processor has not seen yet;
// connected is the Unix time of the connect if known (RCON status), else 0.
func (p *Processor) join(ctx context.Context, ev stats.Ev, ref logparse.Player, connected int64) (*playerState, error) {
	key := ref.Key()
	uid := UniqueID(ref)
	id, skill, err := p.store.Player(ctx, ev, uid, ref.Name)
	if err != nil {
		return nil, err
	}
	st := &playerState{
		live:        stats.Live{ID: id, UniqueID: uid, Name: ref.Name, Team: ref.Team, Skill: skill, Connected: connected},
		userID:      ref.UserID,
		since:       ev.Time,
		lastSeen:    ev.Time,
		teamSince:   ev.Time,
		roleSince:   ev.Time,
		weaponKills: map[string]int{},
	}
	if st.startRank, _, err = p.store.Rank(ctx, ev.Srv.Game, skill); err != nil {
		return nil, err
	}
	if err := p.store.Join(ctx, ev, st.live); err != nil {
		return nil, err
	}
	p.srv.players[key] = st
	if _, err := p.store.ClanFromName(ctx, ev, st.live, ref.Name); err != nil {
		return nil, err
	}
	if err := p.trim(ctx, ev); err != nil {
		return nil, err
	}
	p.counted(ev.Time)
	return st, p.setActive(ctx, ev.Time)
}

// absent returns a player who is not (any more) on the server without
// starting a visit: for lines that come after the disconnect (the plugin
// flushes weaponstats on disconnect). It returns nil for untracked players.
func (p *Processor) absent(ctx context.Context, ev stats.Ev, ref logparse.Player) (*stats.Live, error) {
	if !ref.Trackable() {
		return nil, nil
	}
	uid := UniqueID(ref)
	id, skill, err := p.store.Player(ctx, ev, uid, ref.Name)
	if err != nil {
		return nil, err
	}
	return &stats.Live{ID: id, UniqueID: uid, Name: ref.Name, Team: ref.Team, Skill: skill}, nil
}

func (p *Processor) disconnect(ctx context.Context, ev stats.Ev, ref logparse.Player) error {
	key := ref.Key()
	delete(p.srv.connected, key)
	st := p.srv.players[key]
	if st == nil {
		return nil
	}
	if err := p.endStreak(ctx, ev, st); err != nil {
		return err
	}
	delete(p.srv.players, key)
	if p.srv.gone == nil {
		p.srv.gone = map[string]time.Time{}
	}
	p.srv.gone[key] = ev.Time
	p.counted(ev.Time)
	if err := p.leave(ctx, ev, st, true); err != nil {
		return err
	}
	return p.setActive(ctx, ev.Time)
}

// Tick ends the visits of players without any line for gamewe_idle_timeout
// seconds (their disconnect was lost, e.g. in a server crash), like a
// disconnect but without an hlstats_Events_Disconnects row and without a
// kill streak reward. The connection time is counted up to now.
func (p *Processor) Tick(ctx context.Context, now time.Time) error {
	if err := p.flushKill(ctx); err != nil {
		return err
	}
	if err := p.idle(ctx, now); err != nil {
		return err
	}
	if err := p.flushPlayTime(ctx, now); err != nil {
		return err
	}
	if p.srv.ready {
		if err := p.setActive(ctx, now); err != nil { // the end of a map change's hold
			return err
		}
	}
	return p.serverLoad(ctx, now)
}

// playTimeFlush is how often the time in teams and roles of the players
// on the server is stored while nothing changes (the web's teams page
// fills during a long map, not only at its end).
const playTimeFlush = time.Minute

// flushPlayTime stores the team and role time of every player whose time
// is older than playTimeFlush.
func (p *Processor) flushPlayTime(ctx context.Context, now time.Time) error {
	if !p.srv.ready {
		return nil
	}
	ev := stats.Ev{Srv: p.srv.info, MapID: p.srv.mapID, Time: now}
	for _, st := range p.srv.players {
		if now.Sub(st.teamSince) < playTimeFlush && now.Sub(st.roleSince) < playTimeFlush {
			continue
		}
		if err := p.playTime(ctx, ev, st); err != nil {
			return err
		}
	}
	return nil
}

// mapSettle is how long after a map change the player count from before
// it stands while RCON status polls arrive: the map loads, the players
// connect again, the bots are added anew, and bots of the old map that
// came back with late lines or a late status are gone after the first
// status that misses them. Without status the old bots end only
// after botIdle, so the count stands until then (mapSettleNoStatus).
const (
	mapSettle         = mapRepeat
	mapSettleNoStatus = botIdle + MonitorInterval
)

// statusFresh is how long the player count of an RCON status stands for
// the server: a few missed polls (busy server) keep it.
const statusFresh = 2 * MonitorInterval

// online is the number of players on the server at now: the players the
// last RCON status listed, if it is recent and of the current map, else the
// tracked players (one per identity, Player.Key). During a map change's
// settle time it is the count from before the change. A drop to 0 is only
// counted once the players are really gone after the map change.
//
// Status counts what the server says is on it. The tracked players can be
// more for a while: lines of the old map's bots that come after the map
// change, or after the last status, bring them back until the next status
// or botIdle ends them, next to the new bots (up to the slots).
func (p *Processor) online(now time.Time) int {
	srv := &p.srv
	if srv.heldCount > 0 {
		settle := mapSettleNoStatus
		if srv.lastStatus.After(srv.mapAt) {
			settle = mapSettle
		}
		if now.Sub(srv.mapAt) < settle {
			return srv.heldCount
		}
		srv.heldCount = 0
	}
	if srv.lastStatus.After(srv.mapAt) && now.Sub(srv.lastStatus) <= statusFresh {
		return srv.statusCount
	}
	return len(srv.players)
}

// counted updates the lowest and highest player count of the load interval.
func (p *Processor) counted(now time.Time) {
	n := p.online(now)
	p.srv.minPlay, p.srv.maxPlay = min(p.srv.minPlay, n), max(p.srv.maxPlay, n)
}

// setActive writes hlstats_Servers.act_players when the count changed.
func (p *Processor) setActive(ctx context.Context, now time.Time) error {
	n := p.online(now)
	if n == p.srv.activeCount {
		return nil
	}
	if err := p.store.SetActivePlayers(ctx, p.srv.info.ID, n); err != nil {
		return err
	}
	p.srv.activeCount = n
	return nil
}

// serverLoad writes a row of hlstats_server_load every LoadInterval while
// the server sends log lines (gamewe_server_load). CPU, FPS and the uptime
// come from RCON "stats"; without RCON the uptime is the time since the
// first line after a silence.
func (p *Processor) serverLoad(ctx context.Context, now time.Time) error {
	srv := &p.srv
	if !srv.ready || now.Sub(srv.loadSince) < LoadInterval {
		return nil
	}
	n := p.online(now)
	l := stats.Load{ServerID: srv.info.ID, Time: now.Unix(), Players: n, MinPlayers: min(srv.minPlay, n), MaxPlayers: max(srv.maxPlay, n),
		MapID: srv.mapID, Uptime: int(now.Sub(srv.upSince).Minutes())}
	active := now.Sub(srv.lastLine) <= LoadInterval || now.Sub(srv.lastStatus) <= LoadInterval
	srv.loadSince, srv.minPlay, srv.maxPlay = now, n, n
	if !p.set.Get().ServerLoad || !active {
		return nil // no lines: the server is down or not logging
	}
	if srv.info.RconPassword != "" && p.ServerStats != nil {
		if st, err := p.ServerStats(srv.info, time.Duration(p.set.Get().RconTimeout)*time.Second); err == nil {
			l.CPU, l.FPS, l.Uptime = st.CPU, st.FPS, st.Uptime
		} else {
			p.log.Debug("rcon stats", "err", err)
		}
	}
	return p.store.RecordServerLoad(ctx, l)
}

// idle ends the visits of idle players (see Tick).
func (p *Processor) idle(ctx context.Context, now time.Time) error {
	timeout := time.Duration(p.set.Get().IdleTimeout) * time.Second
	if !p.srv.ready || timeout <= 0 {
		return nil
	}
	ev := stats.Ev{Srv: p.srv.info, MapID: p.srv.mapID, Time: now}
	n := len(p.srv.players)
	for key, st := range p.srv.players {
		limit := timeout
		if stats.IsBot(st.live.UniqueID) {
			// Bots play all the time; one without a line for a while was
			// kicked (bot_quota) without a disconnect line. RCON status
			// keeps listed bots seen.
			limit = min(timeout, botIdle)
		}
		if now.Sub(st.lastSeen) <= limit {
			continue
		}
		delete(p.srv.players, key)
		p.log.Info("player idle, visit ended", "player", st.live.Name, "idle", now.Sub(st.lastSeen).Round(time.Second))
		if err := p.leave(ctx, ev, st, false); err != nil {
			return err
		}
	}
	if len(p.srv.players) == n {
		return nil
	}
	p.counted(now)
	return p.setActive(ctx, now)
}

// endStreak ends the player's kill streak: as HLstatsX, one action
// kill_streak_<n> (n kills in this life, at most MaxKillStreak) when the
// streak ends, if the game defines it.
func (p *Processor) endStreak(ctx context.Context, ev stats.Ev, st *playerState) error {
	n := min(st.killStreak, MaxKillStreak)
	st.killStreak = 0
	if n < 2 {
		return nil
	}
	return p.playerAction(ctx, ev, st, fmt.Sprintf("kill_streak_%d", n))
}

// endStreaks ends the kill streaks of all players (round end, map change).
func (p *Processor) endStreaks(ctx context.Context, ev stats.Ev) error {
	for _, st := range p.srv.players {
		st.headshotLines, st.headshotKills = 0, 0
		if err := p.endStreak(ctx, ev, st); err != nil {
			return err
		}
	}
	return nil
}

// leave ends the visit of st at ev.Time: its time in the team and role,
// then the connection time.
func (p *Processor) leave(ctx context.Context, ev stats.Ev, st *playerState, logged bool) error {
	if err := p.playTime(ctx, ev, st); err != nil {
		return err
	}
	return p.store.Leave(ctx, ev, st.live, int(ev.Time.Sub(st.since).Seconds()), logged)
}

// playTime stores the time since the last change in the player's team and
// role (not while spectator / unassigned), and starts counting anew from
// ev.Time. Called before a team or role change and when the visit ends.
func (p *Processor) playTime(ctx context.Context, ev stats.Ev, st *playerState) error {
	secs := func(since time.Time) int {
		if since.IsZero() || !ev.Time.After(since) {
			return 0
		}
		return int(ev.Time.Sub(since).Seconds())
	}
	team, ts := st.live.Team, secs(st.teamSince)
	role, rs := st.live.Role, secs(st.roleSince)
	if neutralTeam(team) { // spectator: neither team nor role time
		team, ts, role, rs = "", 0, "", 0
	}
	st.teamSince, st.roleSince = ev.Time, ev.Time
	if ts == 0 && rs == 0 {
		return nil
	}
	return p.store.AddPlayTime(ctx, ev, team, ts, role, rs)
}

func neutralTeam(t string) bool {
	switch strings.ToLower(t) {
	case "", "unassigned", "spectator", "spectators":
		return true
	}
	return false
}

func (p *Processor) kill(ctx context.Context, ev stats.Ev, e logparse.Kill) error {
	if e.Killer.Key() == e.Victim.Key() {
		st, err := p.player(ctx, ev, e.Killer)
		if st == nil || err != nil {
			return err
		}
		return p.suicide(ctx, ev, st, e.Weapon)
	}
	k, err := p.player(ctx, ev, e.Killer)
	if err != nil {
		return err
	}
	v, err := p.player(ctx, ev, e.Victim)
	if err != nil || k == nil || v == nil {
		return err // kills involving untracked players (console, pending ids) are skipped
	}
	teamkill := !neutralTeam(e.Killer.Team) && strings.EqualFold(e.Killer.Team, e.Victim.Team)
	v.dead = true
	if err := p.lineLocations(ctx, ev, k, v, e.KillerPos, e.VictimPos); err != nil {
		return err
	}
	kpos, vpos := e.KillerPos, e.VictimPos
	if kpos == nil && vpos == nil {
		// The plugin's location line (already in hlstats_Events_Locations)
		// came first: its positions go into the frag only.
		kpos, vpos = p.takeLocation(ev, e.Killer.Key(), e.Victim.Key())
	}
	// Bots that are not ranked never change anybody's skill: no points, no
	// headshot action and no kill streak for kills with them.
	noSkill := p.unrankedBot(k, v)
	if !teamkill {
		// The victim's life, and with it the kill streak, ends.
		if err := p.endStreak(ctx, ev, v); err != nil {
			return err
		}
		if !noSkill {
			k.killStreak = k.killStreak + 1
		}
		k.deathStreak = 0
		v.deathStreak = v.deathStreak + 1
	}
	headshot := e.Headshot || k.headshotLines > 0
	r, err := p.store.RecordKill(ctx, ev, stats.Kill{
		Killer: k.live, Victim: v.live, Weapon: e.Weapon, Headshot: headshot, Teamkill: teamkill,
		KillStreak: k.killStreak, DeathStreak: v.deathStreak, NoSkill: noSkill,
		KillerPos: kpos, VictimPos: vpos,
	})
	if err != nil {
		return err
	}
	k.live.Skill, v.live.Skill = r.KillerSkill, r.VictimSkill
	k.skillDelta += r.KillerDelta
	v.skillDelta += r.VictimDelta
	if teamkill {
		return nil
	}
	k.kills++
	v.deaths++
	k.weaponKills[e.Weapon]++
	k.bestKill, v.bestDeath = max(k.bestKill, k.killStreak), max(v.bestDeath, v.deathStreak)
	if !noSkill {
		// CS:GO: "(headshot penetrated ...)"; the flags other than headshot are
		// actions of the killer, if the game defines them (kill_penetrated, ...).
		for _, f := range e.Flags {
			if killFlags[f] {
				if err := p.playerAction(ctx, ev, k, "kill_"+f); err != nil {
					return err
				}
			}
		}
	}
	// As HLstatsX does: a headshot is a player action (hlstats_Actions code
	// headshot) with its own reward; kill streaks are rewarded when they end
	// (endStreak). Codes the game does not define are ignored.
	if headshot {
		k.headshots++
		if k.headshotLines > 0 { // the plugin's line came first
			k.headshotLines--
		} else {
			k.headshotKills++ // the plugin's line may follow
		}
		if noSkill {
			return nil
		}
		return p.playerAction(ctx, ev, k, "headshot")
	}
	return nil
}

// unrankedBot reports whether a kill or action between a and b involves a
// bot while bots are not ranked (gamewe_rank_bots = 0).
func (p *Processor) unrankedBot(a, b *playerState) bool {
	return !p.set.Get().RankBots && (stats.IsBot(a.live.UniqueID) || stats.IsBot(b.live.UniqueID))
}

// headshotLine handles the plugin's `triggered "headshot"`, which belongs
// to a headshot kill line (before or after it): the kill line rewards it.
func (p *Processor) headshotLine(ctx context.Context, ev stats.Ev, st *playerState) error {
	if st.headshotKills > 0 { // the kill line came first
		st.headshotKills--
		return nil
	}
	st.headshotLines++
	return nil
}

// killFlags are the kill line flags of CS:GO that are actions (kill_<flag>).
var killFlags = map[string]bool{"penetrated": true, "noscope": true, "throughsmoke": true, "attackerblind": true}

// accolade turns CS:GO's end-of-match accolade of the winner (position 1)
// into the player action accolade_<name> (accolade_mvp, accolade_3k, ...),
// if the game defines it. The line names the player only by name and
// userid: it must be on the server with both.
func (p *Processor) accolade(ctx context.Context, ev stats.Ev, e logparse.Accolade) error {
	if e.Position != 1 || !accoladeName.MatchString(e.Name) {
		return nil
	}
	for _, st := range p.srv.players {
		if st.userID == e.UserID && st.live.Name == e.Player {
			return p.playerAction(ctx, ev, st, "accolade_"+e.Name)
		}
	}
	return nil
}

var accoladeName = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)

// pendingLocation is a kill position waiting for its kill line.
type pendingLocation struct {
	killer, victim string // Player.Key()
	pos, victimPos [3]int
	at             time.Time
}

// locationMatch is how far apart the location line and its kill line may be.
const locationMatch = 2 * time.Second

// takeLocation returns the positions of the kill of killer on victim from
// the location line just before it, or nil.
func (p *Processor) takeLocation(ev stats.Ev, killer, victim string) (kpos, vpos *[3]int) {
	pl := p.srv.pendingLoc
	p.srv.pendingLoc = nil
	if pl == nil || pl.killer != killer || pl.victim != victim || ev.Time.Sub(pl.at).Abs() > locationMatch {
		return nil, nil
	}
	k, v := pl.pos, pl.victimPos
	return &k, &v
}

// located stores the kill and death positions of the plugin's location
// lines (gamewe_store_locations).
func (p *Processor) located(ctx context.Context, ev stats.Ev, e logparse.Located) error {
	if !p.set.Get().StoreLocations || (e.Event != "kill" && e.Event != "suicide") || p.srv.linePositions {
		return nil // with positions in the kill lines, the plugin's lines would count twice
	}
	st, err := p.player(ctx, ev, e.Player)
	if st == nil || err != nil {
		return err
	}
	if e.Event == "suicide" {
		return p.store.RecordLocation(ctx, ev, st.live.ID, stats.LocationDeath, e.Pos[0], e.Pos[1])
	}
	if e.Victim == nil {
		return nil
	}
	v, err := p.player(ctx, ev, *e.Victim)
	if v == nil || err != nil {
		return err
	}
	// Usually before the kill line (kept for it); a kill line already
	// written (other plugins log after it) gets the positions now.
	p.srv.pendingLoc = &pendingLocation{killer: e.Player.Key(), victim: e.Victim.Key(), pos: e.Pos, victimPos: e.VictimAt, at: ev.Time}
	if err := p.store.FragPositions(ctx, ev, st.live.ID, v.live.ID, e.Pos, e.VictimAt); err != nil {
		return err
	}
	if err := p.store.RecordLocation(ctx, ev, st.live.ID, stats.LocationKill, e.Pos[0], e.Pos[1]); err != nil {
		return err
	}
	return p.store.RecordLocation(ctx, ev, v.live.ID, stats.LocationDeath, e.VictimAt[0], e.VictimAt[1])
}

// lineLocations stores the positions of a kill or suicide line (CS:GO,
// TF2, the AMX plugin) in hlstats_Events_Locations, as the plugin's
// "located on" lines: the killer's position (type 1) and the victim's
// (type 2). killer is nil for a suicide.
func (p *Processor) lineLocations(ctx context.Context, ev stats.Ev, killer, victim *playerState, kpos, vpos *[3]int) error {
	if kpos == nil && vpos == nil {
		return nil
	}
	p.srv.linePositions = true
	if !p.set.Get().StoreLocations {
		return nil
	}
	if killer != nil && kpos != nil {
		if err := p.store.RecordLocation(ctx, ev, killer.live.ID, stats.LocationKill, kpos[0], kpos[1]); err != nil {
			return err
		}
	}
	if vpos != nil {
		return p.store.RecordLocation(ctx, ev, victim.live.ID, stats.LocationDeath, vpos[0], vpos[1])
	}
	return nil
}

func (p *Processor) suicide(ctx context.Context, ev stats.Ev, st *playerState, weapon string) error {
	if !st.lastTeamChange.IsZero() && ev.Time.Sub(st.lastTeamChange) < teamSwitchSuicide {
		return nil // the game killed the player for changing teams
	}
	st.dead = true
	if err := p.endStreak(ctx, ev, st); err != nil {
		return err
	}
	d, err := p.store.RecordSuicide(ctx, ev, st.live, weapon)
	if err != nil {
		return err
	}
	st.suicides++
	st.live.Skill += d
	st.skillDelta += d
	return nil
}

// roundWin splits the players into winners and losers (players in a
// neutral team are neither) and stores the round.
func (p *Processor) roundWin(ctx context.Context, ev stats.Ev, team, trigger string) error {
	var winners, losers []stats.Live
	var winStates []*playerState
	survivors := map[int64]bool{} // alive at the end of the round (no death since Round_Start)
	for _, st := range p.srv.players {
		switch {
		case neutralTeam(st.live.Team):
			continue
		case strings.EqualFold(st.live.Team, team):
			winners = append(winners, st.live)
			winStates = append(winStates, st)
		default:
			losers = append(losers, st.live)
		}
		if !st.dead {
			survivors[st.live.ID] = true
		}
	}
	bonus, err := p.store.RecordRoundWin(ctx, ev, team, trigger, winners, losers, survivors)
	if err != nil {
		return err
	}
	for _, st := range winStates {
		st.live.Skill += bonus
		st.skillDelta += bonus
	}
	return nil
}

func num(m map[string]string, k string) int {
	n, _ := strconv.Atoi(m[k])
	return max(n, 0)
}

// playerTrigger handles the plugin's weaponstats / weaponstats2 lines and
// other player actions. The plugin flushes weaponstats on death,
// disconnect and round end (some games also every 180 s), so every line
// is a delta since the previous one.
func (p *Processor) playerTrigger(ctx context.Context, ev stats.Ev, e logparse.PlayerTriggered) error {
	if p.srv.players[e.Player.Key()] == nil && afterLeave(e.Trigger) {
		// A line of a player who already left: no new visit.
		if !isWeaponStats(e.Trigger) {
			return nil
		}
		l, err := p.absent(ctx, ev, e.Player)
		if l == nil || err != nil {
			return err
		}
		return p.weaponStats(ctx, ev, *l, e)
	}
	st, err := p.player(ctx, ev, e.Player)
	if st == nil || err != nil {
		return err
	}
	if isWeaponStats(e.Trigger) {
		if e.Trigger == "weaponstats" {
			st.shots += num(e.Props, "shots")
			st.hits += num(e.Props, "hits")
		}
		return p.weaponStats(ctx, ev, st.live, e)
	}
	// TF2 logs "kill assist", the plugin "kill_assist" (CS:GO, Insurgency).
	if (e.Trigger == "kill assist" || e.Trigger == "kill_assist") && e.Victim != nil {
		v, err := p.player(ctx, ev, *e.Victim)
		if err != nil {
			return err
		}
		if err := p.killAssist(ctx, ev, st, v); err != nil {
			return err
		}
	}
	return p.playerEvent(ctx, ev, st, e)
}

// killAssist counts a kill assist of st on the victim v (nil if not
// tracked). The same assist logged again within a second, CS:GO's
// `assisted killing` line and the plugin's `kill_assist` line, is counted
// once. The actions of both lines are scored as before.
func (p *Processor) killAssist(ctx context.Context, ev stats.Ev, st, v *playerState) error {
	if v != nil {
		if d := ev.Time.Sub(v.assistAt); v.assistBy == st.live.ID && d <= time.Second && d >= -time.Second {
			return nil
		}
		v.assistBy, v.assistAt = st.live.ID, ev.Time
	}
	return p.store.KillAssist(ctx, ev, st.live)
}

func (p *Processor) weaponStats(ctx context.Context, ev stats.Ev, live stats.Live, e logparse.PlayerTriggered) error {
	var w stats.WeaponStats
	switch e.Trigger {
	case "weaponstats":
		w = stats.WeaponStats{
			Shots: num(e.Props, "shots"), Hits: num(e.Props, "hits"), Kills: num(e.Props, "kills"),
			Headshots: num(e.Props, "headshots"), Teamkills: num(e.Props, "tks"),
			Damage: num(e.Props, "damage"), Deaths: num(e.Props, "deaths"),
		}
	case "weaponstats2":
		w = stats.WeaponStats{
			Head: num(e.Props, "head"), Chest: num(e.Props, "chest"), Stomach: num(e.Props, "stomach"),
			LeftArm: num(e.Props, "leftarm"), RightArm: num(e.Props, "rightarm"),
			LeftLeg: num(e.Props, "leftleg"), RightLeg: num(e.Props, "rightleg"),
		}
	}
	weapon := e.Props["weapon"]
	if weapon == "" {
		return nil
	}
	return p.store.AddWeaponStats(ctx, ev, live, weapon, w)
}

func isWeaponStats(trigger string) bool { return trigger == "weaponstats" || trigger == "weaponstats2" }

// afterLeave reports whether a trigger can come after the player's
// disconnect: the plugin's weaponstats, and CS:S drops the bomb of a player
// who left. Such lines must not start a new visit.
func afterLeave(trigger string) bool {
	return isWeaponStats(trigger) || trigger == "Dropped_The_Bomb"
}

// playerEvent handles a trigger other than weaponstats: player-vs-player
// actions, the clan tag and player actions.
func (p *Processor) playerEvent(ctx context.Context, ev stats.Ev, st *playerState, e logparse.PlayerTriggered) error {
	switch {
	case e.Victim != nil:
		v, err := p.player(ctx, ev, *e.Victim)
		if v == nil || err != nil {
			return err
		}
		d, vd, err := p.store.RecordPlayerPlayerAction(ctx, ev, st.live, v.live, e.Trigger, p.unrankedBot(st, v))
		if err != nil {
			return err
		}
		st.live.Skill += d
		st.skillDelta += d
		v.live.Skill += vd
		v.skillDelta += vd
		return nil
	case e.Trigger == "headshot":
		return p.headshotLine(ctx, ev, st)
	case e.Trigger == "clantag":
		if tag := strings.TrimSpace(e.Props["value"]); tag != "" {
			_, err := p.store.ClanTag(ctx, ev, st.live, tag)
			return err
		}
		return nil
	}
	return p.playerAction(ctx, ev, st, e.Trigger)
}

// playerAction scores a player action defined in hlstats_Actions.
func (p *Processor) playerAction(ctx context.Context, ev stats.Ev, st *playerState, code string) error {
	d, err := p.store.RecordPlayerAction(ctx, ev, st.live, code)
	if err != nil {
		return err
	}
	st.live.Skill += d
	st.skillDelta += d
	return nil
}

func (p *Processor) say(ctx context.Context, ev stats.Ev, e logparse.Say, now time.Time) error {
	name := commandName(e.Text)
	storeChat := p.set.Get().StoreChat
	if name == "" && !storeChat {
		return nil
	}
	st, err := p.player(ctx, ev, e.Player)
	if st == nil || err != nil {
		return err
	}
	if storeChat {
		if err := p.store.RecordChat(ctx, ev, st.live, e.Team, e.Text); err != nil {
			return err
		}
	}
	if name == "" {
		return nil
	}
	if name == "hideranking" || name == "settings" {
		return p.cmds.SelfHide(ctx, &p.srv, st, name, e.Text, now)
	}
	return p.cmds.Run(ctx, &p.srv, st, name, now)
}
