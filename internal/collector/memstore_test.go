// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"sync"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// memStore is an in-memory Store for tests. It is safe for concurrent use
// and records the server every event was attributed to.
type memStore struct {
	mu           sync.Mutex
	cfg          settings.Settings
	servers      map[string]stats.Server
	players      map[[2]string]*memPlayer // game, unique id
	byID         map[int64]*memPlayer
	joins        []memEvent
	leaves       []memEvent
	kills        []memKill
	rounds       []memRound
	weapons      map[[2]string]stats.WeaponStats // unique id, weapon
	live         map[[2]int64]bool               // player, server
	actions      []string                        // "uid code" of recorded player actions
	ppas         []string                        // "uid code victim-uid"
	suicide      map[string]int                  // uid -> suicides
	hidden       map[string]bool                 // banned unique ids
	clans        map[int64]string                // player id -> clan tag
	starts       int                             // RoundStart calls
	playTime     map[string]int                  // "team:CT" / "role:gsg9" -> seconds
	geo          []string                        // "player:flag", "serverN:flag"
	loads        []stats.Load
	active       []int // SetActivePlayers values
	status       []string
	maps         []string // MapStarted calls
	connectTypes map[int64]int
	latency      []string         // "player:ping" samples
	teams        map[int64]string // live row team by player id
	teamWrites   int
	assists      []string        // uid of each kill assist
	fragPos      []string        // plugin positions added to frags
	connected    map[int64]int64 // player id -> Live.Connected at Join
	locations    []string        // "player:type:x,y"
	nextID       int64
}

type memPlayer struct {
	id                         int64
	game, uid, name            string
	skill, kills, deaths, wins int
	hide                       int // hideranking
}

type memEvent struct{ server, player int64 }

type memKill struct {
	server         int64
	game           string
	killer, victim string // unique ids
	weapon         string
	teamkill       bool
	headshot       bool
}

type memRound struct {
	server    int64
	team      string
	winners   []string
	survivors []string
}

func newMemStore() *memStore {
	return &memStore{
		cfg: settings.Defaults(), servers: map[string]stats.Server{},
		players: map[[2]string]*memPlayer{}, byID: map[int64]*memPlayer{},
		weapons: map[[2]string]stats.WeaponStats{}, live: map[[2]int64]bool{},
		suicide: map[string]int{}, hidden: map[string]bool{}, clans: map[int64]string{},
	}
}

func (m *memStore) id() int64 { m.nextID++; return m.nextID }

func (m *memStore) Servers(context.Context) ([]stats.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []stats.Server
	for _, s := range m.servers {
		out = append(out, s)
	}
	return out, nil
}

func (m *memStore) Options(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// addServers registers servers like rows of hlstats_Servers; games maps an
// address to its game (default css).
func (m *memStore) addServers(addrs []string, gameOf map[string]string) []stats.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []stats.Server
	for i, a := range addrs {
		game := gameOf[a]
		if game == "" {
			game = "css"
		}
		s := stats.Server{ID: m.id(), Address: a, AddrPort: netip.MustParseAddrPort(a), Name: fmt.Sprintf("srv%d", i+1),
			Game: game, GameID: len(game), RconPassword: "pw"}
		s.Base, s.Engine, _ = games.Resolve("", game)
		m.servers[a] = s
		out = append(out, s)
	}
	return out
}

func (m *memStore) ResetLive(_ context.Context, server int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.live {
		if k[1] == server {
			delete(m.live, k)
		}
	}
	return nil
}

func (m *memStore) MapID(context.Context, string, string) (int, error)              { return 1, nil }
func (m *memStore) Connect(context.Context, stats.Ev, stats.Live, netip.Addr) error { return nil }
func (m *memStore) Enter(context.Context, stats.Ev, stats.Live) error               { return nil }
func (m *memStore) ChangeTeam(_ context.Context, _ stats.Ev, p stats.Live, team string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.liveTeam(p.ID, team)
	return nil
}

func (m *memStore) SetLiveTeam(_ context.Context, _ stats.Ev, p stats.Live) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.liveTeam(p.ID, p.Team)
	m.teamWrites++
	return nil
}

// liveTeam records the live row's team; m.mu must be held.
func (m *memStore) liveTeam(id int64, team string) {
	if m.teams == nil {
		m.teams = map[int64]string{}
	}
	m.teams[id] = team
}
func (m *memStore) SetActivePlayers(_ context.Context, _ int64, n int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = append(m.active, n)
	return nil
}
func (m *memStore) RecordChat(context.Context, stats.Ev, stats.Live, bool, string) error {
	return nil
}
func (m *memStore) UpdateServerPlayers(context.Context) error { return nil }

func (m *memStore) SetLocation(_ context.Context, _ stats.Ev, p stats.Live, loc stats.Location) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.geo = append(m.geo, fmt.Sprintf("%d:%s", p.ID, loc.Flag))
	return nil
}

