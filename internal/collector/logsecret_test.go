// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/rcon/rcontest"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

const secretTS = "L 10/22/2026 - 12:00:00: "

func spkt(secret, line string) []byte {
	return []byte("\xff\xff\xff\xffS" + secret + secretTS + line + "\n\x00")
}

// secretWorker is a hub with one worker that is not running, so the
// test reads what Dispatch queued.
func secretWorker(t *testing.T, mode SecretMode, srv stats.Server) (*Hub, *worker) {
	t.Helper()
	h := NewHub(newMemStore(), settings.NewLive(settings.Defaults()), &recorder{}, quiet)
	h.Secret.Mode = mode
	if srv.AddrPort == (netip.AddrPort{}) {
		srv.AddrPort = netip.MustParseAddrPort("192.0.2.50:27015")
	}
	srv.Address = srv.AddrPort.String()
	w := &worker{id: srv.ID, addr: srv.Address, srv: srv, queue: make(chan packet, 64), gate: newSecretGate()}
	h.applySecret(w, srv, time.Now(), time.Minute)
	h.byID[srv.ID], h.exact[srv.AddrPort] = w, w
	h.byIP[srv.AddrPort.Addr()] = []*worker{w}
	return h, w
}

// queued returns the lines in w's queue (without the time stamp prefix).
func queued(w *worker) []string {
	var out []string
	for {
		select {
		case pk := <-w.queue:
			out = append(out, strings.TrimPrefix(pk.line, secretTS))
		default:
			return out
		}
	}
}

func send(t *testing.T, h *Hub, w *worker, b []byte, now time.Time, want error) {
	t.Helper()
	if err := h.Dispatch(w.srv.AddrPort, b, now); !errors.Is(err, want) {
		t.Errorf("Dispatch(%q) = %v, want %v", b, err, want)
	}
}

func TestSecretOff(t *testing.T) {
	h, w := secretWorker(t, SecretOff, stats.Server{ID: 1, LogSecret: "111", RconPassword: "pw"})
	now := time.Now()
	send(t, h, w, pkt("a"), now, nil)
	send(t, h, w, spkt("999", "b"), now, nil) // not checked, the secret is cut off
	if got := strings.Join(queued(w), ","); got != "a,b" {
		t.Errorf("queued %s", got)
	}
}

func TestSecretOn(t *testing.T) {
	h, w := secretWorker(t, SecretOn, stats.Server{ID: 1, LogSecret: "111", RconPassword: "pw"})
	now := time.Now()
	send(t, h, w, spkt("111", "ok"), now, nil)
	send(t, h, w, spkt("222", "wrong"), now, logparse.ErrBadSecret)
	send(t, h, w, pkt("none"), now, logparse.ErrBadSecret)
	send(t, h, w, spkt("1111", "longer"), now, logparse.ErrBadSecret)
	if got := strings.Join(queued(w), ","); got != "ok" {
		t.Errorf("queued %s", got)
	}

	// A new secret in the database: the old one is accepted for the grace time.
	w.gate.fromDB("222", now, false, now, time.Minute)
	send(t, h, w, spkt("222", "new"), now, nil)
	send(t, h, w, spkt("111", "old"), now.Add(30*time.Second), nil)
	send(t, h, w, spkt("111", "too old"), now.Add(2*time.Minute), logparse.ErrBadSecret)
	// A reload that read the row before (the same stored value) changes nothing.
	w.gate.fromDB("222", now, false, now, time.Minute)
	send(t, h, w, spkt("222", "still"), now, nil)
	if got := strings.Join(queued(w), ","); got != "new,old,still" {
		t.Errorf("queued %s", got)
	}

	// A pending secret (being set over RCON) is accepted already.
	w.gate.setPending("333")
	send(t, h, w, spkt("333", "pending"), now, nil)
	w.gate.setPending("")
	send(t, h, w, spkt("333", "dropped"), now, logparse.ErrBadSecret)
	if got := strings.Join(queued(w), ","); got != "pending" {
		t.Errorf("queued %s", got)
	}

	// mode true never quarantines, even with RCON.
	if w.gate.verify {
		t.Error("mode true verifies over RCON")
	}
}

