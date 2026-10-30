// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
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

	// A new secret in the database (CLI or web admin): the old one is
	// accepted for the grace time.
	w.gate.fromDB("222", now, time.Minute)
	send(t, h, w, spkt("222", "new"), now, nil)
	send(t, h, w, spkt("111", "old"), now.Add(30*time.Second), nil)
	send(t, h, w, spkt("111", "too old"), now.Add(2*time.Minute), logparse.ErrBadSecret)
	// A reload with the same stored value changes nothing.
	w.gate.fromDB("222", now.Add(time.Hour), time.Minute)
	send(t, h, w, spkt("222", "still"), now, nil)
	if got := strings.Join(queued(w), ","); got != "new,old,still" {
		t.Errorf("queued %s", got)
	}

	// The secret removed in the database: everything is accepted again.
	w.gate.fromDB("", now, time.Minute)
	send(t, h, w, pkt("plain"), now, nil)
	send(t, h, w, spkt("5", "any"), now, nil)
	if got := strings.Join(queued(w), ","); got != "plain,any" {
		t.Errorf("after removal: queued %s", got)
	}
}

func TestSecretNotChecked(t *testing.T) {
	now := time.Now()
	// No secret stored: every packet is accepted, with or without RCON.
	for _, rconPW := range []string{"pw", ""} {
		srv := stats.Server{ID: 1, RconPassword: rconPW}
		h, w := secretWorker(t, SecretOn, srv)
		send(t, h, w, pkt("a"), now, nil)
		send(t, h, w, spkt("5", "b"), now, nil)
		if got := strings.Join(queued(w), ","); got != "a,b" {
			t.Errorf("empty secret, rcon %q: queued %s", rconPW, got)
		}
		srv.Address = "192.0.2.1:27015"
		if msg, info := SecretNote(SecretOn, srv); !info || !strings.Contains(msg, "server 1 192.0.2.1:27015 has no log secret, all its log packets are accepted") || strings.Contains(msg, "auto") {
			t.Errorf("no secret note %q %v", msg, info)
		}
	}
	// A stored secret is checked without RCON too.
	h, w := secretWorker(t, SecretOn, stats.Server{ID: 4, LogSecret: "111"})
	send(t, h, w, pkt("d"), now, logparse.ErrBadSecret)
	send(t, h, w, spkt("111", "e"), now, nil)
	if got := strings.Join(queued(w), ","); got != "e" {
		t.Errorf("without rcon: queued %s", got)
	}
	// GoldSrc has no sv_logsecret: a stored secret is ignored.
	srv := stats.Server{ID: 2, LogSecret: "111", RconPassword: "pw", Engine: games.GoldSrc}
	h, w = secretWorker(t, SecretOn, srv)
	send(t, h, w, []byte("\xff\xff\xff\xfflog "+secretTS+"c\n\x00"), now, nil)
	if got := strings.Join(queued(w), ","); got != "c" {
		t.Errorf("GoldSrc: queued %s", got)
	}
	if msg, info := SecretNote(SecretOn, srv); !info || !strings.Contains(msg, "GoldSrc") || !strings.Contains(msg, "ignored") {
		t.Errorf("GoldSrc note %q %v", msg, info)
	}
	// GoldSrc and CS2 send no log secret: without a stored one there is
	// nothing to say, a stored one is ignored.
	for _, eng := range []games.Engine{games.GoldSrc, games.Source2} {
		if msg, _ := SecretNote(SecretOn, stats.Server{ID: 4, Engine: eng}); msg != "" {
			t.Errorf("engine %v without secret: note %q", eng, msg)
		}
	}
	if msg, info := SecretNote(SecretOn, stats.Server{ID: 5, Address: "192.0.2.5:27015", Engine: games.Source2, LogSecret: "111"}); !info || !strings.Contains(msg, "Source 2 (CS2)") || !strings.Contains(msg, "ignored") {
		t.Errorf("CS2 with secret: note %q %v", msg, info)
	}
	if msg, _ := SecretNote(SecretOn, stats.Server{ID: 3, LogSecret: "111"}); msg != "" {
		t.Errorf("with secret: note %q", msg)
	}
	if msg, _ := SecretNote(SecretOff, stats.Server{ID: 3}); msg != "" {
		t.Errorf("off: note %q", msg)
	}
}

// The collector never sets or reads sv_logsecret over RCON, with or
// without a stored secret.
func TestSecretNotManagedOverRcon(t *testing.T) {
	old := firstLogAddress
	firstLogAddress = 10 * time.Millisecond
	t.Cleanup(func() { firstLogAddress = old })
	for _, secret := range []string{"", "111"} {
		var mu sync.Mutex
		var cmds []string
		f := rcontest.Start(t, "pw", func(cmd string) string {
			mu.Lock()
			defer mu.Unlock()
			cmds = append(cmds, cmd)
			return ""
		})
		st := newMemStore()
		srv := stats.Server{ID: 1, LogSecret: secret, AddrPort: netip.MustParseAddrPort(f.Addr)}
		srv.Address, srv.Name, srv.Game, srv.GameID, srv.RconPassword = f.Addr, "srv", "css", 3, "pw"
		st.servers[srv.Address] = srv
		set := settings.Defaults()
		set.RconTimeout = 1
		h := NewHub(st, settings.NewLive(set), &recorder{}, quiet)
		h.Monitor = MonitorConfig{LogAddress: true, LogInterval: 20 * time.Millisecond, ListenPort: 41337}
		h.Secret = SecretConfig{Mode: SecretOn}
		h.SetServers([]stats.Server{srv})
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			n := len(cmds)
			mu.Unlock()
			if n >= 4 || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		h.Close(context.Background())
		mu.Lock()
		if len(cmds) == 0 {
			t.Errorf("secret %q: no RCON commands at all", secret)
		}
		for _, c := range cmds {
			if strings.Contains(c, "sv_logsecret") {
				t.Errorf("secret %q: the collector sent %q", secret, c)
			}
		}
		mu.Unlock()
	}
}