func (m *memStore) ServerLocation(_ context.Context, id int64, loc stats.Location) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.geo = append(m.geo, fmt.Sprintf("server%d:%s", id, loc.Flag))
	return nil
}

func (m *memStore) SetConnectType(_ context.Context, id int64, t int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connectTypes == nil {
		m.connectTypes = map[int64]int{}
	}
	m.connectTypes[id] = t
	return nil
}

func (m *memStore) MapStarted(_ context.Context, _ stats.Ev, name string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maps = append(m.maps, name)
	return 2, nil
}

func (m *memStore) ServerStatus(_ context.Context, id int64, _ string, maxPlayers int, pings map[string]int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = append(m.status, fmt.Sprintf("%d:%d:%d", id, maxPlayers, len(pings)))
	return nil
}

func (m *memStore) RecordServerLoad(_ context.Context, l stats.Load) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads = append(m.loads, l)
	return nil
}
func (m *memStore) ChangeRole(context.Context, stats.Ev, stats.Live, string) error { return nil }

// AddPlayTime sums the seconds per "team:code" and "role:code".
func (m *memStore) AddPlayTime(_ context.Context, _ stats.Ev, team string, teamSecs int, role string, roleSecs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.playTime == nil {
		m.playTime = map[string]int{}
	}
	if team != "" {
		m.playTime["team:"+team] += teamSecs
	}
	if role != "" {
		m.playTime["role:"+role] += roleSecs
	}
	return nil
}

func (m *memStore) RecordLocation(_ context.Context, _ stats.Ev, id int64, typ, x, y int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locations = append(m.locations, fmt.Sprintf("%d:%d:%d,%d", id, typ, x, y))
	return nil
}

func (m *memStore) Above(_ context.Context, game string, skill, n int) ([]stats.Player, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []stats.Player
	for _, p := range m.byID {
		if p.game == game && p.skill > skill {
			out = append(out, stats.Player{ID: p.id, Name: p.name, Skill: p.skill, Kills: p.kills, Deaths: p.deaths})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Skill < out[j].Skill || out[i].Skill == out[j].Skill && out[i].ID < out[j].ID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func (m *memStore) RecordPlayerAction(_ context.Context, _ stats.Ev, p stats.Live, code string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actions = append(m.actions, p.UniqueID+" "+code)
	return 0, nil
}

func (m *memStore) RecordPlayerPlayerAction(_ context.Context, _ stats.Ev, p, v stats.Live, code string, _ bool) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ppas = append(m.ppas, p.UniqueID+" "+code+" "+v.UniqueID)
	return 0, 0, nil
}

func (m *memStore) RecordSuicide(_ context.Context, _ stats.Ev, p stats.Live, _ string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.suicide[p.UniqueID]++
	return 0, nil
}

func (m *memStore) RoundStart(context.Context, stats.Ev) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	return nil
}

func (m *memStore) HideBanned(_ context.Context, _ stats.Ev, uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hidden[uid] = true
	return nil
}

var memClanPatterns = append(stats.ClanPatterns("[AXXXXX]", "EITHER"), stats.ClanPatterns("=AXXXXX=", "EITHER")...)

func (m *memStore) ClanFromName(_ context.Context, _ stats.Ev, p stats.Live, name string) (int64, error) {
	tag, _ := stats.MatchClan(memClanPatterns, name)
	if tag == "" {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clans[p.ID] = tag
	return 1, nil
}

func (m *memStore) ClanTag(_ context.Context, _ stats.Ev, p stats.Live, tag string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clans[p.ID] = tag
	return 1, nil
}

// snapshot returns copies of the recorded actions and per-player data.
func (m *memStore) recorded() (actions, ppas []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.actions...), append([]string(nil), m.ppas...)
}

func (m *memStore) Player(_ context.Context, ev stats.Ev, uid, name string) (int64, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{ev.Srv.Game, uid}
	if p, ok := m.players[k]; ok {
		return p.id, p.skill, nil
	}
	p := &memPlayer{id: m.id(), game: ev.Srv.Game, uid: uid, name: name, skill: m.cfg.StartSkill}
	m.players[k], m.byID[p.id] = p, p
	return p.id, p.skill, nil
}

func (m *memStore) Join(_ context.Context, ev stats.Ev, p stats.Live) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.joins = append(m.joins, memEvent{ev.Srv.ID, p.ID})
	if m.connected == nil {
		m.connected = map[int64]int64{}
	}
	m.connected[p.ID] = p.Connected
	m.liveTeam(p.ID, p.Team)
	m.live[[2]int64{p.ID, ev.Srv.ID}] = true
	return nil
}

func (m *memStore) Leave(_ context.Context, ev stats.Ev, p stats.Live, _ int, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaves = append(m.leaves, memEvent{ev.Srv.ID, p.ID})
	delete(m.live, [2]int64{p.ID, ev.Srv.ID})
	return nil
}

