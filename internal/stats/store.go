// SPDX-License-Identifier: GPL-3.0-or-later

// Package stats writes game events into the HLstatsX (gameME style)
// database, MariaDB / MySQL or PostgreSQL, and answers the queries behind
// the in-game commands. The schemas are internal/schema/mysql/schema.sql
// and internal/schema/postgres/schema.sql.
//
// SQL is written once with ? placeholders; the few dialect differences go
// through ignore, upsert, insertID and skillSet. The store uses no
// transactions (the MariaDB tables were MyISAM, without any, until schema
// version 19), so every counter is changed with relative updates
// (x = x + ?), which are atomic per statement in both databases and let
// several servers write concurrently.
package stats

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

type Store struct {
	db  *database.DB
	set *settings.Live

	mu    sync.Mutex
	cache map[string]cached

	clanMu sync.Mutex // one creator of hlstats_Clans rows at a time (no unique key)

	// ResetClock is the time of day (minutes after midnight) of the
	// scheduled ranking resets (features.scheduled_resets.time); set it
	// before use.
	ResetClock int

	// HistoryZone is the time zone of the days of hlstats_Players_History
	// (the daily awards' zone, features.daily_awards.timezone); nil: UTC.
	// Set it before use.
	HistoryZone *time.Location

	// NoJobOrigin: hlstats_gamewe_jobs lacks the columns origin and
	// origin_detail (a gamewe command on a database before schema version
	// 22); jobs are then queued and read without them.
	NoJobOrigin bool
}

type cached struct {
	val any
	at  time.Time
}

// cacheTTL bounds how long ids and settings from the web admin (weapon
// modifiers, action rewards) are cached.
const cacheTTL = 10 * time.Minute

func NewStore(db *database.DB, set *settings.Live) *Store {
	return &Store{db: db, set: set, cache: map[string]cached{}}
}

// Server is a row of hlstats_Servers with its game.
type Server struct {
	ID           int64
	Address      string         // address:port as stored
	AddrPort     netip.AddrPort // parsed; zero if Err is set
	Name         string
	Game         string // hlstats_Games.code
	GameID       int
	Base         string       // the base game (hlstats_Games.realgame, or guessed): css, cstrike, tf, ...
	Engine       games.Engine // from Base
	Warning      string       // realgame was guessed (logged once)
	Map          string       // act_map
	MaxPlayers   int          // max_players (from RCON status or A2S_INFO), 0 if unknown
	RconPassword string       // empty: no in-game command replies
	// LogSecret is sv_logsecret (features.log_secret), set by hand in the
	// CLI or the web admin; empty: packets are not checked.
	LogSecret string
	Info      ServerInfo // server_version, server_os, server_vac as stored
	Err       error      // why the row cannot be used
}

// ServerInfo is what the game server says about itself (A2S_INFO, RCON
// status; features.server_info): hlstats_Servers.server_version, server_os
// ("linux", "windows", "mac") and server_vac ("secure", "insecure"). An
// empty field is unknown.
type ServerInfo struct {
	Version, OS, VAC string
	// MaxPlayers is the slot count of an A2S_INFO answer, 0 if unknown. It
	// is not one of the info columns: the collector stores it in
	// hlstats_Servers.max_players (see Processor.ServerInfo).
	MaxPlayers int
}

// Or fills the empty fields of i from o.
func (i ServerInfo) Or(o ServerInfo) ServerInfo {
	if i.Version == "" {
		i.Version = o.Version
	}
	if i.OS == "" {
		i.OS = o.OS
	}
	if i.VAC == "" {
		i.VAC = o.VAC
	}
	return i
}

// Ev is the context of one event: server, current map and time.
type Ev struct {
	Srv   Server
	MapID int
	Time  time.Time
}

func (e Ev) ts() int64 { return e.Time.Unix() }

// Live is what the collector knows about a player on a server.
type Live struct {
	ID       int64
	UniqueID string
	Name     string
	Team     string
	Role     string // hlstats_Roles.code (player model, class), "" if unknown
	Skill    int    // last known skill
	// Connected is when the player connected (Unix time) if it is known
	// before the collector saw the player (RCON status); 0 = the event time.
	Connected int64
}

// clip bounds s to n characters of valid UTF-8 for a text column. Bytes
// that are no UTF-8 (a chat line or name in an old code page such as
// Windows-1251) become U+FFFD: MariaDB refuses them in utf8mb4 (error 1366),
// PostgreSQL in UTF8, and the whole event would be lost.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type step struct {
	q    string
	args []any
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.db.Q(q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.db.Q(q), args...)
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.db.Q(q), args...)
}

// ignore turns "INSERT INTO ..." into an insert that skips rows whose key
// already exists.
func (s *Store) ignore(insert string) string {
	if s.db.Postgres() {
		return insert + " ON CONFLICT DO NOTHING"
	}
	return strings.Replace(insert, "INSERT INTO", "INSERT IGNORE INTO", 1)
}

// set is one column of an upsert: expr may use {old} (the stored value) and
// {new} (the value of the insert).
type set struct{ col, expr string }

// upsert adds "update these columns if the key exists" to an insert into
// table. key lists the columns of the unique key (needed by PostgreSQL).
func (s *Store) upsert(insert, table, key string, sets ...set) string {
	parts := make([]string, len(sets))
	for i, st := range sets {
		old, new := st.col, "VALUES("+st.col+")"
		if s.db.Postgres() {
			old, new = table+"."+st.col, "EXCLUDED."+st.col
		}
		parts[i] = st.col + " = " + strings.NewReplacer("{old}", old, "{new}", new).Replace(st.expr)
	}
	if s.db.Postgres() {
		return insert + " ON CONFLICT (" + key + ") DO UPDATE SET " + strings.Join(parts, ", ")
	}
	return insert + " ON DUPLICATE KEY UPDATE " + strings.Join(parts, ", ")
}

// insertID runs an insert and returns the generated id of column idCol.
func (s *Store) insertID(ctx context.Context, q, idCol string, args ...any) (int64, error) {
	if s.db.Postgres() {
		var id int64
		err := s.queryRow(ctx, q+" RETURNING "+idCol, args...).Scan(&id)
		return id, err
	}
	res, err := s.exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) run(ctx context.Context, steps ...step) error {
	for _, st := range steps {
		if _, err := s.exec(ctx, st.q, st.args...); err != nil {
			f := strings.Fields(st.q)
			return fmt.Errorf("%s: %w", strings.Join(f[:min(4, len(f))], " "), err)
		}
	}
	return nil
}

func (s *Store) cached(key string, load func() (any, error)) (any, error) {
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(c.at) < cacheTTL {
		return c.val, nil
	}
	v, err := load()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = cached{v, time.Now()}
	s.mu.Unlock()
	return v, nil
}

// skillSet adds a delta to the skill column without going below zero (in
// MariaDB the column is unsigned, so the sum is computed as signed).
func (s *Store) skillSet() string {
	if s.db.Postgres() {
		return `skill = GREATEST(skill + ?, 0)`
	}
	return `skill = GREATEST(CAST(skill AS SIGNED) + ?, 0)`
}

// ---- reference rows ----

