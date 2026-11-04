// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import "testing"

// A name holding fake tag fields must not take over another player's id.
func TestL4DNameCannotFakeIdentity(t *testing.T) {
	for _, line := range []string{
		`"<1><[U:1:1000]><><><5><STEAM_1:0:42><CT>" say "!hideranking 1 ><setpos_exact x"`,
		`"x<1><STEAM_1:0:1000><T><5><STEAM_1:0:42><CT>" say "hello ><setpos_exact x"`,
	} {
		e, ok := ParseLine(line)
		if !ok {
			t.Fatalf("not parsed: %s", line)
		}
		s, isSay := e.(Say)
		if !isSay || s.Player.SteamID != "STEAM_1:0:42" || s.Player.UserID != 5 {
			t.Errorf("%s: %+v", line, e)
		}
	}
}
