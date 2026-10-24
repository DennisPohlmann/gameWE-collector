// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	"gamewe-collector/internal/schema"
)

// MigrationsTable records the applied schema versions.
const MigrationsTable = "gamewe_schema_migrations"

// SlowRows is the table size (estimated rows) from which a step that
// rebuilds or indexes the table is marked as slow in the plan.
const SlowRows = 500_000

// Database kinds found by PlanMigrations.
const (
	KindEmpty    = "empty"    // no hlstats_* table: a new database
	KindGameWE   = "gameWE"   // a gameWE database
	KindHLstatsX = "HLstatsX" // an HLstatsX (CE) database, no gameWE tables yet
)

// Applied is a row of MigrationsTable.
type Applied struct {
	Version   int
	Name      string
	AppliedAt time.Time
	Checksum  string
	Note      string
}

// Step is one change of a migration plan.
type Step struct {
	Version int    // the migration; 0 for steps outside one (version table, repair, rights)
	Name    string // the migration's name, or "setup", "repair", "rights"
	What    string // what happens, for people
	Table   string // the table it changes, "" if none or many
	Rows    int64  // estimated rows of Table (-1 unknown)
	Slow    bool   // rebuilds or indexes a table with at least SlowRows rows

	kind  stepKind
	stmts []string
	rec   *Migration // kind record
	note  string     // kind record
}

type stepKind int

const (
	stepExec    stepKind = iota // stmts
	stepScript                  // stmts[0] is a script for schema.Split
	stepRecord                  // insert rec into MigrationsTable
	stepGrants                  // run the grants script, failures are warnings
	stepVersion                 // create MigrationsTable
)

// Plan is what `gamewe db migrate` would do; read only to make.
type Plan struct {
	Dialect  string
	Kind     string    // KindEmpty, KindGameWE or KindHLstatsX
	Versions bool      // MigrationsTable exists
	Applied  []Applied // its rows, oldest first
	Current  int       // the highest applied version, 0 for none
	Latest   int       // Latest()
	Pending  []Migration
	Missing  []Requirement // tables, columns and indexes of internal/schema the database lacks
	Steps    []Step
	Warnings []string // e.g. a recorded migration that differs from this binary's
}

// UpToDate reports whether the database has the schema of this collector.
func (p *Plan) UpToDate() bool { return len(p.Steps) == 0 }

// querier is *sql.DB, *sql.Conn or *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// catalog is what the database has, names in lower case.
type catalog struct {
	tables  map[string]bool
	columns map[string]map[string]bool
	indexes map[string]map[string]bool
	rows    map[string]int64
}

func (c catalog) hasColumn(table, col string) bool {
	return c.columns[strings.ToLower(table)][strings.ToLower(col)]
}

func (c catalog) hasIndex(table, name string) bool {
	return c.indexes[strings.ToLower(table)][strings.ToLower(name)]
}

func (c catalog) rowsOf(table string) int64 {
	if n, ok := c.rows[strings.ToLower(table)]; ok {
		return n
	}
	return -1
}

