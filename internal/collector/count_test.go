// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
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

// Map change with 32 bots: the players connect again and new bots come,
// while late lines of the old bots bring them back for a moment. The count stays at 32 meanwhile, neither 0 nor up to
// the slots, and follows the server again once status settled it.
func TestMapChangeHoldsPlayerCount(t *testing.T) {
	p, st := newTestProcessor(t)
	s := settings.Defaults()
	s.IdleTimeout = 0
	p.set.Set(s)
	ctx := context.Background()
	t0 := time.Now()
	bots := func(prefix string) string {
		out := strings.Replace(statusHead, "24 max", "48 max", 1)
		for i := 0; i < 32; i++ {
			out += fmt.Sprintf("# %d \"%s %d\" BOT active\n", 10+i, prefix, i)
		}
		return out
	}
	status := func(at time.Time, out string) {
		t.Helper()
		if err := p.Status(ctx, statusOf(t, out), at); err != nil {
			t.Fatal(err)
		}
	}
	tick := func(at time.Time) {
		t.Helper()
		if err := p.Tick(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	handleAll(t, p, t0, `Started map "de_dust2" (CRC "1")`)
	status(t0.Add(time.Second), bots("Old"))
	if n := p.online(t0.Add(time.Second)); n != 32 {
		t.Fatalf("online %d, want 32", n)
	}

	start := t0.Add(LoadInterval + 2*time.Second)
	tick(start) // starts the interval of the map change (no row: no line or status for an interval)
	change := start.Add(LoadInterval - 5*time.Second)
	handleAll(t, p, change, `Loading map "de_nuke"`)
	if n := p.online(change); n != 32 {
		t.Errorf("online right after the map change %d, want 32 (held)", n)
	}
	// Late lines of the old bots, then the new bots.
	for i := 0; i < 32; i++ {
		handleAll(t, p, change.Add(time.Second), fmt.Sprintf(`"Old %d<%d><BOT><CT>" killed "Old %d<%d><BOT><TERRORIST>" with "ak47"`, i, 10+i, (i+1)%32, 10+(i+1)%32))
	}
	for i := 0; i < 32; i++ {
		handleAll(t, p, change.Add(3*time.Second), fmt.Sprintf(`"New %d<%d><BOT><CT>" entered the game`, i, 50+i))
	}
	tick(change.Add(10 * time.Second)) // load row inside the settle time
	newStatus := strings.Replace(bots("New"), "de_dust2", "de_nuke", 1)
	status(change.Add(10*time.Second), newStatus) // misses the old bots
	if got := len(p.srv.players); got != 32 {
		t.Errorf("%d players tracked after the first status, want 32", got)
	}

	// After the settle time the real count: everyone left.
	empty := strings.Replace(statusHead, "de_dust2", "de_nuke", 1)
	later := change.Add(mapSettle + time.Second)
	status(later, empty)
	status(later.Add(10*time.Second), empty)
	tick(change.Add(LoadInterval + 15*time.Second))

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.loads) != 2 {
		t.Fatalf("%d load rows: %+v", len(st.loads), st.loads)
	}
	if l := st.loads[0]; l.Players != 32 || l.MinPlayers != 32 || l.MaxPlayers != 32 {
		t.Errorf("load row during the map change %+v, want 32 players (32-32)", l)
	}
	if l := st.loads[1]; l.Players != 0 || l.MaxPlayers != 32 {
		t.Errorf("load row after everyone left %+v, want 0 players", l)
	}
	for _, n := range st.active {
		if n > 32 {
			t.Errorf("act_players written as %d, more than the 32 bots: %v", n, st.active)
			break
		}
	}
	if len(st.active) == 0 || st.active[len(st.active)-1] != 0 {
		t.Errorf("act_players writes %v, want 0 last", st.active)
	}
}

// Once the map change's hold is over, the count is what status lists: old
// bots brought back by lines after the last status do not add to the 32
// on the server (they used to, up to the 48 slots).
func TestStatusCountAfterMapChange(t *testing.T) {
	p, st := newTestProcessor(t)
	s := settings.Defaults()
	s.IdleTimeout = 0
	p.set.Set(s)
	ctx := context.Background()
	t0 := time.Now()
	bots := func(m, prefix string) string {
		out := strings.Replace(strings.Replace(statusHead, "24 max", "48 max", 1), "de_dust2", m, 1)
		for i := 0; i < 32; i++ {
			out += fmt.Sprintf("# %d \"%s %d\" BOT active\n", 10+i, prefix, i)
		}
		return out
	}
	status := func(at time.Time, out string) {
		t.Helper()
		if err := p.Status(ctx, statusOf(t, out), at); err != nil {
			t.Fatal(err)
		}
	}
	handleAll(t, p, t0, `Started map "gg_a" (CRC "1")`)
	status(t0.Add(time.Second), bots("gg_a", "Old"))
	start := t0.Add(LoadInterval + 2*time.Second)
	if err := p.Tick(ctx, start); err != nil {
		t.Fatal(err)
	}
	change := start.Add(time.Minute)
	handleAll(t, p, change, `Loading map "gg_b"`)
	for i := 0; i < 32; i++ {
		handleAll(t, p, change.Add(3*time.Second), fmt.Sprintf(`"New %d<%d><BOT><CT>" entered the game`, i, 50+i))
	}
	at := change.Add(10 * time.Second)
	for ; at.Before(change.Add(LoadInterval)); at = at.Add(10 * time.Second) {
		status(at, bots("gg_b", "New"))
		// Lines of old bots between two polls, after the hold.
		if at.After(change.Add(mapSettle)) && at.Before(change.Add(mapSettle+time.Minute)) {
			for i := 0; i < 16; i++ {
				handleAll(t, p, at.Add(time.Second), fmt.Sprintf(`"Old %d<%d><BOT><CT>" killed "New %d<%d><BOT><TERRORIST>" with "ak47"`, i, 10+i, i, 50+i))
			}
			if n := p.online(at.Add(2 * time.Second)); n != 32 {
				t.Fatalf("online %d with %d tracked, want the 32 of status", n, len(p.srv.players))
			}
		}
	}
	if err := p.Tick(ctx, start.Add(LoadInterval+time.Second)); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.loads) != 1 {
		t.Fatalf("%d load rows: %+v", len(st.loads), st.loads)
	}
	if l := st.loads[0]; l.Players != 32 || l.MinPlayers != 32 || l.MaxPlayers != 32 {
		t.Errorf("load row %+v, want 32 players (32-32)", l)
	}
	for _, n := range st.active {
		if n != 32 {
			t.Errorf("act_players writes %v, want only 32", st.active)
			break
		}
	}
}
