// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"strings"
	"testing"

	"gamewe-collector/internal/schema"
)

func TestPrefixSQL(t *testing.T) {
	p := Prefix("gw")
	for in, want := range map[string]string{
		"SELECT * FROM hlstats_Players WHERE playerId = ?":                          "SELECT * FROM gw_Players WHERE playerId = ?",
		"SELECT p.x FROM hlstats_Players p JOIN hlstats_Games g ON g.code = p.game": "SELECT p.x FROM gw_Players p JOIN gw_Games g ON g.code = p.game",
		"UPDATE `hlstats_Servers` SET x = 1":                                        "UPDATE `gw_Servers` SET x = 1",
		`SELECT 1 FROM "hlstats_players"`:                                           `SELECT 1 FROM "gw_players"`,
		"SELECT 1 FROM stats.hlstats_Players":                                       "SELECT 1 FROM stats.gw_Players",
		"INSERT INTO hlstats_gamewe_schema_migrations (version) VALUES (?)":         "INSERT INTO gw_gamewe_schema_migrations (version) VALUES (?)",
		"SELECT 1 FROM gamewe_schema_migrations":                                    "SELECT 1 FROM gamewe_schema_migrations", // the old name: only the default prefix has it
		"CREATE INDEX hlstats_actions_code ON hlstats_Actions (code)":               "CREATE INDEX gw_actions_code ON gw_Actions (code)",
		"SELECT keyname FROM HLSTATS_Options":                                       "SELECT keyname FROM gw_Options",
		// literals, comments, longer identifiers and column names stay
		"SELECT 'hlstats_Players' FROM hlstats_Options":            "SELECT 'hlstats_Players' FROM gw_Options",
		"SELECT 'it''s hlstats_x' FROM hlstats_Options":            "SELECT 'it''s hlstats_x' FROM gw_Options",
		"SELECT 1 -- hlstats_Players\nFROM hlstats_Options":        "SELECT 1 -- hlstats_Players\nFROM gw_Options",
		"SELECT 1 /* hlstats_Players */ FROM hlstats_Options":      "SELECT 1 /* hlstats_Players */ FROM gw_Options",
		"SELECT my_hlstats_Players, xhlstats_y FROM hlstats_Teams": "SELECT my_hlstats_Players, xhlstats_y FROM gw_Teams",
		"SELECT gamewe_schema_migrations_x FROM t":                 "SELECT gamewe_schema_migrations_x FROM t",
		"SELECT GET_LOCK(?, 0)":                                    "SELECT GET_LOCK(?, 0)",
	} {
		if got := p.SQL(in); got != want {
			t.Errorf("SQL(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	for _, d := range []Prefix{"", "hlstats"} {
		q := "SELECT * FROM hlstats_Players JOIN hlstats_gamewe_schema_migrations"
		if got := d.SQL(q); got != q {
			t.Errorf("default prefix %q changed %q to %q", d, q, got)
		}
	}
}

func TestPrefixScript(t *testing.T) {
	p := Prefix("gw")
	in := "EXECUTE 'GRANT SELECT ON `hlstats_gamewe_jobs` TO ' || x; -- hlstats_x\nSELECT pg_get_serial_sequence('hlstats_gamewe_jobs', 'id')"
	want := "EXECUTE 'GRANT SELECT ON `gw_gamewe_jobs` TO ' || x; -- hlstats_x\nSELECT pg_get_serial_sequence('gw_gamewe_jobs', 'id')"
	if got := p.Script(in); got != want {
		t.Errorf("Script\n got %q\nwant %q", got, want)
	}
	// The connection rewrites a script again: that changes nothing.
	if got := p.SQL(p.Script(in)); got != want {
		t.Errorf("SQL(Script) changed the script: %q", got)
	}
}

// Every table and index name of the compiled schemas is rewritten, and no
// other identifier: the rewritten schema has the same statements with
// gw_ names.
func TestPrefixSchema(t *testing.T) {
	p := Prefix("gw")
	for _, d := range []string{schema.MySQL, schema.Postgres} {
		for _, file := range []string{schema.Schema(d), schema.Seed(d), schema.Grants(d)} {
			got := p.Script(file)
			if p.Script(got) != got || got == file {
				t.Errorf("%s: names left to rewrite, or nothing rewritten", d)
			}
			if back := strings.NewReplacer("gw_", "hlstats_").Replace(got); back != strings.NewReplacer("gw_", "hlstats_").Replace(file) {
				t.Errorf("%s: the rewrite changed more than the prefix", d)
			}
		}
		tables, err := schema.Tables(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range tables {
			if got := p.Table(tb.Name); !strings.HasPrefix(got, "gw_") {
				t.Errorf("Table(%s) = %s", tb.Name, got)
			}
			for _, ix := range tb.Indexes {
				// PostgreSQL's index names (<table>_<key>) get the prefix and
				// must stay within its 63 characters with the longest prefix.
				n := p.Table(ix.Name)
				if d == schema.Postgres && len(n)-len("gw")+16 > 63 {
					t.Errorf("index %s is too long with a prefix of 16 characters", ix.Name)
				}
			}
		}
	}
}

func TestPrefixLogical(t *testing.T) {
	p := Prefix("GW")
	for in, want := range map[string]struct {
		name string
		ok   bool
	}{
		"gw_players":                  {"hlstats_players", true},
		"GW_Players":                  {"hlstats_Players", true},
		"gw_gamewe_schema_migrations": {MigrationsTable, true},
		"hlstats_players":             {"hlstats_players", false},     // another installation
		"gamewe_schema_migrations":    {LegacyMigrationsTable, false}, // the old one of the default prefix
		"other_table":                 {"other_table", true},
		"gw":                          {"gw", true},
	} {
		name, ok := p.Logical(in)
		if name != want.name || ok != want.ok {
			t.Errorf("Logical(%q) = %q, %v; want %q, %v", in, name, ok, want.name, want.ok)
		}
	}
	if name, ok := Prefix("hlstats").Logical("hlstats_players"); name != "hlstats_players" || !ok {
		t.Errorf("default Logical = %q, %v", name, ok)
	}
	if got := p.Table("hlstats_Players"); got != "GW_Players" {
		t.Errorf("Table = %q", got)
	}
	if got := p.Table(MigrationsTable); got != "GW_gamewe_schema_migrations" {
		t.Errorf("Table(MigrationsTable) = %q", got)
	}
}

func TestPrefixLocksAndLike(t *testing.T) {
	d := Prefix("hlstats")
	if d.lockName(runLockPrefix) != runLockPrefix || d.lockKey(runLockKey) != runLockKey {
		t.Error("the default prefix changed the lock")
	}
	a, b := Prefix("gw"), Prefix("stats2")
	if a.lockName(runLockPrefix) == runLockPrefix || a.lockName(runLockPrefix) == b.lockName(runLockPrefix) {
		t.Error("lock names not per prefix")
	}
	if a.lockKey(runLockKey) == runLockKey || a.lockKey(runLockKey) == b.lockKey(runLockKey) || a.lockKey(runLockKey) != Prefix("GW").lockKey(runLockKey) {
		t.Error("lock keys not per prefix")
	}
	// PostgreSQL has one key space: a running collector holds the collector
	// lock, its jobs take the job lock, gamewe db migrate the migration lock.
	for _, p := range []Prefix{d, a, b} {
		keys := map[int64]bool{p.lockKey(runLockKey): true, p.lockKey(jobLockKey): true, p.lockKey(migrateLockKey): true}
		if len(keys) != 3 {
			t.Errorf("prefix %q: collector, job and migration lock share a key", p)
		}
	}
	if got := Prefix("my_stats").like(); got != `my\_stats\_%` {
		t.Errorf("like = %q", got)
	}
	if got := d.like(); got != `hlstats\_%` {
		t.Errorf("default like = %q", got)
	}
}
