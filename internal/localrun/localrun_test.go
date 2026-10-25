// SPDX-License-Identifier: GPL-3.0-or-later

package localrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// With LOCALRUN_FAKE_RUN=1 the test binary only sleeps: started with the
// command line of gamewe run, it is a collector for Find.
func TestMain(m *testing.M) {
	if os.Getenv("LOCALRUN_FAKE_RUN") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestIsRun(t *testing.T) {
	for _, c := range []struct {
		args string
		want bool
	}{
		{"/opt/gamewe/collector/bin/gamewe run -config /etc/gamewe/collector/config.yaml", true},
		{"gamewe run", true},
		{"./gamewe -o json run", true},
		{"/usr/local/bin/gamewe --output=yaml run", true},
		{"/tmp/x/gamewe run -config c.yaml", true},
		{"gamewe db migrate -f", false},
		{"gamewe check", false},
		{"gamewe -o", false},
		{"gamewe -config x run", false}, // -config belongs after the command
		{"/usr/bin/gamewe-web run", false},
		{"bash -c gamewe run", false},
		{"gamewe", false},
		{"vim gamewe run", false},
	} {
		if got := IsRun(strings.Fields(c.args)); got != c.want {
			t.Errorf("IsRun(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestParseCgroup(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		name, data        string
		unit              string
		user              bool
		engine, container string
	}{
		{"systemd unit", "0::/system.slice/gamewe-collector.service\n", "gamewe-collector.service", false, "", ""},
		{"other unit", "0::/system.slice/gw-test.service\n", "gw-test.service", false, "", ""},
		{"user unit", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/gamewe.service\n", "gamewe.service", true, "", ""},
		{"shell", "0::/user.slice/user-1000.slice/session-3.scope\n", "", false, "", ""},
		{"user scope", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-x.scope\n", "", false, "", ""},
		{"docker systemd driver", "0::/system.slice/docker-" + id + ".scope\n", "", false, "docker", id[:12]},
		{"docker cgroupfs v1", "12:cpu:/docker/" + id + "\n1:name=systemd:/docker/" + id + "\n", "", false, "docker", id[:12]},
		{"rootless podman", "0::/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + id + ".scope/container\n", "", false, "podman", id[:12]},
		{"hybrid v1", "1:name=systemd:/system.slice/gamewe-collector.service\n0::/\n", "gamewe-collector.service", false, "", ""},
		{"not an id", "0::/docker/../../evil\n", "", false, "", ""},
		{"empty", "", "", false, "", ""},
	} {
		unit, user, engine, container := ParseCgroup(c.data)
		if unit != c.unit || user != c.user || engine != c.engine || container != c.container {
			t.Errorf("%s: got %q %v %q %q, want %q %v %q %q", c.name, unit, user, engine, container, c.unit, c.user, c.engine, c.container)
		}
	}
}

func TestParseShow(t *testing.T) {
	for _, c := range []struct {
		out    string
		active bool
		pid    int
	}{
		{"MainPID=4242\nActiveState=active\n", true, 4242},
		{"ActiveState=reloading\nMainPID=7\n", true, 7},
		{"MainPID=0\nActiveState=inactive\n", false, 0},
		{"MainPID=0\nActiveState=activating\n", false, 0}, // restart loop, e.g. old schema
		{"MainPID=0\nActiveState=failed\n", false, 0},
		{"", false, 0},
	} {
		active, pid := ParseShow(c.out)
		if active != c.active || pid != c.pid {
			t.Errorf("ParseShow(%q) = %v %d, want %v %d", c.out, active, pid, c.active, c.pid)
		}
	}
}

// fakeProc writes a /proc with processes of the given command lines (and
// cgroups) under PIDs 100, 101, ...
func fakeProc(t *testing.T, procs ...[2]string) string {
	t.Helper()
	dir := t.TempDir()
	for i, p := range procs {
		d := filepath.Join(dir, strconv.Itoa(100+i))
		os.Mkdir(d, 0o755)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(p[0]), 0o644)
		if p[1] != "" {
			os.WriteFile(filepath.Join(d, "cgroup"), []byte(p[1]), 0o644)
		}
	}
	os.Mkdir(filepath.Join(dir, "self"), 0o755)
	os.WriteFile(filepath.Join(dir, "uptime"), []byte("1 1\n"), 0o644)
	return dir
}

func TestFindFakeProc(t *testing.T) {
	dir := fakeProc(t,
		[2]string{"/opt/gamewe/collector/bin/gamewe\x00run\x00-config\x00/etc/gamewe/collector/config.yaml\x00", "0::/system.slice/gamewe-collector.service\n"},
		[2]string{"/opt/gamewe/collector/bin/gamewe\x00db\x00migrate\x00-f\x00", "0::/user.slice/user-0.slice/session-1.scope\n"},
		[2]string{"", ""},                          // kernel thread
		[2]string{"/usr/local/src/gamewe\x00run\x00", ""}, // no cgroup readable
		[2]string{"/usr/local/src/gamewe\x00run\x00", ""}, // PID 104 = self
		[2]string{"/opt/gamewe/collector/bin/gamewe\x00run\x00", "0::/system.slice/docker-0123456789abcdef0123.scope\n"},
	)
	got, err := Find(dir, 104)
	if err != nil {
		t.Fatal(err)
	}
	want := []Process{
		{PID: 100, Args: []string{"/opt/gamewe/collector/bin/gamewe", "run", "-config", "/etc/gamewe/collector/config.yaml"}, Unit: "gamewe-collector.service"},
		{PID: 103, Args: []string{"/usr/local/src/gamewe", "run"}},
		{PID: 105, Args: []string{"/opt/gamewe/collector/bin/gamewe", "run"}, Engine: "docker", Container: "0123456789ab"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.PID != w.PID || !slices.Equal(g.Args, w.Args) || g.Unit != w.Unit || g.UserUnit != w.UserUnit || g.Engine != w.Engine || g.Container != w.Container {
			t.Errorf("process %d: got %+v, want %+v", i, g, w)
		}
	}
	if _, err := Find(filepath.Join(dir, "missing"), 0); err == nil {
		t.Error("no error for a missing proc dir")
	}
}

// A process started as /some/dir/gamewe -o json run is found in the real
// /proc, whatever the binary really is; this process itself is not.
func TestFindRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv0 := filepath.Join(t.TempDir(), "gamewe")
	cmd := &exec.Cmd{Path: self, Args: []string{argv0, "-o", "json", "run", "-config", "c.yaml"}, Env: append(os.Environ(), "LOCALRUN_FAKE_RUN=1")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := Find("/proc", os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(got, func(p Process) bool { return p.PID == cmd.Process.Pid })
		if i >= 0 {
			if !slices.Equal(got[i].Args, cmd.Args) {
				t.Errorf("args %q, want %q", got[i].Args, cmd.Args)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PID %d not found: %+v", cmd.Process.Pid, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := Find("/proc", cmd.Process.Pid) // excluded as self
	if slices.ContainsFunc(got, func(p Process) bool { return p.PID == cmd.Process.Pid }) {
		t.Error("self not excluded")
	}
}
