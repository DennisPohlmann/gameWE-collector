// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

func newTestProcessor(t *testing.T) (*Processor, *memStore) {
	t.Helper()
	st := newMemStore()
	srv := st.addServers([]string{"192.0.2.1:27015"}, nil)[0]
	return NewProcessor(st, settings.NewLive(settings.Defaults()), srv, &recorder{}, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

// As HLstatsX: one kill_streak_<n> when the streak ends (death, round end),
// at most kill_streak_12, and nothing for a single kill.
func TestKillStreakRewardedWhenItEnds(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	line := func(l string) {
		t.Helper()
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	k := `"K<2><STEAM_1:0:1><CT>"`
	for i := 0; i < 14; i++ {
		line(k + ` killed "V<3><STEAM_1:0:2><TERRORIST>" with "ak47"`)
	}
	line(`"V<3><STEAM_1:0:2><TERRORIST>" killed ` + k + ` with "glock"`) // ends K's streak of 14
	line(k + ` killed "V<3><STEAM_1:0:2><TERRORIST>" with "ak47"`)
	line(k + ` killed "V<3><STEAM_1:0:2><TERRORIST>" with "ak47"`)
	line(`World triggered "Round_End"`) // ends K's streak of 2
	line(k + ` killed "V<3><STEAM_1:0:2><TERRORIST>" with "ak47"`)
	line(`World triggered "Round_End"`) // a single kill is no streak
	actions, _ := st.recorded()
	var streaks []string
	for _, a := range actions {
		if strings.Contains(a, "kill_streak") {
			streaks = append(streaks, a)
		}
	}
	if got := strings.Join(streaks, ","); got != "0:1 kill_streak_12,0:1 kill_streak_2" {
		t.Errorf("kill streak actions: %s", got)
	}
}

// consoleArgs splits a console command like the engine: quoted arguments
// (also empty ones) are one argument.
func consoleArgs(cmd string) []string {
	var out []string
	for i := 0; i < len(cmd); {
		switch {
		case cmd[i] == ' ':
			i++
		case cmd[i] == '"':
			j := strings.IndexByte(cmd[i+1:], '"')
			if j < 0 {
				j = len(cmd) - i - 1
			}
			out = append(out, cmd[i+1:i+1+j])
			i += j + 2
		default:
			j := strings.IndexByte(cmd[i:], ' ')
			if j < 0 {
				j = len(cmd) - i
			}
			out = append(out, cmd[i:i+j])
			i += j
		}
	}
	return out
}

// The gameME / gameWE plugin logs `triggered "headshot"` for every
// headshot kill (CS:S: before the kill line). One headshot action per kill,
// in either order.
func TestHeadshotOncePerKill(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"K<2><STEAM_1:0:1><CT>" triggered "headshot"`,
		`"K<2><STEAM_1:0:1><CT>" killed "V<3><STEAM_1:0:2><TERRORIST>" with "deagle" (headshot)`,
		`"K<2><STEAM_1:0:1><CT>" killed "V<3><STEAM_1:0:2><TERRORIST>" with "deagle" (headshot)`,
		`"K<2><STEAM_1:0:1><CT>" triggered "headshot" (position "1 2 3")`,
		`"K<2><STEAM_1:0:1><CT>" killed "V<3><STEAM_1:0:2><TERRORIST>" with "deagle"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	actions, _ := st.recorded()
	n := 0
	for _, a := range actions {
		if a == "0:1 headshot" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d headshot actions for 2 headshot kills: %v", n, actions)
	}
}

func TestRawMessageAnswers(t *testing.T) {
	p, _ := newTestProcessor(t)
	rec := &recorder{}
	p.cmds.reply = rec
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"Alice<2><STEAM_1:0:1><CT>" joined team "CT"`,
		`"Bob<3><STEAM_1:0:2><TERRORIST>" joined team "TERRORIST"`,
		`"Alice<2><STEAM_1:0:1><CT>" killed "Bob<3><STEAM_1:0:2><TERRORIST>" with "ak47"`,
		`"Alice<2><STEAM_1:0:1><CT>" requested "playerinfo" (value "17")`,
		`"Server" requested "top10" (value "18")`,
		`"Bob<3><STEAM_1:0:2><TERRORIST>" requested "next" (value "19")`,
		`"Alice<2><STEAM_1:0:1><CT>" requested "spectatorinfo" (value "20")`,
		`"Server" requested "playerinfo" (value "21")`, // no player: no answer
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	sent := rec.get("192.0.2.1:27015")
	if len(sent) != 4 {
		t.Fatalf("%d answers: %q", len(sent), sent)
	}
	info := consoleArgs(sent[0])
	// command + 48 arguments, as the plugin requires (argument_count >= 48)
	if len(info) != 49 || info[0] != "gamewe_raw_message" || info[1] != "101" || info[2] != "17" || info[3] != "2" {
		t.Fatalf("playerinfo: %d args: %q", len(info)-1, info)
	}
	if info[4] != "1" || info[7] != "1" || info[24] != "1" || info[39] != "ak47" || info[48] != "" {
		t.Errorf("playerinfo values (rank, kills, session kills, fav weapon, country): %q", info)
	}
	top := consoleArgs(sent[1])
	if top[1] != "102" || top[2] != "18" || top[3] != "-1" || (len(top)-4)%4 != 0 || top[5] != "Alice" {
		t.Errorf("top10: %q", top)
	}
	next := consoleArgs(sent[2])
	if next[1] != "103" || next[2] != "19" || next[3] != "3" || len(next) != 9 || next[4] != "1" || next[6] != "Alice" {
		t.Errorf("next: %q", next)
	}
	spec := consoleArgs(sent[3])
	if spec[1] != "1001" || spec[2] != "20" || spec[3] != "2,3" || spec[4] != "2" || !strings.HasPrefix(spec[5], `Alice\nRank 1 of 2`) {
		t.Errorf("spectatorinfo: %q", spec)
	}
}

func TestCommandPrefixAndRawMessages(t *testing.T) {
	p, _ := newTestProcessor(t)
	rec := &recorder{}
	p.cmds.reply = rec
	s := settings.Defaults()
	s.CommandPrefix, s.RawMessages = settings.PrefixGameME, true
	p.set.Set(s)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"Alice<2><STEAM_1:0:1><CT>" say "!rank"`,
		`"Alice<2><STEAM_1:0:1><CT>" say "!top10"`,
		`"Alice<2><STEAM_1:0:1><CT>" say "!help"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	sent := rec.get("192.0.2.1:27015")
	// !help is three chat lines
	if len(sent) != 5 || !strings.HasPrefix(sent[0], "gameme_raw_message 1 0 2 ") ||
		!strings.HasPrefix(sent[1], "gameme_raw_message 5 0 2 ") || !strings.HasPrefix(sent[2], "gameme_psay 2 ") {
		t.Errorf("sent: %q", sent)
	}
}

func TestLocations(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"K<2><STEAM_1:0:1><CT>" located on "kill" (position "-1200 345 64") against "V<3><STEAM_1:0:2><TERRORIST>" (victim_position "-1100 300 0")`,
		`"V<3><STEAM_1:0:2><TERRORIST>" located on "suicide" (position "5 6 7")`,
		`"V<3><STEAM_1:0:2><TERRORIST>" located on "Got_The_Bomb" (position "5 6 7")`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if got := strings.Join(st.locations, " "); got != "2:1:-1200,345 3:2:-1100,300 3:2:5,6" {
		t.Errorf("locations: %s", got)
	}
}

func TestParseStats(t *testing.T) {
	for out, want := range map[string]ServerStats{
		"CPU    In (KB/s)  Out (KB/s)  Uptime  Map changes  FPS      Players  Connects\n12.40   1.00       2.00        1439    3            66.67    5        20\n": {CPU: 12, Uptime: 1439, FPS: 67},
		"CPU   In    Out   Uptime  Users   FPS    Players\n 10.00  0.00  0.00      54     1  249.81       0\n":                                                      {CPU: 10, Uptime: 54, FPS: 250},
	} {
		got, err := ParseStats(out)
		if err != nil || got != want {
			t.Errorf("ParseStats = %+v, %v; want %+v", got, err, want)
		}
	}
	if _, err := ParseStats("Unknown command \"stats\"\n"); err == nil {
		t.Error("no error for unexpected output")
	}
}

// Bots are tracked, but with gamewe_bot_points_<game> = 0 kills with them
// change nobody's skill and give no headshot action and no kill streak.
func TestBotsDoNotChangeSkill(t *testing.T) {
	p, st := newTestProcessor(t)
	s := settings.Defaults()
	s.BotsPoints = map[string]bool{"css": false, "tf": true}
	s.BotsStats = map[string]bool{"css": true}
	p.set.Set(s)
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"Bot Bob<9><BOT><TERRORIST>" joined team "TERRORIST"`,
		`"K<2><STEAM_1:0:1><CT>" triggered "headshot"`,
		`"K<2><STEAM_1:0:1><CT>" killed "Bot Bob<9><BOT><TERRORIST>" with "ak47" (headshot)`,
		`"K<2><STEAM_1:0:1><CT>" killed "Bot Bob<9><BOT><TERRORIST>" with "ak47"`,
		`"K<2><STEAM_1:0:1><CT>" killed "Bot Bob<9><BOT><TERRORIST>" with "ak47"`,
		`"Bot Bob<9><BOT><TERRORIST>" killed "K<2><STEAM_1:0:1><CT>" with "glock"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	k, b := st.players[[2]string{"css", "0:1"}], st.players[[2]string{"css", "BOT:Bot Bob"}]
	actions := append([]string(nil), st.actions...)
	st.mu.Unlock()
	if b == nil || k == nil {
		t.Fatalf("bot not tracked: %v", st.players)
	}
	if k.skill != 1000 || b.skill != 1000 || k.kills != 3 || b.kills != 1 {
		t.Errorf("human skill %d kills %d, bot skill %d kills %d; want 1000/3, 1000/1", k.skill, k.kills, b.skill, b.kills)
	}
	if len(actions) != 0 {
		t.Errorf("actions for kills with a bot: %v", actions)
	}

	// With gamewe_bot_points_css = 1 (or without the key) the same kill counts.
	s.BotsPoints = nil
	p.set.Set(s)
	if err := p.Handle(ctx, `"K<2><STEAM_1:0:1><CT>" killed "Bot Bob<9><BOT><TERRORIST>" with "ak47" (headshot)`, now); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if k.skill == 1000 || len(st.actions) != 1 {
		t.Errorf("bot points: skill %d, actions %v", k.skill, st.actions)
	}
}

// Without gamewe_bot_stats_<game> (the default) a kill of a bot is stored
// but not counted in the killer's kills, and a death by a bot not in the
// victim's deaths; kills of players still count, and so do the points and,
// with gamewe_bot_kills_<game> (the default), the career rank. With
// gamewe_bot_stats_<game> = 1 kills with bots count like any other;
// gamewe_bot_kills_<game> = 0 leaves them out of the career rank.
func TestBotStats(t *testing.T) {
	lines := []string{
		`"K<2><STEAM_1:0:1><CT>" killed "Bot Bob<9><BOT><TERRORIST>" with "ak47" (headshot)`,
		`"K<2><STEAM_1:0:1><CT>" killed "V<3><STEAM_1:0:2><TERRORIST>" with "ak47"`,
		`"Bot Bob<9><BOT><TERRORIST>" killed "K<2><STEAM_1:0:1><CT>" with "glock"`,
	}
	for _, c := range []struct {
		name                       string
		stats, career              map[string]bool
		kills, deaths, careerKills int
	}{
		{"defaults", nil, nil, 1, 0, 2},
		{"stats on", map[string]bool{"css": true}, nil, 2, 1, 2},
		{"career off", nil, map[string]bool{"css": false}, 1, 0, 1},
		{"stats on, career off", map[string]bool{"css": true}, map[string]bool{"css": false}, 2, 1, 1},
	} {
		p, st := newTestProcessor(t)
		s := settings.Defaults()
		s.BotsStats, s.BotsKills = c.stats, c.career
		p.set.Set(s)
		ctx, now := context.Background(), time.Now()
		for _, l := range lines {
			if err := p.Handle(ctx, l, now); err != nil {
				t.Fatal(err)
			}
		}
		st.mu.Lock()
		k, b := st.players[[2]string{"css", "0:1"}], st.players[[2]string{"css", "BOT:Bot Bob"}]
		if k.kills != c.kills || k.deaths != c.deaths || k.careerKills != c.careerKills || b.kills != 1 || b.deaths != 1 || len(st.kills) != 3 {
			t.Errorf("%s: K kills %d deaths %d career %d, bot kills %d deaths %d, %d kills stored; want %d %d %d 1 1 3", c.name,
				k.kills, k.deaths, k.careerKills, b.kills, b.deaths, len(st.kills), c.kills, c.deaths, c.careerKills)
		}
		if k.skill == 1000 {
			t.Errorf("%s: bot points are on: the kill of the bot must still give points", c.name)
		}
		st.mu.Unlock()
	}
}

// The bot settings are per ranking; the global keys of collector 0.19 are
// no longer read (migration 15 moves them) and are reported.
func TestBotSettingsPerRanking(t *testing.T) {
	s, warn := settings.FromOptions(map[string]string{
		"gamewe_rank_bots": "0", "gamewe_ignore_bots": "1",
		"gamewe_rank_bots_css": "0", "gamewe_bot_points_css": "1",
		"gamewe_rank_bots_tf": "1", "gamewe_bot_points_tf": "0",
		"gamewe_bot_kills_css": "0",
	})
	if len(warn) != 2 {
		t.Errorf("warnings %q, want two (the global keys)", warn)
	}
	for _, c := range []struct {
		game          string
		ranked, point bool
	}{{"css", false, true}, {"tf", true, false}, {"cstrike", true, true}} {
		if !s.BotKills("tf") || !s.BotKills("cstrike") {
			t.Error("bot kills default")
		}
		if s.RankBots(c.game) != c.ranked || s.BotPoints(c.game) != c.point {
			t.Errorf("%s: ranked %v points %v, want %v %v", c.game, s.RankBots(c.game), s.BotPoints(c.game), c.ranked, c.point)
		}
	}
	if s.BotKills("css") {
		t.Error("gamewe_bot_kills_css = 0 not read")
	}
	if s.BotStats("css") || s.BotStats("tf") {
		t.Error("bot stats default: want off")
	}
	if s, _ := settings.FromOptions(map[string]string{"gamewe_bot_stats_css": "1"}); !s.BotStats("css") || s.BotStats("tf") {
		t.Error("gamewe_bot_stats_css = 1 not read")
	}
	if _, warn := settings.FromOptions(map[string]string{"gamewe_rank_bots_css": "maybe"}); len(warn) != 1 {
		t.Errorf("invalid value: warnings %q", warn)
	}
}

type fakeGeo struct{}

func (fakeGeo) Lookup(_ string, ip netip.Addr) (stats.Location, bool) {
	if b := ip.As4(); ip.Is4() && b[0] == 203 && b[1] == 0 && b[2] == 113 { // 203.0.113.0/24 is in Germany
		return stats.Location{Flag: "de", Country: "Germany"}, true
	}
	return stats.Location{}, false
}

func TestGeoIP(t *testing.T) {
	st := newMemStore()
	srv := st.addServers([]string{"203.0.113.136:27015"}, nil)[0]
	p := NewProcessor(st, settings.NewLive(settings.Defaults()), srv, &recorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.Geo = fakeGeo{}
	ctx, now := context.Background(), time.Now()
	for _, l := range []string{
		`"A<2><STEAM_1:0:1><>" connected, address "203.0.113.2:27005"`,
		`"B<3><STEAM_1:0:2><>" connected, address "192.168.1.2:27005"`,
	} {
		if err := p.Handle(ctx, l, now); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if got := strings.Join(st.geo, " "); got != fmt.Sprintf("server%d:de 2:de", srv.ID) && got != fmt.Sprintf("server%d:de 3:de", srv.ID) {
		t.Errorf("locations: %s", got)
	}
}

// hlstats_server_load: one row per LoadInterval while the server logs, with
// the lowest and highest player count of the interval.
func TestServerLoad(t *testing.T) {
	p, st := newTestProcessor(t)
	s := settings.Defaults()
	s.IdleTimeout = 0
	p.set.Set(s)
	calls := 0
	p.ServerStats = func(stats.Server, time.Duration) (ServerStats, error) {
		calls++
		return ServerStats{CPU: 7, FPS: 66, Uptime: 600}, nil
	}
	ctx := context.Background()
	t0 := time.Now()
	line := func(at time.Time, l string) {
		t.Helper()
		if err := p.Handle(ctx, l, at); err != nil {
			t.Fatal(err)
		}
	}
	line(t0, `"A<2><STEAM_1:0:1><CT>" joined team "CT"`)
	line(t0.Add(time.Minute), `"B<3><STEAM_1:0:2><CT>" joined team "CT"`)
	line(t0.Add(2*time.Minute), `"C<4><STEAM_1:0:3><CT>" joined team "CT"`)
	line(t0.Add(3*time.Minute), `"C<4><STEAM_1:0:3><CT>" disconnected (reason "x")`)
	if err := p.Tick(ctx, t0.Add(5*time.Minute)); err != nil { // too early
		t.Fatal(err)
	}
	if err := p.Tick(ctx, t0.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// No line for more than an interval: no row (server down).
	if err := p.Tick(ctx, t0.Add(25*time.Minute)); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.loads) != 1 || calls != 1 {
		t.Fatalf("%d rows, %d rcon calls: %+v", len(st.loads), calls, st.loads)
	}
	l := st.loads[0]
	if l.Players != 2 || l.MinPlayers != 0 || l.MaxPlayers != 3 || l.CPU != 7 || l.FPS != 66 || l.Uptime != 600 || l.MapID != 1 {
		t.Errorf("load row %+v", l)
	}
}

// An empty server without RCON logs nothing (hibernation), but answers
// A2S_INFO: the answers keep the hlstats_server_load rows coming, so the web
// shows it online; without answers the rows stop.
func TestServerLoadA2S(t *testing.T) {
	p, st := newTestProcessor(t)
	s := settings.Defaults()
	s.IdleTimeout = 0
	p.set.Set(s)
	ctx := context.Background()
	t0 := time.Now()
	info := func(at time.Time) {
		t.Helper()
		if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1"}, at); err != nil {
			t.Fatal(err)
		}
	}
	tick := func(at time.Time) {
		t.Helper()
		if err := p.Tick(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	info(t0)
	info(t0.Add(5 * time.Minute))
	tick(t0.Add(11 * time.Minute))
	info(t0.Add(15 * time.Minute))
	info(t0.Add(20 * time.Minute))
	tick(t0.Add(22 * time.Minute))
	// No answer for more than an interval: no row (server down).
	tick(t0.Add(40 * time.Minute))
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.loads) != 2 {
		t.Fatalf("%d rows: %+v", len(st.loads), st.loads)
	}
	if l := st.loads[1]; l.Players != 0 || l.Uptime != 22 {
		t.Errorf("load row %+v", l)
	}
}

func TestSchedule(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		s            Schedule
		now          time.Time
		lastRun, day string
		next         string
	}{
		// UTC midnight: right after it, yesterday is decided.
		{Schedule{time.UTC, 0, 0}, time.Date(2026, 10, 22, 0, 0, 30, 0, time.UTC), "2026-10-22 00:00 UTC", "2026-10-21", "2026-10-23 00:00 UTC"},
		{Schedule{time.UTC, 0, 0}, time.Date(2026, 10, 22, 23, 59, 0, 0, time.UTC), "2026-10-22 00:00 UTC", "2026-10-21", "2026-10-23 00:00 UTC"},
		// 03:30 Berlin, before the run: the run of yesterday.
		{Schedule{berlin, 3, 30}, time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), "2026-10-21 03:30 CEST", "2026-10-20", "2026-10-22 03:30 CEST"},
		// Across the end of summer time (2026-10-25, 03:00 CEST -> 02:00 CET).
		{Schedule{berlin, 0, 0}, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), "2026-10-25 00:00 CEST", "2026-10-24", "2026-10-26 00:00 CET"},
		// Start of summer time (2027-03-28): 02:30 does not exist that day.
		{Schedule{berlin, 2, 30}, time.Date(2027, 3, 28, 6, 0, 0, 0, time.UTC), "2027-03-28 03:30 CEST", "2027-03-27", "2027-03-29 02:30 CEST"},
	}
	for _, c := range cases {
		run, day := c.s.Last(c.now)
		next := c.s.Next(c.now)
		got := [3]string{run.Format("2006-01-02 15:04 MST"), day.Format("2006-01-02"), next.Format("2006-01-02 15:04 MST")}
		if got != [3]string{c.lastRun, c.day, c.next} || run.After(c.now) || !next.After(c.now) {
			t.Errorf("%v at %v: last %s day %s next %s; want %s %s %s", c.s.Loc, c.now, got[0], got[1], got[2], c.lastRun, c.day, c.next)
		}
	}
}
