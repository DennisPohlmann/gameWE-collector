// SPDX-License-Identifier: GPL-3.0-or-later

// Package logparse parses Source-engine (HL2) server log lines as sent by
// logaddress_add, including the extra lines of the gameWE SourceMod plugin,
// and the lines of Counter-Strike 2 (Source 2), which come over HTTP
// (logaddress_add_http) without the "L " prefix and with milliseconds.
package logparse

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrNotLog    = errors.New("not a log packet")
	ErrBadSecret = errors.New("missing or wrong log secret")
)

// ParsePacket extracts the log line from a UDP log packet. Packets start
// with 0xFFFFFFFF followed by 'R' (0x52, no secret) or 'S'<secret> (0x53,
// sv_logsecret). If secret is set, only packets carrying exactly that
// secret are accepted.
func ParsePacket(b []byte, secret string) (string, error) {
	line, got, hasSecret, err := SplitPacket(b)
	if err != nil {
		return "", err
	}
	if secret != "" && (!hasSecret || !SecretEqual(got, secret)) {
		return "", ErrBadSecret
	}
	return line, nil
}

// SplitPacket extracts the log line and, for 'S' packets, the secret
// (hasSecret) from a UDP log packet; it checks nothing.
func SplitPacket(b []byte) (line, secret string, hasSecret bool, err error) {
	b = bytes.TrimPrefix(b, []byte{0xff, 0xff, 0xff, 0xff})
	s := strings.TrimRight(string(b), "\x00\r\n ")
	switch {
	case strings.HasPrefix(s, "RL "):
		s = s[1:]
	case strings.HasPrefix(s, "S"):
		i := strings.Index(s, "L ")
		if i < 0 {
			return "", "", false, ErrNotLog
		}
		secret, s, hasSecret = s[1:i], s[i:], true
	case strings.HasPrefix(s, "L "):
	case strings.HasPrefix(s, "log L "): // GoldSrc (HLDS)
		s = s[4:]
	default:
		return "", "", false, ErrNotLog
	}
	return s, secret, hasSecret, nil
}

// SecretEqual compares two log secrets in constant time.
func SecretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Player is a player reference: "Name<userid><steamid><team>".
type Player struct {
	Name    string
	UserID  int
	SteamID string
	Team    string
}

// IsBot reports whether the player is a bot.
func (p Player) IsBot() bool { return p.SteamID == "BOT" }

// Trackable reports whether the reference identifies a real player or bot.
func (p Player) Trackable() bool {
	switch p.SteamID {
	case "", "Console", "STEAM_ID_PENDING", "STEAM_ID_LAN", "HLTV", "UNKNOWN",
		"VALVE_ID_LAN", "VALVE_ID_PENDING", "4294967295": // GoldSrc LAN / pending ids
		return false
	}
	// An id that normalizes to nothing ("STEAM_0:") would make a player
	// with an empty unique id.
	return p.UserID >= 0 && p.Key() != ""
}

// Key is the player's identity, also stored as
// hlstats_PlayerUniqueIds.uniqueId: NormalizedID of the SteamID, or
// BOT:<name> for bots.
func (p Player) Key() string {
	if p.IsBot() {
		return "BOT:" + p.Name
	}
	return NormalizedID(p.SteamID)
}

// NormalizedID returns a SteamID in the HLstatsX uniqueId form: without the
// STEAM_x: prefix (STEAM_0:1:123 and STEAM_1:1:123 are 1:123), and Steam3
// ids [U:1:n] in the same form, n%2:n/2 ([U:1:247] is 1:123). Game servers
// log both forms for the same player, on different lines. Other ids are
// returned unchanged.
func NormalizedID(id string) string {
	if strings.HasPrefix(id, "STEAM_") {
		if i := strings.IndexByte(id, ':'); i >= 0 {
			return id[i+1:]
		}
	}
	if strings.HasPrefix(id, "[U:1:") && strings.HasSuffix(id, "]") {
		if n, err := strconv.ParseUint(id[5:len(id)-1], 10, 32); err == nil {
			return strconv.FormatUint(n%2, 10) + ":" + strconv.FormatUint(n/2, 10)
		}
	}
	return id
}

// Event is one of the types below.
type Event interface{ event() }

