// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/stats"
)

func TestDispatchLimits(t *testing.T) {
	const cs2, css = "192.0.2.5:27015", "192.0.2.6:27015"
	hub := newTestHub(newMemStore(), &recorder{}, []string{cs2, css}, map[string]string{cs2: "cs2", css: "css"})
	defer hub.Close(t.Context())
	now := time.Now().UTC()
	// CS2 logs over HTTP: UDP has neither its token nor a secret.
	if err := hub.Dispatch(netip.MustParseAddrPort(cs2), pkt("x"), now); !errors.Is(err, ErrSource2UDP) {
		t.Errorf("UDP line for a CS2 row: %v", err)
	}
	hub.Source2UDP = true
	if err := hub.Dispatch(netip.MustParseAddrPort(cs2), pkt("x"), now); err != nil {
		t.Errorf("with listen.udp.source2: %v", err)
	}
	// One limit for both inputs: the queue counts lines, not bytes.
	long := strings.Repeat("a", MaxHTTPLine+maxLinePrefix)
	if err := hub.Dispatch(netip.MustParseAddrPort(css), pkt(long), now); !errors.Is(err, ErrLineTooLong) {
		t.Errorf("long line: %v", err)
	}
	if err := hub.Dispatch(netip.MustParseAddrPort(css), pkt(strings.Repeat("a", MaxHTTPLine-100)), now); err != nil {
		t.Errorf("line within the limit: %v", err)
	}
}

func TestHTTPRowSecret(t *testing.T) {
	const dummyToken = "dummy-not-secret-1"
	const a = "192.0.2.5:27015"
	hub := newTestHub(newMemStore(), &recorder{}, []string{a}, map[string]string{a: "cs2"})
	defer hub.Close(t.Context())
	src := netip.MustParseAddrPort(a)
	if !hub.httpSecretOK(src, "") {
		t.Error("no stored secret: not checked")
	}
	hub.resolve(src).srv.LogSecret = "55"
	if hub.httpSecretOK(src, "") || hub.httpSecretOK(src, "54") || !hub.httpSecretOK(src, "55") {
		t.Error("stored secret must match")
	}
	hh := &httpHandler{cfg: HTTPConfig{Token: dummyToken}}
	if p, s, err := hh.port("/" + dummyToken + "/27015/55"); err != nil || p != 27015 || s != "55" {
		t.Errorf("token form: %d %q %v", p, s, err)
	}
	if _, _, err := hh.port("/27015/55"); err == nil {
		t.Error("no token, token set")
	}
	hh = &httpHandler{}
	if p, s, err := hh.port("/27015/55"); err != nil || p != 27015 || s != "55" {
		t.Errorf("no token: %d %q %v", p, s, err)
	}
	if got := RedactURL("http://x:1/"+dummyToken+"/27015/55", dummyToken); got != "http://x:1/<token>/27015/<secret>" {
		t.Errorf("redact: %s", got)
	}
}

func TestVACKickChecksStatus(t *testing.T) {
	rec := &recorder{}
	w := &VACBlock{Reply: rec, Log: quiet, Status: func(stats.Server) (Status, error) {
		return Status{Players: []StatusPlayer{{UserID: 42, UniqueID: "0:9"}, {UserID: 43, UniqueID: "1:5"}}}, nil
	}}
	srv := stats.Server{RconPassword: "dummy"}
	w.kick(vacItem{srv: srv, userID: 42, uniqueID: "1:5"}) // another player's id
	w.kick(vacItem{srv: srv, userID: 43, uniqueID: "1:5"}) // the banned player
	w.kick(vacItem{srv: srv, userID: 44, uniqueID: "1:5"}) // nobody: harmless
	if got := fmt.Sprint(rec.sent); strings.Contains(got, "kickid 42") || !strings.Contains(got, "kickid 43") {
		t.Errorf("kicks: %s", got)
	}
}