func readCatalog(ctx context.Context, q querier, postgres bool) (catalog, error) {
	c := catalog{tables: map[string]bool{}, columns: map[string]map[string]bool{}, indexes: map[string]map[string]bool{}, rows: map[string]int64{}}
	type query struct {
		sql string
		add func(a, b string)
	}
	queries := []query{
		{`SELECT TABLE_NAME, '' FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`,
			func(t, _ string) { c.tables[t] = true }},
		{`SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()`,
			func(t, col string) { set(c.columns, t)[col] = true }},
		{`SELECT DISTINCT TABLE_NAME, INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()`,
			func(t, ix string) { set(c.indexes, t)[ix] = true }},
		{`SELECT TABLE_NAME, COALESCE(TABLE_ROWS, -1) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`,
			func(t, n string) { c.rows[t] = atoi64(n) }},
	}
	if postgres {
		queries = []query{
			{`SELECT table_name, '' FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`,
				func(t, _ string) { c.tables[t] = true }},
			{`SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()`,
				func(t, col string) { set(c.columns, t)[col] = true }},
			{`SELECT tablename, indexname FROM pg_indexes WHERE schemaname = current_schema()`,
				func(t, ix string) { set(c.indexes, t)[ix] = true }},
			{`SELECT c.relname, CAST(c.reltuples AS bigint) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')`,
				func(t, n string) { c.rows[t] = atoi64(n) }},
		}
	}
	for _, qu := range queries {
		rows, err := q.QueryContext(ctx, qu.sql)
		if err != nil {
			return c, fmt.Errorf("schema check: %w", err)
		}
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				rows.Close()
				return c, fmt.Errorf("schema check: %w", err)
			}
			qu.add(strings.ToLower(a), strings.ToLower(b))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return c, fmt.Errorf("schema check: %w", err)
		}
	}
	return c, nil
}

func set(m map[string]map[string]bool, k string) map[string]bool {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	return m[k]
}

func atoi64(s string) int64 {
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		return -1
	}
	return n
}

