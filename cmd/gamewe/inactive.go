// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

const inactiveUsage = `Usage:
  gamewe inactive [-dry-run] [-wait DURATION] [-config FILE]

Moves the players who have no event for MinActivity days (hlstats_Options,
the web admin's "days until 0 % activity", default 28) and are not online
from hlstats_Players to hlstats_PlayersInactive, as the daily job
features.inactive_players does, also when that is off. A returning player is
moved back unchanged. -dry-run only counts them.

With a running collector (heartbeat less than 2 minutes old) the move is
queued for it (hlstats_gamewe_jobs, see gamewe reset -list); otherwise it
runs here. Either way it waits for a ranking reset or the daily awards in
progress (up to -wait, default 10m).
-o json|yaml writes the result as a document.`

// inactiveCmd runs `gamewe inactive`.
func inactiveCmd(args []string) error {
	return inactiveWith(args, os.Stdout)
}

func inactiveWith(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("inactive", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, inactiveUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	dry := fs.Bool("dry-run", false, "only count the players")
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for the running collector, or for another job to finish")
	addOutputFlag(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return exitError{2, fmt.Sprintf("unexpected argument %q\n\n%s", pos[0], inactiveUsage)}
	}
	return captured(out, func(out io.Writer, _ *result) error {
		return inactiveRun(*path, *dry, *wait, out)
	})
}

func inactiveRun(path string, dry bool, wait time.Duration, out io.Writer) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	cutoff, days, warn, err := store.InactiveCutoff(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	if warn != "" {
		fmt.Fprintln(out, "warning: "+warn)
	}
	n, err := store.InactiveCount(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	fmt.Fprintf(out, "%d players without an event for %d days (since %s) and not online\n", n, days, cutoff.UTC().Format("2006-01-02 15:04 UTC"))
	if dry {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	jobs, err := jobsTable(ctx, db)
	if err != nil {
		return err
	}
	here := func() (string, error) {
		release, err := database.JobLock(ctx, db, wait, func() { fmt.Fprintln(out, "waiting for another job to finish ...") })
		if err != nil {
			return "", err
		}
		defer release()
		// Counted again under the lock: a reset may have run meanwhile.
		cutoff, days, _, err := store.InactiveCutoff(ctx, time.Now())
		if err != nil {
			return "", err
		}
		moved, err := store.MoveInactive(ctx, cutoff)
		if err != nil {
			return "", fmt.Errorf("stopped after %d players (run it again to complete it): %w", moved, err)
		}
		return collector.InactiveResult(moved, days), nil
	}
	return runJob(ctx, store, jobs, stats.JobInactive, "", "", wait, out, "moving here", here)
}
