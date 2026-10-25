// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/rcon/rcontest"
	"gamewe-collector/internal/stats"
)

const cssStatus = `hostname: {Example GunGame 24/7}
version : 7604765/24 7604765 secure
udp/ip  : 203.0.113.136:27315  (public ip: 203.0.113.136)
account : not logged in  (No account specified)
map     : gg_example at: 0 x, 0 y, 0 z
tags    : gungame
players : 3 humans, 1 bots (48 max)
edicts  : 1023 used of 2048 max
# userid name                uniqueid            connected ping loss state  adr
#      2 "Player One"         [U:1:2469]     1:02:33    45    0 active 192.0.2.9:27005
#      3 "Player Two"              STEAM_0:1:4321  12:01      80    0 active 192.0.2.10:27005
#      4 "Bot Bob"           BOT                                     active
#      5 "say \"hi\" name"   STEAM_0:0:5         00:10      12    0 spawning 192.0.2.11:27005
`

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus(cssStatus)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hostname != "{Example GunGame 24/7}" || st.Map != "gg_example" || st.MaxPlayers != 48 {
		t.Errorf("status %+v", st)
	}
	var got []string
	for _, p := range st.Players {
		got = append(got, p.UniqueID+"="+strconv.Itoa(p.Ping))
	}
	if strings.Join(got, " ") != "1:1234=45 1:4321=80 BOT:Bot Bob=0 0:5=12" {
		t.Errorf("players %v", got)
	}
	var addrs []string
	for _, p := range st.Players {
		addrs = append(addrs, p.Address.String())
	}
	if strings.Join(addrs, " ") != "192.0.2.9 192.0.2.10 invalid IP 192.0.2.11" {
		t.Errorf("addresses %v", addrs)
	}
	// CS:GO style: slot number before the userid.
	csgo := "hostname: x\nmap     : de_dust2\nplayers : 1 humans, 0 bots (20/0 max) (not hibernating)\n" +
		"# userid name uniqueid connected ping loss state rate adr\n# 2 1 \"Alice\" STEAM_1:0:123 00:12 30 0 active 196608 192.0.2.1:27005\n"
	st, err = ParseStatus(csgo)
	if err != nil || st.MaxPlayers != 20 || len(st.Players) != 1 || st.Players[0].UniqueID != "0:123" || st.Players[0].Ping != 30 || st.Players[0].UserID != 2 || st.Players[0].Address.String() != "192.0.2.1" {
		t.Errorf("csgo status %+v %v", st, err)
	}
}

func TestParseLogAddresses(t *testing.T) {
	for out, want := range map[string]string{
		"logaddress_list:  2 entries\n192.0.2.5:41337\n198.51.100.7:27500\n": "192.0.2.5:41337 198.51.100.7:27500",
		"No addresses in the list\n":                                         "",
		"logaddress_list: 0 entries\n":                                       "",
	} {
		var got []string
		for _, a := range ParseLogAddresses(out) {
			got = append(got, a.String())
		}
		if strings.Join(got, " ") != want {
			t.Errorf("ParseLogAddresses(%q) = %v", out, got)
		}
	}
}

// fakeGameServer keeps a logaddress list and answers status.
func fakeGameServer(t *testing.T) *rcontest.Server {
	var mu sync.Mutex
	var list, httpList []string
	logging := false
	return rcontest.Start(t, "pw", func(cmd string) string {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case cmd == "status":
			return cssStatus
		case cmd == "logaddress_list":
			if len(list) == 0 {
				return "No addresses in the list\n"
			}
			return "logaddress_list:  " + strconv.Itoa(len(list)) + " entries\n" + strings.Join(list, "\n") + "\n"
		case strings.HasPrefix(cmd, "logaddress_add "):
			list = append(list, strings.TrimPrefix(cmd, "logaddress_add "))
			return "logaddress_add:  " + strings.TrimPrefix(cmd, "logaddress_add ") + "\n"
		case cmd == "logaddress_list_http":
			return "logaddress_list_http:  " + strconv.Itoa(len(httpList)) + " entries\n" + strings.Join(httpList, "\n") + "\n"
		case strings.HasPrefix(cmd, "logaddress_add_http "):
			httpList = append(httpList, strings.Trim(strings.TrimPrefix(cmd, "logaddress_add_http "), `"`))
		case cmd == "log on":
			logging = true
		case cmd == "log":
			if !logging {
				return "Usage:  log < on | off >\nnot currently logging\n"
			}
			return "Usage:  log < on | off >\ncurrently logging to: console, udp\n"
		}
		return ""
	})
}

