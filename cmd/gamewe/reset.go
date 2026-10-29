// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"cmp"
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
  gamewe reset GAME -mode player-stats|all-stats|all [--dry-run|-n] [--force|-f] [-config FILE]
  gamewe reset -list [-config FILE]

Resets the ranking GAME (an hlstats_Games code):
  -mode player-stats  player statistics only: the players stay with the start
                      skill and empty statistics, the server statistics stay
                      (stats is the old name and still works)
  -mode all-stats     player and server statistics (server counters, load
                      history, trend); the players stay
  -mode all           all statistics and the players of the game
Games, servers, definitions and settings stay. A template (a ranking without
servers, it only holds the definitions for new rankings) is not reset.
-dry-run shows the rows per table and changes nothing. Without --force it asks
for the game code to confirm.

With a running collector (heartbeat less than 2 minutes old, table
hlstats_gamewe_jobs present) the reset is queued for it: it pauses the game's servers, resets and goes
on. Otherwise it runs here; then no collector may be running.
-list shows the latest resets, also the scheduled ones (gamewe jobs: all jobs).
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
	var cf changeFlags
	cf.add(fs, "only show the rows per table")
	dry, yes := &cf.dryRun, &cf.force
	list := fs.Bool("list", false, "show the latest reset jobs")
	addOutputFlag(fs)
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for a running collector to do the reset")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if err := needList(*list); err != nil {
		return err
	}
	if err := cf.check(); err != nil {
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
		return listJobs(ctx, store, stats.JobFilter{Types: []string{stats.JobReset, stats.JobScheduledReset}, Limit: 20}, out)
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
	if err := store.CheckNotTemplate(ctx, game); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(out, "reset %s, mode %s: rows per table\n", game, mode)
	for _, c := range counts {
		fmt.Fprintf(w, "%d\t  %s\t\n", c.Rows, db.Prefix.Table(c.Table))
		if r != nil {
			r.Tables = append(r.Tables, tableDoc{db.Prefix.Table(c.Table), c.Rows})
		}
	}
	w.Flush()
	if dry {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if !yes {
		if !interactive(in) {
			return errNoTTYYes
		}
		line, _ := ask(ctx, in, promptOut(out), fmt.Sprintf("This cannot be undone. Type the game code (%s) to reset it: ", game))
		if strings.TrimSpace(line) != game {
			return errNotConfirmed
		}
	}
	here := func() (string, error) {
		release, err := database.JobLock(ctx, db, wait, func() { fmt.Fprintln(out, "waiting for another job to finish ...") })
		if err != nil {
			return "", err
		}
		defer release()
		res, err := store.Reset(ctx, game, mode)
		if err != nil {
			return "", fmt.Errorf("reset stopped (run it again to complete it): %w; done before: %s", err, stats.FormatCounts(res))
		}
		return stats.FormatCounts(res), nil
	}
	return runJob(ctx, store, jobs, stats.JobReset, game, mode, wait, out, "resetting here", here)
}

// runJob hands a job to the running collector (heartbeat fresh, table
// hlstats_gamewe_jobs present) and waits for it, or else runs it here with
// here (which takes the job lock).
func runJob(ctx context.Context, store *stats.Store, jobs bool, typ, game, mode string, wait time.Duration, out io.Writer, doing string, here func() (string, error)) error {
	if jobs {
		beat, err := store.LastHeartbeat(ctx)
		if err != nil {
			return err
		}
		if !beat.IsZero() && time.Since(beat) < stats.HeartbeatFresh {
			return queueJob(ctx, store, typ, game, mode, wait, out, doing, here)
		}
	}
	fmt.Fprintf(out, "no running collector (no heartbeat in the last 2 minutes): %s\n", doing)
	res, err := here()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "done: %s\n", res)
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

// queueJob hands the job to the running collector and waits for it; if
// the collector stops before it took the job, here runs it.
func queueJob(ctx context.Context, store *stats.Store, typ, game, mode string, wait time.Duration, out io.Writer, doing string, here func() (string, error)) error {
	id, err := store.AddJob(ctx, typ, game, mode, "cli", time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "queued as job %d for the running collector, waiting ...\n", id)
	watch := &jobWatch{out: out, id: id}
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
		case stats.JobRunning:
			watch.update(job, nil, time.Now())
		case stats.JobPending:
			// The collector stopped after the heartbeat: take the job back
			// (only while nobody claimed it) and reset here.
			beat, err := store.LastHeartbeat(ctx)
			if err != nil {
				return err
			}
			if time.Since(beat) < stats.HeartbeatFresh {
				open, err := store.ListJobs(ctx, stats.JobFilter{Statuses: []string{stats.JobPending, stats.JobRunning}, Limit: 100, OldestFirst: true})
				if err != nil {
					return err
				}
				watch.update(job, open, time.Now())
				continue
			}
			ok, err := store.ClaimJob(ctx, id, stats.JobPending, time.Now())
			if err != nil {
				return err
			}
			if !ok {
				continue // the collector took it after all
			}
			fmt.Fprintf(out, "the collector stopped (no heartbeat any more): %s\n", doing)
			result, err := here()
			status := stats.JobDone
			if err != nil {
				status, result = stats.JobFailed, err.Error()
			}
			if ferr := store.FinishJob(ctx, id, status, result, time.Now()); ferr != nil && err == nil {
				err = ferr
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "done: %s\n", result)
			return nil
		}
	}
	return fmt.Errorf("job %d not finished after %v (still queued or running, the collector goes on with it; see gamewe jobs -queue)", id, wait)
}

