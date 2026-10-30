// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"strings"
	"testing"

	"gamewe-collector/internal/schema"
)

// No old table is part of the schema, the version table is never one, and
// the old name of the version table belongs only to the default prefix.
func TestOldTableList(t *testing.T) {
	for _, dialect := range []string{schema.MySQL, schema.Postgres} {
		tables, err := schema.Tables(dialect)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range OldTableList("") {
			if o.Why == "" {
				t.Errorf("%s: no reason", o.Table)
			}
			if isOwnTable(dialect, o.Table) || strings.EqualFold(o.Table, MigrationsTable) {
				t.Errorf("%s: %s is part of the schema", dialect, o.Table)
			}
			for _, tb := range tables {
				if strings.EqualFold(tb.Name, o.Table) {
					t.Errorf("%s: %s is in the schema", dialect, o.Table)
				}
			}
		}
	}
	has := func(p Prefix) bool {
		for _, o := range OldTableList(p) {
			if o.Table == LegacyMigrationsTable {
				return true
			}
		}
		return false
	}
	if !has("") || !has("hlstats") || has("gw") {
		t.Error("the old version table must be listed for the default prefix only")
	}
}

func TestCleanupSQL(t *testing.T) {
	q, err := CleanupSQL(schema.MySQL, "", nil)
	if err != nil || !strings.HasPrefix(q, "DROP TABLE IF EXISTS `hlstats_Events_Admin`, ") || !strings.Contains(q, "`gamewe_schema_migrations`") {
		t.Errorf("all: %q %v", q, err)
	}
	if q, err := CleanupSQL(schema.Postgres, "gw", []string{"hlstats_Trend"}); err != nil || q != "DROP TABLE IF EXISTS hlstats_Trend;\n" {
		t.Errorf("one: %q %v", q, err) // the connection or Script puts in the prefix
	}
	for _, bad := range []string{"hlstats_Players", MigrationsTable, "hlstats_Trend`; DROP DATABASE x; --", "mysql.user"} {
		if _, err := CleanupSQL(schema.MySQL, "", []string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := CleanupSQL(schema.MySQL, "gw", []string{LegacyMigrationsTable}); err == nil {
		t.Error("another prefix may not drop the old version table of the default one")
	}
	if q, err := CleanupSQL(schema.MySQL, "", []string{}); q != "" || err != nil {
		t.Errorf("none: %q %v", q, err)
	}
}
