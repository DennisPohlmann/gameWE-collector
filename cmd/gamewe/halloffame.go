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
	"strconv"
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
  gamewe halloffame schedule -all [EVERY] [-mode MODE] [--dry-run|-n] [--force|-f] [-config FILE]
  gamewe halloffame run GAME [--dry-run|-n] [--force|-f] [-wait DURATION] [-config FILE]

Scheduled ranking resets with a hall of fame. When a period of the schedule
ends, the best five players of the ranking (most points; hidden players and
bots do not count; the web shows three, the others move up when an entry is
deleted) are stored in hlstats_gamewe_hall_of_fame with their values, then
the ranking is reset (-mode, default player-stats). The running collector
does this by itself; the periods follow the calendar of the collector's
timezone, a period ends at features.scheduled_resets.time.

  list      the stored entries, newest period first (all rankings or GAME)
  schedule  show the schedule of GAME, or set it: EVERY is day, week, month,
            quarter or year, optionally with a number ("2 month", "3 day")
            and a start day after @: the day of the month 1-28 ("month@15",
            "quarter@15"), the weekday 1-7 for weeks (Monday 1, "week@5"),
            MM-DD for years ("year@07-01"); default the 1st, Monday, 1
            January. "off": no reset for GAME even with a schedule for all;
            "inherit": follow the schedule for all rankings again.
            With -all instead of GAME: the schedule for all rankings that
            have none of their own ("off" removes it).
            A new schedule starts with the period now running.
            Stored in hlstats_Options (gamewe_reset_schedule_GAME,
            gamewe_reset_mode_GAME; for all: gamewe_reset_schedule,
            gamewe_reset_mode), like the web admin does.
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
	all := fs.Bool("all", false, "schedule: the schedule for all rankings without their own")
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
		game := ""
		if !*all {
			if len(pos) < 1 {
				return exitError{2, hallUsage}
			}
			game, pos = pos[0], pos[1:]
		}
		if len(pos) > 2 {
			return exitError{2, hallUsage}
		}
		return captured(out, func(out io.Writer, _ *result) error {
			return hallRun(*path, func(ctx context.Context, _ *database.DB, store *stats.Store, loc *time.Location) error {
				if game == "" {
					return hallScheduleAll(ctx, store, loc, strings.Join(pos, " "), *mode, cf, in, out)
				}
				return hallSchedule(ctx, store, loc, game, strings.Join(pos, " "), *mode, cf, in, out)
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
	store.ResetClock = resetClock(cfg)
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
			l = append(l, hallDoc{h.Game, h.PeriodStart, h.PeriodEnd, h.Place, h.PlayerID, h.Name, h.Rank, h.Skill, h.Kills, h.Deaths, h.Removed})
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
		removed := ""
		if h.Removed {
			removed = " [removed]"
		}
		fmt.Fprintf(w, "%s\t%s - %s\t%d\t%s (#%d)%s\t%d\t%d\t%d\t%s\n", h.Game, h.PeriodStart, h.PeriodEnd, h.Place, h.Name, h.PlayerID, removed, h.Skill, h.Kills, h.Deaths, h.Rank)
	}
	return w.Flush()
}

// hallSchedule shows or sets the schedule of one ranking: own, "off" or
// "inherit" (the schedule for all rankings).
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
		fmt.Fprintln(out, scheduleLine(game, cur, now, loc))
		return nil
	}
	own, ownMode := "", ""
	switch strings.ToLower(strings.TrimSpace(every)) {
	case "inherit":
		if mode != "" {
			return exitError{2, "-mode: a ranking that follows the schedule for all rankings has its mode (gamewe halloffame schedule -all -mode ...)"}
		}
	case stats.ScheduleOff:
		own = stats.ScheduleOff
	default:
		sch := cur.Schedule
		if every != "" {
			if sch, err = stats.ParseResetSchedule(every); err != nil {
				return exitError{2, err.Error()}
			}
		}
		if !sch.On() {
			return exitError{2, "no schedule: give EVERY, off or inherit"}
		}
		if ownMode, err = stats.ResetMode(cmp.Or(mode, cur.Mode, stats.ResetPlayerStats)); err != nil {
			return exitError{2, err.Error()}
		}
		own = sch.String()
	}
	next := cur
	next.Inherited = own == ""
	if own != "" {
		next.Schedule, _ = stats.ParseResetSchedule(own)
		next.Mode = ownMode
	} else if all, err := store.ScheduledRanking(ctx, ""); err == nil {
		next.Schedule, next.Mode = all.Schedule, all.Mode
	}
	next.Schedule.Clock = store.ResetClock
	fmt.Fprintln(out, scheduleLine(game, next, now, loc))
	if cf.dryRun {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if err := approve(ctx, cf, in, out, interactive(in), "Store this schedule?"); err != nil {
		return err
	}
	// The period now running is the first one: the last reset is its start.
	last := ""
	if next.Schedule.On() {
		last = stats.LastResetValue(next.Schedule.PeriodStart(now, loc), next.Schedule)
	}
	for _, kv := range [][2]string{{stats.ScheduleKey(game), own}, {stats.ModeKey(game), ownMode}, {stats.LastResetKey(game), last}} {
		if err := store.SetOption(ctx, kv[0], kv[1]); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "stored")
	return nil
}

// hallScheduleAll shows or sets the schedule for all rankings without
// their own.
func hallScheduleAll(ctx context.Context, store *stats.Store, loc *time.Location, every, mode string, cf changeFlags, in io.Reader, out io.Writer) error {
	all, err := store.ScheduledRanking(ctx, "")
	if err != nil {
		fmt.Fprintf(out, "warning: stored schedule for all rankings: %v\n", err)
	}
	following, err := store.InheritingRankings(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	show := func(r stats.ScheduledRanking) {
		fmt.Fprintln(out, scheduleLine("all rankings", r, now, loc))
		if len(following) > 0 {
			fmt.Fprintf(out, "rankings without a schedule of their own: %s\n", strings.Join(following, ", "))
		} else {
			fmt.Fprintln(out, "every ranking has a schedule of its own (or off)")
		}
	}
	if every == "" && mode == "" {
		show(all)
		return nil
	}
	sch := all.Schedule
	if every != "" {
		if sch, err = stats.ParseResetSchedule(every); err != nil {
			return exitError{2, err.Error()}
		}
	}
	if mode, err = stats.ResetMode(cmp.Or(mode, all.Mode, stats.ResetPlayerStats)); err != nil {
		return exitError{2, err.Error()}
	}
	if !sch.On() {
		mode = ""
	}
	sch.Clock = store.ResetClock
	show(stats.ScheduledRanking{Schedule: sch, Mode: mode})
	if cf.dryRun {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if err := approve(ctx, cf, in, out, interactive(in), "Store this schedule for all rankings?"); err != nil {
		return err
	}
	// The rankings that follow it start the new schedule with the period
	// now running: their last reset belongs to the old schedule.
	for _, kv := range [][2]string{{stats.GlobalScheduleKey, sch.String()}, {stats.GlobalModeKey, mode}} {
		if err := store.SetOption(ctx, kv[0], kv[1]); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "stored")
	return nil
}

// scheduleLine describes the schedule of r for `schedule`.
func scheduleLine(name string, r stats.ScheduledRanking, now time.Time, loc *time.Location) string {
	from := ""
	if r.Inherited && name != "all rankings" {
		from = " (the schedule for all rankings)"
	}
	if !r.Schedule.On() {
		return name + ": no reset schedule" + from
	}
	return fmt.Sprintf("%s: reset every %s (mode %s)%s; the current period ends %s", name, r.Schedule, r.Mode, from, r.Schedule.Next(now, loc).Format(time.DateTime+" MST"))
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
	Removed     bool   `json:"removed"`
}

type hallEntries []hallDoc

func (hallEntries) Header() []string {
	return []string{"game", "period_start", "period_end", "place", "player_id", "name", "rank", "points", "kills", "deaths", "removed"}
}
func (l hallEntries) Rows() [][]string {
	out := [][]string{}
	for _, h := range l {
		out = append(out, []string{h.Game, h.PeriodStart, h.PeriodEnd, itoa(h.Place), itoa(h.PlayerID), h.Name, h.Rank, itoa(h.Points), itoa(h.Kills), itoa(h.Deaths), strconv.FormatBool(h.Removed)})
	}
	return out
}

// resetClock is features.scheduled_resets.time in minutes after midnight
// (checked when the config is loaded).
func resetClock(cfg config.Config) int {
	h, m, _ := cfg.Features.ScheduledResets.At()
	return h*60 + m
}
