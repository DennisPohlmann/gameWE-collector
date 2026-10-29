// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/stats"
)

// JobPoll is how often the collector looks for queued jobs.
const JobPoll = 10 * time.Second

// Jobs runs the jobs of hlstats_gamewe_jobs (ranking resets queued by the
// web admin or `gamewe reset`) in the running collector, which pauses the
// game's servers for it, writes the heartbeat and queues its daily jobs:
// the daily awards (features.daily_awards) and the move of inactive
// players (features.inactive_players), both run one after the other with
// the other jobs, never during a reset. With the heartbeat it writes when
// they run next (stats.JobPlan), for the web admin.
type Jobs struct {
	Store    *stats.Store
	Hub      *Hub
	Log      *slog.Logger
	Now      func() time.Time // tests
	Inactive InactiveConfig
	Awards   AwardsConfig
	// Version is written to hlstats_Options with the first heartbeat
	// (stats.VersionKey); empty: not written (tests).
	Version string
	// Lock takes the job lock of the database (database.JobLock) for a
	// reset and a move of inactive players; nil: none (tests).
	Lock func(ctx context.Context) (release func(), err error)
	// Zone is the time zone of the scheduled ranking resets (the
	// collector's timezone); nil: the server's.
	Zone *time.Location
	// ResetClock is features.scheduled_resets.time (HH:MM), only shown in
	// the plan for the web admin.
	ResetClock string

	beatEvery    time.Duration // tests; 0: stats.HeartbeatInterval
	lastPlan     time.Time
	lastSchedule time.Time
	lastPrune    time.Time
	lastInactive time.Time
	lastAwards   time.Time
	written      map[string]string // plan keys as last written
	// scheduled: QueueScheduled ran before (its jobs are no longer queued
	// at the collector's start).
	scheduled bool
}

// AwardsConfig is features.daily_awards: once a day at At the awards of
// the previous calendar day (in At.Loc) are queued, and at start if that
// day has none yet.
type AwardsConfig struct {
	Enabled bool
	At      Schedule
}

// InactiveConfig is features.inactive_players: once a day at At (its hour
// and minute in its zone), players without an event for hlstats_Options
// MinActivity days who are not online are moved to hlstats_PlayersInactive.
type InactiveConfig struct {
	Enabled bool
	At      Schedule
}

