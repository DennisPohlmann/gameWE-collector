// SPDX-License-Identifier: GPL-3.0-or-later

// Package convert copies the data of an HLstatsX:CE database into a new
// gameWE database (gamewe db convert). It does what the SQL script
// docs/migration/hlstatsx-ce-to-gamewe.sql does, but reads the CE
// database over a connection of its own: the CE database may be on
// another server, it is only read (a read-only session) and needs no
// copy or character set conversion beforehand.
//
// The values are computed by the CE server with the expressions of the
// SQL script (limits, IP addresses, rounding); the gameWE ids of games,
// maps, weapons, teams, roles, actions, awards and locations are looked
// up in the gameWE database, with its collation, after its reference
// data is complete.
package convert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gamewe-collector/internal/database"
)

// Plan is what Run would do; Inspect makes it and only reads.
type Plan struct {
	SourceName    string // the CE database
	SourceServer  string // its server's version
	SourceVersion string // hlstats_Options version / dbversion of CE, "" if unknown
	ServerZone    string // the source server's time zone: @@time_zone, with @@system_time_zone for SYSTEM
	Zone          Zone
	Mojibake      string
	Collations    map[string]int // table collation of the CE tables -> number of tables
	// DoubleEncoded is the number of player names (hlstats_PlayerNames)
	// that look like UTF-8 stored a second time; Repaired says whether Run
	// repairs them.
	DoubleEncoded int64
	Repaired      bool
	// FirstEvent is the eventTime of the oldest frag and what it becomes,
	// to check the time zone; empty without frags.
	FirstEvent, FirstEventUTC string
	TargetNew                 bool // the gameWE database has no tables yet: the schema is created first
	Steps                     []StepInfo
	Skipped                   []SkippedTable
	Warnings                  []string

	src  *sql.DB
	cols map[string]map[string]bool // CE tables -> columns, lower case
	lat1 map[string]bool            // CE tables in latin1, lower case
}

// StepInfo is one step of the plan.
type StepInfo struct {
	Name   string // what
	Source string // the CE table(s)
	Target string // the gameWE table
	Rows   int64  // rows of Source (the main table)
	Note   string
}

// SkippedTable is a CE table that is not converted.
type SkippedTable struct {
	Table string
	Rows  int64
	Why   string
}

// Result is what Run did.
type Result struct {
	Steps    []StepResult
	Summary  []Compare
	Repaired int64 // text values repaired (double-encoded UTF-8)
	Merged   int64 // player names merged: equal in utf8mb4_unicode_ci (case, accents)
	Duration time.Duration
}

// StepResult is the rows of one step.
type StepResult struct {
	Name    string
	Target  string
	Read    int64 // rows read from CE
	Written int64 // rows the gameWE table has more afterwards
	Skipped int64 // rows left out (unknown server or action, duplicates)
}

// Compare is a line of the summary: rows in CE and in gameWE.
type Compare struct {
	What   string
	CE     int64
	GameWE int64
}

// Errors of Inspect.
var (
	ErrNotMySQL  = errors.New("gamewe db convert works with MariaDB / MySQL only (source and gameWE database)")
	ErrSameDB    = errors.New("the source is the gameWE database itself: -from names the HLstatsX:CE database")
	ErrNotCE     = errors.New("the source is not an HLstatsX:CE database (no hlstats_Events_Frags with eventTime)")
	ErrSourceGWE = errors.New("the source is an HLstatsX database of the variant gameWE uses (timestamp_id instead of eventTime): it needs no conversion, gamewe db migrate upgrades it in place")
)

// NotNewError: the gameWE database already has data.
type NotNewError struct{ Why string }

func (e *NotNewError) Error() string {
	return "the gameWE database is not new (" + e.Why + "): gamewe db convert only fills a new, empty gameWE database"
}

// requiredTables are the CE tables without which nothing is converted.
var requiredTables = []string{"hlstats_Games", "hlstats_Servers", "hlstats_Players", "hlstats_PlayerUniqueIds",
	"hlstats_PlayerNames", "hlstats_Events_Frags"}

