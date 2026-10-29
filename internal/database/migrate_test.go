// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"slices"
	"strings"
	"testing"

	"gamewe-collector/internal/schema"
)

// fullCatalog is a database with every table, column and index of the
// schema.
func fullCatalog(t *testing.T, dialect string) ([]schema.Table, catalog) {
	t.Helper()
	tables, err := schema.Tables(dialect)
	if err != nil {
		t.Fatal(err)
	}
	c := catalog{tables: map[string]bool{}, columns: map[string]map[string]bool{}, indexes: map[string]map[string]bool{}, rows: map[string]int64{}, engines: map[string]string{}, types: map[string]map[string]string{}}
	for _, tb := range tables {
		lt := strings.ToLower(tb.Name)
		c.tables[lt] = true
		c.types[lt] = map[string]string{}
		for _, col := range tb.Columns {
			set(c.columns, lt)[strings.ToLower(col.Name)] = true
			c.types[lt][strings.ToLower(col.Name)] = columnType(col.Def)
		}
		for _, ix := range tb.Indexes {
			set(c.indexes, lt)[strings.ToLower(ix.Name)] = true
		}
	}
	return tables, c
}

func TestSyncNothingMissing(t *testing.T) {
	for _, d := range []string{schema.MySQL, schema.Postgres} {
		tables, c := fullCatalog(t, d)
		if m := missing(tables, c, d == schema.Postgres); len(m) != 0 {
			t.Errorf("%s: %v", d, m)
		}
		if s := syncSteps(tables, c, d == schema.Postgres, true, Migrations[1].Drop, nil); len(s) != 0 {
			t.Errorf("%s: %+v", d, s)
		}
	}
}

// An old database (as the HLstatsX one gameWE started from): the Frags
// table without server_id and positions, with killerId / victimId instead of
// killer_time / victim_time, and no hlstats_gamewe_jobs.
func TestSyncOldDatabase(t *testing.T) {
	for _, d := range []string{schema.MySQL, schema.Postgres} {
		pg := d == schema.Postgres
		tables, c := fullCatalog(t, d)
		delete(c.tables, "hlstats_gamewe_jobs")
		frags := "hlstats_events_frags"
		for _, col := range []string{"server_id", "pos_x", "pos_victim_z"} {
			delete(c.columns[frags], col)
		}
		for _, ix := range []string{"killer_time", "victim_time", "server_id"} {
			delete(c.indexes[frags], strings.ToLower(indexName(pg, frags, ix)))
		}
		c.indexes[frags][strings.ToLower(indexName(pg, frags, "killerId"))] = true
		c.indexes[frags][strings.ToLower(indexName(pg, frags, "victimId"))] = true
		c.rows[frags] = 3_000_000

		m := missing(tables, c, pg)
		if len(m) != 7 || m[0].Table != "hlstats_Events_Frags" {
			t.Errorf("%s missing: %v", d, m)
		}
		steps := syncSteps(tables, c, pg, true, Migrations[1].Drop, nil)
		if len(steps) != 2 {
			t.Fatalf("%s: %d steps: %+v", d, len(steps), steps)
		}
		f, j := steps[0], steps[1]
		if !f.Slow || f.Rows != 3_000_000 || !strings.Contains(f.What, "add columns server_id, pos_x, pos_victim_z") ||
			!strings.Contains(f.What, "drop indexes killerId (replaced by killer_time), victimId (replaced by victim_time)") {
			t.Errorf("%s frags step: %+v", d, f)
		}
		if j.What != "create table hlstats_gamewe_jobs" {
			t.Errorf("%s jobs step: %+v", d, j)
		}
		all := strings.Join(f.stmts, ";\n")
		if pg {
			for _, want := range []string{"ALTER TABLE hlstats_Events_Frags ADD COLUMN IF NOT EXISTS server_id integer NOT NULL DEFAULT 0",
				"CREATE INDEX IF NOT EXISTS hlstats_Events_Frags_killer_time ON hlstats_Events_Frags (killerId, timestamp_id)",
				"DROP INDEX IF EXISTS hlstats_events_frags_killerid"} {
				if !strings.Contains(all, want) {
					t.Errorf("postgres lacks %q in\n%s", want, all)
				}
			}
			if !strings.HasPrefix(j.stmts[0], "CREATE TABLE IF NOT EXISTS hlstats_gamewe_jobs (") || len(j.stmts) != 2 {
				t.Errorf("postgres jobs: %q", j.stmts)
			}
			continue
		}
		if len(f.stmts) != 1 {
			t.Fatalf("mysql: %d statements (one ALTER per table)", len(f.stmts))
		}
		for _, want := range []string{"ALTER TABLE `hlstats_Events_Frags`",
			"ADD COLUMN `server_id` smallint(5) unsigned NOT NULL DEFAULT 0 AFTER `game_id`",
			"ADD COLUMN `pos_x` int(11) DEFAULT NULL AFTER `headshot`",
			"ADD KEY `killer_time` (`killerId`,`timestamp_id`)", "DROP KEY `killerId`", "DROP KEY `victimId`"} {
			if !strings.Contains(all, want) {
				t.Errorf("mysql lacks %q in\n%s", want, all)
			}
		}
		if !strings.HasPrefix(j.stmts[0], "CREATE TABLE IF NOT EXISTS `hlstats_gamewe_jobs` (") {
			t.Errorf("mysql jobs: %q", j.stmts)
		}
	}
}

