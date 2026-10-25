// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

// A database of version 6 (no neck columns, no cs2 rows) gets both from
// version 7; rows of other games and an existing neck-less weapon row stay.
func TestMigrateCS2(t *testing.T) {
	migrateTest(t)
	name := "mig_cs2"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	admin := adminIn(t, name)
	var before int
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Weapons WHERE game <> 'cs2'`).Scan(&before)
	for _, q := range []string{
		`ALTER TABLE hlstats_Players_Weapons DROP COLUMN neck`,
		`ALTER TABLE hlstats_Players_Weapons_History_Months DROP COLUMN neck`,
		`DELETE FROM hlstats_Weapons WHERE game = 'cs2'`,
		`DELETE FROM hlstats_Actions WHERE game = 'cs2'`,
		`DELETE FROM hlstats_Awards WHERE game = 'cs2'`,
		`DELETE FROM hlstats_Teams WHERE game = 'cs2'`,
		`DELETE FROM hlstats_Definition_Ranks_Ribbons WHERE game_code = 'cs2'`,
		`DELETE FROM hlstats_Definition_Ribbons_Classes WHERE game_code = 'cs2'`,
		`DELETE FROM hlstats_Games WHERE code = 'cs2'`,
		`DELETE FROM ` + MigrationsTable + ` WHERE version >= 7`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p, err := PlanMigrations(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Current != 6 || len(p.Steps) == 0 {
		t.Fatalf("plan after taking version 7 back: %+v", p)
	}
	mustMigrate(t, db)
	upToDate(t, db)
	var games, weapons, actions, after int
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Games WHERE code = 'cs2' AND realgame = 'cs2'`).Scan(&games)
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Weapons WHERE game = 'cs2'`).Scan(&weapons)
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Actions WHERE game = 'cs2'`).Scan(&actions)
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Weapons WHERE game <> 'cs2'`).Scan(&after)
	if games != 1 || weapons < 30 || actions < 10 || after != before {
		t.Errorf("cs2 game %d, weapons %d, actions %d; other weapons %d -> %d", games, weapons, actions, before, after)
	}
	if _, err := db.Exec(`INSERT INTO hlstats_Players_Weapons (player_id, yearweek_id, game_id, weapon_id, neck) VALUES (1, 1, 1, 1, 2)`); err != nil {
		t.Errorf("neck column: %v", err)
	}
	webRights(t, name)
}
