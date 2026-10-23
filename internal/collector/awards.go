// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"log/slog"
	"time"

	"gamewe-collector/internal/stats"
)

// Awarder decides the daily awards (*stats.Store).
type Awarder interface {
	DailyAwards(ctx context.Context, day time.Time, game string) ([]stats.AwardResult, error)
	AwardsDone(ctx context.Context, day time.Time) (bool, error)
}

// Schedule is when the daily awards are decided: every day at hour:minute
// in Loc, for the previous calendar day in Loc.
type Schedule struct {
	Loc          *time.Location
	Hour, Minute int
}

// runAt returns the run of the calendar day of t (which may be in the future).
func (s Schedule) runAt(t time.Time) time.Time {
	y, m, d := t.In(s.Loc).Date()
	return time.Date(y, m, d, s.Hour, s.Minute, 0, 0, s.Loc)
}

// Last returns the latest run not after now and the day it decides.
func (s Schedule) Last(now time.Time) (run, day time.Time) {
	run = s.runAt(now)
	if run.After(now) {
		y, m, d := now.In(s.Loc).Date()
		run = s.runAt(time.Date(y, m, d-1, 12, 0, 0, 0, s.Loc))
	}
	y, m, d := run.Date()
	return run, time.Date(y, m, d-1, 0, 0, 0, 0, s.Loc)
}

// Next returns the first run after now.
func (s Schedule) Next(now time.Time) time.Time {
	run, _ := s.Last(now)
	y, m, d := run.Date()
	return s.runAt(time.Date(y, m, d+1, 12, 0, 0, 0, s.Loc))
}

// RunDailyAwards decides the awards of the previous day at every run of
// the schedule, and at start if the day of the latest run has no awards
// yet, until ctx is done (features.daily_awards in the config file).
func RunDailyAwards(ctx context.Context, a Awarder, s Schedule, log *slog.Logger) {
	for {
		now := time.Now()
		_, day := s.Last(now)
		DecideAwards(ctx, a, day, log)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(s.Next(now))):
		}
	}
}

// DecideAwards decides the awards of day unless the day has some already.
func DecideAwards(ctx context.Context, a Awarder, day time.Time, log *slog.Logger) {
	done, err := a.AwardsDone(ctx, day)
	if err != nil {
		log.Error("daily awards", "day", stats.AwardDate(day), "err", err)
		return
	}
	if done {
		return
	}
	res, err := a.DailyAwards(ctx, day, "")
	if err != nil {
		log.Error("daily awards", "day", stats.AwardDate(day), "err", err)
		return
	}
	won := 0
	for _, r := range res {
		if r.WinnerID > 0 {
			won++
		}
	}
	log.Info("daily awards decided", "day", stats.AwardDate(day), "awards", len(res), "with_winner", won)
}