// jobNoteEvery is how often jobWatch says that a job still waits or runs
// when nothing else changed.
const jobNoteEvery = 15 * time.Second

// jobWatch tells the user what a queued job does while queueJob waits:
// its place in the queue, when the collector started it, its progress
// and, every jobNoteEvery without news, how long it waits or runs.
type jobWatch struct {
	out    io.Writer
	id     int64
	status string    // shown last
	text   string    // queue or progress shown last
	since  time.Time // start of the wait or of the run
	shown  time.Time // last line
}

// update shows what changed in job; open are the pending and running jobs,
// oldest first (only looked at while job is pending).
func (w *jobWatch) update(job stats.Job, open []stats.Job, now time.Time) {
	switch job.Status {
	case stats.JobPending:
		q := queueText(job.ID, open)
		if w.status != job.Status {
			w.since = now
		}
		switch {
		case w.status != job.Status || q != w.text:
			w.say(now, "job %d waits for the collector: %s", w.id, q)
		case now.Sub(w.shown) >= jobNoteEvery:
			w.say(now, "job %d still waiting (%s) ...", w.id, dur(now.Sub(w.since)))
		}
		w.status, w.text = job.Status, q
	case stats.JobRunning:
		if w.status != job.Status {
			w.since = now
			if job.StartedAt > 0 {
				w.since = time.Unix(job.StartedAt, 0)
			}
			w.say(now, "job %d started in the collector at %s, running ...", w.id, w.since.Format("15:04:05"))
			w.status, w.text = job.Status, job.Result
			return
		}
		switch {
		case job.Result != "" && job.Result != w.text:
			w.say(now, "job %d running (%s): %s", w.id, dur(now.Sub(w.since)), job.Result)
		case now.Sub(w.shown) >= jobNoteEvery:
			w.say(now, "job %d still running (%s) ...", w.id, dur(now.Sub(w.since)))
		}
		w.text = job.Result
	}
}

func (w *jobWatch) say(now time.Time, format string, a ...any) {
	fmt.Fprintf(w.out, format+"\n", a...)
	w.shown = now
}

// queueText describes the jobs ahead of job id in open: "next in line" or
// "2 jobs ahead: 7 reset css (running), 8 inactive (pending)".
func queueText(id int64, open []stats.Job) string {
	var ahead []string
	for _, j := range open {
		if j.ID < id {
			ahead = append(ahead, strings.TrimSpace(fmt.Sprintf("%d %s %s", j.ID, j.Type, j.Game))+" ("+j.Status+")")
		}
	}
	switch len(ahead) {
	case 0:
		return "next in line"
	case 1:
		return "1 job ahead: " + ahead[0]
	}
	text := fmt.Sprintf("%d jobs ahead: ", len(ahead))
	if len(ahead) > 3 {
		return text + strings.Join(ahead[:3], ", ") + ", ..."
	}
	return text + strings.Join(ahead, ", ")
}

// dur is d rounded to seconds ("1m20s").
func dur(d time.Duration) string { return max(d, 0).Round(time.Second).String() }

// listJobs writes the jobs that match f (gamewe jobs, reset -list,
// inactive -list).
func listJobs(ctx context.Context, store *stats.Store, f stats.JobFilter, out io.Writer) error {
	jobs, err := store.ListJobs(ctx, f)
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
	fmt.Fprintln(w, "ID\tTYPE\tGAME\tMODE\tSTATUS\tBY\tREQUESTED\tFINISHED\tRESULT")
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
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Type, cmp.Or(j.Game, "-"), cmp.Or(j.Mode, "-"), j.Status, j.RequestedBy, ts(j.RequestedAt), ts(j.FinishedAt), res)
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
