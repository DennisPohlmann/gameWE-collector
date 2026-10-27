// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/stats"
)

// A monthly schedule: set up in October it records October as its first
// period; on 1 November the poll queues a scheduled reset, the job stores
// the best of css in the hall of fame and resets css only; a second poll
// does nothing; hidden players and bots never enter the hall of fame.
func TestIntegrationScheduledReset(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Europe/Berlin")
	if loc == nil {
		loc = time.UTC
	}
	exec(t, db, `UPDATE hlstats_Players SET skill = 1500 WHERE game = 'css' AND playerId = (SELECT MIN(playerId) FROM hlstats_Players WHERE game = 'css')`)
	if err := store.SetOption(ctx, stats.ScheduleKey("css"), "month"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, loc)
	jobs := &Jobs{Store: store, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Zone: loc, Now: func() time.Time { return now }}
	if err := jobs.QueueScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	r, err := store.ScheduledRanking(ctx, "css")
	if err != nil || r.Last != time.Date(2026, 10, 1, 0, 0, 0, 0, loc).Unix() || r.Mode != stats.ResetPlayerStats {
		t.Fatalf("schedule after start: %+v, %v", r, err)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_jobs`) != 0 {
		t.Fatal("a job queued before the period ended")
	}
	top, err := store.HallTop(ctx, "css")
	if err != nil || len(top) != 2 || top[0].Skill != 1500 {
		t.Fatalf("top: %+v, %v", top, err)
	}

	now = time.Date(2026, 11, 1, 0, 0, 30, 0, loc)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := store.RecentJobs(ctx, 1)
	if err != nil || len(job) != 1 || job[0].Type != stats.JobScheduledReset || job[0].Status != stats.JobDone {
		t.Fatalf("job: %+v, %v", job, err)
	}
	if !strings.Contains(job[0].Result, "period 2026-10-01 - 2026-10-31") || !strings.Contains(job[0].Result, "1. "+top[0].Name) {
		t.Errorf("result: %s", job[0].Result)
	}
	if n := q(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_hall_of_fame WHERE game = 'css' AND period_start = '2026-10-01' AND period_end = '2026-10-31' AND unit = 'month' AND every = 1`); n != 2 {
		t.Errorf("hall of fame rows: %d, want 2 (two players)", n)
	}
	if q(t, db, `SELECT skill FROM hlstats_gamewe_hall_of_fame WHERE game = 'css' AND place = 1`) != 1500 {
		t.Error("place 1 is not the best player")
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css' AND kills > 0`) != 0 {
		t.Error("css not reset")
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'tf' AND kills > 0`) == 0 {
		t.Error("tf was reset too")
	}
	rows, err := store.HallOfFame(ctx, "css")
	if err != nil || len(rows) != 2 || rows[0].PeriodStart != "2026-10-01" || rows[0].PeriodEnd != "2026-10-31" {
		t.Fatalf("HallOfFame: %+v, %v", rows, err)
	}

	// Nothing due any more in November.
	now = now.Add(2 * time.Minute)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_jobs`) != 1 {
		t.Error("a second job for the same period")
	}

	// A hidden player does not count.
	exec(t, db, `UPDATE hlstats_Players SET kills = 5, skill = 3000, hideranking = 1 WHERE game = 'css' AND playerId = (SELECT MIN(playerId) FROM hlstats_Players WHERE game = 'css')`)
	top, err = store.HallTop(ctx, "css")
	if err != nil || len(top) != 0 {
		t.Errorf("top with a hidden player: %+v, %v", top, err)
	}
	hub.Close(ctx)
}

