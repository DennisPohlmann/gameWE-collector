// SPDX-License-Identifier: GPL-3.0-or-later

// Package settings holds the collector's statistics and command settings.
// They are read from hlstats_Options (keys starting with gamewe_), so they
// can be changed in the web admin; missing or invalid keys use the
// built-in defaults.
package settings

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// Command prefixes of the plugin's server commands (gamewe_psay, ...): the
// gameWE plugin knows both, the original gameME plugin only gameme.
const (
	PrefixGameWE = "gamewe"
	PrefixGameME = "gameme"

	// gamewe_command_display
	DisplayPanel = "panel"
	DisplayChat  = "chat"
	DisplayBoth  = "both"
)

type Settings struct {
	StartSkill      int      // gamewe_start_skill
	KillPoints      int      // gamewe_kill_points
	HeadshotBonus   int      // gamewe_headshot_bonus
	SuicidePenalty  int      // gamewe_suicide_penalty
	TeamkillPenalty int      // gamewe_teamkill_penalty
	StorePlayerIP   bool     // gamewe_store_player_ip
	StoreChat       bool     // gamewe_store_chat
	WinTriggers     []string // gamewe_win_triggers (comma separated)
	Commands        bool     // gamewe_commands
	MenuTime        int      // gamewe_menu_time (seconds)
	RconTimeout     int      // gamewe_rcon_timeout (seconds)
	LogLines        bool     // gamewe_log_lines
	IdleTimeout     int      // gamewe_idle_timeout (seconds, 0 = off)
	CommandPrefix   string   // gamewe_command_prefix (gamewe or gameme)
	CommandDisplay  string   // gamewe_command_display (panel, chat or both)
	RawMessages     bool     // gamewe_raw_messages
	StoreLocations  bool     // gamewe_store_locations
	// Bot settings of each ranking (hlstats_Games.code), see RankBots and
	// BotPoints; a ranking without a key uses true.
	BotsRanked    map[string]bool // gamewe_rank_bots_<game>
	BotsPoints    map[string]bool // gamewe_bot_points_<game>
	BotsKills     map[string]bool // gamewe_bot_kills_<game>
	VACBlocked    map[string]bool // gamewe_block_vac_<game> (BlockVAC; a ranking without a key: off)
	SteamAPIKey   Secret          // gamewe_steam_api_key (web admin, Settings → Steam; never logged)
	GeoIPDB       string          // gamewe_geoip_db (path of a MaxMind-format city database)
	SourceBansURL string          // gamewe_sourcebans_url (public ban list, shared with the web)
	ServerLoad    bool            // gamewe_server_load
	PlayerReset   bool            // gamewe_player_reset
	ResetCooldown int             // gamewe_player_reset_cooldown (hours)
	ConsoleEvents bool            // gamewe_console_events
	GlobalChat    bool            // gamewe_global_chat
	// SiteURL is the web frontend's gamewe_site_url (a web key): the base of
	// the pages the browser commands (!statsme, !status, ...) open.
	SiteURL string
}

// DefaultGeoIPDB is where the collector looks for the GeoIP city database.
const DefaultGeoIPDB = "/var/lib/gamewe/GeoLite2-City.mmdb"

// Defaults are used for every key that is missing in hlstats_Options.
func Defaults() Settings {
	return Settings{
		StartSkill: 1000, KillPoints: 5, HeadshotBonus: 0, SuicidePenalty: 5, TeamkillPenalty: 10,
		WinTriggers: []string{
			"CTs_Win", "Terrorists_Win", "Target_Bombed", "Target_Saved", "Bomb_Defused",
			"All_Hostages_Rescued", "Hostages_Not_Rescued", "VIP_Escaped", "VIP_Assassinated",
			"VIP_Not_Escaped", "Round_Win",
		},
		StorePlayerIP: true, StoreChat: true, Commands: true, MenuTime: 15, RconTimeout: 5, LogLines: true, IdleTimeout: 300,
		CommandPrefix: PrefixGameWE, CommandDisplay: DisplayPanel, StoreLocations: true, GeoIPDB: DefaultGeoIPDB, ServerLoad: true,
		ResetCooldown: 24,
	}
}

