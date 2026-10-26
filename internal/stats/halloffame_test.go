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
		"month":     {Unit: UnitMonth, Every: 1},
		"2 months":  {Unit: UnitMonth, Every: 2},
		"quarter":   {Unit: UnitMonth, Every: 3},
		"2 quarter": {Unit: UnitMonth, Every: 6},
		"Week":      {Unit: UnitWeek, Every: 1},
		" 3 day ":   {Unit: UnitDay, Every: 3},
		"year":      {Unit: UnitYear, Every: 1},
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
	for in, want := range map[string]ResetSchedule{
		"month@15":   {Unit: UnitMonth, Every: 1, Day: 15},
		"2 months@1": {Unit: UnitMonth, Every: 2, Day: 1},
		"quarter@15": {Unit: UnitMonth, Every: 3, Day: 15},
		"week@3":     {Unit: UnitWeek, Every: 1, Day: 3},
		"2 week @ 7": {Unit: UnitWeek, Every: 2, Day: 7},
		"year@07-01": {Unit: UnitYear, Every: 1, Month: 7, Day: 1},
	} {
		got, err := ParseResetSchedule(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"month@29", "month@0", "day@2", "week@8", "year@13-01", "year@02-30", "year@7", "@5", "off@1", "month@x"} {
		if _, err := ParseResetSchedule(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
	for in, want := range map[string]string{"month": "month", "2 month": "2 month", "month@1": "month", "3 month@15": "3 month@15", "week@1": "week", "week@5": "week@5", "year@01-01": "year", "2 year@07-01": "2 year@07-01", "quarter": "3 month"} {
		got, err := ParseResetSchedule(in)
		if err != nil || got.String() != want {
			t.Errorf("String of %q: %q, %v; want %q", in, got.String(), err, want)
		}
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
		{ResetSchedule{Unit: UnitMonth, Every: 1}, "2026-10-26 14:00:00", "2026-10-01 00:00:00", "2026-11-01 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 1}, "2026-12-31 23:59:59", "2026-12-01 00:00:00", "2027-01-01 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 2}, "2026-10-26 14:00:00", "2026-09-01 00:00:00", "2026-11-01 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 3}, "2026-10-26 14:00:00", "2026-10-01 00:00:00", "2027-01-01 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 3}, "2026-09-30 23:00:00", "2026-07-01 00:00:00", "2026-10-01 00:00:00"},
		{ResetSchedule{Unit: UnitYear, Every: 1}, "2026-10-26 14:00:00", "2026-01-01 00:00:00", "2027-01-01 00:00:00"},
		{ResetSchedule{Unit: UnitYear, Every: 2}, "2027-03-01 00:00:00", "2026-01-01 00:00:00", "2028-01-01 00:00:00"},
		{ResetSchedule{Unit: UnitWeek, Every: 1}, "2026-10-26 14:00:00", "2026-10-26 00:00:00", "2026-11-02 00:00:00"}, // a Monday
		{ResetSchedule{Unit: UnitWeek, Every: 1}, "2026-11-01 23:59:00", "2026-10-26 00:00:00", "2026-11-02 00:00:00"}, // the Sunday
		{ResetSchedule{Unit: UnitDay, Every: 1}, "2026-10-25 12:00:00", "2026-10-25 00:00:00", "2026-10-26 00:00:00"},  // DST ends
		{ResetSchedule{Unit: UnitDay, Every: 1}, "2026-03-29 12:00:00", "2026-03-29 00:00:00", "2026-03-30 00:00:00"},  // DST starts
		// Anchors: the 15th, Wednesdays, 1 July.
		{ResetSchedule{Unit: UnitMonth, Every: 1, Day: 15}, "2026-10-26 14:00:00", "2026-10-15 00:00:00", "2026-11-15 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 1, Day: 15}, "2026-10-14 23:59:00", "2026-09-15 00:00:00", "2026-10-15 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 1, Day: 15}, "2027-01-03 10:00:00", "2026-12-15 00:00:00", "2027-01-15 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 3, Day: 15}, "2026-10-26 14:00:00", "2026-10-15 00:00:00", "2027-01-15 00:00:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 3, Day: 15}, "2026-10-10 14:00:00", "2026-07-15 00:00:00", "2026-10-15 00:00:00"},
		{ResetSchedule{Unit: UnitWeek, Every: 1, Day: 3}, "2026-10-26 14:00:00", "2026-10-21 00:00:00", "2026-10-28 00:00:00"}, // Monday after Wed 21
		{ResetSchedule{Unit: UnitWeek, Every: 1, Day: 3}, "2026-10-28 00:00:00", "2026-10-28 00:00:00", "2026-11-04 00:00:00"},
		{ResetSchedule{Unit: UnitYear, Every: 1, Month: 7, Day: 1}, "2026-10-26 14:00:00", "2026-07-01 00:00:00", "2027-07-01 00:00:00"},
		{ResetSchedule{Unit: UnitYear, Every: 1, Month: 7, Day: 1}, "2026-03-01 14:00:00", "2025-07-01 00:00:00", "2026-07-01 00:00:00"},
		// Clock: the reset at 05:30.
		{ResetSchedule{Unit: UnitMonth, Every: 1, Clock: 330}, "2026-11-01 05:00:00", "2026-10-01 05:30:00", "2026-11-01 05:30:00"},
		{ResetSchedule{Unit: UnitMonth, Every: 1, Clock: 330}, "2026-11-01 05:30:00", "2026-11-01 05:30:00", "2026-12-01 05:30:00"},
		{ResetSchedule{Unit: UnitDay, Every: 1, Clock: 330}, "2026-10-25 03:00:00", "2026-10-24 05:30:00", "2026-10-25 05:30:00"}, // DST ends
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
	for _, sch := range []ResetSchedule{{Unit: UnitDay, Every: 3}, {Unit: UnitWeek, Every: 2}, {Unit: UnitMonth, Every: 5}, {Unit: UnitMonth, Every: 2, Day: 15, Clock: 90}, {Unit: UnitWeek, Every: 3, Day: 6, Clock: 600}, {Unit: UnitYear, Every: 1, Month: 7, Day: 20}} {
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
