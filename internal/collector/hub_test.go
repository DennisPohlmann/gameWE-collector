// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// gameLines is one short game on server n. All three servers use the same
// userids (2, 3 and 4) but their own SteamIDs, except that SteamID
// STEAM_1:0:777 plays on every server, with a different userid each time.
func gameLines(n int) []string {
	a := fmt.Sprintf(`"A%d<2><STEAM_1:0:%d01><%%s>"`, n, n)
	b := fmt.Sprintf(`"B%d<3><STEAM_1:1:%d02><%%s>"`, n, n)
	shared := fmt.Sprintf(`"Roamer<%d><STEAM_1:0:777><%%s>"`, 3+n)
	f := func(p, team string) string { return fmt.Sprintf(p, team) }
	return []string{
		f(a, "") + ` connected, address "192.0.2.1:27005"`,
		f(b, "") + ` connected, address "192.0.2.2:27005"`,
		f(shared, "") + ` connected, address "192.0.2.3:27005"`,
		f(a, "Unassigned") + ` joined team "CT"`,
		f(b, "Unassigned") + ` joined team "TERRORIST"`,
		f(shared, "Unassigned") + ` joined team "CT"`,
		f(a, "CT") + ` killed ` + f(b, "TERRORIST") + fmt.Sprintf(` with "weapon%d" (headshot)`, n),
		f(shared, "CT") + ` killed ` + f(b, "TERRORIST") + fmt.Sprintf(` with "weapon%d"`, n),
		f(a, "CT") + ` triggered "weaponstats" (weapon "ak47") (shots "10") (hits "4") (kills "1") (headshots "1") (tks "0") (damage "100") (deaths "0")`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
		f(b, "TERRORIST") + ` say "!rank"`,
		f(a, "CT") + ` disconnected (reason "bye")`,
	}
}

// newTestHub returns a hub serving addrs (rows of hlstats_Servers).
func newTestHub(st *memStore, rec Replier, addrs []string, games map[string]string) *Hub {
	hub := NewHub(st, settings.NewLive(settings.Defaults()), rec, quiet)
	hub.SetServers(st.addServers(addrs, games))
	return hub
}

func pkt(line string) []byte {
	return []byte("\xff\xff\xff\xffRL 10/22/2026 - 12:00:00: " + line + "\n\x00")
}

// checkAttribution asserts that every event of server n landed on server
// n. The third server runs another game (tf), so its players are separate
// hlstats_Players rows even for the same SteamID.
func checkAttribution(t *testing.T, st *memStore, rec *recorder, addrs []string) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.kills) != 2*len(addrs) {
		t.Fatalf("got %d kills, want %d", len(st.kills), 2*len(addrs))
	}
	for i, addr := range addrs {
		n := i + 1
		srv, ok := st.servers[addr]
		if !ok {
			t.Fatalf("server %s not registered", addr)
		}
		var kills []memKill
		for _, k := range st.kills {
			if k.server == srv.ID {
				kills = append(kills, k)
			}
		}
		if len(kills) != 2 {
			t.Fatalf("server %d: %d kills, want 2", n, len(kills))
		}
		for _, k := range kills {
			if k.weapon != fmt.Sprintf("weapon%d", n) || k.victim != fmt.Sprintf("1:%d02", n) || k.game != srv.Game {
				t.Errorf("server %d got foreign kill %+v", n, k)
			}
			if k.killer != fmt.Sprintf("0:%d01", n) && k.killer != "0:777" {
				t.Errorf("server %d: wrong killer %q", n, k.killer)
			}
		}
		var rounds []memRound
		for _, r := range st.rounds {
			if r.server == srv.ID {
				rounds = append(rounds, r)
			}
		}
		want := []string{fmt.Sprintf("0:%d01", n), "0:777"}
		if len(rounds) != 1 || strings.Join(rounds[0].winners, ",") != strings.Join(want, ",") {
			t.Errorf("server %d: round winners %+v, want %v", n, rounds, want)
		}
		sent := rec.get(addr)
		if len(sent) != 1 || !strings.HasPrefix(sent[0], "gamewe_msay 15 3 0 \"->1 - Total") {
			t.Errorf("server %d: replies %q, want one panel to userid 3", n, sent)
		}
		// The roamer joined every server once, as the player of that game.
		roamer := st.players[[2]string{srv.Game, "0:777"}]
		joins := 0
		for _, j := range st.joins {
			if j.server == srv.ID && j.player == roamer.id {
				joins++
			}
		}
		if joins != 1 {
			t.Errorf("server %d: roamer joined %d times", n, joins)
		}
	}
	css, tf := st.players[[2]string{"css", "0:777"}], st.players[[2]string{"tf", "0:777"}]
	if css == nil || tf == nil || css.id == tf.id {
		t.Fatalf("roamer must be one player per game: css=%+v tf=%+v", css, tf)
	}
	if css.kills != 2 || tf.kills != 1 || css.wins != 2 || tf.wins != 1 {
		t.Errorf("roamer stats css=%+v tf=%+v", css, tf)
	}
	if len(st.live) != 0 {
		t.Errorf("live rows left after Close: %v", st.live)
	}
	if w := st.weapons[[2]string{"0:101", "ak47"}]; w.Shots != 10 || w.Damage != 100 {
		t.Errorf("weapon stats of server 1 player: %+v", w)
	}
}

