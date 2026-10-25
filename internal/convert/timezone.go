// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ZoneServer is the -source-timezone that leaves the conversion of
// eventTime to the source server (UNIX_TIMESTAMP in its time zone), as the
// SQL script does with time_zone SYSTEM.
const ZoneServer = "server"

// Zone is the time zone in which HLstatsX:CE wrote eventTime.
type Zone struct {
	Name string         // as given: "server", "Europe/Berlin", "+01:00", "UTC"
	Loc  *time.Location // nil for ZoneServer
}

// Server reports whether the source server converts.
func (z Zone) Server() bool { return z.Loc == nil }

var reOffset = regexp.MustCompile(`^([+-])(\d{1,2}):(\d{2})$`)

// ParseZone reads -source-timezone: "server", an IANA name (Europe/Berlin),
// UTC or a fixed offset (+01:00).
func ParseZone(s string) (Zone, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || strings.EqualFold(s, ZoneServer):
		return Zone{Name: ZoneServer}, nil
	case strings.EqualFold(s, "UTC"):
		return Zone{Name: "UTC", Loc: time.UTC}, nil
	}
	if m := reOffset.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[2])
		mi, _ := strconv.Atoi(m[3])
		if h > 14 || mi > 59 {
			return Zone{}, fmt.Errorf("-source-timezone %q: offset out of range", s)
		}
		off := h*3600 + mi*60
		if m[1] == "-" {
			off = -off
		}
		return Zone{Name: s, Loc: time.FixedZone(s, off)}, nil
	}
	loc, err := time.LoadLocation(s)
	if err != nil || s == "Local" {
		return Zone{}, fmt.Errorf("-source-timezone %q: not a time zone (want server, an IANA name such as Europe/Berlin, UTC or an offset such as +01:00; names need the time zone data of the system, /usr/share/zoneinfo)", s)
	}
	return Zone{Name: s, Loc: loc}, nil
}

// maxTimestamp is the largest timestamp_id (int unsigned).
const maxTimestamp = 1<<32 - 1

// Unix converts a local date and time of CE ("2006-01-02 15:04:05") to
// Unix seconds in loc, the way MariaDB's UNIX_TIMESTAMP does: a time in
// the gap of a change to summer time becomes the first second after the
// gap, a time that exists twice (the change back) the first of the two;
// a value before 1970, after the range of timestamp_id, empty or invalid
// ('0000-00-00 00:00:00') becomes 0 like the IFNULL(UNIX_TIMESTAMP(), 0)
// of the SQL script.
func Unix(s string, loc *time.Location) int64 {
	t, err := time.ParseInLocation(time.DateTime, strings.TrimSpace(s), time.UTC)
	if err != nil {
		return 0
	}
	wall := t.Unix() // the wall clock read as UTC
	_, before := time.Unix(wall-86400, 0).In(loc).Zone()
	_, after := time.Unix(wall+86400, 0).In(loc).Zone()
	fits := func(off int) (int64, bool) {
		u := wall - int64(off)
		_, o := time.Unix(u, 0).In(loc).Zone()
		return u, o == off
	}
	u1, ok1 := fits(before)
	u2, ok2 := fits(after)
	var u int64
	switch {
	case ok1 && ok2:
		u = min(u1, u2)
	case ok1:
		u = u1
	case ok2:
		u = u2
	default: // in the gap: the first second with the new offset
		lo, hi := wall-int64(max(before, after)), wall-int64(min(before, after))
		for lo < hi {
			mid := lo + (hi-lo)/2
			if _, o := time.Unix(mid, 0).In(loc).Zone(); o == after {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		u = lo
	}
	if u < 0 || u > maxTimestamp {
		return 0
	}
	return u
}