func TestEnsureLogAddress(t *testing.T) {
	gs := fakeGameServer(t)
	c := &rcon.Client{Address: gs.Addr, Password: "pw", Timeout: 2 * time.Second}
	defer c.Close()
	if _, err := c.Exec("echo"); err != nil {
		t.Fatal(err)
	}
	addr, err := LogAddress(c, "", 41337)
	if err != nil || addr.String() != "127.0.0.1:41337" {
		t.Fatalf("auto address %v %v", addr, err)
	}
	if a, _ := LogAddress(c, "203.0.113.4:41000", 41337); a.String() != "203.0.113.4:41000" {
		t.Errorf("public address %v", a)
	}
	list, added, err := EnsureLogAddress(c, games.Source, addr, false) // check only
	if err != nil || added || len(list) != 0 {
		t.Fatalf("check: %v %v %v", list, added, err)
	}
	if _, added, err = EnsureLogAddress(c, games.Source, addr, true); err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	list, added, err = EnsureLogAddress(c, games.Source, addr, true) // registered and logging: nothing to do
	if err != nil || added || len(list) != 1 || list[0] != addr {
		t.Fatalf("again: %v %v %v", list, added, err)
	}
	if got := strings.Join(gs.Commands(), ","); strings.Count(got, "logaddress_add") != 1 || !strings.Contains(got, "log on") {
		t.Errorf("commands %s", got)
	}
}

func TestMonitorPolls(t *testing.T) {
	firstStatus, firstLogAddress = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { firstStatus, firstLogAddress = 2*time.Second, 5*time.Second })
	gs := fakeGameServer(t)
	srv := stats.Server{ID: 7, AddrPort: netip.MustParseAddrPort(gs.Addr), RconPassword: "pw"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := MonitorConfig{Status: true, StatusInterval: 20 * time.Millisecond, LogAddress: true, ListenPort: 41337}
	got := make(chan Status, 100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitor(ctx, func() stats.Server { return srv }, cfg, func(st Status, _ time.Time) { got <- st }, nil,
			func() time.Duration { return 2 * time.Second }, log)
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case st := <-got:
			if st.MaxPlayers != 48 || len(st.Players) != 4 {
				t.Fatalf("status %+v", st)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no status delivered")
		}
	}
	cancel()
	<-done
	cmds := strings.Join(gs.Commands(), ",")
	if strings.Count(cmds, "logaddress_add 127.0.0.1:41337") != 1 || strings.Count(cmds, "status") < 3 {
		t.Errorf("commands %s", cmds)
	}
}

func TestMonitorLogInterval(t *testing.T) {
	firstStatus, firstLogAddress = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { firstStatus, firstLogAddress = 2*time.Second, 5*time.Second })
	gs := fakeGameServer(t)
	srv := stats.Server{ID: 7, AddrPort: netip.MustParseAddrPort(gs.Addr), RconPassword: "pw"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := MonitorConfig{LogAddress: true, LogInterval: 20 * time.Millisecond, ListenPort: 41337}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitor(ctx, func() stats.Server { return srv }, cfg, func(Status, time.Time) {}, nil,
			func() time.Duration { return 2 * time.Second }, log)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(strings.Join(gs.Commands(), ","), "logaddress_list") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("log address not checked every LogInterval: %v", gs.Commands())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if cmds := strings.Join(gs.Commands(), ","); strings.Count(cmds, "logaddress_add") != 1 || strings.Contains(cmds, "status") {
		t.Errorf("commands %s", cmds)
	}
}

func TestParseConnected(t *testing.T) {
	for in, want := range map[string]time.Duration{"1:02:33": time.Hour + 2*time.Minute + 33*time.Second, "12:01": 12*time.Minute + time.Second, "": 0, "x": 0} {
		if got := parseConnected(in); got != want {
			t.Errorf("parseConnected(%q) = %v, want %v", in, got, want)
		}
	}
}

const hldsStatus = `hostname:  Example CS 1.6
version :  48/1.1.2.7/Stdio 8684 secure  (10)
tcp/ip  :  198.51.100.16:27015
map     :  de_dust2 at: 0 x, 0 y, 0 z
players :  2 active (32 max)

#      name userid uniqueid frag time ping loss adr
# 1 "Player One"   5 STEAM_0:1:1234   12 12:01   45    0 192.0.2.9:27005
# 2 "Bot Bob"   6 BOT   3 1:00:00    0    0
2 users
`

