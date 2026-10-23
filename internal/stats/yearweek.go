// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import "time"

// YearWeek returns MySQL's YEARWEEK(date) (mode 0) for the UTC date of t:
// weeks start on Sunday, week 1 is the first week with a Sunday in the
// year, and days before it belong to the last week of the previous year.
// It is computed in Go so both databases store the same yearweek_id.
func YearWeek(t time.Time) int {
	d := time.Date(t.UTC().Year(), t.UTC().Month(), t.UTC().Day(), 0, 0, 0, 0, time.UTC)
	year := d.Year()
	first := firstSunday(year)
	if d.Before(first) {
		year--
		first = firstSunday(year)
	}
	return year*100 + int(d.Sub(first).Hours())/(24*7) + 1
}

func firstSunday(year int) time.Time {
	jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	return jan1.AddDate(0, 0, (7-int(jan1.Weekday()))%7)
}
