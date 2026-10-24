// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/stats"
)

func statusOf(t *testing.T, out string) Status {
	t.Helper()
	st, err := ParseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

const statusHead = "hostname: Test\nmap     : de_dust2 at: 0 x, 0 y, 0 z\nplayers : 0 humans, 0 bots (24 max)\n# userid name uniqueid connected ping loss state adr\n"

// A server without log lines (empty, or after a collector restart) is
// filled from status alone.
func TestStatusEmptyServer(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for i := 0; i < 3; i++ {
		if err := p.Status(ctx, statusOf(t, statusHead), now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.status) != 1 || st.status[0] != "1:24:0" {
		t.Errorf("server status writes %v, want one (unchanged polls write nothing)", st.status)
	}
	if len(st.maps) != 1 || st.maps[0] != "de_dust2" || len(st.joins) != 0 {
		t.Errorf("maps %v joins %v", st.maps, st.joins)
	}
}

func TestStatusBotsAndPlayers(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now().Truncate(time.Second)
	out := statusHead + `#  2 "Alice" STEAM_0:1:4321 12:01 80 0 active 192.0.2.10:27005
#  3 "Bot Bob" BOT active
#  4 "Bot Ann" BOT active
`
	for i := 0; i < 2; i++ {
		if err := p.Status(ctx, statusOf(t, out), now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.srv.players) != 3 || p.srv.players["BOT:Bot Bob"] == nil || p.srv.players["1:4321"] == nil {
		t.Fatalf("players %v", p.srv.players)
	}
	st.mu.Lock()
	if len(st.joins) != 3 || len(st.live) != 3 {
		t.Errorf("joins %v live %v", st.joins, st.live)
	}
	alice := p.srv.players["1:4321"].live.ID
	if got := st.connected[alice]; got != now.Add(-12*time.Minute-time.Second).Unix() {
		t.Errorf("connected %d, want %d", got, now.Add(-12*time.Minute-time.Second).Unix())
	}
	if len(st.status) != 1 || st.status[0] != "1:24:3" {
		t.Errorf("status writes %v (pings only when changed)", st.status)
	}
	st.mu.Unlock()

	// A log line for a known player does not start a second visit.
	if err := p.Handle(ctx, `"Bot Bob<3><BOT><CT>" killed "Alice<2><STEAM_0:1:4321><TERRORIST>" with "ak47"`, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.joins) != 3 || len(st.kills) != 1 {
		t.Errorf("joins %v kills %v", st.joins, st.kills)
	}
}

func TestStatusMapChange(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	if err := p.Status(ctx, statusOf(t, statusHead), now); err != nil {
		t.Fatal(err)
	}
	other := "hostname: Test\nmap     : cs_office\nplayers : 0 humans, 0 bots (24 max)\n"
	if err := p.Status(ctx, statusOf(t, other), now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	// The log line of the same map change is not counted again; the next one is.
	for _, l := range []string{`Started map "cs_office" (CRC "1")`, `Started map "de_nuke" (CRC "2")`} {
		if err := p.Handle(ctx, l, now.Add(11*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if got := len(st.maps); got != 3 || st.maps[1] != "cs_office" || st.maps[2] != "de_nuke" {
		t.Errorf("map starts %v, want de_dust2 cs_office de_nuke", st.maps)
	}
}

func TestStatusPlayerLeaves(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	both := statusHead + "#  2 \"Alice\" STEAM_0:1:1 00:10 50 0 active 192.0.2.10:27005\n#  3 \"Bot Bob\" BOT active\n"
	alone := statusHead + "#  3 \"Bot Bob\" BOT active\n"
	steps := []struct {
		out     string
		players int
	}{{both, 2}, {alone, 2}, {alone, 1}, {alone, 1}}
	for i, s := range steps {
		at := now // the first poll starts the map; later ones are past the map change
		if i > 0 {
			at = now.Add(mapRepeat + time.Duration(i)*10*time.Second)
		}
		if err := p.Status(ctx, statusOf(t, s.out), at); err != nil {
			t.Fatal(err)
		}
		if len(p.srv.players) != s.players {
			t.Fatalf("poll %d: %d players, want %d (a player leaves after %d misses)", i, len(p.srv.players), s.players, statusMisses)
		}
	}
	st.mu.Lock()
	if len(st.leaves) != 1 {
		t.Errorf("leaves %v", st.leaves)
	}
	st.mu.Unlock()

	// A disconnect logged after a poll that still lists the player does not
	// bring the player back.
	t0 := now.Add(time.Minute)
	if err := p.Status(ctx, statusOf(t, both), t0); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, `"Alice<2><STEAM_0:1:1><CT>" disconnected (reason "Disconnect")`, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := p.Status(ctx, statusOf(t, both), t0.Add(time.Second)); err != nil { // polled before the disconnect line
		t.Fatal(err)
	}
	if p.srv.players["1:1"] != nil {
		t.Error("player back after the logged disconnect")
	}
}

// countingGeo is fakeGeo that counts lookups.
type countingGeo struct{ n *int }

func (g countingGeo) Lookup(path string, ip netip.Addr) (stats.Location, bool) {
	*g.n++
	return fakeGeo{}.Lookup(path, ip)
}

// Players seen only in status (joined before the collector started) get
// their flag; each address is looked up once.
func TestStatusLocation(t *testing.T) {
	p, st := newTestProcessor(t)
	lookups := 0
	p.Geo = countingGeo{&lookups}
	ctx, now := context.Background(), time.Now()
	out := statusHead + `#  2 "Alice" STEAM_0:1:1 10:00 50 0 active 203.0.113.2:27005
#  3 "Lan" STEAM_0:1:2 10:00 5 0 active 192.168.1.5:27005
#  4 "Bot Bob" BOT active
`
	for i := 0; i < 3; i++ {
		if err := p.Status(ctx, statusOf(t, out), now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	alice := p.srv.players["1:1"].live.ID
	if len(st.geo) != 1 || st.geo[0] != fmt.Sprintf("%d:de", alice) {
		t.Errorf("locations %v, want only Alice once", st.geo)
	}
	if lookups != 3 {
		t.Errorf("%d GeoIP lookups, want 3 (the server, Alice and the LAN address, once each)", lookups)
	}
}

// A player first seen in a kill line (on the server before the collector
// started) gets his connect with the address from status, once per visit;
// a player whose connect line was seen gets no second one.
func TestStatusStoresMissedConnect(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"Bob<3><STEAM_0:1:2><>" connected, address "198.51.100.3:27005"`,
		`"Alice<2><STEAM_0:1:1><CT>" killed "Bob<3><STEAM_0:1:2><TERRORIST>" with "ak47"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	out := statusHead + `#  2 "Alice" STEAM_0:1:1 10:00 50 0 active 203.0.113.2:27005
#  3 "Bob" STEAM_0:1:2 10:00 50 0 active 198.51.100.3:27005
#  4 "Bot Ann" BOT active
`
	// The first poll also changes the map (de_dust2): the visits restart,
	// but nobody connected again.
	for i := 1; i <= 3; i++ {
		if err := p.Status(ctx, statusOf(t, out), now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	alice, bob := p.srv.players["1:1"].live.ID, p.srv.players["1:2"].live.ID
	want := []string{fmt.Sprintf("%d:198.51.100.3", bob), fmt.Sprintf("%d:203.0.113.2", alice)}
	st.mu.Lock()
	got := strings.Join(st.connects, ",")
	st.mu.Unlock()
	if got != strings.Join(want, ",") {
		t.Errorf("connects %v, want %v", got, want)
	}
	// After her disconnect line Alice's next visit is a new connection.
	later := now.Add(time.Minute)
	if err := p.Handle(ctx, `"Alice<2><STEAM_0:1:1><CT>" disconnected (reason "bye")`, later); err != nil {
		t.Fatal(err)
	}
	if err := p.Status(ctx, statusOf(t, out), later.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if n := len(st.connects); n != 3 || st.connects[2] != fmt.Sprintf("%d:203.0.113.2", alice) {
		t.Errorf("connects after reconnect %v", st.connects)
	}
}

func TestConnectType(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"Alice<2><STEAM_1:0:1><>" connected, address "192.0.2.1:27005"`,
		`"Alice<2><STEAM_1:0:1><>" setup "cl_language" (value "german")`,
		`"Alice<2><STEAM_1:0:1><>" setup "cl_connectmethod" (value "serverbrowser_favorites")`,
		`"Bob<3><STEAM_1:0:2><>" setup "cl_connectmethod" (value "")`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	alice := p.srv.players["0:1"].live.ID
	if len(st.connectTypes) != 1 || st.connectTypes[alice] != stats.ConnectFavorites {
		t.Errorf("connect types %v", st.connectTypes)
	}
	for v, want := range map[string]int{"serverbrowser_internet": 1, "serverbrowser_history": 3, "steam": 6, "quickplay_1": 8, "redirect": 9, "something": 10, "": 0} {
		if got := stats.ConnectType(v); got != want {
			t.Errorf("ConnectType(%q) = %d, want %d", v, got, want)
		}
	}
}

// Humans with a ping get one sample per LatencyInterval; bots and ping 0 none.
func TestStatusLatencySamples(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	out := statusHead + `#  2 "Alice" STEAM_0:1:1 10:00 50 0 active 192.0.2.10:27005
#  3 "Lan" STEAM_0:1:2 10:00 0 0 active 192.168.1.5:27005
#  4 "Bot Bob" BOT active
`
	for i := 0; i < 8; i++ { // 0 .. 70 s
		if err := p.Status(ctx, statusOf(t, out), now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	alice := p.srv.players["1:1"].live.ID
	want := fmt.Sprintf("%d:50", alice)
	if len(st.latency) != 2 || st.latency[0] != want || st.latency[1] != want {
		t.Errorf("samples %v, want 2 of %s (at 0 s and 60 s)", st.latency, want)
	}
}

// The live row's team follows the player tag of every log line: bots and
// players first seen in status (no team there) get theirs from the next line.
func TestLiveTeamFromAnyLine(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	out := statusHead + "#  3 \"Bot Bob\" BOT active\n#  2 \"Alice\" STEAM_0:1:1 10:00 50 0 active 192.0.2.10:27005\n"
	if err := p.Status(ctx, statusOf(t, out), now); err != nil {
		t.Fatal(err)
	}
	bob, alice := p.srv.players["BOT:Bot Bob"].live.ID, p.srv.players["1:1"].live.ID
	for _, l := range []string{
		`"Bot Bob<3><BOT><CT>" killed "Alice<2><STEAM_0:1:1><TERRORIST>" with "m4a1"`,
		`"Bot Bob<3><BOT><CT>" killed "Alice<2><STEAM_0:1:1><TERRORIST>" with "m4a1"`, // unchanged: no write
		`"Bot Bob<3><BOT><CT>" triggered "Defused_The_Bomb"`,
		`"Alice<2><STEAM_0:1:1><TERRORIST>" joined team "Spectator"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.teams[bob] != "CT" || st.teams[alice] != "Spectator" {
		t.Errorf("live teams: bot %q, Alice %q", st.teams[bob], st.teams[alice])
	}
	if st.teamWrites != 3 { // bot CT, Alice TERRORIST, Alice Spectator
		t.Errorf("%d team writes, want 3", st.teamWrites)
	}
}

// A map change ends every visit and clears the live view: the bots of the
// old map do not stay next to the new ones.
func TestMapChangeEndsVisits(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	line := func(at time.Time, l string) {
		t.Helper()
		if err := p.Handle(ctx, l, at); err != nil {
			t.Fatal(err)
		}
	}
	line(now, `Started map "de_dust2" (CRC "1")`)
	line(now, `"Alice<2><STEAM_1:0:1><CT>" killed "Bot Bob<3><BOT><TERRORIST>" with "ak47"`)
	line(now, `"Bot Ann<4><BOT><CT>" killed "Bot Bob<3><BOT><TERRORIST>" with "m4a1"`)
	if len(p.srv.players) != 3 {
		t.Fatalf("players %v", p.srv.players)
	}
	later := now.Add(10 * time.Minute)
	line(later, `Loading map "de_nuke"`)
	if len(p.srv.players) != 0 {
		t.Errorf("players after the map change: %v", p.srv.players)
	}
	line(later.Add(time.Second), `Started map "de_nuke" (CRC "2")`) // the same change
	// New bots with other names, Alice stays: they come back with their lines.
	line(later.Add(5*time.Second), `"Alice<2><STEAM_1:0:1><>" entered the game`)
	line(later.Add(6*time.Second), `"Bot Cid<5><BOT><TERRORIST>" killed "Alice<2><STEAM_1:0:1><CT>" with "glock"`)
	if len(p.srv.players) != 2 || p.srv.players["0:1"] == nil || p.srv.players["BOT:Bot Cid"] == nil {
		t.Errorf("players on the new map: %v", p.srv.players)
	}
	st.mu.Lock()
	if len(st.maps) != 2 || len(st.leaves) != 3 {
		t.Errorf("map starts %v (the second map line is the same change), leaves %v", st.maps, st.leaves)
	}
	if len(st.kills) != 3 {
		t.Errorf("kills %d", len(st.kills))
	}
	st.mu.Unlock()

	// A map change seen by the status poll first: the listed players come back at once.
	out := "hostname: Test\nmap     : de_inferno at: 0 x, 0 y, 0 z\nplayers : 1 humans, 1 bots (24 max)\n# userid name uniqueid connected ping loss state adr\n" +
		"#  2 \"Alice\" STEAM_0:0:1 10:00 50 0 active 192.0.2.10:27005\n#  6 \"Bot Dan\" BOT active\n"
	if err := p.Status(ctx, statusOf(t, out), later.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(p.srv.players) != 2 || p.srv.players["BOT:Bot Dan"] == nil || p.srv.players["BOT:Bot Cid"] != nil {
		t.Errorf("players after a status map change: %v", p.srv.players)
	}
	line(later.Add(10*time.Minute+time.Second), `Started map "de_inferno" (CRC "3")`)
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.maps) != 3 {
		t.Errorf("map starts %v", st.maps)
	}
}

// Right after a map change a player the status does not list is gone at
// once (bots of the old map, kicked by bot_quota without a disconnect line).
func TestStatusAfterMapChange(t *testing.T) {
	p, _ := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`Started map "gg_simpsons" (CRC "1")`,
		`"Bot Ann<5><BOT><CT>" killed "Bot Ben<6><BOT><TERRORIST>" with "knife"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	status := strings.Replace(statusHead, "de_dust2", "gg_simpsons", 1) + "#  7 \"Bot Cid\" BOT active\n"
	if err := p.Status(ctx, statusOf(t, status), now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(p.srv.players) != 1 || p.srv.players["BOT:Bot Cid"] == nil {
		t.Errorf("players %v, want only Bot Cid", keys(p.srv.players))
	}
}
