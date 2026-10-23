// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"testing"

	"gamewe-collector/internal/logparse"
)

// RCON status answers come from the network.
func FuzzParseStatus(f *testing.F) {
	for _, s := range []string{cssStatus, hldsStatus, "hostname: x\nmap : y\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		st, err := ParseStatus(s)
		if err == nil {
			for _, p := range st.Players {
				ref := logparse.Player{Name: p.Name, UserID: p.UserID, SteamID: p.SteamID}
				if ref.Trackable() && p.UniqueID == "" {
					t.Errorf("trackable player without unique id: %+v", p)
				}
			}
		}
	})
}
