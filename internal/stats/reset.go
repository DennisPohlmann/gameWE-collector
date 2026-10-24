// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Reset modes of a ranking (hlstats_Games row).
//
// Player statistics: the event tables, the per-player tables and histories,
// the award winners and the ranking totals summed from player events
// (hlstats_Weapons, _Actions, _Teams, _Roles, _Maps); the players keep
// their names and ids with the start skill and zero counters.
// Server statistics: the counters in hlstats_Servers, hlstats_server_load
// and hlstats_Trend. The live view (hlstats_Livestats) is cleared in every
// mode: the collector forgets the players of the paused servers.
const (
	// ResetPlayerStats resets the player statistics and keeps the players
	// and the server statistics.
	ResetPlayerStats = "player-stats"
	// ResetAllStats resets the player and the server statistics and keeps
	// the players.
	ResetAllStats = "all-stats"
	// ResetAll resets all statistics and deletes the players of the game.
	ResetAll = "all"
	// ResetStatsOld is the name of ResetPlayerStats before collector 0.2:
	// still accepted from the CLI and from queued jobs.
	ResetStatsOld = "stats"
)

// ResetModes are the modes for messages, in the order of the usage text.
const ResetModes = ResetPlayerStats + ", " + ResetAllStats + " or " + ResetAll

// ResetMode returns the mode name (ResetStatsOld as ResetPlayerStats), or
// an error for an unknown one.
func ResetMode(mode string) (string, error) {
	switch mode {
	case ResetPlayerStats, ResetAllStats, ResetAll:
		return mode, nil
	case ResetStatsOld:
		return ResetPlayerStats, nil
	}
	return "", fmt.Errorf("mode %q: must be %s", mode, ResetModes)
}

// ErrUnknownGame: the game code is not in hlstats_Games.
var ErrUnknownGame = errors.New("unknown game (not in hlstats_Games)")

// resetStep is one table of a reset: rows of table matching where are
// deleted (set == "") or updated with set. All names are constants of this
// file; the game is only ever a placeholder argument.
type resetStep struct {
	table, set, where string
	args              func(game string, gid int, skill int) []any
}

// TableCount is a table of a reset and its rows (to delete or update, or
// deleted or updated).
type TableCount struct {
	Table string
	Rows  int64
}

// Event tables with a game_id column.
var resetEvents = []string{
	"Frags", "Teamkills", "Suicides", "PlayerActions", "PlayerPlayerActions", "TeamBonuses",
	"Connects", "Disconnects", "Entries", "ChangeName", "ChangeTeam", "ChangeRole", "Chat",
	"Admin", "Rcon", "Latency",
}

// Per-player tables with weekly rows and monthly histories (game_id).
var resetPlayerTables = []string{"Weapons", "Actions", "Maps", "Roles", "Teams"}

var (
	resetPlayerCounters = []string{
		"kills", "deaths", "suicides", "headshots", "team_kills", "team_killed", "shots", "hits",
		"kill_streak", "death_streak", "kill_assists", "kill_assists_medic", "kills_assisted",
		"hpk", "kpd", "sort_kpd", "death_headshots", "death_hpk", "connection_time", "rounds",
		"survived_count", "survived", "wins", "losses", "custom_wins", "points_healed",
		"flags_captured", "last_skill_change",
	}
	resetNameCounters = []string{"connection_time", "kills", "deaths", "suicides", "headshots", "shots", "hits",
		"rounds", "survived_count", "survived", "wins", "losses"}
	resetTeamCounters = []string{"time_played", "kills", "deaths", "headshots", "kill_assists", "flags_captured",
		"rounds", "survived", "wins", "losses", "custom_wins"}
	resetServerCounters = []string{
		"kills", "players", "rounds", "suicides", "headshots", "bombs_planted", "bombs_defused",
		"ct_wins", "ts_wins", "act_players", "map_rounds", "map_ct_wins", "map_ts_wins", "map_changes",
		"ct_shots", "ct_hits", "ts_shots", "ts_hits", "map_ct_shots", "map_ct_hits", "map_ts_shots", "map_ts_hits",
	}
)

func zeroCols(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c + " = 0"
	}
	return strings.Join(parts, ", ")
}

