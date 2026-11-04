// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"fmt"
	"strings"
)

// playerRows are all rows that belong to a player, in delete order: the player
// itself last, so that a failed run can be repeated. Rows shared with another
// player (a kill) go too; the other player's totals stay. A test compares this
// list with the schema.
var playerRows = []struct {
	table string
	cols  []string
}{
	{"hlstats_Events_ChangeName", []string{"playerId"}},
	{"hlstats_Events_ChangeRole", []string{"playerId"}},
	{"hlstats_Events_ChangeTeam", []string{"playerId"}},
	{"hlstats_Events_Chat", []string{"playerId"}},
	{"hlstats_Events_Connects", []string{"playerId"}},
	{"hlstats_Events_Disconnects", []string{"playerId"}},
	{"hlstats_Events_Entries", []string{"playerId"}},
	{"hlstats_Events_Frags", []string{"killerId", "victimId"}},
	{"hlstats_Events_Latency", []string{"playerId"}},
	{"hlstats_Events_Locations", []string{"playerId"}},
	{"hlstats_Events_PlayerActions", []string{"playerId"}},
	{"hlstats_Events_PlayerPlayerActions", []string{"playerId", "victimId"}},
	{"hlstats_Events_Suicides", []string{"playerId"}},
	{"hlstats_Events_TeamBonuses", []string{"playerId"}},
	{"hlstats_Events_Teamkills", []string{"killerId", "victimId"}},
	{"hlstats_Awards_History", []string{"winner_id"}},
	{"hlstats_Bans", []string{"playerId"}},
	{"hlstats_Bans_Players", []string{"playerId"}},
	{"hlstats_Livestats", []string{"player_id"}},
	{"hlstats_PlayerNames", []string{"playerId"}},
	{"hlstats_Players_Actions", []string{"player_id"}},
	{"hlstats_Players_Actions_History_Months", []string{"player_id"}},
	{"hlstats_Players_Bans", []string{"player_id"}},
	{"hlstats_Players_Data", []string{"playerId"}},
	{"hlstats_Players_History", []string{"playerId"}},
	{"hlstats_Players_Maps", []string{"player_id"}},
	{"hlstats_Players_Maps_History_Months", []string{"player_id"}},
	{"hlstats_Players_Resets", []string{"player_id"}},
	{"hlstats_Players_Weapons", []string{"player_id"}},
	{"hlstats_Players_Weapons_History_Months", []string{"player_id"}},
	{"hlstats_gamewe_hall_of_fame", []string{"player_id"}},
	{"hlstats_PlayerUniqueIds", []string{"playerId"}},
	{"hlstats_PlayersInactive", []string{"playerId"}},
	{"hlstats_Players", []string{"playerId"}},
}

// playerRefs are rows that stay and only forget the player.
var playerRefs = []struct{ table, set, col string }{
	{"hlstats_Awards", "d_winner_id = NULL, d_winner_count = NULL", "d_winner_id"},
	{"hlstats_Users", "playerId = 0", "playerId"},
}

// PlayerInGame reports whether the player exists in that ranking, active or
// moved to the inactive players.
func (s *Store) PlayerInGame(ctx context.Context, playerID int64, game string) (bool, error) {
	var n int64
	err := s.queryRow(ctx, `SELECT (SELECT COUNT(*) FROM hlstats_Players WHERE playerId = ? AND game = ?)
		+ (SELECT COUNT(*) FROM hlstats_PlayersInactive WHERE playerId = ? AND game = ?)`, playerID, game, playerID, game).Scan(&n)
	return n > 0, err
}

// DeletePlayer erases a player (the right to erasure), with the rows done so
// far on an error. Whoever plays again is recorded as a new player.
func (s *Store) DeletePlayer(ctx context.Context, playerID int64) ([]TableCount, error) {
	var out []TableCount
	count := func(table, q string, args ...any) error {
		res, err := s.exec(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out = append(out, TableCount{table, n})
		}
		return nil
	}
	for _, r := range playerRefs {
		if err := count(r.table, `UPDATE `+r.table+` SET `+r.set+` WHERE `+r.col+` = ?`, playerID); err != nil {
			return out, err
		}
	}
	for _, r := range playerRows {
		args := make([]any, len(r.cols))
		for i := range args {
			args[i] = playerID
		}
		where := strings.Join(r.cols, " = ? OR ") + " = ?"
		if err := count(r.table, `DELETE FROM `+r.table+` WHERE `+where, args...); err != nil {
			return out, err
		}
	}
	return out, nil
}
