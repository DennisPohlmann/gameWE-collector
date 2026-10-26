// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

// The gamewe binary against throwaway databases (environment: see
// internal/database/migratetest_integration.go): "run" refuses an old
// schema with exit code 1, "db migrate" with -n / -f / the question brings
// it up, then "run" starts.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
)

func buildGamewe(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gamewe")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func writeConfig(t *testing.T, d config.Database) string {
	t.Helper()
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	pwFile := filepath.Join(t.TempDir(), "db-password")
	os.WriteFile(pwFile, []byte(d.Password), 0o600)
	cfg := fmt.Sprintf(`listen:
  address: 127.0.0.1
  port: %d
database:
  type: %s
  host: %s
  port: %d
  user: %s
  password_file: %s
  name: %s
  auto_migrate: %v
  tls:
    mode: disable
geoip:
  path: %s
  auto_update: false
features:
  rcon_status: false
  log_address: false
  daily_awards:
    enabled: false
`, port, d.Type, d.Host, d.Port, d.User, pwFile, d.Name, d.AutoMigrate, filepath.Join(t.TempDir(), "none.mmdb"))
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// gw runs the binary; stdin is not a terminal.
func gw(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader("y\n")
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	t.Logf("$ gamewe %s  (exit %d)\n%s", strings.Join(args, " "), code, out)
	return string(out), code
}

func expect(t *testing.T, out string, code, wantCode int, wants ...string) {
	t.Helper()
	if code != wantCode {
		t.Errorf("exit code %d, want %d", code, wantCode)
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
}

// runStarts starts "gamewe run", waits until it listens, stops it and
// returns its log.
func runStarts(t *testing.T, bin, cfg string) string {
	t.Helper()
	return startRun(t, bin, cfg)()
}

// startRun starts "gamewe run" and waits until it listens; the function it
// returns stops it and returns its log.
func startRun(t *testing.T, bin, cfg string) func() string {
	t.Helper()
	cmd := exec.Command(bin, "run", "-config", cfg)
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	listening := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.WriteString(sc.Text() + "\n")
			if strings.Contains(sc.Text(), "listening for game server logs") {
				listening <- true
			}
		}
		close(listening)
	}()
	select {
	case ok := <-listening:
		if !ok {
			cmd.Wait()
			t.Fatalf("gamewe run ended:\n%s", log.String())
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("gamewe run does not listen:\n%s", log.String())
	}
	return func() string {
		t.Helper()
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			t.Errorf("gamewe run after SIGTERM: %v\n%s", err, log.String())
		}
		if !strings.Contains(log.String(), "database schema up to date") {
			t.Errorf("no schema line:\n%s", log.String())
		}
		return log.String()
	}
}

// rerunLine is the line of a refusal that runs bin with args again; in a
// container (tests in a podman/docker container) the docker compose one.
func rerunLine(bin string, args ...string) string {
	if inContainer() {
		return "  docker compose run --rm collector " + strings.Join(args, " ") + "\n"
	}
	return "  " + bin + " " + strings.Join(args, " ") + "\n"
}

// killLine is the line of a refusal that stops the plain process pid.
func killLine(pid int) string {
	if inContainer() {
		return "  docker compose stop collector"
	}
	return fmt.Sprintf("  kill %d\n", pid)
}

// beating writes the heartbeat every 500 ms, as a running collector up to
// 0.7.1 (no collector lock) does every minute, until the returned function
// is called.
func beating(t *testing.T, admin *database.DB) func() {
	t.Helper()
	beat := func() {
		if _, err := admin.Exec(admin.Q(`UPDATE hlstats_Options SET value = ? WHERE keyname = 'gamewe_heartbeat'`), fmt.Sprint(time.Now().Unix())); err != nil {
			t.Error(err)
		}
	}
	beat()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			case <-time.After(500 * time.Millisecond):
				beat()
			}
		}
	}()
	return func() { close(done); <-stopped }
}

