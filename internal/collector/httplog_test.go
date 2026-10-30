// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

func httpTestHub(t *testing.T) (*Hub, *memStore) {
	t.Helper()
	st := newMemStore()
	hub := newTestHub(st, &recorder{}, []string{"192.0.2.10:27015", "192.0.2.10:27016"},
		map[string]string{"192.0.2.10:27015": "cs2", "192.0.2.10:27016": "css"})
	t.Cleanup(func() { hub.Close(context.Background()) })
	return hub, st
}

func httpCfg() HTTPConfig {
	return HTTPConfig{MaxBody: 4096, ReadTimeout: time.Second, MaxConnections: 4, MaxConnectionsPerIP: 2}
}

func post(h http.Handler, remote, path, body string) int {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestHTTPLogHandler(t *testing.T) {
	hub, _ := httpTestHub(t)
	cfg := httpCfg()
	h := newHTTPHandler(cfg, hub, newRejectLog(quiet, "test"), quiet)
	body := "10/25/2026 - 12:00:00.123 - World triggered \"Round_Start\"\n"
	cases := []struct {
		name, remote, path, body string
		want                     int
	}{
		{"cs2 ok", "192.0.2.10:51000", "/27015", body, 200},
		{"other port", "192.0.2.10:51000", "/27017", body, 403},
		{"stranger", "198.51.100.1:51000", "/27015", body, 403},
		{"not source2", "192.0.2.10:51000", "/27016", body, 403},
		{"no port", "192.0.2.10:51000", "/", body, 404},
		{"bad port", "192.0.2.10:51000", "/70000", body, 404},
		{"plus port", "192.0.2.10:51000", "/+27015", body, 404},
		{"extra path", "192.0.2.10:51000", "/a/27015", body, 404},
		{"too big", "192.0.2.10:51000", "/27015", strings.Repeat("x", 5000), 413},
	}
	for _, c := range cases {
		if got := post(h, c.remote, c.path, c.body); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/27015", nil)
	r.RemoteAddr = "192.0.2.10:51000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestHTTPLogToken(t *testing.T) {
	hub, _ := httpTestHub(t)
	cfg := httpCfg()
	cfg.Token = "abcdefghijklmnop1234" // gitleaks:allow (made-up test token)
	h := newHTTPHandler(cfg, hub, newRejectLog(quiet, "test"), quiet)
	for path, want := range map[string]int{"/abcdefghijklmnop1234/27015": 200, "/27015": 404, "/abcdefghijklmnop1235/27015": 404, "/abcdefghijklmnop1234/": 404} {
		if got := post(h, "192.0.2.10:1", path, "x\n"); got != want {
			t.Errorf("%s: got %d, want %d", path, got, want)
		}
	}
}

func TestHTTPLogProxy(t *testing.T) {
	hub, _ := httpTestHub(t)
	cfg := httpCfg()
	cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	h := newHTTPHandler(cfg, hub, newRejectLog(quiet, "test"), quiet)
	send := func(remote, xff string) int {
		r := httptest.NewRequest(http.MethodPost, "/27015", strings.NewReader("x\n"))
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := send("10.0.0.2:1", "198.51.100.9, 192.0.2.10, 10.0.0.3"); c != 200 {
		t.Errorf("via proxy: %d", c)
	}
	if c := send("10.0.0.2:1", "192.0.2.10, 198.51.100.9"); c != 403 {
		t.Errorf("spoofed left hop accepted: %d", c)
	}
	if c := send("198.51.100.9:1", "192.0.2.10"); c != 403 {
		t.Errorf("X-Forwarded-For from an untrusted peer used: %d", c)
	}
	if c := send("10.0.0.2:1", "garbage"); c != 404 {
		t.Errorf("garbage XFF: %d", c)
	}
}

// logBuf is a logger that writes text lines into a buffer.
func logBuf() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

// Each reason of a 404 is told apart in the log, and the path (with the
// token) is never logged.
func TestHTTPLogPathReasons(t *testing.T) {
	hub, _ := httpTestHub(t)
	const token = "abcdefghijklmnop1234" // gitleaks:allow (made-up test token)
	for _, c := range []struct {
		token, path string
		want        error
	}{
		{"", "/", errHTTPPort},
		{"", "/70000", errHTTPPort},
		{"", "/+27015", errHTTPPort},
		{"", "/x/y/27015", errHTTPPath},
		{"", "/" + token + "/27015", errHTTPExtraToken},
		{token, "/27015", errHTTPNoToken},
		{token, "/abcdefghijklmnop1235/27015", errHTTPToken},
		{token, "/" + token + "/", errHTTPPort},
		{token, "/" + token + "/x/27015", errHTTPPath},
	} {
		cfg := httpCfg()
		cfg.Token = c.token
		log, buf := logBuf()
		h := newHTTPHandler(cfg, hub, newRejectLog(log, "rejected HTTP log post"), log)
		if got := post(h, "192.0.2.10:1", c.path, "x\n"); got != 404 {
			t.Errorf("%s: got %d", c.path, got)
		}
		out := buf.String()
		if !strings.Contains(out, c.want.Error()) || !strings.Contains(out, "status=404") {
			t.Errorf("%s: log %q, want %q", c.path, out, c.want)
		}
		if strings.Contains(out, "abcdefghijklmnop123") {
			t.Errorf("%s: token in the log: %q", c.path, out)
		}
	}
}

// A 403 says which server is missing and which ports are configured.
func TestHTTPLogRejectHint(t *testing.T) {
	hub, _ := httpTestHub(t)
	log, buf := logBuf()
	h := newHTTPHandler(httpCfg(), hub, newRejectLog(log, "rejected HTTP log post"), log)
	post(h, "192.0.2.10:1", "/26015", "x\n")
	post(h, "198.51.100.1:1", "/27015", "x\n")
	out := buf.String()
	for _, want := range []string{"source=192.0.2.10:26015", "status=403", "configured_ports=\"[27015 27016]\"",
		"no server 198.51.100.1:27015 in the web admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// A rejection that keeps coming is logged again every RejectRepeat with
// the count since the last line, not only every 1000th time.
func TestRejectLogRepeat(t *testing.T) {
	log, buf := logBuf()
	rl := newRejectLog(log, "rejected")
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }
	hints := 0
	hint := func() []any { hints++; return []any{"hint", "fix it"} }
	for i := 0; i < 50; i++ {
		rl.note("192.0.2.10:26015", ErrUnknownPort, hint)
		now = now.Add(10 * time.Second) // CS2 posts every few seconds
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// 0 s, then 300 s and 300 s later (at 300 s and 310 s?: no, every
	// 5 minutes from the last line): 0, 300.
	if len(lines) != 2 || hints != 2 {
		t.Fatalf("got %d lines, %d hints:\n%s", len(lines), hints, buf.String())
	}
	if !strings.Contains(lines[0], "count=1 ") || strings.Contains(lines[0], "new=") {
		t.Errorf("first line: %s", lines[0])
	}
	if !strings.Contains(lines[1], "count=31 new=30 since=5m0s") || !strings.Contains(lines[1], "hint=\"fix it\"") {
		t.Errorf("repeat line: %s", lines[1])
	}
	rl.note("192.0.2.10:26015", ErrUnknownSource, nil) // other reason: logged at once
	if n := strings.Count(buf.String(), "\n"); n != 3 {
		t.Errorf("other reason not logged at once: %d lines", n)
	}
}

// Accepted posts show up: the first of a server and the first after a
// pause at info level, the others at debug level.
func TestHTTPLogAccepted(t *testing.T) {
	hub, _ := httpTestHub(t)
	log, buf := logBuf()
	h := newHTTPHandler(httpCfg(), hub, newRejectLog(log, "rejected HTTP log post"), log)
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	body := "10/30/2026 - 20:00:00.000 - World triggered \"Round_Start\"\n"
	post(h, "192.0.2.10:1", "/27015", body)
	now = now.Add(5 * time.Second)
	post(h, "192.0.2.10:1", "/27015", body+body)
	now = now.Add(HTTPQuiet)
	post(h, "192.0.2.10:1", "/27015", body)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	for i, want := range []string{
		`level=INFO msg="receiving HTTP logs" source=192.0.2.10:27015 server_id=`,
		`level=DEBUG msg="HTTP log post" source=192.0.2.10:27015`,
		`level=INFO msg="receiving HTTP logs" source=192.0.2.10:27015`,
	} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d: %s, want %s", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[1], "lines=2") || !strings.Contains(lines[2], "after=10m0s") {
		t.Errorf("lines/after: %s | %s", lines[1], lines[2])
	}
}

func TestSplitHTTPLines(t *testing.T) {
	got := splitHTTPLines("a\r\n\nb\n" + strings.Repeat("y", MaxHTTPLine+1) + "\nc")
	if strings.Join(got, "|") != "a|b|c" {
		t.Errorf("got %q", got)
	}
}

func TestHTTPLogServe(t *testing.T) {
	hub, st := httpTestHub(t)
	cfg := httpCfg()
	cfg.Addr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, cfg, hub, quiet, func(a net.Addr) { addr <- a }) }()
	a := <-addr
	// 127.0.0.1 is no game server: rejected, but the server answers.
	resp, err := http.Post("http://"+a.String()+"/27015", "text/plain", strings.NewReader("x\n"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("got %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve: %v", err)
	}
	_ = st
}

func TestConnLimit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newConnLimit(ln, 3, 1, newRejectLog(quiet, "test"))
	defer l.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	c1, _ := net.Dial("tcp", ln.Addr().String())
	defer c1.Close()
	first := <-accepted
	c2, _ := net.Dial("tcp", ln.Addr().String()) // same IP: closed by the limit
	defer c2.Close()
	c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c2.Read(make([]byte, 1)); err == nil {
		t.Error("second connection from one IP not closed")
	}
	first.Close()
	c3, _ := net.Dial("tcp", ln.Addr().String())
	defer c3.Close()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Error("slot not released")
	}
}

func TestSource2Weapon(t *testing.T) {
	for _, c := range [][3]string{
		{"usp_silencer_off", "CT", "usp_silencer"},
		{"m4a1_silencer_off", "CT", "m4a1_silencer"},
		{"inferno", "CT", "incgrenade"},
		{"inferno", "TERRORIST", "inferno"},
		{"ak47", "CT", "ak47"},
	} {
		if got := source2Weapon(c[0], c[1]); got != c[2] {
			t.Errorf("%s (%s): %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestSource2SourceTV(t *testing.T) {
	tv := logparse.Player{Name: "SourceTV", SteamID: "BOT"}
	bot := logparse.Player{Name: "Romanov", UserID: 5, SteamID: "BOT"}
	s2 := &serverState{info: stats.Server{Engine: games.Source2}}
	s1 := &serverState{info: stats.Server{Engine: games.Source}}
	if s2.tracks(tv) || !s2.tracks(bot) || !s1.tracks(tv) {
		t.Errorf("tracks: cs2 SourceTV %v, cs2 bot %v, css SourceTV %v", s2.tracks(tv), s2.tracks(bot), s1.tracks(tv))
	}
}

// With the gameWE CS2 plugin its weaponstats carry damage, kills,
// headshots, deaths, shots and hits per weapon; the attacked and kill lines
// then only add the hit groups, so nothing is counted twice. That holds also
// when the plugin's first line comes after the hits of the round.
func TestSource2PluginWeaponStats(t *testing.T) {
	a := `"Pat<2><[U:1:42]><%s>"`
	b := `"Bob<3><[U:1:43]><%s>"`
	f := func(p, team string) string { return fmt.Sprintf(p, team) }
	start := `gameWE Plugin 0.2.0 (CounterStrikeSharp)`
	hits := []string{
		`World triggered "Round_Start"`,
		f(a, "") + ` connected, address "198.51.100.41:27005"`,
		f(b, "") + ` connected, address "198.51.100.42:27005"`,
		f(a, "Unassigned") + ` joined team "CT"`,
		f(b, "Unassigned") + ` joined team "TERRORIST"`,
		f(a, "CT") + ` [10 20 30] attacked ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (damage "27") (damage_armor "3") (health "73") (armor "97") (hitgroup "neck")`,
		f(a, "CT") + ` [10 20 30] attacked ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (damage "73") (damage_armor "0") (health "0") (armor "97") (hitgroup "head")`,
		f(a, "CT") + ` [10 20 30] killed ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (headshot)`,
	}
	plugin := []string{
		f(b, "TERRORIST") + ` triggered "weaponstats" (weapon "m4a1_silencer") (shots "0") (hits "0") (kills "0") (headshots "0") (tks "0") (damage "0") (deaths "1")`,
		f(a, "CT") + ` triggered "weaponstats" (weapon "m4a1_silencer") (shots "5") (hits "2") (kills "1") (headshots "1") (tks "0") (damage "100") (deaths "0")`,
	}
	end := []string{`World triggered "Round_End"`}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	for name, tc := range map[string]struct {
		lines []string
		pat   stats.WeaponStats
		bob   stats.WeaponStats
	}{
		"no plugin": {cat(hits, end),
			stats.WeaponStats{Damage: 100, Kills: 1, Headshots: 1, Head: 1, Neck: 1}, stats.WeaponStats{Deaths: 1}},
		"start line first": {cat([]string{start}, hits, plugin, end),
			stats.WeaponStats{Shots: 5, Hits: 2, Damage: 100, Kills: 1, Headshots: 1, Head: 1, Neck: 1}, stats.WeaponStats{Deaths: 1}},
		"weaponstats after the hits": {cat(hits, plugin, end),
			stats.WeaponStats{Shots: 5, Hits: 2, Damage: 100, Kills: 1, Headshots: 1, Head: 1, Neck: 1}, stats.WeaponStats{Deaths: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			st := newMemStore()
			hub := NewHub(st, settings.NewLive(settings.Defaults()), &recorder{}, quiet)
			hub.Source2Hits = true // before the server's worker starts
			hub.SetServers(st.addServers([]string{"192.0.2.20:27015"}, map[string]string{"192.0.2.20:27015": "cs2"}))
			var body []string
			for _, l := range tc.lines {
				body = append(body, "10/26/2026 - 21:00:00.250 - "+l)
			}
			if n, err := hub.DispatchLines(netip.MustParseAddrPort("192.0.2.20:27015"), body, time.Now().UTC()); err != nil || n != len(body) {
				t.Fatalf("dispatch: %d %v", n, err)
			}
			hub.Close(context.Background())
			st.mu.Lock()
			defer st.mu.Unlock()
			if got := st.weapons[[2]string{"0:21", "m4a1_silencer"}]; got != tc.pat {
				t.Errorf("Pat m4a1_silencer: %+v, want %+v", got, tc.pat)
			}
			if got := st.weapons[[2]string{"1:21", "m4a1_silencer"}]; got != tc.bob {
				t.Errorf("Bob m4a1_silencer: %+v, want %+v", got, tc.bob)
			}
		})
	}
}
