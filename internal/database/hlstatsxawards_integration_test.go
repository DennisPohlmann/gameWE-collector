// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

// A database of version 10 with a ranking (css1 of css) gets the HLstatsX:CE
// awards mvp and teamkills and their ribbons from version 11, the templates
// from the seed and the ranking from its template; CE's mostkills, suicide
// and headshot are not added (most_kills, most_suicides, most_headshots cover
// them); a renamed award of the ranking stays, and a second run changes
// nothing.
func TestMigrateHLstatsXAwards(t *testing.T) {
	migrateTest(t)
	name := "mig_hlxawards"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	admin := adminIn(t, name)
	codes := `('mvp', 'teamkills')`
	for _, q := range []string{
		`DELETE FROM hlstats_Awards WHERE code IN ` + codes + ` AND game IN ('css', 'tfc', 'valve', 'tf')`,
		`DELETE FROM hlstats_Definition_Ranks_Ribbons WHERE code IN ` + codes + ` AND game_code IN ('css', 'tfc', 'valve', 'tf')`,
		`DELETE FROM ` + MigrationsTable + ` WHERE version >= 11`,
		// css1: a ranking of css with the definitions of css up to version 10,
		// and its own name for an award it already has.
		`INSERT INTO hlstats_Games (code, name, realgame, hidden) VALUES ('css1', 'Ranking', 'css', '0')`,
		`INSERT INTO hlstats_Awards (awardType, game, code, name, verb) SELECT awardType, 'css1', code, name, verb FROM hlstats_Awards WHERE game = 'css'`,
		`INSERT INTO hlstats_Definition_Ranks_Ribbons (definition_type, game_code, code, name, img_file, thumb_file, minimum, restriction_type)
			SELECT definition_type, 'css1', code, name, img_file, thumb_file, minimum, restriction_type FROM hlstats_Definition_Ranks_Ribbons WHERE game_code = 'css'`,
		`INSERT INTO hlstats_Awards (awardType, game, code, name, verb) VALUES ('O', 'css1', 'mvp', 'Our MVP', 'rounds')`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p, err := PlanMigrations(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Current != 10 || len(p.Steps) == 0 {
		t.Fatalf("plan after taking version 11 back: %+v", p)
	}
	mustMigrate(t, db)
	upToDate(t, db)
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, c := range []struct {
		game, codes string
		want        int
	}{
		{"css", codes, 2},
		{"css1", codes, 2},
		{"tfc", codes, 1},
		{"valve", codes, 0}, // valve is deathmatch: no teamkills
		{"tf", `('teamkills')`, 1},
		{"css", `('headshot', 'mostkills', 'suicide')`, 0},
		{"css1", `('headshot', 'mostkills', 'suicide')`, 0},
		{"tfc", `('headshot', 'mostkills', 'suicide')`, 0},
	} {
		if n := count(`SELECT COUNT(*) FROM hlstats_Awards WHERE game = '` + c.game + `' AND code IN ` + c.codes); n != c.want {
			t.Errorf("%s: %d awards, want %d", c.game, n, c.want)
		}
		if n := count(`SELECT COUNT(*) FROM hlstats_Definition_Ranks_Ribbons WHERE definition_type = 'ribbons2' AND game_code = '` +
			c.game + `' AND code IN ` + c.codes); n != c.want {
			t.Errorf("%s: %d ribbons, want %d", c.game, n, c.want)
		}
	}
	if n := count(`SELECT COUNT(*) FROM hlstats_Awards WHERE game = 'css1' AND code = 'mvp' AND name = 'Our MVP'`); n != 1 {
		t.Errorf("the ranking's own mvp award was changed")
	}
	if n := count(`SELECT COUNT(*) FROM hlstats_Awards WHERE game = 'css1'`); n != count(`SELECT COUNT(*) FROM hlstats_Awards WHERE game = 'css'`) {
		t.Errorf("css1 has %d awards, css %d", n, count(`SELECT COUNT(*) FROM hlstats_Awards WHERE game = 'css'`))
	}
	before := counts(t, db)
	mustMigrate(t, db)
	if after := counts(t, db); len(after) != len(before) {
		t.Errorf("second run: %v -> %v", before, after)
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Errorf("second run: %s %d -> %d", k, v, after[k])
			}
		}
	}
}