// Option keys of a ranking's bot settings. game is a code from hlstats_Games.
func RankBotsKey(game string) string  { return "gamewe_rank_bots_" + game }
func BotPointsKey(game string) string { return "gamewe_bot_points_" + game }
func BotKillsKey(game string) string  { return "gamewe_bot_kills_" + game }

// RankBots reports whether the bots of a ranking are ranked like players
// (hideranking 0, in rankings and awards); otherwise new bots get
// hideranking 1. Default true.
func (s Settings) RankBots(game string) bool { return on(s.BotsRanked, game) }

// BotPoints reports whether kills and player-vs-player actions with a bot
// (either side) change skill in a ranking. Default true.
func (s Settings) BotPoints(game string) bool { return on(s.BotsPoints, game) }

// BotKills reports whether a kill of a bot counts in the killer's kills and
// headshots (kill ranking, K/D, the kill-based ranks) in a ranking; the
// kill itself is always stored. Default true.
func (s Settings) BotKills(game string) bool { return on(s.BotsKills, game) }

// BlockVACKey is the option key of a ranking's VAC block.
func BlockVACKey(game string) string { return "gamewe_block_vac_" + game }

// BlockVAC reports whether a ranking keeps players with a VAC ban on record
// out (kicked at connect, recorded as banned). Default false.
func (s Settings) BlockVAC(game string) bool { return s.VACBlocked[game] }

// VACBlockGames returns the rankings with the VAC block on.
func (s Settings) VACBlockGames() map[string]bool {
	m := map[string]bool{}
	for g, on := range s.VACBlocked {
		if on {
			m[g] = true
		}
	}
	return m
}

// Secret is a value that must never reach a log, an error or the output:
// fmt and slog print it as "[hidden]" (empty: "").
type Secret string

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[hidden]"
}

// GoString hides the value from %#v too.
func (s Secret) GoString() string { return strconv.Quote(s.String()) }

// LogValue hides the value from slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// SteamKey reports whether v looks like a Steam Web API key (32 hex
// characters).
func SteamKey(v string) bool {
	return len(v) == 32 && strings.Trim(strings.ToUpper(v), "0123456789ABCDEF") == ""
}

func on(m map[string]bool, game string) bool {
	v, ok := m[game]
	return v || !ok
}

// Key describes one hlstats_Options key.
type Key struct {
	Name, Default, Meaning string
}

