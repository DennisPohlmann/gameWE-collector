// SPDX-License-Identifier: GPL-3.0-or-later

// Package localrun finds collectors (`gamewe run`) that run on this host:
// in /proc, whatever the path of the binary, and the packaged systemd unit.
// It tells a collector that holds no collector lock (up to 0.7.1) and how
// to stop one (systemd unit, container or PID).
package localrun

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Unit is the systemd unit of the packaged collector.
const Unit = "gamewe-collector.service"

// Process is a `gamewe run` on this host.
type Process struct {
	PID       int
	Args      []string // command line; nil when only systemd named it
	Unit      string   // systemd unit, e.g. gamewe-collector.service; "" none
	UserUnit  bool     // Unit is a unit of systemctl --user
	Engine    string   // docker or podman: it runs in Container
	Container string   // container id, 12 characters
}

// IsRun reports whether the command line args is `gamewe run`: a binary
// named gamewe (any path), the global flags -o / --output skipped.
func IsRun(args []string) bool {
	if len(args) < 2 || filepath.Base(args[0]) != "gamewe" {
		return false
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a == "run"
		}
		name, _, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name != "o" && name != "output" {
			return false
		}
		if !hasVal {
			i++ // the value
		}
	}
	return false
}

// maxPIDs bounds the scan of procDir.
const maxPIDs = 1 << 20

// Find returns the `gamewe run` processes in procDir (normally /proc)
// except self. Processes it cannot read (ended meanwhile, hidepid) are
// left out; an error means procDir cannot be read at all.
func Find(procDir string, self int) ([]Process, error) {
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return nil, err
	}
	var found []Process
	for i, e := range ents {
		if i >= maxPIDs {
			break
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		args := readArgs(filepath.Join(procDir, e.Name(), "cmdline"))
		if !IsRun(args) {
			continue
		}
		p := Process{PID: pid, Args: args}
		if cg, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cgroup")); err == nil {
			p.Unit, p.UserUnit, p.Engine, p.Container = ParseCgroup(string(cg))
		}
		found = append(found, p)
	}
	return found, nil
}

// readArgs reads a NUL-separated /proc/PID/cmdline (at most 64 KiB).
func readArgs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	n, _ := f.Read(buf)
	s := strings.TrimRight(string(buf[:n]), "\x00")
	if s == "" {
		return nil // kernel thread or zombie
	}
	return strings.Split(s, "\x00")
}

// ParseCgroup reads /proc/PID/cgroup: the systemd unit of the process, or
// the container it runs in (docker, podman). The unified hierarchy (0::)
// or the systemd one (name=systemd) is used.
func ParseCgroup(data string) (unit string, user bool, engine, container string) {
	var path string
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" || parts[1] == "name=systemd" {
			path = parts[2]
			if parts[1] == "name=systemd" {
				break
			}
		}
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, "docker-") && strings.HasSuffix(s, ".scope"):
			engine, container = "docker", strings.TrimSuffix(strings.TrimPrefix(s, "docker-"), ".scope")
		case strings.HasPrefix(s, "libpod-") && strings.HasSuffix(s, ".scope"):
			engine, container = "podman", strings.TrimSuffix(strings.TrimPrefix(s, "libpod-"), ".scope")
		case s == "docker" && i+1 < len(segs): // cgroupfs driver: /docker/ID
			engine, container = "docker", segs[i+1]
		}
	}
	if container != "" {
		if !isHex(container) {
			return "", false, "", ""
		}
		return "", false, engine, container[:min(12, len(container))]
	}
	for _, s := range segs {
		if strings.HasPrefix(s, "user@") && strings.HasSuffix(s, ".service") {
			user, unit = true, ""
			continue
		}
		if strings.HasSuffix(s, ".service") {
			unit = s
		}
	}
	if unit == "" {
		user = false
	}
	return unit, user, "", ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return s != ""
}

// Systemd reports whether the systemd unit is active (running, reloading
// or stopping) and its main PID; false without systemctl.
func Systemd(ctx context.Context, unit string) (active bool, pid int) {
	bin, err := exec.LookPath("systemctl")
	if err != nil {
		return false, 0
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "show", "-p", "ActiveState", "-p", "MainPID", "--", unit).Output()
	if err != nil {
		return false, 0
	}
	return ParseShow(string(out))
}

// ParseShow reads the output of systemctl show -p ActiveState -p MainPID.
// Activating (a unit in its restart loop, e.g. on an old schema) does not
// count: the migration is what it waits for.
func ParseShow(out string) (active bool, pid int) {
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "ActiveState":
			active = v == "active" || v == "reloading" || v == "deactivating"
		case "MainPID":
			pid, _ = strconv.Atoi(v)
		}
	}
	return active, pid
}
