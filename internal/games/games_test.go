// SPDX-License-Identifier: GPL-3.0-or-later

package games

import (
	"slices"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	for _, c := range []struct {
		realgame, code, base string
		engine               Engine
		warn                 bool
	}{
		{"css", "css1", "css", Source, false},
		{"cstrike", "cs16_public", "cstrike", GoldSrc, false},
		{"czero", "cz_public", "czero", GoldSrc, false},
		{"", "czero", "czero", GoldSrc, false},
		{"", "czero_bots", "czero", GoldSrc, true},
		{"cs2", "cs2", "cs2", Source2, false},
		{"", "cs2", "cs2", Source2, false},
		{"", "cs2_retake", "cs2", Source2, true},
		{"", "csgo5", "csgo", Source, true},
		{"", "css3", "css", Source, true},
		{"", "dods", "dods", Source, false},
		{"", "dod_x", "dod", GoldSrc, true},
		{"", "tfc", "tfc", GoldSrc, true},
		{"", "tf2fun", "tf", Source, true},
		{"", "l4d2", "l4d2", Source, false},
		{"", "zps", "zps", Source, false},
		{"", "ff", "ff", Source, true},
		{"", "csp", "csp", Source, true},
		{"", "fof", "fof", Source, false},
		{"nmrih", "nmrih_coop", "nmrih", Source, false},
		{"unknown", "myserver", "css", Source, true},
		{"VALVE", "hldm", "valve", GoldSrc, false},
	} {
		base, e, warn := Resolve(c.realgame, c.code)
		if base != c.base || e != c.engine || (warn != "") != c.warn {
			t.Errorf("Resolve(%q, %q) = %s, %s, %q", c.realgame, c.code, base, e, warn)
		}
	}
}

// A retired game keeps its engine (tfc is GoldSrc, not tf; ns not css), so
// its servers keep running, but it is no longer offered or a default ranking.
func TestRetired(t *testing.T) {
	want := map[string]Engine{"tfc": GoldSrc, "ns": GoldSrc, "insurgency": Source, "ff": Source, "csp": Source}
	if got := Retired(); len(got) != len(want) {
		t.Errorf("Retired() = %v", got)
	}
	for code, engine := range want {
		for _, in := range [][2]string{{code, code + "_public"}, {"", code}} {
			base, e, warn := Resolve(in[0], in[1])
			if base != code || e != engine || !strings.Contains(warn, "no longer supported") {
				t.Errorf("Resolve(%q, %q) = %s, %s, %q", in[0], in[1], base, e, warn)
			}
		}
		if IsBase(code) || slices.Contains(Known(), code) {
			t.Errorf("%s is still a supported base game", code)
		}
	}
}

func TestIsBase(t *testing.T) {
	for _, code := range Known() {
		if !IsBase(code) || !IsBase(" "+strings.ToUpper(code)+" ") {
			t.Errorf("IsBase(%q) = false", code)
		}
	}
	for _, code := range []string{"", "css1", "gungame", "cs2_retake", "dod_x", "tf2"} {
		if IsBase(code) {
			t.Errorf("IsBase(%q) = true", code)
		}
	}
}
