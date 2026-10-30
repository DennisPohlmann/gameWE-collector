// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The maintenance of gamewe db purge, optimize, reset and drop on a new
// database (named Migrate* so that the migration step of CI runs it).
func TestMigrateMaintenance(t *testing.T) {
	migrateTest(t)
	ctx := context.Background()
	name := "mig_maint"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	seeded := counts(t, db)

	own, other, err := OwnTables(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) > 0 || !slices.Contains(own, MigrationsTable) || !slices.Contains(own, "hlstats_Events_Frags") {
		t.Fatalf("own %v, other %v", own, other)
	}

	// purge: only events before the limit
	now := time.Now()
	old, recent := now.AddDate(0, 0, -100).Unix(), now.AddDate(0, 0, -1).Unix()
	for _, ts := range []int64{old, old, recent} {
		exec(t, db, `INSERT INTO hlstats_Events_Frags (timestamp_id, killerId, victimId) VALUES (?, 1, 2)`, ts)
		exec(t, db, `INSERT INTO hlstats_Events_Connects (timestamp_id, playerId) VALUES (?, 1)`, ts)
	}
	exec(t, db, `INSERT INTO hlstats_Players (game, kills) VALUES ('css', 3)`)
	before := now.AddDate(0, 0, -90)
	dry, err := Purge(ctx, db, before, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowsOf(dry); got["hlstats_Events_Frags"] != 2 || got["hlstats_Events_Connects"] != 2 || sum(dry) != 4 {
		t.Errorf("dry run: %v", dry)
	}
	if n := count(t, db, "hlstats_Events_Frags"); n != 3 {
		t.Errorf("the dry run deleted rows: %d frags", n)
	}
	done, err := Purge(ctx, db, before, false)
	if err != nil {
		t.Fatal(err)
	}
	if sum(done) != 4 || count(t, db, "hlstats_Events_Frags") != 1 || count(t, db, "hlstats_Events_Connects") != 1 || count(t, db, "hlstats_Players") != 1 {
		t.Errorf("purge: %v, frags %d", done, count(t, db, "hlstats_Events_Frags"))
	}
	tables, _ := PurgeTables(db.Type)
	if slices.Contains(tables, "hlstats_Players") || !slices.Contains(tables, "hlstats_Events_Chat") || !slices.Contains(tables, "hlstats_Events_Latency") {
		t.Errorf("purge tables %v", tables)
	}

	if err := Optimize(ctx, db, own, nil); err != nil {
		t.Errorf("optimize: %v", err)
	}
	if err := Optimize(ctx, db, []string{"mysql.user"}, nil); err == nil {
		t.Error("optimize of a foreign table")
	}

	// reset: everything as new, the counters from 1
	exec(t, db, `INSERT INTO hlstats_Servers (address, port, name, game) VALUES ('192.0.2.1', 27015, 'test', 'css')`)
	exec(t, db, `UPDATE hlstats_Games SET name = 'changed' WHERE code = 'css'`)
	if err := ResetData(ctx, db); err != nil {
		t.Fatal(err)
	}
	if after := counts(t, db); fmt.Sprint(after) != fmt.Sprint(seeded) {
		t.Errorf("after reset %v\nwant (as new) %v", after, seeded)
	}
	var gname string
	if err := db.QueryRowContext(ctx, `SELECT name FROM hlstats_Games WHERE code = 'css'`).Scan(&gname); err != nil || gname == "changed" {
		t.Errorf("css after reset: %q %v", gname, err)
	}
	exec(t, db, `INSERT INTO hlstats_Players (game) VALUES ('css')`)
	var id int
	if err := db.QueryRowContext(ctx, `SELECT playerId FROM hlstats_Players`).Scan(&id); err != nil || id != 1 {
		t.Errorf("first player after reset: id %d %v", id, err)
	}
	upToDate(t, db)

	// drop: on MariaDB the collector user may not; the admin with the SQL
	err = DropTables(ctx, db)
	var re *DropRightsError
	if db.Postgres() {
		if err != nil {
			t.Fatalf("drop as owner: %v", err)
		}
	} else {
		if !errors.As(err, &re) {
			t.Fatalf("drop without DROP right: %v", err)
		}
		if own2, _, _ := OwnTables(ctx, db); len(own2) != len(own) {
			t.Fatalf("a refused drop dropped tables: %d of %d left", len(own2), len(own))
		}
		q, err := DropSQL(db.Type, db.Prefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := RunTestScript(ctx, adminIn(t, name), q); err != nil {
			t.Fatal(err)
		}
	}
	if own, other, err := OwnTables(ctx, db); err != nil || len(own) > 0 || len(other) > 0 {
		t.Fatalf("after drop: %v %v %v", own, other, err)
	}
	if p, err := PlanMigrations(ctx, db); err != nil || p.Kind != KindEmpty {
		t.Fatalf("after drop: %+v %v", p, err)
	}
	mustMigrate(t, db) // db init
	upToDate(t, db)
	if after := counts(t, db); fmt.Sprint(after) != fmt.Sprint(seeded) {
		t.Errorf("after drop and init %v\nwant %v", after, seeded)
	}
}

// Other hlstats_ tables (an old HLstatsX) are listed, never dropped.
func TestMigrateDropKeepsOtherTables(t *testing.T) {
	migrateTest(t)
	ctx := context.Background()
	name := "mig_drop_other"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	admin := adminIn(t, name)
	if _, err := db.ExecContext(ctx, `CREATE TABLE hlstats_Heatmap_Config (id integer)`); err != nil {
		t.Fatal(err)
	}
	_, other, err := OwnTables(ctx, db)
	if err != nil || len(other) != 1 || !strings.EqualFold(other[0], db.Prefix.Table("hlstats_heatmap_config")) {
		t.Fatalf("other %v %v", other, err)
	}
	q, _ := DropSQL(db.Type, db.Prefix)
	if strings.Contains(strings.ToLower(q), "heatmap") {
		t.Fatalf("drop SQL names a foreign table: %s", q)
	}
	if err := RunTestScript(ctx, admin, q); err != nil {
		t.Fatal(err)
	}
	if own, other, _ := OwnTables(ctx, db); len(own) != 0 || len(other) != 1 {
		t.Errorf("after drop: own %v other %v", own, other)
	}
}

func exec(t *testing.T, db *DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(db.Q(q), args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func count(t *testing.T, db *DB, table string) int64 {
	t.Helper()
	r, err := CountRows(context.Background(), db, []string{table})
	if err != nil {
		t.Fatal(err)
	}
	return r[0].Rows
}

func rowsOf(r []TableRows) map[string]int64 {
	m := map[string]int64{}
	for _, x := range r {
		m[x.Table] = x.Rows
	}
	return m
}

func sum(r []TableRows) (n int64) {
	for _, x := range r {
		n += x.Rows
	}
	return n
}