func TestSecretNotChecked(t *testing.T) {
	now := time.Now()
	// No secret stored: not checked.
	h, w := secretWorker(t, SecretOn, stats.Server{ID: 1, RconPassword: "pw"})
	send(t, h, w, pkt("a"), now, nil)
	send(t, h, w, spkt("5", "b"), now, nil)
	if got := strings.Join(queued(w), ","); got != "a,b" {
		t.Errorf("empty secret: queued %s", got)
	}
	// GoldSrc has no sv_logsecret: a stored secret is ignored.
	srv := stats.Server{ID: 2, LogSecret: "111", RconPassword: "pw", Engine: games.GoldSrc}
	h, w = secretWorker(t, SecretAuto, srv)
	send(t, h, w, []byte("\xff\xff\xff\xfflog "+secretTS+"c\n\x00"), now, nil)
	if got := strings.Join(queued(w), ","); got != "c" || w.gate.verify {
		t.Errorf("GoldSrc: queued %s, verify %v", got, w.gate.verify)
	}
	if msg, info := SecretNote(SecretAuto, srv); !info || !strings.Contains(msg, "GoldSrc") {
		t.Errorf("GoldSrc note %q %v", msg, info)
	}
	if msg, info := SecretNote(SecretOn, stats.Server{ID: 3, Address: "192.0.2.1:27015"}); info || !strings.Contains(msg, "log_secret enabled, server 3 192.0.2.1:27015 has no secret") {
		t.Errorf("no secret note %q %v", msg, info)
	}
	if msg, _ := SecretNote(SecretAuto, stats.Server{ID: 3, RconPassword: "pw"}); msg != "" {
		t.Errorf("auto with rcon: note %q", msg)
	}
	if msg, _ := SecretNote(SecretOff, stats.Server{ID: 3}); msg != "" {
		t.Errorf("off: note %q", msg)
	}
	// auto without rcon_password: checked like true, no quarantine.
	h, w = secretWorker(t, SecretAuto, stats.Server{ID: 4, LogSecret: "111"})
	send(t, h, w, pkt("d"), now, logparse.ErrBadSecret)
	send(t, h, w, spkt("111", "e"), now, nil)
	if got := strings.Join(queued(w), ","); got != "e" {
		t.Errorf("auto without rcon: queued %s", got)
	}
}

// auto: packets without the secret wait in the quarantine; valid ones wait
// behind them, so the order is kept.
func TestSecretQuarantine(t *testing.T) {
	h, w := secretWorker(t, SecretAuto, stats.Server{ID: 1, LogSecret: "111", RconPassword: "pw"})
	now := time.Now()
	send(t, h, w, pkt("r1"), now, nil)
	send(t, h, w, spkt("111", "s1"), now, nil)
	send(t, h, w, pkt("r2"), now, nil)
	if got := queued(w); len(got) != 0 {
		t.Fatalf("queued during quarantine: %v", got)
	}
	select {
	case <-w.gate.check:
	default:
		t.Fatal("no sv_logsecret check requested")
	}
	// Forged: only the line with the secret goes on.
	h.endQuarantine(w, false, "test")
	if got := strings.Join(queued(w), ","); got != "s1" {
		t.Errorf("forged: queued %s", got)
	}
	send(t, h, w, spkt("111", "s2"), now, nil) // no quarantine: straight on
	if got := strings.Join(queued(w), ","); got != "s2" {
		t.Errorf("after: queued %s", got)
	}

	// Restart: every line in order, and packets without the secret are
	// accepted shortly after.
	send(t, h, w, pkt("r3"), now, nil)
	send(t, h, w, spkt("111", "s3"), now, nil)
	h.endQuarantine(w, true, "test")
	send(t, h, w, pkt("r4"), time.Now(), nil)
	if got := strings.Join(queued(w), ","); got != "r3,s3,r4" {
		t.Errorf("restart: queued %s", got)
	}
	send(t, h, w, pkt("r5"), time.Now().Add(restartGrace+time.Second), nil) // quarantined again
	if got := queued(w); len(got) != 0 {
		t.Errorf("after the restart grace: queued %v", got)
	}
}