// readApplied returns the rows of MigrationsTable (nil if it does not exist).
func readApplied(ctx context.Context, q querier, cat catalog) ([]Applied, error) {
	if !cat.tables[MigrationsTable] {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT version, name, applied_at, checksum, note FROM `+MigrationsTable+` ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MigrationsTable, err)
	}
	defer rows.Close()
	var out []Applied
	for rows.Next() {
		var a Applied
		var at int64
		if err := rows.Scan(&a.Version, &a.Name, &at, &a.Checksum, &a.Note); err != nil {
			return nil, fmt.Errorf("%s: %w", MigrationsTable, err)
		}
		a.AppliedAt = time.Unix(at, 0).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// ErrHLstatsXCE: HLstatsX:CE stores events with eventTime and other ids
// than the HLstatsX variant gameWE uses; its data is copied into a new
// database instead.
var ErrHLstatsXCE = errors.New("this is an HLstatsX:CE database (events with eventTime, not timestamp_id): it cannot be upgraded in place. " +
	"Create a new gameWE database and copy the data, see docs/MIGRATION-HLSTATSX.md")

// isCE reports whether the database is an HLstatsX:CE one.
func isCE(cat catalog) bool {
	return cat.hasColumn("hlstats_Events_Frags", "eventTime") && !cat.hasColumn("hlstats_Events_Frags", "timestamp_id")
}

// PlanMigrations compares the database with this collector's schema and
// returns what `gamewe db migrate` would do. It only reads.
func PlanMigrations(ctx context.Context, db *DB) (*Plan, error) {
	return plan(ctx, db.DB, db.Type)
}

func plan(ctx context.Context, q querier, dialect string) (*Plan, error) {
	pg := dialect == schema.Postgres
	tables, err := schema.Tables(dialect)
	if err != nil {
		return nil, err
	}
	cat, err := readCatalog(ctx, q, pg)
	if err != nil {
		return nil, err
	}
	applied, err := readApplied(ctx, q, cat)
	if err != nil {
		return nil, err
	}
	p := &Plan{Dialect: dialect, Versions: cat.tables[MigrationsTable], Applied: applied, Latest: Latest()}
	done := map[int]bool{}
	for _, a := range applied {
		done[a.Version] = true
		p.Current = max(p.Current, a.Version)
	}
	known := map[int]Migration{}
	for _, m := range Migrations {
		known[m.Version] = m
	}
	for _, a := range applied {
		if m, ok := known[a.Version]; ok && a.Checksum != m.Checksum() {
			p.Warnings = append(p.Warnings, fmt.Sprintf("migration %d %s differs from the one recorded on %s (this collector: %s)", a.Version, a.Name, a.AppliedAt.Format(time.DateOnly), m.Name))
		}
	}
	if p.Current > p.Latest {
		p.Warnings = append(p.Warnings, fmt.Sprintf("the database has schema version %d, this collector only knows up to %d: update the collector", p.Current, p.Latest))
	}

	if isCE(cat) {
		return nil, ErrHLstatsXCE
	}
	hl := 0
	for t := range cat.tables {
		if strings.HasPrefix(t, "hlstats_") {
			hl++
		}
	}
	switch {
	case hl == 0:
		p.Kind = KindEmpty
	case cat.tables["hlstats_gamewe_jobs"] || cat.tables["hlstats_events_latency"]:
		p.Kind = KindGameWE
	default:
		p.Kind = KindHLstatsX
	}
	for _, m := range Migrations {
		if !done[m.Version] {
			p.Pending = append(p.Pending, m)
		}
	}
	p.Missing = missing(tables, cat, pg)

	if !p.Versions {
		p.Steps = append(p.Steps, Step{Name: "setup", What: "create the table " + MigrationsTable + " (schema versions)", kind: stepVersion})
	}
	if p.Kind == KindEmpty {
		p.Steps = append(p.Steps,
			Step{Version: 1, Name: "baseline", What: fmt.Sprintf("create all %d tables of the gameWE schema", len(tables)), kind: stepScript, stmts: []string{schema.Schema(dialect)}},
			Step{Version: 1, Name: "baseline", What: "load the reference data (games, teams, actions, weapons, awards, ranks, options)", kind: stepScript, stmts: []string{schema.Seed(dialect)}})
		for _, m := range p.Pending {
			p.Steps = append(p.Steps, Step{Version: m.Version, Name: m.Name, What: "record version " + fmt.Sprint(m.Version) + " (part of the new schema)", kind: stepRecord, rec: &m, note: "new database"})
		}
		p.Steps = append(p.Steps, grantsStep())
		return p, nil
	}

	synced := false
	for _, m := range p.Pending {
		if m.Version == 1 {
			p.Steps = append(p.Steps, Step{Version: 1, Name: m.Name, What: "record the existing " + p.Kind + " database as baseline (nothing is changed)",
				kind: stepRecord, rec: &m, note: "existing " + p.Kind + " database"})
			continue
		}
		if m.Sync || len(m.Drop) > 0 {
			steps := syncSteps(tables, cat, pg, m.Sync && !synced, m.Drop)
			synced = synced || m.Sync
			for i := range steps {
				steps[i].Version, steps[i].Name = m.Version, m.Name
			}
			p.Steps = append(p.Steps, steps...)
		}
		if m.Seed {
			p.Steps = append(p.Steps, Step{Version: m.Version, Name: m.Name, What: "add missing reference data (only rows that do not exist; existing rows stay as they are)",
				kind: stepScript, stmts: []string{schema.Seed(dialect)}})
		}
		p.Steps = append(p.Steps, Step{Version: m.Version, Name: m.Name, What: "record version " + fmt.Sprint(m.Version), kind: stepRecord, rec: &m, note: "applied"})
	}
	if !synced && len(p.Missing) > 0 {
		// All versions are applied, but something was removed by hand.
		p.Steps = append(p.Steps, syncSteps(tables, cat, pg, true, nil)...)
		for i := range p.Steps {
			if p.Steps[i].Name == "" {
				p.Steps[i].Name = "repair"
			}
		}
	}
	for _, s := range p.Steps {
		if s.kind == stepExec || s.kind == stepScript {
			p.Steps = append(p.Steps, grantsStep())
			break
		}
	}
	return p, nil
}

func grantsStep() Step {
	return Step{Name: "rights", What: "grant the web users their rights on the tables (internal/schema grants.sql)", kind: stepGrants}
}

// missing lists what the database lacks of the schema: whole tables, or the
// columns and indexes of tables that exist.
func missing(tables []schema.Table, cat catalog, pg bool) []Requirement {
	var out []Requirement
	for _, t := range tables {
		if !cat.tables[strings.ToLower(t.Name)] {
			out = append(out, Requirement{Table: t.Name})
			continue
		}
		for _, c := range t.Columns {
			if !cat.hasColumn(t.Name, c.Name) {
				out = append(out, Requirement{Table: t.Name, Column: c.Name})
			}
		}
		for _, ix := range t.Indexes {
			if pg && ix.Primary {
				continue // the primary key comes with the table
			}
			if !cat.hasIndex(t.Name, ix.Name) {
				out = append(out, Requirement{Table: t.Name, Index: ix.Name})
			}
		}
	}
	return out
}

// syncSteps returns one step per table: create it, or add its missing
// columns and indexes (MySQL: in one ALTER TABLE, so a table is rebuilt only
// once) and drop the replaced indexes.
func syncSteps(tables []schema.Table, cat catalog, pg, sync bool, drops []DropIndex) []Step {
	var out []Step
	for _, t := range tables {
		lt := strings.ToLower(t.Name)
		var dropHere []DropIndex
		for _, d := range drops {
			if strings.EqualFold(d.Table, t.Name) && cat.hasIndex(t.Name, indexName(pg, t.Name, d.Index)) {
				dropHere = append(dropHere, d)
			}
		}
		if !cat.tables[lt] {
			if !sync {
				continue
			}
			stmts := []string{createIfNotExists(t.Create)}
			for _, ix := range t.Indexes {
				if pg && !ix.Primary {
					stmts = append(stmts, createIfNotExists(ix.Def))
				}
			}
			out = append(out, Step{What: "create table " + t.Name, Table: t.Name, Rows: 0, kind: stepExec, stmts: stmts})
			continue
		}
		var addCols, addIdx []string
		var clauses, pgStmts []string
		if sync {
			prev := ""
			for _, c := range t.Columns {
				if cat.hasColumn(t.Name, c.Name) {
					prev = c.Name
					continue
				}
				addCols = append(addCols, c.Name)
				if pg {
					pgStmts = append(pgStmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", t.Name, c.Name, c.Def))
				} else {
					pos := " FIRST"
					if prev != "" {
						pos = " AFTER `" + prev + "`"
					}
					clauses = append(clauses, "ADD COLUMN `"+c.Name+"` "+c.Def+pos)
				}
				prev = c.Name
			}
			for _, ix := range t.Indexes {
				if (pg && ix.Primary) || cat.hasIndex(t.Name, ix.Name) {
					continue
				}
				addIdx = append(addIdx, ix.Name)
				if pg {
					pgStmts = append(pgStmts, createIfNotExists(ix.Def))
				} else {
					clauses = append(clauses, "ADD "+ix.Def)
				}
			}
		}
		var dropped []string
		for _, d := range dropHere {
			// The replacement exists, or is added now (sync) or by the
			// repair of the same run: every index of the schema is.
			repl := indexName(pg, t.Name, d.ReplacedBy)
			replaced := cat.hasIndex(t.Name, repl)
			for _, ix := range t.Indexes {
				replaced = replaced || (sync && strings.EqualFold(ix.Name, repl))
			}
			if !replaced {
				continue
			}
			dropped = append(dropped, d.Index+" (replaced by "+d.ReplacedBy+")")
			if pg {
				pgStmts = append(pgStmts, "DROP INDEX IF EXISTS "+indexName(pg, t.Name, d.Index))
			} else {
				clauses = append(clauses, "DROP KEY `"+d.Index+"`")
			}
		}
		if len(addCols)+len(addIdx)+len(dropped) == 0 {
			continue
		}
		var what []string
		if len(addCols) > 0 {
			what = append(what, "add columns "+strings.Join(addCols, ", "))
		}
		if len(addIdx) > 0 {
			what = append(what, "add indexes "+strings.Join(addIdx, ", "))
		}
		if len(dropped) > 0 {
			what = append(what, "drop indexes "+strings.Join(dropped, ", "))
		}
		s := Step{What: "alter table " + t.Name + ": " + strings.Join(what, "; "), Table: t.Name, Rows: cat.rowsOf(t.Name), kind: stepExec}
		s.Slow = s.Rows >= SlowRows
		if pg {
			s.stmts = pgStmts
		} else {
			s.stmts = []string{"ALTER TABLE `" + t.Name + "`\n  " + strings.Join(clauses, ",\n  ")}
		}
		out = append(out, s)
	}
	return out
}

// indexName is the name of the MySQL key name on the database: the same,
// or <table>_<name> in lower case on PostgreSQL (tools/schemagen).
func indexName(pg bool, table, name string) string {
	if pg {
		return strings.ToLower(table + "_" + name)
	}
	return name
}

// createIfNotExists turns CREATE TABLE / CREATE [UNIQUE] INDEX into the
// IF NOT EXISTS form.
func createIfNotExists(stmt string) string {
	for _, p := range []string{"CREATE TABLE ", "CREATE UNLOGGED TABLE ", "CREATE UNIQUE INDEX ", "CREATE INDEX "} {
		if strings.HasPrefix(stmt, p) {
			return p + "IF NOT EXISTS " + stmt[len(p):]
		}
	}
	return stmt
}

// MigrateOptions are the settings of Migrate.
type MigrateOptions struct {
	Out io.Writer // progress, one line per step
	// LockWait is how long to wait for another `gamewe db migrate` on the
	// same database (default 10 minutes).
	LockWait time.Duration
	// Waiting is called once if another migration holds the lock.
	Waiting func()
}

// MigrateResult is what Migrate did.
type MigrateResult struct {
	Plan     *Plan    // the plan it carried out (made again under the lock)
	Warnings []string // failed grants and the like: the schema is up to date anyway
}

// Migrate brings the database to this collector's schema. It takes a lock
// (only one migration per database at a time), makes the plan again under
// the lock, checks the rights of the database user and carries out the
// steps. It never deletes rows or drops tables or columns.
func Migrate(ctx context.Context, db *DB, opt MigrateOptions) (*MigrateResult, error) {
	if opt.Out == nil {
		opt.Out = io.Discard
	}
	if opt.LockWait == 0 {
		opt.LockWait = 10 * time.Minute
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, Explain(err)
	}
	defer conn.Close()
	unlock, err := migrateLock(ctx, conn, db.Postgres(), opt.LockWait, opt.Waiting)
	if err != nil {
		return nil, err
	}
	defer unlock()

	p, err := plan(ctx, conn, db.Type)
	if err != nil {
		return nil, err
	}
	res := &MigrateResult{Plan: p}
	if p.UpToDate() {
		return res, nil
	}
	if err := checkRights(ctx, conn, db.Postgres(), p); err != nil {
		return res, err
	}
	var tx bool // PostgreSQL: each migration in one transaction
	cur := -1
	commit := func() error {
		if tx {
			tx = false
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return fmt.Errorf("commit: %w", err)
			}
		}
		return nil
	}
	for i, s := range p.Steps {
		if db.Postgres() && s.Version != cur {
			if err := commit(); err != nil {
				return res, err
			}
			cur = s.Version
			if s.Version > 0 {
				if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
					return res, err
				}
				tx = true
			}
		}
		label := s.Name
		if s.Version > 0 {
			label = fmt.Sprintf("v%d %s", s.Version, s.Name)
		}
		fmt.Fprintf(opt.Out, "[%d/%d] %s: %s", i+1, len(p.Steps), label, s.What)
		if s.Rows > 0 {
			fmt.Fprintf(opt.Out, " (about %d rows)", s.Rows)
		}
		fmt.Fprintln(opt.Out)
		if s.Slow {
			fmt.Fprintf(opt.Out, "        %s\n", slowNote(s.Rows))
		}
		start := time.Now()
		warn, err := runStep(ctx, conn, db.Type, s)
		if err != nil {
			if tx {
				conn.ExecContext(context.Background(), "ROLLBACK") // before rightsHint: an aborted transaction answers nothing
			}
			return res, fmt.Errorf("step %d (%s: %s): %w", i+1, label, s.What, rightsHint(ctx, conn, db.Postgres(), err))
		}
		res.Warnings = append(res.Warnings, warn...)
		if d := time.Since(start); d > 2*time.Second {
			fmt.Fprintf(opt.Out, "        done in %s\n", d.Round(100*time.Millisecond))
		}
	}
	if err := commit(); err != nil {
		return res, err
	}
	return res, nil
}

