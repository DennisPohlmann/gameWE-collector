// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// The collector queues its daily jobs as rows of hlstats_gamewe_jobs
// (requested by "schedule"), so they show up in gamewe jobs and the web
// admin, and writes when they run next into hlstats_Options; a restart
// does not queue the awards of a day again.
func TestIntegrationDailyJobsAndPlan(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// css is a ranking (it has a server), not a template.
	if _, err := db.Exec(`INSERT INTO hlstats_Servers (address, port, name, game) VALUES ('198.51.100.70', 27015, 'css', 'css')`); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{stats.ScheduleKey("css"): "month", "MinActivity": "28"} {
		if err := store.SetOption(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, loc)
	newJobs := func() *Jobs {
		return &Jobs{Store: store, Log: strictLog(t), Zone: loc, ResetClock: "00:00", Now: func() time.Time { return now },
			Awards:   AwardsConfig{Enabled: true, At: Schedule{Loc: loc, Minute: 5}},
			Inactive: InactiveConfig{Enabled: true, At: Schedule{Loc: loc, Hour: 1}}}
	}
	jobs := newJobs()
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := store.RecentJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]stats.Job{}
	for _, j := range all {
		got[j.Type] = j
	}
	if len(all) != 2 {
		t.Fatalf("%d jobs, want the awards and the move of inactive players: %+v", len(all), all)
	}
	if a := got[stats.JobAwards]; a.Status != stats.JobDone || a.Mode != "2026-10-26" || a.RequestedBy != stats.JobBySchedule || a.Game != "" {
		t.Errorf("awards job: %+v", a)
	}
	if i := got[stats.JobInactive]; i.Status != stats.JobDone || i.RequestedBy != stats.JobBySchedule {
		t.Errorf("inactive job: %+v", i)
	}

	option := func(key string) string {
		t.Helper()
		var v string
		if err := db.QueryRow(db.Q(`SELECT value FROM hlstats_Options WHERE keyname = ?`), key).Scan(&v); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return v
	}
	var p stats.JobPlan
	if err := json.Unmarshal([]byte(option(stats.NextAwardsKey)), &p); err != nil ||
		p != (stats.JobPlan{Enabled: true, Time: "00:05", Zone: "Europe/Berlin", Next: time.Date(2026, 10, 28, 0, 5, 0, 0, loc).Unix()}) {
		t.Errorf("awards plan %+v, %v", p, err)
	}
	if err := json.Unmarshal([]byte(option(stats.NextInactiveKey)), &p); err != nil || p.Next != time.Date(2026, 10, 28, 1, 0, 0, 0, loc).Unix() {
		t.Errorf("inactive plan %+v, %v", p, err)
	}
	if v := option(stats.NextResetKey("css")); v != strconv.FormatInt(time.Date(2026, 11, 1, 0, 0, 0, 0, loc).Unix(), 10) {
		t.Errorf("next reset of css %s", v)
	}

	// A restart the same day: the awards of the 26th exist (a winner in the
	// history), nothing is queued.
	if _, err := db.Exec(`INSERT INTO hlstats_Awards_History (award_id, winner_id, date, winner_count) VALUES (1, 1, '2026-10-26', 1)`); err != nil {
		t.Fatal(err)
	}
	jobs = newJobs()
	jobs.lastInactive = now
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_gamewe_jobs`); n != 2 {
		t.Errorf("%d jobs after a restart, want 2", n)
	}
}