// Servers returns all rows of hlstats_Servers with their game. Rows whose
// address is not an IP or whose game is not in hlstats_Games are returned
// with Err set, so the caller can report them.
func (s *Store) Servers(ctx context.Context) ([]Server, error) {
	rcon := "''"
	if ok, err := s.hasColumn(ctx, "hlstats_Servers", "rcon_password"); err != nil {
		return nil, err
	} else if ok {
		rcon = "s.rcon_password"
	}
	secret := "''"
	if ok, err := s.hasColumn(ctx, "hlstats_Servers", "log_secret"); err != nil {
		return nil, err
	} else if ok {
		secret = "s.log_secret"
	}
	info := "'', '', ''"
	if ok, err := s.hasColumn(ctx, "hlstats_Servers", "server_version"); err != nil {
		return nil, err
	} else if ok {
		info = "s.server_version, s.server_os, s.server_vac"
	}
	realgame := "''"
	if ok, err := s.hasColumn(ctx, "hlstats_Games", "realgame"); err != nil {
		return nil, err
	} else if ok {
		realgame = "COALESCE(g.realgame, '')"
	}
	rows, err := s.query(ctx, `SELECT s.serverId, s.address, s.port, s.name, s.game, s.act_map, s.max_players, COALESCE(g.id, 0), `+rcon+`, `+secret+`, `+realgame+`, `+info+`
		FROM hlstats_Servers s LEFT JOIN hlstats_Games g ON g.code = s.game ORDER BY s.serverId`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		var srv Server
		var host, realgame string
		var port int
		if err := rows.Scan(&srv.ID, &host, &port, &srv.Name, &srv.Game, &srv.Map, &srv.MaxPlayers, &srv.GameID, &srv.RconPassword,
			&srv.LogSecret, &realgame, &srv.Info.Version, &srv.Info.OS, &srv.Info.VAC); err != nil {
			return nil, err
		}
		srv.Base, srv.Engine, srv.Warning = games.Resolve(realgame, srv.Game)
		srv.Address = net.JoinHostPort(host, strconv.Itoa(port))
		ip, perr := netip.ParseAddr(strings.TrimSpace(host))
		switch {
		case perr != nil:
			srv.Err = fmt.Errorf("address %q is not an IP address", host)
		case port < 1 || port > 65535:
			srv.Err = fmt.Errorf("port %d is invalid", port)
		case srv.GameID == 0:
			srv.Err = fmt.Errorf("game %q is not in hlstats_Games", srv.Game)
		default:
			srv.AddrPort = netip.AddrPortFrom(ip.Unmap(), uint16(port))
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// EnsureServer adds a row to hlstats_Servers if none exists for ap. Only
// 'gamewe simulate' uses it: the collector itself never creates servers,
// they are registered in the web admin. It reports whether a row was added.
func (s *Store) EnsureServer(ctx context.Context, ap netip.AddrPort, game, name string) (bool, error) {
	var gameID int
	if err := s.queryRow(ctx, `SELECT id FROM hlstats_Games WHERE code = ?`, game).Scan(&gameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("game %q is not in hlstats_Games", game)
		}
		return false, err
	}
	var id int64
	err := s.queryRow(ctx, `SELECT serverId FROM hlstats_Servers WHERE address = ? AND port = ?`,
		ap.Addr().String(), ap.Port()).Scan(&id)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = s.exec(ctx, `INSERT INTO hlstats_Servers (address, port, name, game) VALUES (?, ?, ?, ?)`,
		ap.Addr().String(), ap.Port(), clip(name, 255), game)
	return err == nil, err
}

func (s *Store) hasColumn(ctx context.Context, table, column string) (bool, error) {
	var n int
	schema := "DATABASE()"
	if s.db.Postgres() {
		schema = "current_schema()"
	}
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = `+schema+` AND LOWER(table_name) = LOWER(?) AND LOWER(column_name) = LOWER(?)`, s.db.Prefix.Table(table), column).Scan(&n)
	return n > 0, err
}

// Options returns the collector's keys (gamewe_*) from hlstats_Options.
func (s *Store) Options(ctx context.Context) (map[string]string, error) {
	rows, err := s.query(ctx, `SELECT keyname, value FROM hlstats_Options WHERE keyname LIKE 'gamewe!_%' ESCAPE '!'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ResetLive removes the server's rows from hlstats_Livestats (at start and
// stop) and sets act_players and act_bots to 0.
func (s *Store) ResetLive(ctx context.Context, serverID int64) error {
	return s.run(ctx,
		step{`DELETE FROM hlstats_Livestats WHERE server_id = ?`, []any{serverID}},
		step{`UPDATE hlstats_Servers SET act_players = 0, act_bots = 0 WHERE serverId = ?`, []any{serverID}})
}

// refID looks up, and creates if missing, a (game, code) row of
// hlstats_Maps, hlstats_Teams or hlstats_Roles.
func (s *Store) refID(ctx context.Context, table, idCol, codeCol, game, code string) (int, error) {
	v, err := s.cached(table+"|"+game+"|"+code, func() (any, error) {
		q := fmt.Sprintf(`SELECT %s FROM %s WHERE game = ? AND %s = ?`, idCol, table, codeCol)
		var id int
		err := s.queryRow(ctx, q, game, code).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			ins, args := fmt.Sprintf(`INSERT INTO %s (game, %s) VALUES (?, ?)`, table, codeCol), []any{game, code}
			if table == "hlstats_Teams" || table == "hlstats_Roles" {
				ins, args = fmt.Sprintf(`INSERT INTO %s (game, code, name) VALUES (?, ?, ?)`, table), []any{game, code, code}
			}
			ins = s.ignore(ins)
			if _, err := s.exec(ctx, ins, args...); err != nil {
				return nil, err
			}
			err = s.queryRow(ctx, q, game, code).Scan(&id)
		}
		return id, err
	})
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", table, code, err)
	}
	return v.(int), nil
}

// MapID returns the hlstats_Maps id of a map, creating the row if needed.
func (s *Store) MapID(ctx context.Context, game, name string) (int, error) {
	if name == "" {
		return 0, nil
	}
	return s.refID(ctx, "hlstats_Maps", "id", "map", game, clip(name, 96))
}

type weapon struct {
	id       int
	modifier float64
}

// weapon returns id and skill modifier; unknown weapons are added with
// modifier 1.00 (the column default).
func (s *Store) weapon(ctx context.Context, game, code string) (weapon, error) {
	code = clip(code, 128)
	v, err := s.cached("hlstats_Weapons|"+game+"|"+code, func() (any, error) {
		var w weapon
		q := `SELECT weaponId, modifier FROM hlstats_Weapons WHERE game = ? AND code = ?`
		err := s.queryRow(ctx, q, game, code).Scan(&w.id, &w.modifier)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := s.exec(ctx, s.ignore(`INSERT INTO hlstats_Weapons (game, code, name) VALUES (?, ?, ?)`), game, code, code); err != nil {
				return nil, err
			}
			err = s.queryRow(ctx, q, game, code).Scan(&w.id, &w.modifier)
		}
		return w, err
	})
	if err != nil {
		return weapon{}, fmt.Errorf("hlstats_Weapons %q: %w", code, err)
	}
	return v.(weapon), nil
}

// Action is a row of hlstats_Actions; rewards are maintained in the admin.
type Action struct {
	ID              int
	RewardPlayer    int
	RewardTeam      int
	ForPlayer       bool
	ForPlayerPlayer bool
	ForTeam         bool
}

// action looks up an action; ok is false if the game has none with that code.
func (s *Store) action(ctx context.Context, game, code string) (Action, bool, error) {
	v, err := s.cached("hlstats_Actions|"+game+"|"+code, func() (any, error) {
		var a Action
		var fp, fpp, ft string
		err := s.queryRow(ctx,
			`SELECT id, reward_player, reward_team, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions
			FROM hlstats_Actions WHERE game = ? AND code = ?`,
			game, code).Scan(&a.ID, &a.RewardPlayer, &a.RewardTeam, &fp, &fpp, &ft)
		if errors.Is(err, sql.ErrNoRows) {
			return Action{}, nil
		}
		a.ForPlayer, a.ForPlayerPlayer, a.ForTeam = fp == "1", fpp == "1", ft == "1"
		return a, err
	})
	if err != nil {
		return Action{}, false, fmt.Errorf("hlstats_Actions %q: %w", code, err)
	}
	a := v.(Action)
	return a, a.ID != 0, nil
}

// nameUse counts one use of a player name (hlstats_PlayerNames).
func (s *Store) nameUse() string {
	return s.upsert(`INSERT INTO hlstats_PlayerNames (playerId, name, lastuse, numuses) VALUES (?, ?, ?, 1)`,
		"hlstats_PlayerNames", "playerId, name", set{"lastuse", "{new}"}, set{"numuses", "{old} + 1"})
}

// ---- players ----

// Player returns the player id and skill for a unique id in the server's
// game, creating the player if needed. Players that were moved to
// hlstats_PlayersInactive are moved back.
func (s *Store) Player(ctx context.Context, ev Ev, uniqueID, name string) (id int64, skill int, err error) {
	game := ev.Srv.Game
	err = s.queryRow(ctx, `SELECT playerId FROM hlstats_PlayerUniqueIds WHERE uniqueId = ? AND game = ?`,
		uniqueID, game).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return s.newPlayer(ctx, ev, uniqueID, name)
	}
	if err != nil {
		return 0, 0, err
	}
	err = s.queryRow(ctx, `SELECT skill FROM hlstats_Players WHERE playerId = ?`, id).Scan(&skill)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, skill, err
	}
	// Known id but no active row: reactivate it, or recreate it.
	if err := s.reactivate(ctx, id); err != nil {
		return 0, 0, err
	}
	if err := s.run(ctx,
		step{s.ignore(`INSERT INTO hlstats_Players (playerId, game, game_id, skill, last_event, hideranking) VALUES (?, ?, ?, ?, ?, ?)`),
			[]any{id, game, ev.Srv.GameID, s.set.Get().StartSkill, ev.ts(), boolInt(IsBot(uniqueID) && !s.set.Get().RankBots(game))}},
	); err != nil {
		return 0, 0, err
	}
	err = s.queryRow(ctx, `SELECT skill FROM hlstats_Players WHERE playerId = ?`, id).Scan(&skill)
	return id, skill, err
}

// IsBot reports whether a uniqueId is a bot's (BOT:<name>).
func IsBot(uniqueID string) bool { return strings.HasPrefix(uniqueID, "BOT:") }

