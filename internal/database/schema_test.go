// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"os"
	"strings"
	"testing"
)

// schema/hlstatsx.sql must contain an exact copy of schema/init/02_seed.sql,
// so that the schema file alone creates a working database (`make schema`).
func TestSchemaContainsSeed(t *testing.T) {
	schema, err := os.ReadFile("../../schema/hlstatsx.sql")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../schema/init/02_seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(schema)
	begin := strings.Index(s, "-- BEGIN REFERENCE DATA")
	end := strings.Index(s, "-- END REFERENCE DATA")
	if begin < 0 || end < begin {
		t.Fatal("schema/hlstatsx.sql has no REFERENCE DATA section")
	}
	got := s[strings.Index(s[begin:], "\n")+begin+1 : end]
	if got != string(seed) {
		t.Error("the REFERENCE DATA section of schema/hlstatsx.sql differs from schema/init/02_seed.sql: run `make schema`")
	}
	if strings.Contains(string(seed), "INSERT INTO") {
		t.Error("02_seed.sql must only use INSERT IGNORE, so it can run on any database")
	}
}
