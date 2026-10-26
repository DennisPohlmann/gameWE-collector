// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"testing"
	"time"
)

func TestParseResetSchedule(t *testing.T) {
	for in, want := range map[string]ResetSchedule{
		"":          {},
		"off":       {},
		"month":     {UnitMonth, 1},
		"2 months":  {UnitMonth, 2},
		"quarter":   {UnitMonth, 3},
		"2 quarter": {UnitMonth, 6},
		"Week":      {UnitWeek, 1},
		" 3 day ":   {UnitDay, 3},
		"year":      {UnitYear, 1},
	} {
		got, err := ParseResetSchedule(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"0 month", "-1 day", "121 day", "fortnight", "1 2 month", "41 quarter"} {
		if _, err := ParseResetSchedule(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
	if s := (ResetSchedule{UnitMonth, 2}).String(); s != "2 month" {
		t.Errorf("String: %q", s)
	}
}

func TestPeriods(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation(time.DateTime, s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		sch         ResetSchedule
		now         string
		start, next string
	}{
		{ResetSchedule{UnitMonth, 1}, "2026-10-26 14:00:00", "2026-10-01 00:00:00", "2026-11-01 00:00:00"},
		{ResetSchedule{UnitMonth, 1}, "2026-12-31 23:59:59", "2026-12-01 00:00:00", "2027-01-01 00:00:00"},
		{ResetSchedule{UnitMonth, 2}, "2026-10-26 14:00:00", "2026-09-01 00:00:00", "2026-11-01 00:00:00"},
		{ResetSchedule{UnitMonth, 3}, "2026-10-26 14:00:00", "2026-10-01 00:00:00", "2027-01-01 00:00:00"},
		{ResetSchedule{UnitMonth, 3}, "2026-09-30 23:00:00", "2026-07-01 00:00:00", "2026-10-01 00:00:00"},
		{ResetSchedule{UnitYear, 1}, "2026-10-26 14:00:00", "2026-01-01 00:00:00", "2027-01-01 00:00:00"},
		{ResetSchedule{UnitYear, 2}, "2027-03-01 00:00:00", "2026-01-01 00:00:00", "2028-01-01 00:00:00"},
		{ResetSchedule{UnitWeek, 1}, "2026-10-26 14:00:00", "2026-10-26 00:00:00", "2026-11-02 00:00:00"}, // a Monday
		{ResetSchedule{UnitWeek, 1}, "2026-11-01 23:59:00", "2026-10-26 00:00:00", "2026-11-02 00:00:00"}, // the Sunday
		{ResetSchedule{UnitDay, 1}, "2026-10-25 12:00:00", "2026-10-25 00:00:00", "2026-10-26 00:00:00"}, // DST ends
		{ResetSchedule{UnitDay, 1}, "2026-03-29 12:00:00", "2026-03-29 00:00:00", "2026-03-30 00:00:00"}, // DST starts
	} {
		now := at(c.now)
		if got := c.sch.PeriodStart(now, loc); !got.Equal(at(c.start)) {
			t.Errorf("%s at %s: start %s, want %s", c.sch, c.now, got, c.start)
		}
		if got := c.sch.Next(now, loc); !got.Equal(at(c.next)) {
			t.Errorf("%s at %s: next %s, want %s", c.sch, c.now, got, c.next)
		}
	}
	// Every N days and weeks: consecutive periods have the same length and
	// one ends where the next starts.
	for _, sch := range []ResetSchedule{{UnitDay, 3}, {UnitWeek, 2}, {UnitMonth, 5}} {
		t0 := at("2026-01-01 12:00:00")
		for range 40 {
			next := sch.Next(t0, loc)
			if !sch.PeriodStart(next, loc).Equal(next) || !sch.PeriodStart(next.Add(-time.Second), loc).Equal(sch.PeriodStart(t0, loc)) {
				t.Fatalf("%s: periods do not line up at %s", sch, next)
			}
			t0 = next.Add(time.Hour)
		}
	}
}
