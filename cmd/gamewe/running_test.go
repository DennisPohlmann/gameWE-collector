// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/localrun"
)

// With GAMEWE_FAKE_RUN=1 the test binary only sleeps: started by fakeRun
// with the command line of gamewe run, it is a collector up to 0.7.1 (no
// collector lock) for the /proc scan.
func init() {
	if os.Getenv("GAMEWE_FAKE_RUN") == "1" {
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
}

// fakeRun starts a process with the command line DIR/gamewe run -config
// cfg that holds no collector lock; the function it returns stops it.
func fakeRun(t *testing.T, cfg string) (int, func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: self, Args: []string{filepath.Join(t.TempDir(), "gamewe"), "run", "-config", cfg}, Env: append(os.Environ(), "GAMEWE_FAKE_RUN=1")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, func() { cmd.Process.Kill(); cmd.Wait() }
}

// fakeHost replaces what refuseRunning looks at: a /proc with the given
// command lines and cgroups (PIDs 100, 101, ...), the state of the systemd
// unit, the binary, the arguments and whether it runs in a container.
func fakeHost(t *testing.T, active bool, mainPID int, container bool, args []string, procs ...[2]string) {
	t.Helper()
	dir := t.TempDir()
	for i, p := range procs {
		d := filepath.Join(dir, strconv.Itoa(100+i))
		os.Mkdir(d, 0o755)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(p[0]), 0o644)
		os.WriteFile(filepath.Join(d, "cgroup"), []byte(p[1]), 0o644)
	}
	oldProc, oldSys, oldExe, oldArgs, oldCont, oldSelf := procDir, systemdState, executable, commandArgs, inContainer, selfPID
	t.Cleanup(func() {
		procDir, systemdState, executable, commandArgs, inContainer, selfPID = oldProc, oldSys, oldExe, oldArgs, oldCont, oldSelf
	})
	procDir = dir
	systemdState = func(context.Context, string) (bool, int) { return active, mainPID }
	executable = func() (string, error) { return "/opt/gamewe/collector/bin/gamewe", nil }
	commandArgs = func() []string { return args }
	inContainer = func() bool { return container }
	selfPID = func() int { return 1 }
}

const runLine = "/opt/gamewe/collector/bin/gamewe\x00run\x00-config\x00/etc/gamewe/collector/config.yaml\x00"

// errLines returns the refusal as lines without the indentation.
func refusal(t *testing.T, locked bool) (string, []string) {
	t.Helper()
	local := localCollectors(context.Background())
	if len(local) == 0 && !locked {
		t.Fatal("nothing running")
	}
	msg := (&runningError{local: local, locked: locked, what: "migrate", rerun: rerunCommand()}).Error()
	t.Logf("\n%s", msg)
	var cmds []string
	for _, l := range strings.Split(msg, "\n") {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "  PID ") {
			cmds = append(cmds, strings.TrimSpace(l))
		}
	}
	return msg, cmds
}

func wantCmds(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRefusalSystemdUnit(t *testing.T) {
	fakeHost(t, true, 100, false, []string{"db", "migrate", "--force"},
		[2]string{runLine, "0::/system.slice/gamewe-collector.service\n"})
	msg, cmds := refusal(t, true)
	wantCmds(t, cmds,
		"systemctl stop gamewe-collector",
		"/opt/gamewe/collector/bin/gamewe db migrate --force",
		"systemctl start gamewe-collector")
	for _, w := range []string{"PID 100: /opt/gamewe/collector/bin/gamewe run -config /etc/gamewe/collector/config.yaml (systemd unit gamewe-collector.service)", "then migrate"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message lacks %q", w)
		}
	}
	if strings.Contains(msg, "0.7.1") {
		t.Error("a collector with the lock called an old one")
	}
}