// A GoldSrc server: UDP challenge RCON, HLDS status, "logaddress_add ip port".
func TestMonitorGoldSrc(t *testing.T) {
	firstStatus, firstLogAddress = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { firstStatus, firstLogAddress = 2*time.Second, 5*time.Second })
	var mu sync.Mutex
	var list []string
	gs := rcontest.StartGoldSrc(t, "pw", func(cmd string) string {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case cmd == "status":
			return hldsStatus
		case cmd == "logaddress_list":
			return strings.Join(list, "\n") + "\n"
		case strings.HasPrefix(cmd, "logaddress_add "):
			list = append(list, strings.TrimPrefix(cmd, "logaddress_add "))
		}
		return ""
	})
	srv := stats.Server{ID: 9, AddrPort: netip.MustParseAddrPort(gs.Addr), RconPassword: "pw", Engine: games.GoldSrc}
	got := make(chan Status, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitor(ctx, func() stats.Server { return srv }, MonitorConfig{Status: true, StatusInterval: 50 * time.Millisecond, LogAddress: true, ListenPort: 41337},
			func(st Status, _ time.Time) { got <- st }, nil, func() time.Duration { return time.Second }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	var st Status
	select {
	case st = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if st.Map != "de_dust2" || st.MaxPlayers != 32 || len(st.Players) != 2 || st.Players[0].UniqueID != "1:1234" ||
		st.Players[0].UserID != 5 || st.Players[0].Ping != 45 || st.Players[0].Address.String() != "192.0.2.9" || st.Players[1].UniqueID != "BOT:Bot Bob" {
		t.Errorf("status %+v", st)
	}
	if cmds := strings.Join(gs.Commands(), ","); !strings.Contains(cmds, "logaddress_add 127.0.0.1 41337") || strings.Count(cmds, "logaddress_add") != 1 {
		t.Errorf("commands %s", cmds)
	}
}

func TestDuplicateLogAddresses(t *testing.T) {
	me := netip.MustParseAddrPort("203.0.113.4:41337")
	for _, c := range []struct {
		list string
		want int
	}{
		{"203.0.113.4:41337", 0},
		{"203.0.113.4:41337 198.51.100.9:27500", 0}, // another tool
		{"203.0.113.4:41337 10.0.0.4:41337", 1},     // public and local address
		{"10.0.0.4:41337 203.0.113.4:41337 203.0.113.4:41337", 2},
	} {
		var list []netip.AddrPort
		for _, a := range strings.Fields(c.list) {
			list = append(list, netip.MustParseAddrPort(a))
		}
		if got := DuplicateLogAddresses(list, me); len(got) != c.want {
			t.Errorf("%s: extra %v, want %d", c.list, got, c.want)
		}
	}
}

// css48Status is a full 48-slot CS:S (SteamPipe) status: SourceTV, 29 bots
// and 18 humans with Steam3 ids, more than one RCON packet.
func css48Status() string {
	var b strings.Builder
	b.WriteString("hostname: GunGame 24/7 | Example\nversion : 9540945/24 9540945 secure\n" +
		"udp/ip  : 203.0.113.48:27315  (public ip: 203.0.113.48)\nmap     : gg_simpsons at: 0 x, 0 y, 0 z\n" +
		"tags    : gungame\nplayers : 18 humans, 30 bots (48 max)\nedicts  : 900 used of 2048 max\n" +
		"# userid name                uniqueid            connected ping loss state  adr\n")
	b.WriteString("#      2 \"SourceTV\"          BOT                                     active\n")
	for i := 0; i < 29; i++ {
		b.WriteString(fmt.Sprintf("#    %3d \"Bot %02d\"            BOT                                     active\n", 3+i, i))
	}
	for i := 0; i < 18; i++ {
		b.WriteString(fmt.Sprintf("#    %3d \"Player %02d\"         [U:1:%d]       %02d:%02d       %3d    0 active 192.0.2.%d:27005\n",
			40+i, i, 1000+i, 10+i, 30, 40+i, 10+i))
	}
	return b.String()
}

// The 48-slot status arrives in several RCON packets and is read whole;
// bots and humans get the keys of the log lines, so the log lines of the
// same players start no second visit.
func TestStatus48SlotsMultiPacket(t *testing.T) {
	out := css48Status()
	if len(out) <= 4096 {
		t.Fatalf("status only %d bytes, want more than one packet", len(out))
	}
	srv := rcontest.Start(t, "pw", func(cmd string) string { return out })
	c := rcon.New(false, srv.Addr, "pw", 2*time.Second)
	defer c.Close()
	st, err := pollStatus(c)
	if err != nil {
		t.Fatal(err)
	}
	if st.MaxPlayers != 48 || len(st.Players) != 48 || st.Map != "gg_simpsons" {
		t.Fatalf("max %d, %d players, map %q", st.MaxPlayers, len(st.Players), st.Map)
	}
	p, _ := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	if err := p.Status(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 29; i++ {
		handleAll(t, p, now.Add(time.Second), fmt.Sprintf(`"Bot %02d<%d><BOT><CT>" triggered "gg_levelup"`, i, 3+i))
	}
	for i := 0; i < 18; i++ {
		// the plugin's lines use Steam2, the engine's Steam3
		handleAll(t, p, now.Add(time.Second), fmt.Sprintf(`"Player %02d<%d><STEAM_1:%d:%d><TERRORIST>" triggered "gg_levelup"`, i, 40+i, (1000+i)%2, (1000+i)/2))
	}
	if n := len(p.srv.players); n != 48 {
		t.Errorf("%d players tracked, want 48", n)
	}
	// Two more polls with the same list keep them; a stale poll sent
	// before a map change (old map, old bots) changes nothing.
	for i := 1; i <= 2; i++ {
		if err := p.Status(ctx, st, now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	handleAll(t, p, now.Add(25*time.Second), `Started map "de_dust2" (CRC "1")`)
	if err := p.Status(ctx, st, now.Add(24*time.Second)); err != nil {
		t.Fatal(err)
	}
	if p.srv.info.Map != "de_dust2" || len(p.srv.players) != 0 {
		t.Errorf("after a stale poll: map %q, %d players, want de_dust2 and 0", p.srv.info.Map, len(p.srv.players))
	}
}

func TestEnsureHTTPLogAddress(t *testing.T) {
	gs := fakeGameServer(t)
	c := &rcon.Client{Address: gs.Addr, Password: "pw", Timeout: 2 * time.Second}
	defer c.Close()
	if _, err := c.Exec("echo"); err != nil {
		t.Fatal(err)
	}
	u, err := HTTPLogURL(c, "", 41338, "", 27015)
	if err != nil || u != "http://127.0.0.1:41338/27015" {
		t.Fatalf("auto url %q %v", u, err)
	}
	if u, _ := HTTPLogURL(c, "http://stats.example.org:8080/", 41338, "abcdefghijklmnop1234", 27016); u != "http://stats.example.org:8080/abcdefghijklmnop1234/27016" {
		t.Errorf("public url %q", u)
	}
	if r := RedactURL("http://x:1/abcdefghijklmnop1234/27016", "abcdefghijklmnop1234"); r != "http://x:1/<token>/27016" {
		t.Errorf("redacted %q", r)
	}
	srv := stats.Server{ID: 3, AddrPort: netip.MustParseAddrPort("127.0.0.1:27015"), Engine: games.Source2}
	cfg := MonitorConfig{LogAddress: true, HTTP: true, HTTPPort: 41338, ListenPort: 41337}
	var st httpAddState
	for i := 0; i < 3; i++ {
		if err := ensureLogAddress(c, srv, cfg, &st, quiet); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(gs.Commands(), ",")
	if strings.Count(got, "logaddress_add_http") != 1 || strings.Contains(got, "logaddress_add ") || !strings.Contains(got, "log on") {
		t.Errorf("commands %s", got)
	}
	cfg.HTTP = false // CS2 without the HTTP registration: nothing
	n := len(gs.Commands())
	if err := ensureLogAddress(c, srv, cfg, &st, quiet); err != nil || len(gs.Commands()) != n {
		t.Errorf("http off sent %v (%v)", gs.Commands()[n:], err)
	}
}

// A server whose logaddress_list_http never shows the URL gets
// logaddress_add_http once, not every interval.
func TestHTTPLogAddressUnconfirmed(t *testing.T) {
	gs := rcontest.Start(t, "pw", func(cmd string) string { return "" })
	c := &rcon.Client{Address: gs.Addr, Password: "pw", Timeout: 2 * time.Second}
	defer c.Close()
	srv := stats.Server{ID: 4, AddrPort: netip.MustParseAddrPort("127.0.0.1:27015"), Engine: games.Source2}
	cfg := MonitorConfig{LogAddress: true, HTTP: true, HTTPPort: 41338}
	var st httpAddState
	for i := 0; i < 3; i++ {
		if err := ensureLogAddress(c, srv, cfg, &st, quiet); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(strings.Join(gs.Commands(), ","), "logaddress_add_http"); n != 1 {
		t.Errorf("logaddress_add_http sent %d times, want 1", n)
	}
	st.last = time.Now().Add(-httpReAdd) // an hour later: once more
	if err := ensureLogAddress(c, srv, cfg, &st, quiet); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(gs.Commands(), ","), "logaddress_add_http"); n != 2 {
		t.Errorf("after an hour: %d, want 2", n)
	}
}
