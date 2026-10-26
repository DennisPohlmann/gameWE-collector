// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"testing"
	"time"
)

// Values from MariaDB: SELECT YEARWEEK('<date>'). The integration test
// compares many more dates against a live MariaDB.
func TestYearWeek(t *testing.T) {
	cases := map[string]int{
		"2026-10-22": 202642, "2026-01-01": 202552, "2026-01-04": 202601, "2025-12-31": 202552,
		"2023-01-01": 202301, "2022-12-31": 202252, "2021-01-02": 202052, "2000-01-01": 199952,
	}
	for d, want := range cases {
		tm, _ := time.Parse("2006-01-02", d)
		if got := YearWeek(tm); got != want {
			t.Errorf("YearWeek(%s) = %d, want %d", d, got, want)
		}
	}
}

// The history day is the calendar day in HistoryZone, UTC without one:
// 23:30 UTC on 25 Oct is already 26 Oct in Berlin (CET, +1).
func TestHistoryDay(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	at := time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC)
	if got := (&Store{}).historyDay(at); got != "2026-10-25" {
		t.Errorf("without zone: %s, want 2026-10-25", got)
	}
	if got := (&Store{HistoryZone: berlin}).historyDay(at); got != "2026-10-26" {
		t.Errorf("Europe/Berlin: %s, want 2026-10-26", got)
	}
	// 21:59 UTC in summer is still the same day in Berlin (CEST, +2).
	if got := (&Store{HistoryZone: berlin}).historyDay(time.Date(2026, 7, 1, 21, 59, 0, 0, time.UTC)); got != "2026-07-01" {
		t.Errorf("Europe/Berlin summer: %s, want 2026-07-01", got)
	}
}
