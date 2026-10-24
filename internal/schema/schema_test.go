// SPDX-License-Identifier: GPL-3.0-or-later

package schema

import (
	"slices"
	"strings"
	"testing"
)

// Both dialects have the same tables with the same columns and indexes, in
// the same order.
func TestTablesBothDialects(t *testing.T) {
	my, err := Tables(MySQL)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := Tables(Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if len(my) < 50 || len(my) != len(pg) {
		t.Fatalf("%d MariaDB tables, %d PostgreSQL tables", len(my), len(pg))
	}
	for i := range my {
		a, b := my[i], pg[i]
		if a.Name != b.Name {
			t.Errorf("table %d: %s vs %s", i, a.Name, b.Name)
			continue
		}
		if !strings.HasPrefix(a.Create, "CREATE TABLE `"+a.Name+"` (") || !strings.HasSuffix(b.Create, ")") {
			t.Errorf("%s: CREATE statements %q / %q", a.Name, a.Create[:30], b.Create[len(b.Create)-5:])
		}
		var ca, cb []string
		for _, c := range a.Columns {
			ca = append(ca, c.Name)
		}
		for _, c := range b.Columns {
			cb = append(cb, c.Name)
		}
		if !slices.Equal(ca, cb) {
			t.Errorf("%s: columns %v vs %v", a.Name, ca, cb)
		}
		if len(a.Indexes) != len(b.Indexes) {
			t.Errorf("%s: %d vs %d indexes", a.Name, len(a.Indexes), len(b.Indexes))
			continue
		}
		for j, ix := range a.Indexes {
			want := strings.ToLower(a.Name + "_" + ix.Name)
			if ix.Primary {
				want = strings.ToLower(a.Name) + "_pkey"
			}
			if b.Indexes[j].Name != want && !(ix.Primary && b.Indexes[j].Primary) {
				// PostgreSQL lists the primary key with the columns, before
				// the indexes; compare by name instead.
				found := false
				for _, o := range b.Indexes {
					found = found || o.Name == want
				}
				if !found {
					t.Errorf("%s: no PostgreSQL index %s", a.Name, want)
				}
			}
		}
	}
	frags := my[slices.IndexFunc(my, func(t Table) bool { return t.Name == "hlstats_Events_Frags" })]
	i := slices.IndexFunc(frags.Indexes, func(ix Index) bool { return ix.Name == "killer_time" })
	if i < 0 || !slices.Equal(frags.Indexes[i].Columns, []string{"killerId", "timestamp_id"}) || frags.Indexes[i].Def != "KEY `killer_time` (`killerId`,`timestamp_id`)" {
		t.Errorf("killer_time: %+v", frags.Indexes)
	}
	j := slices.IndexFunc(frags.Columns, func(c Column) bool { return c.Name == "server_id" })
	if j < 0 || frags.Columns[j].Def != "smallint(5) unsigned NOT NULL DEFAULT 0" {
		t.Errorf("server_id: %+v", frags.Columns)
	}
}

// The schema only creates; the seed only adds missing rows, so both can run
// on any database.
func TestSeedOnlyAdds(t *testing.T) {
	for _, d := range []string{MySQL, Postgres} {
		for _, st := range Split(d, Schema(d)) {
			if u := strings.ToUpper(st.SQL); !strings.HasPrefix(u, "CREATE ") && !strings.HasPrefix(u, "SET ") {
				t.Errorf("%s schema: %.60s", d, st.SQL)
			}
		}
		n := 0
		for _, st := range Split(d, Seed(d)) {
			u := strings.ToUpper(st.SQL)
			switch {
			case strings.HasPrefix(u, "SET "):
			case d == MySQL && strings.HasPrefix(u, "INSERT IGNORE INTO "):
				n++
			case d == Postgres && strings.HasPrefix(u, "INSERT INTO ") && strings.HasSuffix(u, " ON CONFLICT DO NOTHING"):
				n++
			default:
				t.Errorf("%s seed: %.60s", d, st.SQL)
			}
		}
		if n < 10 {
			t.Errorf("%s seed: only %d inserts", d, n)
		}
	}
}

func TestSplit(t *testing.T) {
	my := "SET a = 1; -- comment; not a statement\n" +
		"/*M!999999\\- enable the sandbox mode */\n" +
		"INSERT IGNORE INTO t VALUES ('a;b', 'it\\'s', 'x''y', \"q;\"), (`c;`);\n" +
		"DELIMITER //\nBEGIN NOT ATOMIC\n  REVOKE x; -- inner\n  REVOKE y;\nEND //\nDELIMITER ;\n" +
		"GRANT z;\n"
	got := Split(MySQL, my)
	want := []string{
		"SET a = 1",
		"INSERT IGNORE INTO t VALUES ('a;b', 'it\\'s', 'x''y', \"q;\"), (`c;`)",
		"BEGIN NOT ATOMIC\n  REVOKE x; \n  REVOKE y;\nEND",
		"GRANT z",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d statements: %+v", len(got), got)
	}
	for i := range want {
		if got[i].SQL != want[i] || got[i].GExec {
			t.Errorf("statement %d: %q, want %q", i, got[i].SQL, want[i])
		}
	}
	pg := "SELECT format('GRANT %I;', current_database()) \\gexec\n" +
		"DO $$\nBEGIN\n  GRANT a; -- x\nEND\n$$;\n" +
		"INSERT INTO t VALUES ('a\\', 'b''c;') ON CONFLICT DO NOTHING;"
	gp := Split(Postgres, pg)
	if len(gp) != 3 || !gp[0].GExec || gp[0].SQL != "SELECT format('GRANT %I;', current_database())" ||
		gp[1].SQL != "DO $$\nBEGIN\n  GRANT a; -- x\nEND\n$$" ||
		gp[2].SQL != "INSERT INTO t VALUES ('a\\', 'b''c;') ON CONFLICT DO NOTHING" {
		t.Errorf("postgres: %+v", gp)
	}
}
