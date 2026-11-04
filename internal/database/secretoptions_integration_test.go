// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

// Version 25 moves the secret settings out of hlstats_Options, leaves the
// other settings there, and a second run changes nothing.
func TestMigrateSecretOptions(t *testing.T) {
	migrateTest(t)
	name := "mig_secret_options"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	admin := adminIn(t, name)
	for _, q := range []string{
		`DELETE FROM ` + MigrationsTable + ` WHERE version >= 25`,
		`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_metrics_token', 'dummy-token'), ('gamewe_ts_host', 'ts.example.org')`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if p, err := PlanMigrations(context.Background(), db); err != nil || p.Current != 24 {
		t.Fatalf("plan after taking version 25 back: %+v, %v", p, err)
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
	if n := count(`SELECT COUNT(*) FROM hlstats_gamewe_secrets WHERE keyname = 'gamewe_metrics_token' AND value = 'dummy-token'`); n != 1 {
		t.Errorf("token in hlstats_gamewe_secrets: %d rows", n)
	}
	if n := count(`SELECT COUNT(*) FROM hlstats_Options WHERE keyname = 'gamewe_metrics_token'`); n != 0 {
		t.Error("token still in hlstats_Options")
	}
	if n := count(`SELECT COUNT(*) FROM hlstats_Options WHERE keyname = 'gamewe_ts_host'`); n != 1 {
		t.Error("another setting left hlstats_Options")
	}
}