// notConverted lists the CE tables that are left out, and why.
var notConverted = []SkippedTable{
	{Table: "hlstats_Events_Rcon", Why: "RCON commands with the RCON passwords in clear text"},
	{Table: "hlstats_Events_Statsme", Why: "no such table in gameWE (weapon statistics come from the gameWE plugin)"},
	{Table: "hlstats_Events_Statsme2", Why: "no such table in gameWE"},
	{Table: "hlstats_Events_StatsmeLatency", Why: "no such table in gameWE"},
	{Table: "hlstats_Events_StatsmeTime", Why: "no such table in gameWE"},
	{Table: "hlstats_Livestats", Why: "who is online now; the collector fills it"},
	{Table: "hlstats_Options", Why: "CE's settings; gameWE has its own (web admin, gamewe option set)"},
	{Table: "hlstats_Options_Choices", Why: "CE's settings"},
	{Table: "hlstats_Servers_Config", Why: "CE's server settings"},
	{Table: "hlstats_Servers_Config_Default", Why: "CE's server settings"},
	{Table: "hlstats_Servers_VoiceComm", Why: "no such table in gameWE"},
	{Table: "hlstats_Ranks", Why: "gameWE has its own ranks and derives them per player"},
	{Table: "hlstats_Ribbons", Why: "gameWE has its own ribbons"},
	{Table: "hlstats_Ribbons_Triggers", Why: "gameWE has its own ribbons"},
	{Table: "hlstats_Players_Ribbons", Why: "gameWE derives the ribbons per player"},
	{Table: "hlstats_ClanTags", Why: "gameWE ships its own clan tag patterns"},
	{Table: "hlstats_Heatmap_Config", Why: "CE's reference data"},
	{Table: "hlstats_Games_Defaults", Why: "CE's reference data"},
	{Table: "hlstats_Games_Supported", Why: "CE's reference data"},
	{Table: "hlstats_Mods_Defaults", Why: "CE's reference data"},
	{Table: "hlstats_Mods_Supported", Why: "CE's reference data"},
	{Table: "hlstats_Countries", Why: "CE's reference data"},
}

// Options are the settings of Inspect and Run.
type Options struct {
	Zone     Zone
	Mojibake string // MojibakeAuto (default), MojibakeRepair, MojibakeKeep
	Out      io.Writer
	// BatchRows is the number of rows per INSERT (default 500).
	BatchRows int
}

