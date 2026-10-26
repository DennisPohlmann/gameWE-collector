// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

// Version 15 moves the global bot setting to every ranking with the effect it
// had: gamewe_rank_bots = 0 becomes "not ranked, no points" in each ranking,
// except the points of Left 4 Dead (a player's kill of the AI always gave
// points); a stored gamewe_ignore_bots = 1 meant not ranked. The global rows
// are gone afterwards, values already set per ranking stay, a second run
// changes nothing, and a new database has no bot rows (default: ranked,
// with points).
func TestMigrateBotsPerRanking(t *testing.T) {
	migrateTest(t)
	name := "mig_bots_ranking"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	opts := func() map[string]string {
		t.Helper()
		rows, err := db.Query(`SELECT keyname, value FROM hlstats_Options WHERE keyname LIKE 'gamewe_%bot%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			m[k] = v
		}
		return m
	}
	if o := opts(); len(o) != 0 {
		t.Errorf("new database has bot settings: %v", o)
	}

	admin := adminIn(t, name)
	redo := func(stmts ...string) {
		t.Helper()
		for _, q := range append([]string{`DELETE FROM ` + MigrationsTable + ` WHERE version >= 15`}, stmts...) {
			if _, err := admin.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		p, err := PlanMigrations(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if p.Current != 14 {
			t.Fatalf("plan after taking version 15 back: %+v", p)
		}
		mustMigrate(t, db)
		upToDate(t, db)
	}

	// Collector 0.19 with bots not ranked; css set by hand already.
	redo(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_rank_bots', '0'), ('gamewe_rank_bots_css', '1')`)
	o := opts()
	for k, want := range map[string]string{
		"gamewe_rank_bots_css": "1", "gamewe_bot_points_css": "0",
		"gamewe_rank_bots_tf": "0", "gamewe_bot_points_tf": "0",
		"gamewe_rank_bots_l4d2": "0", "gamewe_bot_points_l4d2": "1",
	} {
		if o[k] != want {
			t.Errorf("%s = %q, want %q", k, o[k], want)
		}
	}
	if _, ok := o["gamewe_rank_bots"]; ok {
		t.Error("gamewe_rank_bots not deleted")
	}

	// Bots ranked: everything on; the rows written above stay.
	if _, err := admin.Exec(`DELETE FROM hlstats_Options WHERE keyname LIKE 'gamewe_%bot%' AND keyname <> 'gamewe_rank_bots_css'`); err != nil {
		t.Fatal(err)
	}
	redo(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_rank_bots', '1')`)
	o = opts()
	if o["gamewe_rank_bots_tf"] != "1" || o["gamewe_bot_points_tf"] != "1" || o["gamewe_bot_points_css"] != "1" {
		t.Errorf("rank_bots 1: %v", o)
	}

	// The old gamewe_ignore_bots = 1 wins over gamewe_rank_bots = 1.
	if _, err := admin.Exec(`DELETE FROM hlstats_Options WHERE keyname LIKE 'gamewe_%bot%'`); err != nil {
		t.Fatal(err)
	}
	redo(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_rank_bots', '1'), ('gamewe_ignore_bots', '1')`)
	o = opts()
	if o["gamewe_rank_bots_css"] != "0" || o["gamewe_bot_points_css"] != "0" || o["gamewe_ignore_bots"] != "" {
		t.Errorf("ignore_bots 1: %v", o)
	}

	// A second run (version 15 recorded) is a no-op; a repeated version 15
	// without global rows writes nothing.
	n := len(o)
	redo()
	if len(opts()) != n {
		t.Errorf("repeated version 15 changed the rows: %v -> %v", o, opts())
	}
}