func (s *Store) newPlayer(ctx context.Context, ev Ev, uniqueID, name string) (int64, int, error) {
	hide := 0
	if IsBot(uniqueID) && !s.set.Get().RankBots(ev.Srv.Game) {
		hide = 1 // bots are recorded but not ranked (gamewe_rank_bots_<game>)
	}
	id, err := s.insertID(ctx, `INSERT INTO hlstats_Players (game, game_id, skill, last_event, hideranking) VALUES (?, ?, ?, ?, ?)`, "playerId",
		ev.Srv.Game, ev.Srv.GameID, s.set.Get().StartSkill, ev.ts(), hide)
	if err != nil {
		return 0, 0, fmt.Errorf("insert player: %w", err)
	}
	res, err := s.exec(ctx, s.ignore(`INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game) VALUES (?, ?, ?)`),
		id, clip(uniqueID, 64), ev.Srv.Game)
	if err != nil {
		return 0, 0, fmt.Errorf("insert unique id: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Another server created the same player at the same moment.
		if err := s.run(ctx, step{`DELETE FROM hlstats_Players WHERE playerId = ?`, []any{id}}); err != nil {
			return 0, 0, err
		}
		return s.Player(ctx, ev, uniqueID, name)
	}
	err = s.run(ctx, step{s.ignore(`INSERT INTO hlstats_Players_Data (playerId, first_connect, last_connect, lastName) VALUES (?, ?, ?, ?)`),
		[]any{id, ev.ts(), ev.ts(), clip(name, 128)}})
	return id, s.set.Get().StartSkill, err
}

// Join records that a tracked player is on the server: the name use and
// the live view, and last_event. It runs when the collector first sees the
// player. A row moved to hlstats_PlayersInactive between Player and the
// live row (features.inactive_players) is moved back.
func (s *Store) Join(ctx context.Context, ev Ev, p Live) error {
	if err := s.join(ctx, ev, p); err != nil {
		return err
	}
	// After the live row: the move skips players in hlstats_Livestats, and
	// on PostgreSQL this waits for a move of the row in progress.
	seen := step{`UPDATE hlstats_Players SET last_event = GREATEST(last_event, ?) WHERE playerId = ?`, []any{ev.ts(), p.ID}}
	res, err := s.exec(ctx, seen.q, seen.args...)
	if err != nil {
		return fmt.Errorf("UPDATE hlstats_Players last_event: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	// No row changed: moved meanwhile, or (MariaDB) last_event was already
	// this time. Moving back is a no-op then.
	return s.run(ctx, append(s.reactivateSteps(p.ID), seen)...)
}

// reactivate moves a player from hlstats_PlayersInactive back to
// hlstats_Players (all columns, unchanged); a row in hlstats_Players wins.
func (s *Store) reactivate(ctx context.Context, id int64) error {
	return s.run(ctx, s.reactivateSteps(id)...)
}

func (s *Store) reactivateSteps(id int64) []step {
	return []step{
		{s.ignore(`INSERT INTO hlstats_Players SELECT * FROM hlstats_PlayersInactive WHERE playerId = ?`), []any{id}},
		{`DELETE FROM hlstats_PlayersInactive WHERE playerId = ?`, []any{id}},
	}
}

func (s *Store) join(ctx context.Context, ev Ev, p Live) error {
	return s.run(ctx,
		step{s.nameUse(),
			[]any{p.ID, clip(p.Name, 128), ev.Time.UTC()}},
		// A new live row (with zero counters), replacing an older one of the
		// player in one statement: two servers may join the same player at once.
		step{s.upsert(`INSERT INTO hlstats_Livestats (player_id, server_id, unique_id, name, team, connected, skill) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"hlstats_Livestats", "player_id", liveReset...),
			[]any{p.ID, ev.Srv.ID, clip(p.UniqueID, 32), clip(p.Name, 128), clip(p.Team, 64), cmp.Or(p.Connected, ev.ts()), max(p.Skill, 0)}})
}

// liveReset sets an existing hlstats_Livestats row to what a new row would
// be (as REPLACE did): the inserted values, all other columns their default.
var liveReset = func() []set {
	sets := []set{}
	for _, c := range []string{"server_id", "unique_id", "name", "team", "connected", "skill"} {
		sets = append(sets, set{c, "{new}"})
	}
	for _, c := range []string{"cli_address_id", "cli_location_id", "kills", "deaths", "suicides", "headshots",
		"shots", "hits", "is_dead", "has_bomb", "ping", "skill_change", "hpk", "kpd", "kill_assists",
		"kill_assists_medic", "kills_assisted", "rounds", "survived_count", "survived", "kill_streak",
		"death_streak", "wins", "losses", "custom_wins", "death_headshots", "team_kills", "team_killed",
		"death_hpk", "player_hidden", "points_healed", "flags_captured"} {
		sets = append(sets, set{c, "0"})
	}
	return append(sets, set{"squad", "''"})
}()

// Connect stores a connect event. ip is stored only if it is IPv4 (the
// column is an integer); pass the zero Addr to store none.
func (s *Store) Connect(ctx context.Context, ev Ev, p Live, ip netip.Addr) error {
	var ipInt uint32
	if ip.Is4() {
		b := ip.As4()
		ipInt = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	}
	return s.run(ctx,
		step{`INSERT INTO hlstats_Events_Connects (timestamp_id, game_id, map, playerId, ipAddress, hostname, hostgroup) VALUES (?, ?, ?, ?, ?, '', '')`,
			[]any{ev.ts(), ev.Srv.GameID, ev.MapID, p.ID, ipInt}},
		step{`UPDATE hlstats_Players_Data SET last_connect = ?, total_connects = total_connects + 1, lastName = ?,
			first_connect = CASE WHEN first_connect = 0 THEN ? ELSE first_connect END WHERE playerId = ?`,
			[]any{ev.ts(), clip(p.Name, 128), ev.ts(), p.ID}},
		step{`UPDATE hlstats_Players SET last_event = ? WHERE playerId = ?`, []any{ev.ts(), p.ID}})
}

// Enter stores the "entered the game" event.
func (s *Store) Enter(ctx context.Context, ev Ev, p Live) error {
	return s.run(ctx, s.ev(ctx, step{`INSERT INTO hlstats_Events_Entries (timestamp_id, game_id, server_id, map, playerId) VALUES (?, ?, ?, ?, ?)`,
		[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID}}))
}

// Leave ends a player's stay on the server: connection time and live
// view. logged is false when the collector shuts down (no disconnect line).
func (s *Store) Leave(ctx context.Context, ev Ev, p Live, seconds int, logged bool) error {
	seconds = max(seconds, 0)
	steps := []step{
		{`UPDATE hlstats_Players SET connection_time = connection_time + ?, last_event = ? WHERE playerId = ?`, []any{seconds, ev.ts(), p.ID}},
		{`UPDATE hlstats_PlayerNames SET connection_time = connection_time + ? WHERE playerId = ? AND name = ?`, []any{seconds, p.ID, clip(p.Name, 128)}},
		{`DELETE FROM hlstats_Livestats WHERE player_id = ? AND server_id = ?`, []any{p.ID, ev.Srv.ID}},
	}
	if logged {
		steps = append(steps, s.ev(ctx, step{`INSERT INTO hlstats_Events_Disconnects (timestamp_id, game_id, server_id, map, playerId) VALUES (?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID}}))
	}
	if err := s.run(ctx, steps...); err != nil {
		return err
	}
	return s.history(ctx, ev, p, hist{conn: seconds})
}

// Rename stores a name change.
func (s *Store) Rename(ctx context.Context, ev Ev, p Live, newName string) error {
	newName = clip(newName, 128)
	return s.run(ctx,
		step{`INSERT INTO hlstats_Events_ChangeName (timestamp_id, game_id, map, playerId, oldName, newName) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.MapID, p.ID, clip(p.Name, 255), newName}},
		step{`UPDATE hlstats_Players_Data SET lastName = ? WHERE playerId = ?`, []any{newName, p.ID}},
		step{s.nameUse(),
			[]any{p.ID, newName, ev.Time.UTC()}},
		// Not REPLACE: that would reset the visit's live counters.
		step{`UPDATE hlstats_Livestats SET name = ? WHERE player_id = ? AND server_id = ?`, []any{newName, p.ID, ev.Srv.ID}},
	)
}

// SetLiveTeam sets the team of the player's live row (hlstats_Livestats)
// without a team change event: the team of the player tag of any log line.
func (s *Store) SetLiveTeam(ctx context.Context, ev Ev, p Live) error {
	return s.run(ctx, step{`UPDATE hlstats_Livestats SET team = ? WHERE player_id = ? AND server_id = ?`, []any{clip(p.Team, 64), p.ID, ev.Srv.ID}})
}

// ChangeTeam stores a team change.
func (s *Store) ChangeTeam(ctx context.Context, ev Ev, p Live, team string) error {
	tid, err := s.refID(ctx, "hlstats_Teams", "teamId", "code", ev.Srv.Game, clip(team, 128))
	if err != nil {
		return err
	}
	return s.run(ctx,
		s.ev(ctx, step{`INSERT INTO hlstats_Events_ChangeTeam (timestamp_id, game_id, server_id, map, playerId, team) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID, min(tid, 255)}}),
		step{`UPDATE hlstats_Livestats SET team = ? WHERE player_id = ? AND server_id = ?`, []any{clip(team, 64), p.ID, ev.Srv.ID}})
}

// ChangeRole stores a role change (CS:S player model, TF2 class).
func (s *Store) ChangeRole(ctx context.Context, ev Ev, p Live, role string) error {
	rid, err := s.refID(ctx, "hlstats_Roles", "roleId", "code", ev.Srv.Game, clip(role, 128))
	if err != nil {
		return err
	}
	return s.run(ctx, step{`INSERT INTO hlstats_Events_ChangeRole (timestamp_id, game_id, map, playerId, role) VALUES (?, ?, ?, ?, ?)`,
		[]any{ev.ts(), ev.Srv.GameID, ev.MapID, p.ID, min(rid, 255)}})
}

// AddPlayTime adds the seconds a player spent in a team and in a role to
// hlstats_Teams.time_played and hlstats_Roles.time_played (the web's time
// and share per team and role). An empty code or no seconds is skipped.
func (s *Store) AddPlayTime(ctx context.Context, ev Ev, team string, teamSecs int, role string, roleSecs int) error {
	var steps []step
	if team != "" && teamSecs > 0 {
		tid, err := s.refID(ctx, "hlstats_Teams", "teamId", "code", ev.Srv.Game, clip(team, 128))
		if err != nil {
			return err
		}
		steps = append(steps, step{`UPDATE hlstats_Teams SET time_played = time_played + ? WHERE teamId = ?`, []any{teamSecs, tid}})
	}
	if role != "" && roleSecs > 0 {
		rid, err := s.refID(ctx, "hlstats_Roles", "roleId", "code", ev.Srv.Game, clip(role, 128))
		if err != nil {
			return err
		}
		steps = append(steps, step{`UPDATE hlstats_Roles SET time_played = time_played + ? WHERE roleId = ?`, []any{roleSecs, rid}})
	}
	return s.run(ctx, steps...)
}

// Location types in hlstats_Events_Locations.
const (
	LocationKill  = 1 // where the killer stood
	LocationDeath = 2 // where the victim (or the player who killed himself) died
)

// RecordLocation stores a position of the plugin's location lines: day is
// days since 1970-01-01 (UTC), map is hlstats_Maps.id, loc_1 and loc_2 are
// the x and y map coordinates.
func (s *Store) RecordLocation(ctx context.Context, ev Ev, playerID int64, typ, x, y int) error {
	clamp := func(v int) int { return max(min(v, 32767), -32768) }
	return s.run(ctx, step{`INSERT INTO hlstats_Events_Locations (day, map, type, playerId, loc_1, loc_2) VALUES (?, ?, ?, ?, ?, ?)`,
		[]any{ev.Time.Unix() / 86400, min(ev.MapID, 65535), typ, playerID, clamp(x), clamp(y)}})
}

// ---- kills and scoring ----

// Kill describes one kill between two tracked players. The streaks are
// the current ones after this kill, tracked by the collector.
type Kill struct {
	Killer, Victim     Live
	Weapon             string
	Headshot, Teamkill bool
	// Positions of the kill line (nil: not logged), for
	// hlstats_Events_Frags.pos_* (heatmaps).
	KillerPos, VictimPos *[3]int
	KillStreak           int  // killer's kills in a row
	DeathStreak          int  // victim's deaths in a row
	NoSkill              bool // no skill change (a kill with a bot that is not ranked)
	// OneSided: only the killer gains (Left 4 Dead kills of the AI, which
	// is not ranked), see ScoreOneSidedKill.
	OneSided bool
	// NoKillCount: the kill (of a bot, gamewe_bot_kills_<game> = 0) is
	// stored, but not counted in the killer's kills and headshots
	// (hlstats_Players, hlstats_PlayerNames, history): not for the kill
	// ranking, K/D and the kill-based ranks.
	NoKillCount bool
	// NoDeathCount: the death (by a bot, gamewe_bot_stats_<game> = 0) is
	// not counted in the victim's deaths and death headshots.
	NoDeathCount bool
	// NoCareerKill: the kill (of a bot, gamewe_bot_kills_<game> = 0) does
	// not count for the killer's career rank (hlstats_Players.career_kills).
	NoCareerKill bool
}

