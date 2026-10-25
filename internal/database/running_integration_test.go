// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// waitRunning waits until CollectorRunning reports want.
func waitRunning(t *testing.T, db *DB, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := CollectorRunning(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("CollectorRunning stays %v", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// toVersion4 takes versions 5 (server-info) and 6 (act-bots) back, as a
// database of 0.6.x.
func toVersion4(t *testing.T, admin *DB) {
	t.Helper()
	for _, q := range []string{
		`ALTER TABLE hlstats_Servers DROP COLUMN server_version, DROP COLUMN server_os, DROP COLUMN server_vac, DROP COLUMN act_bots`,
		`DELETE FROM ` + MigrationsTable + ` WHERE version >= 5`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func pending(t *testing.T, db *DB) int {
	t.Helper()
	p, err := PlanMigrations(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return len(p.Steps)
}

// A running collector holds the collector lock: Migrate refuses without
// changing anything, the lock comes back after its connection was killed
// and goes when the collector ends. A table another session holds makes a
// step fail after LockTimeout (with progress lines while it waits), and
// Ctrl-C while it waits cancels cleanly.
func TestMigrateCollectorLock(t *testing.T) {
	migrateTest(t)
	name := "mig_running"
	db := newTestDB(t, name, false)
	mustMigrate(t, db)
	admin := adminIn(t, name)
	toVersion4(t, admin)
	steps := pending(t, db)
	if steps == 0 {
		t.Fatal("nothing pending after taking version 5 back")
	}

	waitRunning(t, db, false)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := HoldCollectorLock(ctx, TestConfig(name, "gamewe", TestPassword("gamewe")), 100*time.Millisecond, log); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, db, true)
	_, err := Migrate(context.Background(), db, MigrateOptions{})
	if !errors.Is(err, ErrCollectorRunning) || !strings.Contains(err.Error(), "systemctl stop gamewe-collector") {
		t.Fatalf("migrate with a running collector: %v", err)
	}
	if n := pending(t, db); n != steps {
		t.Fatalf("refused migrate changed the database: %d steps pending, want %d", n, steps)
	}

	// The database ends the lock's session (restart, network): it is
	// taken again.
	kill := `SELECT CONCAT('KILL ', IS_USED_LOCK(LEFT(CONCAT(?, DATABASE()), 64)))`
	if admin.Postgres() {
		kill = `SELECT 'SELECT pg_terminate_backend(' || pid || ')' FROM pg_locks WHERE locktype = 'advisory' AND granted AND objid::bigint = $1`
	}
	var q string
	arg := any(runLockPrefix)
	if admin.Postgres() {
		arg = int64(runLockKey & 0xffffffff)
	}
	if err := admin.QueryRow(kill, arg).Scan(&q); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(q); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, db, true)
	stop()
	waitRunning(t, db, false)

	// A table held by another session: the step fails after LockTimeout.
	conn, err := admin.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hold := []string{`LOCK TABLES hlstats_Servers READ`}
	release := []string{`UNLOCK TABLES`}
	if admin.Postgres() {
		hold = []string{`BEGIN`, `LOCK TABLE hlstats_Servers IN ACCESS SHARE MODE`}
		release = []string{`ROLLBACK`}
	}
	execAll := func(c *sql.Conn, qs []string) {
		t.Helper()
		for _, q := range qs {
			if _, err := c.ExecContext(context.Background(), q); err != nil {
				t.Fatal(err)
			}
		}
	}
	execAll(conn, hold)
	var out strings.Builder
	start := time.Now()
	_, err = Migrate(context.Background(), db, MigrateOptions{Out: &out, LockTimeout: 2 * time.Second, Progress: time.Second})
	t.Logf("migrate output:\n%s", out.String())
	if !errors.Is(err, ErrTableLocked) || time.Since(start) > 20*time.Second {
		t.Fatalf("locked table: %v after %s", err, time.Since(start))
	}
	if !strings.Contains(out.String(), "still running after") {
		t.Error("no progress line while the step waits")
	}

	// Ctrl-C while a step waits: cancelled, the rest not done.
	cctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(time.Second, cancel)
	_, err = Migrate(cctx, db, MigrateOptions{LockTimeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), "migrate again") {
		t.Fatalf("cancelled migrate: %v", err)
	}
	execAll(conn, release)
	time.Sleep(time.Second) // a statement still waiting on the server would run now
	if n := pending(t, db); n != steps {
		t.Fatalf("the cancelled step was done after all: %d steps pending, want %d", n, steps)
	}
	mustMigrate(t, db)
	upToDate(t, db, "new database", "new database")

	// The lock timeout of the migration does not stay on the connection
	// (gamewe run goes on with its pool).
	cfg := TestConfig(name, "gamewe", TestPassword("gamewe"))
	cfg.MaxOpenConns = 1
	one, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, err := Migrate(context.Background(), one, MigrateOptions{LockTimeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	q, want := `SELECT @@SESSION.lock_wait_timeout = @@GLOBAL.lock_wait_timeout`, "1"
	if one.Postgres() {
		q, want = `SHOW lock_timeout`, "0"
	}
	var got string
	if err := one.QueryRow(q).Scan(&got); err != nil || got != want {
		t.Errorf("lock timeout after Migrate: %q, %v; want %q", got, err, want)
	}
}