// Inspect checks the source and the gameWE database and makes the plan.
// It only reads both.
func Inspect(ctx context.Context, src, dst *database.DB, opt Options) (*Plan, error) {
	if src.Postgres() || dst.Postgres() {
		return nil, ErrNotMySQL
	}
	if opt.Mojibake == "" {
		opt.Mojibake = MojibakeAuto
	}
	var sHost, dHost string
	var sPort, dPort int64
	p := &Plan{Zone: opt.Zone, Mojibake: opt.Mojibake, Collations: map[string]int{}, src: src.DB,
		cols: map[string]map[string]bool{}, lat1: map[string]bool{}}
	if err := src.QueryRowContext(ctx, `SELECT DATABASE(), VERSION(), @@hostname, @@port`).Scan(&p.SourceName, &p.SourceServer, &sHost, &sPort); err != nil {
		return nil, fmt.Errorf("source: %w", database.Explain(err))
	}
	var dName string
	if err := dst.QueryRowContext(ctx, `SELECT DATABASE(), @@hostname, @@port`).Scan(&dName, &dHost, &dPort); err != nil {
		return nil, fmt.Errorf("gameWE database: %w", database.Explain(err))
	}
	if strings.EqualFold(p.SourceName, dName) && sHost == dHost && sPort == dPort {
		return nil, ErrSameDB
	}
	var tz, sys string
	if err := src.QueryRowContext(ctx, `SELECT @@time_zone, @@system_time_zone`).Scan(&tz, &sys); err != nil {
		return nil, fmt.Errorf("source time zone: %w", err)
	}
	p.ServerZone = tz
	if strings.EqualFold(tz, "SYSTEM") {
		p.ServerZone = "SYSTEM = " + sys
	}

	// tables, their collations and columns
	rows, err := src.QueryContext(ctx, `SELECT TABLE_NAME, COALESCE(TABLE_COLLATION, '') FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`)
	if err != nil {
		return nil, fmt.Errorf("source tables: %w", err)
	}
	tables := map[string]string{} // lower -> name
	for rows.Next() {
		var t, coll string
		if err := rows.Scan(&t, &coll); err != nil {
			rows.Close()
			return nil, err
		}
		if !strings.HasPrefix(strings.ToLower(t), "hlstats_") {
			continue
		}
		tables[strings.ToLower(t)] = t
		p.Collations[coll]++
		if isLatin1(coll) {
			p.lat1[strings.ToLower(t)] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = src.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()`)
	if err != nil {
		return nil, fmt.Errorf("source columns: %w", err)
	}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return nil, err
		}
		t = strings.ToLower(t)
		if p.cols[t] == nil {
			p.cols[t] = map[string]bool{}
		}
		p.cols[t][strings.ToLower(c)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch {
	case p.has("hlstats_Events_Frags", "timestamp_id"):
		return nil, ErrSourceGWE
	case !p.has("hlstats_Events_Frags", "eventTime"):
		return nil, ErrNotCE
	}
	var missing []string
	for _, t := range requiredTables {
		if !p.hasTable(t) {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the HLstatsX:CE database lacks the tables %s", strings.Join(missing, ", "))
	}
	if p.hasTable("hlstats_Options") {
		var v sql.NullString
		err := src.QueryRowContext(ctx, `SELECT value FROM hlstats_Options WHERE keyname IN ('version', 'dbversion') ORDER BY keyname = 'version' DESC`).Scan(&v)
		if err == nil && v.Valid {
			p.SourceVersion = v.String
			var dbv sql.NullString
			if src.QueryRowContext(ctx, `SELECT value FROM hlstats_Options WHERE keyname = 'dbversion'`).Scan(&dbv) == nil && dbv.Valid && dbv.String != v.String {
				p.SourceVersion += " (database version " + dbv.String + ")"
			}
		}
	}

	// the gameWE database: new, or a fresh gameWE schema without data
	mp, err := database.PlanMigrations(ctx, dst)
	if errors.Is(err, database.ErrHLstatsXCE) {
		return nil, &NotNewError{"it is an HLstatsX:CE database itself"}
	}
	if err != nil {
		return nil, err
	}
	switch {
	case mp.Kind == database.KindEmpty:
		p.TargetNew = true
	case mp.Kind != database.KindGameWE:
		return nil, &NotNewError{"it has HLstatsX tables"}
	case !mp.UpToDate():
		return nil, &NotNewError{fmt.Sprintf("its schema version is %d, this collector needs %d", mp.Current, mp.Latest)}
	default:
		if why, err := hasData(ctx, dst.DB); err != nil {
			return nil, err
		} else if why != "" {
			return nil, &NotNewError{why}
		}
	}

	// time zone example
	{
		var first sql.NullString
		var unix sql.NullInt64
		err := src.QueryRowContext(ctx, `SELECT DATE_FORMAT(MIN(eventTime), '%Y-%m-%d %H:%i:%s'), UNIX_TIMESTAMP(MIN(eventTime)) FROM hlstats_Events_Frags`).Scan(&first, &unix)
		if err != nil {
			return nil, fmt.Errorf("source events: %w", err)
		}
		if first.Valid {
			p.FirstEvent = first.String
			u := unix.Int64
			if !opt.Zone.Server() {
				u = Unix(first.String, opt.Zone.Loc)
			}
			p.FirstEventUTC = time.Unix(u, 0).UTC().Format(time.DateTime)
		}
	}

	// double-encoded names
	if opt.Mojibake != MojibakeKeep {
		rows, err := src.QueryContext(ctx, `SELECT name FROM hlstats_PlayerNames WHERE name REGEXP '[ÃÂÅÄ]'`)
		if err != nil {
			return nil, fmt.Errorf("source player names: %w", err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return nil, err
			}
			if LooksDoubleEncoded(n) {
				p.DoubleEncoded++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		p.Repaired = p.repairs("hlstats_PlayerNames")
		if p.DoubleEncoded > 0 && !p.Repaired {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%d player names look like UTF-8 stored a second time (MÃ¼ller) in a utf8 table: -mojibake repair repairs them", p.DoubleEncoded))
		}
	}

	// steps and rows
	c := &conv{plan: p}
	for _, s := range c.steps() {
		if !s.available(p) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: the CE database has no %s, left out", s.name, strings.Join(s.missing(p), ", ")))
			continue
		}
		n, err := countRows(ctx, src.DB, tables[strings.ToLower(s.count)])
		if err != nil {
			return nil, err
		}
		p.Steps = append(p.Steps, StepInfo{Name: s.name, Source: s.from, Target: s.table, Rows: n, Note: s.note})
	}
	for _, sk := range notConverted {
		name, ok := tables[strings.ToLower(sk.Table)]
		if !ok {
			continue
		}
		n, err := countRows(ctx, src.DB, name)
		if err != nil {
			return nil, err
		}
		p.Skipped = append(p.Skipped, SkippedTable{Table: name, Rows: n, Why: sk.Why})
	}
	return p, nil
}

// hasData returns why a gameWE database is not new ("" if it is): the
// SQL script's check (players or servers) and clans.
func hasData(ctx context.Context, q *sql.DB) (string, error) {
	for _, t := range []string{"hlstats_Players", "hlstats_Servers", "hlstats_Clans", "hlstats_Events_Frags"} {
		var n int64
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return "", fmt.Errorf("%s: %w", t, err)
		}
		if n > 0 {
			return fmt.Sprintf("%s has %d rows", t, n), nil
		}
	}
	return "", nil
}

// countRows counts the rows of the CE table t (a name read from
// information_schema, never from input).
func countRows(ctx context.Context, q *sql.DB, t string) (int64, error) {
	if t == "" {
		return 0, nil
	}
	var n int64
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+strings.ReplaceAll(t, "`", "``")+"`").Scan(&n); err != nil {
		return 0, fmt.Errorf("source %s: %w", t, err)
	}
	return n, nil
}

func (p *Plan) hasTable(t string) bool { return p.cols[strings.ToLower(t)] != nil }

func (p *Plan) has(t, col string) bool { return p.cols[strings.ToLower(t)][strings.ToLower(col)] }

// repairs reports whether the text of the CE table t is repaired.
func (p *Plan) repairs(t string) bool {
	switch p.Mojibake {
	case MojibakeRepair:
		return true
	case MojibakeKeep:
		return false
	}
	return p.lat1[strings.ToLower(t)]
}

// Run converts. The gameWE database has the schema of this collector (the
// caller creates it first for a new database: Plan.TargetNew) and no data;
// Run holds the collector lock meanwhile. A failure leaves a partly filled
// gameWE database: empty it (gamewe db reset) and run again.
func Run(ctx context.Context, src, dst *database.DB, p *Plan, opt Options) (*Result, error) {
	if opt.Out == nil {
		opt.Out = io.Discard
	}
	if opt.BatchRows <= 0 {
		opt.BatchRows = 500
	}
	unlock, err := database.LockCollector(ctx, dst)
	if errors.Is(err, database.ErrCollectorRunning) {
		return nil, errors.New("a collector is running on the gameWE database (it holds the collector lock): stop it first (systemctl stop gamewe-collector, docker compose stop collector), then convert")
	}
	if err != nil {
		return nil, err
	}
	defer unlock()
	if why, err := hasData(ctx, dst.DB); err != nil {
		return nil, err
	} else if why != "" {
		return nil, &NotNewError{why}
	}
	// The CE session can only read.
	sconn, err := src.Conn(ctx)
	if err != nil {
		return nil, database.Explain(err)
	}
	defer sconn.Close()
	if _, err := sconn.ExecContext(ctx, `SET SESSION TRANSACTION READ ONLY`); err != nil {
		return nil, fmt.Errorf("source: read-only session: %w", err)
	}
	c := &conv{plan: p, opt: opt, src: sconn, dst: dst, ids: map[string]int64{}, actions: map[string]int64{}, awards: map[string]int64{}}
	start := time.Now()
	res := &Result{}
	steps := []step{}
	for _, s := range c.steps() {
		if s.available(p) {
			steps = append(steps, s)
		}
	}
	for i, s := range steps {
		fmt.Fprintf(opt.Out, "[%d/%d] %s: %s -> %s\n", i+1, len(steps), s.name, s.from, s.table)
		t0 := time.Now()
		before, err := c.count(ctx, s.table)
		if err != nil {
			return res, err
		}
		var read, skipped int64
		enable := c.disableKeys(ctx, s.table)
		for _, part := range s.parts {
			r, sk, err := c.run(ctx, part)
			read += r
			skipped += sk
			if err != nil {
				enable()
				return res, fmt.Errorf("%s (%s): %w", s.name, s.table, err)
			}
		}
		if err := enable(); err != nil {
			return res, fmt.Errorf("%s (%s): rebuilding the indexes: %w", s.name, s.table, err)
		}
		if s.after != nil {
			if err := s.after(ctx, c); err != nil {
				return res, fmt.Errorf("%s (%s): %w", s.name, s.table, err)
			}
		}
		after, err := c.count(ctx, s.table)
		if err != nil {
			return res, err
		}
		sr := StepResult{Name: s.name, Target: s.table, Read: read, Written: after - before, Skipped: skipped}
		res.Steps = append(res.Steps, sr)
		fmt.Fprintf(opt.Out, "        %d rows read, %d added (%s)\n", read, sr.Written, time.Since(t0).Round(10*time.Millisecond))
	}
	res.Repaired, res.Merged = c.repaired, c.merged
	if res.Summary, err = c.summary(ctx); err != nil {
		return res, err
	}
	res.Duration = time.Since(start)
	return res, nil
}

// conv is one conversion.
type conv struct {
	plan *Plan
	opt  Options
	src  *sql.Conn
	dst  *database.DB

	ids     map[string]int64 // gameWE ids by kind, game and code
	actions map[string]int64 // CE action id -> gameWE action id
	awards  map[string]int64 // CE award id -> gameWE award id

	repaired, merged int64
}

// bulkTables are the big tables whose indexes are built once after the
// copy (MyISAM DISABLE KEYS / ENABLE KEYS: about four times faster than
// row by row). They have no unique indexes, which DISABLE KEYS leaves on.
var bulkTables = func() map[string]bool {
	m := map[string]bool{"hlstats_server_load": true, "hlstats_Players_History": true}
	for _, t := range eventTables {
		m[t] = true
	}
	return m
}()

// disableKeys turns off the indexes of a big table (a constant name of
// bulkTables) and returns the function that builds them again; without the
// ALTER right, or on InnoDB, the rows are indexed one by one instead.
func (c *conv) disableKeys(ctx context.Context, table string) func() error {
	if !bulkTables[table] {
		return func() error { return nil }
	}
	if _, err := c.dst.ExecContext(ctx, `ALTER TABLE `+table+` DISABLE KEYS`); err != nil {
		fmt.Fprintf(c.opt.Out, "        (indexes kept on: %v)\n", err)
		return func() error { return nil }
	}
	return func() error { // also after a failure or Ctrl-C: never leave the indexes off
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		_, err := c.dst.ExecContext(ctx, `ALTER TABLE `+table+` ENABLE KEYS`)
		return err
	}
}

func (c *conv) count(ctx context.Context, table string) (int64, error) {
	var n int64
	if err := c.dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		return 0, fmt.Errorf("%s: %w", table, err)
	}
	return n, nil
}

// ts is the SQL expression of a CE event time: Unix seconds from the
// server, or the date and time as text for Unix().
func (c *conv) ts(col string) string {
	if c.plan.Zone.Server() {
		return "UNIX_TIMESTAMP(" + col + ")"
	}
	return "DATE_FORMAT(" + col + ", '%Y-%m-%d %H:%i:%s')"
}

// unix is the timestamp_id of a value of ts().
func (c *conv) unix(v *string) int64 {
	if v == nil {
		return 0
	}
	if c.plan.Zone.Server() {
		var n int64
		if _, err := fmt.Sscan(*v, &n); err != nil || n < 0 || n > maxTimestamp {
			return 0
		}
		return n
	}
	return Unix(*v, c.plan.Zone.Loc)
}

// text is a text value of the CE table t, repaired if it is double
// encoded and the plan repairs t.
func (c *conv) text(v *string, t string) any {
	if v == nil {
		return nil
	}
	if !c.plan.repairs(t) {
		return *v
	}
	r := Repair(*v)
	if r != *v {
		c.repaired++
	}
	return r
}

// str is a value taken over as it is (numbers, hashes, passwords).
func str(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

// textKey is a text value for a lookup ("" for NULL).
func (c *conv) textKey(v *string, t string) string {
	if s, ok := c.text(v, t).(string); ok {
		return s
	}
	return ""
}

// lookups are the queries of the gameWE ids; the gameWE database compares
// with its collation (utf8mb4_unicode_ci), as the joins of the SQL script.
var lookups = map[string]string{
	"game":     `SELECT id FROM hlstats_Games WHERE code = ?`,
	"map":      `SELECT id FROM hlstats_Maps WHERE game = ? AND map = ?`,
	"weapon":   `SELECT weaponId FROM hlstats_Weapons WHERE game = ? AND code = ?`,
	"team":     `SELECT teamId FROM hlstats_Teams WHERE game = ? AND code = ?`,
	"role":     `SELECT roleId FROM hlstats_Roles WHERE game = ? AND code = ?`,
	"action":   `SELECT id FROM hlstats_Actions WHERE game = ? AND code = ?`,
	"award":    `SELECT awardId FROM hlstats_Awards WHERE game = ? AND code = ?`,
	"location": `SELECT id FROM hlstats_Players_Locations WHERE lat = ? AND lng = ?`,
}

// maxIDs bounds the cache of id (one entry per location: as many as
// players); beyond it the ids are looked up each time.
const maxIDs = 500_000

// id returns the gameWE id of kind for (a, b) (game and code; code only
// for "game"), 0 if there is none. Only used once the table is complete.
func (c *conv) id(ctx context.Context, kind, a, b string) (int64, error) {
	key := kind + "\x00" + a + "\x00" + b
	if n, ok := c.ids[key]; ok {
		return n, nil
	}
	args := []any{a, b}
	if kind == "game" {
		args = args[:1]
	}
	var n int64
	err := c.dst.QueryRowContext(ctx, lookups[kind], args...).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		n, err = 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("%s id: %w", kind, err)
	}
	if len(c.ids) < maxIDs {
		c.ids[key] = n
	}
	return n, nil
}

// part copies the rows of one source query into a gameWE table.
type part struct {
	query  string   // on the CE database
	table  string   // the gameWE table
	cols   []string // its columns
	ignore bool     // INSERT IGNORE: rows whose key exists are left out
	upsert string   // ON DUPLICATE KEY UPDATE clause
	// row turns a CE row into the gameWE values; false leaves it out.
	row func(ctx context.Context, c *conv, v []*string) ([]any, bool, error)
	// exec runs an UPDATE per CE row instead of the INSERT.
	exec string
}

// maxBatchBytes bounds the values of one INSERT (max_allowed_packet is 16
// MiB by default on MariaDB 11).
const maxBatchBytes = 4 << 20

// run copies one part and returns the rows read and left out.
func (c *conv) run(ctx context.Context, p part) (read, skipped int64, err error) {
	rows, err := c.src.QueryContext(ctx, p.query)
	if err != nil {
		return 0, 0, fmt.Errorf("source: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, 0, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return 0, 0, err
	}
	raw := make([]sql.RawBytes, len(cols))
	times := make([]sql.NullTime, len(cols))
	layout := make([]string, len(cols)) // date and time columns: the driver returns time.Time (parseTime)
	dest := make([]any, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
		switch types[i].DatabaseTypeName() {
		case "DATETIME", "TIMESTAMP":
			layout[i], dest[i] = time.DateTime, &times[i]
		case "DATE":
			layout[i], dest[i] = time.DateOnly, &times[i]
		}
	}
	var batch []any
	n, size := 0, 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		if err := c.insert(ctx, p, batch, n); err != nil {
			return err
		}
		batch, n, size = batch[:0], 0, 0
		return nil
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return read, skipped, err
		}
		read++
		v := make([]*string, len(raw))
		for i, b := range raw {
			if layout[i] != "" {
				if times[i].Valid {
					s := dateTime(times[i].Time, layout[i])
					v[i] = &s
				}
				continue
			}
			if b != nil {
				s := string(b)
				v[i] = &s
				size += len(s)
			}
		}
		out, keep, err := p.row(ctx, c, v)
		if err != nil {
			return read, skipped, err
		}
		if !keep {
			skipped++
			continue
		}
		if p.exec != "" {
			if _, err := c.dst.ExecContext(ctx, p.exec, out...); err != nil {
				return read, skipped, err
			}
			continue
		}
		batch = append(batch, out...)
		n++
		if n >= c.opt.BatchRows || size >= maxBatchBytes || len(batch)+len(p.cols) > 60000 {
			if err := flush(); err != nil {
				return read, skipped, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return read, skipped, fmt.Errorf("source: %w", err)
	}
	return read, skipped, flush()
}

// dateTime is a date or date and time of CE as text; the zero date
// ('0000-00-00', time.Time{} from the driver) stays the zero date.
func dateTime(t time.Time, layout string) string {
	if t.IsZero() {
		return strings.NewReplacer("2006", "0000", "01", "00", "02", "00", "15", "00", "04", "00", "05", "00").Replace(layout)
	}
	return t.UTC().Format(layout) // the connection reads them as UTC: the wall clock as stored
}

// insert writes n rows of p.
func (c *conv) insert(ctx context.Context, p part, vals []any, n int) error {
	var b strings.Builder
	b.WriteString("INSERT ")
	if p.ignore {
		b.WriteString("IGNORE ")
	}
	b.WriteString("INTO " + p.table + " (" + strings.Join(p.cols, ", ") + ") VALUES ")
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(p.cols)), ", ") + ")"
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(one)
	}
	if p.upsert != "" {
		b.WriteString(" ON DUPLICATE KEY UPDATE " + p.upsert)
	}
	res, err := c.dst.ExecContext(ctx, b.String(), vals...)
	if err != nil {
		return err
	}
	if p.upsert != "" { // MariaDB counts an updated row twice
		if a, err := res.RowsAffected(); err == nil && a > int64(n) {
			c.merged += a - int64(n)
		}
	}
	return nil
}

// compare is a line of the summary: the CE table it needs and the
// counts on both sides (dst "" counts the same as src).
type compare struct{ what, table, src, dst string }

// summary compares the rows of the main tables (and all kills).
func (c *conv) summary(ctx context.Context) ([]Compare, error) {
	list := []compare{
		{"players", "hlstats_Players", "SELECT COUNT(*) FROM hlstats_Players", ""},
		{"unique ids", "hlstats_PlayerUniqueIds", "SELECT COUNT(*) FROM hlstats_PlayerUniqueIds", ""},
		{"player names", "hlstats_PlayerNames", "SELECT COUNT(*) FROM hlstats_PlayerNames", ""},
		{"clans", "hlstats_Clans", "SELECT COUNT(*) FROM hlstats_Clans", ""},
		{"servers", "hlstats_Servers", "SELECT COUNT(*) FROM hlstats_Servers", ""},
		{"history days", "hlstats_Players_History", "SELECT COUNT(*) FROM hlstats_Players_History WHERE eventTime IS NOT NULL", "SELECT COUNT(*) FROM hlstats_Players_History"},
		{"award winners", "hlstats_Players_Awards", "SELECT COUNT(*) FROM hlstats_Players_Awards", "SELECT COUNT(*) FROM hlstats_Awards_History"},
		{"kills of all players", "hlstats_Players", "SELECT COALESCE(SUM(kills), 0) FROM hlstats_Players", ""},
	}
	for _, t := range append(eventTables, "hlstats_server_load", "hlstats_Trend") {
		list = append(list, compare{t, t, "SELECT COUNT(*) FROM " + t, ""})
	}
	var out []Compare
	for _, e := range list {
		if !c.plan.hasTable(e.table) {
			continue
		}
		if e.dst == "" {
			e.dst = e.src
		}
		cmp := Compare{What: e.what}
		if err := c.src.QueryRowContext(ctx, e.src).Scan(&cmp.CE); err != nil {
			return out, fmt.Errorf("source: %w", err)
		}
		if err := c.dst.QueryRowContext(ctx, e.dst).Scan(&cmp.GameWE); err != nil {
			return out, err
		}
		out = append(out, cmp)
	}
	return out, nil
}
