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
