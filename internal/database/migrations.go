// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Migration is one version of the database schema. The tables, columns and
// indexes themselves are defined once, in internal/schema; a migration says
// what an older database needs besides them.
//
// Adding a column, index or table: change internal/schema/mysql/schema.sql,
// run `make schema`, and append a migration with Sync (and a grant decision
// in grants.sql for a new table). Never change a released migration; write
// a new one.
type Migration struct {
	Version int
	Name    string
	Desc    string // one line for the plan and gamewe db status
	// Sync adds every table, column and index of internal/schema the
	// database lacks (never removes or changes one).
	Sync bool
	// Drop are indexes that newer ones replace; each is dropped only when
	// its replacement exists (or is added in the same step).
	Drop []DropIndex
	// Widen are columns whose type grew in internal/schema (e.g. an id from
	// tinyint to smallint); each gets the schema's definition when its type
	// still differs. Only for types every value of the old type fits in.
	Widen []WidenColumn
	// Seed adds the missing reference data (internal/schema seed.sql).
	Seed bool
	// RankingAwards are award codes that rankings (games whose realgame is
	// another game, e.g. css1 of css) get from their template after Seed:
	// the template's award and its ribbons, where the ranking has no award
	// or ribbon of that code yet. Seed itself only reaches the templates.
	RankingAwards []string
	// RetireOptions are hlstats_Options rows that neither the collector nor
	// the website reads any more; each is deleted as given there.
	RetireOptions []RetiredOption
	// BotsPerRanking moves the global bot setting of collector 0.20 and older
	// (gamewe_rank_bots, gamewe_ignore_bots) to every ranking
	// (gamewe_rank_bots_<game>, gamewe_bot_points_<game>) and deletes it.
	BotsPerRanking bool
	// CareerKills fills hlstats_Players.career_kills (and the inactive
	// players') from kills, which the career rank read until then.
	CareerKills bool
}

// RetiredOption is an hlstats_Options key to delete: always when IfValue is
// nil, else only while its value (trimmed) is one of IfValue, so a value
// someone set on purpose stays.
type RetiredOption struct {
	Key     string
	IfValue []string
}

// DropIndex is an index that ReplacedBy makes superfluous.
type DropIndex struct {
	Table, Index, ReplacedBy string
}

// WidenColumn is a column that takes the schema's (wider) type.
type WidenColumn struct {
	Table, Column string
}

