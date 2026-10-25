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

// A2S_INFO answers come from the network (any sender that can spoof the
// game server's address).
func FuzzParseA2SInfo(f *testing.F) {
	for _, b := range [][]byte{a2sSourceReply(240, 'l', 1, "9540945"), a2sSourceReply(2400, 'w', 0, "1"),
		a2sGoldSrcReply('l', 1, true), {0xFF, 0xFF, 0xFF, 0xFF, 'A', 1, 2, 3, 4}} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		info, ch, err := ParseA2SInfo(b)
		if err != nil {
			return
		}
		if ch != nil && len(ch) != 4 {
			t.Errorf("challenge of %d bytes", len(ch))
		}
		if len(info.Version) > maxVersionLen || len(info.OS) > 8 || len(info.VAC) > 8 {
			t.Errorf("too long: %+v", info)
		}
		for i := 0; i < len(info.Version); i++ {
			if c := info.Version[i]; c < 0x20 || c >= 0x7F {
				t.Errorf("unprintable version %q", info.Version)
			}
		}
	})
}
