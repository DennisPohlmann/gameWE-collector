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
`, port, d.Type, d.Host, d.Port, d.User, pwFile, d.Name, filepath.Join(t.TempDir(), "none.mmdb"))
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

// runStarts starts "gamewe run", waits until it listens and stops it.
func runStarts(t *testing.T, bin, cfg string) {
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
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("gamewe run after SIGTERM: %v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "database schema up to date") {
		t.Errorf("no schema line:\n%s", log.String())
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
	cfg := writeConfig(t, d)

	out, code := gw(t, bin, "run", "-config", cfg)
	expect(t, out, code, 1, "does not match the layout this collector needs", "Make a backup, then run: ", "gamewe db migrate -config "+cfg)
	out, code = gw(t, bin, "check", "-config", cfg)
	expect(t, out, code, 1, "migration steps pending, the collector does not start", "db migrate -config "+cfg)
	out, code = gw(t, bin, "db")
	expect(t, out, code, 0, "gamewe db migrate", "gamewe db status")
	out, code = gw(t, bin, "db", "migrate", "-f", "-n", "-config", cfg)
	expect(t, out, code, 2, "cannot be used together")
	out, code = gw(t, bin, "db", "migrate", "--dry-run", "-config", cfg)
	expect(t, out, code, 2, "plan (", "Dry run: nothing changed")
	out, code = gw(t, bin, "db", "migrate", "-config", cfg)
	expect(t, out, code, 1, "plan (", "Make a backup before migrating", "no interactive confirmation possible", "-force")
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
		err := migrate(ctx, db, d, migrateFlags{}, strings.NewReader(answer), &out, true)
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 1 || !strings.Contains(out.String(), "Cancelled: nothing changed") {
			t.Errorf("answer %q: %v\n%s", answer, err, out.String())
		}
	}
	if p, _ := database.PlanMigrations(ctx, db); p == nil || p.Versions {
		t.Fatal("a cancelled migration changed the database")
	}
	var out bytes.Buffer
	if err := migrate(ctx, db, d, migrateFlags{}, strings.NewReader("ja\n"), &out, true); err != nil {
		t.Fatalf("ja: %v\n%s", err, out.String())
	}
	if p, _ := database.PlanMigrations(ctx, db); p == nil || !p.UpToDate() {
		t.Errorf("after ja: not up to date\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Backup made? Migrate now? [y/N]") {
		t.Errorf("no question:\n%s", out.String())
	}
}
