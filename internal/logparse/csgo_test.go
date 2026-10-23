// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"strings"
	"testing"
)

// TestCSGOFormats has one line per CS:GO log format the collector reads
// (docs/GAMES.md, CS:GO) and the CS:GO lines it ignores. Made-up names and
// ids in the form CS:GO logs them (STEAM_1, positions after the tag).
func TestCSGOFormats(t *testing.T) {
	a := `"Alpha<2><STEAM_1:0:11><CT>"`
	b := `"Bravo<3><STEAM_1:1:22><TERRORIST>"`
	cases := []struct {
		name, line string
		want       func(Event) bool
	}{
		{"kill", a + ` [100 -200 30] killed ` + b + ` [1 2 3] with "ak47"`,
			func(e Event) bool {
				k, ok := e.(Kill)
				return ok && k.Weapon == "ak47" && !k.Headshot && len(k.Flags) == 0 && k.KillerPos != nil && k.Victim.SteamID == "STEAM_1:1:22"
			}},
		{"kill headshot flags", a + ` [1 2 3] killed ` + b + ` [4 5 6] with "awp" (headshot penetrated noscope)`,
			func(e Event) bool {
				k, ok := e.(Kill)
				return ok && k.Headshot && strings.Join(k.Flags, ",") == "penetrated,noscope"
			}},
		{"kill knife variant", a + ` [1 2 3] killed ` + b + ` [4 5 6] with "knife_karambit"`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Weapon == "knife_karambit" }},
		{"kill molotov", a + ` [1 2 3] killed ` + b + ` [4 5 6] with "inferno"`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Weapon == "inferno" }},
		{"teamkill", a + ` [1 2 3] killed "Charlie<4><STEAM_1:0:33><CT>" [4 5 6] with "m4a1_silencer"`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Killer.Team == k.Victim.Team }},
		{"assist", a + ` assisted killing ` + b,
			func(e Event) bool { k, ok := e.(KillAssist); return ok && !k.Flash && k.Victim.Name == "Bravo" }},
		{"flash assist", a + ` flash-assisted killing ` + b,
			func(e Event) bool { k, ok := e.(KillAssist); return ok && k.Flash }},
		{"suicide", b + ` [1 2 3] committed suicide with "world"`,
			func(e Event) bool { s, ok := e.(Suicide); return ok && s.Weapon == "world" && s.Pos != nil }},
		{"suicide grenade", b + ` [1 2 3] committed suicide with "hegrenade"`,
			func(e Event) bool { s, ok := e.(Suicide); return ok && s.Weapon == "hegrenade" }},
		{"bomb death", a + ` [1 2 3] was killed by the bomb.`,
			func(e Event) bool { w, ok := e.(WorldDeath); return ok && w.Weapon == "planted_c4" }},
		{"bomb planted", b + ` triggered "Planted_The_Bomb" at bombsite A`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Trigger == "Planted_The_Bomb" }},
		{"bomb defused", a + ` triggered "Defused_The_Bomb"`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Trigger == "Defused_The_Bomb" }},
		{"begin defuse", a + ` triggered "Begin_Bomb_Defuse_With_Kit"`,
			func(e Event) bool {
				p, ok := e.(PlayerTriggered)
				return ok && p.Trigger == "Begin_Bomb_Defuse_With_Kit"
			}},
		{"got bomb", b + ` triggered "Got_The_Bomb"`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Trigger == "Got_The_Bomb" }},
		{"hostage", a + ` triggered "Rescued_A_Hostage"`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Trigger == "Rescued_A_Hostage" }},
		{"team win", `Team "TERRORIST" triggered "SFUI_Notice_Target_Bombed" (CT "3") (T "5")`,
			func(e Event) bool {
				tt, ok := e.(TeamTriggered)
				return ok && tt.Team == "TERRORIST" && tt.Trigger == "Target_Bombed"
			}},
		{"team score", `Team "CT" scored "3" with "5" players`,
			func(e Event) bool { s, ok := e.(TeamScored); return ok && s.Score == 3 && s.Players == 5 }},
		{"team switch", `"Alpha<2><STEAM_1:0:11>" switched from team <Unassigned> to <CT>`,
			func(e Event) bool { j, ok := e.(JoinedTeam); return ok && j.Team == "CT" }},
		{"connect", `"Alpha<2><STEAM_1:0:11><>" connected, address "203.0.113.7:27005"`,
			func(e Event) bool { c, ok := e.(Connected); return ok && c.Address == "203.0.113.7:27005" }},
		{"entered", `"Alpha<2><STEAM_1:0:11><>" entered the game`,
			func(e Event) bool { _, ok := e.(Entered); return ok }},
		{"disconnect", a + ` disconnected (reason "Disconnect")`,
			func(e Event) bool { d, ok := e.(Disconnected); return ok && d.Reason == "Disconnect" }},
		{"say", a + ` say "gg"`,
			func(e Event) bool { s, ok := e.(Say); return ok && s.Text == "gg" && !s.Team }},
		{"say_team", a + ` say_team "rush b"`,
			func(e Event) bool { s, ok := e.(Say); return ok && s.Team }},
		{"name", a + ` changed name to "Alpha2"`,
			func(e Event) bool { n, ok := e.(NameChange); return ok && n.NewName == "Alpha2" }},
		{"map", `Started map "de_mirage" (CRC "-1234567")`,
			func(e Event) bool { m, ok := e.(MapStarted); return ok && m.Map == "de_mirage" }},
		{"accolade", "ACCOLADE, FINAL: {5k},\tAlpha<2>,\tVALUE: 1.000000,\tPOS: 1,\tSCORE: 10.000000",
			func(e Event) bool {
				c, ok := e.(Accolade)
				return ok && c.Name == "5k" && c.UserID == 2 && c.Position == 1
			}},
		// gameWE SourceMod plugin
		{"plugin mvp", a + ` triggered "mvp"`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Trigger == "mvp" }},
		{"plugin weaponstats", a + ` triggered "weaponstats" (weapon "ak47") (shots "30") (hits "9") (kills "2") (headshots "1") (tks "0") (damage "310") (deaths "1")`,
			func(e Event) bool {
				p, ok := e.(PlayerTriggered)
				return ok && p.Props["weapon"] == "ak47" && p.Props["shots"] == "30" && p.Props["damage"] == "310"
			}},
		{"plugin weaponstats2", a + ` triggered "weaponstats2" (weapon "ak47") (head "1") (chest "5") (stomach "1") (leftarm "1") (rightarm "0") (leftleg "1") (rightleg "0")`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Props["chest"] == "5" }},
		{"plugin role", a + ` changed role to "gign"`,
			func(e Event) bool { r, ok := e.(RoleChange); return ok && r.Role == "gign" }},
		{"plugin location", a + ` located on "kill" (position "1 2 3") against ` + b + ` (victim_position "4 5 6")`,
			func(e Event) bool { l, ok := e.(Located); return ok && l.Event == "kill" }},
	}
	for _, c := range cases {
		e, ok := ParseLine(c.line)
		if !ok || !c.want(e) {
			t.Errorf("%s: %q -> %#v %v", c.name, c.line, e, ok)
		}
	}

	// CS:GO lines the collector does not use.
	for _, l := range []string{
		a + ` [1 2 3] killed other "chicken<123>" [4 5 6] with "knife"`,
		a + ` [1 2 3] attacked ` + b + ` [4 5 6] with "ak47" (damage "27") (damage_armor "3") (health "73") (armor "96") (hitgroup "chest")`,
		a + ` purchased "ak47"`,
		a + ` threw hegrenade [1 2 3]`,
		a + ` blinded for 2.10 by ` + b + ` from flashbang entindex 123 `,
		a + ` left buyzone with [ weapon_knife weapon_usp_silencer ]`,
		a + ` money change 800-2700 = $-1900 (tracked) (purchase: weapon_ak47)`,
		`Game Over: competitive mg_active de_mirage score 16:9 after 35 min`,
		`World triggered "Round_Start"`,
	} {
		if e, ok := ParseLine(l); ok {
			switch e.(type) {
			case Kill, Suicide, WorldDeath, PlayerTriggered, KillAssist:
				t.Errorf("%q parsed as %T", l, e)
			}
		}
	}
}
