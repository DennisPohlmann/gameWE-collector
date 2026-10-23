// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func keys(m map[string]*playerState) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func handleAll(t *testing.T, p *Processor, at time.Time, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if err := p.Handle(context.Background(), l, at); err != nil {
			t.Fatal(err)
		}
	}
}

// Never more players than the server has slots: bots kicked by bot_quota
// without a disconnect line end when new ones come.
func TestTrimToMaxPlayers(t *testing.T) {
	p, st := newTestProcessor(t)
	p.srv.info.MaxPlayers = 4
	now := time.Now()
	for i := 0; i < 10; i++ {
		handleAll(t, p, now.Add(time.Duration(i)*time.Second),
			fmt.Sprintf(`"Bot %d<%d><BOT><CT>" triggered "Planted_The_Bomb"`, i, 10+i))
	}
	if len(p.srv.players) != 4 {
		t.Fatalf("players %v, want the last 4", keys(p.srv.players))
	}
	for i := 6; i < 10; i++ {
		if p.srv.players[fmt.Sprintf("BOT:Bot %d", i)] == nil {
			t.Errorf("Bot %d (seen last) ended", i)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.leaves) != 6 {
		t.Errorf("%d visits ended, want 6", len(st.leaves))
	}
}

// Lines of a player after the logged disconnect start no new visit; a
// reconnect does, also with a new userid.
func TestNoVisitAfterDisconnect(t *testing.T) {
	p, _ := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now,
		`"A<2><STEAM_1:0:1><CT>" entered the game`,
		`"A<2><STEAM_1:0:1><CT>" disconnected (reason "Disconnect")`,
		`"A<2><STEAM_1:0:1><CT>" triggered "Planted_The_Bomb"`,
		`"A<2><STEAM_1:0:1><>" say "late"`)
	if len(p.srv.players) != 0 {
		t.Fatalf("players %v after the disconnect", keys(p.srv.players))
	}
	handleAll(t, p, now.Add(5*time.Second),
		`"A<7><STEAM_1:0:1><>" connected, address "192.0.2.10:27005"`,
		`"A<7><STEAM_1:0:1><>" entered the game`,
		`"A<9><[U:1:2]><CT>" triggered "Planted_The_Bomb"`) // same player, Steam3 form, another userid
	if len(p.srv.players) != 1 {
		t.Errorf("players %v after the reconnect, want 1", keys(p.srv.players))
	}
}

// A server that sends every line twice (registered twice): the copies are
// skipped once that is clear; a line repeated by the game stays counted.
func TestDuplicateLines(t *testing.T) {
	p, st := newTestProcessor(t)
	now := time.Now()
	var lines []string
	for i := 0; i < 6; i++ {
		l := fmt.Sprintf(`L 10/23/2026 - 12:00:%02d: "K<2><STEAM_1:0:1><CT>" killed "V%d<%d><STEAM_1:0:%d><TERRORIST>" with "ak47"`, i, i, 10+i, 10+i)
		lines = append(lines, l, l)
	}
	handleAll(t, p, now, lines...)
	st.mu.Lock()
	kills := len(st.kills)
	st.mu.Unlock()
	if kills != 6+2 { // the first two copies come before the third pair shows the doubled stream
		t.Errorf("%d kills, want 8", kills)
	}

	// One player saying the same twice in a second is counted twice.
	q, st2 := newTestProcessor(t)
	k := `L 10/23/2026 - 12:00:00: "K<2><STEAM_1:0:1><CT>" killed "V<3><STEAM_1:0:2><TERRORIST>" with "ak47"`
	handleAll(t, q, now, k, k, `L 10/23/2026 - 12:00:01: "K<2><STEAM_1:0:1><CT>" say "gg"`)
	st2.mu.Lock()
	defer st2.mu.Unlock()
	if len(st2.kills) != 2 {
		t.Errorf("%d kills, want 2 (a single repeated line is real)", len(st2.kills))
	}
}

// Map change on a 24/7 server (same map): every visit ends, the bots of
// the new map are the only ones; old bots without a disconnect line do
// not stay.
func TestSameMapRestartBots(t *testing.T) {
	p, _ := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now, `Started map "gg_simpsons" (CRC "1")`)
	for i := 0; i < 5; i++ {
		handleAll(t, p, now.Add(time.Second), fmt.Sprintf(`"Old %d<%d><BOT><CT>" entered the game`, i, 10+i))
	}
	later := now.Add(10 * time.Minute)
	handleAll(t, p, later, `Loading map "gg_simpsons"`, `Started map "gg_simpsons" (CRC "1")`)
	for i := 0; i < 5; i++ {
		handleAll(t, p, later.Add(time.Second), fmt.Sprintf(`"New %d<%d><BOT><CT>" entered the game`, i, 20+i))
	}
	got := keys(p.srv.players)
	if len(got) != 5 || !strings.HasPrefix(got[0], "BOT:New") {
		t.Errorf("players %v, want the 5 new bots", got)
	}
}

// Bots without any line for botIdle end (kicked without a disconnect line).
func TestBotIdle(t *testing.T) {
	p, _ := newTestProcessor(t)
	now := time.Now()
	handleAll(t, p, now, `"Bot<5><BOT><CT>" entered the game`, `"A<2><STEAM_1:0:1><CT>" entered the game`)
	if err := p.Tick(context.Background(), now.Add(botIdle+time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := keys(p.srv.players); len(got) != 1 || got[0] != "0:1" {
		t.Errorf("players %v, want only the human", got)
	}
}