// resetSteps lists the tables of a reset (mode as from ResetMode),
// dependent rows first: the tables may be MyISAM (no transactions), so an
// interrupted reset is completed by running it again.
func resetSteps(mode string) []resetStep {
	byGame := func(g string, _, _ int) []any { return []any{g} }
	byGid := func(_ string, id, _ int) []any { return []any{id} }
	byGame2 := func(g string, _, _ int) []any { return []any{g, g} }
	players := `playerId IN (SELECT playerId FROM hlstats_Players WHERE game = ? UNION SELECT playerId FROM hlstats_PlayersInactive WHERE game = ?)`
	servers := `server_id IN (SELECT serverId FROM hlstats_Servers WHERE game = ?)`
	var steps []resetStep
	for _, e := range resetEvents {
		steps = append(steps, resetStep{table: "hlstats_Events_" + e, where: "game_id = ?", args: byGid})
	}
	steps = append(steps,
		resetStep{table: "hlstats_Events_Locations", where: players, args: byGame2},
		resetStep{table: "hlstats_Players_History", where: "game = ?", args: byGame})
	for _, t := range resetPlayerTables {
		steps = append(steps,
			resetStep{table: "hlstats_Players_" + t + "_History_Months", where: "game_id = ?", args: byGid},
			resetStep{table: "hlstats_Players_" + t, where: "game_id = ?", args: byGid})
	}
	steps = append(steps,
		resetStep{table: "hlstats_Awards_History", where: "award_id IN (SELECT awardId FROM hlstats_Awards WHERE game = ?)", args: byGame},
		resetStep{table: "hlstats_Livestats", where: servers, args: byGame},
		resetStep{table: "hlstats_Weapons", set: "kills = 0, headshots = 0", where: "game = ?", args: byGame},
		resetStep{table: "hlstats_Actions", set: "count = 0", where: "game = ?", args: byGame},
		resetStep{table: "hlstats_Teams", set: zeroCols(resetTeamCounters), where: "game = ?", args: byGame},
		resetStep{table: "hlstats_Roles", set: zeroCols(resetTeamCounters), where: "game = ?", args: byGame},
		resetStep{table: "hlstats_Maps", set: "kills = 0, headshots = 0, teamkills = 0, teama_wins = 0, teamb_wins = 0, played = 0, play_time = 0, rounds = 0",
			where: "game = ?", args: byGame},
		resetStep{table: "hlstats_Awards", set: "d_winner_id = NULL, d_winner_count = NULL", where: "game = ?", args: byGame},
	)
	if mode != ResetPlayerStats {
		steps = append(steps,
			resetStep{table: "hlstats_server_load", where: servers, args: byGame},
			resetStep{table: "hlstats_Trend", where: "game = ?", args: byGame},
			resetStep{table: "hlstats_Servers", set: zeroCols(resetServerCounters), where: "game = ?", args: byGame},
		)
	}
	if mode == ResetAll {
		return append(steps,
			resetStep{table: "hlstats_Players_Data", where: players, args: byGame2},
			resetStep{table: "hlstats_PlayerNames", where: players, args: byGame2},
			resetStep{table: "hlstats_Players_Resets", where: strings.Replace(players, "playerId IN", "player_id IN", 1), args: byGame2},
			resetStep{table: "hlstats_PlayerUniqueIds", where: "game = ?", args: byGame},
			resetStep{table: "hlstats_PlayersInactive", where: "game = ?", args: byGame},
			resetStep{table: "hlstats_Players", where: "game = ?", args: byGame},
			resetStep{table: "hlstats_Clans", where: "game = ?", args: byGame},
		)
	}
	withSkill := func(g string, _, skill int) []any { return []any{skill, skill, g} }
	return append(steps,
		resetStep{table: "hlstats_PlayerNames", set: zeroCols(resetNameCounters), where: players, args: byGame2},
		resetStep{table: "hlstats_PlayersInactive", set: "skill = ?, sort_skill = ?, " + zeroCols(resetPlayerCounters), where: "game = ?", args: withSkill},
		resetStep{table: "hlstats_Players", set: "skill = ?, sort_skill = ?, " + zeroCols(resetPlayerCounters), where: "game = ?", args: withSkill},
	)
}

// resetGame checks mode and game and returns the game's hlstats_Games id
// and the mode name.
func (s *Store) resetGame(ctx context.Context, game, mode string) (int, string, error) {
	mode, err := ResetMode(mode)
	if err != nil {
		return 0, "", err
	}
	var id int
	if err := s.queryRow(ctx, `SELECT id FROM hlstats_Games WHERE code = ?`, game).Scan(&id); err != nil {
		return 0, "", fmt.Errorf("game %q: %w", game, ErrUnknownGame)
	}
	return id, mode, nil
}

// ResetCounts returns, without changing anything, how many rows a reset of
// game would delete or update per table.
func (s *Store) ResetCounts(ctx context.Context, game, mode string) ([]TableCount, error) {
	gid, mode, err := s.resetGame(ctx, game, mode)
	if err != nil {
		return nil, err
	}
	skill := s.set.Get().StartSkill
	var out []TableCount
	for _, st := range resetSteps(mode) {
		args := st.args(game, gid, skill)
		if st.set != "" && strings.HasPrefix(st.set, "skill = ?") {
			args = args[2:] // the WHERE arguments only
		}
		var n int64
		if err := s.queryRow(ctx, `SELECT COUNT(*) FROM `+st.table+` WHERE `+st.where, args...).Scan(&n); err != nil {
			return out, fmt.Errorf("%s: %w", st.table, err)
		}
		out = append(out, TableCount{st.table, n})
	}
	return out, nil
}

// Reset resets the ranking game: every table of resetSteps in order, then
// the store's caches (clans and ids of the game may be gone). It returns
// the rows deleted or updated per table; on an error the tables done so
// far (running it again completes it). Only while no processor of the
// game's servers works (the collector pauses them).
func (s *Store) Reset(ctx context.Context, game, mode string) ([]TableCount, error) {
	gid, mode, err := s.resetGame(ctx, game, mode)
	if err != nil {
		return nil, err
	}
	skill := max(0, s.set.Get().StartSkill)
	defer s.ClearCache()
	var out []TableCount
	for _, st := range resetSteps(mode) {
		q := `DELETE FROM ` + st.table + ` WHERE ` + st.where
		if st.set != "" {
			q = `UPDATE ` + st.table + ` SET ` + st.set + ` WHERE ` + st.where
		}
		res, err := s.exec(ctx, q, st.args(game, gid, skill)...)
		if err != nil {
			return out, fmt.Errorf("%s: %w", st.table, err)
		}
		n, _ := res.RowsAffected()
		out = append(out, TableCount{st.table, n})
	}
	return out, nil
}

// ClearCache forgets every cached id (after a reset).
func (s *Store) ClearCache() {
	s.mu.Lock()
	s.cache = map[string]cached{}
	s.mu.Unlock()
}

// FormatCounts is the short result of a reset: "table rows, ...", tables
// without rows left out.
func FormatCounts(c []TableCount) string {
	var parts []string
	for _, t := range c {
		if t.Rows > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", strings.TrimPrefix(t.Table, "hlstats_"), t.Rows))
		}
	}
	if len(parts) == 0 {
		return "no rows"
	}
	return strings.Join(parts, ", ")
}
