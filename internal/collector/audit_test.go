// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
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
