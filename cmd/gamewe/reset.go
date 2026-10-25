// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

const resetUsage = `Usage:
  gamewe reset GAME -mode player-stats|all-stats|all [-dry-run] [-yes] [-config FILE]
  gamewe reset -list [-config FILE]

Resets the ranking GAME (an hlstats_Games code):
  -mode player-stats  player statistics only: the players stay with the start
                      skill and empty statistics, the server statistics stay
                      (stats is the old name and still works)
  -mode all-stats     player and server statistics (server counters, load
                      history, trend); the players stay
  -mode all           all statistics and the players of the game
Games, servers, definitions and settings stay.
-dry-run shows the rows per table and changes nothing. Without -yes it asks
for the game code to confirm.

With a running collector (heartbeat less than 2 minutes old, table
hlstats_gamewe_jobs present) the reset is queued for it: it pauses the game's servers, resets and goes
on. Otherwise it runs here; then no collector may be running.
-list shows the latest queued resets.
-o json|yaml writes the result as a document (the question goes to stderr);
-list also -o csv|tsv|name.`

// resetCmd runs `gamewe reset`.
func resetCmd(args []string) error {
	return resetWith(args, os.Stdin, os.Stdout)
}

func resetWith(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, resetUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	mode := fs.String("mode", "", "player-stats (keep players and server statistics), all-stats (keep players) or all (delete the players too)")
	dry := fs.Bool("dry-run", false, "only show the rows per table")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	list := fs.Bool("list", false, "show the latest queued resets")
	addOutputFlag(fs)
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for a running collector to do the reset")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if err := needList(*list); err != nil {
		return err
	}
	if *list {
		return resetRun(pos, *path, *mode, *dry, *yes, true, *wait, in, out, nil)
	}
	return captured(out, func(out io.Writer, r *result) error {
		return resetRun(pos, *path, *mode, *dry, *yes, false, *wait, in, out, r)
	})
}

