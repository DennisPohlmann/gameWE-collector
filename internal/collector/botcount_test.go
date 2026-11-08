// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/stats"
)

// Bots in the status formats of the engines: Source (CS:S, TF2, L4D2,
// DoD:S, HL2DM) lists "BOT" in the uniqueid column, CS:GO with a slot
// number before the userid, GoldSrc in its own column order; SourceTV is a
// "BOT" too (the engine counts it in "N bots").
func TestStatusBotFormats(t *testing.T) {
	for name, tc := range map[string]struct {
		out        string
		total, bot int
	}{
		"css":                        {cssStatus, 4, 1},
		"css 48 slots with SourceTV": {css48Status(), 48, 30},
		"csgo": {"hostname: x\nmap     : de_dust2\nplayers : 1 humans, 2 bots (20/0 max) (not hibernating)\n" +
			"# userid name uniqueid connected ping loss state rate adr\n" +
			"# 2 1 \"Alice\" STEAM_1:0:123 00:12 30 0 active 196608 192.0.2.1:27005\n" +
			"# 3 2 \"Bot Joe\" BOT active 64\n" +
			"# 4 3 \"Bot Ann\" BOT active 64\n", 3, 2},
		"tf2": {"hostname: x\nversion : 8835751/24 8835751 secure\nmap     : ctf_2fort at: 0 x, 0 y, 0 z\nplayers : 1 humans, 2 bots (24 max)\n" +
			"# userid name                uniqueid            connected ping loss state  adr\n" +
			"#      2 \"Alice\"             [U:1:2469]          05:12       60    0 active 192.0.2.1:27005\n" +
			"#      3 \"Heavy\"             BOT                                     active\n" +
			"#      4 \"Medic\"             BOT                                     active\n", 3, 2},
		"l4d2": {"hostname: x\nmap     : c1m1_hotel\nplayers : 0 humans, 4 bots (4 max)\n" +
			"# userid name uniqueid connected ping loss state rate adr\n" +
			"#  1 \"Coach\" BOT active\n#  2 \"Ellis\" BOT active\n#  3 \"Nick\" BOT active\n#  4 \"Rochelle\" BOT active\n", 4, 4},
		"lower case bot": {"hostname: x\nmap     : de_dust2\nplayers : 0 humans, 1 bots (24 max)\n" +
			"# userid name uniqueid connected ping loss state adr\n#      3 \"Bot Joe\" bot active\n", 1, 1},
		"goldsrc": {hldsStatus, 2, 1},
	} {
		st, err := ParseStatus(tc.out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		bots := 0
		for _, p := range st.Players {
			if stats.IsBot(p.UniqueID) {
				bots++
			}
		}
		if len(st.Players) != tc.total || bots != tc.bot {
			t.Errorf("%s: %d players, %d bots, want %d, %d", name, len(st.Players), bots, tc.total, tc.bot)
		}
	}
}

// statusWith is a status of the current map with humans and bots.
func statusWith(humans, bots int, prefix string) string {
	out := strings.Replace(statusHead, "24 max", "48 max", 1)
	for i := 0; i < humans; i++ {
		out += fmt.Sprintf("# %d \"Player %d\" STEAM_0:1:%d 10:00 30 0 active 192.0.2.%d:27005\n", 2+i, i, 100+i, 10+i)
	}
	for i := 0; i < bots; i++ {
		out += fmt.Sprintf("# %d \"%s %d\" BOT active\n", 40+i, prefix, i)
	}
	return out
}

func lastActive(st *memStore) (players, bots int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.active) == 0 {
		return -1, -1
	}
	return st.active[len(st.active)-1], st.activeBots[len(st.activeBots)-1]
}

// act_bots follows the bots RCON status lists, also when only the bots
// change and the player count stays.
func TestActBotsFollowStatus(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, t0 := context.Background(), time.Now()
	handleAll(t, p, t0, `Started map "de_dust2" (CRC "1")`)
	poll := func(at time.Time, humans, bots int) {
		t.Helper()
		if err := p.Status(ctx, statusOf(t, statusWith(humans, bots, "Bot")), at); err != nil {
			t.Fatal(err)
		}
	}
	poll(t0.Add(time.Second), 1, 31)
	if n, b := lastActive(st); n != 32 || b != 31 {
		t.Errorf("after the first status: act_players %d, act_bots %d, want 32, 31", n, b)
	}
	// A human takes a bot's slot (bot_quota): 32 players, 30 bots.
	poll(t0.Add(11*time.Second), 2, 30)
	poll(t0.Add(21*time.Second), 2, 30)
	if n, b := lastActive(st); n != 32 || b != 30 {
		t.Errorf("after the swap: act_players %d, act_bots %d, want 32, 30", n, b)
	}
	// The bots are kicked.
	poll(t0.Add(31*time.Second), 2, 0)
	poll(t0.Add(41*time.Second), 2, 0)
	if n, b := lastActive(st); n != 2 || b != 0 {
		t.Errorf("without bots: act_players %d, act_bots %d, want 2, 0", n, b)
	}
}