// gamewe db migrate refuses while a collector runs: one that holds the
// collector lock (gamewe run of this version, also from another host), or
// a gamewe run on this host without it (up to 0.7.1). It names the PID and
// the commands to run. A fresh heartbeat without either is only noted:
// nothing waits for it. Nothing is changed while it refuses.
func TestMigrateRefusesRunningCollector(t *testing.T) {
	if database.MigrateTestEnv() == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
	ctx := context.Background()
	name := "mig_refuse"
	if err := database.CreateTestDatabase(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DropTestDatabase(context.Background(), name) })
	admin, err := database.AdminDB(name)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	bin := buildGamewe(t)
	d := database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	cfg := writeConfig(t, d)
	stop := startRun(t, bin, cfg) // creates the tables (auto_migrate) and holds the collector lock
	for _, q := range []string{
		`ALTER TABLE hlstats_Servers DROP COLUMN server_version, DROP COLUMN server_os, DROP COLUMN server_vac, DROP COLUMN act_bots`,
		`DELETE FROM ` + database.MigrationsTable + ` WHERE version >= 5`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	version := func() int {
		t.Helper()
		p, err := database.PlanMigrations(ctx, admin)
		if err != nil {
			t.Fatal(err)
		}
		return p.Current
	}
	start := time.Now()
	out, code := gw(t, bin, "db", "migrate", "-f", "-config", cfg)
	expect(t, out, code, 1, "a collector is running on this host:", "PID ", bin+" run -config "+cfg, "Stop it first, then migrate",
		rerunLine(bin, "db", "migrate", "-f", "-config", cfg))
	if strings.Contains(out, "0.7.1") {
		t.Error("the collector with the lock called an old one")
	}
	if time.Since(start) > 15*time.Second || version() != 4 {
		t.Errorf("refusal took %s, version %d", time.Since(start), version())
	}
	log := stop()
	if !strings.Contains(log, "collector lock held") {
		t.Errorf("run log lacks the collector lock:\n%s", log)
	}

	// The lock of a collector elsewhere (another host, a container).
	release, err := database.LockCollector(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	out, code = gw(t, bin, "db", "migrate", "-f", "-config", cfg)
	release()
	expect(t, out, code, 1, "it holds the collector lock), but not here", "  docker compose stop collector", "docker stop CONTAINER",
		"systemctl stop gamewe-collector", rerunLine(bin, "db", "migrate", "-f", "-config", cfg), "  docker compose start collector")

	// A collector up to 0.7.1: no lock, its process runs here.
	stopBeat := beating(t, admin)
	pid, kill := fakeRun(t, cfg)
	start = time.Now()
	out, code = gw(t, bin, "db", "migrate", "-f", "-config", cfg)
	kill()
	expect(t, out, code, 1, fmt.Sprintf("PID %d: ", pid), "holds no collector lock", killLine(pid))
	if time.Since(start) > 15*time.Second || version() != 4 {
		t.Fatalf("refusal took %s, version %d", time.Since(start), version())
	}

	// A fresh heartbeat, no lock, no process: noted, no waiting.
	start = time.Now()
	out, code = gw(t, bin, "db", "migrate", "-f", "-config", cfg)
	stopBeat()
	expect(t, out, code, 0, "note: the collector heartbeat is", "[1/", "done: the database has schema version")
	if strings.Contains(out, "waiting up to") || time.Since(start) > 15*time.Second {
		t.Errorf("waited (%s)", time.Since(start))
	}
	if version() != database.Latest() {
		t.Errorf("version %d after migrate", version())
	}
}

// The way of an old HLstatsX database (MariaDB; on PostgreSQL a new one):
// run fails, the plan, the questions and the rights error, the migration,
// a second migration that does nothing, then the collector starts.
func TestMigrateBinary(t *testing.T) {
	typ := database.MigrateTestEnv()
	if typ == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
	ctx := context.Background()
	name := "mig_e2e"
	if err := database.CreateTestDatabase(ctx, name, typ == config.MySQL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DropTestDatabase(context.Background(), name) })
	admin, err := database.AdminDB(name)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if typ == config.MySQL {
		for _, f := range []string{"internal/database/testdata/hlstatsx-old.sql", "internal/database/testdata/hlstatsx-old-data.sql"} {
			if err := database.LoadTestFile(ctx, admin, f); err != nil {
				t.Fatal(err)
			}
		}
	}
	bin := buildGamewe(t)
	d := database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	d.AutoMigrate = false
	cfg := writeConfig(t, d)

	out, code := gw(t, bin, "run", "-config", cfg)
	expect(t, out, code, exitConfig, "does not match the layout this collector needs", "database.auto_migrate is false", "Make a backup, then run: ", "gamewe db migrate -config "+cfg)
	out, code = gw(t, bin, "check", "-config", cfg)
	expect(t, out, code, 1, "migration steps pending, the collector does not start", "db migrate -config "+cfg)
	out, code = gw(t, bin, "db")
	expect(t, out, code, 0, "gamewe db migrate", "gamewe db status")
	out, code = gw(t, bin, "db", "migrate", "-f", "-n", "-config", cfg)
	expect(t, out, code, 2, "cannot be used together")
	out, code = gw(t, bin, "db", "migrate", "--dry-run", "-config", cfg)
	expect(t, out, code, 2, "plan (", "Dry run: nothing changed")
	out, code = gw(t, bin, "db", "migrate", "-config", cfg)
	expect(t, out, code, 1, "plan (", "no interactive confirmation possible", "-force")
	if typ == config.MySQL && !strings.Contains(out, "Make a backup before migrating") { // PostgreSQL: a new database, nothing to back up
		t.Error("no backup advice")
	}
	out, code = gw(t, bin, "db", "status", "-config", cfg)
	expect(t, out, code, 0, "NOT up to date", "pending:   v1 baseline")
	if typ == config.MySQL {
		out, code = gw(t, bin, "db", "migrate", "-f", "-config", cfg)
		expect(t, out, code, 1, "may not change the table structure", "WITH GRANT OPTION")
		if _, err := admin.Exec("GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX ON `" + name + "`.* TO 'gamewe'@'%' WITH GRANT OPTION"); err != nil {
			t.Fatal(err)
		}
	}
	out, code = gw(t, bin, "db", "migrate", "--force", "-config", cfg)
	expect(t, out, code, 0, "plan (", "done: the database has schema version")
	out, code = gw(t, bin, "db", "migrate", "-n", "-config", cfg)
	expect(t, out, code, 0, "The database is up to date.")
	out, code = gw(t, bin, "db", "migrate", "-config", cfg) // nothing to do: no question
	expect(t, out, code, 0, "The database is up to date.")
	out, code = gw(t, bin, "db", "status", "-config", cfg)
	expect(t, out, code, 0, ": up to date", "applied:   v1 baseline", "v2 gamewe-0.2.1", "pending:   none")
	out, _ = gw(t, bin, "check", "-config", cfg)
	expect(t, out, 0, 0, fmt.Sprintf("schema:    ok (version %d)", database.Latest()))
	runStarts(t, bin, cfg)
}