// slowNote is the warning before a step on a big table.
func slowNote(rows int64) string {
	return fmt.Sprintf("NOTE: the table has about %d rows; it is locked while it is rebuilt or indexed, this can take minutes (about 3 s per million rows on MariaDB). Make sure you have a current backup.", rows)
}

// SlowNote is the warning for a slow step, for the plan output.
func (s Step) SlowNote() string { return slowNote(s.Rows) }

func runStep(ctx context.Context, conn *sql.Conn, dialect string, s Step) ([]string, error) {
	switch s.kind {
	case stepVersion:
		_, err := conn.ExecContext(ctx, versionTableSQL(dialect))
		return nil, err
	case stepRecord:
		q := `INSERT IGNORE INTO ` + MigrationsTable + ` (version, name, applied_at, checksum, note) VALUES (?, ?, ?, ?, ?)`
		if dialect == schema.Postgres {
			q = Rebind(`INSERT INTO `+MigrationsTable+` (version, name, applied_at, checksum, note) VALUES (?, ?, ?, ?, ?)`) + ` ON CONFLICT DO NOTHING`
		}
		_, err := conn.ExecContext(ctx, q, s.rec.Version, s.rec.Name, time.Now().Unix(), s.rec.Checksum(), s.note)
		return nil, err
	case stepExec:
		for _, q := range s.stmts {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case stepScript:
		return nil, runScript(ctx, conn, dialect, s.stmts[0])
	case stepGrants:
		return RunGrants(ctx, conn, dialect), nil
	}
	return nil, fmt.Errorf("unknown step %d", s.kind)
}

// runScript runs every statement of a script; psql's \gexec runs the
// statements the query returns.
func runScript(ctx context.Context, q querier, dialect, script string) error {
	for _, st := range schema.Split(dialect, script) {
		if !st.GExec {
			if _, err := q.ExecContext(ctx, st.SQL); err != nil {
				return fmt.Errorf("%.80s: %w", st.SQL, err)
			}
			continue
		}
		rows, err := q.QueryContext(ctx, st.SQL)
		if err != nil {
			return err
		}
		var gen []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return err
			}
			gen = append(gen, s)
		}
		rows.Close()
		for _, g := range gen {
			if _, err := q.ExecContext(ctx, g); err != nil {
				return fmt.Errorf("%.80s: %w", g, err)
			}
		}
	}
	return nil
}