// The schedule for all rankings: css follows it (every month on the 15th),
// tf has "off" and is never reset; a changed schedule starts again with the
// period now running instead of resetting at once.
func TestIntegrationScheduledResetAll(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	ctx := context.Background()
	loc := time.UTC
	set := func(k, v string) {
		t.Helper()
		if err := store.SetOption(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	set(stats.GlobalScheduleKey, "month@15")
	set(stats.GlobalModeKey, stats.ResetPlayerStats)
	set(stats.ScheduleKey("tf"), stats.ScheduleOff)
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, loc)
	jobs := &Jobs{Store: store, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Zone: loc, Now: func() time.Time { return now }}
	if err := jobs.QueueScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	r, err := store.ScheduledRanking(ctx, "css")
	if err != nil || !r.Inherited || r.Last != time.Date(2026, 10, 15, 0, 0, 0, 0, loc).Unix() || r.LastSchedule != "month@15" {
		t.Fatalf("css after start: %+v, %v", r, err)
	}
	if r, _ := store.ScheduledRanking(ctx, "tf"); r.Schedule.On() || r.Inherited || r.Last != 0 {
		t.Fatalf("tf: %+v", r)
	}
	if following, err := store.InheritingRankings(ctx); err != nil || !slices.Contains(following, "css") || slices.Contains(following, "tf") {
		t.Fatalf("inheriting: %v, %v", following, err)
	}

	now = time.Date(2026, 11, 15, 0, 0, 30, 0, loc)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := store.RecentJobs(ctx, 5)
	if err != nil || len(job) != 1 || job[0].Game != "css" || job[0].Status != stats.JobDone {
		t.Fatalf("jobs: %+v, %v", job, err)
	}
	if n := q(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_hall_of_fame WHERE game = 'css' AND period_start = '2026-10-15' AND period_end = '2026-11-14'`); n != 2 {
		t.Errorf("hall of fame rows of css: %d, want 2", n)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'tf' AND kills > 0`) == 0 {
		t.Error("tf was reset although its schedule is off")
	}

	// A value of collector 0.19 (no schedule) gets the schedule added.
	exec(t, db, `UPDATE hlstats_Options SET value = ? WHERE keyname = ?`, fmt.Sprint(time.Date(2026, 11, 15, 0, 0, 0, 0, loc).Unix()), stats.LastResetKey("css"))
	now = time.Date(2026, 11, 16, 12, 0, 0, 0, loc)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err := store.ScheduledRanking(ctx, "css"); err != nil || r.LastSchedule != "month@15" {
		t.Fatalf("css after the upgrade: %+v, %v", r, err)
	}

	// Weekly from now on: started with the week now running, no reset.
	set(stats.GlobalScheduleKey, "week")
	now = time.Date(2026, 11, 18, 12, 0, 0, 0, loc) // a Wednesday
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_jobs`) != 1 {
		t.Error("the changed schedule queued a reset")
	}
	if r, err := store.ScheduledRanking(ctx, "css"); err != nil || r.Last != time.Date(2026, 11, 16, 0, 0, 0, 0, loc).Unix() || r.LastSchedule != "week" {
		t.Errorf("css after the change: %+v, %v", r, err)
	}

	// Off (the web admin deletes the schedule and keeps it in
	// gamewe_reset_paused): the running period is forgotten, so turning the
	// same schedule on again weeks later starts anew instead of resetting
	// for the time it was off.
	exec(t, db, `DELETE FROM hlstats_Options WHERE keyname = ?`, stats.GlobalScheduleKey)
	set("gamewe_reset_paused", "week")
	now = time.Date(2026, 11, 19, 12, 0, 0, 0, loc)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err := store.ScheduledRanking(ctx, "css"); err != nil || r.Schedule.On() || r.Last != 0 {
		t.Fatalf("css while off: %+v, %v", r, err)
	}
	set(stats.GlobalScheduleKey, "week")
	now = time.Date(2026, 12, 9, 12, 0, 0, 0, loc) // three weeks later, a Wednesday
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if q(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_jobs`) != 1 {
		t.Error("turning the schedule on again queued a reset")
	}
	if r, err := store.ScheduledRanking(ctx, "css"); err != nil || r.Last != time.Date(2026, 12, 7, 0, 0, 0, 0, loc).Unix() {
		t.Errorf("css after turning it on again: %+v, %v", r, err)
	}
	hub.Close(ctx)
}

func exec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(db.Q(q), args...); err != nil {
		t.Fatal(err)
	}
}
