// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
)

// A server logs some lines with Steam3 ids and others with STEAM_x ids for
// the same player: one player, one visit.
func TestSteam3AndSteam2AreOnePlayer(t *testing.T) {
	st := newMemStore()
	srv := st.addServers([]string{"192.0.2.1:27015"}, nil)[0]
	p := NewProcessor(st, settings.NewLive(settings.Defaults()), srv, &recorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"Player One<2><[U:1:2469]><>" connected, address "192.0.2.9:27005"`,
		`"Player One<2><[U:1:2469]><CT>" killed "Bob<3><[U:1:4690]><TERRORIST>" with "ak47"`,
		`"Player One<2><STEAM_0:1:1234><>" triggered "weaponstats" (weapon "ak47") (shots "3") (hits "1") (kills "1") (headshots "0") (tks "0") (damage "30") (deaths "0")`,
		`"Bob<3><STEAM_0:0:2345><>" triggered "weaponstats" (weapon "glock") (shots "2") (hits "0") (kills "0") (headshots "0") (tks "0") (damage "0") (deaths "1")`,
		`"Player One<2><STEAM_0:1:1234><CT>" disconnected (reason "Disconnect by user.")`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.players) != 2 {
		t.Errorf("%d players, want 2: %v", len(st.players), st.players)
	}
	for _, uid := range []string{"1:1234", "0:2345"} {
		if st.players[[2]string{"css", uid}] == nil {
			t.Errorf("no player %s", uid)
		}
	}
	if len(st.joins) != 2 || len(st.leaves) != 1 {
		t.Errorf("%d visits started, %d ended; want 2, 1", len(st.joins), len(st.leaves))
	}
	if w := st.weapons[[2]string{"1:1234", "ak47"}]; w.Shots != 3 {
		t.Errorf("weaponstats of the STEAM_x line: %+v", w)
	}
}
