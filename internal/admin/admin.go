// SPDX-License-Identifier: GPL-3.0-or-later

// Package admin changes games (rankings), game servers and settings in the
// database, as the web admin does: `gamewe game|server|option ...`.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
)

// Admin runs the commands on DB and prints to Out. With DryRun it only
// reports what it would change.
type Admin struct {
	DB     *database.DB
	Out    io.Writer
	DryRun bool

	warnedRealgame bool // the note about migration 010 is printed once
}

var (
	ErrExists   = errors.New("already exists")
	ErrNotFound = errors.New("not found")
)

// reCode is a hlstats_Games.code: hlstats_Players.game is char(10).
var reCode = regexp.MustCompile(`^[a-z0-9_]{1,10}$`)

func (a *Admin) q(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return a.DB.QueryContext(ctx, a.DB.Q(query), args...)
}

func (a *Admin) row(ctx context.Context, query string, args ...any) *sql.Row {
	return a.DB.QueryRowContext(ctx, a.DB.Q(query), args...)
}

func (a *Admin) exec(ctx context.Context, query string, args ...any) (int64, error) {
	r, err := a.DB.ExecContext(ctx, a.DB.Q(query), args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ignore makes an INSERT skip rows that violate a unique key.
func (a *Admin) ignore(insert string) string {
	if a.DB.Postgres() {
		return insert + " ON CONFLICT DO NOTHING"
	}
	return strings.Replace(insert, "INSERT INTO", "INSERT IGNORE INTO", 1)
}

func (a *Admin) say(format string, args ...any) {
	if a.DryRun {
		format = "dry run: " + format
	}
	fmt.Fprintf(a.Out, format+"\n", args...)
}

// ---- games ----

// Game is a row of hlstats_Games with its use.
type Game struct {
	Code     string `json:"code"`
	Realgame string `json:"realgame"`
	// Guessed: realgame is empty or unknown, the base game is guessed from
	// the code (ListGames marks it with "?").
	Guessed bool   `json:"realgame_guessed"`
	Engine  string `json:"engine"`
	Hidden  bool   `json:"hidden"`
	Servers int    `json:"servers"`
	Players int    `json:"players"`
	Name    string `json:"name"`
}

// realgameCol is the column expression of hlstats_Games.realgame: the
// column, or an empty string literal in an old database without it (the
// base game is then guessed from the code).
func (a *Admin) realgameCol(ctx context.Context) (string, error) {
	missing, err := database.MissingSchema(ctx, a.DB)
	if err != nil {
		return "", err
	}
	for _, r := range missing {
		if r.Table == "hlstats_Games" && r.Column == "realgame" {
			if !a.warnedRealgame {
				a.warnedRealgame = true
				fmt.Fprintf(a.Out, "note: %s; %s\n", r, database.SchemaHint)
			}
			return "''", nil
		}
	}
	return "realgame", nil
}

func (a *Admin) game(ctx context.Context, code string) (Game, error) {
	g := Game{Code: code}
	var hidden string
	col, err := a.realgameCol(ctx)
	if err != nil {
		return g, err
	}
	err = a.row(ctx, `SELECT name, `+col+`, hidden FROM hlstats_Games WHERE code = ?`, code).Scan(&g.Name, &g.Realgame, &hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return g, fmt.Errorf("game %q: %w", code, ErrNotFound)
	}
	g.Hidden = hidden == "1"
	return g, err
}

// ListGames prints all games with their number of servers and players.
func (a *Admin) ListGames(ctx context.Context) error {
	list, err := a.Games(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tREALGAME\tENGINE\tHIDDEN\tSERVERS\tPLAYERS\tNAME")
	for _, g := range list {
		rg := g.Realgame
		if g.Guessed {
			rg += "?"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", g.Code, rg, g.Engine, map[bool]string{true: "yes"}[g.Hidden], g.Servers, g.Players, g.Name)
	}
	return w.Flush()
}

// Games returns all games with their number of servers and players.
func (a *Admin) Games(ctx context.Context) ([]Game, error) {
	col, err := a.realgameCol(ctx)
	if err != nil {
		return nil, err
	}
	if col == "realgame" {
		col = "g.realgame"
	}
	rows, err := a.q(ctx, `SELECT g.code, g.name, `+col+`, g.hidden,
		(SELECT COUNT(*) FROM hlstats_Servers s WHERE s.game = g.code),
		(SELECT COUNT(*) FROM hlstats_Players p WHERE p.game = g.code)
		FROM hlstats_Games g ORDER BY g.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Game{}
	for rows.Next() {
		var g Game
		var hidden string
		if err := rows.Scan(&g.Code, &g.Name, &g.Realgame, &hidden, &g.Servers, &g.Players); err != nil {
			return nil, err
		}
		_, engine, warn := games.Resolve(g.Realgame, g.Code)
		g.Engine, g.Guessed, g.Hidden = engine.String(), warn != "", hidden == "1"
		list = append(list, g)
	}
	return list, rows.Err()
}

// cloned are the per-game definition tables a new ranking copies from its
// template, as the web admin does: table, game column, other columns.
var cloned = []struct{ table, game, cols string }{
	{"hlstats_Teams", "game", "code, name, hidden"},
	{"hlstats_Roles", "game", "code, name, hidden"},
	{"hlstats_Actions", "game", "code, reward_player, reward_team, team, description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions"},
	{"hlstats_Weapons", "game", "code, name, modifier"},
	{"hlstats_Awards", "game", "awardType, code, name, verb"},
	{"hlstats_Definition_Ranks_Ribbons", "game_code", "definition_type, code, name, img_file, thumb_file, minimum, restriction_type"},
	{"hlstats_Definition_Ribbons_Classes", "game_code", "name, maximum, steps, class"},
}

// AddGame creates a game (ranking). With a template its definitions
// (teams, roles, actions, weapons, awards, ranks, ribbons) and realgame are
// copied; realgame overrides the template's. An existing game is an error,
// except that the copy is completed (missing rows added) when it has the
// same template-derived realgame: running add twice is safe.
func (a *Admin) AddGame(ctx context.Context, code, name, template, realgame string, hidden bool) error {
	if !reCode.MatchString(code) {
		return fmt.Errorf("code %q: 1-10 characters a-z, 0-9, _", code)
	}
	if name == "" || utf8.RuneCountInString(name) > 255 {
		return errors.New("name: 1-255 characters")
	}
	if template != "" {
		t, err := a.game(ctx, template)
		if err != nil {
			return fmt.Errorf("template: %w", err)
		}
		if realgame == "" {
			realgame = t.Realgame
			if realgame == "" {
				realgame, _, _ = games.Resolve("", template)
			}
		}
	}
	if realgame == "" {
		realgame, _, _ = games.Resolve("", code)
	}
	if base, _, warn := games.Resolve(realgame, code); warn != "" || base != strings.ToLower(realgame) {
		return fmt.Errorf("realgame %q is not a known game (%s)", realgame, strings.Join(games.Known(), ", "))
	}
	realgame = strings.ToLower(realgame)
	existing, err := a.game(ctx, code)
	switch {
	case err == nil:
		if template == "" {
			return fmt.Errorf("game %q: %w", code, ErrExists)
		}
		a.say("game %s exists (%s), completing the copy of %s", code, existing.Name, template)
	case errors.Is(err, ErrNotFound):
		if !a.DryRun {
			h := "0"
			if hidden {
				h = "1"
			}
			col, err := a.realgameCol(ctx)
			if err != nil {
				return err
			}
			if col == "realgame" {
				_, err = a.exec(ctx, `INSERT INTO hlstats_Games (code, name, realgame, hidden) VALUES (?, ?, ?, ?)`, code, name, realgame, h)
			} else { // an old database: the collector guesses the base game from the code
				_, err = a.exec(ctx, `INSERT INTO hlstats_Games (code, name, hidden) VALUES (?, ?, ?)`, code, name, h)
			}
			if err != nil {
				return err
			}
		}
		stored := ""
		if col, err := a.realgameCol(ctx); err == nil && col != "realgame" {
			stored = " (not stored: the database has no hlstats_Games.realgame; the collector guesses it from the code)"
		}
		a.say("game %s added: %s, realgame %s%s%s", code, name, realgame, stored, map[bool]string{true: ", hidden"}[hidden])
	default:
		return err
	}
	if template == "" {
		return nil
	}
	for _, c := range cloned {
		var n int64
		if a.DryRun {
			err = a.row(ctx, `SELECT COUNT(*) FROM `+c.table+` WHERE `+c.game+` = ?`, template).Scan(&n)
		} else {
			n, err = a.exec(ctx, a.ignore(`INSERT INTO `+c.table+` (`+c.game+`, `+c.cols+`) SELECT ?, `+c.cols+` FROM `+c.table+` WHERE `+c.game+` = ?`), code, template)
		}
		if err != nil {
			return fmt.Errorf("copy %s: %w", c.table, err)
		}
		a.say("  %s: %d rows copied from %s", c.table, n, template)
	}
	return nil
}

// SetGameHidden hides a game from the web's game list, or shows it.
func (a *Admin) SetGameHidden(ctx context.Context, code string, hidden bool) error {
	if _, err := a.game(ctx, code); err != nil {
		return err
	}
	h := map[bool]string{true: "1", false: "0"}[hidden]
	if !a.DryRun {
		if _, err := a.exec(ctx, `UPDATE hlstats_Games SET hidden = ? WHERE code = ?`, h, code); err != nil {
			return err
		}
	}
	a.say("game %s %s", code, map[bool]string{true: "hidden", false: "shown"}[hidden])
	return nil
}

// GameFields are the values of `game set`; nil = keep.
type GameFields struct {
	Name, Realgame *string
	Order          *int // hlstats_Games.display_id
	Hidden         *bool
}

// SetGame changes the given fields of a game.
func (a *Admin) SetGame(ctx context.Context, code string, f GameFields) error {
	if f.Name == nil && f.Realgame == nil && f.Order == nil && f.Hidden == nil {
		return errors.New("nothing to change")
	}
	g, err := a.game(ctx, code)
	if err != nil {
		return err
	}
	if f.Name != nil && (*f.Name == "" || utf8.RuneCountInString(*f.Name) > 255) {
		return errors.New("name: 1-255 characters")
	}
	if f.Realgame != nil {
		col, err := a.realgameCol(ctx)
		if err != nil {
			return err
		}
		if col != "realgame" {
			return fmt.Errorf("-realgame: the database has no hlstats_Games.realgame; %s", database.SchemaHint)
		}
		if base, _, warn := games.Resolve(*f.Realgame, code); warn != "" || base != strings.ToLower(*f.Realgame) {
			return fmt.Errorf("realgame %q is not a known game (%s)", *f.Realgame, strings.Join(games.Known(), ", "))
		}
		r := strings.ToLower(*f.Realgame)
		f.Realgame = &r
	}
	var order int
	if f.Order != nil {
		if *f.Order < -32768 || *f.Order > 32767 {
			return errors.New("order: -32768 to 32767")
		}
		if err := a.row(ctx, `SELECT display_id FROM hlstats_Games WHERE code = ?`, g.Code).Scan(&order); err != nil {
			return err
		}
	}
	var sets []string
	var args []any
	var changed []string
	add := func(col string, v any, desc string) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
		changed = append(changed, desc)
	}
	if f.Name != nil && *f.Name != g.Name {
		add("name", *f.Name, fmt.Sprintf("name %q -> %q", g.Name, *f.Name))
	}
	if f.Realgame != nil && *f.Realgame != g.Realgame {
		add("realgame", *f.Realgame, fmt.Sprintf("realgame %q -> %q", g.Realgame, *f.Realgame))
	}
	if f.Order != nil && *f.Order != order {
		add("display_id", *f.Order, fmt.Sprintf("order %d -> %d", order, *f.Order))
	}
	if f.Hidden != nil && *f.Hidden != g.Hidden {
		add("hidden", map[bool]string{true: "1", false: "0"}[*f.Hidden], map[bool]string{true: "hidden", false: "shown"}[*f.Hidden])
	}
	if len(sets) == 0 {
		a.say("game %s unchanged", g.Code)
		return nil
	}
	if !a.DryRun {
		if _, err := a.exec(ctx, `UPDATE hlstats_Games SET `+strings.Join(sets, ", ")+` WHERE code = ?`, append(args, g.Code)...); err != nil {
			return err
		}
	}
	a.say("game %s: %s", g.Code, strings.Join(changed, ", "))
	return nil
}

// DeleteGame deletes an unused game with its definitions. A default
// ranking (a known base game) and a game with servers or players are
// refused.
func (a *Admin) DeleteGame(ctx context.Context, code string) error {
	if games.IsBase(code) {
		return fmt.Errorf("game %q is a default ranking: not deleted (hide it instead)", code)
	}
	if _, err := a.game(ctx, code); err != nil {
		return err
	}
	var servers, players int
	if err := a.row(ctx, `SELECT (SELECT COUNT(*) FROM hlstats_Servers WHERE game = ?), (SELECT COUNT(*) FROM hlstats_Players WHERE game = ?)`, code, code).Scan(&servers, &players); err != nil {
		return err
	}
	if servers > 0 || players > 0 {
		return fmt.Errorf("game %q has %d servers and %d players: not deleted (hide it instead)", code, servers, players)
	}
	for _, c := range append(cloned, struct{ table, game, cols string }{"hlstats_Maps", "game", ""}) {
		var n int64
		var err error
		if a.DryRun {
			err = a.row(ctx, `SELECT COUNT(*) FROM `+c.table+` WHERE `+c.game+` = ?`, code).Scan(&n)
		} else {
			n, err = a.exec(ctx, `DELETE FROM `+c.table+` WHERE `+c.game+` = ?`, code)
		}
		if err != nil {
			return err
		}
		if n > 0 {
			a.say("  %s: %d rows deleted", c.table, n)
		}
	}
	if !a.DryRun {
		if _, err := a.exec(ctx, `DELETE FROM hlstats_Games WHERE code = ?`, code); err != nil {
			return err
		}
	}
	a.say("game %s deleted", code)
	return nil
}

// ---- servers ----

// ServerFields are the values of `server add` / `server set`; nil = keep.
type ServerFields struct {
	Game, Name, RconPassword, PublicAddress *string
	// LogSecret is hlstats_Servers.log_secret (sv_logsecret, "" = none);
	// shown only as changed.
	LogSecret *string
}

type server struct {
	id                                 int64
	address                            string
	port                               int
	name, game, rcon, public, realgame string
	secret                             string
}

func (a *Admin) server(ctx context.Context, ref string) (server, error) {
	var s server
	q := `SELECT s.serverId, s.address, s.port, s.name, s.game, s.rcon_password, s.publicaddress, COALESCE(g.realgame, ''), s.log_secret
		FROM hlstats_Servers s LEFT JOIN hlstats_Games g ON g.code = s.game WHERE `
	var r *sql.Row
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		r = a.row(ctx, q+`s.serverId = ?`, id)
	} else if ap, err := netip.ParseAddrPort(ref); err == nil {
		r = a.row(ctx, q+`s.address = ? AND s.port = ?`, ap.Addr().Unmap().String(), int(ap.Port()))
	} else {
		return s, fmt.Errorf("server %q: give the serverId or ip:port", ref)
	}
	err := r.Scan(&s.id, &s.address, &s.port, &s.name, &s.game, &s.rcon, &s.public, &s.realgame, &s.secret)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("server %s: %w", ref, ErrNotFound)
	}
	return s, err
}

// ServerRow is a game server of ListServers. The RCON password and the
// log secret are never read, only whether they are set.
type ServerRow struct {
	ID              int64  `json:"id"`
	Address         string `json:"address"` // ip:port
	Game            string `json:"game"`
	Engine          string `json:"engine"`
	RconPasswordSet bool   `json:"rcon_password_set"`
	LogSecretSet    bool   `json:"log_secret_set"`
	Name            string `json:"name"`
}

// ListServers prints the game servers; the RCON password and the log
// secret only as set / not set.
func (a *Admin) ListServers(ctx context.Context) error {
	list, err := a.Servers(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tADDRESS\tGAME\tENGINE\tRCON\tLOGSECRET\tNAME")
	setStr := map[bool]string{true: "set", false: "-"}
	for _, s := range list {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Address, s.Game, s.Engine, setStr[s.RconPasswordSet], setStr[s.LogSecretSet], s.Name)
	}
	return w.Flush()
}

// Servers returns the game servers; the RCON password and the log secret
// only as set / not set (the query does not read them).
func (a *Admin) Servers(ctx context.Context) ([]ServerRow, error) {
	rows, err := a.q(ctx, `SELECT s.serverId, s.address, s.port, s.game, COALESCE(g.realgame, ''), s.rcon_password <> '', s.log_secret <> '', s.name
		FROM hlstats_Servers s LEFT JOIN hlstats_Games g ON g.code = s.game ORDER BY s.serverId`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []ServerRow{}
	for rows.Next() {
		var s ServerRow
		var addr, realgame string
		var port int
		if err := rows.Scan(&s.ID, &addr, &port, &s.Game, &realgame, &s.RconPasswordSet, &s.LogSecretSet, &s.Name); err != nil {
			return nil, err
		}
		_, engine, _ := games.Resolve(realgame, s.Game)
		s.Address, s.Engine = fmt.Sprintf("%s:%d", addr, port), engine.String()
		list = append(list, s)
	}
	return list, rows.Err()
}

func (a *Admin) checkFields(ctx context.Context, f ServerFields) error {
	if f.Game != nil {
		if _, err := a.game(ctx, *f.Game); err != nil {
			return err
		}
	}
	if f.Name != nil && utf8.RuneCountInString(*f.Name) > 255 {
		return errors.New("name: at most 255 characters")
	}
	if f.RconPassword != nil && (len(*f.RconPassword) > 128 || strings.ContainsAny(*f.RconPassword, "\"\r\n\x00")) {
		return errors.New("rcon password: at most 128 characters, no quotes or line breaks")
	}
	if f.LogSecret != nil {
		if err := logparse.CheckSecret(*f.LogSecret); err != nil {
			return err
		}
	}
	if f.PublicAddress != nil && utf8.RuneCountInString(*f.PublicAddress) > 255 {
		return errors.New("public address: at most 255 characters")
	}
	return nil
}

// AddServer registers a game server. An existing ip:port is an error
// (use SetServer).
func (a *Admin) AddServer(ctx context.Context, addr string, f ServerFields) error {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || ap.Port() == 0 {
		return fmt.Errorf("address %q: want ip:port", addr)
	}
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	if !ap.Addr().Is4() {
		return fmt.Errorf("address %q: only IPv4 (hlstats_Servers.address)", addr)
	}
	if f.Game == nil {
		return errors.New("-game is required")
	}
	if err := a.checkFields(ctx, f); err != nil {
		return err
	}
	if s, err := a.server(ctx, ap.String()); err == nil {
		return fmt.Errorf("server %s (id %d): %w, use server set", ap, s.id, ErrExists)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	str := func(p *string, def string) string {
		if p == nil {
			return def
		}
		return *p
	}
	if !a.DryRun {
		var changed int64
		if str(f.LogSecret, "") != "" {
			changed = time.Now().Unix()
		}
		if _, err := a.exec(ctx, `INSERT INTO hlstats_Servers (address, port, name, game, rcon_password, publicaddress, log_secret, log_secret_changed) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ap.Addr().String(), int(ap.Port()), str(f.Name, ""), *f.Game, str(f.RconPassword, ""), str(f.PublicAddress, ap.String()),
			str(f.LogSecret, ""), changed); err != nil {
			return err
		}
	}
	isSet := map[bool]string{true: "set", false: "not set"}
	a.say("server %s added: game %s, rcon password %s, log secret %s", ap, *f.Game, isSet[str(f.RconPassword, "") != ""], isSet[str(f.LogSecret, "") != ""])
	return nil
}

// SetServer changes the given fields of a game server.
func (a *Admin) SetServer(ctx context.Context, ref string, f ServerFields) error {
	s, err := a.server(ctx, ref)
	if err != nil {
		return err
	}
	if err := a.checkFields(ctx, f); err != nil {
		return err
	}
	var sets []string
	var args []any
	var changed []string
	add := func(col, label string, v *string, old string, secret bool) {
		if v == nil || *v == old {
			return
		}
		sets = append(sets, col+" = ?")
		args = append(args, *v)
		if secret {
			changed = append(changed, label+" changed")
		} else {
			changed = append(changed, fmt.Sprintf("%s %q -> %q", label, old, *v))
		}
	}
	add("game", "game", f.Game, s.game, false)
	add("name", "name", f.Name, s.name, false)
	add("rcon_password", "rcon password", f.RconPassword, s.rcon, true)
	add("publicaddress", "public address", f.PublicAddress, s.public, false)
	if f.LogSecret != nil && *f.LogSecret != s.secret {
		add("log_secret", "log secret", f.LogSecret, s.secret, true)
		sets = append(sets, "log_secret_changed = ?")
		args = append(args, time.Now().Unix())
		if *f.LogSecret == "" {
			changed[len(changed)-1] = "log secret removed"
		}
	}
	if len(sets) == 0 {
		a.say("server %d (%s:%d) unchanged", s.id, s.address, s.port)
		return nil
	}
	if !a.DryRun {
		if _, err := a.exec(ctx, `UPDATE hlstats_Servers SET `+strings.Join(sets, ", ")+` WHERE serverId = ?`, append(args, s.id)...); err != nil {
			return err
		}
	}
	a.say("server %d (%s:%d): %s", s.id, s.address, s.port, strings.Join(changed, ", "))
	return nil
}

// RemoveServer deletes a game server row (its players and statistics stay).
func (a *Admin) RemoveServer(ctx context.Context, ref string) error {
	s, err := a.server(ctx, ref)
	if err != nil {
		return err
	}
	if !a.DryRun {
		if _, err := a.exec(ctx, `DELETE FROM hlstats_Livestats WHERE server_id = ?`, s.id); err != nil {
			return err
		}
		if _, err := a.exec(ctx, `DELETE FROM hlstats_Servers WHERE serverId = ?`, s.id); err != nil {
			return err
		}
	}
	a.say("server %d (%s:%d, %s) removed; players and statistics are kept", s.id, s.address, s.port, s.game)
	return nil
}

// ---- options ----

// Option is a value of hlstats_Options.
type Option struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Default: the key is not stored, Value is the collector's default.
	Default     bool   `json:"default"`
	Description string `json:"description,omitempty"`
}

// ListOptions prints the collector settings (gamewe_*) with their values,
// defaults for missing keys marked.
func (a *Admin) ListOptions(ctx context.Context) error {
	list, err := a.Options(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tVALUE\tDESCRIPTION")
	for _, o := range list {
		v := o.Value
		if o.Default {
			v += " (default)"
		}
		if utf8.RuneCountInString(v) > 40 {
			v = string([]rune(v)[:37]) + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", o.Key, v, o.Description)
	}
	return w.Flush()
}

// Options returns the collector settings (gamewe_*) with their values or
// defaults.
func (a *Admin) Options(ctx context.Context) ([]Option, error) {
	opts, err := a.options(ctx)
	if err != nil {
		return nil, err
	}
	list := []Option{}
	for _, k := range settings.Keys() {
		v, ok := opts[k.Name]
		if !ok {
			v = k.Default
		}
		list = append(list, Option{Key: k.Name, Value: v, Default: !ok, Description: k.Meaning})
	}
	return list, nil
}

func (a *Admin) options(ctx context.Context) (map[string]string, error) {
	rows, err := a.q(ctx, `SELECT keyname, value FROM hlstats_Options`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// GetOption prints one value of hlstats_Options.
func (a *Admin) GetOption(ctx context.Context, key string) error {
	o, err := a.Option(ctx, key)
	if err != nil {
		return err
	}
	if o.Default {
		fmt.Fprintf(a.Out, "%s (default, not stored)\n", o.Value)
		return nil
	}
	fmt.Fprintln(a.Out, o.Value)
	return nil
}

// Option returns one value of hlstats_Options, or the default of a
// collector setting that is not stored.
func (a *Admin) Option(ctx context.Context, key string) (Option, error) {
	o := Option{Key: key}
	err := a.row(ctx, `SELECT value FROM hlstats_Options WHERE keyname = ?`, key).Scan(&o.Value)
	if errors.Is(err, sql.ErrNoRows) {
		for _, k := range settings.Keys() {
			if k.Name == key {
				o.Value, o.Default, o.Description = k.Default, true, k.Meaning
				return o, nil
			}
		}
		return o, fmt.Errorf("option %q: %w", key, ErrNotFound)
	}
	for _, k := range settings.Keys() {
		if k.Name == key {
			o.Description = k.Meaning
		}
	}
	return o, err
}

// SetOption stores a value in hlstats_Options. Collector settings
// (gamewe_*) are checked: unknown keys and invalid values are refused.
func (a *Admin) SetOption(ctx context.Context, key, value string) error {
	if key == "" || len(key) > 128 || utf8.RuneCountInString(value) > 255 {
		return errors.New("key: 1-128 characters, value: at most 255")
	}
	if strings.HasPrefix(key, "gamewe_") {
		known := false
		for _, k := range settings.Keys() {
			known = known || k.Name == key
		}
		if !known && key != "gamewe_page_size" && key != "gamewe_heartbeat" {
			names := []string{}
			for _, k := range settings.Keys() {
				names = append(names, k.Name)
			}
			sort.Strings(names)
			return fmt.Errorf("%s is not a collector setting (%s)", key, strings.Join(names, ", "))
		}
		if _, warn := settings.FromOptions(map[string]string{key: value}); len(warn) > 0 {
			msg, _, _ := strings.Cut(warn[0], ", using")
			return errors.New(strings.TrimPrefix(msg, "hlstats_Options "))
		}
	}
	var old string
	err := a.row(ctx, `SELECT value FROM hlstats_Options WHERE keyname = ?`, key).Scan(&old)
	switch {
	case err == nil && old == value:
		a.say("%s = %s (unchanged)", key, value)
		return nil
	case err == nil:
		if !a.DryRun {
			if _, err := a.exec(ctx, `UPDATE hlstats_Options SET value = ? WHERE keyname = ?`, value, key); err != nil {
				return err
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		if !a.DryRun {
			if _, err := a.exec(ctx, `INSERT INTO hlstats_Options (keyname, value) VALUES (?, ?)`, key, value); err != nil {
				return err
			}
		}
	default:
		return err
	}
	a.say("%s = %s (the collector reads it within 60 s)", key, value)
	return nil
}
