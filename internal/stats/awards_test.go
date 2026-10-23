// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"testing"
	"time"
)

func TestDayBounds(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		day   time.Time
		date  string
		start string
		hours int64
	}{
		{time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), "2026-10-21", "2026-10-21T00:00:00Z", 24},
		{time.Date(2026, 10, 21, 0, 0, 0, 0, berlin), "2026-10-21", "2026-10-20T22:00:00Z", 24},
		{time.Date(2026, 10, 25, 0, 0, 0, 0, berlin), "2026-10-25", "2026-10-24T22:00:00Z", 25}, // summer time ends
		{time.Date(2027, 3, 28, 0, 0, 0, 0, berlin), "2027-03-28", "2027-03-27T23:00:00Z", 23},  // summer time starts
	} {
		from, to := dayBounds(c.day)
		if AwardDate(c.day) != c.date || time.Unix(from, 0).UTC().Format(time.RFC3339) != c.start || (to-from)/3600 != c.hours {
			t.Errorf("%v: date %s, from %s, %d hours", c.day, AwardDate(c.day), time.Unix(from, 0).UTC().Format(time.RFC3339), (to-from)/3600)
		}
	}
}
