// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

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

// Chat text comes from the log (any player, or a forged packet): the
// argument of a command stays short valid UTF-8, and a site address that
// siteBase accepts cannot leave the quoted browse argument.
func FuzzParseCommand(f *testing.F) {
	for _, s := range []string{"!rank", "/weapons ak47", "!actions x\";quit", "kill him", "!gamewe_reset 1", "\xff!top20"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		name, arg := parseCommand(s)
		if name == "" && arg != "" {
			t.Errorf("argument %q without a command", arg)
		}
		if len(arg) > 64 || !utf8.ValidString(arg) {
			t.Errorf("argument %q: %d bytes or invalid UTF-8", arg, len(arg))
		}
		if base, ok := siteBase(s); ok && (strings.ContainsAny(base, "\";\\ \n") || Sanitize(base) != base) {
			t.Errorf("siteBase accepted %q", base)
		}
	})
}

// FuzzHTTPLogBody posts hostile bodies to the Counter-Strike 2 HTTP input of
// a registered server: no panic (also not in the line workers), and the
// answer is 200, or 413 when the body is too large.
func FuzzHTTPLogBody(f *testing.F) {
	for _, s := range []string{
		"10/25/2026 - 12:00:00.123 - World triggered \"Round_Start\"\n",
		"10/25/2026 - 12:00:00.123 - \"A<2><[U:1:42]><CT>\" [1 2 3] attacked \"V<3><[U:1:43]><TERRORIST>\" [4 5 6] with \"ak47\" (damage \"27\") (hitgroup \"neck\")\n",
		"10/25/2026 - 12:00:00.123 - \"A<2><[U:1:42]><CT>\" [1 2 3] killed \"V<3><[U:1:43]><TERRORIST>\" [4 5 6] with \"inferno\" (headshot)\r\n\x00",
		"L 10/25/2026 - 12:00:00: Started map \"de_nuke\"\n\n\n",
		"",
	} {
		f.Add(s)
	}
	st := newMemStore()
	hub := newTestHub(st, &recorder{}, []string{"192.0.2.10:27015"}, map[string]string{"192.0.2.10:27015": "cs2"})
	hub.Source2Hits = true
	f.Cleanup(func() { hub.Close(context.Background()) })
	h := newHTTPHandler(httpCfg(), hub, newRejectLog(quiet, "test"), quiet)
	f.Fuzz(func(t *testing.T, body string) {
		if code := post(h, "192.0.2.10:51000", "/27015", body); code != 200 && code != 413 {
			t.Fatalf("status %d", code)
		}
	})
}
