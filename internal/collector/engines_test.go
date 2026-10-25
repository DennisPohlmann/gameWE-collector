// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
)

// A GoldSrc (cstrike) server: kills, the AMX plugin's headshot trigger and
// replies through the AMX plugin's commands.
func TestGoldSrcServer(t *testing.T) {
	st := newMemStore()
	srv := st.addServers([]string{"198.51.100.16:27015"}, map[string]string{"198.51.100.16:27015": "cstrike"})[0]
	rec := &recorder{}
	p := NewProcessor(st, settings.NewLive(settings.Defaults()), srv, rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"A<1><STEAM_0:0:11><CT>" killed "B<2><STEAM_0:1:22><TERRORIST>" with "ak47" (attacker_position "1 2 3") (victim_position "4 5 6")`,
		`"A<1><STEAM_0:0:11><CT>" triggered "headshot"`,
		`"L<3><VALVE_ID_LAN><CT>" killed "B<2><STEAM_0:1:22><TERRORIST>" with "usp"`,
		`"A<1><STEAM_0:0:11><CT>" say "!rank"`,
		`"A<1><STEAM_0:0:11><CT>" say "/session"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.kills) != 1 || st.kills[0].game != "cstrike" || st.kills[0].killer != "0:11" {
		t.Errorf("kills %+v (the LAN id is not tracked)", st.kills)
	}
	sent := rec.get("198.51.100.16:27015")
	if len(sent) != 2 || !strings.HasPrefix(sent[0], `gamewe_amx_msay 15 1 0 "->1 - Total`) || !strings.HasPrefix(sent[1], `gamewe_amx_msay 15 1 0 "`) {
		t.Errorf("replies %q", sent)
	}
}

