// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"gamewe-collector/internal/stats"
)

// The daily move of inactive players runs exactly once per calendar day,
// also on the days of a DST change, polled as the jobs are (JobPoll).
func TestInactiveDailyRun(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name         string
		hour, minute int
		from         time.Time
	}{
		{"spring forward 01:00", 1, 0, time.Date(2026, 3, 27, 12, 0, 0, 0, berlin)},
		{"spring forward 02:30 (skipped hour)", 2, 30, time.Date(2026, 3, 27, 12, 0, 0, 0, berlin)},
		{"fall back 02:30 (repeated hour)", 2, 30, time.Date(2026, 10, 23, 12, 0, 0, 0, berlin)},
		{"fall back 01:00", 1, 0, time.Date(2026, 10, 23, 12, 0, 0, 0, berlin)},
	} {
		j := &Jobs{Inactive: InactiveConfig{Enabled: true, At: Schedule{Loc: berlin, Hour: c.hour, Minute: c.minute}}}
		j.lastInactive = c.from // Run starts here: no move at start
		runs := map[string]int{}
		for now := c.from; now.Before(c.from.Add(5 * 24 * time.Hour)); now = now.Add(JobPoll) {
			if j.inactiveDue(now) {
				runs[now.In(berlin).Format("2006-01-02")]++
			}
		}
		if len(runs) != 5 {
			t.Errorf("%s: runs on %d days, want 5: %v", c.name, len(runs), runs)
		}
		for day, n := range runs {
			if n != 1 {
				t.Errorf("%s: %d runs on %s", c.name, n, day)
			}
		}
	}
}

func TestInactiveDisabled(t *testing.T) {
	if (&Jobs{}).inactiveDue(time.Now()) { // no zone: never looked at
		t.Error("due while disabled")
	}
}

// The daily awards are due at the first poll (the latest run's day may
// lack its awards) and then once per calendar day, for the day before.
func TestAwardsDailyRun(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	j := &Jobs{Awards: AwardsConfig{Enabled: true, At: Schedule{Loc: berlin, Hour: 0, Minute: 5}}}
	from := time.Date(2026, 10, 23, 12, 0, 0, 0, berlin)
	days := map[string]int{}
	first := true
	for now := from; now.Before(from.Add(5 * 24 * time.Hour)); now = now.Add(JobPoll) {
		day, ok := j.awardsDue(now)
		if !ok {
			continue
		}
		if first && !now.Equal(from) {
			t.Errorf("first run at %v, want at the first poll", now)
		}
		first = false
		days[stats.AwardDate(day)]++
		if want := stats.AwardDate(now.In(berlin).AddDate(0, 0, -1)); stats.AwardDate(day) != want {
			t.Errorf("run at %v decides %s, want %s", now, stats.AwardDate(day), want)
		}
	}
	// 22 (at start) and 23-27 (at 00:05 of 24-28, across the DST change).
	if len(days) != 6 {
		t.Errorf("awards for %d days, want 6: %v", len(days), days)
	}
	for d, n := range days {
		if n != 1 {
			t.Errorf("%d runs for %s", n, d)
		}
	}
	if _, ok := (&Jobs{}).awardsDue(time.Now()); ok {
		t.Error("due while disabled")
	}
}

// The plan the web admin shows: time, zone and the next run of the daily
// jobs (none while off), the time of day of the scheduled resets.
func TestPlan(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, berlin)
	j := &Jobs{Zone: berlin, ResetClock: "00:00",
		Awards:   AwardsConfig{Enabled: true, At: Schedule{Loc: berlin, Hour: 0, Minute: 5}},
		Inactive: InactiveConfig{Enabled: false, At: Schedule{Loc: time.UTC, Hour: 1}}}
	got := j.Plan(now)
	next := time.Date(2026, 10, 28, 0, 5, 0, 0, berlin).Unix()
	want := map[string]string{
		stats.NextAwardsKey:   `{"enabled":true,"time":"00:05","zone":"Europe/Berlin","next":` + strconv.FormatInt(next, 10) + `}`,
		stats.NextInactiveKey: `{"enabled":false,"time":"01:00","zone":"UTC"}`,
		stats.NextResetsKey:   `{"enabled":true,"time":"00:00","zone":"Europe/Berlin"}`,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
		if len(got[k]) > 255 { // hlstats_Options.value
			t.Errorf("%s longer than 255", k)
		}
	}
	var p stats.JobPlan
	if err := json.Unmarshal([]byte(got[stats.NextAwardsKey]), &p); err != nil || p.Next != next {
		t.Errorf("plan does not parse back: %+v, %v", p, err)
	}
}
