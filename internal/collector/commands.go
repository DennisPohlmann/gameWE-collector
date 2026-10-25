// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// commandAliases maps the chat words of gameME's command list to the
// command gameWE answers. Variants ending in _all answer to every player on
// the server.
var commandAliases = map[string]string{
	"rank": "rank", "skill": "rank", "points": "rank", "place": "rank_all",
	"kpd": "kpd", "kdratio": "kpd", "kdeath": "kpd_all",
	"session": "session", "sdata": "session", "session_data": "session_all",
	"top": "top10", "top5": "top5", "top10": "top10", "top20": "top20",
	"next": "next",
	"help": "help", "cmd": "help", "cmds": "help", "commands": "help",
	// browser commands: a page of the web frontend (gamewe_site_url)
	"load": "load", "status": "status", "servers": "servers", "clans": "clans",
	"cheaters": "cheaters", "maps": "maps", "map_stats": "maps", "statsme": "statsme",
	"weapons": "weapons", "weapon": "weapons", "accuracy": "accuracy",
	"targets": "targets", "target": "targets", "kills": "kills", "kill": "kills",
	"player_kills": "kills", "actions": "actions", "action": "actions",
	// settings, only with ! or / in front
	"hideranking": "hideranking", "gamewe_hideranking": "hideranking", "gameme_hideranking": "hideranking",
	"settings": "settings", "gamewe_settings": "settings",
	"gamewe_reset": "reset", "gameme_reset": "reset",
}

// prefixOnly are the commands that need a ! or / in front: words that are
// common in chat would otherwise change settings.
var prefixOnly = map[string]bool{"hideranking": true, "settings": true, "reset": true}

// chatWords may be followed by more words without ! or / ("rank me"), as
// gameME's first commands were; every other command without a prefix must
// be the whole message, so "kill him" in chat opens no web page.
var chatWords = map[string]bool{"rank": true, "top": true, "top10": true, "next": true, "help": true, "session": true, "statsme": true}

// parseCommand maps chat text like "!rank", "/top10", "rank" or
// "!weapons ak47" to a command name and its argument (as written, at most
// 64 bytes), or "" if the text is not a command gameWE answers. The plugin
// logs commands from chat and from its menu (with a / prefix) as say lines.
func parseCommand(text string) (name, arg string) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", ""
	}
	prefixed := strings.ContainsAny(f[0][:1], "!/")
	w := strings.ToLower(strings.TrimLeft(f[0], "!/"))
	name = commandAliases[w]
	switch {
	case name == "":
		return "", ""
	case prefixOnly[name] && !prefixed:
		return "", ""
	case !prefixed && len(f) > 1 && !chatWords[w]:
		return "", ""
	}
	if len(f) > 1 {
		arg = clipArg(f[1])
	}
	return name, arg
}

// commandName is the command of chat text (see parseCommand).
func commandName(text string) string {
	name, _ := parseCommand(text)
	return name
}