// Keys lists all hlstats_Options keys the collector reads (for docs and
// `gamewe check`).
func Keys() []Key {
	d := Defaults()
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return []Key{
		{"gamewe_start_skill", strconv.Itoa(d.StartSkill), "skill of a new player"},
		{"gamewe_kill_points", strconv.Itoa(d.KillPoints), "base skill per kill (scaled by victim/killer skill and weapon modifier, at least 1)"},
		{"gamewe_headshot_bonus", strconv.Itoa(d.HeadshotBonus), "extra skill per headshot on top of the hlstats_Actions headshot action (default 0: the action rewards it)"},
		{"gamewe_suicide_penalty", strconv.Itoa(d.SuicidePenalty), "skill lost per suicide"},
		{"gamewe_teamkill_penalty", strconv.Itoa(d.TeamkillPenalty), "skill lost per teamkill"},
		{"gamewe_store_player_ip", b(d.StorePlayerIP), "1 = store IPv4 addresses in hlstats_Events_Connects.ipAddress (personal data; only the web admin user can read it)"},
		{"gamewe_store_chat", b(d.StoreChat), "1 = store chat in hlstats_Events_Chat (personal data; only the web admin user can read it)"},
		{"gamewe_win_triggers", strings.Join(d.WinTriggers, ","), "log triggers that end a round with a winner (comma separated)"},
		{"gamewe_commands", b(d.Commands), "1 = answer !rank, !top10, ... in game"},
		{"gamewe_menu_time", strconv.Itoa(d.MenuTime), "seconds the panels (!top10, !rank, ...) stay open"},
		{"gamewe_rcon_timeout", strconv.Itoa(d.RconTimeout), "seconds for RCON connect, send and receive"},
		{"gamewe_log_lines", b(d.LogLines), "1 = write every received game-server log line to the collector's log (journal)"},
		{"gamewe_idle_timeout", strconv.Itoa(d.IdleTimeout), "seconds without any log line of a player after which the visit ends (lost disconnect); 0 = never"},
		{"gamewe_command_display", d.CommandDisplay, "how !rank, !statsme, !session, !status and !next are answered: panel (menu on the left, as gameME), chat, or both"},
		{"gamewe_command_prefix", d.CommandPrefix, "prefix of the plugin's server commands: gamewe, or gameme for servers with the original gameME plugin"},
		{"gamewe_raw_messages", b(d.RawMessages), "1 = answer !rank, !session, !top10 and !next with gamewe_raw_message for a message plugin instead of chat text"},
		{"gamewe_store_locations", b(d.StoreLocations), "1 = store kill and death positions (plugin location lines) in hlstats_Events_Locations"},
		{"gamewe_server_load", b(d.ServerLoad), "1 = write hlstats_server_load every 10 minutes per server"},
		{"gamewe_sourcebans_url", d.SourceBansURL, "address of the public SourceBans ban list (set in the web admin under Settings → Bans); with features.sourcebans the collector looks up players kicked as banned there"},
		{"gamewe_player_reset", b(d.PlayerReset), "1 = players may reset their own stats in game (!gamewe_reset, confirmed with !gamewe_reset 1); never for banned or admin-hidden players"},
		{"gamewe_player_reset_cooldown", strconv.Itoa(d.ResetCooldown), "hours before a player may reset the own stats again (0 = no wait)"},
		{"gamewe_console_events", b(d.ConsoleEvents), "1 = tell the involved players in their console the points of every kill and action (players switch it off with gamewe_display 0)"},
		{"gamewe_global_chat", b(d.GlobalChat), "1 = announce player and team actions with points in the chat (players switch it off with gamewe_chat 0)"},
	}
}

// WebKeys lists the gamewe_* keys in hlstats_Options that belong to the web
// frontend (src/lib/settings.ts, admin-settings.ts, voice.ts,
// privacy-operator.ts of gameWE-web).
// The collector does not read them, `gamewe check` only must not report
// them as typos. Keep it in step with the web; slot and per-user keys are
// matched in otherKey.
var WebKeys = []string{
	"gamewe_page_size", "gamewe_site_url", "gamewe_logo_url", "gamewe_header_logo",
	"gamewe_favicon", "gamewe_apple_icon", "gamewe_title_format", "gamewe_title_ranking", "gamewe_geoip_credit",
	"gamewe_sig_brand", "gamewe_chat_public", "gamewe_game_events", "gamewe_player_events", "gamewe_skin",
	"gamewe_game_skins", "gamewe_map_live", "gamewe_map_players", "gamewe_map_style",
	"gamewe_map_satellite", "gamewe_map_carto_key", "gamewe_map_esri_key",
	"gamewe_seo_description", "gamewe_seo_keywords", "gamewe_seo_image", "gamewe_seo_index", "gamewe_seo_twitter",
	"gamewe_header_home", "gamewe_header_order", "gamewe_metrics_token",
	"gamewe_voice_type", "gamewe_discord_guild", "gamewe_discord_invite",
	"gamewe_ts_host", "gamewe_ts_port", "gamewe_ts_query_port", "gamewe_ts_query_user", "gamewe_ts_query_password",
	"gamewe_ts_password", "gamewe_ts_name", "gamewe_ts_link", "gamewe_ts_display", "gamewe_ts_country",
	"gamewe_ts_show_clients", "gamewe_update_check",
	"gamewe_default_lang", "gamewe_steam_profiles", "gamewe_steam_api_key",
	"gamewe_privacy_name", "gamewe_privacy_address", "gamewe_privacy_email",
}

var webKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range WebKeys {
		m[k] = true
	}
	return m
}()

