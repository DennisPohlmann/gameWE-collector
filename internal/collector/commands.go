// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// commandName maps chat text like "!rank", "/top10" or "rank" to a command
// name, or "" if the text is not a command gameWE answers. The plugin logs
// commands from chat and from its menu (with a / prefix) as say lines.
// Other plugin menu commands (/clans, /weapons, /accuracy, /targets, /kills,
// /cheaters, /gamewe_hideranking, /gamewe_reset, /gamewe_auto,
// /gamewe_display, /gamewe_chat) are not supported yet and are ignored.
func commandName(text string) string {
	f := strings.Fields(strings.ToLower(text))
	if len(f) == 0 {
		return ""
	}
	switch w := strings.TrimLeft(f[0], "!/"); w {
	case "rank", "top10", "status", "next", "help":
		return w
	case "session", "statsme":
		return "session"
	case "top":
		return "top10"
	case "hideranking", "gamewe_hideranking", "gameme_hideranking":
		if strings.ContainsAny(f[0][:1], "!/") { // only as a command, not a word in chat
			return "hideranking"
		}
	case "settings", "gamewe_settings":
		if strings.ContainsAny(f[0][:1], "!/") {
			return "settings"
		}
	}
	return ""
}

// HideCooldown is how often a player may change the own ranking display.
const HideCooldown = 10 * time.Second

// hideArg reads the argument of !hideranking: hide (1, on), show (0, off)
// or toggle (none). ok is false for anything else.
func hideArg(text string) (hide *bool, ok bool) {
	f := strings.Fields(strings.ToLower(text))
	if len(f) < 2 {
		return nil, true
	}
	switch f[1] {
	case "1", "on", "yes", "hide":
		t := true
		return &t, true
	case "0", "off", "no", "show":
		v := false
		return &v, true
	}
	return nil, false
}

// chatTag starts every chat line the collector sends with <prefix>_psay.
// The plugins replace it with their gamewe_message_prefix when one is set.
const chatTag = "[gameWE]"

// SelfHide answers !hideranking [1|0] and !settings: players hide
// themselves from the rankings (hideranking 3) or show themselves again
// (0). Hidden by an admin or as a bot (1) or banned (2) is never changed.
// The plugin gets the resulting state with <prefix>_hidestate for its
// settings menu.
func (c Commands) SelfHide(ctx context.Context, srv *serverState, st *playerState, name, text string, now time.Time) error {
	if !c.set.Get().Commands {
		return nil
	}
	h, err := c.selfHide(ctx, srv, st, name, text, now)
	if err != nil {
		return err
	}
	c.hideState(srv, st.userID, h)
	return nil
}

func (c Commands) selfHide(ctx context.Context, srv *serverState, st *playerState, name, text string, now time.Time) (int, error) {
	uid := st.userID
	cur, err := c.store.HideRanking(ctx, st.live.ID)
	if err != nil {
		return 0, err
	}
	state := func(h int) string {
		switch h {
		case 0:
			return "visible in the ranking"
		case 3:
			return "hidden from the ranking (by you)"
		case 2:
			return "hidden from the ranking (banned)"
		}
		return "hidden from the ranking (by an admin)"
	}
	if name == "settings" {
		c.psay(srv, uid, chatTag+" Settings: you are "+state(cur)+". !hideranking 1 hides you, !hideranking 0 shows you again.")
		return cur, nil
	}
	hide, ok := hideArg(text)
	if !ok {
		c.psay(srv, uid, chatTag+" Use !hideranking 1 (hide me), !hideranking 0 (show me) or !hideranking (switch).")
		return cur, nil
	}
	if cur == 1 || cur == 2 {
		c.psay(srv, uid, chatTag+" You are "+state(cur)+"; only an admin can change that.")
		return cur, nil
	}
	want := cur == 0 // toggle
	if hide != nil {
		want = *hide
	}
	target := map[bool]int{true: 3, false: 0}[want]
	if target == cur {
		c.psay(srv, uid, chatTag+" You are already "+state(cur)+".")
		return cur, nil
	}
	if !st.lastHide.IsZero() && now.Sub(st.lastHide) < HideCooldown {
		c.psay(srv, uid, fmt.Sprintf(chatTag+" Please wait %d seconds before changing it again.", int((HideCooldown-now.Sub(st.lastHide)).Seconds())+1))
		return cur, nil
	}
	got, err := c.store.SetHideRanking(ctx, st.live.ID, cur, target)
	if err != nil {
		return 0, err
	}
	st.lastHide = now
	c.psay(srv, uid, chatTag+" You are now "+state(got)+".")
	return got, nil
}

