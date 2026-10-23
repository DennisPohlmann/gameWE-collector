// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"strings"
	"testing"
)

// Lines in the formats of the other engines (documentation addresses,
// made-up names and ids).
func TestEngineFormats(t *testing.T) {
	// GoldSrc sends "log L ..." packets.
	if l, err := ParsePacket([]byte("\xff\xff\xff\xfflog L 10/23/2026 - 12:00:00: \"A<1><STEAM_0:0:11><CT>\" killed \"B<2><STEAM_0:1:22><TERRORIST>\" with \"ak47\"\n\x00"), ""); err != nil || l != `L 10/23/2026 - 12:00:00: "A<1><STEAM_0:0:11><CT>" killed "B<2><STEAM_0:1:22><TERRORIST>" with "ak47"` {
		t.Errorf("GoldSrc packet: %q %v", l, err)
	}
	cases := []struct {
		line string
		want func(Event) bool
	}{
		// cstrike (AMX plugin): positions as properties, no (headshot)
		{`"A<1><STEAM_0:0:11><CT>" killed "B<2><STEAM_0:1:22><TERRORIST>" with "ak47" (attacker_position "1 2 3") (victim_position "4 5 6")`,
			func(e Event) bool {
				k, ok := e.(Kill)
				return ok && k.Weapon == "ak47" && !k.Headshot && k.Victim.SteamID == "STEAM_0:1:22"
			}},
		{`"A<1><STEAM_0:0:11><CT>" triggered "headshot"`,
			func(e Event) bool { p, ok := e.(PlayerTriggered); return ok && p.Trigger == "headshot" }},
		// HL1 weapon with a space
		{`"A<1><STEAM_0:0:11><>" killed "B<2><STEAM_0:1:22><>" with "gluon gun"`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Weapon == "gluon gun" }},
		// TF2: customkill headshot, kill assist, class
		{`"A<2><[U:1:22]><Red>" killed "B<3><[U:1:44]><Blue>" with "sniperrifle" (customkill "headshot") (attacker_position "1 2 3") (victim_position "4 5 6")`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Headshot && k.Weapon == "sniperrifle" }},
		{`"C<4><[U:1:66]><Red>" triggered "kill assist" against "B<3><[U:1:44]><Blue>" (assister_position "1 2 3") (attacker_position "1 2 3") (victim_position "4 5 6")`,
			func(e Event) bool {
				p, ok := e.(PlayerTriggered)
				return ok && p.Trigger == "kill assist" && p.Victim != nil && p.Victim.Name == "B"
			}},
		{`"A<2><[U:1:22]><Red>" changed role to "sniper"`,
			func(e Event) bool { r, ok := e.(RoleChange); return ok && r.Role == "sniper" }},
		// CS:GO: positions after the player tag, assists
		{`"A<2><STEAM_1:0:11><CT>" [100 -200 30] killed "B<3><STEAM_1:1:22><TERRORIST>" [1 2 3] with "ak47" (headshot) (penetrated)`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Headshot && k.Weapon == "ak47" }},
		{`"C<4><STEAM_1:0:33><CT>" assisted killing "B<3><STEAM_1:1:22><TERRORIST>"`,
			func(e Event) bool {
				a, ok := e.(KillAssist)
				return ok && a.Assister.Name == "C" && a.Victim.Name == "B"
			}},
	}
	cases = append(cases,
		// CS:GO: Steam3 ids, SFUI triggers, team switch
		struct {
			line string
			want func(Event) bool
		}{`Team "CT" triggered "SFUI_Notice_CTs_Win" (CT "1") (T "0")`,
			func(e Event) bool { tt, ok := e.(TeamTriggered); return ok && tt.Trigger == "CTs_Win" }},
		struct {
			line string
			want func(Event) bool
		}{`World triggered "Match_Start" on "de_dust2"`,
			func(e Event) bool { m, ok := e.(MapStarted); return ok && m.Map == "de_dust2" }},
		struct {
			line string
			want func(Event) bool
		}{`"Player-Name<12><[U:1:22]>" switched from team <Unassigned> to <CT>`,
			func(e Event) bool {
				j, ok := e.(JoinedTeam)
				return ok && j.Team == "CT" && j.Player.SteamID == "[U:1:22]"
			}},
		struct {
			line string
			want func(Event) bool
		}{`"Player-Name<12><[U:1:22]><TERRORIST>" [-225 -1829 -168] killed "Zim<20><BOT><CT>" [-476 -1709 -110] with "glock" (headshot penetrated)`,
			func(e Event) bool { k, ok := e.(Kill); return ok && k.Headshot && k.Victim.IsBot() }},
	)
	// Positions: CS:GO brackets, TF2/AMX properties.
	k, _ := ParseLine(`"A<2><STEAM_1:0:11><CT>" [100 -200 30] killed "B<3><STEAM_1:1:22><TERRORIST>" [1 2 3] with "ak47" (headshot)`)
	if kk := k.(Kill); kk.KillerPos == nil || *kk.KillerPos != [3]int{100, -200, 30} || *kk.VictimPos != [3]int{1, 2, 3} {
		t.Errorf("CS:GO positions %+v", kk)
	}
	k, _ = ParseLine(`"A<2><[U:1:22]><Red>" killed "B<3><[U:1:44]><Blue>" with "x" (attacker_position "-5 6 7") (victim_position "8 -9 10")`)
	if kk := k.(Kill); kk.KillerPos == nil || *kk.KillerPos != [3]int{-5, 6, 7} || *kk.VictimPos != [3]int{8, -9, 10} {
		t.Errorf("TF2 positions %+v", kk)
	}
	k, _ = ParseLine(`"A<2><STEAM_1:0:11><CT>" killed "B<3><STEAM_1:1:22><TERRORIST>" with "ak47"`)
	if kk := k.(Kill); kk.KillerPos != nil || kk.VictimPos != nil {
		t.Errorf("positions without any %+v", kk)
	}
	s, _ := ParseLine(`"A<2><STEAM_1:0:11><CT>" [4 5 6] committed suicide with "world"`)
	if ss := s.(Suicide); ss.Pos == nil || *ss.Pos != [3]int{4, 5, 6} {
		t.Errorf("suicide position %+v", ss)
	}
	for _, c := range cases {
		e, ok := ParseLine(c.line)
		if !ok || !c.want(e) {
			t.Errorf("%s\n  got %#v", c.line, e)
		}
	}
	for _, id := range []string{"VALVE_ID_LAN", "STEAM_ID_LAN", "VALVE_ID_PENDING", "STEAM_0:"} {
		if (Player{Name: "x", UserID: 1, SteamID: id}).Trackable() {
			t.Errorf("%s is trackable", id)
		}
	}
	// Ignored CS:GO lines must not become other events.
	for _, l := range []string{
		`"A<2><STEAM_1:0:11><CT>" purchased "ak47"`,
		`"A<2><STEAM_1:0:11><CT>" threw hegrenade [1 2 3]`,
		`"A<2><STEAM_1:0:11><CT>" left buyzone with [ weapon_knife ]`,
	} {
		if e, ok := ParseLine(l); ok {
			if _, isKill := e.(Kill); isKill {
				t.Errorf("%s parsed as %#v", l, e)
			}
		}
	}
}