// gameCode reports whether g can be the hlstats_Games.code in a per-ranking key.
func gameCode(g string) bool {
	return g != "" && strings.Trim(g, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

// otherKey reports gamewe_* keys that are not collector settings but are
// expected in hlstats_Options: web settings, header link slots
// (gamewe_link_<n>), web admin TOTP secrets (gamewe_totp_<hash>, pending
// gamewe_totpp_<hash>), the operator's free text of the privacy page
// (gamewe_privacy_text_<n>), the scheduled ranking resets and hall of fame per
// game (gamewe_reset_schedule_<game>, gamewe_reset_mode_<game>,
// gamewe_reset_last_<game>, gamewe_hof_<game>) and of all rankings
// (gamewe_reset_schedule, gamewe_reset_mode; gamewe_reset_paused: the
// schedule the web admin turned off), the bot settings per game
// (gamewe_rank_bots_<game>, gamewe_bot_points_<game>, gamewe_bot_kills_<game>), the VAC block per game
// (gamewe_block_vac_<game>), the collector's heartbeat, version and job
// plan (gamewe_next_awards, gamewe_next_inactive, gamewe_next_resets,
// gamewe_next_reset_<game>) and retired keys that the migrations delete.
func otherKey(k string) bool {
	switch k {
	case "gamewe_reset_schedule", "gamewe_reset_mode", "gamewe_reset_paused", // the scheduled reset of all rankings
		"gamewe_heartbeat", "gamewe_collector_version", // written by the collector
		"gamewe_next_awards", "gamewe_next_inactive", "gamewe_next_resets", // the job plan, written by the collector
		"gamewe_daily_awards",                    // 1.4.x; now features.daily_awards in the config file
		"gamewe_default_game",                    // old web setting nothing reads; migration 13 deletes both
		"gamewe_geoip_db",                        // legacy, read below; config.yaml geoip.path replaces it
		"gamewe_rank_bots", "gamewe_ignore_bots": // reported above
		return true
	}
	if webKeys[k] {
		return true
	}
	for _, p := range []string{"gamewe_link_", "gamewe_privacy_text_"} {
		if n, ok := strings.CutPrefix(k, p); ok {
			return n != "" && strings.Trim(n, "0123456789") == ""
		}
	}
	for _, p := range []string{"gamewe_next_reset_", "gamewe_reset_schedule_", "gamewe_reset_mode_", "gamewe_reset_last_", "gamewe_hof_", "gamewe_rank_bots_", "gamewe_bot_points_", "gamewe_bot_kills_", "gamewe_block_vac_"} {
		if g, ok := strings.CutPrefix(k, p); ok {
			return gameCode(g)
		}
	}
	for _, p := range []string{"gamewe_totp_", "gamewe_totpp_"} {
		if h, ok := strings.CutPrefix(k, p); ok {
			return len(h) == 32 && strings.Trim(h, "0123456789abcdef") == ""
		}
	}
	return false
}

// FromOptions builds settings from hlstats_Options rows. Invalid values
// keep the default and are reported in the returned warnings.
func FromOptions(opts map[string]string) (Settings, []string) {
	s := Defaults()
	var warn []string
	num := func(key string, dst *int, minVal int) {
		v, ok := opts[key]
		if !ok {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < minVal {
			warn = append(warn, fmt.Sprintf("hlstats_Options %s = %q is invalid (must be a number >= %d), using %d", key, v, minVal, *dst))
			return
		}
		*dst = n
	}
	flag := func(key string, dst *bool) {
		v, ok := opts[key]
		if !ok {
			return
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			*dst = true
		case "0", "false", "no", "off", "":
			*dst = false
		default:
			warn = append(warn, fmt.Sprintf("hlstats_Options %s = %q is invalid (must be 0 or 1)", key, v))
		}
	}
	num("gamewe_start_skill", &s.StartSkill, 0)
	num("gamewe_kill_points", &s.KillPoints, 1)
	num("gamewe_headshot_bonus", &s.HeadshotBonus, 0)
	num("gamewe_suicide_penalty", &s.SuicidePenalty, 0)
	num("gamewe_teamkill_penalty", &s.TeamkillPenalty, 0)
	flag("gamewe_store_player_ip", &s.StorePlayerIP)
	flag("gamewe_store_chat", &s.StoreChat)
	flag("gamewe_commands", &s.Commands)
	flag("gamewe_log_lines", &s.LogLines)
	num("gamewe_menu_time", &s.MenuTime, 1)
	num("gamewe_rcon_timeout", &s.RconTimeout, 1)
	num("gamewe_idle_timeout", &s.IdleTimeout, 0)
	flag("gamewe_raw_messages", &s.RawMessages)
	flag("gamewe_store_locations", &s.StoreLocations)
	// The bot settings and the VAC block of each ranking (keys sorted:
	// stable warnings).
	var keys []string
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, f := range []struct {
			prefix string
			dst    *map[string]bool
			def    bool
		}{{"gamewe_rank_bots_", &s.BotsRanked, true}, {"gamewe_bot_points_", &s.BotsPoints, true}, {"gamewe_bot_kills_", &s.BotsKills, true},
			{"gamewe_block_vac_", &s.VACBlocked, false}} {
			g, ok := strings.CutPrefix(k, f.prefix)
			if !ok || !gameCode(g) {
				continue
			}
			v := f.def
			flag(k, &v)
			if *f.dst == nil {
				*f.dst = map[string]bool{}
			}
			(*f.dst)[g] = v
		}
	}
	// The global keys of collector 0.20 and older; migration 15 copies
	// them to every ranking and deletes them.
	for _, k := range []string{"gamewe_rank_bots", "gamewe_ignore_bots"} {
		if _, ok := opts[k]; ok {
			warn = append(warn, "hlstats_Options "+k+" is obsolete and not read: the bot settings are per ranking now (gamewe_rank_bots_<game>, gamewe_bot_points_<game>; gamewe db migrate moves it); delete the row")
		}
	}
	flag("gamewe_server_load", &s.ServerLoad)
	flag("gamewe_player_reset", &s.PlayerReset)
	num("gamewe_player_reset_cooldown", &s.ResetCooldown, 0)
	flag("gamewe_console_events", &s.ConsoleEvents)
	flag("gamewe_global_chat", &s.GlobalChat)
	s.SiteURL = strings.TrimSpace(opts["gamewe_site_url"])
	if v, ok := opts["gamewe_geoip_db"]; ok {
		s.GeoIPDB = strings.TrimSpace(v)
	}
	if v, ok := opts["gamewe_sourcebans_url"]; ok {
		s.SourceBansURL = strings.TrimSpace(v)
	}
	// The web admin checks the key; an invalid one is not used (the VAC
	// block reports a missing key).
	if v := strings.TrimSpace(opts["gamewe_steam_api_key"]); SteamKey(v) {
		s.SteamAPIKey = Secret(v)
	}
	if v, ok := opts["gamewe_command_prefix"]; ok {
		switch p := strings.ToLower(strings.TrimSpace(v)); p {
		case PrefixGameWE, PrefixGameME:
			s.CommandPrefix = p
		default:
			warn = append(warn, fmt.Sprintf("hlstats_Options gamewe_command_prefix = %q is invalid (gamewe or gameme), using %s", v, s.CommandPrefix))
		}
	}
	if v, ok := opts["gamewe_command_display"]; ok {
		switch d := strings.ToLower(strings.TrimSpace(v)); d {
		case DisplayPanel, DisplayChat, DisplayBoth:
			s.CommandDisplay = d
		default:
			warn = append(warn, fmt.Sprintf("hlstats_Options gamewe_command_display = %q is invalid (panel, chat or both), using %s", v, s.CommandDisplay))
		}
	}
	if v, ok := opts["gamewe_win_triggers"]; ok {
		var t []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				t = append(t, p)
			}
		}
		s.WinTriggers = t
	}
	known := map[string]bool{}
	for _, k := range Keys() {
		known[k.Name] = true
	}
	var unknown []string
	for k := range opts {
		if !known[k] && strings.HasPrefix(k, "gamewe_") && !otherKey(k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		warn = append(warn, fmt.Sprintf("hlstats_Options %s is not a collector setting (typo?)", k))
	}
	return s, warn
}

// Live holds the current settings; it is replaced on every reload and is
// safe for concurrent use.
type Live struct{ p atomic.Pointer[Settings] }

// NewLive starts with s.
func NewLive(s Settings) *Live {
	l := &Live{}
	l.Set(s)
	return l
}

func (l *Live) Get() Settings  { return *l.p.Load() }
func (l *Live) Set(s Settings) { l.p.Store(&s) }
