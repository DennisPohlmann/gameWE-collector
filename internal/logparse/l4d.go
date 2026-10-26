package logparse

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Left 4 Dead 1 and 2 log their players with more fields than other Source
// games: name, userid, SteamID, team, then role, state, health and the
// position, e.g.
//
//	"Nick<3><STEAM_1:0:1><Survivor><Gambler><ALIVE><100+0><setpos_exact 1.00 2.00 3.00; setang 0.00 0.00 0.00><Area 12>"
//
// and mark kills with "(DEATH)", incapacitations with
// `(INCAP)"V" was incapped by "A" with "w"` and a Smoker's grab with
// `(TONGUE) Tongue grab starting.  Smoker:"A". Victim:"V".` (as read by
// HLstatsX:CE's hlstats.pl). The common infected and the Witch have no
// SteamID; they are the bots "Infected Horde" and "Witch".

// l4dTag is what a Left 4 Dead player tag has beyond the four usual fields.
type l4dTag struct {
	role string
	pos  *[3]int
}

var (
	reL4DTag    = regexp.MustCompile(`"([^"]*?)<(-?\d+)><([^<>]*)><([^<>]*)>((?:<[^<>"]*>)+)"`)
	reL4DRole   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	reL4DPos    = regexp.MustCompile(`<setpos_exact (-?\d+(?:\.\d+)?) (-?\d+(?:\.\d+)?) (-?\d+(?:\.\d+)?)[;>]`)
	reL4DIncap  = regexp.MustCompile(`^` + pref + ` was incapped by ` + pref + ` with "([^"]*)"(.*)$`)
	reL4DTongue = regexp.MustCompile(`^\(TONGUE\) Tongue grab starting\.\s+Smoker:` + pref + `\.\s+Victim:` + pref + `\.?$`)
)

// isL4DLine is a cheap check for a line in the Left 4 Dead format.
func isL4DLine(line string) bool {
	return strings.HasPrefix(line, "(") || strings.Contains(line, "><setpos_exact ")
}

// l4dLine turns a Left 4 Dead line into the usual form: without the
// "(DEATH)" mark and with four-field player tags. It returns the extra
// fields of each rewritten tag in line order.
func l4dLine(line string) (string, []l4dTag) {
	line = strings.TrimPrefix(line, "(DEATH)")
	line = strings.TrimPrefix(line, "(INCAP)")
	var tags []l4dTag
	line = reL4DTag.ReplaceAllStringFunc(line, func(s string) string {
		m := reL4DTag.FindStringSubmatch(s)
		name, steam, team, extra := m[1], m[3], m[4], m[5]
		t := l4dTag{}
		if i := strings.Index(extra[1:], ">"); i >= 0 && reL4DRole.MatchString(extra[1:i+1]) {
			t.role = extra[1 : i+1] // a known form only: each new role is a hlstats_Roles row (tinyint id)
		}
		if p := reL4DPos.FindStringSubmatch(extra); p != nil {
			t.pos = l4dPos(p[1:4])
		}
		if steam == "" && strings.EqualFold(team, "Infected") {
			switch strings.ToLower(name) {
			case "infected":
				name, steam = "Infected Horde", "BOT"
			case "witch":
				name, steam = "Witch", "BOT"
			}
		}
		tags = append(tags, t)
		return `"` + name + "<" + m[2] + "><" + steam + "><" + team + `>"`
	})
	return line, tags
}

// l4dPos reads the three setpos_exact coordinates, rounded to map units.
func l4dPos(m []string) *[3]int {
	var p [3]int
	for i := range p {
		f, err := strconv.ParseFloat(m[i], 64)
		if err != nil || math.Abs(f) > 1e9 {
			return nil
		}
		p[i] = int(math.Round(f))
	}
	return &p
}

// parseL4DOnly parses the lines only Left 4 Dead logs: the
// incapacitation (a Kill with Incap) and the Smoker's tongue grab (the
// player-vs-player action tongue_grab).
func parseL4DOnly(line string) (Event, bool) {
	switch {
	case strings.HasPrefix(line, "(INCAP)"):
		l, tags := l4dLine(line)
		m := reL4DIncap.FindStringSubmatch(l)
		if m == nil {
			return nil, false
		}
		if strings.EqualFold(m[4], "Infected") {
			return nil, false // only survivors are incapacitated as HLstatsX:CE counts it
		}
		// "V was incapped by A" is "A killed V".
		k := Kill{Killer: player(m[5:9]), Victim: player(m[1:5]), Weapon: m[9], Incap: true,
			Headshot: strings.Contains(m[10], "(headshot")}
		if len(tags) == 2 {
			k.VictimRole, k.KillerRole = tags[0].role, tags[1].role
			k.VictimPos, k.KillerPos = tags[0].pos, tags[1].pos
		}
		return k, true
	case strings.HasPrefix(line, "(TONGUE)"):
		l, _ := l4dLine(line)
		m := reL4DTongue.FindStringSubmatch(l)
		if m == nil {
			return nil, false
		}
		v := player(m[5:9])
		return PlayerTriggered{Player: player(m[1:5]), Trigger: "tongue_grab", Props: map[string]string{}, Victim: &v}, true
	}
	return nil, false // "(DEATH)" kill lines are parsed as usual after l4dLine
}

// cmpOr returns a if it is set, else b.
func cmpOr(a, b *[3]int) *[3]int {
	if a != nil {
		return a
	}
	return b
}
