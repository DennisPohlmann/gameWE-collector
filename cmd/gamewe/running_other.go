// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package main

import (
	"context"
	"fmt"
	"slices"

	"gamewe-collector/internal/localrun"
)

// What localCollectors looks at; tests replace them.
var (
	procDir      = "/proc"
	systemdState = localrun.Systemd
)

const unitKind = "systemd unit"

func unitCommands(unit string) (stop, start string) {
	return "systemctl stop " + shellQuote(unit), "systemctl start " + shellQuote(unit)
}

func killCommand(pid int) string { return fmt.Sprintf("kill %d", pid) }

// localCollectors returns the `gamewe run` processes on this host and the
// active systemd unit gamewe-collector (also when /proc hides its process).
// The systemd unit of a process (from its cgroup) is kept only when it is
// active with this process as its main PID: a collector started by hand
// from another service (a CI runner, a script) is in that service's cgroup,
// and stopping that service is not the way to stop it. User units cannot
// be checked from here: such a process is stopped by its PID.
func localCollectors(ctx context.Context) []localrun.Process {
	found, _ := localrun.Find(procDir, selfPID()) // no /proc: the lock and systemd still tell
	for i, p := range found {
		if p.Unit == "" {
			continue
		}
		if active, pid := systemdState(ctx, p.Unit); p.UserUnit || !active || pid != p.PID {
			found[i].Unit, found[i].UserUnit = "", false
		}
	}
	active, pid := systemdState(ctx, localrun.Unit)
	if !active {
		return found
	}
	i := slices.IndexFunc(found, func(p localrun.Process) bool { return pid > 0 && p.PID == pid })
	if i < 0 {
		return append(found, localrun.Process{PID: pid, Unit: localrun.Unit})
	}
	if found[i].Container == "" {
		found[i].Unit = localrun.Unit
	}
	return found
}