// Commands answers in-game commands through the gameWE SourceMod plugin's
// server commands (<prefix>_psay, <prefix>_msay, ...), sent over RCON.
type Commands struct {
	set   *settings.Live
	store Store
	reply Replier
	log   *slog.Logger
}

// Run answers one command for the player.
func (c Commands) Run(ctx context.Context, srv *serverState, st *playerState, name string, now time.Time) error {
	if !c.set.Get().Commands {
		return nil
	}
	uid := st.userID
	if name == "help" {
		c.psay(srv, uid, chatTag+" Commands: !rank, !next, !top10, !status, !session (or !statsme), !hideranking [1|0], !settings, !help")
		return nil
	}
	if c.set.Get().RawMessages && srv.info.Engine != games.GoldSrc { // the AMX plugin has no raw messages
		// A message plugin shows the answer (its forwards onGameWEStats...).
		switch name {
		case "rank":
			return c.rawPlayer(ctx, srv, st, rawRank, 0, now)
		case "session":
			return c.rawPlayer(ctx, srv, st, rawSession, 0, now)
		case "top10":
			return c.rawTop10(ctx, srv, rawTop10, 0, uid)
		case "next":
			return c.rawNext(ctx, srv, st, rawNext, 0)
		}
	}
	game := srv.info.Game
	pl, err := c.store.PlayerStats(ctx, st.live.ID)
	if errors.Is(err, stats.ErrNotFound) {
		c.psay(srv, uid, chatTag+" No stats for you yet.")
		return nil
	}
	if err != nil {
		return err
	}
	switch name {
	case "rank", "status", "session":
		rank, total, err := c.store.Rank(ctx, game, pl.Skill)
		if err != nil {
			return err
		}
		var chat []string
		switch name {
		case "rank":
			chat = []string{fmt.Sprintf(chatTag+" You are ranked #%d of %d with %d points.", rank, total, pl.Skill)}
		case "session":
			chat = []string{fmt.Sprintf(chatTag+" This session: %d kills, %d deaths (K:D %s), %d headshots, %+d skill, %d min.",
				st.kills, st.deaths, kd(st.kills, st.deaths), st.headshots, st.skillDelta, int(now.Sub(st.since).Minutes()))}
		default:
			hs := 0
			if pl.Kills > 0 {
				hs = 100 * pl.Headshots / pl.Kills
			}
			chat = []string{
				fmt.Sprintf(chatTag+" %s: %d points, rank #%d/%d, %d h played.", pl.Name, pl.Skill, rank, total, pl.ConnectionTime/3600),
				fmt.Sprintf(chatTag+" Kills %d, deaths %d (K:D %s), headshots %d (%d%%), suicides %d, teamkills %d.",
					pl.Kills, pl.Deaths, kd(pl.Kills, pl.Deaths), pl.Headshots, hs, pl.Suicides, pl.TK),
			}
		}
		c.show(srv, uid, statsPanel(pl, rank, total, st, now), chat)
	case "next":
		nx, err := c.store.Next(ctx, game, pl.Skill)
		if errors.Is(err, stats.ErrNotFound) {
			c.psay(srv, uid, chatTag+" You are number one!")
			return nil
		}
		if err != nil {
			return err
		}
		rank, _, err := c.store.Rank(ctx, game, nx.Skill)
		if err != nil {
			return err
		}
		c.show(srv, uid, []string{"->Next player", fmt.Sprintf("   #%d %s", rank, nx.Name), fmt.Sprintf("   %d Points", nx.Skill),
			fmt.Sprintf("   %d Points ahead of you", nx.Skill-pl.Skill)},
			[]string{fmt.Sprintf(chatTag+" Next rank: #%d %s with %d points, %d points ahead of you.", rank, nx.Name, nx.Skill, nx.Skill-pl.Skill)})
	case "top10":
		top, err := c.store.Top(ctx, game, 10)
		if err != nil {
			return err
		}
		lines := []string{"gameWE - Top 10"}
		for i, t := range top {
			lines = append(lines, fmt.Sprintf("%d. %s - %d points", i+1, t.Name, t.Skill))
		}
		c.send(srv, fmt.Sprintf(`%s_msay %d %d 0 "%s"`, c.prefix(srv), c.set.Get().MenuTime, uid, menuText(lines)))
	}
	return nil
}

