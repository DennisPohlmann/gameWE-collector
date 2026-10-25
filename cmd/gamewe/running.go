// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/localrun"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// What refuseRunning looks at; tests replace them.
var (
	procDir      = "/proc"
	systemdState = localrun.Systemd
	executable   = os.Executable
	commandArgs  = func() []string { return os.Args[1:] }
	inContainer  = func() bool { return exists("/.dockerenv") || exists("/run/.containerenv") }
	selfPID      = os.Getpid
)

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// refuseRunning returns an error while a collector runs: one that holds
// the collector lock of the database (gamewe run since 0.8, also on
// another host or in a container), or a `gamewe run` on this host (any
// version, also up to 0.7.1, which holds no lock) or the active systemd
// unit gamewe-collector. The error lists the commands to stop it, do what
// (the command as given) and start it again. A fresh heartbeat without
// either is only noted: a collector stopped a moment ago, or one up to
// 0.7.1 on another host.
func refuseRunning(ctx context.Context, db *database.DB, out io.Writer, what string) error {
	locked, err := database.CollectorRunning(ctx, db)
	if err != nil {
		return fmt.Errorf("is a collector running? %w", database.Explain(err))
	}
	if local := localCollectors(ctx); len(local) > 0 {
		return &runningError{local: local, locked: locked, what: what, rerun: rerunCommand()}
	}
	if locked {
		return &runningError{locked: true, what: what, rerun: rerunCommand()}
	}
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	if beat, err := store.LastHeartbeat(ctx); err == nil && !beat.IsZero() && time.Since(beat) < stats.HeartbeatFresh {
		fmt.Fprintf(out, "note: the collector heartbeat is %s old, but no collector holds the collector lock and no gamewe run runs here: a collector stopped a moment ago (going on), or one up to 0.7.1 runs on another host (stop it there first)\n",
			max(time.Since(beat), time.Second).Round(time.Second))
	}
	return nil
}

// localCollectors returns the `gamewe run` processes on this host and the
// active systemd unit gamewe-collector (also when /proc hides its process).
func localCollectors(ctx context.Context) []localrun.Process {
	found, _ := localrun.Find(procDir, selfPID()) // no /proc: the lock and systemd still tell
	active, pid := systemdState(ctx, localrun.Unit)
	if !active {
		return found
	}
	i := slices.IndexFunc(found, func(p localrun.Process) bool { return pid > 0 && p.PID == pid })
	if i < 0 {
		return append(found, localrun.Process{PID: pid, Unit: localrun.Unit})
	}
	if found[i].Unit == "" && found[i].Container == "" {
		found[i].Unit, found[i].UserUnit = localrun.Unit, false
	}
	return found
}

// rerunCommand is this command again, as given, with the path of this
// binary; in a container the docker compose run of it.
func rerunCommand() string {
	args := commandArgs()
	if inContainer() {
		return "docker compose run --rm collector " + shellJoin(args)
	}
	bin, err := executable()
	if err != nil {
		bin = "gamewe"
	}
	return shellJoin(append([]string{bin}, args...))
}

// shellJoin quotes args for a POSIX shell where needed.
func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// shellQuote quotes s for a POSIX shell where needed; control characters
// (from the command line of another process) become "?".
func shellQuote(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runningError is the refusal of refuseRunning with the commands to run.
type runningError struct {
	local  []localrun.Process
	locked bool   // a collector holds the collector lock
	what   string // e.g. "migrate"
	rerun  string // the command line to run again
}

func (e *runningError) Error() string {
	var b strings.Builder
	var stops, starts []string
	if len(e.local) == 0 {
		b.WriteString("a collector is running on this database (it holds the collector lock), but not here (no gamewe run on this host or in this container): it runs on another host or in a container.\n")
		stops = []string{"docker compose stop collector    # in the directory of its docker-compose.yml; or: docker stop CONTAINER; on its host: systemctl stop gamewe-collector"}
		starts = []string{"docker compose start collector   # or: docker start CONTAINER; on its host: systemctl start gamewe-collector"}
	} else {
		b.WriteString("a collector is running on this host:")
		for _, p := range e.local {
			fmt.Fprintf(&b, "\n  %s", describe(p))
			stop, start := stopStart(p)
			stops = append(stops, stop)
			starts = append(starts, start)
		}
		b.WriteString("\n")
		if !e.locked {
			b.WriteString("It holds no collector lock of this database: a collector up to 0.7.1, or one of another database; stop it all the same.\n")
		}
	}
	fmt.Fprintf(&b, "Stop it first, then %s, then start it again:\n\n", e.what)
	for _, l := range stops {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("  " + e.rerun + "\n")
	for _, l := range starts {
		b.WriteString("  " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// describe names p: PID, command line, unit or container.
func describe(p localrun.Process) string {
	s := fmt.Sprintf("PID %d", p.PID)
	if p.Args != nil {
		s += ": " + shellJoin(p.Args)
	}
	switch {
	case p.Container != "":
		s += fmt.Sprintf(" (%s container %s)", p.Engine, p.Container)
	case p.UserUnit:
		s += fmt.Sprintf(" (systemd user unit %s)", p.Unit)
	case p.Unit != "":
		s += fmt.Sprintf(" (systemd unit %s)", p.Unit)
	}
	return s
}

// stopStart returns the commands that stop p and start it again.
func stopStart(p localrun.Process) (string, string) {
	unit := strings.TrimSuffix(p.Unit, ".service")
	switch {
	case p.Container != "":
		return p.Engine + " stop " + p.Container, p.Engine + " start " + p.Container
	case p.UserUnit:
		return "systemctl --user stop " + shellQuote(unit), "systemctl --user start " + shellQuote(unit)
	case p.Unit != "":
		return "systemctl stop " + shellQuote(unit), "systemctl start " + shellQuote(unit)
	case inContainer(): // docker compose exec: the collector of this container
		return "docker compose stop collector    # on the host, in the directory of its docker-compose.yml",
			"docker compose start collector"
	case p.Args != nil:
		return fmt.Sprintf("kill %d", p.PID), shellJoin(p.Args) + " &    # as it was started before"
	}
	return fmt.Sprintf("kill %d", p.PID), "# start it again as before"
}