// CS:GO lines of the full log detail (mp_logdetail 3), synthetic players.
func TestCSGOLines(t *testing.T) {
	k, ok := ParseLine(`"A<12><[U:1:22]><CT>" [-225 -1829 -168] killed "B<20><BOT><TERRORIST>" [-476 -1709 -110] with "awp" (headshot penetrated noscope throughsmoke attackerblind)`)
	kill, isKill := k.(Kill)
	if !ok || !isKill || !kill.Headshot || strings.Join(kill.Flags, ",") != "penetrated,noscope,throughsmoke,attackerblind" {
		t.Errorf("kill %+v", k)
	}
	k, _ = ParseLine(`"A<12><[U:1:22]><CT>" [1 2 3] killed "B<20><BOT><TERRORIST>" [4 5 6] with "ak47"`)
	if kill := k.(Kill); kill.Headshot || len(kill.Flags) != 0 {
		t.Errorf("plain kill %+v", kill)
	}
	if _, ok := ParseLine(`"A<12><[U:1:22]><CT>" [-1 2 -3] attacked "B<20><BOT><TERRORIST>" [4 -5 6] with "m4a1_silencer" (damage "27") (damage_armor "3") (health "73") (armor "96") (hitgroup "left arm")`); ok {
		t.Error("damage lines are not used (the plugin reports hits)")
	}
	f, ok := ParseLine(`"C<13><[U:1:44]><CT>" flash-assisted killing "B<20><BOT><TERRORIST>"`)
	if fa, isFa := f.(KillAssist); !ok || !isFa || !fa.Flash || fa.Assister.Name != "C" || fa.Victim.Name != "B" {
		t.Errorf("flash assist %+v", f)
	}
	f, _ = ParseLine(`"C<13><[U:1:44]><CT>" assisted killing "B<20><BOT><TERRORIST>"`)
	if fa := f.(KillAssist); fa.Flash {
		t.Errorf("assist is not a flash assist: %+v", fa)
	}
	b, ok := ParseLine(`"B<20><BOT><CT>" [10 20 30] was killed by the bomb.`)
	if wd, isWd := b.(WorldDeath); !ok || !isWd || wd.Weapon != "planted_c4" || wd.Player.Name != "B" {
		t.Errorf("bomb death %+v", b)
	}
	ac, ok := ParseLine("ACCOLADE, FINAL: {mvp},\tPlayer Name<12>,\tVALUE: 3.000000,\tPOS: 1,\tSCORE: 40.000000")
	if acc, isAcc := ac.(Accolade); !ok || !isAcc || acc.Name != "mvp" || acc.Player != "Player Name" || acc.UserID != 12 || acc.Position != 1 {
		t.Errorf("accolade %+v", ac)
	}
	for _, l := range []string{
		`ACCOLADE, FINAL: {3k},	X<5>,	VALUE: oops`,
		`"A<12><[U:1:22]><CT>" say "\"B<20><BOT><T>\" was killed by the bomb."`,
	} {
		if e, ok := ParseLine(l); ok {
			if _, bad := e.(WorldDeath); bad {
				t.Errorf("%q parsed as %T", l, e)
			}
			if _, bad := e.(Accolade); bad {
				t.Errorf("%q parsed as %T", l, e)
			}
		}
	}
}