func (m *memStore) Rename(_ context.Context, _ stats.Ev, p stats.Live, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[p.ID].name = name
	return nil
}

func (m *memStore) RecordKill(_ context.Context, ev stats.Ev, k stats.Kill) (stats.KillResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kp, vp := m.byID[k.Killer.ID], m.byID[k.Victim.ID]
	if kp.game != ev.Srv.Game || vp.game != ev.Srv.Game {
		panic("kill between players of another game")
	}
	r := stats.ScoreKill(kp.skill, vp.skill, k.Headshot, k.Teamkill, 1, m.cfg)
	if k.NoSkill {
		r = stats.KillResult{}
	}
	kp.skill += r.KillerDelta
	vp.skill += r.VictimDelta
	r.KillerSkill, r.VictimSkill = kp.skill, vp.skill
	if !k.Teamkill {
		kp.kills++
		vp.deaths++
	}
	m.kills = append(m.kills, memKill{ev.Srv.ID, ev.Srv.Game, kp.uid, vp.uid, k.Weapon, k.Teamkill, k.Headshot})
	return r, nil
}

func (m *memStore) RecordRoundWin(_ context.Context, ev stats.Ev, team, _ string, winners, _ []stats.Live, survivors map[int64]bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := memRound{server: ev.Srv.ID, team: team}
	for id := range survivors {
		r.survivors = append(r.survivors, m.byID[id].uid)
	}
	sort.Strings(r.survivors)
	for _, w := range winners {
		r.winners = append(r.winners, w.UniqueID)
		m.byID[w.ID].wins++
	}
	sort.Strings(r.winners)
	m.rounds = append(m.rounds, r)
	return 0, nil
}

func (m *memStore) AddWeaponStats(_ context.Context, _ stats.Ev, p stats.Live, weapon string, w stats.WeaponStats) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{p.UniqueID, weapon}
	t := m.weapons[k]
	t.Shots += w.Shots
	t.Damage += w.Damage
	t.Hits += w.Hits
	t.Chest += w.Chest
	t.LeftArm += w.LeftArm
	m.weapons[k] = t
	return nil
}

func (m *memStore) PlayerStats(_ context.Context, id int64) (stats.Player, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return stats.Player{}, stats.ErrNotFound
	}
	return stats.Player{ID: p.id, Name: p.name, Skill: p.skill, Kills: p.kills, Deaths: p.deaths}, nil
}

func (m *memStore) Rank(_ context.Context, game string, skill int) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rank, total := 1, 0
	for _, p := range m.byID {
		if p.game == game {
			total++
			if p.skill > skill {
				rank++
			}
		}
	}
	return rank, total, nil
}

func (m *memStore) Next(_ context.Context, game string, skill int) (stats.Player, error) {
	return stats.Player{}, stats.ErrNotFound
}

func (m *memStore) Top(_ context.Context, game string, n int) ([]stats.Player, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []stats.Player
	for _, p := range m.byID {
		if p.game == game {
			out = append(out, stats.Player{ID: p.id, Name: p.name, Skill: p.skill, Kills: p.kills, Deaths: p.deaths})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Skill > out[j].Skill || out[i].Skill == out[j].Skill && out[i].ID < out[j].ID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// recorder is a Replier that records which server each command went to.
type recorder struct {
	mu   sync.Mutex
	sent map[string][]string // rcon address -> commands
}

func (r *recorder) Send(srv stats.Server, cmd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string][]string{}
	}
	r.sent[srv.Address] = append(r.sent[srv.Address], cmd)
}

func (r *recorder) get(addr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent[addr]...)
}

func (m *memStore) RecordLatency(_ context.Context, _ stats.Ev, p stats.Live, ping int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latency = append(m.latency, fmt.Sprintf("%d:%d", p.ID, ping))
	return nil
}

func (m *memStore) KillAssist(_ context.Context, _ stats.Ev, p stats.Live) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assists = append(m.assists, p.UniqueID)
	return nil
}

func (m *memStore) FragPositions(_ context.Context, _ stats.Ev, killerID, victimID int64, kpos, vpos [3]int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fragPos = append(m.fragPos, fmt.Sprintf("%d>%d %v %v", killerID, victimID, kpos, vpos))
	return nil
}

func (m *memStore) HideRanking(_ context.Context, id int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id].hide, nil
}

func (m *memStore) SetHideRanking(_ context.Context, id int64, from, to int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.byID[id]; p.hide == from {
		p.hide = to
	}
	return m.byID[id].hide, nil
}

func (m *memStore) RecordWorldDeath(_ context.Context, _ stats.Ev, p stats.Live, weapon string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actions = append(m.actions, m.byID[p.ID].uid+" death:"+weapon)
	return nil
}
