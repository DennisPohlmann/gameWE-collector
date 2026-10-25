// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"!rank": "rank", "/top10": "top10", "!TOP": "top10", "status": "status",
		"!session please": "session", "rank me": "rank", "hello": "", "": "", "!ranking": "",
		"/statsme": "session", "/next": "next", "!help": "help", "/clans": "", "/gamewe_reset": "",
	}
	for in, want := range cases {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitize(t *testing.T) {
	in := "Eve\"; quit; echo \"\nx\x00"
	want := "Eve', quit, echo 'x"
	if got := Sanitize(in); got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

func TestMenuText(t *testing.T) {
	got := menuText([]string{"Top", `1. a\b - 5`})
	if got != `Top\n1. a/b - 5` {
		t.Errorf("menuText = %q", got)
	}
}

func TestUniqueID(t *testing.T) {
	cases := []struct {
		ref  logparse.Player
		want string
	}{
		{logparse.Player{SteamID: "STEAM_1:0:123"}, "0:123"},
		// Confirmed format of the production database: both universes map to 0:12345.
		{logparse.Player{SteamID: "STEAM_1:0:12345"}, "0:12345"},
		{logparse.Player{SteamID: "STEAM_0:0:12345"}, "0:12345"},
		{logparse.Player{SteamID: "STEAM_0:1:5"}, "1:5"},
		// Steam3 ids are the same players: [U:1:n] is STEAM_0:(n%2):(n/2).
		{logparse.Player{SteamID: "[U:1:246]"}, "0:123"},
		{logparse.Player{SteamID: "[U:1:24691]"}, "1:12345"},
		// Pairs of the Steam3 and STEAM_x forms, also a large id.
		{logparse.Player{SteamID: "[U:1:2469]"}, "1:1234"},
		{logparse.Player{SteamID: "[U:1:900000001]"}, "1:450000000"},
		{logparse.Player{SteamID: "[U:1:4690]"}, "0:2345"},
		{logparse.Player{SteamID: "STEAM_0:0:2345"}, "0:2345"},
		{logparse.Player{SteamID: "[U:1:x]"}, "[U:1:x]"},
		{logparse.Player{SteamID: "[A:1:5:6]"}, "[A:1:5:6]"},
		{logparse.Player{SteamID: "BOT", Name: "Joe"}, "BOT:Joe"},
	}
	for _, c := range cases {
		if got := UniqueID(c.ref); got != c.want {
			t.Errorf("UniqueID(%+v) = %q, want %q", c.ref, got, c.want)
		}
	}
}

// !rank, !statsme, !session and !status open the gameME panel (totals and
// this visit) by default; gamewe_command_display chat / both use chat lines.
func TestCommandDisplay(t *testing.T) {
	for _, mode := range []string{settings.DisplayPanel, settings.DisplayChat, settings.DisplayBoth} {
		p, _ := newTestProcessor(t)
		rec := &recorder{}
		p.cmds.reply = rec
		s := settings.Defaults()
		s.CommandDisplay = mode
		p.set.Set(s)
		ctx, now := context.Background(), time.Now()
		for _, l := range []string{
			`"Alice<2><STEAM_1:0:1><CT>" killed "Bob<3><STEAM_1:0:2><TERRORIST>" with "ak47" (headshot)`,
			`"Alice<2><STEAM_1:0:1><CT>" say "!statsme"`,
		} {
			if err := p.Handle(ctx, l, now); err != nil {
				t.Fatal(err)
			}
		}
		sent := rec.get("192.0.2.1:27015")
		var panels, chats int
		for _, c := range sent {
			switch {
			case strings.HasPrefix(c, "gamewe_msay 15 2 0 "):
				panels++
				want := `gamewe_msay 15 2 0 "->1 - Total\n   Position 1 of 2\n   1005 Points\n   1:0 Frags (1.00)\n   0 (0%) Headshots\n` +
					`   0% Accuracy\n   Time 0d 00:00:00h\n\n->2 - Session\n   +0 Positions\n   +5 Points\n   1:0 Frags (1.00)\n` +
					`   1 (100%) Headshots\n   0% Accuracy\n   ak47\n   Time 0d 00:00:00h"`
				if c != want {
					t.Errorf("panel\n got %s\nwant %s", c, want)
				}
			case strings.HasPrefix(c, "gamewe_psay 2 0 "):
				chats++
			}
		}
		want := map[string][2]int{settings.DisplayPanel: {1, 0}, settings.DisplayChat: {0, 1}, settings.DisplayBoth: {1, 1}}[mode]
		if panels != want[0] || chats != want[1] || len(sent) != panels+chats {
			t.Errorf("%s: sent %q", mode, sent)
		}
	}
}

// Text from log lines (weapon codes, player names) in a panel cannot end the
// quoted argument, start another console command or fake line breaks.
func TestPanelHostileText(t *testing.T) {
	st := &playerState{weaponKills: map[string]int{`ak47";quit;echo "\nx` + "\n\x00": 1}, since: time.Now()}
	got := menuText(statsPanel(stats.Player{Name: "Eve"}, 1, 1, st, time.Now()))
	if strings.ContainsAny(got, "\";\n\x00") {
		t.Errorf("panel text %q contains a quote, semicolon or control character", got)
	}
	if strings.Count(got, `\n`) != 15 {
		t.Errorf("panel has %d line breaks, want 15 (a weapon name must not add one): %q", strings.Count(got, `\n`), got)
	}
}

func TestHideRankingCommand(t *testing.T) {
	for in, want := range map[string]string{
		"!hideranking": "hideranking", "/gamewe_hideranking 1": "hideranking", "!HIDERANKING off": "hideranking",
		"hideranking": "", "!settings": "settings", "/gamewe_settings": "settings", "settings": "",
	} {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q, want %q", in, got, want)
		}
	}
	p, st := newTestProcessor(t)
	rec := &recorder{}
	p.cmds.reply = rec
	ctx, now := context.Background(), time.Now()
	hide := func() int {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.players[[2]string{"css", "0:1"}].hide
	}
	say := func(at time.Time, text string) string {
		t.Helper()
		n := len(rec.get("192.0.2.1:27015"))
		if err := p.Handle(ctx, `"Alice<2><STEAM_1:0:1><CT>" say "`+text+`"`, at); err != nil {
			t.Fatal(err)
		}
		sent := rec.get("192.0.2.1:27015")
		if len(sent) != n+2 || !strings.HasPrefix(sent[n], "gamewe_psay 2 0 \"[gameWE] ") {
			t.Fatalf("%s: replies %q", text, sent[n:])
		}
		// the plugin's settings menu gets the state after every answer
		if want := fmt.Sprintf("gamewe_hidestate 2 %d", hide()); sent[n+1] != want {
			t.Errorf("%s: state %q, want %q", text, sent[n+1], want)
		}
		return sent[n]
	}
	if r := say(now, "!hideranking"); hide() != 3 || !strings.Contains(r, "now hidden") {
		t.Errorf("toggle: %d %q", hide(), r)
	}
	if r := say(now.Add(time.Second), "!hideranking 0"); hide() != 3 || !strings.Contains(r, "wait") {
		t.Errorf("cooldown: %d %q", hide(), r)
	}
	if r := say(now.Add(11*time.Second), "/gamewe_hideranking 0"); hide() != 0 || !strings.Contains(r, "now visible") {
		t.Errorf("show: %d %q", hide(), r)
	}
	if r := say(now.Add(30*time.Second), "!hideranking off"); hide() != 0 || !strings.Contains(r, "already visible") {
		t.Errorf("already: %d %q", hide(), r)
	}
	if r := say(now.Add(31*time.Second), "!hideranking maybe"); !strings.Contains(r, "Use !hideranking") {
		t.Errorf("bad argument: %q", r)
	}
	for _, h := range []int{1, 2} {
		st.mu.Lock()
		st.players[[2]string{"css", "0:1"}].hide = h
		st.mu.Unlock()
		if r := say(now.Add(time.Minute), "!hideranking 0"); hide() != h || !strings.Contains(r, "only an admin") {
			t.Errorf("hideranking %d changed: %d %q", h, hide(), r)
		}
	}
	if r := say(now.Add(2*time.Minute), "!settings"); !strings.Contains(r, "banned") || !strings.Contains(r, "!hideranking 1") {
		t.Errorf("settings: %q", r)
	}
	// the settings menu of the plugin asks for the state
	n := len(rec.get("192.0.2.1:27015"))
	if err := p.Handle(ctx, `"Alice<2><STEAM_1:0:1><CT>" requested "hideranking" (value "0")`, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if sent := rec.get("192.0.2.1:27015")[n:]; len(sent) != 1 || sent[0] != "gamewe_hidestate 2 2" {
		t.Errorf("hideranking query: %q", sent)
	}
}