// RunGrants runs the grants script of the dialect statement by statement
// and returns the statements that failed (as warnings: missing rights of the
// web users do not stop the collector). The block for the optional user
// gamewe_admin reads mysql.user, which the collector user may not: its
// failure is not reported.
func RunGrants(ctx context.Context, q querier, dialect string) []string {
	var warn []string
	for _, st := range schema.Split(dialect, schema.Grants(dialect)) {
		var err error
		if st.GExec {
			err = runScript(ctx, q, dialect, st.SQL+` \gexec`)
		} else {
			_, err = q.ExecContext(ctx, st.SQL)
		}
		if err != nil && !strings.Contains(st.SQL, "mysql.user") {
			first := strings.SplitN(st.SQL, "\n", 2)[0]
			warn = append(warn, fmt.Sprintf("%.100s: %v", first, err))
		}
	}
	return warn
}

// RunSeed adds the missing reference data (internal/schema seed.sql: only
// rows that do not exist, existing rows stay as they are).
func RunSeed(ctx context.Context, db *DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return runScript(ctx, conn, db.Type, schema.Seed(db.Type))
}

func versionTableSQL(dialect string) string {
	if dialect == schema.Postgres {
		return `CREATE TABLE IF NOT EXISTS ` + MigrationsTable + ` (
  version integer NOT NULL PRIMARY KEY,
  name varchar(64) NOT NULL,
  applied_at bigint NOT NULL,
  checksum varchar(64) NOT NULL,
  note varchar(255) NOT NULL DEFAULT ''
)`
	}
	return "CREATE TABLE IF NOT EXISTS `" + MigrationsTable + "` (\n" +
		"  `version` int(10) unsigned NOT NULL,\n" +
		"  `name` varchar(64) NOT NULL,\n" +
		"  `applied_at` bigint(20) NOT NULL,\n" +
		"  `checksum` varchar(64) NOT NULL,\n" +
		"  `note` varchar(255) NOT NULL DEFAULT '',\n" +
		"  PRIMARY KEY (`version`)\n" +
		") DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='gameWE schema versions (gamewe db migrate)'"
}