// A collector up to 0.7.1 holds no lock: the process tells it.
func TestRefusalOldCollectorOtherUnit(t *testing.T) {
	fakeHost(t, false, 0, false, []string{"db", "migrate", "-config", "/srv/my config.yaml"},
		[2]string{"/usr/local/bin/gamewe\x00run\x00", "0::/system.slice/gw-cs2.service\n"})
	msg, cmds := refusal(t, false)
	wantCmds(t, cmds,
		"systemctl stop gw-cs2",
		"/opt/gamewe/collector/bin/gamewe db migrate -config '/srv/my config.yaml'",
		"systemctl start gw-cs2")
	if !strings.Contains(msg, "holds no collector lock") || !strings.Contains(msg, "0.7.1") {
		t.Error("no note about the missing lock")
	}
}

func TestRefusalPlainProcessAndContainer(t *testing.T) {
	fakeHost(t, false, 0, false, []string{"db", "reset", "-yes"},
		[2]string{"./gamewe\x00run\x00-config\x00c.yaml\x00", "0::/user.slice/user-1000.slice/session-2.scope\n"},
		[2]string{"/opt/gamewe/collector/bin/gamewe\x00run\x00", "0::/system.slice/docker-89abcdef0123456789abcdef.scope\n"},
		[2]string{"/usr/bin/vim\x00gamewe\x00run\x00", ""})
	_, cmds := refusal(t, true)
	wantCmds(t, cmds,
		"kill 100",
		"docker stop 89abcdef0123",
		"/opt/gamewe/collector/bin/gamewe db reset -yes",
		"./gamewe run -config c.yaml &    # as it was started before",
		"docker start 89abcdef0123")
}

// /proc hides the process (hidepid), systemd names the unit.
func TestRefusalSystemdOnly(t *testing.T) {
	fakeHost(t, true, 4242, false, []string{"db", "drop"})
	msg, cmds := refusal(t, false)
	wantCmds(t, cmds,
		"systemctl stop gamewe-collector",
		"/opt/gamewe/collector/bin/gamewe db drop",
		"systemctl start gamewe-collector")
	if !strings.Contains(msg, "PID 4242 (systemd unit gamewe-collector.service)") {
		t.Error("PID or unit missing")
	}
}

// Only the lock: the collector runs on another host or in a container.
func TestRefusalLockOnly(t *testing.T) {
	fakeHost(t, false, 0, false, []string{"db", "migrate", "-f"},
		[2]string{"/opt/gamewe/collector/bin/gamewe\x00db\x00migrate\x00", ""})
	msg, cmds := refusal(t, true)
	if len(cmds) != 3 || !strings.HasPrefix(cmds[0], "docker compose stop collector") || cmds[1] != "/opt/gamewe/collector/bin/gamewe db migrate -f" || !strings.HasPrefix(cmds[2], "docker compose start collector") {
		t.Errorf("commands %q", cmds)
	}
	for _, w := range []string{"holds the collector lock", "another host or in a container", "docker stop CONTAINER", "systemctl stop gamewe-collector"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message lacks %q", w)
		}
	}
}

func TestRerunInContainer(t *testing.T) {
	fakeHost(t, false, 0, true, []string{"db", "migrate", "-f"})
	if got, want := rerunCommand(), "docker compose run --rm collector db migrate -f"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// gamewe db migrate in the collector container (docker compose exec).
func TestRefusalInContainer(t *testing.T) {
	fakeHost(t, false, 0, true, []string{"db", "migrate"}, [2]string{runLine, "0::/\n"})
	_, cmds := refusal(t, true)
	wantCmds(t, cmds,
		"docker compose stop collector    # on the host, in the directory of its docker-compose.yml",
		"docker compose run --rm collector db migrate",
		"docker compose start collector")
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"-config":       "-config",
		"/etc/x.yaml":   "/etc/x.yaml",
		"a b":           "'a b'",
		"it's":          `'it'\''s'`,
		"":              "''",
		"$(rm -rf /)":   "'$(rm -rf /)'",
		"--output=json": "--output=json",
		"x;y":           "'x;y'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalCollectorsSystemdSamePID(t *testing.T) {
	fakeHost(t, true, 100, false, nil, [2]string{runLine, ""}) // cgroup unreadable
	got := localCollectors(context.Background())
	if len(got) != 1 || got[0].Unit != localrun.Unit {
		t.Errorf("got %+v", got)
	}
}