// Lines of three servers arrive interleaved; workers run in parallel.
func TestHubAttributesInterleavedServers(t *testing.T) {
	addrs := []string{"198.51.100.1:27015", "198.51.100.1:27016", "198.51.100.2:27015"}
	st, rec := newMemStore(), &recorder{}
	hub := newTestHub(st, rec, addrs, map[string]string{addrs[2]: "tf"})

	games := [][]string{gameLines(1), gameLines(2), gameLines(3)}
	now := time.Now().UTC()
	for i := range games[0] {
		for s, addr := range addrs {
			if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(games[s][i]), now); err != nil {
				t.Fatalf("dispatch to %s: %v", addr, err)
			}
		}
	}
	hub.Close(context.Background())
	checkAttribution(t, st, rec, addrs)
}

func TestHubRejects(t *testing.T) {
	st := newMemStore()
	hub := newTestHub(st, &recorder{}, []string{"198.51.100.1:27015", "198.51.100.1:27016", "198.51.100.3:27015"}, nil)
	defer hub.Close(context.Background())
	line := `World triggered "Round_Start"`
	now := time.Now()
	cases := []struct {
		src  string
		want error
	}{
		{"203.0.113.9:27015", ErrUnknownSource}, // unknown IP
		{"198.51.100.1:27099", ErrUnknownPort},  // known IP, port without a row
		{"198.51.100.1:27016", nil},
		{"198.51.100.3:40000", ErrUnknownPort}, // single server on that IP: still only its port
		{"[::ffff:198.51.100.3]:40000", ErrUnknownPort},
		{"[::ffff:198.51.100.1]:27016", nil}, // IPv4-mapped source
	}
	for _, c := range cases {
		if err := hub.Dispatch(netip.MustParseAddrPort(c.src), pkt(line), now); err != c.want {
			t.Errorf("Dispatch(%s) = %v, want %v", c.src, err, c.want)
		}
	}
	if err := hub.Dispatch(netip.MustParseAddrPort("198.51.100.1:27015"), []byte("\xff\xff\xff\xffTSource Engine Query"), now); err != logparse.ErrNotLog {
		t.Errorf("non-log packet: %v", err)
	}
}

// Regression: with only the first server of a host in hlstats_Servers,
// the logs of the other game servers on that host (other ports) were
// attributed to it, so their maps and kills ended up in its ranking.
// They must be rejected until they have their own row.
func TestHubKeepsOtherPortsOfTheSameHostOut(t *testing.T) {
	assault, gungame := "198.51.100.1:27015", "198.51.100.1:27016"
	st, rec := newMemStore(), &recorder{}
	hub := newTestHub(st, rec, []string{assault}, nil)
	foreign := append([]string{`Loading map "gg_simpsons_v2"`, `Started map "gg_simpsons_v2" (CRC "1")`}, gameLines(2)...)
	now := time.Now().UTC()
	for _, line := range foreign {
		for _, src := range []string{gungame, "198.51.100.1:27017"} {
			if err := hub.Dispatch(netip.MustParseAddrPort(src), pkt(line), now); err != ErrUnknownPort {
				t.Fatalf("Dispatch(%s) = %v, want %v", src, err, ErrUnknownPort)
			}
		}
	}
	for _, line := range append([]string{`Started map "cs_assault" (CRC "1")`}, gameLines(1)...) {
		if err := hub.Dispatch(netip.MustParseAddrPort(assault), pkt(line), now); err != nil {
			t.Fatalf("Dispatch(%s): %v", assault, err)
		}
	}
	// Once the second server has its row, its lines go to it.
	st.addServers([]string{gungame}, nil)
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, line := range gameLines(2) {
		if err := hub.Dispatch(netip.MustParseAddrPort(gungame), pkt(line), now); err != nil {
			t.Fatalf("Dispatch(%s) after reload: %v", gungame, err)
		}
	}
	hub.Close(context.Background())
	st.mu.Lock()
	defer st.mu.Unlock()
	maps := strings.Join(st.maps, ",")
	if maps != "cs_assault" {
		t.Errorf("maps %q, want only cs_assault", maps)
	}
	ids := map[int64]int{st.servers[assault].ID: 1, st.servers[gungame].ID: 2}
	if len(st.kills) != 4 {
		t.Fatalf("got %d kills, want 2 per server", len(st.kills))
	}
	for _, k := range st.kills {
		if n := ids[k.server]; k.weapon != fmt.Sprintf("weapon%d", n) {
			t.Errorf("server %d got foreign kill %+v", n, k)
		}
	}
}