// During a map change's hold act_players keeps the count from before the
// change while the live rows are cleared; act_bots is held with it, so
// the 31 bots are not shown as 32 real players (the web counted the live
// rows: 0 bots for up to mapSettleNoStatus).
func TestMapChangeHoldsBotCount(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, t0 := context.Background(), time.Now()
	handleAll(t, p, t0, `Started map "de_dust2" (CRC "1")`)
	if err := p.Status(ctx, statusOf(t, statusWith(1, 31, "Old")), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	change := t0.Add(time.Minute)
	handleAll(t, p, change, `Loading map "de_nuke"`)
	if len(p.srv.players) != 0 {
		t.Fatalf("players %v after the map change, want none (visits ended)", keys(p.srv.players))
	}
	if n, b := lastActive(st); n != 32 || b != 31 {
		t.Errorf("during the hold: act_players %d, act_bots %d, want 32, 31", n, b)
	}
	if n, b := p.online(change.Add(30 * time.Second)); n != 32 || b != 31 {
		t.Errorf("online during the hold %d, %d bots, want 32, 31", n, b)
	}
	// After the hold the new map's status counts.
	newStatus := strings.Replace(statusWith(1, 20, "New"), "de_dust2", "de_nuke", 1)
	if err := p.Status(ctx, statusOf(t, newStatus), change.Add(mapSettle+time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, b := lastActive(st); n != 21 || b != 20 {
		t.Errorf("after the hold: act_players %d, act_bots %d, want 21, 20", n, b)
	}
}

// Without RCON status the tracked players count: bots by their BOT tag in
// the log lines.
func TestActBotsFromLogLines(t *testing.T) {
	p, st := newTestProcessor(t)
	t0 := time.Now()
	handleAll(t, p, t0, `Started map "de_dust2" (CRC "1")`,
		`"Bot Joe<3><BOT><CT>" entered the game`,
		`"Bot Ann<4><BOT><TERRORIST>" entered the game`,
		`"Alice<2><STEAM_0:1:100><CT>" entered the game`)
	if n, b := lastActive(st); n != 3 || b != 2 {
		t.Errorf("act_players %d, act_bots %d, want 3, 2", n, b)
	}
	handleAll(t, p, t0.Add(time.Second), `"Bot Ann<4><BOT><TERRORIST>" disconnected (reason "Kicked by Console")`)
	if n, b := lastActive(st); n != 2 || b != 1 {
		t.Errorf("after a bot left: act_players %d, act_bots %d, want 2, 1", n, b)
	}
}

// Without RCON status, bots that log nothing for botIdle (dead in a long
// round) are ended, yet still on the server: the bots of a fresh A2S_INFO
// answer count (act_bots, hlstats_server_load), humans come from the log
// lines; a stale answer counts no more.
func TestA2SBotsWithoutStatus(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, t0 := context.Background(), time.Now()
	line := func(l string, at time.Time) {
		t.Helper()
		if err := p.Handle(ctx, l, at); err != nil {
			t.Fatal(err)
		}
	}
	tick := func(at time.Time) {
		t.Helper()
		if err := p.Tick(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		line(fmt.Sprintf(`"Bot %d<%d><BOT><TERRORIST>" joined team "TERRORIST"`, i, 10+i), t0)
	}
	line(`"K<2><STEAM_1:0:1><CT>" joined team "CT"`, t0)
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 20, Players: 4, Bots: 4}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The bots log nothing for longer than botIdle: idle ends them.
	line(`"K<2><STEAM_1:0:1><CT>" say "hi"`, t0.Add(4*time.Minute))
	tick(t0.Add(4 * time.Minute))
	if n, bots := p.online(t0.Add(4 * time.Minute)); len(p.srv.players) != 1 || n != 5 || bots != 4 {
		t.Fatalf("tracked %d, online %d (%d bots), want 1 tracked, 5 (4 bots)", len(p.srv.players), n, bots)
	}
	if n, bots := lastActive(st); n != 5 || bots != 4 {
		t.Errorf("act_players %d, act_bots %d, want 5, 4", n, bots)
	}
	// Two load intervals with an answer every 5 minutes: the second row
	// (the first starts at 0) stays at 5 throughout.
	for m := 5; m <= 20; m += 5 {
		at := t0.Add(time.Duration(m)*time.Minute - 30*time.Second)
		if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 20, Players: 4, Bots: 4}, at); err != nil {
			t.Fatal(err)
		}
		line(`"K<2><STEAM_1:0:1><CT>" say "hi"`, at)
		tick(at.Add(31 * time.Second))
	}
	st.mu.Lock()
	loads := append([]stats.Load(nil), st.loads...)
	st.mu.Unlock()
	if len(loads) != 2 || loads[0].Players != 5 || loads[1].Players != 5 || loads[1].MinPlayers != 5 || loads[1].MaxPlayers != 5 {
		t.Errorf("server load %+v, want two rows, the second 5/5/5", loads)
	}
	// The answer is older than two intervals: the tracked players only.
	line(`"K<2><STEAM_1:0:1><CT>" say "hi"`, t0.Add(31*time.Minute))
	if n, bots := p.online(t0.Add(31 * time.Minute)); n != 1 || bots != 0 {
		t.Errorf("stale A2S: online %d (%d bots), want 1 (0 bots)", n, bots)
	}
}

// The A2S bots never push the count over the slots.
func TestA2SBotsCappedAtSlots(t *testing.T) {
	p, _ := newTestProcessor(t)
	ctx, t0 := context.Background(), time.Now()
	for i := 0; i < 3; i++ {
		if err := p.Handle(ctx, fmt.Sprintf(`"P%d<%d><STEAM_1:0:%d><CT>" joined team "CT"`, i, 2+i, 1+i), t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 4, Players: 4, Bots: 4}, t0); err != nil {
		t.Fatal(err)
	}
	if n, bots := p.online(t0); n != 4 || bots != 1 {
		t.Errorf("online %d (%d bots), want 4 (1 bot)", n, bots)
	}
}