type (
	Connected struct {
		Player  Player
		Address string
	}
	Entered      struct{ Player Player }
	Disconnected struct {
		Player Player
		Reason string
	}
	JoinedTeam struct {
		Player Player
		Team   string
	}
	NameChange struct {
		Player  Player
		NewName string
	}
	Kill struct {
		Killer, Victim Player
		Weapon         string
		Headshot       bool
		// Positions in the kill line (map units), nil if not logged: CS:GO
		// "[x y z]" after the player tags, TF2 and the AMX plugin
		// (attacker_position "x y z") (victim_position "x y z").
		KillerPos, VictimPos *[3]int
		// Flags are the other words of CS:GO's "(headshot penetrated noscope
		// throughsmoke attackerblind)", e.g. "penetrated".
		Flags []string
		// KillerRole and VictimRole are the roles in Left 4 Dead's player
		// tags (survivor or infected class, e.g. "Gambler", "SMOKER"), ""
		// for other games. Incap is a Left 4 Dead incapacitation
		// (`(INCAP)"V" was incapped by "A"`), counted as a kill as
		// HLstatsX:CE does.
		KillerRole, VictimRole string
		Incap                  bool
	}
	// WorldDeath is a death without a killer: CS:GO's
	// `"P" [x y z] was killed by the bomb.` (Weapon "planted_c4").
	WorldDeath struct {
		Player Player
		Weapon string
	}
	// Accolade is CS:GO's end-of-match line
	// `ACCOLADE, FINAL: {mvp},	Name<12>,	VALUE: 3.000000,	POS: 1,	SCORE: 40.000000`
	// (the player only by name and userid).
	Accolade struct {
		Name     string // mvp, 3k, hsp, ...
		Player   string
		UserID   int
		Position int
	}
	Suicide struct {
		Player Player
		Weapon string
		Pos    *[3]int // CS:GO "[x y z]"
	}
	Say struct {
		Player Player
		Text   string
		Team   bool
	}
	// PlayerTriggered covers `"P" triggered "x" (key "value") ...`,
	// e.g. the plugin's weaponstats / weaponstats2 lines.
	PlayerTriggered struct {
		Player  Player
		Trigger string
		Props   map[string]string
		Victim  *Player // `... triggered "domination" against "Victim<..>"`
	}
	TeamTriggered struct {
		Team    string
		Trigger string
	}
	TeamScored struct {
		Team    string
		Score   int
		Players int
	}
	WorldTriggered struct {
		Trigger string
		Winner  string // set for e.g. Round_Win (winner "Red")
	}
	// MapStarted is `Loading map "x"` or `Started map "x" (CRC "...")`.
	MapStarted struct{ Map string }
	// Banned is `Banid: "P" was kicked and banned "permanently" by "Console"`
	// (also "for 5.00 minutes").
	Banned struct {
		Player    Player
		Permanent bool
	}
	// Requested is a query of another plugin through the gameWE / gameME
	// plugin's natives: `"P" requested "playerinfo" (value "17")`; Player is
	// nil for `"Server" requested "top10" (value "3")`.
	Requested struct {
		Player  *Player
		Type    string
		QueryID int
	}
	// Located is the plugin's position line, e.g. `"P" located on "kill"
	// (position "1 2 3") against "V" (victim_position "4 5 6")`.
	Located struct {
		Player   Player
		Event    string
		Pos      [3]int
		Victim   *Player
		VictimAt [3]int
	}
	// RoleChange is `"P" changed role to "sas"` (CS:S models, TF2 classes).
	RoleChange struct {
		Player Player
		Role   string
	}
	// Attacked is a hit of CS:GO / CS2 (mp_logdetail 3):
	// `"A" [x y z] attacked "V" [x y z] with "ak47" (damage "27")
	// (damage_armor "3") (health "73") (armor "96") (hitgroup "chest")`.
	Attacked struct {
		Attacker, Victim Player
		Weapon           string
		Damage           int
		Hitgroup         string // head, neck, chest, stomach, left arm, ... ("" if not logged)
	}
	// KillAssist is CS:GO's `"A" assisted killing "V"`, or
	// `"A" flash-assisted killing "V"` (Flash).
	KillAssist struct {
		Assister, Victim Player
		Flash            bool
	}
	// Setup is a client setting the plugin queried:
	// `"P" setup "cl_connectmethod" (value "serverbrowser_internet")`.
	Setup struct {
		Player      Player
		Name, Value string
	}
)

func (Connected) event()       {}
func (Entered) event()         {}
func (Disconnected) event()    {}
func (JoinedTeam) event()      {}
func (NameChange) event()      {}
func (Kill) event()            {}
func (Suicide) event()         {}
func (Say) event()             {}
func (PlayerTriggered) event() {}
func (TeamTriggered) event()   {}
func (TeamScored) event()      {}
func (WorldTriggered) event()  {}
func (MapStarted) event()      {}
func (Banned) event()          {}
func (Requested) event()       {}
func (Located) event()         {}
func (RoleChange) event()      {}
func (Setup) event()           {}
func (KillAssist) event()      {}
func (WorldDeath) event()      {}
func (Accolade) event()        {}
func (Attacked) event()        {}

