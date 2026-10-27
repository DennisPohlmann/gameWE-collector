// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

const jobsUsage = `Usage:
  gamewe jobs [-queue] [-type TYPE[,TYPE]] [-status STATUS[,STATUS]] [-n N] [-config FILE]

Lists the jobs in hlstats_gamewe_jobs: ranking resets (reset, from the
command line or the web admin), scheduled resets (scheduled-reset) and moves
of inactive players (inactive), newest first. The running collector takes
pending jobs one after the other.
  -queue    only the waiting and running jobs (pending, running), in the
            order the collector does them
  -type     reset, scheduled-reset, inactive (comma separated)
  -status   pending, running, done, failed (comma separated)
  -n        at most N jobs (default 20)
Finished jobs stay 30 days (gamewe db purge deletes them).
gamewe reset -list and gamewe inactive -list are the same with -type.
-o json|yaml|csv|tsv|name writes the list as a document.`

var (
	jobTypes    = []string{stats.JobReset, stats.JobScheduledReset, stats.JobInactive}
	jobStatuses = []string{stats.JobPending, stats.JobRunning, stats.JobDone, stats.JobFailed}
)

// jobsCmd runs `gamewe jobs`.
func jobsCmd(args []string) error {
	return jobsWith(args, os.Stdout)
}

func jobsWith(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, jobsUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	queue := fs.Bool("queue", false, "only the pending and running jobs, oldest first")
	types := fs.String("type", "", "only these job types: "+strings.Join(jobTypes, ", "))
	statuses := fs.String("status", "", "only these states: "+strings.Join(jobStatuses, ", "))
	n := fs.Int("n", 20, "at most N jobs")
	addOutputFlag(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return exitError{2, fmt.Sprintf("unexpected argument %q\n\n%s", pos[0], jobsUsage)}
	}
	f := stats.JobFilter{Limit: *n, OldestFirst: *queue}
	if f.Types, err = jobValues("-type", *types, jobTypes); err != nil {
		return err
	}
	if f.Statuses, err = jobValues("-status", *statuses, jobStatuses); err != nil {
		return err
	}
	if *queue {
		if len(f.Statuses) > 0 {
			return exitError{2, "-queue and -status cannot be used together"}
		}
		f.Statuses = []string{stats.JobPending, stats.JobRunning}
	}
	if *n < 1 {
		return exitError{2, "-n must be at least 1"}
	}
	return showJobs(*path, f, out)
}

// jobValues splits a comma separated flag value and checks every part
// against allowed.
func jobValues(flagName, v string, allowed []string) ([]string, error) {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !slices.Contains(allowed, s) {
			return nil, exitError{2, fmt.Sprintf("%s %q: use %s", flagName, s, strings.Join(allowed, ", "))}
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// showJobs opens the database of the config file at path and lists the
// jobs that match f.
func showJobs(path string, f stats.JobFilter, out io.Writer) error {
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
	jobs, err := jobsTable(ctx, db)
	if err != nil {
		return err
	}
	if !jobs {
		return errors.New("no hlstats_gamewe_jobs: " + database.SchemaHint)
	}
	return listJobs(ctx, stats.NewStore(db, settings.NewLive(settings.Defaults())), f, out)
}
