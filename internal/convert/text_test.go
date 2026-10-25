// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"testing"
	"time"
)

func TestRepair(t *testing.T) {
	for in, want := range map[string]string{
		"MÃ¼ller":           "Müller",
		"JosÃ© GarcÃ­a":     "José García",
		"Ã„rger Ã–l ÃŸ":     "Ärger Öl ß",
		"â‚¬uro":            "€uro", // € is three bytes, the second one 0x82 is ‚ in cp1252
		"ÐŸÑ€Ð¸Ð²ÐµÑ‚":      "Привет",
		"ðŸ˜€ smile":        "😀 smile",
		"Müller":            "Müller", // correct latin1 text stays
		"plain ascii [TAG]": "plain ascii [TAG]",
		"Ã":                 "Ã",          // 0xC3 alone is no UTF-8
		"Ãx":                "Ãx",         // nor 0xC3 'x'
		"日本":                "日本",         // not latin1 at all
		"MÃ¼ller 日本":        "MÃ¼ller 日本", // mixed: left alone
		"":                  "",
	} {
		if got := Repair(in); got != want {
			t.Errorf("Repair(%q) = %q, want %q", in, got, want)
		}
		if LooksDoubleEncoded(in) != (in != want) {
			t.Errorf("LooksDoubleEncoded(%q) = %v", in, !(in != want))
		}
	}
	// Repairing twice changes nothing more.
	if got := Repair(Repair("MÃ¼ller")); got != "Müller" {
		t.Errorf("twice: %q", got)
	}
}

func TestLatin1Byte(t *testing.T) {
	for b := 0; b < 256; b++ {
		var r rune
		switch {
		case b >= 0x80 && b < 0xa0:
			r = cp1252[b-0x80]
		default:
			r = rune(b)
		}
		got, ok := latin1Byte(r)
		if !ok || int(got) != b {
			t.Errorf("latin1Byte(%U) = %#x, %v; want %#x", r, got, ok, b)
		}
	}
	if _, ok := latin1Byte('日'); ok {
		t.Error("日 in latin1")
	}
}

func TestParseMojibake(t *testing.T) {
	for _, s := range []string{"auto", "repair", "keep"} {
		if _, err := ParseMojibake(s); err != nil {
			t.Error(err)
		}
	}
	if _, err := ParseMojibake("fix"); err == nil {
		t.Error("fix accepted")
	}
}

func TestRepairsPerTable(t *testing.T) {
	p := &Plan{lat1: map[string]bool{"hlstats_playernames": true}}
	for mode, want := range map[string][2]bool{MojibakeAuto: {true, false}, MojibakeRepair: {true, true}, MojibakeKeep: {false, false}} {
		p.Mojibake = mode
		if got := [2]bool{p.repairs("hlstats_PlayerNames"), p.repairs("hlstats_Events_Chat")}; got != want {
			t.Errorf("%s: %v, want %v", mode, got, want)
		}
	}
}

func TestDateTime(t *testing.T) {
	for _, c := range []struct {
		t      time.Time
		layout string
		want   string
	}{
		{time.Date(2024, 3, 31, 2, 30, 0, 0, time.UTC), time.DateTime, "2024-03-31 02:30:00"},
		{time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), time.DateOnly, "2024-03-31"},
		{time.Time{}, time.DateTime, "0000-00-00 00:00:00"},
		{time.Time{}, time.DateOnly, "0000-00-00"},
	} {
		if got := dateTime(c.t, c.layout); got != c.want {
			t.Errorf("dateTime(%v, %q) = %q, want %q", c.t, c.layout, got, c.want)
		}
	}
}