// The question of gamewe db migrate: no and an empty answer change nothing,
// "ja" migrates.
func TestMigrateQuestion(t *testing.T) {
	if database.MigrateTestEnv() == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
	ctx := context.Background()
	name := "mig_question"
	if err := database.CreateTestDatabase(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DropTestDatabase(context.Background(), name) })
	d := database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	db, err := database.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, answer := range []string{"n\n", "\n", ""} {
		var out bytes.Buffer
		err := migrate(ctx, db, d, migrateFlags{}, strings.NewReader(answer), &out, true, nil)
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 1 || !strings.Contains(out.String(), "Cancelled: nothing changed") {
			t.Errorf("answer %q: %v\n%s", answer, err, out.String())
		}
	}
	if p, _ := database.PlanMigrations(ctx, db); p == nil || p.Versions {
		t.Fatal("a cancelled migration changed the database")
	}
	var out bytes.Buffer
	if err := migrate(ctx, db, d, migrateFlags{}, strings.NewReader("ja\n"), &out, true, nil); err != nil {
		t.Fatalf("ja: %v\n%s", err, out.String())
	}
	if p, _ := database.PlanMigrations(ctx, db); p == nil || !p.UpToDate() {
		t.Errorf("after ja: not up to date\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Backup made? Migrate now? [y/N]") {
		t.Errorf("no question:\n%s", out.String())
	}
}