func TestSecretQuarantineLimit(t *testing.T) {
	old := quarantineLines
	quarantineLines = 3
	t.Cleanup(func() { quarantineLines = old })
	h, w := secretWorker(t, SecretAuto, stats.Server{ID: 1, LogSecret: "111", RconPassword: "pw"})
	now := time.Now()
	for i := 1; i <= 3; i++ {
		send(t, h, w, pkt("r"+strconv.Itoa(i)), now, nil)
	}
	send(t, h, w, pkt("r4"), now, ErrQuarantineFull)
	// A valid line on a full quarantine: the forged-looking lines are dropped.
	send(t, h, w, spkt("111", "s1"), now, nil)
	if got := strings.Join(queued(w), ","); got != "s1" {
		t.Errorf("queued %s", got)
	}
	if w.gate.quarantined() {
		t.Error("quarantine not emptied")
	}
	// The byte limit too.
	oldB := quarantineBytes
	quarantineBytes = 100
	t.Cleanup(func() { quarantineBytes = oldB })
	send(t, h, w, pkt(strings.Repeat("x", 60)), now, nil)
	send(t, h, w, pkt(strings.Repeat("y", 60)), now, ErrQuarantineFull)
}

// Without an answer the quarantine runs out: valid lines go on, the
// others are dropped.
func TestSecretQuarantineTimeout(t *testing.T) {
	old := quarantineTime
	quarantineTime = 30 * time.Millisecond
	t.Cleanup(func() { quarantineTime = old })
	h, w := secretWorker(t, SecretAuto, stats.Server{ID: 1, LogSecret: "111", RconPassword: "pw"})
	now := time.Now()
	send(t, h, w, pkt("r1"), now, nil)
	send(t, h, w, spkt("111", "s1"), now, nil)
	deadline := time.Now().Add(2 * time.Second)
	for w.gate.quarantined() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := strings.Join(queued(w), ","); got != "s1" {
		t.Errorf("queued %s", got)
	}
}

// fakeSecretServer is a Source server with sv_logsecret. block makes it
// not answer the query (an RCON timeout); ignoreSet makes it not take a
// new value.
type fakeSecretServer struct {
	*rcontest.Server
	mu               sync.Mutex
	value            string
	block, ignoreSet bool
}

func startSecretServer(t *testing.T, value string) *fakeSecretServer {
	f := &fakeSecretServer{value: value}
	f.Server = rcontest.Start(t, "pw", func(cmd string) string {
		f.mu.Lock()
		block := f.block
		f.mu.Unlock()
		switch {
		case cmd == "sv_logsecret":
			if block {
				time.Sleep(time.Second)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return fmt.Sprintf("\"sv_logsecret\" = \"%s\" ( def. \"0\" )\n - If set then include this secret when doing UDP logging (will use 0x53 as packet type, not usual 0x52)\n", f.value)
		case strings.HasPrefix(cmd, "sv_logsecret "):
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.ignoreSet {
				f.value = strings.TrimPrefix(cmd, "sv_logsecret ")
			}
		}
		return ""
	})
	return f
}

func (f *fakeSecretServer) get() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value
}

