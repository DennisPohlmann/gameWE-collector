// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"!rank": "rank", "/top10": "top10", "!TOP": "top10", "status": "status",
		"!session please": "session", "rank me": "rank", "hello": "", "": "", "!ranking": "",
		"/statsme": "statsme", "/next": "next", "!help": "help", "/clans": "clans", "/gamewe_reset": "reset",
		"gamewe_reset": "", "!gameme_reset 1": "reset", "!skill": "rank", "points": "rank", "!place": "rank_all",
		"/kdratio": "kpd", "kdeath": "kpd_all", "!sdata": "session", "session_data": "session_all",
		"!top5": "top5", "top20": "top20", "!cmds": "help", "commands": "help", "/cmd": "help",
		"load": "load", "!servers": "servers", "/cheaters": "cheaters", "!weapon ak47": "weapons",
		"/targets": "targets", "!player_kills": "kills", "action": "actions", "!map_stats": "maps",
		// words common in chat only as the whole message or with a prefix
		"kill him": "", "!kill him": "kills", "status of the server": "", "place 2": "",
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
	got := menuText(statsPanel("en", stats.Player{Name: "Eve"}, 1, 1, st, time.Now()))
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

// gameME's commands in gameWE: one test server with Alice (userid 2),
// Bob (3) and a bot (4), answers recorded; display chat so that the rank
// commands answer with chat lines.
func gamemeTestServer(t *testing.T, mod func(*settings.Settings)) (say func(at time.Time, text string) []string, p *Processor, ms *memStore) {
	t.Helper()
	p, ms = newTestProcessor(t)
	rec := &recorder{}
	p.cmds.reply = rec
	s := settings.Defaults()
	s.CommandDisplay = settings.DisplayChat
	if mod != nil {
		mod(&s)
	}
	p.set.Set(s)
	ctx := context.Background()
	for _, l := range []string{
		`"Alice<2><STEAM_1:0:1><CT>" killed "Bob<3><STEAM_1:0:2><TERRORIST>" with "ak47"`,
		`"Bot<4><BOT><TERRORIST>" killed "Bob<3><STEAM_1:0:2><TERRORIST>" with "ak47"`,
	} {
		if err := p.Handle(ctx, l, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	say = func(at time.Time, text string) []string {
		t.Helper()
		n := len(rec.get("192.0.2.1:27015"))
		if err := p.Handle(ctx, `"Alice<2><STEAM_1:0:1><CT>" say "`+text+`"`, at); err != nil {
			t.Fatal(err)
		}
		return rec.get("192.0.2.1:27015")[n:]
	}
	return say, p, ms
}

func playerID(ms *memStore, uid string) int64 {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.players[[2]string{"css", uid}].id
}

// The _all variants answer every player on the server (not the bot), each in
// the own language; the others only the caller.
func TestCommandsToAll(t *testing.T) {
	say, p, _ := gamemeTestServer(t, nil)
	if err := p.Handle(context.Background(), `"Bob<3><STEAM_1:0:2><TERRORIST>" setup "gamewe_lang" (value "de")`, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	got := say(now, "!place")
	want := []string{`gamewe_psay 2 0 "[gameWE] Alice is ranked #1 of 3 with 1005 points."`,
		`gamewe_psay 3 0 "[gameWE] Alice ist auf Platz 1 von 3 mit 1005 Punkten."`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("!place: %q", got)
	}
	if got := say(now.Add(time.Second), "!place"); len(got) != 0 {
		t.Errorf("!place again within AllCooldown: %q", got)
	}
	now = now.Add(AllCooldown)
	if got := say(now, "!kdeath"); len(got) != 4 || !strings.HasPrefix(got[0], "gamewe_psay 2 0 ") || !strings.Contains(got[3], "Kills 1, Tode 0") {
		t.Errorf("!kdeath: %q", got)
	}
	now = now.Add(AllCooldown)
	if got := say(now, "session_data"); len(got) != 2 || !strings.Contains(got[0], "Alice this session: 1 kills") {
		t.Errorf("session_data: %q", got)
	}
	for _, c := range []string{"!skill", "!points", "rank"} {
		if got := say(now, c); len(got) != 1 || got[0] != `gamewe_psay 2 0 "[gameWE] You are ranked #1 of 3 with 1005 points."` {
			t.Errorf("%s: %q", c, got)
		}
	}
	if got := say(now, "!kdratio"); len(got) != 2 || !strings.Contains(got[1], "Kills 1, deaths 0 (K:D 1.00)") {
		t.Errorf("!kdratio: %q", got)
	}
	if got := say(now, "!sdata"); len(got) != 1 || !strings.Contains(got[0], "This session: 1 kills") {
		t.Errorf("!sdata: %q", got)
	}
	for _, n := range []string{"5", "10", "20"} {
		got := say(now, "!top"+n)
		if len(got) != 1 || !strings.HasPrefix(got[0], `gamewe_msay 15 2 0 "gameWE - Top `+n+`\n1. Alice - 1005 points`) {
			t.Errorf("!top%s: %q", n, got)
		}
	}
	for _, c := range []string{"!cmds", "commands", "/cmd"} {
		if got := say(now, c); len(got) != 3 || !strings.Contains(got[1], "!weapons [w]") {
			t.Errorf("%s: %q", c, got)
		}
	}
}

// Top lists stay below the menu limit with long names.
func TestTopTextFits(t *testing.T) {
	var top []stats.Player
	for i := 0; i < 20; i++ {
		top = append(top, stats.Player{Name: strings.Repeat("Ж", 32), Skill: 99999})
	}
	if s := topText("ru", 20, top); len(s) > maxMenu {
		t.Errorf("top 20 is %d bytes", len(s))
	}
}

// The browser commands open gamewe_site_url plus fixed paths and ids or
// codes from the database; hidden players get no profile.
func TestBrowseCommands(t *testing.T) {
	say, p, ms := gamemeTestServer(t, func(s *settings.Settings) { s.SiteURL = " https://stats.example.com/ " })
	ms.mu.Lock()
	ms.weaponCodes = map[string]string{"css:ak47": "ak47", "css:m4a1_silencer": "m4a1_silencer"}
	ms.actionIDs = map[string]int{"css:planted_the_bomb": 7}
	ms.mu.Unlock()
	now := time.Now()
	alice := playerID(ms, "0:1")
	base := `gamewe_browse 2 "https://stats.example.com`
	for cmd, want := range map[string]string{
		"!statsme": fmt.Sprintf(`/css/players/%d"`, alice), "/accuracy": fmt.Sprintf(`/css/players/%d"`, alice),
		"!targets": fmt.Sprintf(`/css/players/%d"`, alice), "!kills": fmt.Sprintf(`/css/players/%d"`, alice),
		"!weapons": fmt.Sprintf(`/css/players/%d"`, alice), "!weapon AK47": `/css/weapons/ak47"`,
		"/status": fmt.Sprintf(`/css/servers/%d"`, p.srv.info.ID), "load": `/"`, "!servers": `/css"`,
		"!clans": `/css/clans"`, "!cheaters": `/css/bans"`, "!maps": `/css/maps"`, "!actions": `/css/actions"`,
		"!action Planted_The_Bomb": `/css/actions/7"`,
	} {
		if got := say(now, cmd); len(got) != 1 || got[0] != base+want {
			t.Errorf("%s: %q, want %s", cmd, got, base+want)
		}
	}
	// Unknown codes are not put into a URL; the typed text only comes back sanitized.
	for cmd, want := range map[string]string{
		"!weapons ../../admin": `gamewe_psay 2 0 "[gameWE] Unknown weapon: ../../admin"`,
		"!actions x';quit":     `gamewe_psay 2 0 "[gameWE] Unknown action: x',quit"`,
	} {
		if got := say(now, cmd); len(got) != 1 || got[0] != want {
			t.Errorf("%s: %q", cmd, got)
		}
	}
	// hidden by the player, by an admin or banned: no profile, the other pages still open
	for _, h := range []int{3, 1, 2} {
		ms.mu.Lock()
		ms.byID[alice].hide = h
		ms.mu.Unlock()
		for _, cmd := range []string{"!statsme", "!kills", "!weapons"} {
			if got := say(now, cmd); len(got) != 1 || !strings.Contains(got[0], "_psay 2 0 ") || !strings.Contains(got[0], "no web page") {
				t.Errorf("hideranking %d %s: %q", h, cmd, got)
			}
		}
		if got := say(now, "!status"); len(got) != 1 || !strings.HasPrefix(got[0], base) {
			t.Errorf("hideranking %d !status: %q", h, got)
		}
	}
	ms.mu.Lock()
	ms.byID[alice].hide = 0
	ms.mu.Unlock()
	// GoldSrc games other than Counter-Strike show the MOTD as text: the address in chat
	p.srv.info.Engine, p.srv.info.Base = games.GoldSrc, "dod"
	if got := say(now, "!clans"); len(got) != 1 || got[0] != `gamewe_amx_psay 2 0 "[gameWE] Web page: https://stats.example.com/css/clans"` {
		t.Errorf("dod !clans: %q", got)
	}
	if got := say(now, "!statsme"); len(got) != 1 || !strings.HasPrefix(got[0], "gamewe_amx_psay 2 0 ") || !strings.Contains(got[0], "This session") {
		t.Errorf("dod !statsme (stats instead of a page): %q", got)
	}
	p.srv.info.Base = "cstrike"
	if got := say(now, "!clans"); len(got) != 1 || got[0] != `gamewe_amx_browse 2 "https://stats.example.com/css/clans"` {
		t.Errorf("cstrike !clans: %q", got)
	}
	p.srv.info.Base = "czero" // Condition Zero: the same plugin and MOTD as Counter-Strike 1.6
	if got := say(now, "!clans"); len(got) != 1 || got[0] != `gamewe_amx_browse 2 "https://stats.example.com/css/clans"` {
		t.Errorf("czero !clans: %q", got)
	}
	p.srv.info.Engine, p.srv.info.Base = games.Source, "css"
	// no or an unusable site address: a chat line, !statsme falls back to the stats
	for _, site := range []string{"", "javascript:alert(1)", `https://x.example/";quit`, "https://x.example/?a=1", "ftp://x.example", "https://user@x.example", "https://x.example/ä"} {
		s := p.set.Get()
		s.SiteURL = site
		p.set.Set(s)
		if got := say(now, "!clans"); len(got) != 1 || !strings.Contains(got[0], "not configured") {
			t.Errorf("site %q !clans: %q", site, got)
		}
		if got := say(now, "!statsme"); len(got) != 1 || !strings.Contains(got[0], "This session") {
			t.Errorf("site %q !statsme: %q", site, got)
		}
	}
}

func TestSiteBase(t *testing.T) {
	for in, want := range map[string]string{
		"https://stats.example.com/": "https://stats.example.com", "http://203.0.113.10:3000/gamewe": "http://203.0.113.10:3000/gamewe",
		"https://x.example/#a": "", "https://x.example/a b": "", "//x.example": "", "https://": "", "https://x.example/{x}": "",
	} {
		if got, _ := siteBase(in); got != want {
			t.Errorf("siteBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// !gamewe_reset only with gamewe_player_reset, after a confirmation, once per
// cooldown and never for banned or admin-hidden players.
func TestPlayerReset(t *testing.T) {
	say, p, ms := gamemeTestServer(t, nil)
	now := time.Now()
	alice := playerID(ms, "0:1")
	skill := func() int {
		ms.mu.Lock()
		defer ms.mu.Unlock()
		return ms.byID[alice].skill
	}
	if got := say(now, "!gamewe_reset 1"); len(got) != 1 || !strings.Contains(got[0], "not allowed") || skill() != 1005 {
		t.Errorf("switched off: %q %d", got, skill())
	}
	if got := say(now, "gamewe_reset 1"); len(got) != 0 {
		t.Errorf("without ! or /: %q", got)
	}
	s := p.set.Get()
	s.PlayerReset = true
	p.set.Set(s)
	// gamewe_reset 1 without a question first only asks
	if got := say(now, "!gamewe_reset 1"); len(got) != 2 || !strings.Contains(got[0], "points to 1000") || !strings.Contains(got[1], "within 30 seconds") || skill() != 1005 {
		t.Errorf("first call: %q %d", got, skill())
	}
	if got := say(now.Add(time.Second), "/gamewe_reset 0"); len(got) != 1 || !strings.Contains(got[0], "not reset") {
		t.Errorf("cancel: %q", got)
	}
	if got := say(now.Add(2*time.Second), "!gamewe_reset 1"); len(got) != 2 || skill() != 1005 {
		t.Errorf("after the cancel: %q", got)
	}
	// too late: asks again
	if got := say(now.Add(40*time.Second), "!gamewe_reset 1"); len(got) != 2 || skill() != 1005 {
		t.Errorf("confirmed too late: %q", got)
	}
	if got := say(now.Add(50*time.Second), "!gameme_reset 1"); len(got) != 1 || !strings.Contains(got[0], "have been reset") || skill() != 1000 {
		t.Errorf("confirmed: %q %d", got, skill())
	}
	if st := p.srv.players[logparse.Player{Name: "Alice", UserID: 2, SteamID: "STEAM_1:0:1"}.Key()]; st.live.Skill != 1000 || st.kills != 0 {
		t.Errorf("player state after the reset: skill %d, kills %d", st.live.Skill, st.kills)
	}
	if got := say(now.Add(2*time.Hour), "!gamewe_reset"); len(got) != 1 || !strings.Contains(got[0], "again in 23 hours") {
		t.Errorf("cooldown: %q", got)
	}
	for _, h := range []int{1, 2} {
		ms.mu.Lock()
		ms.byID[alice].hide = h
		ms.mu.Unlock()
		if got := say(now.Add(48*time.Hour), "!gamewe_reset"); len(got) != 1 || !strings.Contains(got[0], "cannot be reset") {
			t.Errorf("hideranking %d: %q", h, got)
		}
	}
}

// The answers follow the player's gamewe_lang, else cl_language, else English.
func TestCommandLanguage(t *testing.T) {
	say, p, _ := gamemeTestServer(t, nil)
	ctx, now := context.Background(), time.Now()
	setup := func(name, value string) {
		t.Helper()
		if err := p.Handle(ctx, `"Alice<2><STEAM_1:0:1><CT>" setup "`+name+`" (value "`+value+`")`, now); err != nil {
			t.Fatal(err)
		}
	}
	setup("cl_language", "german")
	if got := say(now, "!rank"); len(got) != 1 || !strings.Contains(got[0], "Du bist auf Platz 1 von 3") {
		t.Errorf("cl_language german: %q", got)
	}
	setup("gamewe_lang", "pl")
	setup("cl_language", "russian") // the player's choice wins
	if got := say(now, "!rank"); len(got) != 1 || !strings.Contains(got[0], "Jesteś na miejscu 1 z 3") {
		t.Errorf("gamewe_lang pl: %q", got)
	}
	setup("gamewe_lang", "xx") // unknown: unchanged
	setup("gamewe_lang", "dk")
	if got := say(now, "!hideranking maybe"); len(got) != 2 || !strings.Contains(got[0], "Brug !hideranking") {
		t.Errorf("gamewe_lang dk: %q", got)
	}
	setup("gamewe_lang", "en")
	if got := say(now, "!rank"); len(got) != 1 || !strings.Contains(got[0], "You are ranked") {
		t.Errorf("gamewe_lang en: %q", got)
	}
	for _, l := range Languages {
		if langCode(l) != l {
			t.Errorf("langCode(%q) = %q", l, langCode(l))
		}
	}
}

// Every language has every message with the same verbs as English.
func TestCatalogComplete(t *testing.T) {
	verbs := regexp.MustCompile(`%[+]?[.0-9]*[a-z%]`)
	if len(catalog) != len(Languages) {
		t.Errorf("%d languages in the catalog, %d in Languages", len(catalog), len(Languages))
	}
	for _, l := range Languages {
		m, ok := catalog[l]
		if !ok {
			t.Errorf("no catalog for %s", l)
			continue
		}
		for k, en := range catalog["en"] {
			v, ok := m[k]
			if !ok {
				t.Errorf("%s lacks %s", l, k)
				continue
			}
			if a, b := strings.Join(verbs.FindAllString(en, -1), " "), strings.Join(verbs.FindAllString(v, -1), " "); a != b {
				t.Errorf("%s %s: verbs %q, English %q", l, k, b, a)
			}
			if len(v) > 160 {
				t.Errorf("%s %s is %d bytes (chat lines must stay below the engine limit)", l, k, len(v))
			}
		}
		if len(m) != len(catalog["en"]) {
			t.Errorf("%s has %d messages, English %d", l, len(m), len(catalog["en"]))
		}
	}
}

// gamewe_console_events and gamewe_global_chat: points of kills and actions
// to the players involved, actions with points to everybody; off by default.
func TestConsoleAndChatEvents(t *testing.T) {
	_, p, ms := gamemeTestServer(t, nil)
	rec := p.cmds.reply.(*recorder)
	ms.mu.Lock()
	ms.actionPoints = map[string]int{"Planted_The_Bomb": 3}
	ms.mu.Unlock()
	ctx, now := context.Background(), time.Now()
	lines := []string{
		`"Alice<2><STEAM_1:0:1><CT>" killed "Bob<3><STEAM_1:0:2><TERRORIST>" with "ak47"`,
		`"Bob<3><STEAM_1:0:2><TERRORIST>" triggered "Planted_The_Bomb"`,
	}
	run := func() []string {
		t.Helper()
		n := len(rec.get("192.0.2.1:27015"))
		for _, l := range lines {
			if err := p.Handle(ctx, l, now); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Second)
		}
		return rec.get("192.0.2.1:27015")[n:]
	}
	if got := run(); len(got) != 0 {
		t.Errorf("default off: %q", got)
	}
	s := p.set.Get()
	s.ConsoleEvents, s.GlobalChat = true, true
	p.set.Set(s)
	got := run()
	if len(got) != 4 || !strings.HasPrefix(got[0], `gamewe_console_event 2 "+`) || !strings.Contains(got[0], "you killed Bob with ak47") ||
		!strings.HasPrefix(got[1], `gamewe_console_event 3 "-`) || !strings.Contains(got[1], "Alice killed you with ak47") ||
		got[2] != `gamewe_console_event 3 "+3 points: Planted_The_Bomb."` ||
		got[3] != `gamewe_chat_event 2,3 "[gameWE] Bob got +3 points for Planted_The_Bomb."` {
		t.Errorf("events: %q", got)
	}
}

// CS2 without the gameWE plugin: in-game commands are answered by nobody,
// no RCON command is sent.
func TestSource2NoReplies(t *testing.T) {
	st := newMemStore()
	rec := &recorder{}
	hub := newTestHub(st, rec, []string{"192.0.2.20:27015"}, map[string]string{"192.0.2.20:27015": "cs2"})
	src := netip.MustParseAddrPort("192.0.2.20:27015")
	p := `"Pat<2><[U:1:42]><%s>"`
	for _, l := range []string{
		fmt.Sprintf(p, "") + ` connected, address "198.51.100.7:27005"`,
		fmt.Sprintf(p, "Unassigned") + ` joined team "CT"`,
		// Hostile chat that quotes the plugin's lines does not switch the
		// replies on.
		fmt.Sprintf(p, "CT") + ` say "gameWE Plugin 0.2.0 (CounterStrikeSharp)"`,
		fmt.Sprintf(p, "CT") + ` say "x" triggered "weaponstats" (weapon "ak47") (shots "1")"`,
		`gameWE Plugin 0.2.0 (CounterStrikeSharp) "Pat<2><[U:1:42]><CT>" say "x"`,
		fmt.Sprintf(p, "CT") + ` say "!rank"`,
	} {
		if err := hub.Dispatch(src, pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	if got := rec.get("192.0.2.20:27015"); len(got) != 0 {
		t.Errorf("replies sent to CS2: %v", got)
	}
}

// CS2 with the gameWE CounterStrikeSharp plugin: after its start line (or
// a weaponstats line) the in-game commands are answered with the plugin's
// gamewe_* commands, as on the SourceMod plugin.
func TestSource2PluginReplies(t *testing.T) {
	for name, first := range map[string]string{
		"start line":  `gameWE Plugin 0.2.0 (CounterStrikeSharp)`,
		"weaponstats": `"Pat<2><[U:1:42]><CT>" triggered "weaponstats" (weapon "ak47") (shots "3") (hits "1") (kills "0") (headshots "0") (tks "0") (damage "27") (deaths "0")`,
	} {
		t.Run(name, func(t *testing.T) {
			st := newMemStore()
			rec := &recorder{}
			hub := newTestHub(st, rec, []string{"192.0.2.20:27015"}, map[string]string{"192.0.2.20:27015": "cs2"})
			src := netip.MustParseAddrPort("192.0.2.20:27015")
			p := `"Pat<2><[U:1:42]><%s>"`
			for _, l := range []string{
				fmt.Sprintf(p, "") + ` connected, address "198.51.100.7:27005"`,
				fmt.Sprintf(p, "Unassigned") + ` joined team "CT"`,
				first,
				fmt.Sprintf(p, "CT") + ` say "!rank"`,
			} {
				if err := hub.Dispatch(src, pkt(l), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			hub.Close(context.Background())
			got := rec.get("192.0.2.20:27015")
			found := false
			for _, c := range got {
				if strings.HasPrefix(c, "gamewe_psay 2 ") || strings.HasPrefix(c, "gamewe_msay ") {
					found = true
				}
				if strings.HasPrefix(c, "gamewe_amx") {
					t.Errorf("GoldSrc prefix on CS2: %q", c)
				}
			}
			if !found {
				t.Errorf("no reply to !rank: %q", got)
			}
		})
	}
}