// database.auto_migrate (the default): gamewe run creates the tables of a
// new database itself and starts; with auto_migrate: false it refuses; an
// HLstatsX database without versions is never migrated automatically; a
// user without the rights stops the start with the GRANT to run.
func TestMigrateAuto(t *testing.T) {
	typ := database.MigrateTestEnv()
	if typ == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
	ctx := context.Background()
	bin := buildGamewe(t)
	newDB := func(name string, oldRights bool) config.Database {
		if err := database.CreateTestDatabase(ctx, name, oldRights); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.DropTestDatabase(context.Background(), name) })
		return database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	}

	d := newDB("mig_auto", false)
	off := d
	off.AutoMigrate = false
	out, code := gw(t, bin, "run", "-config", writeConfig(t, off))
	expect(t, out, code, exitConfig, "database.auto_migrate is false", "db migrate")
	cfg := writeConfig(t, d)
	out, _ = gw(t, bin, "check", "-config", cfg)
	expect(t, out, 0, 0, "gamewe run migrates at start (database.auto_migrate)")
	log := runStarts(t, bin, cfg)
	for _, want := range []string{"migrating automatically (database.auto_migrate)", "migrate: [1/", "create all", "database migrated"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	out, code = gw(t, bin, "db", "status", "-config", cfg)
	expect(t, out, code, 0, ": up to date", "pending:   none")
	if log := runStarts(t, bin, cfg); strings.Contains(log, "migrating automatically") {
		t.Errorf("second start migrated again:\n%s", log)
	}

	if typ == config.MySQL {
		d := newDB("mig_auto_hlx", true)
		admin, err := database.AdminDB(d.Name)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		for _, f := range []string{"internal/database/testdata/hlstatsx-old.sql", "internal/database/testdata/hlstatsx-old-data.sql"} {
			if err := database.LoadTestFile(ctx, admin, f); err != nil {
				t.Fatal(err)
			}
		}
		out, code = gw(t, bin, "run", "-config", writeConfig(t, d))
		expect(t, out, code, exitConfig, "not migrated automatically", "taken over once by hand", "db migrate")
		if p, _ := database.PlanMigrations(ctx, admin); p == nil || p.Versions {
			t.Error("the HLstatsX database was changed")
		}
	}

	d = newDB("mig_auto_rights", true)
	out, code = gw(t, bin, "run", "-config", writeConfig(t, d))
	expect(t, out, code, 1, "automatic migration (database.auto_migrate) failed", "may not change the table structure")
}