// A replaced index is only dropped when its replacement is there.
func TestDropNeedsReplacement(t *testing.T) {
	tables, c := fullCatalog(t, schema.MySQL)
	delete(c.indexes["hlstats_events_frags"], "killer_time")
	c.indexes["hlstats_events_frags"]["killerid"] = true
	// No sync in this run: killer_time is not added, killerId stays.
	if s := syncSteps(tables, c, false, false, Migrations[1].Drop, nil); len(s) != 0 {
		t.Errorf("dropped without replacement: %+v", s)
	}
}

func TestMigrationsOrdered(t *testing.T) {
	for i, m := range Migrations {
		if m.Version != i+1 || m.Name == "" || m.Desc == "" {
			t.Errorf("migration %d: %+v", i, m)
		}
		if len(m.Checksum()) != 64 {
			t.Errorf("checksum %q", m.Checksum())
		}
	}
	if Migrations[0].Sync || Migrations[0].Seed {
		t.Error("the baseline only creates new databases")
	}
	if Latest() != Migrations[len(Migrations)-1].Version {
		t.Error("Latest")
	}
}

func TestCreateIfNotExists(t *testing.T) {
	for in, want := range map[string]string{
		"CREATE TABLE `a` (":             "CREATE TABLE IF NOT EXISTS `a` (",
		"CREATE UNLOGGED TABLE a (":      "CREATE UNLOGGED TABLE IF NOT EXISTS a (",
		"CREATE UNIQUE INDEX a ON b (c)": "CREATE UNIQUE INDEX IF NOT EXISTS a ON b (c)",
		"CREATE INDEX a ON b (c)":        "CREATE INDEX IF NOT EXISTS a ON b (c)",
	} {
		if got := createIfNotExists(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestRightsErrorText(t *testing.T) {
	e := &RightsError{User: "'gamewe'@'%'", Reason: "CREATE command denied", Fix: "  GRANT ..."}
	if s := e.Error(); !strings.Contains(s, "'gamewe'@'%' may not change the table structure (CREATE command denied)") ||
		!strings.Contains(s, "GRANT ...") || !strings.Contains(s, "gamewe db migrate again") {
		t.Error(s)
	}
	fix := pgFix(`ga"me`, "stats", "")
	if !strings.Contains(fix, `\c "stats"`) || !strings.Contains(fix, `TO "ga""me";`) || !strings.Contains(fix, `'ga"me'`) {
		t.Error(fix)
	}
}

// An HLstatsX:CE database (eventTime instead of timestamp_id) is not
// upgraded in place.
func TestDetectCE(t *testing.T) {
	_, c := fullCatalog(t, schema.MySQL)
	if isCE(c) {
		t.Error("the gameWE schema is not CE")
	}
	delete(c.columns["hlstats_events_frags"], "timestamp_id")
	c.columns["hlstats_events_frags"]["eventtime"] = true
	if !isCE(c) {
		t.Error("CE not detected")
	}
	if !strings.Contains(ErrHLstatsXCE.Error(), "MIGRATION-HLSTATSX.md") || !strings.Contains(ErrHLstatsXCE.Error(), "gamewe db convert") {
		t.Error(ErrHLstatsXCE)
	}
}

func TestDropSQL(t *testing.T) {
	for _, dialect := range []string{schema.MySQL, schema.Postgres} {
		q, err := DropSQL(dialect)
		if err != nil {
			t.Fatal(err)
		}
		tables, _ := schema.Tables(dialect)
		if !strings.HasPrefix(q, "DROP TABLE IF EXISTS ") || strings.Count(q, ",") != len(tables) || !strings.Contains(q, MigrationsTable) {
			t.Errorf("%s: %s", dialect, q)
		}
		if dialect == schema.MySQL && !strings.Contains(q, "`hlstats_Events_Frags`") {
			t.Errorf("mysql quoting: %s", q)
		}
	}
}

func TestPurgeTables(t *testing.T) {
	for _, dialect := range []string{schema.MySQL, schema.Postgres} {
		tables, err := PurgeTables(dialect)
		if err != nil {
			t.Fatal(err)
		}
		if len(tables) < 15 || !slices.Contains(tables, "hlstats_Events_Frags") || slices.ContainsFunc(tables, func(s string) bool {
			return !strings.HasPrefix(s, "hlstats_Events_")
		}) {
			t.Errorf("%s: %v", dialect, tables)
		}
	}
	if !isOwnTable(schema.MySQL, "hlstats_Players") || !isOwnTable(schema.MySQL, MigrationsTable) || isOwnTable(schema.MySQL, "mysql.user") || isOwnTable(schema.MySQL, "hlstats_Players`; DROP") {
		t.Error("isOwnTable")
	}
}

// A MariaDB database whose tables are still MyISAM (collector 0.x) moves to
// InnoDB with the sync: one ALTER TABLE per table, together with the indexes
// it lacks; hlstats_Livestats (MEMORY in the schema) and tables already on
// InnoDB are left alone. PostgreSQL has no engines.
func TestSyncEngine(t *testing.T) {
	tables, c := fullCatalog(t, schema.MySQL)
	for lt := range c.tables {
		c.engines[lt] = "myisam"
	}
	c.engines["hlstats_livestats"] = "memory"
	c.engines["hlstats_options"] = "innodb"
	frags := "hlstats_events_frags"
	delete(c.indexes[frags], "map_time")
	c.indexes[frags]["map"] = true
	c.rows[frags] = 3_000_000

	m := missing(tables, c, false)
	if len(m) != len(tables)-1 /* Livestats */ -1 /* Options */ +1 /* map_time */ {
		t.Errorf("%d requirements: %v", len(m), m)
	}
	var engines, maps int
	for _, r := range m {
		switch {
		case r.Engine == "InnoDB" && r.Table == "hlstats_Livestats", r.Engine == "InnoDB" && r.Table == "hlstats_Options":
			t.Errorf("not to convert: %v", r)
		case r.Engine == "InnoDB":
			engines++
			if r.String() != "schema outdated: table "+r.Table+" is not InnoDB" {
				t.Error(r.String())
			}
		case r.Index == "map_time":
			maps++
		default:
			t.Errorf("unexpected: %v", r)
		}
	}
	if engines != len(tables)-2 || maps != 1 {
		t.Errorf("%d engines, %d map_time", engines, maps)
	}

	steps := syncSteps(tables, c, false, true, Migrations[18].Drop, Migrations[18].Widen)
	if len(steps) != len(tables)-2 {
		t.Fatalf("%d steps", len(steps))
	}
	for _, s := range steps {
		if s.Table == "hlstats_Livestats" || s.Table == "hlstats_Options" {
			t.Errorf("step on %s: %+v", s.Table, s)
		}
		if len(s.stmts) != 1 || !strings.Contains(s.stmts[0], "ENGINE=InnoDB") || !strings.Contains(s.What, "convert from myisam to InnoDB") {
			t.Errorf("%s: %+v", s.Table, s)
		}
		if s.Table == "hlstats_Events_Frags" {
			if !s.Slow || !strings.Contains(s.stmts[0], "ADD KEY `map_time` (`map`,`timestamp_id`)") || !strings.Contains(s.stmts[0], "DROP KEY `map`") {
				t.Errorf("frags: %+v", s)
			}
		}
	}
	// Without sync (a Drop-only migration) nothing is converted.
	if s := syncSteps(tables, c, false, false, nil, nil); len(s) != 0 {
		t.Errorf("converted without sync: %+v", s)
	}
	// PostgreSQL: engines do not exist.
	pgTables, pc := fullCatalog(t, schema.Postgres)
	for lt := range pc.tables {
		pc.engines[lt] = "myisam"
	}
	if m := missing(pgTables, pc, true); len(m) != 0 {
		t.Errorf("postgres: %v", m)
	}
}

// An engine the database did not tell (an older information_schema) is
// not converted.
func TestSyncEngineUnknown(t *testing.T) {
	tables, c := fullCatalog(t, schema.MySQL)
	if m := missing(tables, c, false); len(m) != 0 {
		t.Errorf("%v", m)
	}
}

func TestColumnType(t *testing.T) {
	for in, want := range map[string]string{
		"smallint(5) unsigned NOT NULL AUTO_INCREMENT":      "smallint unsigned",
		"tinyint(3) unsigned":                               "tinyint unsigned",
		"tinyint unsigned":                                  "tinyint unsigned",
		"int(11) NOT NULL DEFAULT 0":                        "int",
		"bigint(20) unsigned NOT NULL DEFAULT 0":            "bigint unsigned",
		"varchar(10) NOT NULL DEFAULT 'valve'":              "varchar(10)",
		"enum('0','1') NOT NULL DEFAULT '0'":                "enum('0','1')",
		"integer GENERATED BY DEFAULT AS IDENTITY NOT NULL": "integer",
		"smallint":          "smallint",
		"character varying": "character varying",
	} {
		if got := columnType(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A database of 0.x: the ids of teams and roles are tinyint. Migration 19
// widens them (MySQL: MODIFY COLUMN in the table's ALTER; PostgreSQL: ALTER
// COLUMN TYPE); a column of the right type, or one the database did not
// tell, is left alone.
func TestSyncWiden(t *testing.T) {
	for _, d := range []string{schema.MySQL, schema.Postgres} {
		pg := d == schema.Postgres
		tables, c := fullCatalog(t, d)
		oldType, want, stmt := "tinyint unsigned", "smallint unsigned", "MODIFY COLUMN `teamId` smallint(5) unsigned NOT NULL AUTO_INCREMENT"
		if pg {
			oldType, want, stmt = "smallint", "integer", "ALTER TABLE hlstats_Teams ALTER COLUMN teamId TYPE integer"
		}
		c.types["hlstats_teams"]["teamid"] = oldType
		c.types["hlstats_roles"]["roleid"] = ""  // unknown: left alone
		c.types["hlstats_games"]["id"] = oldType // not in any Widen: left alone
		m := missing(tables, c, pg)
		if len(m) != 1 || m[0].Table != "hlstats_Teams" || m[0].Column != "teamId" || m[0].Type != want {
			t.Fatalf("%s: missing %v", d, m)
		}
		if got := m[0].String(); got != "schema outdated: column hlstats_Teams.teamId is not "+want {
			t.Error(got)
		}
		steps := syncSteps(tables, c, pg, true, nil, allWiden())
		if len(steps) != 1 || steps[0].Table != "hlstats_Teams" || !strings.Contains(steps[0].What, "widen columns teamId (from "+oldType+" to "+want+")") {
			t.Fatalf("%s: steps %+v", d, steps)
		}
		if len(steps[0].stmts) != 1 || !strings.Contains(steps[0].stmts[0], stmt) {
			t.Errorf("%s: %q", d, steps[0].stmts)
		}
		// Without Widen (an older migration's sync) the type stays.
		if steps := syncSteps(tables, c, pg, true, nil, nil); len(steps) != 0 {
			t.Errorf("%s: sync without widen: %+v", d, steps)
		}
	}
}