func (f *fakeSecretServer) set(fn func(f *fakeSecretServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// secretHub runs a hub in auto mode for one server at the fake's
// address; every processed line goes to the returned buffer.
func secretHub(t *testing.T, f *fakeSecretServer, srv stats.Server, cfg MonitorConfig, every time.Duration) (*Hub, *memStore, *syncBuffer) {
	t.Helper()
	oldFirst, oldCheck := firstLogAddress, secretCheckEvery
	firstLogAddress, secretCheckEvery = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { firstLogAddress, secretCheckEvery = oldFirst, oldCheck })
	st := newMemStore()
	srv.AddrPort = netip.MustParseAddrPort(f.Addr)
	srv.Address, srv.Name, srv.Game, srv.GameID, srv.RconPassword = f.Addr, "srv", "css", 3, "pw"
	st.servers[srv.Address] = srv
	set := settings.Defaults()
	set.RconTimeout = 1
	h := NewHub(st, settings.NewLive(set), &recorder{}, quiet)
	var out syncBuffer
	h.LineLog = &out
	h.Monitor = cfg
	h.Secret = SecretConfig{Mode: SecretAuto, Interval: every}
	h.SetServers([]stats.Server{srv})
	t.Cleanup(func() { h.Close(context.Background()) })
	return h, st, &out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A restarted game server sends without its secret: the lines are kept
// and sv_logsecret is set again.
func TestSecretRestart(t *testing.T) {
	f := startSecretServer(t, "0")
	h, _, out := secretHub(t, f, stats.Server{ID: 1, LogSecret: "111", LogSecretChanged: time.Now()}, MonitorConfig{LogInterval: time.Hour}, time.Hour)
	ap := netip.MustParseAddrPort(f.Addr)
	now := time.Now()
	for _, l := range []string{"r1", "r2"} {
		if err := h.Dispatch(ap, pkt(l), now); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "lines after the restart", func() bool { return strings.Contains(out.String(), "r2") })
	if got := out.String(); strings.Index(got, "r1") > strings.Index(got, "r2") {
		t.Errorf("order: %s", got)
	}
	if f.get() != "111" {
		t.Errorf("sv_logsecret not set again: %q", f.get())
	}
}

// The game server has the secret: packets without it are forged.
func TestSecretForged(t *testing.T) {
	f := startSecretServer(t, "111")
	h, _, out := secretHub(t, f, stats.Server{ID: 1, LogSecret: "111", LogSecretChanged: time.Now()}, MonitorConfig{LogInterval: time.Hour}, time.Hour)
	ap := netip.MustParseAddrPort(f.Addr)
	now := time.Now()
	h.Dispatch(ap, pkt("forged"), now)
	h.Dispatch(ap, spkt("111", "real"), now)
	waitFor(t, "the valid line", func() bool { return strings.Contains(out.String(), "real") })
	time.Sleep(20 * time.Millisecond)
	if strings.Contains(out.String(), "forged") {
		t.Errorf("forged line processed: %s", out.String())
	}
	if strings.Contains(strings.Join(f.Commands(), ","), "sv_logsecret 111") {
		t.Error("sv_logsecret set although the server had it")
	}
}

// No answer over RCON: the quarantine runs out, only valid lines go on.
func TestSecretRconTimeout(t *testing.T) {
	old := quarantineTime
	quarantineTime = 300 * time.Millisecond
	t.Cleanup(func() { quarantineTime = old })
	f := startSecretServer(t, "0")
	f.set(func(f *fakeSecretServer) { f.block = true })
	h, _, out := secretHub(t, f, stats.Server{ID: 1, LogSecret: "111", LogSecretChanged: time.Now()}, MonitorConfig{LogInterval: time.Hour}, time.Hour)
	ap := netip.MustParseAddrPort(f.Addr)
	now := time.Now()
	h.Dispatch(ap, pkt("unknown"), now)
	h.Dispatch(ap, spkt("111", "real"), now)
	waitFor(t, "the valid line", func() bool { return strings.Contains(out.String(), "real") })
	if strings.Contains(out.String(), "unknown") {
		t.Errorf("unchecked line processed: %s", out.String())
	}
}

// auto gives a server without a secret one, stores it, and rotates it;
// the previous one stays valid for the grace time.
func TestSecretSetAndRotate(t *testing.T) {
	f := startSecretServer(t, "0")
	h, st, out := secretHub(t, f, stats.Server{ID: 1}, MonitorConfig{LogInterval: 20 * time.Millisecond}, time.Hour)
	stored := func() stats.Server {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.servers[f.Addr]
	}
	waitFor(t, "a secret", func() bool { return stored().LogSecret != "" })
	first := stored().LogSecret
	if err := logparse.CheckSecret(first); err != nil || f.get() != first || stored().LogSecretChanged.IsZero() {
		t.Fatalf("secret %v, server %v, changed %v", err, f.get() == first, stored().LogSecretChanged)
	}
	ap := netip.MustParseAddrPort(f.Addr)
	if err := h.Dispatch(ap, spkt(first, "with"), time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the line", func() bool { return strings.Contains(out.String(), "with") })

	// Rotation is due: a new value, the old one still accepted.
	h.mu.RLock()
	w := h.byID[1]
	h.mu.RUnlock()
	w.gate.mu.Lock()
	w.gate.changed = time.Now().Add(-2 * time.Hour)
	w.gate.mu.Unlock()
	waitFor(t, "a rotation", func() bool { s := stored().LogSecret; return s != first && f.get() == s })
	if err := h.Dispatch(ap, spkt(first, "old"), time.Now()); err != nil {
		t.Errorf("old secret in the grace time: %v", err)
	}
	if err := h.Dispatch(ap, spkt(stored().LogSecret, "new"), time.Now()); err != nil {
		t.Errorf("new secret: %v", err)
	}
	// The game server lost it (restart, no packets yet): set again.
	f.set(func(f *fakeSecretServer) { f.value = "0" })
	waitFor(t, "set again", func() bool { return f.get() == stored().LogSecret })
}

// A failed step leaves the old secret everywhere.
func TestSecretRotateFails(t *testing.T) {
	f := startSecretServer(t, "111")
	f.set(func(f *fakeSecretServer) { f.ignoreSet = true })
	gate := newSecretGate()
	gate.fromDB("111", time.Now().Add(-48*time.Hour), true, time.Now(), time.Minute)
	stored := 0
	j := &secretJob{gate: gate, interval: time.Hour, grace: time.Minute, log: quiet,
		store:   func(context.Context, string, time.Time) error { stored++; return nil },
		verdict: func(bool, string) {}}
	c := rcon.New(false, f.Addr, "pw", time.Second)
	defer c.Close()
	if err := j.manage(c, stats.Server{}); err == nil || !strings.Contains(err.Error(), "did not take") {
		t.Errorf("ignored set: %v", err)
	}
	if cur, _ := gate.snapshot(); cur != "111" || gate.pending != "" || stored != 0 {
		t.Errorf("after a failed set: current %q, pending %q, stored %d", cur, gate.pending, stored)
	}

	// The database fails: the game server gets the old value back.
	f.set(func(f *fakeSecretServer) { f.ignoreSet = false })
	j.store = func(context.Context, string, time.Time) error { return errors.New("db down") }
	if err := j.manage(c, stats.Server{}); err == nil {
		t.Error("store error not returned")
	}
	if cur, _ := gate.snapshot(); cur != "111" || gate.pending != "" || f.get() != "111" {
		t.Errorf("after a failed store: current %q, pending %q, server %q", cur, gate.pending, f.get())
	}

	// A server without sv_logsecret.
	g := rcontest.Start(t, "pw", func(string) string { return "Unknown command \"sv_logsecret\"\n" })
	c2 := rcon.New(false, g.Addr, "pw", time.Second)
	defer c2.Close()
	if err := j.manage(c2, stats.Server{}); !errors.Is(err, errSecretUnsupported) {
		t.Errorf("unknown cvar: %v", err)
	}
	// GoldSrc: nothing is sent.
	if err := j.manage(nil, stats.Server{Engine: games.GoldSrc}); err != nil {
		t.Errorf("GoldSrc: %v", err)
	}
}

func TestQuerySecret(t *testing.T) {
	for out, want := range map[string]string{
		"\"sv_logsecret\" = \"0\" ( def. \"0\" )\n - If set ...\n": "",
		"\"sv_logsecret\" = \"12345\" ( def. \"0\" )\n":            "12345",
		"\"sv_logsecret\" = \"77\"\n - If set ...\n":               "77", // CS:GO
		"sv_logsecret = 42\n":                                      "42",
	} {
		srv := rcontest.Start(t, "pw", func(string) string { return out })
		c := rcon.New(false, srv.Addr, "pw", time.Second)
		got, err := querySecret(c)
		c.Close()
		if err != nil || got != want {
			t.Errorf("querySecret(%q) = %q, %v; want %q", out, got, err, want)
		}
	}
	for i := 0; i < 100; i++ {
		s, err := RandomSecret()
		if err != nil || logparse.CheckSecret(s) != nil {
			t.Fatalf("RandomSecret = %q, %v", s, err)
		}
	}
	if err := setSecret(nil, "1; quit"); err == nil {
		t.Error("setSecret accepted a command")
	}
}
