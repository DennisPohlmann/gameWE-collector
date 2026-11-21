// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"fmt"
	"testing"
	"time"
)

// Lines that come between "Loading map" and "Started map" are of the old
// map: they are stored, but their visits end with "Started map" (no
// disconnect), so old bots do not stay next to the new ones. Players who
// are on the new map come back with their next line.
func TestMapLoadingLinesEndAtStart(t *testing.T) {
	p, st := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now, `Started map "de_dust2" (CRC "1")`,
		`"Alice<2><STEAM_1:0:1><CT>" killed "Old Bot<3><BOT><TERRORIST>" with "ak47"`)
	change := now.Add(10 * time.Minute)
	handleAll(t, p, change, `Loading map "de_nuke"`)
	if len(p.srv.players) != 0 {
		t.Fatalf("players after Loading map: %v", keys(p.srv.players))
	}
	// Late lines of the old map while the new one loads.
	handleAll(t, p, change.Add(time.Second),
		`"Old Bot<3><BOT><TERRORIST>" killed "Alice<2><STEAM_1:0:1><CT>" with "glock"`)
	if len(p.srv.players) != 2 {
		t.Fatalf("late lines: players %v", keys(p.srv.players))
	}
	handleAll(t, p, change.Add(5*time.Second), `Started map "de_nuke" (CRC "2")`)
	if len(p.srv.players) != 0 {
		t.Errorf("players after Started map: %v, want none (seen only while loading)", keys(p.srv.players))
	}
	handleAll(t, p, change.Add(8*time.Second),
		`"Alice<2><STEAM_1:0:1><>" entered the game`,
		`"New Bot<5><BOT><TERRORIST>" killed "Alice<2><STEAM_1:0:1><CT>" with "glock"`)
	if got := keys(p.srv.players); len(got) != 2 || p.srv.players["0:1"] == nil || p.srv.players["BOT:New Bot"] == nil {
		t.Errorf("players on the new map %v, want Alice and New Bot", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.kills) != 3 {
		t.Errorf("%d kills stored, want 3 (the late kill too)", len(st.kills))
	}
	if len(st.maps) != 2 {
		t.Errorf("map starts %v, want 2 (Loading and Started are one change)", st.maps)
	}
	live := 0
	for k := range st.live {
		if k[1] == p.srv.info.ID {
			live++
		}
	}
	if live != 2 {
		t.Errorf("%d live rows, want 2", live)
	}
}

// A status during the loading confirms who it lists: those stay at
// "Started map". A map change without "Loading map" ends nobody later.
func TestMapLoadingStatusConfirms(t *testing.T) {
	p, _ := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now, `Started map "de_dust2" (CRC "1")`)
	change := now.Add(10 * time.Minute)
	handleAll(t, p, change, `Loading map "de_nuke"`,
		`"Alice<2><STEAM_1:0:1><CT>" say "hi"`, `"Bob<4><STEAM_1:0:4><CT>" say "hi"`)
	status := "hostname: Test\nmap     : de_nuke at: 0 x, 0 y, 0 z\nplayers : 1 humans, 0 bots (24 max)\n# userid name uniqueid connected ping loss state adr\n" +
		"#  2 \"Alice\" STEAM_0:0:1 10:00 50 0 active 192.0.2.10:27005\n"
	if err := p.Status(t.Context(), statusOf(t, status), change.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	handleAll(t, p, change.Add(5*time.Second), `Started map "de_nuke" (CRC "2")`)
	if got := keys(p.srv.players); len(got) != 1 || got[0] != "0:1" {
		t.Errorf("players %v, want only Alice (listed by status)", got)
	}

	// CS:GO's Match_Start during the loading is the same change and ends nobody.
	next := change.Add(5 * time.Minute)
	handleAll(t, p, next, `Loading map "de_mirage"`, `"Bob<4><STEAM_1:0:4><CT>" say "hi"`,
		`World triggered "Match_Start" on "de_mirage"`)
	if p.srv.players["0:4"] == nil {
		t.Errorf("Bob ended by Match_Start: %v", keys(p.srv.players))
	}

	// Started map alone (no Loading line before): lines after it are of the new map.
	later := change.Add(10 * time.Minute)
	handleAll(t, p, later, `Started map "de_inferno" (CRC "3")`,
		`"Alice<2><STEAM_1:0:1><CT>" say "gg"`, `Started map "de_inferno" (CRC "3")`)
	if p.srv.players["0:1"] == nil {
		t.Errorf("Alice ended by a repeated Started map: %v", keys(p.srv.players))
	}
}

