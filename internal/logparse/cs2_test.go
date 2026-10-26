// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import "testing"

// Counter-Strike 2 lines as they come over HTTP (logaddress_add_http): no
// "L " prefix, milliseconds and " - " after the time. Synthetic players.
func TestCS2Lines(t *testing.T) {
	for _, line := range []string{
		`10/25/2026 - 21:00:00.123 - "A<0><[U:1:22]><CT>" [-225 -1829 -168] killed "B<3><BOT><TERRORIST>" [-476 -1709 -110] with "ak47" (headshot)`,
		`L 10/25/2026 - 21:00:00.123 - "A<0><[U:1:22]><CT>" [-225 -1829 -168] killed "B<3><BOT><TERRORIST>" [-476 -1709 -110] with "ak47" (headshot)`,
		`L 10/25/2026 - 21:00:00: "A<0><[U:1:22]><CT>" [-225 -1829 -168] killed "B<3><BOT><TERRORIST>" [-476 -1709 -110] with "ak47" (headshot)`,
	} {
		e, ok := ParseLine(line)
		k, isKill := e.(Kill)
		if !ok || !isKill || !k.Headshot || k.Killer.UserID != 0 || k.Killer.Key() != "0:11" || k.KillerPos == nil || k.Weapon != "ak47" {
			t.Errorf("%q: %+v", line, e)
		}
	}
	// The first player of a CS2 server has userid 0 and is tracked.
	if e, _ := ParseLine(`10/25/2026 - 21:00:01.000 - "A<0><[U:1:22]><>" entered the game`); !e.(Entered).Player.Trackable() {
		t.Error("userid 0 must be trackable")
	}
	// CS2's demo recorder has no id: not a player.
	if e, _ := ParseLine(`10/25/2026 - 21:00:02.000 - "DemoRecorder<0><UNKNOWN><>" entered the game`); e.(Entered).Player.Trackable() {
		t.Error("DemoRecorder must not be tracked")
	}
	if e, _ := ParseLine(`10/25/2026 - 21:00:03.000 - "Romanov<5><BOT><TERRORIST>" entered the game`); !e.(Entered).Player.Trackable() {
		t.Error("an ordinary bot is tracked")
	}
	e, ok := ParseLine(`10/25/2026 - 21:00:04.500 - "A<0><[U:1:22]><CT>" [1 2 3] attacked "B<3><BOT><TERRORIST>" [4 5 6] with "usp_silencer_off" (damage "-5") (damage_armor "0") (health "100") (armor "0") (hitgroup "neck")`)
	if a, isA := e.(Attacked); !ok || !isA || a.Damage != 0 || a.Hitgroup != "neck" || a.Weapon != "usp_silencer_off" {
		t.Errorf("attacked %+v", e)
	}
	// Hostile: a name that fakes a second player tag and a huge damage.
	e, ok = ParseLine(`10/25/2026 - 21:00:04.600 - "X<9><[U:1:9]><CT>" attacked "Y<8><[U:1:8]><TERRORIST>" with "ak47" (damage "1")" [1 2 3] attacked "B<3><BOT><TERRORIST>" [4 5 6] with "ak47" (damage "99999999999999999999") (hitgroup "head")`)
	if a, isA := e.(Attacked); ok && isA && (a.Damage < 0 || a.Attacker.SteamID == "[U:1:8]") {
		t.Errorf("hostile attacked line parsed as %+v", a)
	}
	if e, ok := ParseLine(`10/25/2026 - 21:00:05.000 - Team "CT" triggered "SFUI_Notice_Target_Saved" (CT "1") (T "0")`); !ok || e.(TeamTriggered).Trigger != "Target_Saved" {
		t.Errorf("team trigger %+v", e)
	}
}

// The start line of the gameWE CounterStrikeSharp plugin. Only the exact
// form counts; a line with more after it is not the plugin's.
func TestPluginStarted(t *testing.T) {
	e, ok := ParseLine(`10/26/2026 - 21:00:00.000 - gameWE Plugin 0.2.0 (CounterStrikeSharp)`)
	if p, isP := e.(PluginStarted); !ok || !isP || p.Version != "0.2.0" || p.Platform != "CounterStrikeSharp" {
		t.Errorf("start line: %+v %v", e, ok)
	}
	for _, line := range []string{
		`10/26/2026 - 21:00:00.000 - gameWE Plugin 0.2.0 (CounterStrikeSharp) extra`,
		`10/26/2026 - 21:00:00.000 - gameWE Plugin (CounterStrikeSharp)`,
		`10/26/2026 - 21:00:00.000 - gameWE Plugin 0.2.0 ("x")`,
	} {
		if e, ok := ParseLine(line); ok {
			t.Errorf("%q parsed as %+v", line, e)
		}
	}
}