// fragPositions reports whether hlstats_Events_Frags has the position
// columns (older databases lack them); checked once.
// eventServerID reports whether the event tables have server_id (older
// databases lack it); checked once.
func (s *Store) eventServerID(ctx context.Context) bool {
	v, err := s.cached("hlstats_Events_Frags.server_id", func() (any, error) {
		return s.hasColumn(ctx, "hlstats_Events_Frags", "server_id")
	})
	return err == nil && v.(bool)
}

// ev takes an event INSERT written with server_id as the third column
// (timestamp_id, game_id, server_id, ...) and its arguments; on a database
// without the column it leaves server_id out.
func (s *Store) ev(ctx context.Context, st step) step {
	if s.eventServerID(ctx) {
		return st
	}
	q := strings.Replace(st.q, "game_id, server_id, ", "game_id, ", 1)
	q = strings.Replace(q, "VALUES (?, ", "VALUES (", 1)
	args := append(append([]any{}, st.args[:2]...), st.args[3:]...)
	return step{q, args}
}

func (s *Store) fragPositions(ctx context.Context) bool {
	v, err := s.cached("hlstats_Events_Frags.pos_x", func() (any, error) {
		return s.hasColumn(ctx, "hlstats_Events_Frags", "pos_x")
	})
	return err == nil && v.(bool)
}

// posArgs are the three coordinates of p, or NULL.
func posArgs(p *[3]int) []any {
	if p == nil {
		return []any{nil, nil, nil}
	}
	return []any{p[0], p[1], p[2]}
}

func (s *Store) fragInsert(ctx context.Context, ev Ev, k Kill, weapon, hs int) step {
	args := []any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, k.Killer.ID, k.Victim.ID, weapon, hs}
	if !s.fragPositions(ctx) {
		return s.ev(ctx, step{`INSERT INTO hlstats_Events_Frags (timestamp_id, game_id, server_id, map, killerId, victimId, weapon, headshot) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, args})
	}
	args = append(append(args, posArgs(k.KillerPos)...), posArgs(k.VictimPos)...)
	return s.ev(ctx, step{`INSERT INTO hlstats_Events_Frags (timestamp_id, game_id, server_id, map, killerId, victimId, weapon, headshot,
		pos_x, pos_y, pos_z, pos_victim_x, pos_victim_y, pos_victim_z) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args})
}

// FragPositions adds the positions of the SourceMod plugin's "located on
// kill" line to the frag it belongs to (same killer, victim and second,
// still without positions).
func (s *Store) FragPositions(ctx context.Context, ev Ev, killerID, victimID int64, kpos, vpos [3]int) error {
	if !s.fragPositions(ctx) {
		return nil
	}
	return s.run(ctx, step{`UPDATE hlstats_Events_Frags SET pos_x = ?, pos_y = ?, pos_z = ?, pos_victim_x = ?, pos_victim_y = ?, pos_victim_z = ?
		WHERE timestamp_id = ? AND killerId = ? AND victimId = ? AND pos_x IS NULL`,
		[]any{kpos[0], kpos[1], kpos[2], vpos[0], vpos[1], vpos[2], ev.ts(), killerID, victimID}})
}

func (s *Store) skill(ctx context.Context, id int64) (int, error) {
	var sk int
	if err := s.queryRow(ctx, `SELECT skill FROM hlstats_Players WHERE playerId = ?`, id).Scan(&sk); err != nil {
		return 0, fmt.Errorf("skill of player %d: %w", id, err)
	}
	return sk, nil
}

// RecordKill scores a kill and stores it.
func (s *Store) RecordKill(ctx context.Context, ev Ev, k Kill) (KillResult, error) {
	w, err := s.weapon(ctx, ev.Srv.Game, k.Weapon)
	if err != nil {
		return KillResult{}, err
	}
	ksk, err := s.skill(ctx, k.Killer.ID)
	if err != nil {
		return KillResult{}, err
	}
	vsk, err := s.skill(ctx, k.Victim.ID)
	if err != nil {
		return KillResult{}, err
	}
	r := ScoreKill(ksk, vsk, k.Headshot, k.Teamkill, w.modifier, s.set.Get())
	if k.OneSided {
		r = ScoreOneSidedKill(ksk, k.Headshot, w.modifier, s.set.Get())
	}
	if k.NoSkill {
		r = KillResult{}
	}
	r.KillerSkill, r.VictimSkill = ksk+r.KillerDelta, vsk+r.VictimDelta
	hs := boolInt(k.Headshot)
	ks, ds := min(k.KillStreak, 65535), min(k.DeathStreak, 255)
	counted, died, career := boolInt(!k.NoKillCount), boolInt(!k.NoDeathCount), boolInt(!k.NoCareerKill)
	var steps []step
	if k.Teamkill {
		steps = []step{
			{`UPDATE hlstats_Players SET team_kills = team_kills + 1, ` + s.skillSet() + `, last_skill_change = ?, last_event = ? WHERE playerId = ?`,
				[]any{r.KillerDelta, r.KillerDelta, ev.ts(), k.Killer.ID}},
			{`UPDATE hlstats_Players SET team_killed = team_killed + 1, last_event = ? WHERE playerId = ?`, []any{ev.ts(), k.Victim.ID}},
			s.ev(ctx, step{`INSERT INTO hlstats_Events_Teamkills (timestamp_id, game_id, server_id, map, killerId, victimId, weapon) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, k.Killer.ID, k.Victim.ID, w.id}}),
			{`UPDATE hlstats_Livestats SET team_kills = team_kills + 1, ` + s.skillSet() + `, skill_change = skill_change + ? WHERE player_id = ? AND server_id = ?`,
				[]any{r.KillerDelta, r.KillerDelta, k.Killer.ID, ev.Srv.ID}},
			{`UPDATE hlstats_Livestats SET team_killed = team_killed + 1, is_dead = 1 WHERE player_id = ? AND server_id = ?`, []any{k.Victim.ID, ev.Srv.ID}},
		}
	} else {
		// The ratios come first and use the old counts plus this kill:
		// MariaDB evaluates SET from left to right with the new values,
		// PostgreSQL always with the old ones, so this is right in both.
		killerStep := step{`UPDATE hlstats_Players SET hpk = (headshots + ?) * 1.0 / (kills + 1),
				kpd = (kills + 1) * 1.0 / CASE WHEN deaths > 0 THEN deaths ELSE 1 END,
				kills = kills + 1, headshots = headshots + ?, career_kills = career_kills + ?, ` + s.skillSet() + `, last_skill_change = ?,
				kill_streak = GREATEST(kill_streak, ?), last_event = ? WHERE playerId = ?`,
			[]any{hs, hs, career, r.KillerDelta, r.KillerDelta, ks, ev.ts(), k.Killer.ID}}
		if k.NoKillCount {
			killerStep = step{`UPDATE hlstats_Players SET career_kills = career_kills + ?, ` + s.skillSet() + `, last_skill_change = ?,
				last_event = ? WHERE playerId = ?`,
				[]any{career, r.KillerDelta, r.KillerDelta, ev.ts(), k.Killer.ID}}
		}
		victimStep := step{`UPDATE hlstats_Players SET kpd = kills * 1.0 / (deaths + 1), death_hpk = (death_headshots + ?) * 1.0 / (deaths + 1),
				deaths = deaths + 1, death_headshots = death_headshots + ?, ` + s.skillSet() + `, last_skill_change = ?,
				death_streak = GREATEST(death_streak, ?), last_event = ? WHERE playerId = ?`,
			[]any{hs, hs, r.VictimDelta, r.VictimDelta, ds, ev.ts(), k.Victim.ID}}
		if k.NoDeathCount {
			victimStep = step{`UPDATE hlstats_Players SET ` + s.skillSet() + `, last_skill_change = ?, last_event = ? WHERE playerId = ?`,
				[]any{r.VictimDelta, r.VictimDelta, ev.ts(), k.Victim.ID}}
		}
		steps = []step{
			// (the ratios of the killer: killerStep) and use the old counts plus this kill:
			// MariaDB evaluates SET from left to right with the new values,
			// PostgreSQL always with the old ones, so this is right in both.
			killerStep,
			victimStep,
			s.fragInsert(ctx, ev, k, w.id, hs),
			{`UPDATE hlstats_Servers SET kills = kills + 1, headshots = headshots + ? WHERE serverId = ?`, []any{hs, ev.Srv.ID}},
			{`UPDATE hlstats_Weapons SET kills = kills + 1, headshots = headshots + ? WHERE weaponId = ?`, []any{hs, w.id}},
			{`UPDATE hlstats_PlayerNames SET kills = kills + ?, headshots = headshots + ? WHERE playerId = ? AND name = ?`,
				[]any{counted, hs * counted, k.Killer.ID, clip(k.Killer.Name, 128)}},
			{`UPDATE hlstats_PlayerNames SET deaths = deaths + ? WHERE playerId = ? AND name = ?`, []any{died, k.Victim.ID, clip(k.Victim.Name, 128)}},
			{`UPDATE hlstats_Livestats SET kills = kills + ?, headshots = headshots + ?, ` + s.skillSet() + `, skill_change = skill_change + ?,
				kill_streak = GREATEST(kill_streak, ?) WHERE player_id = ? AND server_id = ?`,
				[]any{counted, hs * counted, r.KillerDelta, r.KillerDelta, ks * counted, k.Killer.ID, ev.Srv.ID}},
			{`UPDATE hlstats_Livestats SET deaths = deaths + ?, death_headshots = death_headshots + ?, ` + s.skillSet() + `, skill_change = skill_change + ?,
				death_streak = GREATEST(death_streak, ?), is_dead = 1, has_bomb = 0 WHERE player_id = ? AND server_id = ?`,
				[]any{died, hs * died, r.VictimDelta, r.VictimDelta, ds * died, k.Victim.ID, ev.Srv.ID}},
		}
	}
	if !k.Teamkill {
		if k.Killer.Team != "" {
			steps = append(steps, step{`UPDATE hlstats_Teams SET kills = kills + 1, headshots = headshots + ? WHERE game = ? AND code = ?`,
				[]any{hs, ev.Srv.Game, clip(k.Killer.Team, 128)}})
		}
		if k.Victim.Team != "" {
			steps = append(steps, step{`UPDATE hlstats_Teams SET deaths = deaths + 1 WHERE game = ? AND code = ?`,
				[]any{ev.Srv.Game, clip(k.Victim.Team, 128)}})
		}
		if k.Killer.Role != "" {
			steps = append(steps, step{`UPDATE hlstats_Roles SET kills = kills + 1, headshots = headshots + ? WHERE game = ? AND code = ?`,
				[]any{hs, ev.Srv.Game, clip(k.Killer.Role, 128)}})
		}
		if k.Victim.Role != "" {
			steps = append(steps, step{`UPDATE hlstats_Roles SET deaths = deaths + 1 WHERE game = ? AND code = ?`,
				[]any{ev.Srv.Game, clip(k.Victim.Role, 128)}})
		}
	}
	if ev.MapID > 0 {
		normal := boolInt(!k.Teamkill)
		steps = append(steps, step{`UPDATE hlstats_Maps SET kills = kills + ?, headshots = headshots + ?, teamkills = teamkills + ? WHERE id = ?`,
			[]any{normal, hs * normal, boolInt(k.Teamkill), ev.MapID}})
	}
	if err := s.run(ctx, steps...); err != nil {
		return KillResult{}, err
	}
	killer, victim := k.Killer, k.Victim
	killer.Skill, victim.Skill = r.KillerSkill, r.VictimSkill
	if k.Teamkill {
		return r, s.history(ctx, ev, killer, hist{teamkills: 1, skillChange: r.KillerDelta})
	}
	if err := s.history(ctx, ev, killer, hist{kills: counted, headshots: hs * counted, skillChange: r.KillerDelta, killStreak: ks * counted}); err != nil {
		return r, err
	}
	return r, s.history(ctx, ev, victim, hist{deaths: died, skillChange: r.VictimDelta, deathStreak: ds * died})
}

// RecordSuicide costs the player suicide_penalty skill (not below zero)
// and returns the change.
func (s *Store) RecordSuicide(ctx context.Context, ev Ev, p Live, weaponCode string) (int, error) {
	w, err := s.weapon(ctx, ev.Srv.Game, weaponCode)
	if err != nil {
		return 0, err
	}
	sk, err := s.skill(ctx, p.ID)
	if err != nil {
		return 0, err
	}
	d := max(sk-s.set.Get().SuicidePenalty, 0) - sk
	if err := s.run(ctx,
		step{`UPDATE hlstats_Players SET suicides = suicides + 1, ` + s.skillSet() + `, last_skill_change = ?, last_event = ? WHERE playerId = ?`,
			[]any{d, d, ev.ts(), p.ID}},
		s.ev(ctx, step{`INSERT INTO hlstats_Events_Suicides (timestamp_id, game_id, server_id, map, playerId, weapon) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID, w.id}}),
		step{`UPDATE hlstats_Servers SET suicides = suicides + 1 WHERE serverId = ?`, []any{ev.Srv.ID}},
		step{`UPDATE hlstats_PlayerNames SET suicides = suicides + 1 WHERE playerId = ? AND name = ?`, []any{p.ID, clip(p.Name, 128)}},
		step{`UPDATE hlstats_Livestats SET suicides = suicides + 1, ` + s.skillSet() + `, skill_change = skill_change + ?, is_dead = 1, has_bomb = 0
			WHERE player_id = ? AND server_id = ?`, []any{d, d, p.ID, ev.Srv.ID}},
	); err != nil {
		return 0, err
	}
	p.Skill = sk + d
	return d, s.history(ctx, ev, p, hist{suicides: 1, skillChange: d})
}

