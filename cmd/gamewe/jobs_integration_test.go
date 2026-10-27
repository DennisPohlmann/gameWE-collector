// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// a2sReply is an A2S_INFO 'I' answer of a Linux, VAC secured server.
func a2sReply(version string) []byte {
	b := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'I', 17}
	for _, s := range []string{"Example Server", "de_dust2", "cstrike", "Counter-Strike: Source"} {
		b = append(append(b, s...), 0)
	}
	b = append(b, 240, 0, 3, 24, 1, 'd', 'l', 0, 1)
	return append(append(b, version...), 0)
}

// The jobs of the collector from the command line, also with the features
// off (the config of writeConfig has daily_awards and
// inactive_players off): gamewe inactive (here, under the job lock, and queued
// for a running collector), gamewe awards under the job lock, gamewe
// serverinfo, and gamewe db purge deleting old finished jobs. Named
// Migrate* so that CI runs it with the binary tests.
func TestMigrateJobCommands(t *testing.T) {
	typ := database.MigrateTestEnv()
	if typ == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* to run (creates and drops databases mig_*)")
	}
	ctx := context.Background()
	name := "mig_jobs"
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
	num := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(admin.Q(q), args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	bin := buildGamewe(t)
	d := database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	cfg := writeConfig(t, d)
	out, code := gw(t, bin, "db", "init", "-f", "-config", cfg)
	expect(t, out, code, 0)
	db, err := database.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	old := time.Now().Add(-60 * 24 * time.Hour).Unix()
	players := func(n int, lastEvent int64) {
		t.Helper()
		for range n {
			sql(`INSERT INTO hlstats_Players (game, game_id, last_event) VALUES ('css', 1, ?)`, lastEvent)
		}
	}
	players(2, old)
	players(1, time.Now().Unix())

	// -dry-run counts; MinActivity of the seed is 28.
	out, code = gw(t, bin, "inactive", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, "2 players without an event for 28 days", "dry run: nothing changed")
	if n := num(`SELECT COUNT(*) FROM hlstats_PlayersInactive`); n != 0 {
		t.Fatalf("dry run moved %d", n)
	}

	// Another job holds the lock: inactive and awards wait, then give up.
	release, err := database.JobLock(ctx, db, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, code = gw(t, bin, "inactive", "-f", "-wait", "2s", "-config", cfg)
	expect(t, out, code, 1, "waiting for another job", "another job")
	out, code = gw(t, bin, "awards", "-f", "-wait", "2s", "-config", cfg)
	expect(t, out, code, 1, "another job")
	if _, err := database.JobLock(ctx, db, time.Second, nil); err == nil {
		t.Error("a second job lock was granted")
	}
	release()

	// Without a terminal and without --force nothing happens.
	out, code = gw(t, bin, "inactive", "-config", cfg)
	expect(t, out, code, 1, "2 players without an event", "no interactive confirmation possible", "--force")
	out, code = gw(t, bin, "awards", "-config", cfg)
	expect(t, out, code, 1, "decide the daily awards of", "--force")
	out, code = gw(t, bin, "awards", "-n", "-config", cfg)
	expect(t, out, code, 0, "daily awards of", "dry run: nothing changed")
	out, code = gw(t, bin, "inactive", "-f", "-n", "-config", cfg)
	expect(t, out, code, 2, "cannot be used together")
	// No collector: moved here.
	out, code = gw(t, bin, "inactive", "--force", "-config", cfg)
	expect(t, out, code, 0, "no running collector", "done: 2 players moved to hlstats_PlayersInactive")
	if p, i := num(`SELECT COUNT(*) FROM hlstats_Players`), num(`SELECT COUNT(*) FROM hlstats_PlayersInactive`); p != 1 || i != 2 {
		t.Errorf("hlstats_Players %d, hlstats_PlayersInactive %d; want 1, 2", p, i)
	}
	out, code = gw(t, bin, "awards", "-yes", "-config", cfg) // features.daily_awards is off; -yes: older name of --force
	expect(t, out, code, 0, "daily awards of")

	// A running collector (heartbeat, jobs loop): queued and done there.
	players(3, old)
	stop := beating(t, admin)
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	jobs := &collector.Jobs{Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "9.8.7",
		Lock: func(ctx context.Context) (func(), error) { return database.JobLock(ctx, db, time.Minute, nil) }}
	jctx, cancel := context.WithCancel(ctx)
	loop := make(chan struct{})
	go func() {
		defer close(loop)
		for jctx.Err() == nil {
			jobs.Poll(jctx)
			select {
			case <-jctx.Done():
			case <-time.After(300 * time.Millisecond):
			}
		}
	}()
	out, code = gw(t, bin, "inactive", "-f", "-wait", "30s", "-config", cfg)
	cancel()
	<-loop
	stop()
	expect(t, out, code, 0, "queued as job", "done: 3 players moved")
	if n := num(`SELECT COUNT(*) FROM hlstats_gamewe_jobs WHERE job_type = 'inactive' AND status = 'done'`); n != 1 {
		t.Errorf("%d done inactive jobs, want 1", n)
	}
	if n := num(`SELECT COUNT(*) FROM hlstats_Options WHERE keyname = 'gamewe_collector_version' AND value = '9.8.7'`); n != 1 {
		t.Errorf("gamewe_collector_version not written (%d rows)", n)
	}
	out, code = gw(t, bin, "reset", "-list", "-config", cfg)
	expect(t, out, code, 0, "inactive")

	// db purge also deletes the finished jobs older than 30 days.
	sql(`UPDATE hlstats_gamewe_jobs SET requested_at = ?`, time.Now().Add(-31*24*time.Hour).Unix())
	out, code = gw(t, bin, "db", "purge", "-older-than", "7", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, tablePrefix(d).Table("hlstats_gamewe_jobs"), "dry run: nothing changed")
	out, code = gw(t, bin, "db", "purge", "-older-than", "7", "-yes", "-config", cfg)
	expect(t, out, code, 0, "done: 1 rows deleted")
	if n := num(`SELECT COUNT(*) FROM hlstats_gamewe_jobs`); n != 0 {
		t.Errorf("%d jobs left", n)
	}

	// The admin commands show the change and ask; --dry-run and --force.
	out, code = gw(t, bin, "option", "set", "gamewe_kill_points", "7", "-config", cfg)
	expect(t, out, code, 1, "dry run:", "gamewe_kill_points", "--force")
	out, code = gw(t, bin, "option", "set", "gamewe_kill_points", "7", "-n", "-config", cfg)
	expect(t, out, code, 0, "dry run:")
	if n := num(`SELECT COUNT(*) FROM hlstats_Options WHERE keyname = 'gamewe_kill_points' AND value = '7'`); n != 0 {
		t.Fatal("option set changed it without --force")
	}
	out, code = gw(t, bin, "option", "set", "gamewe_kill_points", "7", "-f", "-config", cfg)
	expect(t, out, code, 0)
	if n := num(`SELECT COUNT(*) FROM hlstats_Options WHERE keyname = 'gamewe_kill_points' AND value = '7'`); n != 1 {
		t.Error("option set --force did not set it")
	}
	out, code = gw(t, bin, "server", "add", "198.51.100.90:27015", "-game", "css", "-rcon-password-file", "-", "-config", cfg)
	expect(t, out, code, 2, "cannot ask", "--force")
	out, code = gw(t, bin, "option", "list", "-config", cfg) // reading never asks
	expect(t, out, code, 0, "gamewe_kill_points")
	out, code = gw(t, bin, "simulate", "-n", "-config", cfg)
	expect(t, out, code, 0, "replay ", "dry run: nothing changed")
	out, code = gw(t, bin, "simulate", "-config", cfg)
	expect(t, out, code, 1, "--force")
	out, code = gw(t, bin, "geoip-update", "-n", "-file", t.TempDir()+"/x.mmdb")
	expect(t, out, code, 0, "download DB-IP City Lite", "dry run: nothing changed")
	out, code = gw(t, bin, "geoip-update", "-file", t.TempDir()+"/x.mmdb")
	expect(t, out, code, 1, "--force")

	// gamewe serverinfo with features.server_info off (the config's
	// default is on; the command does not look at it).
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(a2sReply("9540945"), addr)
		}
	}()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	sql(`INSERT INTO hlstats_Servers (address, port, name, game) VALUES ('127.0.0.1', ?, 'test', 'css')`, port)
	out, code = gw(t, bin, "serverinfo", "-dry-run", "-config", cfg)
	expect(t, out, code, 0, `version "9540945"`, "dry run: nothing changed (1 servers would be updated")
	out, code = gw(t, bin, "serverinfo", "-config", cfg)
	expect(t, out, code, 1, `version "9540945"`, "--force")
	out, code = gw(t, bin, "serverinfo", "-f", "-config", cfg)
	expect(t, out, code, 0, "done: 1 servers updated")
	var version, os, vac string
	if err := admin.QueryRow(`SELECT server_version, server_os, server_vac FROM hlstats_Servers WHERE port = `+strconv.Itoa(port)).Scan(&version, &os, &vac); err != nil {
		t.Fatal(err)
	}
	if version != "9540945" || os != "linux" || vac != "secure" {
		t.Errorf("stored %q %q %q", version, os, vac)
	}
	out, code = gw(t, bin, "serverinfo", "-o", "csv", "-config", cfg)
	expect(t, out, code, 0, "9540945,linux,secure,false")
	if !strings.Contains(out, "id,address,version") {
		t.Error("csv header missing")
	}
}
