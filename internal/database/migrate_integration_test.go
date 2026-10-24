// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

// Integration tests of gamewe db migrate on throwaway databases (see
// migratetest_integration.go for the environment).
package database

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/schema"
)

func migrateTest(t *testing.T) {
	t.Helper()
	if MigrateTestEnv() == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
}

// newTestDB creates the database name and returns the collector's
// connection to it.
func newTestDB(t *testing.T, name string, oldRights bool) *DB {
	t.Helper()
	ctx := context.Background()
	if err := CreateTestDatabase(ctx, name, oldRights); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DropTestDatabase(context.Background(), name) })
	return openAs(t, name, "gamewe")
}

func openAs(t *testing.T, name, user string) *DB {
	t.Helper()
	db, err := Open(TestConfig(name, user, TestPassword(user)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func adminIn(t *testing.T, name string) *DB {
	t.Helper()
	db, err := AdminDB(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// counts returns the rows of every hlstats_ table.
func counts(t *testing.T, db *DB) map[string]int {
	t.Helper()
	q := `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE 'hlstats\_%'`
	if db.Postgres() {
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'hlstats\_%'`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, tb := range names {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[strings.ToLower(tb)] = n
	}
	return out
}

func mustMigrate(t *testing.T, db *DB) *MigrateResult {
	t.Helper()
	var out strings.Builder
	res, err := Migrate(context.Background(), db, MigrateOptions{Out: &out})
	t.Logf("migrate output:\n%s", out.String())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, w := range res.Warnings {
		t.Errorf("warning: %s", w)
	}
	return res
}

// upToDate checks the database after a migration: nothing pending,
// nothing missing, every version recorded, and a second run changes nothing.
func upToDate(t *testing.T, db *DB, notes ...string) {
	t.Helper()
	ctx := context.Background()
	p, err := PlanMigrations(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !p.UpToDate() || len(p.Missing) > 0 || p.Current != Latest() || len(p.Warnings) > 0 {
		t.Fatalf("after migrate: %d steps %+v, missing %v, version %d, warnings %v", len(p.Steps), p.Steps, p.Missing, p.Current, p.Warnings)
	}
	if len(p.Applied) != len(Migrations) {
		t.Errorf("applied %+v", p.Applied)
	}
	for i, n := range notes {
		if i < len(p.Applied) && p.Applied[i].Note != n {
			t.Errorf("v%d note %q, want %q", p.Applied[i].Version, p.Applied[i].Note, n)
		}
	}
	before := counts(t, db)
	res := mustMigrate(t, db)
	if !res.Plan.UpToDate() {
		t.Errorf("second migrate was not a no-op: %+v", res.Plan.Steps)
	}
	if after := counts(t, db); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("second migrate changed rows: %v -> %v", before, after)
	}
	if err := CheckTables(ctx, db); err != nil {
		t.Error(err)
	}
}

// webRights checks the rights of the web users after a migration.
func webRights(t *testing.T, name string) {
	t.Helper()
	web := openAs(t, name, "gamewe_web")
	for _, q := range []string{`SELECT COUNT(*) FROM hlstats_Events_Latency`, `SELECT COUNT(*) FROM hlstats_gamewe_jobs`, `SELECT COUNT(name) FROM hlstats_Servers`} {
		var n int
		if err := web.QueryRow(q).Scan(&n); err != nil {
			t.Errorf("gamewe_web: %s: %v", q, err)
		}
	}
	for _, q := range []string{`SELECT COUNT(*) FROM hlstats_Users`, `SELECT COUNT(rcon_password) FROM hlstats_Servers`, `SELECT COUNT(log_secret) FROM hlstats_Servers`, `SELECT COUNT(*) FROM hlstats_Events_Chat`} {
		var n int
		if err := web.QueryRow(q).Scan(&n); err == nil {
			t.Errorf("gamewe_web may: %s", q)
		}
	}
	adm := openAs(t, name, "gamewe_web_admin")
	if _, err := adm.Exec(adm.Q(`INSERT INTO hlstats_gamewe_jobs (job_type, game, mode, requested_by) VALUES ('reset', 'zzt', 'stats', 'test')`)); err != nil {
		t.Errorf("gamewe_web_admin cannot queue a job: %v", err)
	}
	if _, err := adm.Exec(`DELETE FROM hlstats_Events_Frags`); err == nil {
		t.Error("gamewe_web_admin may delete events")
	}
}

// A new database from the init script: the plan creates everything, the
// collector user may do it, and a second run is a no-op.
func TestMigrateFresh(t *testing.T) {
	migrateTest(t)
	ctx := context.Background()
	db := newTestDB(t, "mig_fresh", false)
	p, err := PlanMigrations(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindEmpty || p.UpToDate() || p.Versions || p.Current != 0 || len(p.Pending) != len(Migrations) {
		t.Fatalf("plan of a new database: %+v", p)
	}
	tables, _ := schema.Tables(db.Type)
	if len(p.Missing) != len(tables) {
		t.Errorf("missing %d of %d tables", len(p.Missing), len(tables))
	}
	res := mustMigrate(t, db)
	if res.Plan.UpToDate() {
		t.Fatal("nothing migrated")
	}
	upToDate(t, db, "new database", "new database")
	var games, opts int
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Games`).Scan(&games)
	db.QueryRow(`SELECT COUNT(*) FROM hlstats_Options WHERE keyname LIKE 'gamewe_%'`).Scan(&opts)
	if games < 5 || opts < 5 {
		t.Errorf("reference data: %d games, %d gamewe options", games, opts)
	}
	webRights(t, "mig_fresh")
}

// The old HLstatsX database gameWE started from (testdata/hlstatsx-old.sql
// with a few rows), with the collector user's rights of gameWE <= 0.2.1:
// the migration first stops with the GRANT to run, then brings it to the
// current schema without losing a row; the same database with
// rcon_password and realgame already added (as HLstatsX:CE has them) too.
func TestMigrateHLstatsX(t *testing.T) {
	migrateTest(t)
	if MigrateTestEnv() != config.MySQL {
		t.Skip("HLstatsX databases are MariaDB / MySQL")
	}
	for _, ce := range []bool{false, true} {
		name := map[bool]string{false: "mig_hlstatsx", true: "mig_hlstatsx_rcon"}[ce]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := newTestDB(t, name, true)
			admin := adminIn(t, name)
			for _, f := range []string{"internal/database/testdata/hlstatsx-old.sql", "internal/database/testdata/hlstatsx-old-data.sql"} {
				if err := LoadTestFile(ctx, admin, f); err != nil {
					t.Fatal(err)
				}
			}
			if ce {
				for _, q := range []string{"ALTER TABLE hlstats_Games ADD COLUMN realgame varchar(32) NOT NULL DEFAULT 'css' AFTER name",
					"ALTER TABLE hlstats_Servers ADD COLUMN rcon_password varchar(128) NOT NULL DEFAULT '' AFTER game",
					"UPDATE hlstats_Servers SET rcon_password = 'test-rcon' WHERE serverId = 1"} {
					if _, err := admin.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := counts(t, db)
			p, err := PlanMigrations(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != KindHLstatsX || p.UpToDate() || len(p.Missing) == 0 {
				t.Fatalf("plan: kind %s, %d steps, missing %v", p.Kind, len(p.Steps), p.Missing)
			}
			// Without CREATE / ALTER: a clear error with the GRANT, nothing changed.
			_, err = Migrate(ctx, db, MigrateOptions{})
			var re *RightsError
			if !errors.As(err, &re) || !strings.Contains(re.Fix, "GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX ON `"+name+"`.* TO 'gamewe'@'%' WITH GRANT OPTION;") {
				t.Fatalf("without rights: %v", err)
			}
			t.Logf("rights error:\n%v", err)
			if after, _ := MissingSchema(ctx, db); len(after) != len(p.Missing) {
				t.Errorf("the failed migration changed the schema")
			}
			if _, err := admin.Exec(strings.TrimSpace(re.Fix)); err != nil {
				t.Fatalf("the fix of the error message: %v", err)
			}
			db = openAs(t, name, "gamewe") // MariaDB applies new database rights to new connections

			res := mustMigrate(t, db)
			if res.Plan.Kind != KindHLstatsX {
				t.Errorf("kind %s", res.Plan.Kind)
			}
			upToDate(t, db, "existing HLstatsX database", "applied")
			after := counts(t, db)
			seeded := []string{"hlstats_actions", "hlstats_awards", "hlstats_clantags", "hlstats_definition_ranks_ribbons", "hlstats_definition_ribbons_classes",
				"hlstats_games", "hlstats_options", "hlstats_roles", "hlstats_teams", "hlstats_weapons"}
			for tb, n := range before {
				switch {
				case slices.Contains(seeded, tb) && after[tb] < n:
					t.Errorf("%s: %d rows, before %d", tb, after[tb], n)
				case !slices.Contains(seeded, tb) && after[tb] != n:
					t.Errorf("%s: %d rows, before %d", tb, after[tb], n)
				}
			}
			// The old rows are unchanged, the new columns have their defaults.
			var hidden, sitename, rcon string
			var serverID, frags, killer int
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Error(err)
				}
			}
			check(db.QueryRow(`SELECT hidden FROM hlstats_Games WHERE code = 'css2'`).Scan(&hidden))
			check(db.QueryRow(`SELECT value FROM hlstats_Options WHERE keyname = 'sitename'`).Scan(&sitename))
			check(db.QueryRow(`SELECT rcon_password FROM hlstats_Servers WHERE serverId = 1`).Scan(&rcon))
			check(db.QueryRow(`SELECT COUNT(*), SUM(killerId), MAX(server_id) FROM hlstats_Events_Frags`).Scan(&frags, &killer, &serverID))
			wantRcon := map[bool]string{false: "", true: "test-rcon"}[ce]
			if hidden != "1" || sitename != "Old stats" || rcon != wantRcon || frags != 3 || killer != 4 || serverID != 0 {
				t.Errorf("data after migrate: hidden %q sitename %q rcon %q frags %d killer %d server_id %d", hidden, sitename, rcon, frags, killer, serverID)
			}
			var name2 string
			check(db.QueryRow(`SELECT name FROM hlstats_PlayerNames WHERE playerId = 2`).Scan(&name2))
			if name2 != "Bravo ä" {
				t.Errorf("player name %q", name2)
			}
			cat, _ := readCatalog(ctx, db, false)
			if cat.hasIndex("hlstats_Events_Frags", "killerId") || !cat.hasIndex("hlstats_Events_Frags", "killer_time") {
				t.Errorf("frags indexes %v", cat.indexes["hlstats_events_frags"])
			}
			webRights(t, name)
		})
	}
}

// A gameWE database created by the init scripts of 0.2.1 (tables owned by
// the admin, no version table) and one of an older gameWE (no
// hlstats_gamewe_jobs, no server_id and kill positions, killerId / victimId):
// the error names the fix for the rights, then both are brought up.
func TestMigrateOldGameWE(t *testing.T) {
	migrateTest(t)
	for _, older := range []bool{false, true} {
		name := map[bool]string{false: "mig_v021", true: "mig_v020"}[older]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := newTestDB(t, name, true)
			admin := adminIn(t, name)
			for _, s := range []string{schema.Schema(db.Type), schema.Seed(db.Type), schema.Grants(db.Type)} {
				if err := RunTestScript(ctx, admin, s); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := admin.Exec(admin.Q(`INSERT INTO hlstats_Players (playerId, game, skill) VALUES (4711, 'css', 1234)`)); err != nil {
				t.Fatal(err)
			}
			if older {
				var qs []string
				if admin.Postgres() {
					qs = []string{`DROP TABLE hlstats_gamewe_jobs`, `ALTER TABLE hlstats_Events_Frags DROP COLUMN server_id, DROP COLUMN pos_x, DROP COLUMN pos_victim_z`,
						`DROP INDEX hlstats_Events_Frags_killer_time`, `DROP INDEX hlstats_Events_Frags_victim_time`,
						`CREATE INDEX hlstats_Events_Frags_killerId ON hlstats_Events_Frags (killerId)`, `CREATE INDEX hlstats_Events_Frags_victimId ON hlstats_Events_Frags (victimId)`}
				} else {
					qs = []string{"DROP TABLE hlstats_gamewe_jobs", "ALTER TABLE hlstats_Events_Frags DROP COLUMN server_id, DROP COLUMN pos_x, DROP COLUMN pos_victim_z, " +
						"DROP KEY killer_time, DROP KEY victim_time, ADD KEY killerId (killerId), ADD KEY victimId (victimId)"}
				}
				for _, q := range qs {
					if _, err := admin.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}
			p, err := PlanMigrations(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != KindGameWE || (len(p.Missing) > 0) != older {
				t.Fatalf("plan: kind %s, missing %v", p.Kind, p.Missing)
			}
			_, err = Migrate(ctx, db, MigrateOptions{})
			var re *RightsError
			if !errors.As(err, &re) {
				t.Fatalf("without rights: %v", err)
			}
			t.Logf("rights error:\n%v", err)
			// The fix, as the admin runs it (psql's \c is the connection).
			var fix []string
			for _, l := range strings.Split(re.Fix, "\n") {
				if !strings.HasPrefix(strings.TrimSpace(l), `\c `) {
					fix = append(fix, l)
				}
			}
			if err := RunTestScript(ctx, admin, strings.Join(fix, "\n")); err != nil {
				t.Fatalf("the fix of the error message: %v\n%s", err, re.Fix)
			}
			db = openAs(t, name, "gamewe")
			mustMigrate(t, db)
			upToDate(t, db, "existing gameWE database", "applied")
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM hlstats_Players WHERE playerId = 4711 AND skill = 1234`).Scan(&n); err != nil || n != 1 {
				t.Errorf("player row: %d, %v", n, err)
			}
			cat, _ := readCatalog(ctx, db, db.Postgres())
			if cat.hasIndex("hlstats_Events_Frags", indexName(db.Postgres(), "hlstats_Events_Frags", "killerId")) {
				t.Error("killerId is still there")
			}
			webRights(t, name)
		})
	}
}

// Two migrations at the same time: one does the work, the other waits for
// the lock and then finds nothing to do.
func TestMigrateLock(t *testing.T) {
	migrateTest(t)
	ctx := context.Background()
	newTestDB(t, "mig_lock", false)
	var wg sync.WaitGroup
	results := make([]*MigrateResult, 2)
	errs := make([]error, 2)
	waited := make([]bool, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db := openAs(t, "mig_lock", "gamewe")
			results[i], errs[i] = Migrate(ctx, db, MigrateOptions{Waiting: func() { waited[i] = true }})
		}()
	}
	wg.Wait()
	did := 0
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if !results[i].Plan.UpToDate() {
			did++
		}
	}
	if did != 1 || waited[0] == waited[1] {
		t.Errorf("%d runs migrated (want 1), waited %v", did, waited)
	}
	db := openAs(t, "mig_lock", "gamewe")
	upToDate(t, db, "new database", "new database")

	// A held lock: Migrate waits, then gives up with a clear error.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	unlock, err := migrateLock(ctx, conn, db.Postgres(), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := openAs(t, "mig_lock", "gamewe")
	told := false
	_, err = Migrate(ctx, other, MigrateOptions{LockWait: 2 * time.Second, Waiting: func() { told = true }})
	if err == nil || !strings.Contains(err.Error(), "another gamewe db migrate") || !told {
		t.Errorf("held lock: %v (told %v)", err, told)
	}
	unlock()
}

// A user with all rights on its database gets no rights error: MariaDB
// ALL PRIVILEGES ON db.* (without GRANT OPTION: the web users' rights are
// skipped with one warning), PostgreSQL the owner of the database and of
// its tables (the owner may grant: no warning).
func TestMigrateOwnerRights(t *testing.T) {
	migrateTest(t)
	ctx := context.Background()
	name := "mig_owner"
	db := newTestDB(t, name, true)
	admin := adminIn(t, name)
	if db.Postgres() {
		server := adminIn(t, "")
		if _, err := server.Exec(`ALTER DATABASE ` + name + ` OWNER TO gamewe`); err != nil {
			t.Fatal(err)
		}
		// The old install, run as the owner: the tables belong to gamewe.
		owner := openAs(t, name, "gamewe")
		for _, s := range []string{schema.Schema(db.Type), schema.Seed(db.Type)} {
			if err := RunTestScript(ctx, owner, s); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		if _, err := admin.Exec("GRANT ALL PRIVILEGES ON `" + name + "`.* TO 'gamewe'@'%'"); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"internal/database/testdata/hlstatsx-old.sql", "internal/database/testdata/hlstatsx-old-data.sql"} {
			if err := LoadTestFile(ctx, admin, f); err != nil {
				t.Fatal(err)
			}
		}
	}
	db = openAs(t, name, "gamewe")
	var out strings.Builder
	res, err := Migrate(ctx, db, MigrateOptions{Out: &out})
	t.Logf("migrate output:\n%s", out.String())
	if err != nil {
		t.Fatalf("all rights, but: %v", err)
	}
	switch {
	case db.Postgres() && len(res.Warnings) != 0:
		t.Errorf("owner: warnings %v", res.Warnings)
	case !db.Postgres() && (len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "may not grant rights")):
		t.Errorf("ALL without GRANT OPTION: warnings %v (want one)", res.Warnings)
	}
	t.Logf("warnings: %v", res.Warnings)
	upToDate(t, db)
}