// Names must not contain a double quote. This keeps chat text or crafted
// names from being parsed as a different event (log injection).
const (
	pref  = `"([^"]*?)<(-?\d+)><([^<>]*)><([^<>]*)>"`
	pref3 = `"([^"]*?)<(-?\d+)><([^<>]*)>"`
	pos   = `(?: \[-?\d+ -?\d+ -?\d+\])?`
	posC  = `(?: \[(-?\d+) (-?\d+) (-?\d+)\])?` // the same, captured
)

var (
	// "L 10/25/2026 - 21:00:00: " (Source 1, GoldSrc), CS2 over HTTP
	// "10/25/2026 - 21:00:00.123 - " (and with "L " over UDP).
	reTimestamp  = regexp.MustCompile(`^(?:L )?\d\d/\d\d/\d{4} - \d\d:\d\d:\d\d(?:: |\.\d{1,3} - )`)
	reKill       = regexp.MustCompile(`^` + pref + posC + ` killed ` + pref + posC + ` with "([^"]*)"(.*)$`)
	reSuicide    = regexp.MustCompile(`^` + pref + posC + ` committed suicide with "([^"]*)"`)
	rePosProp    = regexp.MustCompile(`\((attacker|victim)_position "(-?\d+) (-?\d+) (-?\d+)"\)`)
	reConnected  = regexp.MustCompile(`^` + pref + ` connected, address "([^"]*)"$`)
	reEntered    = regexp.MustCompile(`^` + pref + ` entered the game$`)
	reDisconn    = regexp.MustCompile(`^` + pref + ` disconnected(?: \(reason "(.*)"\))?$`)
	reJoined     = regexp.MustCompile(`^` + pref + ` joined team "([^"]*)"`)
	reSwitched   = regexp.MustCompile(`^` + pref3 + ` switched from team <([^>]*)> to <([^>]*)>$`)
	reName       = regexp.MustCompile(`^` + pref + ` changed name to "(.*)"$`)
	reSay        = regexp.MustCompile(`^` + pref + ` say(_team)? "(.*)"$`)
	rePTrig      = regexp.MustCompile(`^` + pref + ` triggered "([^"]*)"(.*)$`)
	reTeamTrig   = regexp.MustCompile(`^Team "([^"]*)" triggered "([^"]*)"`)
	reTeamScore  = regexp.MustCompile(`^Team "([^"]*)" scored "(-?\d+)" with "(\d+)" players`)
	reWorldTrig  = regexp.MustCompile(`^World triggered "([^"]*)"(.*)$`)
	reAgainst    = regexp.MustCompile(`^ against ` + pref + `(.*)$`)
	reProp       = regexp.MustCompile(`\((\w+) "([^"]*)"\)`)
	reMatchStart = regexp.MustCompile(`^World triggered "Match_Start" on "([^"]+)"`)
	reMap        = regexp.MustCompile(`^(?:Loading|Started) map "([^"]+)"`)
	reRequested  = regexp.MustCompile(`^"(?:([^"]*?)<(-?\d+)><([^<>]*)><([^<>]*)>|Server)" requested "([^"]*)" \(value "(\d+)"\)$`)
	reLocated    = regexp.MustCompile(`^` + pref + ` located on "([^"]*)" \(position "(-?\d+) (-?\d+) (-?\d+)"\)` +
		`(?: against ` + pref + ` \(victim_position "(-?\d+) (-?\d+) (-?\d+)"\))?$`)
	reRole   = regexp.MustCompile(`^` + pref + ` changed role to "([^"]*)"$`)
	reAssist = regexp.MustCompile(`^` + pref + pos + ` (flash-)?assisted killing ` + pref + pos + `$`)
	reBomb   = regexp.MustCompile(`^` + pref + pos + ` was killed by the bomb\.?$`)
	reAttack = regexp.MustCompile(`^` + pref + pos + ` attacked ` + pref + pos + ` with "([^"]*)"(.*)$`)
	reAccol  = regexp.MustCompile(`^ACCOLADE, FINAL: \{(\w+)\},\s+(.*?)<(\d+)>,\s+VALUE: [-\d.]+,\s+POS: (\d+),`)
	reFlags  = regexp.MustCompile(`\(([a-z]+(?: [a-z]+)*)\)`)
	reSetup  = regexp.MustCompile(`^` + pref + ` setup "([^"]*)" \(value "([^"]*)"\)$`)
	reBanned = regexp.MustCompile(`^Banid: ` + pref + ` was (?:kicked and )?banned "(permanently|for [\d.]+ minutes)"`)
)

