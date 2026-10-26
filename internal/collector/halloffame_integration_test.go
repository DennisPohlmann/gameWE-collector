// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"io"
	"log/slog"
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

func exec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(db.Q(q), args...); err != nil {
		t.Fatal(err)
	}
}