// gamewe db init, purge, optimize, reset and drop: the questions, -yes,
// -dry-run, the refusal while a collector runs and the DROP right.
func TestMigrateDBCommands(t *testing.T) {
	typ := database.MigrateTestEnv()
	if typ == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
	ctx := context.Background()
	name := "mig_cmds"
	if err := database.CreateTestDatabase(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DropTestDatabase(context.Background(), name) })
	admin, err := database.AdminDB(name)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(admin.Q(q), args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	bin := buildGamewe(t)
	d := database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	d.AutoMigrate = false
	cfg := writeConfig(t, d)

	out, code := gw(t, bin, "db", "init", "-config", cfg)
	expect(t, out, code, 1, "create all", "no interactive confirmation possible", "--force") // no terminal
	out, code = gw(t, bin, "db", "init", "-n", "-config", cfg)
	expect(t, out, code, 0, "create all", "Dry run: nothing changed")
	out, code = gw(t, bin, "db", "init", "-f", "-config", cfg)
	expect(t, out, code, 0, "create all", "done: the database has schema version")
	if strings.Contains(out, "Make a backup") {
		t.Error("backup advice for a new database")
	}
	out, code = gw(t, bin, "db", "init", "-config", cfg)
	expect(t, out, code, 1, "is not empty", "db migrate", "db reset")

	out, code = gw(t, bin, "db", "purge", "-config", cfg)
	expect(t, out, code, 2, "-older-than", "at least 7")
	out, code = gw(t, bin, "db", "purge", "-older-than", "3d", "-config", cfg)
	expect(t, out, code, 2, "at least 7")
	out, code = gw(t, bin, "db", "purge", "-older-than", "30", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, "nothing to delete")
	old := time.Now().AddDate(0, 0, -40).Unix()
	sql(`INSERT INTO hlstats_Events_Frags (timestamp_id, killerId, victimId) VALUES (?, 1, 2)`, old)
	sql(`INSERT INTO hlstats_Events_Frags (timestamp_id, killerId, victimId) VALUES (?, 1, 2)`, time.Now().Unix())
	out, code = gw(t, bin, "db", "purge", "-older-than", "30d", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, "dry run: nothing changed")
	oneFrag(t, out)
	out, code = gw(t, bin, "db", "purge", "-older-than", "30d", "-config", cfg)
	expect(t, out, code, 1, "no interactive confirmation possible", "--force")
	out, code = gw(t, bin, "db", "purge", "-older-than", "30d", "-yes", "-config", cfg) // the older name of --force
	expect(t, out, code, 0, "done: 1 rows deleted", "db optimize")
	out, code = gw(t, bin, "db", "optimize", "-config", cfg)
	expect(t, out, code, 1, "no interactive confirmation possible")
	out, code = gw(t, bin, "db", "optimize", "--dry-run", "-config", cfg)
	expect(t, out, code, 0, "hlstats_Events_Frags", "dry run: nothing changed")
	out, code = gw(t, bin, "db", "optimize", "--force", "-config", cfg)
	expect(t, out, code, 0, "optimize hlstats_Events_Frags", "tables optimized")
	out, code = gw(t, bin, "db", "grants", "-config", cfg)
	expect(t, out, code, 1, "no interactive confirmation possible")
	out, code = gw(t, bin, "db", "grants", "-n", "-config", cfg)
	expect(t, out, code, 0, "dry run: nothing changed")
	out, code = gw(t, bin, "db", "grants", "-f", "-config", cfg)
	expect(t, out, code, 0, "rights of the web users granted")
	out, code = gw(t, bin, "db", "seed", "-config", cfg)
	expect(t, out, code, 1, "no interactive confirmation possible")
	out, code = gw(t, bin, "db", "seed", "-y", "-config", cfg)
	expect(t, out, code, 0, "missing reference data added")

	out, code = gw(t, bin, "db", "reset", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, "dry run: nothing changed")
	oneFrag(t, out)
	out, code = gw(t, bin, "db", "reset", "-config", cfg)
	expect(t, out, code, 1, "Make a backup before resetting", "no interactive confirmation possible")
	sql(`UPDATE hlstats_Options SET value = ? WHERE keyname = 'gamewe_heartbeat'`, fmt.Sprint(time.Now().Unix()))
	sql(`INSERT INTO hlstats_Options (keyname, value) SELECT 'gamewe_heartbeat', ? WHERE NOT EXISTS (SELECT 1 FROM hlstats_Options WHERE keyname = 'gamewe_heartbeat')`, fmt.Sprint(time.Now().Unix()))
	stopBeat := beating(t, admin)
	_, kill := fakeRun(t, cfg) // a collector up to 0.7.1
	out, code = gw(t, bin, "db", "reset", "-yes", "-config", cfg)
	expect(t, out, code, 1, "a collector is running on this host:", "holds no collector lock", "then reset the database", rerunLine(bin, "db", "reset", "-yes", "-config", cfg))
	out, code = gw(t, bin, "db", "drop", "-yes", "-config", cfg)
	expect(t, out, code, 1, "a collector is running on this host:", "then drop the tables", rerunLine(bin, "db", "drop", "-yes", "-config", cfg))
	kill()
	stopBeat()
	stop := startRun(t, bin, cfg) // this version: the collector lock
	out, code = gw(t, bin, "db", "reset", "-yes", "-config", cfg)
	expect(t, out, code, 1, "a collector is running on this host:", bin+" run -config "+cfg)
	if strings.Contains(out, "holds no collector lock") {
		t.Error("the collector with the lock called an old one")
	}
	stop()
	sql(`UPDATE hlstats_Options SET value = '1' WHERE keyname = 'gamewe_heartbeat'`)
	out, code = gw(t, bin, "db", "reset", "-yes", "-config", cfg)
	expect(t, out, code, 0, "done: the database is as new")
	var n int
	if err := admin.QueryRow(`SELECT COUNT(*) FROM hlstats_Events_Frags`).Scan(&n); err != nil || n != 0 {
		t.Errorf("frags after reset: %d %v", n, err)
	}

	out, code = gw(t, bin, "db", "drop", "-print", "-config", cfg)
	expect(t, out, code, 0, "DROP TABLE IF EXISTS ", "hlstats_Events_Frags", database.MigrationsTable)
	drop := out
	out, code = gw(t, bin, "db", "drop", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, "drop ", "tables of the database "+name, "dry run: nothing changed")
	if typ == config.MySQL {
		out, code = gw(t, bin, "db", "drop", "-yes", "-config", cfg)
		expect(t, out, code, 1, "may not drop tables", "nothing was dropped", "db drop -config "+cfg+" -print | mariadb -u root -p "+name)
		if err := database.RunTestScript(ctx, admin, drop); err != nil {
			t.Fatal(err)
		}
	} else {
		out, code = gw(t, bin, "db", "drop", "-yes", "-config", cfg)
		expect(t, out, code, 0, "tables dropped", "db init")
	}
	out, code = gw(t, bin, "db", "drop", "-yes", "-config", cfg)
	expect(t, out, code, 0, "nothing to drop")
	out, code = gw(t, bin, "db", "reset", "-yes", "-config", cfg)
	expect(t, out, code, 1, "no gameWE tables yet", "db init")
	out, code = gw(t, bin, "db", "init", "--force", "-config", cfg)
	expect(t, out, code, 0, "done: the database has schema version")
	runStarts(t, bin, cfg)
}

// oneFrag checks the row count line of hlstats_Events_Frags.
func oneFrag(t *testing.T, out string) {
	t.Helper()
	if !regexp.MustCompile(`(?m)^\s*1\s+hlstats_Events_Frags$`).MatchString(out) {
		t.Errorf("no line with 1 row of hlstats_Events_Frags")
	}
}