// RecordWorldDeath stores a death without a killer (CS:GO "was
// killed by the bomb"): a death of the player, no suicide and no skill
// change; the weapon row (planted_c4) is created if missing.
func (s *Store) RecordWorldDeath(ctx context.Context, ev Ev, p Live, weaponCode string) error {
	if _, err := s.weapon(ctx, ev.Srv.Game, weaponCode); err != nil {
		return err
	}
	if err := s.run(ctx,
		step{`UPDATE hlstats_Players SET kpd = kills * 1.0 / (deaths + 1), deaths = deaths + 1, last_event = ? WHERE playerId = ?`, []any{ev.ts(), p.ID}},
		step{`UPDATE hlstats_PlayerNames SET deaths = deaths + 1 WHERE playerId = ? AND name = ?`, []any{p.ID, clip(p.Name, 128)}},
		step{`UPDATE hlstats_Livestats SET deaths = deaths + 1, is_dead = 1, has_bomb = 0 WHERE player_id = ? AND server_id = ?`, []any{p.ID, ev.Srv.ID}},
	); err != nil {
		return err
	}
	return s.history(ctx, ev, p, hist{deaths: 1})
}

// RecordPlayerAction scores a player action (e.g. Planted_The_Bomb) with
// reward_player from hlstats_Actions and returns the skill change. Actions
// that are not defined for the game are not stored.
func (s *Store) RecordPlayerAction(ctx context.Context, ev Ev, p Live, code string) (int, error) {
	switch code {
	case "Planted_The_Bomb":
		if err := s.run(ctx, step{`UPDATE hlstats_Servers SET bombs_planted = bombs_planted + 1 WHERE serverId = ?`, []any{ev.Srv.ID}}); err != nil {
			return 0, err
		}
	case "Defused_The_Bomb":
		if err := s.run(ctx, step{`UPDATE hlstats_Servers SET bombs_defused = bombs_defused + 1 WHERE serverId = ?`, []any{ev.Srv.ID}}); err != nil {
			return 0, err
		}
	}
	// The bomb carrier in the live view (CS:S, CS:GO).
	if code == "Got_The_Bomb" || code == "Dropped_The_Bomb" || code == "Planted_The_Bomb" {
		if err := s.run(ctx, step{`UPDATE hlstats_Livestats SET has_bomb = ? WHERE player_id = ? AND server_id = ?`,
			[]any{boolInt(code == "Got_The_Bomb"), p.ID, ev.Srv.ID}}); err != nil {
			return 0, err
		}
	}
	a, ok, err := s.action(ctx, ev.Srv.Game, code)
	if err != nil || !ok || !a.ForPlayer {
		return 0, err
	}
	bonus := max(min(a.RewardPlayer, 32767), -32768) // Events_PlayerActions.bonus is a smallint
	if err := s.run(ctx,
		step{`UPDATE hlstats_Players SET ` + s.skillSet() + `, last_skill_change = ?, last_event = ? WHERE playerId = ?`, []any{bonus, bonus, ev.ts(), p.ID}},
		s.ev(ctx, step{`INSERT INTO hlstats_Events_PlayerActions (timestamp_id, game_id, server_id, map, playerId, actionId, bonus) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID, a.ID, bonus}}),
		step{s.upsert(`INSERT INTO hlstats_Players_Actions (player_id, yearweek_id, game_id, action_id, count, points) VALUES (?, ?, ?, ?, 1, ?)`,
			"hlstats_Players_Actions", "player_id, yearweek_id, game_id, action_id", set{"count", "{old} + 1"}, set{"points", "{old} + {new}"}),
			[]any{p.ID, YearWeek(ev.Time), ev.Srv.GameID, a.ID, bonus}},
		step{`UPDATE hlstats_Actions SET count = count + 1 WHERE id = ?`, []any{a.ID}},
		step{`UPDATE hlstats_Livestats SET ` + s.skillSet() + `, skill_change = skill_change + ? WHERE player_id = ? AND server_id = ?`,
			[]any{bonus, bonus, p.ID, ev.Srv.ID}},
	); err != nil {
		return 0, err
	}
	p.Skill += bonus
	return bonus, s.history(ctx, ev, p, hist{skillChange: bonus})
}

// RecordPlayerPlayerAction scores an action of one player against another
// (e.g. domination, revenge): as in HLstatsX the player gets reward_player
// of the action (for_PlayerPlayerActions = '1') and the victim loses it (not
// below zero). The event is stored with the victim. It returns both skill
// changes; with noSkill nobody's skill changes. Undefined actions are not stored.
func (s *Store) RecordPlayerPlayerAction(ctx context.Context, ev Ev, p, victim Live, code string, noSkill bool) (int, int, error) {
	a, ok, err := s.action(ctx, ev.Srv.Game, code)
	if err != nil || !ok || !a.ForPlayerPlayer {
		return 0, 0, err
	}
	bonus := max(min(a.RewardPlayer, 32767), -32768) // Events_PlayerPlayerActions.bonus is a smallint
	if noSkill {
		bonus = 0 // against or by a bot that is not ranked
	}
	vsk, err := s.skill(ctx, victim.ID)
	if err != nil {
		return 0, 0, err
	}
	vd := max(vsk-bonus, 0) - vsk
	if err := s.run(ctx,
		step{`UPDATE hlstats_Players SET ` + s.skillSet() + `, last_skill_change = ?, last_event = ? WHERE playerId = ?`, []any{bonus, bonus, ev.ts(), p.ID}},
		step{`UPDATE hlstats_Players SET ` + s.skillSet() + `, last_skill_change = ?, last_event = ? WHERE playerId = ?`, []any{vd, vd, ev.ts(), victim.ID}},
		s.ev(ctx, step{`INSERT INTO hlstats_Events_PlayerPlayerActions (timestamp_id, game_id, server_id, map, playerId, victimId, actionId, bonus) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID, victim.ID, a.ID, bonus}}),
		step{s.upsert(`INSERT INTO hlstats_Players_Actions (player_id, yearweek_id, game_id, action_id, count, points) VALUES (?, ?, ?, ?, 1, ?)`,
			"hlstats_Players_Actions", "player_id, yearweek_id, game_id, action_id", set{"count", "{old} + 1"}, set{"points", "{old} + {new}"}),
			[]any{p.ID, YearWeek(ev.Time), ev.Srv.GameID, a.ID, bonus}},
		step{`UPDATE hlstats_Actions SET count = count + 1 WHERE id = ?`, []any{a.ID}},
		step{`UPDATE hlstats_Livestats SET ` + s.skillSet() + `, skill_change = skill_change + ? WHERE player_id = ? AND server_id = ?`,
			[]any{bonus, bonus, p.ID, ev.Srv.ID}},
		step{`UPDATE hlstats_Livestats SET ` + s.skillSet() + `, skill_change = skill_change + ? WHERE player_id = ? AND server_id = ?`,
			[]any{vd, vd, victim.ID, ev.Srv.ID}},
	); err != nil {
		return 0, 0, err
	}
	p.Skill += bonus
	victim.Skill = vsk + vd
	if err := s.history(ctx, ev, p, hist{skillChange: bonus}); err != nil {
		return 0, 0, err
	}
	return bonus, vd, s.history(ctx, ev, victim, hist{skillChange: vd})
}