// Lock keys: one migration per database at a time.
const (
	migrateLockPrefix = "gamewe_db_migrate."
	migrateLockKey    = 0x67616d6577650001 // pg_advisory_lock, per database
)

// migrateLock takes the migration lock on conn, waiting up to wait.
func migrateLock(ctx context.Context, conn *sql.Conn, postgres bool, wait time.Duration, waiting func()) (func(), error) {
	try := func() (bool, error) {
		var got sql.NullInt64
		if postgres {
			var ok bool
			err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, int64(migrateLockKey)).Scan(&ok)
			return ok, err
		}
		err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(LEFT(CONCAT(?, DATABASE()), 64), 0)`, migrateLockPrefix).Scan(&got)
		return got.Valid && got.Int64 == 1, err
	}
	deadline := time.Now().Add(wait)
	told := false
	for {
		ok, err := try()
		if err != nil {
			return nil, fmt.Errorf("migration lock: %w", err)
		}
		if ok {
			break
		}
		if !told && waiting != nil {
			waiting()
			told = true
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another gamewe db migrate has been running on this database for more than %s", wait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return func() {
		if postgres {
			conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(migrateLockKey))
		} else {
			conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK(LEFT(CONCAT(?, DATABASE()), 64))`, migrateLockPrefix)
		}
	}, nil
}

