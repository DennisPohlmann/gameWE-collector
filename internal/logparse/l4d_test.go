// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import "testing"

// Left 4 Dead lines in the format HLstatsX:CE reads (made-up names and ids).
func TestL4DLines(t *testing.T) {
	const (
		nick   = `"Nick<3><STEAM_1:0:11><Survivor><Gambler><ALIVE><100+0><setpos_exact 10.40 -20.60 30.00; setang 0.00 90.00 0.00><Area 1234>"`
		smoker = `"Smoker<5><BOT><Infected><SMOKER><DEAD><0+0><setpos_exact 40.00 50.00 60.00; setang 0.00 0.00 0.00><Area 99>"`
		horde  = `"infected<170><><Infected><infected><DEAD><0+0><setpos_exact 1.00 2.00 3.00; setang 0.00 0.00 0.00><Area 7>"`
	)
	e, ok := ParseLine(`L 10/26/2026 - 00:00:00: (DEATH)` + nick + ` killed ` + smoker + ` with "autoshotgun" (headshot)`)
	k, isKill := e.(Kill)
	if !ok || !isKill || k.Killer.Name != "Nick" || k.Killer.SteamID != "STEAM_1:0:11" || k.Killer.Team != "Survivor" ||
		k.Victim.Name != "Smoker" || !k.Victim.IsBot() || k.Weapon != "autoshotgun" || !k.Headshot ||
		k.KillerRole != "Gambler" || k.VictimRole != "SMOKER" || k.Incap ||
		k.KillerPos == nil || *k.KillerPos != [3]int{10, -21, 30} || k.VictimPos == nil || *k.VictimPos != [3]int{40, 50, 60} {
		t.Fatalf("DEATH kill: %+v %v", e, ok)
	}

	// The common infected have no SteamID: the bot "Infected Horde".
	e, ok = ParseLine(nick + ` killed ` + horde + ` with "smg"`)
	if k, isKill := e.(Kill); !ok || !isKill || k.Victim.Name != "Infected Horde" || k.Victim.Key() != "BOT:Infected Horde" ||
		!k.Victim.Trackable() || k.VictimRole != "infected" {
		t.Fatalf("horde kill: %+v %v", e, ok)
	}

	// "V was incapped by A" is a kill of A.
	e, ok = ParseLine(`(INCAP)` + nick + ` was incapped by ` + smoker + ` with "smoker_claw"`)
	if k, isKill := e.(Kill); !ok || !isKill || !k.Incap || k.Killer.Name != "Smoker" || k.Victim.Name != "Nick" ||
		k.Weapon != "smoker_claw" || k.KillerRole != "SMOKER" || k.VictimRole != "Gambler" {
		t.Fatalf("incap: %+v %v", e, ok)
	}

	e, ok = ParseLine(`(TONGUE) Tongue grab starting.  Smoker:` + smoker + `. Victim:` + nick + `.`)
	if p, isTrig := e.(PlayerTriggered); !ok || !isTrig || p.Trigger != "tongue_grab" || p.Player.Name != "Smoker" ||
		p.Victim == nil || p.Victim.Name != "Nick" {
		t.Fatalf("tongue: %+v %v", e, ok)
	}

	// The plugin's lines keep the usual four fields.
	e, ok = ParseLine(`"Nick<3><STEAM_1:0:11><Survivor>" triggered "healed_teammate"`)
	if p, isTrig := e.(PlayerTriggered); !ok || !isTrig || p.Trigger != "healed_teammate" {
		t.Fatalf("plugin action: %+v %v", e, ok)
	}

	// A chat text that looks like a tag stays chat.
	e, ok = ParseLine(`"Nick<3><STEAM_1:0:11><Survivor>" say "x" killed ` + smoker + ` with "smg"`)
	if _, isSay := e.(Say); !ok || !isSay {
		t.Fatalf("chat: %+v %v", e, ok)
	}

	// A role that is not a plain word is not taken (each role is a table row).
	e, _ = ParseLine(`(DEATH)"N<3><STEAM_1:0:11><Survivor><Gam bler!><ALIVE>" killed ` + smoker + ` with "smg"`)
	if k, _ := e.(Kill); k.KillerRole != "" || k.VictimRole != "SMOKER" {
		t.Fatalf("odd role: %+v", e)
	}

	for _, l := range []string{`(INCAP)garbage`, `(TONGUE) Tongue grab starting.`, `(CHECKPOINT) something`} {
		if e, ok := ParseLine(l); ok || e != nil {
			t.Errorf("%q: %+v %v", l, e, ok)
		}
	}
}
