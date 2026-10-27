// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gamewe-collector/internal/stats"
)

// Awarder decides the daily awards (*stats.Store).
type Awarder interface {
	DailyAwards(ctx context.Context, day time.Time, game string) ([]stats.AwardResult, error)
	AwardsDone(ctx context.Context, day time.Time) (bool, error)
}

// Schedule is when a daily job runs (the daily awards, the move of
// inactive players): every day at hour:minute in Loc; the daily awards
// decide the previous calendar day in Loc.
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

// Clock is the time of day as HH:MM.
func (s Schedule) Clock() string { return fmt.Sprintf("%02d:%02d", s.Hour, s.Minute) }

// DecideAwards decides the awards of day unless the day has some already,
// under the job lock (nil: none), and logs the result.
func DecideAwards(ctx context.Context, a Awarder, day time.Time, lock func(context.Context) (func(), error), log *slog.Logger) {
	if _, err := decideAwards(ctx, a, day, lock, log); err != nil {
		log.Error("daily awards", "day", stats.AwardDate(day), "err", err)
	}
}

// decideAwards is DecideAwards with its result text for the job row.
func decideAwards(ctx context.Context, a Awarder, day time.Time, lock func(context.Context) (func(), error), log *slog.Logger) (string, error) {
	if lock != nil {
		release, err := lock(ctx)
		if err != nil {
			return "", err
		}
		defer release()
	}
	done, err := a.AwardsDone(ctx, day)
	if err != nil {
		return "", err
	}
	if done {
		return "awards of " + stats.AwardDate(day) + " decided already", nil
	}
	res, err := a.DailyAwards(ctx, day, "")
	if err != nil {
		return "", err
	}
	won := 0
	for _, r := range res {
		if r.WinnerID > 0 {
			won++
		}
	}
	log.Info("daily awards decided", "day", stats.AwardDate(day), "awards", len(res), "with_winner", won)
	return AwardsResult(day, len(res), won), nil
}

// AwardsResult is the result text of the daily awards of day.
func AwardsResult(day time.Time, awards, won int) string {
	return fmt.Sprintf("awards of %s decided: %d awards, %d with a winner", stats.AwardDate(day), awards, won)
}