// Migrations are all versions, oldest first. Version 1 is the baseline: on
// an empty database it creates the whole schema and the reference data
// (which already contains every later version); an existing gameWE or
// HLstatsX database without a version table is recorded as baseline and
// gets the later versions.
var Migrations = []Migration{
	{
		Version: 1, Name: "baseline",
		Desc: "the gameWE schema: all tables and the reference data",
	},
	{
		Version: 2, Name: "gamewe-0.2.1",
		Desc: "HLstatsX / gameWE up to 0.2.0 -> 0.2.1: missing tables (hlstats_Events_Latency, hlstats_gamewe_jobs), columns " +
			"(rcon_password, realgame, server_id, kill positions) and indexes; killer_time and victim_time replace killerId and victimId; missing reference data",
		Sync: true,
		Drop: []DropIndex{
			{Table: "hlstats_Events_Frags", Index: "killerId", ReplacedBy: "killer_time"},
			{Table: "hlstats_Events_Frags", Index: "victimId", ReplacedBy: "victim_time"},
		},
		Seed: true,
	},
	{
		Version: 3, Name: "awards-indexes",
		Desc: "indexes for the awards page: time_player on hlstats_Events_Latency (timestamp_id, playerId, ping), " +
			"time_action on hlstats_Events_PlayerActions (timestamp_id, actionId, playerId)",
		Sync: true,
	},
	{
		Version: 4, Name: "log-secret",
		Desc: "hlstats_Servers.log_secret and log_secret_changed for features.log_secret (sv_logsecret; not readable by gamewe_web)",
		Sync: true,
	},
	{
		Version: 5, Name: "server-info",
		Desc: "hlstats_Servers.server_version, server_os and server_vac (features.server_info: A2S_INFO and RCON status; readable by gamewe_web)",
		Sync: true,
	},
	{
		Version: 6, Name: "act-bots",
		Desc: "hlstats_Servers.act_bots: the bots among act_players (RCON status or the tracked players; readable by gamewe_web)",
		Sync: true,
	},
	{
		Version: 7, Name: "cs2",
		Desc: "Counter-Strike 2 (Source 2): neck hits in hlstats_Players_Weapons and hlstats_Players_Weapons_History_Months (readable by gamewe_web); " +
			"the cs2 game with its teams, actions, weapons, awards and ranks",
		Sync: true,
		Seed: true,
	},
	{
		Version: 8, Name: "czero",
		Desc: "Counter-Strike: Condition Zero (GoldSrc): the czero game with the teams, actions, weapons, awards and ranks of cstrike",
		Seed: true,
	},
	{
		Version: 9, Name: "more-source-games",
		Desc: "the games Zombie Panic! Source (zps), Fortress Forever (ff), CSPromod (csp), Fistful of Frags (fof) and No More Room in Hell (nmrih) " +
			"with their teams, roles, actions, weapons, awards, ranks and ribbons",
		Seed: true,
	},
	{
		Version: 10, Name: "l4d",
		Desc: "Left 4 Dead (l4d) and Left 4 Dead 2 (l4d2): the roles, actions, weapons, awards and ribbons of HLstatsX:CE",
		Seed: true,
	},
	{
		Version: 11, Name: "hlstatsx-awards",
		Desc: "the HLstatsX:CE awards mvp (css, CE's round_mvp) and teamkills (every team game) " +
			"with their ribbons, also for the rankings of these games",
		Seed:          true,
		RankingAwards: []string{"mvp", "teamkills"},
	},
	{
		Version: 12, Name: "hall-of-fame",
		Desc: "table hlstats_gamewe_hall_of_fame: the best three players of each period of a scheduled ranking reset (readable by gamewe_web)",
		Sync: true,
	},
	{
		Version: 13, Name: "retired-options",
		Desc: "delete hlstats_Options rows nothing reads any more: gamewe_default_game and gamewe_daily_awards, " +
			"contact and siteurl (HLstatsX:CE) when empty, gamewe_geoip_db when empty or the old default path",
		RetireOptions: []RetiredOption{
			{Key: "gamewe_default_game"},
			{Key: "gamewe_daily_awards"},
			{Key: "contact", IfValue: []string{""}},
			{Key: "siteurl", IfValue: []string{""}},
			{Key: "gamewe_geoip_db", IfValue: []string{"", "/var/lib/gamewe/GeoLite2-City.mmdb"}},
		},
	},
	{
		Version: 14, Name: "player-bans",
		Desc: "table hlstats_Players_Bans: bans the collector records (features.sourcebans: SourceBans bans of players kicked as banned), " +
			"with type, reason, length, start, end and whether the ban is still running; readable by gamewe_web",
		Sync: true,
	},
	{
		Version: 15, Name: "bots-per-ranking",
		Desc: "the bot setting per ranking: the global gamewe_rank_bots (and the old gamewe_ignore_bots) becomes gamewe_rank_bots_<game> " +
			"and gamewe_bot_points_<game> of every ranking with the same effect, then the global rows are deleted",
		BotsPerRanking: true,
	},
	{
		Version: 16, Name: "hall-of-fame-edit",
		Desc: "column hlstats_gamewe_hall_of_fame.removed: entries an admin took out of the hall of fame (the places below move up; " +
			"the row stays and can be restored); gamewe_web_admin may update only this column",
		Sync: true,
	},
	{
		Version: 17, Name: "event-indexes",
		Desc: "indexes for the web's latest events: game_time (game_id, timestamp_id) on the twelve event tables of the live box, " +
			"game_activity (game, hideranking, last_event, playerId) on hlstats_PlayersInactive for the list of inactive players",
		Sync: true,
	},
	{
		Version: 18, Name: "player-indexes",
		Desc: "indexes for the web's player list on large rankings: game_skill (game, hideranking, skill, playerId) and " +
			"game_activity (game, hideranking, last_event, playerId) on hlstats_Players, so a page of the ranking, the player map " +
			"and the ranking positions no longer sort or read every player of the ranking",
		Sync: true,
	},
	{
		Version: 19, Name: "innodb",
		Desc: "MariaDB: every table moves from MyISAM to InnoDB (row locks instead of table locks: a long read of the web no longer " +
			"holds back the collector's writes; hlstats_Livestats stays MEMORY); indexes game_time (game_id, timestamp_id) on " +
			"hlstats_Events_Chat, server_time (server_id, timestamp) on hlstats_server_load (replaces server_id) and " +
			"map_time (map, timestamp_id) on hlstats_Events_Frags (replaces map) for the web's chat, load graphs and map pages, " +
			"player_time (playerId, timestamp_id; replaces playerId) and map_action (map, actionId, playerId) on " +
			"hlstats_Events_PlayerActions and player_team (playerId, team; replaces playerId) on hlstats_Events_ChangeTeam for the " +
			"player events, map and teams pages; " +
			"the ids of hlstats_Teams and hlstats_Roles grow from tinyint to smallint (InnoDB does not give the ids of deleted " +
			"rows back: 255 would run out)",
		Sync: true,
		Drop: []DropIndex{
			{Table: "hlstats_server_load", Index: "server_id", ReplacedBy: "server_time"},
			{Table: "hlstats_Events_Frags", Index: "map", ReplacedBy: "map_time"},
			{Table: "hlstats_Events_PlayerActions", Index: "playerId", ReplacedBy: "player_time"},
			{Table: "hlstats_Events_ChangeTeam", Index: "playerId", ReplacedBy: "player_team"},
		},
		Widen: []WidenColumn{{Table: "hlstats_Teams", Column: "teamId"}, {Table: "hlstats_Roles", Column: "roleId"}},
	},
	{
		Version: 20, Name: "career-kills",
		Desc: "column career_kills on hlstats_Players and hlstats_PlayersInactive: the kills of the career rank, which count " +
			"kills of bots by gamewe_bot_kills_<game> while kills follows gamewe_bot_stats_<game>; filled from kills",
		Sync: true, CareerKills: true,
	},
}

