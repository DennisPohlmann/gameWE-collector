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

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

const hallUsage = `Usage:
  gamewe halloffame [list] [GAME] [-config FILE]
  gamewe halloffame schedule GAME [EVERY] [-mode MODE] [--dry-run|-n] [--force|-f] [-config FILE]
  gamewe halloffame run GAME [--dry-run|-n] [--force|-f] [-wait DURATION] [-config FILE]

Scheduled ranking resets with a hall of fame. When a period of the schedule
ends, the best three players of the ranking (most points; hidden players and
bots do not count) are stored in hlstats_gamewe_hall_of_fame with their
values, then the ranking is reset (-mode, default player-stats). The running
collector does this by itself; the periods follow the calendar of the
collector's timezone.

  list      the stored entries, newest period first (all rankings or GAME)
  schedule  show the schedule of GAME, or set it: EVERY is day, week, month,
            quarter or year, optionally with a number ("2 month", "3 day"),
            or off. A new schedule starts with the period now running.
            Stored in hlstats_Options (gamewe_reset_schedule_GAME,
            gamewe_reset_mode_GAME), like the web admin does.
  run       do the scheduled reset of GAME now if a period has ended (e.g.
            from cron while no collector runs). With a running collector it
            is queued for it.

schedule and run show what they will do and ask [y/N]; --force (-f) does not
ask, --dry-run (-n) changes nothing.
-o json|yaml writes the result as a document; list also -o csv|tsv|name.`

// hallCmd runs `gamewe halloffame`.
func hallCmd(args []string) error {
	return hallWith(args, os.Stdin, os.Stdout)
}

