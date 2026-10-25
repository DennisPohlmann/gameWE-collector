// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
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
	h := newHTTPHandler(cfg, hub, newRejectLog(quiet))
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
	h := newHTTPHandler(cfg, hub, newRejectLog(quiet))
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
	h := newHTTPHandler(cfg, hub, newRejectLog(quiet))
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
	l := newConnLimit(ln, 3, 1, newRejectLog(quiet))
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