// Kill assists are counted from CS:GO's native line, the SourceMod
// plugin's kill_assist line (CS:GO, Insurgency) and TF2's "kill assist".
// CS:GO logs the native and the plugin line for the same kill: counted once.
func TestKillAssists(t *testing.T) {
	st := newMemStore()
	addrs := []string{"198.51.100.17:27015", "198.51.100.18:27015", "198.51.100.19:27015", "198.51.100.20:27015"}
	srvs := st.addServers(addrs, map[string]string{addrs[0]: "csgo", addrs[1]: "tf", addrs[2]: "csgo", addrs[3]: "insurgency"})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, now := context.Background(), time.Now()
	type line struct {
		at time.Duration
		l  string
	}
	c, b := `"C<4><STEAM_1:0:33><CT>"`, `"B<3><STEAM_1:1:22><TERRORIST>"`
	lines := map[int][]line{
		0: {{0, c + ` assisted killing ` + b}},
		1: {{0, `"C<4><[U:1:67]><Red>" triggered "kill assist" against "B<3><[U:1:44]><Blue>" (assister_position "1 2 3")`}},
		2: {
			// kill 1: native line, then the plugin's (counted once)
			{0, `"A<2><STEAM_1:0:11><CT>" [1 2 3] killed ` + b + ` [4 5 6] with "ak47"`},
			{0, c + ` assisted killing ` + b},
			{0, c + ` triggered "kill_assist" against ` + b},
			// kill 2: the plugin's line first, the native one in the next second
			{30 * time.Second, `"A<2><STEAM_1:0:11><CT>" [1 2 3] killed ` + b + ` [4 5 6] with "ak47"`},
			{30 * time.Second, c + ` triggered "kill_assist" against ` + b},
			{31 * time.Second, c + ` assisted killing ` + b},
			// kill 3: plugin only (counted)
			{60 * time.Second, c + ` triggered "kill_assist" against ` + b},
		},
		3: {{0, `"I<5><STEAM_1:0:55><#Team_Security>" triggered "kill_assist" against "J<6><STEAM_1:1:66><#Team_Insurgent>"`}},
	}
	for i, srv := range srvs {
		p := NewProcessor(st, settings.NewLive(settings.Defaults()), srv, &recorder{}, log)
		for _, l := range lines[i] {
			if err := p.Handle(ctx, l.l, now.Add(l.at)); err != nil {
				t.Fatal(err)
			}
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if got := strings.Join(st.assists, " "); got != "0:33 1:33 0:33 0:33 0:33 0:55" {
		t.Errorf("assists %v", got)
	}
	n := 0
	for _, a := range st.actions {
		if a == "0:33 kill_assist" {
			n++
		}
	}
	if n != 3 { // the native lines' kill_assist action, as before
		t.Errorf("%d kill_assist actions of 0:33, want 3: %v", n, st.actions)
	}
	n = 0
	for _, a := range st.ppas {
		if strings.HasSuffix(a, " kill_assist 1:22") || a == "0:55 kill_assist 1:66" {
			n++
		}
	}
	if n != 4 { // the plugin lines still go through the player-player actions
		t.Errorf("%d kill_assist player-player actions, want 4: %v", n, st.ppas)
	}
}

// CS 1.6: the AMX plugin logs the kill line, then `triggered "headshot"` for
// a headshot kill. The headshot belongs to the kill line before it.
func TestGoldSrcHeadshotAfterKill(t *testing.T) {
	st := newMemStore()
	addr := "198.51.100.16:27015"
	srv := st.addServers([]string{addr}, map[string]string{addr: "cstrike"})[0]
	p := NewProcessor(st, settings.NewLive(settings.Defaults()), srv, &recorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, now := context.Background(), time.Now()
	a, b, c := `"A<1><STEAM_0:0:11><CT>"`, `"B<2><STEAM_0:1:22><TERRORIST>"`, `"C<3><STEAM_0:0:33><TERRORIST>"`
	for _, l := range []string{
		a + ` killed ` + b + ` with "ak47" (attacker_position "1 2 3") (victim_position "4 5 6")`, // 1: headshot
		a + ` triggered "headshot"`,
		a + ` killed ` + c + ` with "ak47"`, // 2: no headshot (another line follows)
		b + ` say "gg"`,
		a + ` killed ` + b + ` with "deagle"`, // 3: no headshot
		a + ` killed ` + c + ` with "deagle"`, // 4: headshot
		a + ` triggered "headshot"`,
		c + ` killed ` + a + ` with "glock"`, // 5: the next line is A's headshot: not C's
		a + ` triggered "headshot"`,
		b + ` killed ` + a + ` with "knife"`, // 6: last line: recorded at the tick
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	n := len(st.kills)
	st.mu.Unlock()
	if n != 5 {
		t.Fatalf("%d kills recorded before the tick, want 5 (the last one is held)", n)
	}
	if err := p.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	var got []bool
	for _, k := range st.kills {
		got = append(got, k.headshot)
	}
	want := []bool{true, false, false, true, false, false}
	if len(got) != len(want) {
		t.Fatalf("kills %+v", st.kills)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kill %d (%s -> %s): headshot %v, want %v", i+1, st.kills[i].killer, st.kills[i].victim, got[i], want[i])
		}
	}
	hs := 0
	for _, x := range st.actions {
		if strings.HasSuffix(x, " headshot") {
			hs++
		}
	}
	if hs != 2 {
		t.Errorf("headshot actions %v, want one per headshot kill", st.actions)
	}
}

// Positions in kill lines go to hlstats_Events_Locations (killer type 1,
// victim type 2); the plugin's located line of the same kill is then skipped.
func TestKillLinePositions(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"A<2><STEAM_1:0:11><CT>" [100 -200 30] killed "B<3><STEAM_1:1:22><TERRORIST>" [1 2 3] with "ak47" (headshot)`,
		`"A<2><STEAM_1:0:11><CT>" located on "kill" (position "100 -200 30") against "B<3><STEAM_1:1:22><TERRORIST>" (victim_position "1 2 3")`,
		`"B<3><STEAM_1:1:22><TERRORIST>" [4 5 6] committed suicide with "world"`,
		`"A<2><STEAM_1:0:11><CT>" killed "C<4><STEAM_1:0:33><TERRORIST>" with "ak47" (attacker_position "-5 6 7") (victim_position "8 -9 10")`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	a, b, c := p.srv.players["0:11"].live.ID, p.srv.players["1:22"].live.ID, p.srv.players["0:33"].live.ID
	want := []string{
		fmt.Sprintf("%d:1:100,-200", a), fmt.Sprintf("%d:2:1,2", b), fmt.Sprintf("%d:2:4,5", b),
		fmt.Sprintf("%d:1:-5,6", a), fmt.Sprintf("%d:2:8,-9", c),
	}
	if strings.Join(st.locations, " ") != strings.Join(want, " ") {
		t.Errorf("locations %v, want %v", st.locations, want)
	}
}

// Players alive at the end of a won round survived it.
func TestSurvivedRounds(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`World triggered "Round_Start"`,
		`"A<2><STEAM_1:0:1><CT>" killed "B<3><STEAM_1:0:2><TERRORIST>" with "ak47"`,
		`"C<4><STEAM_1:0:3><TERRORIST>" triggered "Planted_The_Bomb"`,
		`"D<5><STEAM_1:0:4><Spectator>" say "hi"`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
		`World triggered "Round_Start"`,
		`Team "TERRORIST" triggered "Terrorists_Win" (CT "1") (T "1")`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.rounds) != 2 || strings.Join(st.rounds[0].survivors, ",") != "0:1,0:3" || strings.Join(st.rounds[1].survivors, ",") != "0:1,0:2,0:3" {
		t.Errorf("rounds %+v", st.rounds)
	}
}

// The time in a team and a role is stored when it changes and when the
// visit ends; spectator time counts for neither.
func TestPlayTime(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, t0 := context.Background(), time.Now()
	for _, l := range []struct {
		at   int
		line string
	}{
		{0, `"A<2><STEAM_1:0:1><Unassigned>" joined team "CT"`},
		{10, `"A<2><STEAM_1:0:1><CT>" changed role to "gsg9"`},
		{60, `"A<2><STEAM_1:0:1><CT>" joined team "TERRORIST"`},
		{100, `"A<2><STEAM_1:0:1><TERRORIST>" joined team "Spectator"`},
		{130, `"A<2><STEAM_1:0:1><Spectator>" disconnected (reason "Disconnect")`},
	} {
		if err := p.Handle(ctx, l.line, t0.Add(time.Duration(l.at)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	want := map[string]int{"team:CT": 60, "team:TERRORIST": 40, "role:gsg9": 90}
	for k, v := range want {
		if st.playTime[k] != v {
			t.Errorf("%s = %d, want %d (all %v)", k, st.playTime[k], v, st.playTime)
		}
	}
	if len(st.playTime) != len(want) {
		t.Errorf("play time %v, want %v", st.playTime, want)
	}
}

// While nothing changes, the team and role time is stored every minute
// (Tick), not only at the next change or the end of the visit; nothing is
// counted twice.
func TestPlayTimeFlush(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, t0 := context.Background(), time.Now()
	for _, l := range []string{
		`"A<2><STEAM_1:0:1><Unassigned>" joined team "CT"`,
		`"A<2><STEAM_1:0:1><CT>" changed role to "gsg9"`,
	} {
		if err := p.Handle(ctx, l, t0); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []int{30, 90, 100, 150} { // flushed at 90 and 150
		if err := p.Tick(ctx, t0.Add(time.Duration(at)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	got := map[string]int{"team:CT": st.playTime["team:CT"], "role:gsg9": st.playTime["role:gsg9"]}
	st.mu.Unlock()
	if got["team:CT"] != 150 || got["role:gsg9"] != 150 {
		t.Errorf("after 150 s: %v, want 150 each", got)
	}
	if err := p.Handle(ctx, `"A<2><STEAM_1:0:1><CT>" disconnected (reason "Disconnect")`, t0.Add(170*time.Second)); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.playTime["team:CT"] != 170 || st.playTime["role:gsg9"] != 170 {
		t.Errorf("at the disconnect: %v, want 170 each", st.playTime)
	}
}