func hallWith(args []string, in io.Reader, out io.Writer) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "list", "schedule", "run":
			sub, args = args[0], args[1:]
		case "help":
			fmt.Fprintln(out, hallUsage)
			return nil
		}
	}
	fs := flag.NewFlagSet("halloffame "+sub, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, hallUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	mode := fs.String("mode", "", "reset mode of the schedule: player-stats (default), all-stats or all")
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for the running collector, or for another job to finish")
	var cf changeFlags
	cf.add(fs, "only show what would be done")
	addOutputFlag(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	switch sub {
	case "list":
		if err := needList(true); err != nil {
			return err
		}
		if len(pos) > 1 {
			return exitError{2, hallUsage}
		}
		return hallRun(*path, func(ctx context.Context, _ *database.DB, store *stats.Store, _ *time.Location) error {
			game := ""
			if len(pos) == 1 {
				game = pos[0]
			}
			return hallList(ctx, store, game, out)
		})
	case "schedule":
		if len(pos) < 1 || len(pos) > 3 {
			return exitError{2, hallUsage}
		}
		return captured(out, func(out io.Writer, _ *result) error {
			return hallRun(*path, func(ctx context.Context, _ *database.DB, store *stats.Store, loc *time.Location) error {
				return hallSchedule(ctx, store, loc, pos[0], strings.Join(pos[1:], " "), *mode, cf, in, out)
			})
		})
	}
	if len(pos) != 1 {
		return exitError{2, hallUsage}
	}
	return captured(out, func(out io.Writer, r *result) error {
		return hallRun(*path, func(ctx context.Context, db *database.DB, store *stats.Store, loc *time.Location) error {
			return hallReset(ctx, db, store, loc, pos[0], cf, *wait, in, out, r)
		})
	})
}

// hallRun opens the database and the store (with the start skill of the
// options) and runs fn.
func hallRun(path string, fn func(ctx context.Context, db *database.DB, store *stats.Store, loc *time.Location) error) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	loc, err := cfg.Location()
	if err != nil {
		return fmt.Errorf("config %s: timezone: %w", path, err)
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
	missing, err := database.MissingSchema(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range missing {
		if m.Table == "hlstats_gamewe_hall_of_fame" || m.Table == "hlstats_gamewe_jobs" {
			return errors.New("no " + m.Table + ": " + database.SchemaHint)
		}
	}
	return fn(ctx, db, store, loc)
}

func hallList(ctx context.Context, store *stats.Store, game string, out io.Writer) error {
	rows, err := store.HallOfFame(ctx, game)
	if err != nil {
		return err
	}
	if structured() {
		l := hallEntries{}
		for _, h := range rows {
			l = append(l, hallDoc{h.Game, h.PeriodStart, h.PeriodEnd, h.Place, h.PlayerID, h.Name, h.Rank, h.Skill, h.Kills, h.Deaths})
		}
		return emit(out, l)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no entries yet")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "GAME\tPERIOD\tPLACE\tPLAYER\tPOINTS\tKILLS\tDEATHS\tRANK")
	for _, h := range rows {
		fmt.Fprintf(w, "%s\t%s - %s\t%d\t%s (#%d)\t%d\t%d\t%d\t%s\n", h.Game, h.PeriodStart, h.PeriodEnd, h.Place, h.Name, h.PlayerID, h.Skill, h.Kills, h.Deaths, h.Rank)
	}
	return w.Flush()
}

func hallSchedule(ctx context.Context, store *stats.Store, loc *time.Location, game, every, mode string, cf changeFlags, in io.Reader, out io.Writer) error {
	if _, err := store.ResetCounts(ctx, game, stats.ResetPlayerStats); err != nil { // checks the game
		return err
	}
	cur, err := store.ScheduledRanking(ctx, game)
	if err != nil {
		fmt.Fprintf(out, "warning: stored schedule of %s: %v\n", game, err)
	}
	now := time.Now()
	if every == "" && mode == "" {
		if !cur.Schedule.On() {
			fmt.Fprintf(out, "%s: no reset schedule\n", game)
			return nil
		}
		fmt.Fprintf(out, "%s: reset every %s (mode %s), the current period ends %s\n", game, cur.Schedule, cur.Mode, cur.Schedule.Next(now, loc).Format(time.DateTime+" MST"))
		return nil
	}
	sch := cur.Schedule
	if every != "" {
		if sch, err = stats.ParseResetSchedule(every); err != nil {
			return exitError{2, err.Error()}
		}
	}
	if mode == "" {
		mode = cmp.Or(cur.Mode, stats.ResetPlayerStats)
	}
	if mode, err = stats.ResetMode(mode); err != nil {
		return exitError{2, err.Error()}
	}
	if sch.On() {
		fmt.Fprintf(out, "%s: reset every %s (mode %s); the current period ends %s\n", game, sch, mode, sch.Next(now, loc).Format(time.DateTime+" MST"))
	} else {
		fmt.Fprintf(out, "%s: no scheduled reset any more (the hall of fame entries stay)\n", game)
	}
	if cf.dryRun {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if err := approve(ctx, cf, in, out, interactive(in), "Store this schedule?"); err != nil {
		return err
	}
	// The period now running is the first one: the last reset is its start.
	last := ""
	if sch.On() {
		last = itoa(sch.PeriodStart(now, loc).Unix())
	}
	for k, v := range map[string]string{stats.ScheduleKey(game): sch.String(), stats.ModeKey(game): mode, stats.LastResetKey(game): last} {
		if err := store.SetOption(ctx, k, v); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "stored")
	return nil
}

func hallReset(ctx context.Context, db *database.DB, store *stats.Store, loc *time.Location, game string, cf changeFlags, wait time.Duration, in io.Reader, out io.Writer, r *result) error {
	sr, err := store.ScheduledRanking(ctx, game)
	if err != nil {
		return err
	}
	due, err := store.Due(ctx, sr, time.Now(), loc)
	if err != nil {
		return err
	}
	if !due.Due {
		fmt.Fprintln(out, collector.ScheduledText(due))
		if due.Started && !cf.dryRun {
			if err := approve(ctx, cf, in, out, interactive(in), "Start the schedule with the period now running?"); err != nil {
				return err
			}
			_, err := store.ScheduledReset(ctx, sr, time.Now(), loc)
			return err
		}
		return nil
	}
	fmt.Fprintf(out, "%s: the period %s - %s has ended\n", game, due.From.Format(time.DateOnly), due.To.AddDate(0, 0, -1).Format(time.DateOnly))
	if len(due.Top) == 0 {
		fmt.Fprintln(out, "hall of fame: nobody played")
	}
	for _, e := range due.Top {
		fmt.Fprintf(out, "hall of fame %d. %s (#%d): %d points, %d kills, %d deaths\n", e.Place, e.Name, e.PlayerID, e.Skill, e.Kills, e.Deaths)
	}
	counts, err := store.ResetCounts(ctx, game, sr.Mode)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "then reset %s, mode %s: rows per table\n", game, sr.Mode)
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', tabwriter.AlignRight)
	for _, c := range counts {
		fmt.Fprintf(w, "%d\t  %s\t\n", c.Rows, c.Table)
		if r != nil {
			r.Tables = append(r.Tables, tableDoc{c.Table, c.Rows})
		}
	}
	w.Flush()
	if cf.dryRun {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if err := approve(ctx, cf, in, out, interactive(in), fmt.Sprintf("Store the hall of fame and reset %s now? This cannot be undone.", game)); err != nil {
		return err
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
		sr, err := store.ScheduledRanking(ctx, game) // again under the lock
		if err != nil {
			return "", err
		}
		res, err := store.ScheduledReset(ctx, sr, time.Now(), loc)
		if err != nil {
			return "", fmt.Errorf("stopped (run it again to complete it): %w; done before: %s", err, stats.FormatCounts(res.Tables))
		}
		return collector.ScheduledText(res), nil
	}
	return runJob(ctx, store, jobs, stats.JobScheduledReset, game, sr.Mode, wait, out, "resetting here", here)
}

type hallDoc struct {
	Game        string `json:"game"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	Place       int    `json:"place"`
	PlayerID    int64  `json:"player_id"`
	Name        string `json:"name"`
	Rank        string `json:"rank"`
	Points      int64  `json:"points"`
	Kills       int64  `json:"kills"`
	Deaths      int64  `json:"deaths"`
}

type hallEntries []hallDoc

func (hallEntries) Header() []string {
	return []string{"game", "period_start", "period_end", "place", "player_id", "name", "rank", "points", "kills", "deaths"}
}
func (l hallEntries) Rows() [][]string {
	out := [][]string{}
	for _, h := range l {
		out = append(out, []string{h.Game, h.PeriodStart, h.PeriodEnd, itoa(h.Place), itoa(h.PlayerID), h.Name, h.Rank, itoa(h.Points), itoa(h.Kills), itoa(h.Deaths)})
	}
	return out
}
