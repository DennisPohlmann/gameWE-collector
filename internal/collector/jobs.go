// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gamewe-collector/internal/stats"
)

// JobPoll is how often the collector looks for queued jobs.
const JobPoll = 10 * time.Second

// Jobs runs the jobs of hlstats_gamewe_jobs (ranking resets queued by the
// web admin or `gamewe reset`) in the running collector, which pauses the
// game's servers for it, writes the heartbeat and moves inactive players
// (features.inactive_players; one after the other, never during a reset).
type Jobs struct {
	Store    *stats.Store
	Hub      *Hub
	Log      *slog.Logger
	Now      func() time.Time // tests
	Inactive InactiveConfig
	// Version is written to hlstats_Options with the first heartbeat
	// (stats.VersionKey); empty: not written (tests).
	Version string
	// Lock takes the job lock of the database (database.JobLock) for a
	// reset and a move of inactive players; nil: none (tests).
	Lock func(ctx context.Context) (release func(), err error)

	lastBeat     time.Time
	lastPrune    time.Time
	lastInactive time.Time
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
// every JobPoll until ctx is done.
func (j *Jobs) Run(ctx context.Context) {
	j.lastInactive = j.now() // the first move at the next daily time, not at start
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

// Poll writes the heartbeat when due, removes old jobs and runs the
// pending ones, oldest first. A second job for a game that has one pending
// or running already is refused.
func (j *Jobs) Poll(ctx context.Context) error {
	now := j.now()
	if now.Sub(j.lastBeat) >= stats.HeartbeatInterval {
		if j.lastBeat.IsZero() && j.Version != "" {
			if err := j.Store.SetVersion(ctx, j.Version); err != nil {
				return fmt.Errorf("version: %w", err)
			}
		}
		if err := j.Store.Heartbeat(ctx, now); err != nil {
			return fmt.Errorf("heartbeat: %w", err)
		}
		j.lastBeat = now
	}
	if now.Sub(j.lastPrune) >= time.Hour {
		if err := j.Store.DeleteOldJobs(ctx, now.Add(-stats.JobKeep)); err != nil {
			return fmt.Errorf("delete old jobs: %w", err)
		}
		j.lastPrune = now
	}
	if j.inactiveDue(now) {
		if err := j.MoveInactive(ctx, now); err != nil && ctx.Err() == nil {
			j.Log.Error("jobs: moving inactive players failed", "err", err)
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
		if job.Type == stats.JobInactive { // all games; a second one is a no-op
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
	_, err := j.moveInactive(ctx, now)
	return err
}

// moveInactive is MoveInactive with its result for the job row.
func (j *Jobs) moveInactive(ctx context.Context, now time.Time) (string, error) {
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
	n, err := j.Store.MoveInactive(ctx, cutoff)
	if n > 0 || err != nil {
		j.Log.Info("jobs: inactive players moved to hlstats_PlayersInactive", "players", n, "days", days)
	}
	return InactiveResult(n, days), err
}

// InactiveResult is the result text of a move of inactive players.
func InactiveResult(moved int64, days int) string {
	return fmt.Sprintf("%d players moved to hlstats_PlayersInactive (no event for %d days, not online)", moved, days)
}

// do runs a claimed job and stores its result.
func (j *Jobs) do(ctx context.Context, job stats.Job) {
	if job.Type == stats.JobInactive {
		res, err := j.moveInactive(ctx, j.now())
		if err != nil {
			j.Log.Error("jobs: moving inactive players failed", "job", job.ID, "err", err)
			j.finish(ctx, job, stats.JobFailed, err.Error())
			return
		}
		j.finish(ctx, job, stats.JobDone, res)
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

// reset pauses the servers of game, resets the ranking and lets them go on.
func (j *Jobs) reset(ctx context.Context, game, mode string) ([]stats.TableCount, error) {
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
	return j.Store.Reset(ctx, game, mode)
}

func (j *Jobs) finish(ctx context.Context, job stats.Job, status, result string) {
	if err := j.Store.FinishJob(context.WithoutCancel(ctx), job.ID, status, result, j.now()); err != nil {
		j.Log.Error("jobs: storing the result failed", "job", job.ID, "err", err)
	}
}