// RecordRoundWin counts the round, wins and losses and gives the winners
// reward_team of the trigger's team action in hlstats_Actions. It returns
// the bonus each winner got.
//
// survivors are the players alive at the end of the round: their
// survived_count goes up, and survived (players, live view, history) is
// survived_count / rounds in percent.
func (s *Store) RecordRoundWin(ctx context.Context, ev Ev, team, trigger string, winners, losers []Live, survivors map[int64]bool) (int, error) {
	a, ok, err := s.action(ctx, ev.Srv.Game, trigger)
	if err != nil {
		return 0, err
	}
	bonus := 0
	steps := []step{}
	if ok && a.ForTeam {
		bonus = max(min(a.RewardTeam, 127), -128) // Events_TeamBonuses.bonus is a tinyint
		steps = append(steps, step{`UPDATE hlstats_Actions SET count = count + 1 WHERE id = ?`, []any{a.ID}})
	}
	ct, ts := 0, 0
	switch strings.ToUpper(team) {
	case "CT":
		ct = 1
	case "TERRORIST":
		ts = 1
	}
	steps = append(steps, step{`UPDATE hlstats_Servers SET rounds = rounds + 1, map_rounds = map_rounds + 1,
		ct_wins = ct_wins + ?, ts_wins = ts_wins + ?, map_ct_wins = map_ct_wins + ?, map_ts_wins = map_ts_wins + ? WHERE serverId = ?`,
		[]any{ct, ts, ct, ts, ev.Srv.ID}})
	if ev.MapID > 0 {
		steps = append(steps, step{`UPDATE hlstats_Maps SET rounds = rounds + 1 WHERE id = ?`, []any{ev.MapID}})
	}
	for _, p := range winners {
		steps = append(steps,
			step{`UPDATE hlstats_Players SET wins = wins + 1, rounds = rounds + 1, ` + s.skillSet() + ` WHERE playerId = ?`, []any{bonus, p.ID}},
			step{`UPDATE hlstats_Livestats SET wins = wins + 1, rounds = rounds + 1, ` + s.skillSet() + `, skill_change = skill_change + ?
				WHERE player_id = ? AND server_id = ?`, []any{bonus, bonus, p.ID, ev.Srv.ID}})
		if bonus != 0 {
			steps = append(steps, s.ev(ctx, step{`INSERT INTO hlstats_Events_TeamBonuses (timestamp_id, game_id, server_id, map, playerId, actionId, bonus) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID, a.ID, bonus}}))
		}
	}
	for _, p := range losers {
		steps = append(steps,
			step{`UPDATE hlstats_Players SET losses = losses + 1, rounds = rounds + 1 WHERE playerId = ?`, []any{p.ID}},
			step{`UPDATE hlstats_Livestats SET losses = losses + 1, rounds = rounds + 1 WHERE player_id = ? AND server_id = ?`, []any{p.ID, ev.Srv.ID}})
	}
	steps = append(steps, teamRoleRounds(ev, winners, losers, survivors)...)
	pct := `LEAST(100.0 * survived_count / CASE WHEN rounds > 0 THEN rounds ELSE 1 END, 100)`
	for _, p := range append(append([]Live{}, winners...), losers...) {
		if survivors[p.ID] {
			steps = append(steps,
				step{`UPDATE hlstats_Players SET survived_count = LEAST(survived_count + 1, 65535) WHERE playerId = ?`, []any{p.ID}},
				step{`UPDATE hlstats_Livestats SET survived_count = LEAST(survived_count + 1, 65535) WHERE player_id = ? AND server_id = ?`, []any{p.ID, ev.Srv.ID}})
		}
		steps = append(steps,
			step{`UPDATE hlstats_Players SET survived = ` + pct + ` WHERE playerId = ?`, []any{p.ID}},
			step{`UPDATE hlstats_Livestats SET survived = ` + pct + ` WHERE player_id = ? AND server_id = ?`, []any{p.ID, ev.Srv.ID}})
	}
	if err := s.run(ctx, steps...); err != nil {
		return 0, err
	}
	survived := func(p Live) int { return boolInt(survivors[p.ID]) }
	for _, p := range winners {
		p.Skill += bonus
		if err := s.history(ctx, ev, p, hist{rounds: 1, wins: 1, skillChange: bonus, survived: survived(p)}); err != nil {
			return 0, err
		}
	}
	for _, p := range losers {
		if err := s.history(ctx, ev, p, hist{rounds: 1, losses: 1, survived: survived(p)}); err != nil {
			return 0, err
		}
	}
	var hsteps []step
	for _, p := range append(append([]Live{}, winners...), losers...) {
		hsteps = append(hsteps, step{`UPDATE hlstats_Players_History SET survived = ` + pct + ` WHERE playerId = ? AND eventTime = ? AND game = ?`,
			[]any{p.ID, s.historyDay(ev.Time), ev.Srv.Game}})
	}
	if err := s.run(ctx, hsteps...); err != nil {
		return 0, err
	}
	return bonus, nil
}

// roundCount is what one round adds to a team or role.
type roundCount struct{ rounds, wins, losses, survived int }

// teamRoleRounds counts a won round in hlstats_Teams (rounds, wins or
// losses once per team, survived per surviving player) and hlstats_Roles
// (rounds, wins, losses, survived per player in the role).
func teamRoleRounds(ev Ev, winners, losers []Live, survivors map[int64]bool) []step {
	teams, roles := map[string]*roundCount{}, map[string]*roundCount{}
	get := func(m map[string]*roundCount, code string) *roundCount {
		c := m[code]
		if c == nil {
			c = &roundCount{}
			m[code] = c
		}
		return c
	}
	add := func(list []Live, win bool) {
		for _, p := range list {
			sv := boolInt(survivors[p.ID])
			if p.Team != "" {
				t := get(teams, clip(p.Team, 128))
				t.rounds, t.survived = 1, t.survived+sv
				if win {
					t.wins = 1
				} else {
					t.losses = 1
				}
			}
			if p.Role != "" {
				r := get(roles, clip(p.Role, 128))
				r.rounds, r.survived = r.rounds+1, r.survived+sv
				if win {
					r.wins++
				} else {
					r.losses++
				}
			}
		}
	}
	add(winners, true)
	add(losers, false)
	var steps []step
	for _, t := range []struct {
		table string
		m     map[string]*roundCount
	}{{"hlstats_Teams", teams}, {"hlstats_Roles", roles}} {
		codes := make([]string, 0, len(t.m))
		for c := range t.m {
			codes = append(codes, c)
		}
		sort.Strings(codes) // a fixed order of the updates
		for _, c := range codes {
			n := t.m[c]
			steps = append(steps, step{`UPDATE ` + t.table + ` SET rounds = rounds + ?, wins = wins + ?, losses = losses + ?, survived = survived + ?
				WHERE game = ? AND code = ?`, []any{n.rounds, n.wins, n.losses, n.survived, ev.Srv.Game, c}})
		}
	}
	return steps
}

// WeaponStats are the counters of the plugin's weaponstats and weaponstats2
// lines (deltas since the previous line), or of the attacked lines of CS2.
// Missing values are zero. Neck: CS2 only.
type WeaponStats struct {
	Shots, Hits, Kills, Headshots, Teamkills, Damage, Deaths         int
	Head, Neck, Chest, Stomach, LeftArm, RightArm, LeftLeg, RightLeg int
}

// AddWeaponStats adds the counters to this week's row in
// hlstats_Players_Weapons, and shots and hits also to the player, the
// player's week on the current map (hlstats_Players_Maps, for the accuracy
// per map), the live view and the server's team totals.
// hlstats_Players_Weapons has no teamkill column, so tks is not stored.
func (s *Store) AddWeaponStats(ctx context.Context, ev Ev, p Live, weaponCode string, x WeaponStats) error {
	w, err := s.weapon(ctx, ev.Srv.Game, weaponCode)
	if err != nil {
		return err
	}
	// smallint unsigned columns are capped instead of failing in strict mode.
	sets := []set{{"shots", "{old} + {new}"}, {"damage", "{old} + {new}"}}
	for _, c := range []string{"hits", "headshots", "kills", "deaths", "head", "neck", "chest", "stomach", "leftarm", "rightarm", "leftleg", "rightleg"} {
		sets = append(sets, set{c, "LEAST({old} + {new}, 65535)"})
	}
	steps := []step{{s.upsert(`INSERT INTO hlstats_Players_Weapons (player_id, yearweek_id, game_id, weapon_id, shots, hits, headshots, damage, kills, deaths,
		head, neck, chest, stomach, leftarm, rightarm, leftleg, rightleg)
		VALUES (?, ?, ?, ?, ?, LEAST(?, 65535), LEAST(?, 65535), ?, LEAST(?, 65535), LEAST(?, 65535),
		LEAST(?, 65535), LEAST(?, 65535), LEAST(?, 65535), LEAST(?, 65535), LEAST(?, 65535), LEAST(?, 65535), LEAST(?, 65535), LEAST(?, 65535))`,
		"hlstats_Players_Weapons", "player_id, yearweek_id, game_id, weapon_id", sets...),
		[]any{p.ID, YearWeek(ev.Time), ev.Srv.GameID, w.id, x.Shots, x.Hits, x.Headshots, x.Damage, x.Kills, x.Deaths,
			x.Head, x.Neck, x.Chest, x.Stomach, x.LeftArm, x.RightArm, x.LeftLeg, x.RightLeg}}}
	if x.Shots != 0 || x.Hits != 0 {
		steps = append(steps,
			step{`UPDATE hlstats_Players SET shots = shots + ?, hits = hits + ? WHERE playerId = ?`, []any{x.Shots, x.Hits, p.ID}},
			step{`UPDATE hlstats_PlayerNames SET shots = shots + ?, hits = hits + ? WHERE playerId = ? AND name = ?`,
				[]any{x.Shots, x.Hits, p.ID, clip(p.Name, 128)}},
			step{`UPDATE hlstats_Livestats SET shots = LEAST(shots + ?, 65535), hits = LEAST(hits + ?, 65535) WHERE player_id = ? AND server_id = ?`,
				[]any{x.Shots, x.Hits, p.ID, ev.Srv.ID}})
		// map_id is smallint unsigned in the MariaDB schema; without a
		// known map (or beyond that range) there is no row to add to.
		if ev.MapID > 0 && ev.MapID <= 65535 {
			steps = append(steps, step{s.upsert(`INSERT INTO hlstats_Players_Maps (player_id, yearweek_id, game_id, map_id, shots, hits)
				VALUES (?, ?, ?, ?, ?, LEAST(?, 65535))`,
				"hlstats_Players_Maps", "player_id, yearweek_id, game_id, map_id",
				set{"shots", "{old} + {new}"}, set{"hits", "LEAST({old} + {new}, 65535)"}),
				[]any{p.ID, YearWeek(ev.Time), ev.Srv.GameID, ev.MapID, x.Shots, x.Hits}})
		}
		switch strings.ToUpper(p.Team) {
		case "CT":
			steps = append(steps, step{`UPDATE hlstats_Servers SET ct_shots = ct_shots + ?, ct_hits = ct_hits + ?,
				map_ct_shots = map_ct_shots + ?, map_ct_hits = map_ct_hits + ? WHERE serverId = ?`, []any{x.Shots, x.Hits, x.Shots, x.Hits, ev.Srv.ID}})
		case "TERRORIST":
			steps = append(steps, step{`UPDATE hlstats_Servers SET ts_shots = ts_shots + ?, ts_hits = ts_hits + ?,
				map_ts_shots = map_ts_shots + ?, map_ts_hits = map_ts_hits + ? WHERE serverId = ?`, []any{x.Shots, x.Hits, x.Shots, x.Hits, ev.Srv.ID}})
		}
	}
	if err := s.run(ctx, steps...); err != nil {
		return err
	}
	if x.Shots == 0 && x.Hits == 0 && x.Damage == 0 {
		return nil
	}
	return s.history(ctx, ev, p, hist{shots: x.Shots, hits: x.Hits, damage: x.Damage})
}

// MapStarted records a map change on the server and returns the map id.
func (s *Store) MapStarted(ctx context.Context, ev Ev, name string) (int, error) {
	id, err := s.MapID(ctx, ev.Srv.Game, name)
	if err != nil {
		return 0, err
	}
	steps := []step{{`UPDATE hlstats_Servers SET act_map = ?, map_started = ?, map_changes = map_changes + 1, map_rounds = 0,
		map_ct_wins = 0, map_ts_wins = 0, map_ct_shots = 0, map_ct_hits = 0, map_ts_shots = 0, map_ts_hits = 0 WHERE serverId = ?`,
		[]any{clip(name, 255), ev.ts(), ev.Srv.ID}}}
	if id > 0 {
		steps = append(steps, step{`UPDATE hlstats_Maps SET played = played + 1 WHERE id = ?`, []any{id}})
	}
	return id, s.run(ctx, steps...)
}

// SetActivePlayers updates hlstats_Servers.act_players (players and bots)
// and act_bots (the bots among them).
func (s *Store) SetActivePlayers(ctx context.Context, serverID int64, n, bots int) error {
	return s.run(ctx, step{`UPDATE hlstats_Servers SET act_players = ?, act_bots = ? WHERE serverId = ?`, []any{n, bots, serverID}})
}

// RecordChat stores a chat line (only with gamewe_store_chat). message_mode
// is 1 for say and 2 for say_team.
func (s *Store) RecordChat(ctx context.Context, ev Ev, p Live, team bool, msg string) error {
	return s.run(ctx, step{`INSERT INTO hlstats_Events_Chat (timestamp_id, game_id, server_id, playerId, message_mode, message) VALUES (?, ?, ?, ?, ?, ?)`,
		[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, p.ID, 1 + boolInt(team), clip(msg, 4096)}})
}

type hist struct {
	kills, deaths, suicides, headshots, teamkills, shots, hits, conn, skillChange int
	rounds, wins, losses, killStreak, deathStreak, damage, killAssists            int
	survived                                                                      int // survived rounds
}

// KillAssist counts a kill assist (CS:GO "assisted killing", TF2 "kill
// assist"): hlstats_Players.kill_assists, the live row and the history.
func (s *Store) KillAssist(ctx context.Context, ev Ev, p Live) error {
	if err := s.run(ctx,
		step{`UPDATE hlstats_Players SET kill_assists = kill_assists + 1 WHERE playerId = ?`, []any{p.ID}},
		step{`UPDATE hlstats_Livestats SET kill_assists = LEAST(kill_assists + 1, 65535) WHERE player_id = ? AND server_id = ?`, []any{p.ID, ev.Srv.ID}},
	); err != nil {
		return err
	}
	return s.history(ctx, ev, p, hist{killAssists: 1})
}

// historyDay is the day of t in hlstats_Players_History: the calendar day
// in HistoryZone, so that it is the day of the daily awards and of the web.
func (s *Store) historyDay(t time.Time) string {
	if s.HistoryZone == nil {
		return t.UTC().Format("2006-01-02")
	}
	return t.In(s.HistoryZone).Format("2006-01-02")
}

// history adds to the player's row of today in hlstats_Players_History.
// kill_streak and death_streak are the best of the day.
func (s *Store) history(ctx context.Context, ev Ev, p Live, h hist) error {
	return s.run(ctx, step{s.upsert(`INSERT INTO hlstats_Players_History (playerId, eventTime, game, kills, deaths, suicides, headshots, team_kills,
		shots, hits, connection_time, skill, skill_change, rounds, wins, losses, kill_streak, death_streak, damage, kill_assists, survived_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"hlstats_Players_History", "playerId, eventTime, game",
		set{"kills", "LEAST({old} + {new}, 65535)"}, set{"deaths", "LEAST({old} + {new}, 65535)"},
		set{"suicides", "LEAST({old} + {new}, 65535)"}, set{"headshots", "LEAST({old} + {new}, 65535)"},
		set{"team_kills", "LEAST({old} + {new}, 65535)"}, set{"shots", "LEAST({old} + {new}, 16777215)"},
		set{"hits", "LEAST({old} + {new}, 16777215)"}, set{"connection_time", "LEAST({old} + {new}, 16777215)"},
		set{"skill", "{new}"}, set{"skill_change", "{old} + {new}"},
		set{"rounds", "LEAST({old} + {new}, 65535)"}, set{"wins", "LEAST({old} + {new}, 65535)"},
		set{"losses", "LEAST({old} + {new}, 65535)"},
		set{"kill_streak", "GREATEST({old}, {new})"}, set{"death_streak", "GREATEST({old}, {new})"},
		set{"damage", "LEAST({old} + {new}, 4294967295)"}, set{"kill_assists", "LEAST({old} + {new}, 65535)"},
		set{"survived_count", "LEAST({old} + {new}, 65535)"}),
		[]any{p.ID, s.historyDay(ev.Time), ev.Srv.Game, h.kills, h.deaths, h.suicides, h.headshots, h.teamkills,
			h.shots, h.hits, h.conn, max(p.Skill, 0), h.skillChange,
			min(h.rounds, 65535), min(h.wins, 65535), min(h.losses, 65535), min(h.killStreak, 65535), min(h.deathStreak, 255),
			min(h.damage, 4294967295), min(h.killAssists, 65535), min(h.survived, 65535)}})
}

// RoundStart revives everybody on the server in the live view and clears
// the bomb carrier.
func (s *Store) RoundStart(ctx context.Context, ev Ev) error {
	return s.run(ctx, step{`UPDATE hlstats_Livestats SET is_dead = 0, has_bomb = 0 WHERE server_id = ?`, []any{ev.Srv.ID}})
}

// HideRanking returns hlstats_Players.hideranking: 0 ranked, 1 hidden by
// an admin (and unranked bots), 2 banned, 3 hidden by the player.
func (s *Store) HideRanking(ctx context.Context, playerID int64) (int, error) {
	var h int
	err := s.queryRow(ctx, `SELECT hideranking FROM hlstats_Players WHERE playerId = ?`, playerID).Scan(&h)
	return h, err
}

// SetHideRanking changes hideranking from one value to another (the
// player's own !hideranking: 0 and 3 only) and returns the value now
// stored; a concurrent change (a ban) wins.
func (s *Store) SetHideRanking(ctx context.Context, playerID int64, from, to int) (int, error) {
	if (from != 0 && from != 3) || (to != 0 && to != 3) {
		return from, fmt.Errorf("hideranking %d -> %d is not a player's own change", from, to)
	}
	if err := s.run(ctx, step{`UPDATE hlstats_Players SET hideranking = ? WHERE playerId = ? AND hideranking = ?`, []any{to, playerID, from}}); err != nil {
		return 0, err
	}
	return s.HideRanking(ctx, playerID)
}

// HideBanned hides a banned player from the rankings (hideranking = 2, as
// HLstatsX does for VAC bans and permanent bans). Players hidden as
// inactive or by an admin are hidden as banned as well; nothing is created.
func (s *Store) HideBanned(ctx context.Context, ev Ev, uniqueID string) error {
	return s.run(ctx, step{`UPDATE hlstats_Players SET hideranking = 2 WHERE hideranking <> 2
		AND playerId IN (SELECT playerId FROM hlstats_PlayerUniqueIds WHERE uniqueId = ? AND game = ?)`,
		[]any{clip(uniqueID, 64), ev.Srv.Game}},
		// The ban hides the player now: the end of a recorded ban (EndBan)
		// must not put the player back.
		step{`UPDATE hlstats_Players_Bans SET hid = 0 WHERE active = 1
		AND player_id IN (SELECT playerId FROM hlstats_PlayerUniqueIds WHERE uniqueId = ? AND game = ?)`,
			[]any{clip(uniqueID, 64), ev.Srv.Game}})
}

// UpdateServerPlayers sets hlstats_Servers.players to the number of players
// of each server's game that are not hidden as banned (as HLstatsX).
func (s *Store) UpdateServerPlayers(ctx context.Context) error {
	return s.run(ctx, step{`UPDATE hlstats_Servers SET players = (SELECT COUNT(*) FROM hlstats_Players p
		WHERE p.game = hlstats_Servers.game AND p.hideranking <> 2)`, nil})
}

// ---- clans ----

// clanWord is the clan name part of a pattern. HLstatsX matches
// [A-Za-z0-9]+[A-Za-z0-9_-]* on the quotemeta'd pattern, where - is escaped
// and so never part of the name.
var clanWord = regexp.MustCompile(`[A-Za-z0-9]+[A-Za-z0-9_]*`)

// ClanPatterns turns an hlstats_ClanTags row into regular expressions, as
// HLstatsX does: in the pattern, A is one character, X an optional one, the
// first run of letters is the clan name and the whole match the tag (e.g.
// [AXXXXX] matches "[ZOOM]Player": tag "[ZOOM]", name "ZOOM"). position is
// START, END or EITHER (the tag before or after the rest of the name).
func ClanPatterns(pattern, position string) []*regexp.Regexp {
	q := regexp.QuoteMeta(pattern)
	if loc := clanWord.FindStringIndex(q); loc != nil {
		q = q[:loc[0]] + "(" + q[loc[0]:loc[1]] + ")" + q[loc[1]:]
	}
	q = strings.NewReplacer("A", ".", "X", ".?").Replace(q)
	var out []*regexp.Regexp
	pos := strings.ToUpper(position)
	if pos == "START" || pos == "EITHER" {
		out = append(out, regexp.MustCompile(`(?i)^(`+q+`).+`))
	}
	if pos == "END" || pos == "EITHER" {
		out = append(out, regexp.MustCompile(`(?i).+(`+q+`)$`))
	}
	return out
}

// MatchClan returns the clan tag and clan name in a player name, using the
// patterns in order; tag is empty if none matches.
func MatchClan(patterns []*regexp.Regexp, name string) (tag, clanName string) {
	for _, re := range patterns {
		if m := re.FindStringSubmatch(name); m != nil {
			tag, clanName = m[1], m[1]
			if len(m) > 2 && m[2] != "" {
				clanName = m[2]
			}
			return tag, clanName
		}
	}
	return "", ""
}

func (s *Store) clanPatterns(ctx context.Context) ([]*regexp.Regexp, error) {
	v, err := s.cached("hlstats_ClanTags", func() (any, error) {
		rows, err := s.query(ctx, `SELECT pattern, position FROM hlstats_ClanTags ORDER BY pattern_length DESC, sort_id DESC, id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []*regexp.Regexp
		for rows.Next() {
			var pattern, position string
			if err := rows.Scan(&pattern, &position); err != nil {
				return nil, err
			}
			if pattern != "" {
				out = append(out, ClanPatterns(pattern, position)...)
			}
		}
		return out, rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("hlstats_ClanTags: %w", err)
	}
	return v.([]*regexp.Regexp), nil
}

// clanID returns the id of the game's clan with tag, creating the clan.
func (s *Store) clanID(ctx context.Context, game, tag, name string) (int64, error) {
	tag = clip(tag, 128)
	v, err := s.cached("hlstats_Clans|"+game+"|"+tag, func() (any, error) {
		s.clanMu.Lock()
		defer s.clanMu.Unlock()
		var id int64
		err := s.queryRow(ctx, `SELECT clanId FROM hlstats_Clans WHERE tag = ? AND game = ? ORDER BY clanId LIMIT 1`, tag, game).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id, err = s.insertID(ctx, `INSERT INTO hlstats_Clans (tag, name, game) VALUES (?, ?, ?)`, "clanId", tag, clip(name, 255), game)
		}
		return id, err
	})
	if err != nil {
		return 0, fmt.Errorf("hlstats_Clans %q: %w", tag, err)
	}
	return v.(int64), nil
}

// ClanFromName sets the player's clan from a clan tag in name
// (hlstats_ClanTags), creating the clan if needed, and returns the clan id.
// Without a tag the player's clan is left as it is (it may have been set in
// the web admin) and 0 is returned.
func (s *Store) ClanFromName(ctx context.Context, ev Ev, p Live, name string) (int64, error) {
	patterns, err := s.clanPatterns(ctx)
	if err != nil {
		return 0, err
	}
	tag, clanName := MatchClan(patterns, name)
	if tag == "" {
		return 0, nil
	}
	return s.setClan(ctx, ev, p, tag, clanName)
}

// ClanTag sets the player's clan from the game's own clan tag (CS:GO
// `triggered "clantag" (value "...")`); an empty tag removes the clan.
func (s *Store) ClanTag(ctx context.Context, ev Ev, p Live, tag string) (int64, error) {
	if tag == "" {
		return 0, s.run(ctx, step{`UPDATE hlstats_Players SET clan = 0 WHERE playerId = ?`, []any{p.ID}})
	}
	return s.setClan(ctx, ev, p, tag, tag)
}

func (s *Store) setClan(ctx context.Context, ev Ev, p Live, tag, name string) (int64, error) {
	id, err := s.clanID(ctx, ev.Srv.Game, tag, name)
	if err != nil {
		return 0, err
	}
	return id, s.run(ctx, step{`UPDATE hlstats_Players SET clan = ? WHERE playerId = ? AND clan <> ?`, []any{id, p.ID, id}})
}

// ---- queries for in-game commands ----

type Player struct {
	ID                                     int64
	Name                                   string
	Skill                                  int
	Kills, Deaths, Headshots, Suicides, TK int
	ConnectionTime                         int // seconds
	Shots, Hits                            int
	KillStreak, DeathStreak                int // best
	KillAssists, KillsAssisted             int
	PointsHealed, FlagsCaptured            int
	CustomWins                             int
	Country                                string // hlstats_Players_Locations.flag, "" if unknown
}

const playerCols = `p.playerId, COALESCE(NULLIF(d.lastName, ''), '?'), p.skill, p.kills, p.deaths, p.headshots, p.suicides, p.team_kills,
	p.connection_time, p.shots, p.hits, p.kill_streak, p.death_streak, p.kill_assists, p.kills_assisted, p.points_healed,
	p.flags_captured, p.custom_wins, COALESCE(l.flag, '')`
const playerFrom = ` FROM hlstats_Players p LEFT JOIN hlstats_Players_Data d ON d.playerId = p.playerId
	LEFT JOIN hlstats_Players_Locations l ON l.id = d.location_id `

func scanPlayer(r interface{ Scan(...any) error }) (Player, error) {
	var p Player
	err := r.Scan(&p.ID, &p.Name, &p.Skill, &p.Kills, &p.Deaths, &p.Headshots, &p.Suicides, &p.TK, &p.ConnectionTime,
		&p.Shots, &p.Hits, &p.KillStreak, &p.DeathStreak, &p.KillAssists, &p.KillsAssisted, &p.PointsHealed,
		&p.FlagsCaptured, &p.CustomWins, &p.Country)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) PlayerStats(ctx context.Context, playerID int64) (Player, error) {
	return scanPlayer(s.queryRow(ctx, `SELECT `+playerCols+playerFrom+`WHERE p.playerId = ?`, playerID))
}

// Rank returns the 1-based rank for skill among the game's ranked players
// (hideranking = 0), and their number.
func (s *Store) Rank(ctx context.Context, game string, skill int) (rank, total int, err error) {
	err = s.queryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN skill > ? THEN 1 ELSE 0 END), 0), COUNT(*) FROM hlstats_Players WHERE game = ? AND hideranking = 0`,
		skill, game).Scan(&rank, &total)
	return rank + 1, total, err
}

// Next returns the ranked player with the lowest skill above skill.
func (s *Store) Next(ctx context.Context, game string, skill int) (Player, error) {
	return scanPlayer(s.queryRow(ctx, `SELECT `+playerCols+playerFrom+
		`WHERE p.game = ? AND p.hideranking = 0 AND p.skill > ? ORDER BY p.skill, p.playerId LIMIT 1`, game, skill))
}

// Above returns up to n ranked players with more skill than skill, the
// nearest first.
func (s *Store) Above(ctx context.Context, game string, skill, n int) ([]Player, error) {
	return s.players(ctx, `SELECT `+playerCols+playerFrom+
		`WHERE p.game = ? AND p.hideranking = 0 AND p.skill > ? ORDER BY p.skill, p.playerId LIMIT ?`, game, skill, n)
}

// WeaponCode returns the code of the game's weapon named code, ignoring
// case (for !weapons <weapon>), or ErrNotFound.
func (s *Store) WeaponCode(ctx context.Context, game, code string) (string, error) {
	var c string
	err := s.queryRow(ctx, `SELECT code FROM hlstats_Weapons WHERE game = ? AND LOWER(code) = LOWER(?) ORDER BY weaponId LIMIT 1`,
		game, clip(code, 64)).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return c, err
}

// ActionID returns the hlstats_Actions id of the game's action code,
// ignoring case (for !actions <action>), or ErrNotFound.
func (s *Store) ActionID(ctx context.Context, game, code string) (int, error) {
	var id int
	err := s.queryRow(ctx, `SELECT id FROM hlstats_Actions WHERE game = ? AND LOWER(code) = LOWER(?) ORDER BY id LIMIT 1`,
		game, clip(code, 64)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// Top returns the n best ranked players of the game.
func (s *Store) Top(ctx context.Context, game string, n int) ([]Player, error) {
	return s.players(ctx, `SELECT `+playerCols+playerFrom+
		`WHERE p.game = ? AND p.hideranking = 0 ORDER BY p.skill DESC, p.kills DESC, p.playerId LIMIT ?`, game, n)
}

func (s *Store) players(ctx context.Context, q string, args ...any) ([]Player, error) {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Player
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
