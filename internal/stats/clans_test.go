// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"regexp"
	"testing"
)

func TestMatchClan(t *testing.T) {
	var patterns []*regexp.Regexp
	for _, p := range [][2]string{{"[AXXXXX]", "EITHER"}, {"=AXXXXX=", "START"}, {"-AXX-", "END"}, {"AXXX|", "START"}} {
		patterns = append(patterns, ClanPatterns(p[0], p[1])...)
	}
	cases := []struct{ name, tag, clan string }{
		{"[ZOOM]Player", "[ZOOM]", "ZOOM"},
		{"Player[zoom]", "[zoom]", "zoom"},
		{"=RAGE=Bob", "=RAGE=", "RAGE"},
		{"Bob=RAGE=", "", ""}, // START only
		{"Bob-ab-", "-ab-", "ab"},
		{"ABC|Joe", "ABC|", "ABC"},
		{"[ZOOM]", "", ""},          // nothing but the tag
		{"[TOOLONGTAG]Bob", "", ""}, // at most 6 characters
		{"Plain Name", "", ""},
	}
	for _, c := range cases {
		tag, clan := MatchClan(patterns, c.name)
		if tag != c.tag || clan != c.clan {
			t.Errorf("MatchClan(%q) = %q, %q; want %q, %q", c.name, tag, clan, c.tag, c.clan)
		}
	}
}
