// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// Left 4 Dead 2 on both databases: the game's own kill, incap and tongue
// lines with the seeded data, the one-sided points for kills of the AI,
// the derived killed_<class> / killed_survivor actions and the roles.
func TestIntegrationL4D(t *testing.T) {
	db := testDB(t)
	loadSeed(t, db)
	addr := "198.51.100.4:27016"
	// The AI not ranked, kills with it worth points (what migration 15 makes
	// of gamewe_rank_bots = 0 for Left 4 Dead).
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_rank_bots_l4d2', '0')`)); err != nil {
		t.Fatal(err)
	}
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "l4d2"}})
	const (
		nick   = `"Nick<3><STEAM_1:0:71><Survivor><Gambler><ALIVE><100+0><setpos_exact 10.00 20.00 30.00; setang 0.00 0.00 0.00><Area 1>"`
		smoker = `"Smoker<5><BOT><Infected><SMOKER><DEAD><0+0><setpos_exact 40.00 50.00 60.00; setang 0.00 0.00 0.00><Area 2>"`
		hunter = `"Hunter<6><BOT><Infected><HUNTER><ALIVE><250+0><setpos_exact 1.00 2.00 3.00; setang 0.00 0.00 0.00><Area 3>"`
		tank   = `"Tank<7><BOT><Infected><TANK><ALIVE><4000+0><setpos_exact 1.00 2.00 3.00; setang 0.00 0.00 0.00><Area 4>"`
		horde  = `"infected<170><><Infected><infected><DEAD><0+0><setpos_exact 5.00 6.00 7.00; setang 0.00 0.00 0.00><Area 5>"`
	)
	now := time.Now().UTC()
	for _, l := range []string{
		`"Nick<3><STEAM_1:0:71><Unassigned>" joined team "Survivor"`,
		`(TONGUE) Tongue grab starting.  Smoker:` + smoker + `. Victim:` + nick + `.`,
		`(DEATH)` + nick + ` killed ` + smoker + ` with "autoshotgun" (headshot)`,
		`(DEATH)` + nick + ` killed ` + horde + ` with "smg"`,
		`(DEATH)` + tank + ` killed ` + horde + ` with "tank_claw"`, // infected vs infected: ignored
		`(INCAP)` + nick + ` was incapped by ` + hunter + ` with "hunter_claw"`,
		`"Nick<3><STEAM_1:0:71><Survivor>" triggered "healed_teammate"`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), now); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	player := func(col, uid string) int {
		return count(t, db, `SELECT p.`+col+` FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId
			WHERE p.game = 'l4d2' AND u.uniqueId = ?`, uid)
	}
	action := func(table, code string) int {
		return count(t, db, `SELECT COUNT(*) FROM `+table+` e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.game = 'l4d2' AND a.code = ?`, code)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		// 1000 + 5 (autoshotgun 1.00) + 1 (killed_smoker) + 0 (headshot)
		// + 6 (smg 1.20) + 5 (healed_teammate); the incap by the AI costs nothing.
		{"Nick skill", player("skill", "0:71"), 1017},
		{"Nick kills", player("kills", "0:71"), 2},
		{"Nick deaths", player("deaths", "0:71"), 1},
		{"Nick headshots", player("headshots", "0:71"), 1},
		{"Smoker skill (AI not ranked)", player("skill", "BOT:Smoker"), 1000},
		{"Hunter kills (the incap)", player("kills", "BOT:Hunter"), 1},
		{"Infected Horde deaths", player("deaths", "BOT:Infected Horde"), 1},
		{"frags", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`), 3},
		{"frag positions", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE pos_x = 10 AND pos_victim_x = 40`), 1},
		{"teamkills", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Teamkills`), 0},
		{"autoshotgun kills", count(t, db, `SELECT kills FROM hlstats_Weapons WHERE game = 'l4d2' AND code = 'autoshotgun'`), 1},
		{"killed_smoker", action("hlstats_Events_PlayerActions", "killed_smoker"), 1},
		{"healed_teammate", action("hlstats_Events_PlayerActions", "healed_teammate"), 1},
		{"tongue_grab", action("hlstats_Events_PlayerPlayerActions", "tongue_grab"), 1},
		{"killed_survivor", action("hlstats_Events_PlayerPlayerActions", "killed_survivor"), 1},
		{"Gambler kills", count(t, db, `SELECT kills FROM hlstats_Roles WHERE game = 'l4d2' AND code = 'Gambler'`), 2},
		{"SMOKER deaths", count(t, db, `SELECT deaths FROM hlstats_Roles WHERE game = 'l4d2' AND code = 'SMOKER'`), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}
