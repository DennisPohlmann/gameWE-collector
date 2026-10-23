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