// Servers added to or removed from hlstats_Servers are picked up by the
// next reload without a restart.
func TestHubServersChangeAtRuntime(t *testing.T) {
	st, rec := newMemStore(), &recorder{}
	hub := newTestHub(st, rec, []string{"198.51.100.1:27015"}, nil)
	defer hub.Close(context.Background())
	newSrv := netip.MustParseAddrPort("198.51.100.7:27015")
	line := `"A<2><STEAM_1:0:1><CT>" say "!rank"`
	if err := hub.Dispatch(newSrv, pkt(line), time.Now()); err != ErrUnknownSource {
		t.Fatalf("before reload: %v", err)
	}
	st.addServers([]string{"198.51.100.7:27015"}, nil)
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hub.Count() != 2 {
		t.Fatalf("%d servers after reload", hub.Count())
	}
	if err := hub.Dispatch(newSrv, pkt(line), time.Now()); err != nil {
		t.Fatalf("after reload: %v", err)
	}
	// Remove the first server: its packets are rejected, its players' visits end.
	st.mu.Lock()
	delete(st.servers, "198.51.100.1:27015")
	st.mu.Unlock()
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := hub.Dispatch(netip.MustParseAddrPort("198.51.100.1:27015"), pkt(line), time.Now()); err != ErrUnknownSource {
		t.Errorf("removed server: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(rec.get("198.51.100.7:27015")) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := rec.get("198.51.100.7:27015"); len(got) != 1 {
		t.Errorf("reply to the new server: %q", got)
	}
}

// A changed RCON password is used without restarting the worker.
func TestHubUpdatesRconPassword(t *testing.T) {
	st, rec := newMemStore(), &recorder{}
	addr := "198.51.100.1:27015"
	hub := newTestHub(st, rec, []string{addr}, nil)
	defer hub.Close(context.Background())
	st.mu.Lock()
	s := st.servers[addr]
	s.RconPassword = "new"
	st.servers[addr] = s
	st.mu.Unlock()
	if added, removed := hub.SetServers([]stats.Server{s}); added != 0 || removed != 0 {
		t.Errorf("password change restarted the worker: +%d -%d", added, removed)
	}
	hub.mu.RLock()
	got := hub.byID[s.ID].srv.RconPassword
	hub.mu.RUnlock()
	if got != "new" {
		t.Errorf("password %q", got)
	}
}

// End to end over real UDP sockets: three senders with fixed source ports
// send concurrently to one collector.
func TestServeUDPThreeServers(t *testing.T) {
	var senders []*net.UDPConn
	var addrs []string
	for range 3 {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		senders = append(senders, c)
		addrs = append(addrs, c.LocalAddr().String())
	}
	st, rec := newMemStore(), &recorder{}
	hub := newTestHub(st, rec, addrs, map[string]string{addrs[2]: "tf"})

	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- ServeConn(ctx, ln, hub, quiet) }()

	done := make(chan struct{})
	for s, c := range senders {
		go func() {
			defer func() { done <- struct{}{} }()
			for _, l := range gameLines(s + 1) {
				c.WriteToUDP(pkt(l), ln.LocalAddr().(*net.UDPAddr))
				time.Sleep(time.Millisecond)
			}
		}()
	}
	for range senders {
		<-done
	}
	// Wait until all replies (the last-but-one line of each game) arrived.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, a := range addrs {
			n += len(rec.get(a))
		}
		if n == len(addrs) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	hub.Close(context.Background())
	checkAttribution(t, st, rec, addrs)
}

// syncBuffer is a bytes.Buffer safe for the hub's concurrent workers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Every accepted line is written as "[name ip:port] line"; rejected
// packets are not; gamewe_log_lines 0 switches it off.
func TestHubLineLog(t *testing.T) {
	st := newMemStore()
	set := settings.NewLive(settings.Defaults())
	hub := NewHub(st, set, &recorder{}, quiet)
	var out syncBuffer
	hub.LineLog = &out
	addrs := []string{"198.51.100.1:27015", "198.51.100.2:27015"}
	hub.SetServers(st.addServers(addrs, nil))
	now := time.Now()
	hub.Dispatch(netip.MustParseAddrPort(addrs[0]), pkt(`World triggered "Round_Start"`), now)
	hub.Dispatch(netip.MustParseAddrPort(addrs[1]), pkt("\"Evil\x1b[31m<2><STEAM_1:0:9><CT>\" say \"hi\x07\""), now)
	hub.Dispatch(netip.MustParseAddrPort("203.0.113.1:27015"), pkt(`World triggered "Round_Start"`), now)
	off := settings.Defaults()
	off.LogLines = false
	time.Sleep(50 * time.Millisecond) // let the workers write before switching off
	set.Set(off)
	hub.Dispatch(netip.MustParseAddrPort(addrs[0]), pkt(`World triggered "Round_End"`), now)
	hub.Close(context.Background())

	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	sort.Strings(got)
	want := []string{
		`[srv1 198.51.100.1:27015] L 10/22/2026 - 12:00:00: World triggered "Round_Start"`,
		`[srv2 198.51.100.2:27015] L 10/22/2026 - 12:00:00: "Evil\x1b[31m<2><STEAM_1:0:9><CT>" say "hi\x07"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("line log:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPrintable(t *testing.T) {
	if got := Printable("a\nb\x00c\xffdä"); got != `a\x0ab\x00c`+"�"+"dä" {
		t.Errorf("Printable = %q", got)
	}
}