// clipArg bounds a command argument to 64 bytes of valid UTF-8.
func clipArg(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.ToValidUTF8(s, "")
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

// hideState names a hideranking value in the player's language.
func hideStateName(lang string, h int) string {
	switch h {
	case 0:
		return tr(lang, "state_visible")
	case 3:
		return tr(lang, "state_self")
	case 2:
		return tr(lang, "state_banned")
	}
	return tr(lang, "state_admin")
}

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
	uid, lang := st.userID, st.lang
	cur, err := c.store.HideRanking(ctx, st.live.ID)
	if err != nil {
		return 0, err
	}
	if name == "settings" {
		c.tell(srv, st, "settings", hideStateName(lang, cur))
		return cur, nil
	}
	hide, ok := hideArg(text)
	if !ok {
		c.tell(srv, st, "hide_usage")
		return cur, nil
	}
	if cur == 1 || cur == 2 {
		c.tell(srv, st, "hide_admin", hideStateName(lang, cur))
		return cur, nil
	}
	want := cur == 0 // toggle
	if hide != nil {
		want = *hide
	}
	target := map[bool]int{true: 3, false: 0}[want]
	if target == cur {
		c.tell(srv, st, "hide_already", hideStateName(lang, cur))
		return cur, nil
	}
	if !st.lastHide.IsZero() && now.Sub(st.lastHide) < HideCooldown {
		c.tell(srv, st, "wait", int((HideCooldown-now.Sub(st.lastHide)).Seconds())+1)
		return cur, nil
	}
	got, err := c.store.SetHideRanking(ctx, st.live.ID, cur, target)
	if err != nil {
		return 0, err
	}
	st.lastHide = now
	c.psay(srv, uid, chatTag+" "+tr(lang, "hide_now", hideStateName(lang, got)))
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

// browseCommands open a page of the web frontend.
var browseCommands = map[string]bool{
	"load": true, "status": true, "servers": true, "clans": true, "cheaters": true, "maps": true,
	"statsme": true, "weapons": true, "accuracy": true, "targets": true, "kills": true, "actions": true,
}

// Run answers one command for the player; text is the whole chat line.
func (c Commands) Run(ctx context.Context, srv *serverState, st *playerState, name, text string, now time.Time) error {
	if !c.set.Get().Commands || stats.IsBot(st.live.UniqueID) {
		return nil
	}
	_, arg := parseCommand(text)
	switch {
	case name == "help":
		c.tell(srv, st, "help_cmds", helpCmds)
		c.tell(srv, st, "help_web", helpWeb)
		c.tell(srv, st, "help_settings", helpSettings)
		return nil
	case name == "reset":
		return c.reset(ctx, srv, st, arg, now)
	case browseCommands[name]:
		return c.browse(ctx, srv, st, name, arg, now)
	}
	return c.stats(ctx, srv, st, name, now)
}

// AllCooldown is how often a player may send an answer to every player
// (!place, !kdeath, !session_data); more are ignored, so nobody can flood
// the chat of the whole server.
const AllCooldown = 15 * time.Second

// stats answers the commands with the player's numbers: rank, kpd,
// session, next and the top lists, and their _all variants.
func (c Commands) stats(ctx context.Context, srv *serverState, st *playerState, name string, now time.Time) error {
	uid, lang := st.userID, st.lang
	if strings.HasSuffix(name, "_all") {
		if !st.lastAll.IsZero() && now.Sub(st.lastAll) < AllCooldown && !now.Before(st.lastAll) {
			return nil
		}
		st.lastAll = now
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
	switch name {
	case "top5", "top10", "top20":
		n, _ := strconv.Atoi(strings.TrimPrefix(name, "top"))
		top, err := c.store.Top(ctx, srv.info.Game, n)
		if err != nil {
			return err
		}
		c.send(srv, fmt.Sprintf(`%s_msay %d %d 0 "%s"`, c.prefix(srv), c.set.Get().MenuTime, uid, topText(lang, n, top)))
		return nil
	}
	game := srv.info.Game
	pl, err := c.store.PlayerStats(ctx, st.live.ID)
	if errors.Is(err, stats.ErrNotFound) {
		c.tell(srv, st, "no_stats")
		return nil
	}
	if err != nil {
		return err
	}
	if name == "next" {
		nx, err := c.store.Next(ctx, game, pl.Skill)
		if errors.Is(err, stats.ErrNotFound) {
			c.tell(srv, st, "number_one")
			return nil
		}
		if err != nil {
			return err
		}
		rank, _, err := c.store.Rank(ctx, game, nx.Skill)
		if err != nil {
			return err
		}
		c.show(srv, uid, []string{tr(lang, "p_next"), fmt.Sprintf("   #%d %s", rank, nx.Name), "   " + tr(lang, "p_points", nx.Skill),
			"   " + tr(lang, "p_ahead", nx.Skill-pl.Skill)},
			[]string{chatTag + " " + tr(lang, "next", rank, nx.Name, nx.Skill, nx.Skill-pl.Skill)})
		return nil
	}
	rank, total, err := c.store.Rank(ctx, game, pl.Skill)
	if err != nil {
		return err
	}
	mins := int(now.Sub(st.since).Minutes())
	switch name {
	case "rank":
		c.show(srv, uid, statsPanel(lang, pl, rank, total, st, now), []string{chatTag + " " + tr(lang, "rank", rank, total, pl.Skill)})
	case "session":
		c.show(srv, uid, statsPanel(lang, pl, rank, total, st, now), []string{chatTag + " " + tr(lang, "session",
			st.kills, st.deaths, kd(st.kills, st.deaths), st.headshots, st.skillDelta, mins)})
	case "kpd":
		c.show(srv, uid, statsPanel(lang, pl, rank, total, st, now), kpdLines(lang, pl, rank, total))
	case "rank_all":
		c.psayAll(srv, func(l string) []string {
			return []string{chatTag + " " + tr(l, "rank_all", pl.Name, rank, total, pl.Skill)}
		})
	case "session_all":
		c.psayAll(srv, func(l string) []string {
			return []string{chatTag + " " + tr(l, "session_all", pl.Name, st.kills, st.deaths, kd(st.kills, st.deaths), st.headshots, st.skillDelta, mins)}
		})
	case "kpd_all":
		c.psayAll(srv, func(l string) []string { return kpdLines(l, pl, rank, total) })
	}
	return nil
}

// kpdLines are the chat lines of !kpd: the player's totals.
func kpdLines(lang string, pl stats.Player, rank, total int) []string {
	hs := 0
	if pl.Kills > 0 {
		hs = 100 * pl.Headshots / pl.Kills
	}
	return []string{
		chatTag + " " + tr(lang, "kpd", pl.Name, pl.Skill, rank, total, pl.ConnectionTime/3600),
		chatTag + " " + tr(lang, "kpd_detail", pl.Kills, pl.Deaths, kd(pl.Kills, pl.Deaths), pl.Headshots, hs, pl.Suicides, pl.TK),
	}
}

// maxMenu keeps a panel below the engine's menu limit (show_menu: 512
// bytes) with the command around it.
const maxMenu = 440

// topText is the panel of !top5, !top10 and !top20; names are shortened
// until it fits, then the entries lose the word "points" (long languages).
func topText(lang string, n int, top []stats.Player) string {
	var lines []string
	for _, compact := range []bool{false, true} {
		for nameLen := 32; nameLen >= 4; nameLen -= 4 {
			lines = []string{tr(lang, "top_title", n)}
			for i, t := range top {
				name := t.Name
				if utf8.RuneCountInString(name) > nameLen {
					name = string([]rune(name)[:nameLen])
				}
				if compact {
					lines = append(lines, fmt.Sprintf("%d. %s %d", i+1, name, t.Skill))
				} else {
					lines = append(lines, tr(lang, "top_entry", i+1, name, t.Skill))
				}
			}
			if s := menuText(append([]string(nil), lines...)); len(s) <= maxMenu {
				return s
			}
		}
	}
	return fitMenu(lines)
}

// statsPanel is the panel of !rank, !statsme, !session and !kpd, laid out
// as the gameME message plugin showed it: the totals and this visit.
func statsPanel(lang string, pl stats.Player, rank, total int, st *playerState, now time.Time) []string {
	posChange := 0
	if st.startRank > 0 {
		posChange = st.startRank - rank
	}
	return []string{
		tr(lang, "p_total"),
		"   " + tr(lang, "p_position", rank, total),
		"   " + tr(lang, "p_points", pl.Skill),
		"   " + tr(lang, "p_frags", pl.Kills, pl.Deaths, kd(pl.Kills, pl.Deaths)),
		"   " + tr(lang, "p_hs", pl.Headshots, 100*hpk(pl.Headshots, pl.Kills)),
		"   " + tr(lang, "p_acc", accuracy(pl.Hits, pl.Shots)),
		"   " + tr(lang, "p_time", duration(pl.ConnectionTime)),
		"",
		tr(lang, "p_session"),
		"   " + tr(lang, "p_positions", posChange),
		"   " + tr(lang, "p_points_delta", st.skillDelta),
		"   " + tr(lang, "p_frags", st.kills, st.deaths, kd(st.kills, st.deaths)),
		"   " + tr(lang, "p_hs", st.headshots, 100*hpk(st.headshots, st.kills)),
		"   " + tr(lang, "p_acc", accuracy(st.hits, st.shots)),
		"   " + favWeapon(st),
		"   " + tr(lang, "p_time", duration(int(now.Sub(st.since).Seconds()))),
	}
}

// duration formats seconds as gameME did: 2d 03:04:05h.
func duration(sec int) string {
	sec = max(sec, 0)
	return fmt.Sprintf("%dd %02d:%02d:%02dh", sec/86400, sec/3600%24, sec/60%60, sec%60)
}

// siteBase checks gamewe_site_url and returns it without a trailing slash:
// only http(s) with a host, no user, query or fragment, and none of the
// characters that could leave the quoted console argument or the plugin's
// URL check. ok is false for an empty or unusable address.
func siteBase(raw string) (base string, ok bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" || len(s) > 200 {
		return "", false
	}
	for _, r := range s {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("\"';<>\\^`{|}", r) {
			return "", false
		}
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(s, "#") {
		return "", false
	}
	return s, true
}

// canBrowse reports whether the server's plugin can show a web page: the
// SourceMod plugin (CS:GO prints the address in chat itself) and Counter-
// Strike 1.6, whose MOTD window shows HTML. The other GoldSrc games show
// the MOTD as plain text.
func canBrowse(srv *serverState) bool {
	return srv.info.Engine != games.GoldSrc || srv.info.Base == "cstrike"
}

// browse opens a page of the web frontend for the player
// (<prefix>_browse <userid> "<url>"). The address is gamewe_site_url plus
// fixed paths, the game code, and ids or codes found in the database;
// nothing the player typed goes into it. Without a usable site address the
// player gets a chat line, !statsme the stats panel instead.
func (c Commands) browse(ctx context.Context, srv *serverState, st *playerState, name, arg string, now time.Time) error {
	base, ok := siteBase(c.set.Get().SiteURL)
	if name == "statsme" && (!ok || !canBrowse(srv)) {
		return c.stats(ctx, srv, st, "session", now)
	}
	if !ok {
		if c.set.Get().SiteURL != "" {
			c.log.Debug("gamewe_site_url is not a usable http(s) address, browser commands are answered in chat")
		}
		c.tell(srv, st, "site_missing")
		return nil
	}
	game := "/" + url.PathEscape(srv.info.Game)
	var path string
	profile := false
	switch name {
	case "load":
		path = "/"
	case "servers":
		path = game
	case "status":
		path = fmt.Sprintf("%s/servers/%d", game, srv.info.ID)
	case "clans", "maps":
		path = game + "/" + name
	case "cheaters":
		path = game + "/bans"
	case "actions":
		path = game + "/actions"
		if arg != "" {
			id, err := c.store.ActionID(ctx, srv.info.Game, arg)
			if errors.Is(err, stats.ErrNotFound) {
				c.tell(srv, st, "unknown_action", shortArg(arg))
				return nil
			}
			if err != nil {
				return err
			}
			path = fmt.Sprintf("%s/actions/%d", game, id)
		}
	case "weapons":
		if arg == "" {
			profile = true
			break
		}
		code, err := c.store.WeaponCode(ctx, srv.info.Game, arg)
		if errors.Is(err, stats.ErrNotFound) {
			c.tell(srv, st, "unknown_weapon", shortArg(arg))
			return nil
		}
		if err != nil {
			return err
		}
		path = game + "/weapons/" + url.PathEscape(code)
	default: // statsme, accuracy, targets, kills: the player's profile
		profile = true
	}
	if profile {
		// Hidden and banned players have no profile page; nobody else's
		// profile is ever linked.
		h, err := c.store.HideRanking(ctx, st.live.ID)
		if err != nil {
			return err
		}
		if h != 0 {
			c.tell(srv, st, "profile_hidden")
			return nil
		}
		path = fmt.Sprintf("%s/players/%d", game, st.live.ID)
	}
	u := base + path
	if !canBrowse(srv) {
		c.tell(srv, st, "web_page", u)
		return nil
	}
	c.send(srv, fmt.Sprintf(`%s_browse %d "%s"`, c.prefix(srv), st.userID, Sanitize(u)))
	return nil
}

// shortArg is a typed argument as quoted back in an answer.
func shortArg(s string) string {
	if utf8.RuneCountInString(s) > 32 {
		s = string([]rune(s)[:32])
	}
	return s
}

// ResetConfirmTime is how long !gamewe_reset waits for !gamewe_reset 1.
const ResetConfirmTime = 30 * time.Second

// reset answers !gamewe_reset [0|1]: with gamewe_player_reset on, the first
// call explains what happens, !gamewe_reset 1 within ResetConfirmTime
// resets the player's stats, !gamewe_reset 0 cancels. A player may reset
// once per gamewe_player_reset_cooldown hours (hlstats_Players_Resets);
// players hidden by an admin or banned never.
func (c Commands) reset(ctx context.Context, srv *serverState, st *playerState, arg string, now time.Time) error {
	set := c.set.Get()
	if !set.PlayerReset {
		c.tell(srv, st, "reset_off")
		return nil
	}
	h, err := c.store.HideRanking(ctx, st.live.ID)
	if err != nil {
		return err
	}
	if h == 1 || h == 2 {
		st.resetAsked = time.Time{}
		c.tell(srv, st, "reset_locked", hideStateName(st.lang, h))
		return nil
	}
	if arg == "0" {
		st.resetAsked = time.Time{}
		c.tell(srv, st, "reset_cancel")
		return nil
	}
	last, err := c.store.LastReset(ctx, st.live.ID)
	if err != nil {
		return err
	}
	if wait := time.Unix(last, 0).Add(time.Duration(set.ResetCooldown) * time.Hour).Sub(now); last > 0 && wait > 0 {
		c.tell(srv, st, "reset_cooldown", int((wait+time.Hour-1)/time.Hour))
		return nil
	}
	if arg == "1" && !st.resetAsked.IsZero() && now.Sub(st.resetAsked) <= ResetConfirmTime && !now.Before(st.resetAsked) {
		st.resetAsked = time.Time{}
		ok, err := c.store.ResetPlayer(ctx, st.live.ID, set.StartSkill, now)
		if err != nil {
			return err
		}
		if !ok { // banned in the meantime
			c.tell(srv, st, "reset_locked", hideStateName(st.lang, 2))
			return nil
		}
		st.live.Skill = max(0, set.StartSkill)
		st.kills, st.deaths, st.headshots, st.suicides, st.shots, st.hits = 0, 0, 0, 0, 0, 0
		st.skillDelta, st.bestKill, st.bestDeath, st.startRank = 0, 0, 0, 0
		st.weaponKills = map[string]int{}
		c.log.Info("player reset the own stats", "player_id", st.live.ID)
		c.tell(srv, st, "reset_done")
		return nil
	}
	st.resetAsked = now
	c.tell(srv, st, "reset_ask", max(0, set.StartSkill))
	c.tell(srv, st, "reset_confirm", int(ResetConfirmTime.Seconds()))
	return nil
}

// show answers with a panel (menu on the left of the screen, <prefix>_msay),
// chat lines (<prefix>_psay) or both, as gamewe_command_display says.
func (c Commands) show(srv *serverState, uid int, panel, chat []string) {
	set := c.set.Get()
	if set.CommandDisplay != settings.DisplayChat {
		c.send(srv, fmt.Sprintf(`%s_msay %d %d 0 "%s"`, c.prefix(srv), set.MenuTime, uid, fitMenu(panel)))
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

// tell sends the player one chat line of the catalog in the player's
// language.
func (c Commands) tell(srv *serverState, st *playerState, key string, args ...any) {
	c.psay(srv, st.userID, chatTag+" "+tr(st.lang, key, args...))
}

// humans are the userids of the players (no bots) on the server, by
// language, sorted.
func humans(srv *serverState) map[string][]int {
	out := map[string][]int{}
	for _, o := range srv.players {
		if !stats.IsBot(o.live.UniqueID) && o.userID > 0 {
			out[o.lang] = append(out[o.lang], o.userID)
		}
	}
	for _, ids := range out {
		sort.Ints(ids)
	}
	return out
}

// toAll sends cmd (a format with the userid list and the message) to the
// players of every language, each in their own language.
func (c Commands) toAll(srv *serverState, format string, text func(lang string) []string) {
	groups := humans(srv)
	langs := make([]string, 0, len(groups))
	for l := range groups {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		ids := make([]string, len(groups[l]))
		for i, u := range groups[l] {
			ids[i] = strconv.Itoa(u)
		}
		for _, line := range text(l) {
			c.send(srv, fmt.Sprintf(format, c.prefix(srv), strings.Join(ids, ","), Sanitize(line)))
		}
	}
}

// psayAll sends chat lines to every player on the server (!place, !kdeath,
// !session_data).
func (c Commands) psayAll(srv *serverState, text func(lang string) []string) {
	c.toAll(srv, `%s_psay %s 0 "%s"`, text)
}

// ConsoleEvent tells a player the points of a kill or an action in the
// console (gamewe_console_events; <prefix>_console_event, which the plugin
// shows only to players with gamewe_display 1).
func (c Commands) ConsoleEvent(srv *serverState, st *playerState, points int, key string, args ...any) {
	set := c.set.Get()
	if !set.Commands || !set.ConsoleEvents || points == 0 || st == nil || st.userID <= 0 || stats.IsBot(st.live.UniqueID) {
		return
	}
	c.send(srv, fmt.Sprintf(`%s_console_event %d "%s"`, c.prefix(srv), st.userID,
		Sanitize(tr(st.lang, key, append([]any{points}, args...)...))))
}

// ChatEvent announces an action with points to every player on the server
// (gamewe_global_chat; <prefix>_chat_event, which the plugin shows only to
// players with gamewe_chat 1).
func (c Commands) ChatEvent(srv *serverState, points int, text func(lang string) string) {
	set := c.set.Get()
	if !set.Commands || !set.GlobalChat || points == 0 {
		return
	}
	c.toAll(srv, `%s_chat_event %s "%s"`, func(l string) []string { return []string{chatTag + " " + text(l)} })
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
	if srv.info.Engine == games.Source2 {
		// CS2 has no gameWE plugin to show the reply: nothing to send.
		c.log.Debug("Source 2 server (CS2) has no plugin for replies, reply dropped", "server", srv.info.Address)
		return
	}
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

// fitMenu is menuText of the lines, without the last ones as long as it is
// longer than maxMenu (translated panels can be longer than the English).
func fitMenu(lines []string) string {
	for n := len(lines); ; n-- {
		if s := menuText(append([]string(nil), lines[:n]...)); len(s) <= maxMenu || n <= 1 {
			return s
		}
	}
}

// menuText joins menu lines with a literal \n, which the plugin turns into
// line breaks.
func menuText(lines []string) string {
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(Sanitize(l), `\`, "/")
	}
	return strings.Join(lines, `\n`)
}
