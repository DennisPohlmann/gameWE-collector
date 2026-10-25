// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

// A database of version 7 (no czero rows) gets the czero game with the
// cstrike definitions from version 8; rows of other games stay.
func TestMigrateCZero(t *testing.T) {
	migrateTest(t)
	name := "mig_czero"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	admin := adminIn(t, name)
	var before int
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Weapons WHERE game <> 'czero'`).Scan(&before)
	for _, q := range []string{
		`DELETE FROM hlstats_Weapons WHERE game = 'czero'`,
		`DELETE FROM hlstats_Actions WHERE game = 'czero'`,
		`DELETE FROM hlstats_Awards WHERE game = 'czero'`,
		`DELETE FROM hlstats_Teams WHERE game = 'czero'`,
		`DELETE FROM hlstats_Definition_Ranks_Ribbons WHERE game_code = 'czero'`,
		`DELETE FROM hlstats_Definition_Ribbons_Classes WHERE game_code = 'czero'`,
		`DELETE FROM hlstats_Games WHERE code = 'czero'`,
		`DELETE FROM ` + MigrationsTable + ` WHERE version >= 8`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p, err := PlanMigrations(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Current != 7 || len(p.Steps) == 0 {
		t.Fatalf("plan after taking version 8 back: %+v", p)
	}
	mustMigrate(t, db)
	upToDate(t, db)
	var games, after int
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Games WHERE code = 'czero' AND realgame = 'czero'`).Scan(&games)
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Weapons WHERE game <> 'czero'`).Scan(&after)
	if games != 1 || after != before {
		t.Errorf("czero game %d; other weapons %d -> %d", games, before, after)
	}
	// czero has exactly the definitions of cstrike.
	for _, c := range []struct{ table, col string }{
		{"hlstats_Teams", "game"}, {"hlstats_Actions", "game"}, {"hlstats_Weapons", "game"}, {"hlstats_Awards", "game"},
		{"hlstats_Definition_Ranks_Ribbons", "game_code"}, {"hlstats_Definition_Ribbons_Classes", "game_code"},
	} {
		var cz, cs int
		db.QueryRow(`SELECT COUNT(*) FROM ` + c.table + ` WHERE ` + c.col + ` = 'czero'`).Scan(&cz)
		db.QueryRow(`SELECT COUNT(*) FROM ` + c.table + ` WHERE ` + c.col + ` = 'cstrike'`).Scan(&cs)
		if cz == 0 || cz != cs {
			t.Errorf("%s: czero %d rows, cstrike %d", c.table, cz, cs)
		}
	}
	webRights(t, name)
}
