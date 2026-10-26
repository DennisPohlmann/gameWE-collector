// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"testing"
	"time"
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
