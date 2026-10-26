// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

// A database of version 12 with the option rows of older seeds loses those
// nobody reads in version 13: gamewe_default_game and gamewe_daily_awards
// always, contact, siteurl and gamewe_geoip_db only while they are empty or
// the old default. Set values stay, other rows are untouched, and a new
// database has none of them.
func TestMigrateRetiredOptions(t *testing.T) {
	migrateTest(t)
	name := "mig_retired_opts"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	value := func(key string) (string, bool) {
		t.Helper()
		var v string
		err := db.QueryRow(`SELECT value FROM hlstats_Options WHERE keyname = '` + key + `'`).Scan(&v) // key: a constant below
		return v, err == nil
	}
	for _, k := range []string{"gamewe_default_game", "contact", "siteurl", "gamewe_geoip_db"} {
		if _, ok := value(k); ok {
			t.Errorf("new database has %s", k)
		}
	}

	admin := adminIn(t, name)
	for _, q := range []string{
		`DELETE FROM ` + MigrationsTable + ` WHERE version >= 13`,
		`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_default_game', 'css'), ('gamewe_daily_awards', '1'),
			('contact', ' '), ('siteurl', 'https://stats.example.org/'), ('gamewe_geoip_db', '/var/lib/gamewe/GeoLite2-City.mmdb')`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p, err := PlanMigrations(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Current != 12 || len(p.Steps) == 0 {
		t.Fatalf("plan after taking version 13 back: %+v", p)
	}
	mustMigrate(t, db)
	upToDate(t, db)
	for k, want := range map[string]bool{
		"gamewe_default_game": false,
		"gamewe_daily_awards": false,
		"contact":             false, // blank
		"siteurl":             true,  // set on purpose
		"gamewe_geoip_db":     false, // the old default
		"sitename":            true,
		"gamewe_page_size":    true,
	} {
		if _, ok := value(k); ok != want {
			t.Errorf("%s present: %v, want %v", k, ok, want)
		}
	}

	// Another path is still in use (config.yaml without geoip.path): kept.
	if _, err := admin.Exec(`DELETE FROM ` + MigrationsTable + ` WHERE version >= 13`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_geoip_db', '/srv/geo/city.mmdb')`); err != nil {
		t.Fatal(err)
	}
	mustMigrate(t, db)
	if v, _ := value("gamewe_geoip_db"); v != "/srv/geo/city.mmdb" {
		t.Errorf("gamewe_geoip_db = %q, want the set path", v)
	}
}
