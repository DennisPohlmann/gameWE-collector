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
	c := catalog{tables: map[string]bool{}, columns: map[string]map[string]bool{}, indexes: map[string]map[string]bool{}, rows: map[string]int64{}}
	for _, tb := range tables {
		lt := strings.ToLower(tb.Name)
		c.tables[lt] = true
		for _, col := range tb.Columns {
			set(c.columns, lt)[strings.ToLower(col.Name)] = true
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
		if s := syncSteps(tables, c, d == schema.Postgres, true, Migrations[1].Drop); len(s) != 0 {
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
		steps := syncSteps(tables, c, pg, true, Migrations[1].Drop)
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
	if s := syncSteps(tables, c, false, false, Migrations[1].Drop); len(s) != 0 {
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
		"CREATE TABLE `a` (":            "CREATE TABLE IF NOT EXISTS `a` (",
		"CREATE UNLOGGED TABLE a (":     "CREATE UNLOGGED TABLE IF NOT EXISTS a (",
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
	fix := pgFix(`ga"me`, "stats")
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
