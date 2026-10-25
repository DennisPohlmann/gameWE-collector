// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"testing"
	"time"
)

func TestParseZone(t *testing.T) {
	for _, s := range []string{"", "server", "SERVER"} {
		if z, err := ParseZone(s); err != nil || !z.Server() {
			t.Errorf("ParseZone(%q) = %+v, %v; want server", s, z, err)
		}
	}
	for s, off := range map[string]int{"UTC": 0, "+01:00": 3600, "-05:30": -19800, "+14:00": 50400} {
		z, err := ParseZone(s)
		if err != nil || z.Server() {
			t.Fatalf("ParseZone(%q): %v", s, err)
		}
		if _, o := time.Unix(0, 0).In(z.Loc).Zone(); o != off {
			t.Errorf("%s: offset %d, want %d", s, o, off)
		}
	}
	if z, err := ParseZone("Europe/Berlin"); err != nil || z.Loc.String() != "Europe/Berlin" {
		t.Errorf("Europe/Berlin: %+v, %v", z, err)
	}
	for _, s := range []string{"Local", "Mars/Olympus", "+15:00", "+01:60", "1:00", "CEST"} {
		if _, err := ParseZone(s); err == nil {
			t.Errorf("ParseZone(%q): no error", s)
		}
	}
}

// The expected values are those of MariaDB 11.4 (SET time_zone =
// 'Europe/Berlin'; SELECT IFNULL(UNIX_TIMESTAMP(...), 0)).
func TestUnixLikeMariaDB(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"2024-01-15 12:00:00", 1705316400},   // winter, +01:00
		{"2024-07-15 12:00:00", 1721037600},   // summer, +02:00
		{"2024-03-31 01:59:59", 1711846799},   // last second before the gap
		{"2024-03-31 02:00:00", 1711846800},   // in the gap: first second after it
		{"2024-03-31 02:30:00", 1711846800},   // in the gap
		{"2024-03-31 03:00:00", 1711846800},   // first second of summer time
		{"2024-10-27 01:59:59", 1729987199},   // before the hour that exists twice
		{"2024-10-27 02:30:00", 1729989000},   // twice: the first (summer time)
		{"2024-10-27 03:00:00", 1729994400},   // winter time again
		{"1969-12-31 23:00:00", 0},            // before 1970
		{"0000-00-00 00:00:00", 0},            // zero date
		{"", 0},                               // NULL-like
		{"garbage", 0},                        //
		{"2200-01-01 00:00:00", 0},            // beyond timestamp_id
		{" 2024-01-15 12:00:00 ", 1705316400}, // spaces
	} {
		if got := Unix(c.in, berlin); got != c.want {
			t.Errorf("Unix(%q, Europe/Berlin) = %d, want %d", c.in, got, c.want)
		}
	}
	if got := Unix("2024-03-31 02:30:00", time.UTC); got != 1711852200 {
		t.Errorf("UTC: %d", got)
	}
	plus1 := time.FixedZone("+01:00", 3600)
	if got := Unix("2024-07-15 12:00:00", plus1); got != 1721041200 {
		t.Errorf("+01:00: %d", got)
	}
}

// Every second of a year in Europe/Berlin and America/New_York converts
// back to the same wall clock (outside the gaps), the earlier one for the
// doubled hour.
func TestUnixRoundTrip(t *testing.T) {
	for _, name := range []string{"Europe/Berlin", "America/New_York", "Australia/Lord_Howe"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		for u := start; u < start+366*86400; u += 997 {
			wall := time.Unix(u, 0).In(loc).Format(time.DateTime)
			got := Unix(wall, loc)
			if got != u && got != u-3600 && got != u-1800 { // the second time of a doubled hour maps to the first
				t.Fatalf("%s: %s (%d) -> %d", name, wall, u, got)
			}
			if back := time.Unix(got, 0).In(loc).Format(time.DateTime); back != wall {
				t.Fatalf("%s: %s -> %d -> %s", name, wall, got, back)
			}
		}
	}
}
