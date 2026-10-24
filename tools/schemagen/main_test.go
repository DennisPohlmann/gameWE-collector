// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The PostgreSQL files in the repository must be what the generator makes
// from the MariaDB files (run `make schema` after changing those).
func TestPostgresFilesUpToDate(t *testing.T) {
	schema, _ := os.ReadFile("../../internal/schema/mysql/schema.sql")
	seed, _ := os.ReadFile("../../internal/schema/mysql/seed.sql")
	wantSchema, wantSeed, err := Generate(string(schema), string(seed))
	if err != nil {
		t.Fatal(err)
	}
	grants, _ := os.ReadFile("../../internal/schema/mysql/grants.sql")
	wantGrants, err := ConvertGrants(string(grants))
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"../../internal/schema/postgres/schema.sql":       wantSchema,
		"../../internal/schema/postgres/seed.sql":   wantSeed,
		"../../internal/schema/postgres/grants.sql": wantGrants,
	} {
		got, err := os.ReadFile(file)
		if err != nil || string(got) != want {
			t.Errorf("%s is out of date: run `make schema` (%v)", file, err)
		}
	}
}

// Requirements of the web frontend: unquoted identifiers, no CREATE TYPE,
// the same tables, and both seeds with the same number of rows per table.
func TestPostgresSchemaRules(t *testing.T) {
	pg, err := os.ReadFile("../../internal/schema/postgres/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(pg)
	if strings.Contains(s, `"`) || strings.Contains(s, "`") {
		t.Error("the PostgreSQL schema must not quote identifiers")
	}
	if strings.Contains(strings.ToUpper(s), "CREATE TYPE") {
		t.Error("the PostgreSQL schema must not create types (ENUMs are varchar + CHECK)")
	}
	my, _ := os.ReadFile("../../internal/schema/mysql/schema.sql")
	myTables := regexp.MustCompile("CREATE TABLE `([^`]+)`").FindAllStringSubmatch(string(my), -1)
	pgTables := regexp.MustCompile(`CREATE (?:UNLOGGED )?TABLE (\w+)`).FindAllStringSubmatch(s, -1)
	if len(myTables) != len(pgTables) {
		t.Fatalf("%d MariaDB tables, %d PostgreSQL tables", len(myTables), len(pgTables))
	}
	for i := range myTables {
		if myTables[i][1] != pgTables[i][1] {
			t.Errorf("table %d: %s vs %s", i, myTables[i][1], pgTables[i][1])
		}
	}
	rows := func(seed string) map[string]int {
		out := map[string]int{}
		table := ""
		for _, l := range strings.Split(seed, "\n") {
			if m := regexp.MustCompile(`^INSERT (?:IGNORE )?INTO (\w+)`).FindStringSubmatch(l); m != nil {
				table = m[1]
			} else if strings.HasPrefix(l, "  (") && table != "" {
				out[table] += strings.Count(l, "), (") + 1
			}
		}
		return out
	}
	mySeed, _ := os.ReadFile("../../internal/schema/mysql/seed.sql")
	pgSeed, _ := os.ReadFile("../../internal/schema/postgres/seed.sql")
	a, b := rows(string(mySeed)), rows(string(pgSeed))
	if len(a) < 10 || len(a) != len(b) {
		t.Fatalf("seed tables: %v vs %v", a, b)
	}
	for k, v := range a {
		if b[k] != v {
			t.Errorf("%s: %d MariaDB rows, %d PostgreSQL rows", k, v, b[k])
		}
	}
}

func TestPgLiterals(t *testing.T) {
	cases := map[string]string{
		`  ('a', 'it''s'),`:         `  ('a', 'it''s'),`,
		`  ('/AXXXXXX\\', 'x'),`:    `  ('/AXXXXXX\', 'x'),`,
		`  ('it\'s'), -- comment`:   `  ('it''s'),`,
		`  ('--not a comment', 1),`: `  ('--not a comment', 1),`,
	}
	for in, want := range cases {
		if got, err := pgLiterals(in); err != nil || got != want {
			t.Errorf("pgLiterals(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
