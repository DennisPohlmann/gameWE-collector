// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BanTypeSourceBans is hlstats_Players_Bans.ban_type of a SourceBans ban.
const BanTypeSourceBans = "sourcebans"

// Ban is a ban to record for a player (hlstats_Players_Bans).
type Ban struct {
	Type          string    // BanTypeSourceBans
	Reason        string    // at most 255 characters are stored
	LengthMinutes int       // 0: permanent or unknown
	BannedAt      time.Time // zero: unknown (the time of the first record)
	EndsAt        time.Time // zero: permanent or unknown
}

// PlayerBan is a running ban of hlstats_Players_Bans.
type PlayerBan struct {
	ID       int64
	PlayerID int64
	Game     string
	UniqueID string
	BannedAt int64
	EndsAt   int64 // 0: permanent or unknown
	Hid      bool  // the record took the player out of the ranking
}

// RecordBan stores the running ban b of the player uniqueID in game and
// takes the player out of the ranking (hideranking 2) when it is shown
// there (hideranking 0); hid of the row remembers that, so EndBan puts the
// player back only then (not players hidden by an admin, bots or banned in
// another way). A running ban of the same type of the player is updated
// instead of adding a second one. It returns how many players it took out
// of the ranking.
func (s *Store) RecordBan(ctx context.Context, game, uniqueID string, b Ban, now time.Time) (int, error) {
	ids, err := s.playerIDs(ctx, game, uniqueID)
	if err != nil {
		return 0, err
	}
	hidden := 0
	for _, pid := range ids {
		h, err := s.hideranking(ctx, pid)
		if err != nil {
			return hidden, err
		}
		hide := h == 0
		bannedAt := now.Unix()
		if !b.BannedAt.IsZero() {
			bannedAt = b.BannedAt.Unix()
		}
		var endsAt int64
		if !b.EndsAt.IsZero() {
			endsAt = b.EndsAt.Unix()
		}
		var id int64
		var wasHid int
		// The running ban of the player, else an ended one of the same start
		// (lifted by mistake and banned again).
		err = s.queryRow(ctx, `SELECT id, hid FROM hlstats_Players_Bans WHERE player_id = ? AND ban_type = ?
			AND (active = 1 OR banned_at = ?) ORDER BY active DESC, id DESC LIMIT 1`, pid, b.Type, bannedAt).Scan(&id, &wasHid)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = s.run(ctx, step{`INSERT INTO hlstats_Players_Bans (player_id, game, unique_id, ban_type, reason, length_minutes,
				banned_at, ends_at, active, hid, checked_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, 0)`,
				[]any{pid, clip(game, 32), clip(uniqueID, 64), b.Type, clip(b.Reason, 255), max(0, b.LengthMinutes),
					bannedAt, endsAt, boolInt(hide), now.Unix()}})
		case err == nil:
			q := `UPDATE hlstats_Players_Bans SET reason = ?, length_minutes = ?, ends_at = ?, active = 1, ended_at = 0, checked_at = ?, hid = ?`
			args := []any{clip(b.Reason, 255), max(0, b.LengthMinutes), endsAt, now.Unix(), boolInt(hide || wasHid == 1)}
			if !b.BannedAt.IsZero() {
				q += `, banned_at = ?`
				args = append(args, bannedAt)
			}
			err = s.run(ctx, step{q + ` WHERE id = ?`, append(args, id)})
		}
		if err != nil {
			return hidden, err
		}
		if hide {
			if err := s.setHideranking(ctx, pid, 0, 2); err != nil {
				return hidden, err
			}
			hidden++
		}
	}
	return hidden, nil
}

// RunningBans returns the running bans of a type, oldest check first.
func (s *Store) RunningBans(ctx context.Context, banType string) ([]PlayerBan, error) {
	rows, err := s.query(ctx, `SELECT id, player_id, game, unique_id, banned_at, ends_at, hid FROM hlstats_Players_Bans
		WHERE active = 1 AND ban_type = ? ORDER BY checked_at, id`, banType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayerBan
	for rows.Next() {
		var b PlayerBan
		var hid int
		if err := rows.Scan(&b.ID, &b.PlayerID, &b.Game, &b.UniqueID, &b.BannedAt, &b.EndsAt, &hid); err != nil {
			return nil, err
		}
		b.Hid = hid == 1
		out = append(out, b)
	}
	return out, rows.Err()
}

// CheckedBan notes that the ban is still running (checked_at).
func (s *Store) CheckedBan(ctx context.Context, id int64, now time.Time) error {
	return s.run(ctx, step{`UPDATE hlstats_Players_Bans SET checked_at = ? WHERE id = ?`, []any{now.Unix(), id}})
}

// EndBan marks the ban as ended (expired or lifted) and, when the record
// took the player out of the ranking and no other running ban of the
// player did, puts the player back (hideranking 2 to 0; a player an admin
// hid meanwhile, hideranking 1, stays hidden). It returns whether the
// player is back in the ranking.
func (s *Store) EndBan(ctx context.Context, b PlayerBan, now time.Time) (bool, error) {
	if err := s.run(ctx, step{`UPDATE hlstats_Players_Bans SET active = 0, ended_at = ? WHERE id = ? AND active = 1`,
		[]any{now.Unix(), b.ID}}); err != nil {
		return false, err
	}
	if !b.Hid {
		return false, nil
	}
	var others int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE player_id = ? AND active = 1 AND hid = 1`,
		b.PlayerID).Scan(&others); err != nil {
		return false, err
	}
	if others > 0 {
		return false, nil
	}
	h, err := s.hideranking(ctx, b.PlayerID)
	if err != nil || h != 2 {
		return false, err
	}
	return true, s.setHideranking(ctx, b.PlayerID, 2, 0)
}

// playerIDs returns the players of a unique id in a game.
func (s *Store) playerIDs(ctx context.Context, game, uniqueID string) ([]int64, error) {
	rows, err := s.query(ctx, `SELECT playerId FROM hlstats_PlayerUniqueIds WHERE uniqueId = ? AND game = ?`, clip(uniqueID, 64), game)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// hideranking of a player in hlstats_Players or hlstats_PlayersInactive
// (-1: neither).
func (s *Store) hideranking(ctx context.Context, playerID int64) (int, error) {
	for _, t := range []string{"hlstats_Players", "hlstats_PlayersInactive"} {
		var h int
		err := s.queryRow(ctx, `SELECT hideranking FROM `+t+` WHERE playerId = ?`, playerID).Scan(&h)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	return -1, nil
}

// setHideranking changes hideranking from one value to another in both
// player tables (the row is in one of them).
func (s *Store) setHideranking(ctx context.Context, playerID int64, from, to int) error {
	return s.run(ctx,
		step{`UPDATE hlstats_Players SET hideranking = ? WHERE playerId = ? AND hideranking = ?`, []any{to, playerID, from}},
		step{`UPDATE hlstats_PlayersInactive SET hideranking = ? WHERE playerId = ? AND hideranking = ?`, []any{to, playerID, from}})
}
