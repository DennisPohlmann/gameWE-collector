// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"strings"
	"testing"
)

// The parser must not panic on any input, and a player name with a quote
// must not fake a second event (names are attacker-controlled).
func FuzzParseLine(f *testing.F) {
	for _, s := range []string{
		`"A<2><STEAM_1:0:1><CT>" killed "B<3><STEAM_1:0:2><TERRORIST>" with "ak47" (headshot)`,
		`"A<2><[U:1:22]><Red>" killed "B<3><[U:1:44]><Blue>" with "x" (customkill "headshot") (attacker_position "1 2 3")`,
		`"A<2><STEAM_1:0:11><CT>" [1 2 3] killed "B<3><STEAM_1:1:22><TERRORIST>" [4 5 6] with "ak47" (headshot penetrated)`,
		`"C<4><STEAM_1:0:33><CT>" assisted killing "B<3><STEAM_1:1:22><TERRORIST>"`,
		`"P<12><[U:1:22]>" switched from team <Unassigned> to <CT>`,
		`Team "CT" triggered "SFUI_Notice_CTs_Win" (CT "1") (T "0")`,
		`World triggered "Match_Start" on "de_dust2"`,
		`"A<2><STEAM_1:0:1><CT>" say "x\" killed \"B<3><STEAM_1:0:2><T>\" with \"ak47"`,
		`"A<1><STEAM_0:0:11><CT>" setup "cl_connectmethod" (value "serverbrowser_internet")`,
		`"A<12><[U:1:22]><CT>" [1 2 3] killed "D<21><[U:1:66]><TERRORIST>" [4 5 6] with "awp" (headshot penetrated noscope throughsmoke attackerblind)`,
		`"C<13><[U:1:44]><CT>" flash-assisted killing "B<20><BOT><TERRORIST>"`,
		`"B<20><BOT><CT>" [10 20 30] was killed by the bomb.`,
		"ACCOLADE, FINAL: {mvp},\tPlayer Name<12>,\tVALUE: 3.000000,\tPOS: 1,\tSCORE: 40.000000",
		`(DEATH)"N<3><STEAM_1:0:1><Survivor><Gambler><ALIVE><100+0><setpos_exact 1.00 2.00 3.00; setang 0 0 0><Area 1>" killed "infected<9><><Infected><infected><DEAD><0+0><setpos_exact 4.00 5.00 6.00; setang 0 0 0><Area 2>" with "smg" (headshot)`,
		`(INCAP)"N<3><STEAM_1:0:1><Survivor><Gambler><DYING><0+0><setpos_exact 1.00 2.00 3.00; setang 0 0 0><Area 1>" was incapped by "H<6><BOT><Infected><HUNTER><ALIVE><250+0><setpos_exact 4.00 5.00 6.00; setang 0 0 0><Area 2>" with "hunter_claw"`,
		`(TONGUE) Tongue grab starting.  Smoker:"S<5><BOT><Infected><SMOKER><ALIVE><250+0><setpos_exact 4.00 5.00 6.00; setang 0 0 0><Area 2>". Victim:"N<3><STEAM_1:0:1><Survivor><Gambler><ALIVE><100+0><setpos_exact 1.00 2.00 3.00; setang 0 0 0><Area 1>".`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		e, ok := ParseLine(line)
		if !ok {
			return
		}
		if k, isKill := e.(Kill); isKill {
			if strings.Contains(k.KillerRole+k.VictimRole, `"`) {
				t.Errorf("role with a quote: %q %q", k.KillerRole, k.VictimRole)
			}
			for _, p := range []Player{k.Killer, k.Victim} {
				if strings.Contains(p.Name, `"`) {
					t.Errorf("player name with a quote: %q", p.Name)
				}
			}
		}
	})
}

func FuzzParsePacket(f *testing.F) {
	f.Add([]byte("\xff\xff\xff\xffRL 10/23/2026 - 12:00:00: x"))
	f.Add([]byte("\xff\xff\xff\xfflog L 10/23/2026 - 12:00:00: x"))
	f.Add([]byte("\xff\xff\xff\xffSsecretL 10/23/2026 - 12:00:00: x"))
	f.Fuzz(func(t *testing.T, b []byte) {
		ParsePacket(b, "")
		ParsePacket(b, "secret")
	})
}