// resetRun is gamewe reset after the flags; r is the document of -o
// json|yaml (nil for the text and for -list).
func resetRun(pos []string, path, mode string, dry, yes, list bool, wait time.Duration, in io.Reader, out io.Writer, r *result) error {
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
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	if opts, err := store.Options(ctx); err == nil {
		s, _ := settings.FromOptions(opts)
		set.Set(s) // the start skill
	}
	jobs, err := jobsTable(ctx, db)
	if err != nil {
		return err
	}
	if list {
		if !jobs {
			return errors.New("no hlstats_gamewe_jobs: " + database.SchemaHint)
		}
		return listJobs(ctx, store, out)
	}
	if len(pos) != 1 {
		return errors.New(resetUsage)
	}
	if mode, err = stats.ResetMode(mode); err != nil {
		return fmt.Errorf("%w\n\n%s", err, resetUsage)
	}
	game := pos[0]
	counts, err := store.ResetCounts(ctx, game, mode)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(out, "reset %s, mode %s: rows per table\n", game, mode)
	for _, c := range counts {
		fmt.Fprintf(w, "%d\t  %s\t\n", c.Rows, c.Table)
		if r != nil {
			r.Tables = append(r.Tables, tableDoc{c.Table, c.Rows})
		}
	}
	w.Flush()
	if dry {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if !yes {
		fmt.Fprintf(promptOut(out), "This cannot be undone. Type the game code (%s) to reset it: ", game)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != game {
			return errors.New("not confirmed, nothing changed")
		}
	}
	if jobs {
		beat, err := store.LastHeartbeat(ctx)
		if err != nil {
			return err
		}
		if !beat.IsZero() && time.Since(beat) < stats.HeartbeatFresh {
			return queueReset(ctx, store, game, mode, wait, out)
		}
	}
	fmt.Fprintln(out, "no running collector (no heartbeat in the last 2 minutes): resetting here")
	res, err := store.Reset(ctx, game, mode)
	if err != nil {
		return fmt.Errorf("reset stopped (run it again to complete it): %w; done before: %s", err, stats.FormatCounts(res))
	}
	fmt.Fprintf(out, "done: %s\n", stats.FormatCounts(res))
	return nil
}

// jobsTable reports whether the database has hlstats_gamewe_jobs.
func jobsTable(ctx context.Context, db *database.DB) (bool, error) {
	missing, err := database.MissingSchema(ctx, db)
	if err != nil {
		return false, err
	}
	for _, r := range missing {
		if r.Table == "hlstats_gamewe_jobs" {
			return false, nil
		}
	}
	return true, nil
}

// queueReset hands the reset to the running collector and waits for it.
func queueReset(ctx context.Context, store *stats.Store, game, mode string, wait time.Duration, out io.Writer) error {
	id, err := store.AddJob(ctx, stats.JobReset, game, mode, "cli", time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "queued as job %d for the running collector, waiting ...\n", id)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		job, err := store.Job(ctx, id)
		if err != nil {
			return err
		}
		switch job.Status {
		case stats.JobDone:
			fmt.Fprintf(out, "done: %s\n", job.Result)
			return nil
		case stats.JobFailed:
			return fmt.Errorf("job %d failed: %s", id, job.Result)
		case stats.JobPending:
			// The collector stopped after the heartbeat: take the job back
			// (only while nobody claimed it) and reset here.
			beat, err := store.LastHeartbeat(ctx)
			if err != nil {
				return err
			}
			if time.Since(beat) < stats.HeartbeatFresh {
				continue
			}
			ok, err := store.ClaimJob(ctx, id, stats.JobPending, time.Now())
			if err != nil {
				return err
			}
			if !ok {
				continue // the collector took it after all
			}
			fmt.Fprintln(out, "the collector stopped (no heartbeat any more): resetting here")
			res, err := store.Reset(ctx, game, mode)
			status, result := stats.JobDone, stats.FormatCounts(res)
			if err != nil {
				status, result = stats.JobFailed, err.Error()+"; done before: "+result
			}
			if ferr := store.FinishJob(ctx, id, status, result, time.Now()); ferr != nil && err == nil {
				err = ferr
			}
			if err != nil {
				return fmt.Errorf("reset stopped (run it again to complete it): %w", err)
			}
			fmt.Fprintf(out, "done: %s\n", result)
			return nil
		}
	}
	return fmt.Errorf("job %d not finished after %v (still queued or running; see gamewe reset -list)", id, wait)
}

func listJobs(ctx context.Context, store *stats.Store, out io.Writer) error {
	jobs, err := store.RecentJobs(ctx, 20)
	if err != nil {
		return err
	}
	if structured() {
		l := jobList{}
		for _, j := range jobs {
			l = append(l, jobDoc{j.ID, j.Type, j.Game, j.Mode, j.Status, j.RequestedBy, unixTime(j.RequestedAt), unixTime(j.StartedAt), unixTime(j.FinishedAt), j.Result})
		}
		return emit(out, l)
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tGAME\tMODE\tSTATUS\tBY\tREQUESTED\tFINISHED\tRESULT")
	ts := func(t int64) string {
		if t == 0 {
			return "-"
		}
		return time.Unix(t, 0).Format("2006-01-02 15:04:05")
	}
	for _, j := range jobs {
		res := j.Result
		if r := []rune(res); len(r) > 80 {
			res = string(r[:77]) + "..."
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Game, j.Mode, j.Status, j.RequestedBy, ts(j.RequestedAt), ts(j.FinishedAt), res)
	}
	return w.Flush()
}

type jobDoc struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Game        string `json:"game"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	Result      string `json:"result"`
}

type jobList []jobDoc

func (jobList) Header() []string {
	return []string{"id", "type", "game", "mode", "status", "requested_by", "requested_at", "started_at", "finished_at", "result"}
}
func (l jobList) Rows() [][]string {
	out := [][]string{}
	for _, j := range l {
		out = append(out, []string{itoa(j.ID), j.Type, j.Game, j.Mode, j.Status, j.RequestedBy, j.RequestedAt, j.StartedAt, j.FinishedAt, j.Result})
	}
	return out
}