// RightsError is returned when the database user may not change the
// structure; Fix holds the statements a database admin runs once.
type RightsError struct {
	User   string
	Reason string
	Fix    string
}

func (e *RightsError) Error() string {
	return fmt.Sprintf("the database user %s may not change the table structure (%s).\n"+
		"gamewe db migrate needs these rights once; run as a database admin:\n\n%s\n\nthen run gamewe db migrate again", e.User, e.Reason, e.Fix)
}

// isDenied reports whether err is a missing-privilege error.
func isDenied(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1044, 1142, 1143, 1227, 1370: // db / table / column access denied, need privilege
			return true
		}
	}
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "42501" // insufficient_privilege (also "must be owner")
}

// rightsHint turns a privilege error into a RightsError.
func rightsHint(ctx context.Context, conn *sql.Conn, postgres bool, err error) error {
	if !isDenied(err) {
		return err
	}
	if re := rightsError(ctx, conn, postgres, err.Error()); re != nil {
		return re
	}
	return err
}

func rightsError(ctx context.Context, conn *sql.Conn, postgres bool, reason string) *RightsError {
	if postgres {
		var user, dbname string
		if conn.QueryRowContext(ctx, `SELECT current_user, current_database()`).Scan(&user, &dbname) != nil {
			return nil
		}
		return &RightsError{User: user, Reason: reason, Fix: pgFix(user, dbname)}
	}
	var cu, dbname string
	if conn.QueryRowContext(ctx, `SELECT CURRENT_USER(), DATABASE()`).Scan(&cu, &dbname) != nil {
		return nil
	}
	user, host := cu, "%"
	if i := strings.LastIndexByte(cu, '@'); i >= 0 {
		user, host = cu[:i], cu[i+1:]
	}
	q := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	who := fmt.Sprintf("'%s'@'%s'", q(user), q(host))
	return &RightsError{User: who, Reason: reason,
		Fix: fmt.Sprintf("  GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX ON `%s`.* TO %s WITH GRANT OPTION;",
			strings.ReplaceAll(dbname, "`", "``"), who)}
}