// Latest is the newest schema version this collector knows.
func Latest() int { return Migrations[len(Migrations)-1].Version }

// Checksum identifies what a migration does; gamewe db status warns when
// a recorded migration differs from the one in this binary.
func (m Migration) Checksum() string {
	b := fmt.Appendf(nil, "%d|%s|%t|%v|%t", m.Version, m.Name, m.Sync, m.Drop, m.Seed)
	if len(m.RankingAwards) > 0 { // only then: the checksums of older migrations stay as recorded
		b = fmt.Appendf(b, "|%v", m.RankingAwards)
	}
	if len(m.RetireOptions) > 0 {
		b = fmt.Appendf(b, "|%v", m.RetireOptions)
	}
	if m.BotsPerRanking {
		b = fmt.Appendf(b, "|bots-per-ranking")
	}
	if len(m.Widen) > 0 {
		b = fmt.Appendf(b, "|%v", m.Widen)
	}
	if m.CareerKills {
		b = fmt.Appendf(b, "|career-kills")
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// rankingAwardsSQL copies the awards of codes, and their ribbons, from each
// ranking's template (the game whose code is the ranking's realgame) to the
// ranking, where the ranking has no award or ribbon of that code yet. It only
// adds rows. codes are constants of Migrations, never user input.
func rankingAwardsSQL(postgres bool, codes []string) []string {
	quoted := make([]string, len(codes))
	for i, c := range codes {
		quoted[i] = "'" + strings.ReplaceAll(c, "'", "''") + "'"
	}
	in := strings.Join(quoted, ", ")
	ins, conflict := "INSERT IGNORE INTO ", ""
	if postgres {
		ins, conflict = "INSERT INTO ", " ON CONFLICT DO NOTHING"
	}
	return []string{
		ins + `hlstats_Awards (awardType, game, code, name, verb)
			SELECT a.awardType, g.code, a.code, a.name, a.verb
			FROM hlstats_Games g JOIN hlstats_Awards a ON a.game = g.realgame
			WHERE g.realgame <> '' AND g.code <> g.realgame AND a.code IN (` + in + `)
			  AND NOT EXISTS (SELECT 1 FROM hlstats_Awards x WHERE x.game = g.code AND x.code = a.code)` + conflict,
		ins + `hlstats_Definition_Ranks_Ribbons (definition_type, game_code, code, name, img_file, thumb_file, minimum, restriction_type)
			SELECT r.definition_type, g.code, r.code, r.name, r.img_file, r.thumb_file, r.minimum, r.restriction_type
			FROM hlstats_Games g JOIN hlstats_Definition_Ranks_Ribbons r ON r.game_code = g.realgame
			WHERE g.realgame <> '' AND g.code <> g.realgame AND r.definition_type = 'ribbons2' AND r.code IN (` + in + `)
			  AND NOT EXISTS (SELECT 1 FROM hlstats_Definition_Ranks_Ribbons x
			                  WHERE x.game_code = g.code AND x.definition_type = r.definition_type AND x.code = r.code)` + conflict,
	}
}

// retireOptionsSQL deletes the retired hlstats_Options rows. Keys and values
// are constants of Migrations, never user input.
func retireOptionsSQL(opts []RetiredOption) []string {
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		q := `DELETE FROM hlstats_Options WHERE keyname = ` + quote(o.Key)
		if o.IfValue != nil {
			vals := make([]string, len(o.IfValue))
			for i, v := range o.IfValue {
				vals[i] = quote(v)
			}
			q += ` AND TRIM(value) IN (` + strings.Join(vals, ", ") + `)`
		}
		out = append(out, q)
	}
	return out
}

// botsPerRankingSQL writes the bot settings of every ranking from the global
// gamewe_rank_bots of collector 0.20 and older, with the effect it had, and
// deletes the global rows:
//
//   - gamewe_rank_bots_<game> = the global value (0 when gamewe_ignore_bots
//     was 1, 0 when the key was missing: the old default);
//   - gamewe_bot_points_<game> = the same, except Left 4 Dead, where a
//     player's kill of the AI always gave points.
//
// A database without either global row (an HLstatsX database converted by
// this collector) gets no per-ranking rows: the new default, bots ranked
// with points. Rows that exist are kept, so a second run changes nothing.
// Only constants, no user input.
func botsPerRankingSQL(postgres bool) []string {
	ins, conflict := "INSERT IGNORE INTO ", ""
	if postgres {
		ins, conflict = "INSERT INTO ", " ON CONFLICT DO NOTHING"
	}
	truthy := func(key string) string {
		return `EXISTS (SELECT 1 FROM hlstats_Options o WHERE o.keyname = '` + key + `' AND LOWER(TRIM(o.value)) IN ('1', 'true', 'yes', 'on'))`
	}
	ranked := `CASE WHEN ` + truthy("gamewe_ignore_bots") + ` THEN '0' WHEN ` + truthy("gamewe_rank_bots") + ` THEN '1' ELSE '0' END`
	l4d := `(g.realgame IN ('l4d', 'l4d2') OR g.code IN ('l4d', 'l4d2'))`
	write := func(prefix, value string) string {
		return ins + `hlstats_Options (keyname, value)
			SELECT CONCAT('` + prefix + `', g.code), ` + value + `
			FROM hlstats_Games g
			WHERE EXISTS (SELECT 1 FROM hlstats_Options o WHERE o.keyname IN ('gamewe_rank_bots', 'gamewe_ignore_bots'))
			  AND NOT EXISTS (SELECT 1 FROM hlstats_Options x WHERE x.keyname = CONCAT('` + prefix + `', g.code))` + conflict
	}
	return []string{
		write("gamewe_rank_bots_", ranked),
		write("gamewe_bot_points_", `CASE WHEN `+l4d+` THEN '1' ELSE `+ranked+` END`),
		`DELETE FROM hlstats_Options WHERE keyname IN ('gamewe_rank_bots', 'gamewe_ignore_bots')`,
	}
}

// careerKillsSQL fills career_kills from kills where it is still 0, so a
// second run changes nothing. Only constants.
func careerKillsSQL() []string {
	return []string{
		`UPDATE hlstats_Players SET career_kills = kills WHERE career_kills = 0 AND kills > 0`,
		`UPDATE hlstats_PlayersInactive SET career_kills = kills WHERE career_kills = 0 AND kills > 0`,
	}
}