func (j *Jobs) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Run first completes jobs left running by a stopped collector, then polls
// every JobPoll until ctx is done. The heartbeat has its own loop (Beat),
// so a long job (a reset, the daily awards, the move of inactive players or
// the wait for the job lock) does not make the collector look stopped.
func (j *Jobs) Run(ctx context.Context) {
	j.lastInactive = j.now() // the first move at the next daily time, not at start
	go j.beatLoop(ctx)
	if err := j.Recover(ctx); err != nil {
		j.Log.Error("jobs: recovering running jobs failed", "err", err)
	}
	t := time.NewTicker(JobPoll)
	defer t.Stop()
	for {
		if err := j.Poll(ctx); err != nil && ctx.Err() == nil {
			j.Log.Error("jobs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Recover runs again the jobs left in status running (the collector
// stopped during a reset; a reset can run again).
func (j *Jobs) Recover(ctx context.Context) error {
	jobs, err := j.Store.JobsByStatus(ctx, stats.JobRunning)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		j.Log.Warn("jobs: completing a job a stopped collector left running", "job", job.ID, "game", job.Game, "mode", job.Mode)
		j.do(ctx, job)
	}
	return nil
}

// beatLoop writes the heartbeat now and every HeartbeatInterval until ctx
// is done, apart from the jobs.
func (j *Jobs) beatLoop(ctx context.Context) {
	every := j.beatEvery
	if every <= 0 {
		every = stats.HeartbeatInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	var versionWritten bool
	for {
		if err := j.Beat(ctx, !versionWritten); err != nil && ctx.Err() == nil {
			j.Log.Error("jobs", "err", err)
		} else {
			versionWritten = true
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Beat writes the heartbeat and, with version, the collector's version
// (Version, when set).
func (j *Jobs) Beat(ctx context.Context, version bool) error {
	if version && j.Version != "" {
		if err := j.Store.SetVersion(ctx, j.Version); err != nil {
			return fmt.Errorf("version: %w", err)
		}
	}
	if err := j.Store.Heartbeat(ctx, j.now()); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// Poll writes when the daily jobs run next (each HeartbeatInterval),
// removes old jobs and runs the pending ones, oldest first. A second job
// for a game that has one pending or running already is refused.
func (j *Jobs) Poll(ctx context.Context) error {
	now := j.now()
	if now.Sub(j.lastPlan) >= stats.HeartbeatInterval {
		if err := j.writePlan(ctx, j.Plan(now)); err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		j.lastPlan = now
	}
	if now.Sub(j.lastPrune) >= time.Hour {
		if err := j.Store.DeleteOldJobs(ctx, now.Add(-stats.JobKeep)); err != nil {
			return fmt.Errorf("delete old jobs: %w", err)
		}
		j.lastPrune = now
	}
	if now.Sub(j.lastSchedule) >= time.Minute {
		j.lastSchedule = now
		if err := j.QueueScheduled(ctx, now); err != nil && ctx.Err() == nil {
			j.Log.Error("jobs: scheduled ranking resets", "err", err)
		}
	}
	if j.inactiveDue(now) {
		if err := j.queueDaily(ctx, stats.JobInactive, "", j.origin(stats.OriginInactive, false), now); err != nil && ctx.Err() == nil {
			j.Log.Error("jobs: queueing the move of inactive players failed", "err", err)
		}
	}
	startup := j.lastAwards.IsZero()
	if day, ok := j.awardsDue(now); ok {
		if err := j.queueAwards(ctx, day, startup, now); err != nil && ctx.Err() == nil {
			j.Log.Error("jobs: queueing the daily awards failed", "day", stats.AwardDate(day), "err", err)
		}
	}
	pending, err := j.Store.JobsByStatus(ctx, stats.JobPending)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	running, err := j.Store.JobsByStatus(ctx, stats.JobRunning)
	if err != nil {
		return err
	}
	busy := map[string]int64{} // game -> the job that has it
	for _, r := range running {
		busy[r.Game] = r.ID
	}
	for _, job := range pending {
		if job.Type == stats.JobInactive || job.Type == stats.JobAwards { // all games; a second one is a no-op
			if ok, err := j.Store.ClaimJob(ctx, job.ID, stats.JobPending, j.now()); err != nil || !ok {
				continue
			}
			j.do(ctx, job)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if other, ok := busy[job.Game]; ok {
			if ok, err := j.Store.ClaimJob(ctx, job.ID, stats.JobPending, j.now()); err != nil || !ok {
				continue
			}
			j.finish(ctx, job, stats.JobFailed, fmt.Sprintf("refused: job %d resets %s already", other, job.Game))
			continue
		}
		busy[job.Game] = job.ID
		ok, err := j.Store.ClaimJob(ctx, job.ID, stats.JobPending, j.now())
		if err != nil {
			return err
		}
		if !ok {
			continue // taken by someone else
		}
		j.do(ctx, job)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// origin is the origin of a job the collector queues itself.
func (j *Jobs) origin(feature string, startup bool) stats.JobOrigin {
	return stats.JobOrigin{Feature: feature, Startup: startup, Version: j.Version}
}

// queueDaily queues a daily job of typ (mode: its argument) unless one of
// that type and mode is pending or running already.
func (j *Jobs) queueDaily(ctx context.Context, typ, mode string, o stats.JobOrigin, now time.Time) error {
	for _, st := range []string{stats.JobPending, stats.JobRunning} {
		jobs, err := j.Store.JobsByStatus(ctx, st)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(jobs, func(o stats.Job) bool { return o.Type == typ && o.Mode == mode }) {
			return nil
		}
	}
	_, err := j.Store.AddJob(ctx, typ, "", mode, stats.JobBySchedule, o, now)
	return err
}

// awardsDue reports whether the daily awards are due and the day they
// decide, and marks them done: at the first poll (the latest run's day may
// lack its awards) and once per daily time passed since.
func (j *Jobs) awardsDue(now time.Time) (time.Time, bool) {
	if !j.Awards.Enabled {
		return time.Time{}, false
	}
	run, day := j.Awards.At.Last(now)
	if !j.lastAwards.IsZero() && !run.After(j.lastAwards) {
		return time.Time{}, false
	}
	j.lastAwards = now
	return day, true
}

// queueAwards queues the daily awards of day unless the day has some
// already (a restart of the collector); startup: at the collector's start.
func (j *Jobs) queueAwards(ctx context.Context, day time.Time, startup bool, now time.Time) error {
	done, err := j.Store.AwardsDone(ctx, day)
	if err != nil || done {
		return err
	}
	o := j.origin(stats.OriginDailyAwards, startup)
	o.Day = stats.AwardDate(day)
	return j.queueDaily(ctx, stats.JobAwards, stats.AwardDate(day), o, now)
}

// awardsDay is the day of a queued awards job (its mode) in the zone of
// the daily awards.
func (j *Jobs) awardsDay(mode string) (time.Time, error) {
	loc := j.Awards.At.Loc
	if loc == nil {
		loc = j.zone()
	}
	day, err := time.ParseInLocation(time.DateOnly, mode, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("awards job: day %q is not YYYY-MM-DD", mode)
	}
	return day, nil
}

// Plan returns the plan entries the web admin shows: key -> value.
func (j *Jobs) Plan(now time.Time) map[string]string {
	plan := func(on bool, s Schedule, next time.Time) string {
		p := stats.JobPlan{Enabled: on, Time: s.Clock()}
		if s.Loc != nil {
			p.Zone = s.Loc.String()
		}
		if on && s.Loc != nil {
			p.Next = next.Unix()
		}
		b, _ := json.Marshal(p)
		return string(b)
	}
	var awardsNext, inactiveNext time.Time
	if j.Awards.At.Loc != nil {
		awardsNext = j.Awards.At.Next(now)
	}
	if j.Inactive.At.Loc != nil {
		inactiveNext = j.Inactive.At.Next(now)
	}
	resets, _ := json.Marshal(stats.JobPlan{Enabled: true, Time: j.ResetClock, Zone: j.zone().String()})
	return map[string]string{
		stats.NextAwardsKey:   plan(j.Awards.Enabled, j.Awards.At, awardsNext),
		stats.NextInactiveKey: plan(j.Inactive.Enabled, j.Inactive.At, inactiveNext),
		stats.NextResetsKey:   string(resets),
	}
}

// writePlan stores the plan entries that changed since the last call.
func (j *Jobs) writePlan(ctx context.Context, entries map[string]string) error {
	if j.written == nil {
		j.written = map[string]string{}
	}
	for _, k := range slices.Sorted(maps.Keys(entries)) {
		v := entries[k]
		if old, ok := j.written[k]; ok && old == v {
			continue
		}
		if err := j.Store.SetOption(ctx, k, v); err != nil {
			return err
		}
		j.written[k] = v
	}
	return nil
}

// inactiveDue reports whether the daily move is due and marks it done: once
// per daily time passed since the last move (also across DST changes:
// At.Last gives one run per calendar day).
func (j *Jobs) inactiveDue(now time.Time) bool {
	if !j.Inactive.Enabled {
		return false
	}
	if run, _ := j.Inactive.At.Last(now); !run.After(j.lastInactive) {
		return false
	}
	j.lastInactive = now
	return true
}

func (j *Jobs) zone() *time.Location {
	if j.Zone != nil {
		return j.Zone
	}
	return time.Local
}

// QueueScheduled queues a scheduled reset for every ranking whose period
// ended (unless one is queued or running for it already). A schedule
// without a last reset gets the current period as its first one. The jobs
// of its first call after the collector's start are marked as such
// (JobOrigin.Startup).
func (j *Jobs) QueueScheduled(ctx context.Context, now time.Time) error {
	startup := !j.scheduled
	j.scheduled = true
	rankings, bad, err := j.Store.ScheduledRankings(ctx)
	if err != nil {
		return err
	}
	for _, e := range bad {
		j.Log.Warn("jobs: broken reset schedule, skipped", "err", e)
	}
	if off, err := j.Store.ForgetStoppedPeriods(ctx); err != nil {
		return err
	} else if len(off) > 0 {
		j.Log.Info("jobs: reset schedule stopped", "games", strings.Join(off, ","))
	}
	var open []stats.Job
	for _, st := range []string{stats.JobPending, stats.JobRunning} {
		jobs, err := j.Store.JobsByStatus(ctx, st)
		if err != nil {
			return err
		}
		open = append(open, jobs...)
	}
	next := map[string]string{}
	defer func() {
		if err := j.writePlan(ctx, next); err != nil && ctx.Err() == nil {
			j.Log.Warn("jobs: storing the next scheduled resets failed", "err", err)
		}
	}()
	for _, r := range rankings {
		due, err := j.Store.Due(ctx, r, now, j.zone())
		if err != nil {
			return fmt.Errorf("%s: %w", r.Game, err)
		}
		next[stats.NextResetKey(r.Game)] = strconv.FormatInt(due.Next.Unix(), 10)
		if due.Started {
			if _, err := j.Store.ScheduledReset(ctx, r, now, j.zone()); err != nil {
				return fmt.Errorf("%s: %w", r.Game, err)
			}
			j.Log.Info("jobs: reset schedule started", "game", r.Game, "every", r.Schedule.String(), "next", due.Next.Format(time.DateTime))
			continue
		}
		if due.Moved {
			if _, err := j.Store.ScheduledReset(ctx, r, now, j.zone()); err != nil {
				return fmt.Errorf("%s: %w", r.Game, err)
			}
			j.Log.Info("jobs: reset time or time zone changed, the running period stays", "game", r.Game, "every", r.Schedule.String(), "next", due.Next.Format(time.DateTime))
			continue
		}
		if !due.Due && r.LastSchedule == "" {
			// Written by collector 0.19 without the schedule: add it, so a
			// later change of the schedule is noticed.
			if err := j.Store.SetOption(ctx, stats.LastResetKey(r.Game), stats.LastResetValue(time.Unix(r.Last, 0), r.Schedule)); err != nil {
				return fmt.Errorf("%s: %w", r.Game, err)
			}
		}
		if !due.Due || slices.ContainsFunc(open, func(o stats.Job) bool { return o.Game == r.Game }) {
			continue
		}
		o := j.origin(stats.OriginScheduledResets, startup)
		o.From, o.To, o.Schedule, o.Inherited, o.Zone = due.From.Unix(), due.To.Unix(), r.Schedule.String(), r.Inherited, j.zone().String()
		id, err := j.Store.AddJob(ctx, stats.JobScheduledReset, r.Game, r.Mode, stats.JobBySchedule, o, now)
		if err != nil {
			return err
		}
		j.Log.Info("jobs: scheduled ranking reset queued", "job", id, "game", r.Game, "period", due.From.Format(time.DateOnly)+" - "+due.To.Format(time.DateOnly), "at_start", startup)
	}
	return nil
}

// lock takes the job lock (a no-op without Lock).
func (j *Jobs) lock(ctx context.Context) (func(), error) {
	if j.Lock == nil {
		return func() {}, nil
	}
	return j.Lock(ctx)
}

// MoveInactive moves the players without an event for hlstats_Options
// MinActivity days before now (and not online) to hlstats_PlayersInactive,
// under the job lock.
func (j *Jobs) MoveInactive(ctx context.Context, now time.Time) error {
	_, err := j.moveInactive(ctx, now, 0)
	return err
}

// moveInactive is MoveInactive with its result for the job row; for a
// queued job (id not 0) it writes the progress into the row, so the
// command that queued it can show it.
func (j *Jobs) moveInactive(ctx context.Context, now time.Time, id int64) (string, error) {
	release, err := j.lock(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	cutoff, days, warn, err := j.Store.InactiveCutoff(ctx, now)
	if err != nil {
		return "", err
	}
	if warn != "" {
		j.Log.Warn("jobs: " + warn)
	}
	var progress func(int64)
	if id != 0 {
		total, err := j.Store.InactiveCount(ctx, cutoff)
		if err != nil {
			return "", err
		}
		var last time.Time
		progress = func(moved int64) {
			if time.Since(last) < ProgressEvery {
				return
			}
			last = time.Now()
			if err := j.Store.JobProgress(ctx, id, InactiveProgress(moved, total)); err != nil {
				j.Log.Warn("jobs: storing the progress failed", "job", id, "err", err)
			}
		}
	}
	n, err := j.Store.MoveInactiveProgress(ctx, cutoff, progress)
	if n > 0 || err != nil {
		j.Log.Info("jobs: inactive players moved to hlstats_PlayersInactive", "players", n, "days", days)
	}
	return InactiveResult(n, days), err
}

// ProgressEvery is how often a long job reports its progress.
const ProgressEvery = 5 * time.Second

// InactiveProgress is the progress text of a move of inactive players.
func InactiveProgress(moved, total int64) string {
	return fmt.Sprintf("%d of %d players moved so far", moved, total)
}

// InactiveResult is the result text of a move of inactive players.
func InactiveResult(moved int64, days int) string {
	return fmt.Sprintf("%d players moved to hlstats_PlayersInactive (no event for %d days, not online)", moved, days)
}

// do runs a claimed job and stores its result.
func (j *Jobs) do(ctx context.Context, job stats.Job) {
	if job.Type == stats.JobInactive {
		res, err := j.moveInactive(ctx, j.now(), job.ID)
		if err != nil {
			j.Log.Error("jobs: moving inactive players failed", "job", job.ID, "err", err)
			j.finish(ctx, job, stats.JobFailed, err.Error())
			return
		}
		j.finish(ctx, job, stats.JobDone, res)
		return
	}
	if job.Type == stats.JobAwards {
		res, err := j.decideAwards(ctx, job.Mode)
		if err != nil {
			j.Log.Error("jobs: daily awards failed", "job", job.ID, "day", job.Mode, "err", err)
			j.finish(ctx, job, stats.JobFailed, err.Error())
			return
		}
		j.finish(ctx, job, stats.JobDone, res)
		return
	}
	if job.Type == stats.JobReset || job.Type == stats.JobScheduledReset {
		// Templates are not offered for a reset (an older web did).
		if err := j.Store.CheckNotTemplate(ctx, job.Game); err != nil {
			j.Log.Warn("jobs: ranking reset refused", "job", job.ID, "game", job.Game, "err", err)
			j.finish(ctx, job, stats.JobFailed, err.Error())
			return
		}
	}
	if job.Type == stats.JobScheduledReset {
		res, err := j.scheduledReset(ctx, job.Game)
		if err != nil {
			j.Log.Error("jobs: scheduled ranking reset failed", "job", job.ID, "game", job.Game, "err", err)
			msg := err.Error()
			if len(res.Tables) > 0 {
				msg += "; done before: " + stats.FormatCounts(res.Tables)
			}
			j.finish(ctx, job, stats.JobFailed, msg)
			return
		}
		text := ScheduledText(res)
		j.Log.Info("jobs: scheduled ranking reset", "job", job.ID, "game", job.Game, "result", text)
		j.finish(ctx, job, stats.JobDone, text)
		return
	}
	if job.Type != stats.JobReset {
		j.finish(ctx, job, stats.JobFailed, fmt.Sprintf("unknown job type %q", job.Type))
		return
	}
	res, err := j.reset(ctx, job.Game, job.Mode)
	if err != nil {
		j.Log.Error("jobs: ranking reset failed", "job", job.ID, "game", job.Game, "mode", job.Mode, "err", err)
		msg := err.Error()
		if len(res) > 0 {
			msg += "; done before: " + stats.FormatCounts(res)
		}
		j.finish(ctx, job, stats.JobFailed, msg)
		return
	}
	j.Log.Info("jobs: ranking reset", "job", job.ID, "game", job.Game, "mode", job.Mode, "by", job.RequestedBy, "rows", stats.FormatCounts(res))
	j.finish(ctx, job, stats.JobDone, stats.FormatCounts(res))
}

// decideAwards decides the awards of the day mode (YYYY-MM-DD) under the
// job lock.
func (j *Jobs) decideAwards(ctx context.Context, mode string) (string, error) {
	day, err := j.awardsDay(mode)
	if err != nil {
		return "", err
	}
	return decideAwards(ctx, j.Store, day, j.Lock, j.Log)
}

// ScheduledText is the result of a scheduled reset for the job row and
// the CLI.
func ScheduledText(r stats.ScheduledResult) string {
	switch {
	case r.Started:
		return "schedule started: the first period ends " + r.Next.Format(time.DateTime)
	case r.Moved:
		return "not due: the reset time or time zone changed, the running period stays and ends " + r.Next.Format(time.DateTime)
	case !r.Due:
		return "not due: the period ends " + r.Next.Format(time.DateTime)
	}
	names := make([]string, len(r.Top))
	for i, e := range r.Top {
		names[i] = fmt.Sprintf("%d. %s", e.Place, e.Name)
	}
	top := "nobody played"
	if len(names) > 0 {
		top = strings.Join(names, ", ")
	}
	return fmt.Sprintf("period %s - %s, hall of fame: %s; reset: %s", r.From.Format(time.DateOnly), r.To.AddDate(0, 0, -1).Format(time.DateOnly), top, stats.FormatCounts(r.Tables))
}

// scheduledReset does the due scheduled reset of game with its servers
// paused, under the job lock.
func (j *Jobs) scheduledReset(ctx context.Context, game string) (stats.ScheduledResult, error) {
	r, err := j.Store.ScheduledRanking(ctx, game)
	if err != nil {
		return stats.ScheduledResult{}, err
	}
	var res stats.ScheduledResult
	_, err = j.paused(ctx, game, r.Mode, func() ([]stats.TableCount, error) {
		var err error
		res, err = j.Store.ScheduledReset(ctx, r, j.now(), j.zone())
		return res.Tables, err
	})
	return res, err
}

// reset pauses the servers of game, resets the ranking and lets them go on.
func (j *Jobs) reset(ctx context.Context, game, mode string) ([]stats.TableCount, error) {
	return j.paused(ctx, game, mode, func() ([]stats.TableCount, error) { return j.Store.Reset(ctx, game, mode) })
}

// paused runs do under the job lock while the servers of game are paused,
// and clears their live view afterwards.
func (j *Jobs) paused(ctx context.Context, game, mode string, do func() ([]stats.TableCount, error)) ([]stats.TableCount, error) {
	mode, err := stats.ResetMode(mode)
	if err != nil {
		return nil, err
	}
	if _, err := j.Store.ResetCounts(ctx, game, mode); err != nil { // checks the game before pausing
		return nil, err
	}
	release, err := j.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	resume := func() {}
	if j.Hub != nil {
		var err error
		var servers []int64
		resume, servers, err = j.Hub.PauseGame(pctx, game)
		defer resume()
		if err != nil {
			return nil, err
		}
		defer func() {
			for _, id := range servers {
				if err := j.Store.ResetLive(context.WithoutCancel(ctx), id); err != nil {
					j.Log.Warn("jobs: clearing the live view failed", "server_id", id, "err", err)
				}
			}
		}()
	}
	return do()
}

func (j *Jobs) finish(ctx context.Context, job stats.Job, status, result string) {
	if err := j.Store.FinishJob(context.WithoutCancel(ctx), job.ID, status, result, j.now()); err != nil {
		j.Log.Error("jobs: storing the result failed", "job", job.ID, "err", err)
	}
}