// position reads three captured coordinates (nil if not logged). Values
// beyond the int range are clamped by the caller's storage.
func position(m []string) *[3]int {
	if len(m) < 3 || m[0] == "" {
		return nil
	}
	var p [3]int
	for i := range p {
		v, err := strconv.Atoi(m[i])
		if err != nil {
			return nil
		}
		p[i] = v
	}
	return &p
}

func player(m []string) Player {
	id, _ := strconv.Atoi(m[1])
	return Player{Name: m[0], UserID: id, SteamID: m[2], Team: m[3]}
}

// ParseLine parses one log line, with or without the "L date - time: "
// prefix. It returns false for lines gameWE does not use.
func ParseLine(line string) (Event, bool) {
	line = strings.TrimRight(line, "\x00\r\n ")
	line = reTimestamp.ReplaceAllString(line, "")
	var tags []l4dTag
	if isL4DLine(line) {
		if e, ok := parseL4DOnly(line); ok {
			return e, true
		}
		line, tags = l4dLine(line)
	}

	switch {
	case strings.HasPrefix(line, "Team "):
		if m := reTeamScore.FindStringSubmatch(line); m != nil {
			score, _ := strconv.Atoi(m[2])
			players, _ := strconv.Atoi(m[3])
			return TeamScored{Team: m[1], Score: score, Players: players}, true
		}
		if m := reTeamTrig.FindStringSubmatch(line); m != nil {
			// CS:GO logs "SFUI_Notice_CTs_Win"; the win triggers are "CTs_Win".
			return TeamTriggered{Team: m[1], Trigger: strings.TrimPrefix(m[2], "SFUI_Notice_")}, true
		}
		return nil, false
	case strings.HasPrefix(line, "World triggered"):
		if m := reMatchStart.FindStringSubmatch(line); m != nil {
			return MapStarted{Map: m[1]}, true // CS:GO match start
		}
		if m := reWorldTrig.FindStringSubmatch(line); m != nil {
			return WorldTriggered{Trigger: m[1], Winner: props(m[2])["winner"]}, true
		}
		return nil, false
	case strings.HasPrefix(line, "Banid: "):
		if m := reBanned.FindStringSubmatch(line); m != nil {
			return Banned{Player: player(m[1:5]), Permanent: m[5] == "permanently"}, true
		}
		return nil, false
	case strings.HasPrefix(line, "ACCOLADE, FINAL: "):
		if m := reAccol.FindStringSubmatch(line); m != nil {
			id, err1 := strconv.Atoi(m[3])
			p, err2 := strconv.Atoi(m[4])
			if err1 != nil || err2 != nil {
				return nil, false
			}
			return Accolade{Name: strings.ToLower(m[1]), Player: m[2], UserID: id, Position: p}, true
		}
		return nil, false
	case strings.HasPrefix(line, "Loading map") || strings.HasPrefix(line, "Started map"):
		if m := reMap.FindStringSubmatch(line); m != nil {
			return MapStarted{Map: m[1]}, true
		}
		return nil, false
	case !strings.HasPrefix(line, `"`):
		return nil, false
	}

	// Chat first: its free text is the only attacker-controlled part.
	if m := reSay.FindStringSubmatch(line); m != nil {
		return Say{Player: player(m[1:5]), Team: m[5] != "", Text: m[6]}, true
	}
	if m := reKill.FindStringSubmatch(line); m != nil {
		k := Kill{
			Killer: player(m[1:5]), Victim: player(m[8:12]), Weapon: m[15],
			// CS:S "(headshot)", CS:GO "(headshot penetrated)", TF2 (customkill "headshot")
			Headshot:  strings.Contains(m[16], "(headshot") || strings.Contains(m[16], `(customkill "headshot`),
			KillerPos: position(m[5:8]), VictimPos: position(m[12:15]),
		}
		for _, f := range reFlags.FindAllStringSubmatch(m[16], -1) {
			for _, w := range strings.Fields(f[1]) {
				if w != "headshot" && len(k.Flags) < 8 {
					k.Flags = append(k.Flags, w)
				}
			}
		}
		for _, p := range rePosProp.FindAllStringSubmatch(m[16], 2) {
			if p[1] == "attacker" && k.KillerPos == nil {
				k.KillerPos = position(p[2:5])
			} else if p[1] == "victim" && k.VictimPos == nil {
				k.VictimPos = position(p[2:5])
			}
		}
		if len(tags) == 2 {
			k.KillerRole, k.VictimRole = tags[0].role, tags[1].role
			k.KillerPos, k.VictimPos = cmpOr(k.KillerPos, tags[0].pos), cmpOr(k.VictimPos, tags[1].pos)
		}
		return k, true
	}
	if m := reAttack.FindStringSubmatch(line); m != nil {
		pr := props(m[10])
		dmg, _ := strconv.Atoi(pr["damage"])
		return Attacked{Attacker: player(m[1:5]), Victim: player(m[5:9]), Weapon: m[9], Damage: max(dmg, 0), Hitgroup: pr["hitgroup"]}, true
	}
	if m := reSuicide.FindStringSubmatch(line); m != nil {
		return Suicide{Player: player(m[1:5]), Weapon: m[8], Pos: position(m[5:8])}, true
	}
	if m := rePTrig.FindStringSubmatch(line); m != nil {
		ev := PlayerTriggered{Player: player(m[1:5]), Trigger: m[5], Props: props(m[6])}
		if a := reAgainst.FindStringSubmatch(m[6]); a != nil {
			v := player(a[1:5])
			ev.Victim, ev.Props = &v, props(a[5])
		}
		return ev, true
	}

	if m := reConnected.FindStringSubmatch(line); m != nil {
		return Connected{Player: player(m[1:5]), Address: m[5]}, true
	}
	if m := reEntered.FindStringSubmatch(line); m != nil {
		return Entered{Player: player(m[1:5])}, true
	}
	if m := reDisconn.FindStringSubmatch(line); m != nil {
		return Disconnected{Player: player(m[1:5]), Reason: m[5]}, true
	}
	if m := reJoined.FindStringSubmatch(line); m != nil {
		return JoinedTeam{Player: player(m[1:5]), Team: m[5]}, true
	}
	if m := reSwitched.FindStringSubmatch(line); m != nil {
		p := player(append(m[1:4:4], m[4]))
		p.Team = m[5]
		return JoinedTeam{Player: p, Team: m[5]}, true
	}
	if m := reName.FindStringSubmatch(line); m != nil {
		return NameChange{Player: player(m[1:5]), NewName: m[5]}, true
	}
	if m := reLocated.FindStringSubmatch(line); m != nil {
		ev := Located{Player: player(m[1:5]), Event: m[5], Pos: ints(m[6:9])}
		if m[10] != "" {
			v := player(m[9:13])
			ev.Victim, ev.VictimAt = &v, ints(m[13:16])
		}
		return ev, true
	}
	if m := reRole.FindStringSubmatch(line); m != nil {
		return RoleChange{Player: player(m[1:5]), Role: m[5]}, true
	}
	if m := reAssist.FindStringSubmatch(line); m != nil {
		return KillAssist{Assister: player(m[1:5]), Flash: m[5] != "", Victim: player(m[6:10])}, true
	}
	if m := reBomb.FindStringSubmatch(line); m != nil {
		return WorldDeath{Player: player(m[1:5]), Weapon: "planted_c4"}, true
	}
	if m := reSetup.FindStringSubmatch(line); m != nil {
		return Setup{Player: player(m[1:5]), Name: m[5], Value: m[6]}, true
	}
	if m := reRequested.FindStringSubmatch(line); m != nil {
		id, err := strconv.Atoi(m[6])
		if err != nil {
			return nil, false
		}
		ev := Requested{Type: m[5], QueryID: id}
		if m[2] != "" {
			p := player(m[1:5])
			ev.Player = &p
		}
		return ev, true
	}
	return nil, false
}

func ints(s []string) [3]int {
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(s[i])
	}
	return out
}

func props(s string) map[string]string {
	out := map[string]string{}
	for _, m := range reProp.FindAllStringSubmatch(s, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// MaxSecret is the largest log secret: the Source engine reads
// sv_logsecret as a number (a value that is not one, or 0, sends no
// secret), so secrets are decimal numbers from 1 to the largest int32.
const MaxSecret = 2147483647

// CheckSecret reports whether s can be used as sv_logsecret: empty (no
// secret) or a decimal number 1..MaxSecret without leading zeros.
func CheckSecret(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n < 1 || n > MaxSecret || s[0] == '0' {
		return fmt.Errorf("log secret: want a number from 1 to %d (sv_logsecret is read as a number)", MaxSecret)
	}
	return nil
}