// statsPanel is the panel of !rank, !statsme, !session and !status, laid out
// as the gameME message plugin showed it: the totals and this visit.
func statsPanel(pl stats.Player, rank, total int, st *playerState, now time.Time) []string {
	posChange := 0
	if st.startRank > 0 {
		posChange = st.startRank - rank
	}
	return []string{
		"->1 - Total",
		fmt.Sprintf("   Position %d of %d", rank, total),
		fmt.Sprintf("   %d Points", pl.Skill),
		fmt.Sprintf("   %d:%d Frags (%s)", pl.Kills, pl.Deaths, kd(pl.Kills, pl.Deaths)),
		fmt.Sprintf("   %d (%.0f%%) Headshots", pl.Headshots, 100*hpk(pl.Headshots, pl.Kills)),
		fmt.Sprintf("   %.0f%% Accuracy", accuracy(pl.Hits, pl.Shots)),
		"   Time " + duration(pl.ConnectionTime),
		"",
		"->2 - Session",
		fmt.Sprintf("   %+d Positions", posChange),
		fmt.Sprintf("   %+d Points", st.skillDelta),
		fmt.Sprintf("   %d:%d Frags (%s)", st.kills, st.deaths, kd(st.kills, st.deaths)),
		fmt.Sprintf("   %d (%.0f%%) Headshots", st.headshots, 100*hpk(st.headshots, st.kills)),
		fmt.Sprintf("   %.0f%% Accuracy", accuracy(st.hits, st.shots)),
		"   " + favWeapon(st),
		"   Time " + duration(int(now.Sub(st.since).Seconds())),
	}
}

// duration formats seconds as gameME did: 2d 03:04:05h.
func duration(sec int) string {
	sec = max(sec, 0)
	return fmt.Sprintf("%dd %02d:%02d:%02dh", sec/86400, sec/3600%24, sec/60%60, sec%60)
}

// show answers with a panel (menu on the left of the screen, <prefix>_msay),
// chat lines (<prefix>_psay) or both, as gamewe_command_display says.
func (c Commands) show(srv *serverState, uid int, panel, chat []string) {
	set := c.set.Get()
	if set.CommandDisplay != settings.DisplayChat {
		c.send(srv, fmt.Sprintf(`%s_msay %d %d 0 "%s"`, c.prefix(srv), set.MenuTime, uid, menuText(panel)))
	}
	if set.CommandDisplay != settings.DisplayPanel {
		for _, l := range chat {
			c.psay(srv, uid, l)
		}
	}
}

func kd(k, d int) string {
	if d == 0 {
		return fmt.Sprintf("%d.00", k)
	}
	return fmt.Sprintf("%.2f", float64(k)/float64(d))
}

func (c Commands) psay(srv *serverState, userID int, msg string) {
	c.send(srv, fmt.Sprintf(`%s_psay %d 0 "%s"`, c.prefix(srv), userID, Sanitize(msg)))
}

// hideState tells the plugin the player's hideranking value (0 visible,
// 1 hidden by an admin or as a bot, 2 banned, 3 hidden by the player), so
// its settings menu offers the one ranking entry that fits.
func (c Commands) hideState(srv *serverState, userID, h int) {
	c.send(srv, fmt.Sprintf(`%s_hidestate %d %d`, c.prefix(srv), userID, h))
}

// prefix is the start of the plugin's reply commands: gamewe (or gameme)
// for the SourceMod plugin, gamewe_amx (gameme_amx) for the AMX Mod X
// plugin of GoldSrc servers.
func (c Commands) prefix(srv *serverState) string {
	if srv.info.Engine == games.GoldSrc {
		return c.set.Get().CommandPrefix + "_amx"
	}
	return c.set.Get().CommandPrefix
}

func (c Commands) send(srv *serverState, cmd string) {
	if srv.info.RconPassword == "" {
		c.log.Debug("no rcon_password in hlstats_Servers, reply dropped", "server", srv.info.Address, "cmd", cmd)
		return
	}
	c.reply.Send(srv.info, cmd)
}

// Sanitize makes text safe inside a double-quoted console argument:
// quotes, semicolons and control characters (newlines!) would otherwise
// let player names inject further console commands.
func Sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r == ';':
			return ','
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.ToValidUTF8(s, "")
}

// menuText joins menu lines with a literal \n, which the plugin turns into
// line breaks.
func menuText(lines []string) string {
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(Sanitize(l), `\`, "/")
	}
	return strings.Join(lines, `\n`)
}