// One player is one visit and one live row, however often and in which
// SteamID form the lines name them; a reconnect does not add a second one.
func TestSamePlayerOnce(t *testing.T) {
	p, st := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now,
		`"Alice<2><STEAM_1:0:1><>" connected, address "192.0.2.10:27005"`,
		`"Alice<2><STEAM_1:0:1><>" entered the game`,
		`"Alice<2><[U:1:2]><CT>" say "steam3 form"`,
		`"Alice<2><STEAM_0:0:1><>" connected, address "192.0.2.10:27005"`,
		`"Alice<7><STEAM_1:0:1><CT>" entered the game`)
	if got := keys(p.srv.players); len(got) != 1 {
		t.Errorf("players %v, want one", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.live) != 1 || len(st.joins) != 1 {
		t.Errorf("live rows %v, joins %d, want 1 and 1", st.live, len(st.joins))
	}
}

// After a map change the slots still bound the tracked players: old bots
// brought back by late lines end first when the new ones come.
func TestSlotsAfterMapChange(t *testing.T) {
	p, st := newTestProcessor(t)
	p.srv.info.MaxPlayers = 4
	now := time.Now()
	handleAll(t, p, now, `Started map "de_dust2" (CRC "1")`)
	change := now.Add(10 * time.Minute)
	handleAll(t, p, change, `Started map "de_nuke" (CRC "2")`)
	for i := 0; i < 4; i++ {
		handleAll(t, p, change.Add(time.Second), fmt.Sprintf(`"Old %d<%d><BOT><CT>" say "late"`, i, 10+i))
	}
	for i := 0; i < 4; i++ {
		handleAll(t, p, change.Add(time.Duration(2+i)*time.Second), fmt.Sprintf(`"New %d<%d><BOT><CT>" entered the game`, i, 20+i))
	}
	got := keys(p.srv.players)
	if len(got) != 4 {
		t.Fatalf("players %v, want 4 (slots)", got)
	}
	for _, k := range got {
		if k[:7] != "BOT:New" {
			t.Errorf("players %v, want the 4 new bots", got)
			break
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.live) != 4 {
		t.Errorf("%d live rows, want 4", len(st.live))
	}
}

// A plugin that names the bots after they join (GunGame servers, at every
// map start): the bot under its new name is another player (BOT:<name>), so
// the old visit ends and RCON status, which lists the new name, finds the
// same visit. Renaming the old row left two live rows per bot.
func TestBotRenameIsNewPlayer(t *testing.T) {
	p, st := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now, `Started map "gg_italy" (CRC "1")`)
	// Later than mapRepeat: status ends an unlisted player only at its second miss.
	now = now.Add(5 * time.Minute)
	handleAll(t, p, now, `"Brett<3><BOT><>" entered the game`,
		`"Brett<3><BOT><Unassigned>" joined team "CT"`,
		`"Brett<3><BOT><CT>" changed name to "Rakeri"`,
		`"Joe<4><BOT><>" entered the game`, // a bot nobody renames
		`"Joe<4><BOT><Unassigned>" joined team "TERRORIST"`)
	status := "hostname: Test\nmap     : gg_italy at: 0 x, 0 y, 0 z\nplayers : 0 humans, 2 bots (24 max)\n# userid name uniqueid connected ping loss state adr\n" +
		"#  3 \"Rakeri\" BOT active\n#  4 \"Joe\" BOT active\n"
	if err := p.Status(t.Context(), statusOf(t, status), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := keys(p.srv.players); len(got) != 2 || p.srv.players["BOT:Rakeri"] == nil || p.srv.players["BOT:Joe"] == nil {
		t.Fatalf("players %v, want BOT:Joe and BOT:Rakeri", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	live := 0
	for k := range st.live {
		if k[1] == p.srv.info.ID {
			live++
		}
	}
	if live != 2 || len(st.joins) != 3 {
		t.Errorf("%d live rows, %d joins, want 2 and 3 (Brett, Rakeri, Joe)", live, len(st.joins))
	}
	if ps := p.srv.players["BOT:Joe"]; ps.live.Team != "TERRORIST" {
		t.Errorf("Joe's team %q, want TERRORIST", ps.live.Team)
	}
	if ps := p.srv.players["BOT:Rakeri"]; ps.live.Team != "CT" {
		t.Errorf("team %q, want CT (from the rename line)", ps.live.Team)
	}
}