// pgFix is what a PostgreSQL superuser runs: the collector role may create
// tables and owns the existing ones (only the owner may alter a table).
func pgFix(user, dbname string) string {
	qi := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	ql := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return fmt.Sprintf(`  \c %s
  GRANT USAGE, CREATE ON SCHEMA public TO %s;
  DO $$ DECLARE t record; BEGIN
    FOR t IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND (tablename LIKE 'hlstats\_%%' OR tablename = %s) LOOP
      EXECUTE format('ALTER TABLE public.%%I OWNER TO %%I', t.tablename, %s);
    END LOOP;
  END $$;`, qi(dbname), qi(user), ql(MigrationsTable), ql(user))
}

// checkRights tests, before the first change, that the user may create and
// alter tables: on MySQL by creating and altering the version table
// (which the plan creates anyway), on PostgreSQL by the schema right and
// the owners of the tables.
func checkRights(ctx context.Context, conn *sql.Conn, postgres bool, p *Plan) error {
	if postgres {
		reason, err := pgRights(ctx, conn)
		if err != nil {
			return err
		}
		if reason != "" {
			if re := rightsError(ctx, conn, true, reason); re != nil {
				return re
			}
			return errors.New(reason)
		}
		return nil
	}
	for _, q := range []string{versionTableSQL(p.Dialect),
		"ALTER TABLE `" + MigrationsTable + "` COMMENT = 'gameWE schema versions (gamewe db migrate)'"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return rightsHint(ctx, conn, false, err)
		}
	}
	return nil
}

// pgRights returns why the current role cannot migrate ("" if it can).
func pgRights(ctx context.Context, q querier) (string, error) {
	var create bool
	if err := q.QueryRowContext(ctx, `SELECT has_schema_privilege(current_schema(), 'CREATE')`).Scan(&create); err != nil {
		return "", fmt.Errorf("rights check: %w", err)
	}
	var foreign int
	var example sql.NullString
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), MIN(tablename) FROM pg_tables
		WHERE schemaname = current_schema() AND (tablename LIKE 'hlstats\_%' OR tablename = $1)
		AND NOT pg_has_role(current_user, tableowner, 'MEMBER')`, MigrationsTable).Scan(&foreign, &example)
	if err != nil {
		return "", fmt.Errorf("rights check: %w", err)
	}
	switch {
	case !create && foreign > 0:
		return fmt.Sprintf("no CREATE right on the schema, and %d tables (e.g. %s) belong to another role", foreign, example.String), nil
	case !create:
		return "no CREATE right on the schema", nil
	case foreign > 0:
		return fmt.Sprintf("%d tables (e.g. %s) belong to another role; only the owner may alter a table", foreign, example.String), nil
	}
	return "", nil
}

// PostgresRights returns, read only, why the database user cannot migrate
// a PostgreSQL database, with the fix ("" and nil if it can or the database
// is MySQL).
func PostgresRights(ctx context.Context, db *DB) (string, error) {
	if !db.Postgres() {
		return "", nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	reason, err := pgRights(ctx, conn)
	if err != nil || reason == "" {
		return "", err
	}
	if re := rightsError(ctx, conn, true, reason); re != nil {
		return re.Error(), nil
	}
	return reason, nil
}
