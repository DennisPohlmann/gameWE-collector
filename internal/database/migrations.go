// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
}

// Latest is the newest schema version this collector knows.
func Latest() int { return Migrations[len(Migrations)-1].Version }

// Checksum identifies what a migration does; gamewe db status warns when
// a recorded migration differs from the one in this binary.
func (m Migration) Checksum() string {
	h := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%t|%v|%t", m.Version, m.Name, m.Sync, m.Drop, m.Seed))
	return hex.EncodeToString(h[:])
}
