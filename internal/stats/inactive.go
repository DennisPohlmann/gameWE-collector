// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinActivityKey is the hlstats_Options key of HLstatsX and the web: days
// without play after which a player's activity is 0 % and the player
// leaves the rankings.
const MinActivityKey = "MinActivity"

// DefaultInactiveDays is MinActivity when the key is missing or invalid.
const DefaultInactiveDays = 28

// maxInactiveDays bounds MinActivity (as the web admin does).
const maxInactiveDays = 3650

// inactiveBatch is how many players one batch of MoveInactive moves.
const inactiveBatch = 500

// InactiveDays returns MinActivity from hlstats_Options, or
// DefaultInactiveDays with a warning when it is missing or invalid.
func (s *Store) InactiveDays(ctx context.Context) (days int, warning string, err error) {
	var v string
	err = s.queryRow(ctx, `SELECT value FROM hlstats_Options WHERE keyname = ?`, MinActivityKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultInactiveDays, fmt.Sprintf("hlstats_Options %s is missing, using %d days", MinActivityKey, DefaultInactiveDays), nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("hlstats_Options %s: %w", MinActivityKey, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > maxInactiveDays {
		return DefaultInactiveDays, fmt.Sprintf("hlstats_Options %s = %q is invalid (must be 1 to %d days), using %d",
			MinActivityKey, v, maxInactiveDays, DefaultInactiveDays), nil
	}
	return n, "", nil
}

// inactive is the condition of a player row p to move: no event since the
// cutoff and not online.
const inactive = `p.last_event < ? AND NOT EXISTS (SELECT 1 FROM hlstats_Livestats l WHERE l.player_id = p.playerId)`

// MoveInactive moves the players whose last event is before cutoff and who
// are not in hlstats_Livestats from hlstats_Players to
// hlstats_PlayersInactive (all columns, as Player moves them back), in
// batches of inactiveBatch. Other tables are not touched. It returns the
// number of players moved.
func (s *Store) MoveInactive(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.MoveInactiveProgress(ctx, cutoff, nil)
}

// MoveInactiveProgress is MoveInactive that calls progress (if not nil)
// with the number of players moved so far after every batch but the last.
func (s *Store) MoveInactiveProgress(ctx context.Context, cutoff time.Time, progress func(moved int64)) (int64, error) {
	var moved, after int64
	for {
		ids, err := s.inactiveIDs(ctx, cutoff, after)
		if err != nil || len(ids) == 0 {
			return moved, err
		}
		n, err := s.moveInactive(ctx, ids, cutoff)
		moved += n
		if err != nil || len(ids) < inactiveBatch {
			return moved, err
		}
		if progress != nil {
			progress(moved)
		}
		after = ids[len(ids)-1]
	}
}

// InactiveCount returns how many players MoveInactive would move now.
func (s *Store) InactiveCount(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM hlstats_Players p WHERE `+inactive, cutoff.Unix()).Scan(&n)
	return n, err
}

// InactiveCutoff is the last_event before which a player counts as
// inactive at now: MinActivity days (and a warning when the key is missing
// or invalid).
func (s *Store) InactiveCutoff(ctx context.Context, now time.Time) (cutoff time.Time, days int, warning string, err error) {
	days, warning, err = s.InactiveDays(ctx)
	return now.Add(-time.Duration(days) * 24 * time.Hour), days, warning, err
}

// inactiveIDs returns the next batch of players to move, by id after after.
func (s *Store) inactiveIDs(ctx context.Context, cutoff time.Time, after int64) ([]int64, error) {
	rows, err := s.query(ctx, `SELECT p.playerId FROM hlstats_Players p WHERE p.playerId > ? AND `+inactive+
		` ORDER BY p.playerId LIMIT `+strconv.Itoa(inactiveBatch), after, cutoff.Unix())
	if err != nil {
		return nil, fmt.Errorf("hlstats_Players: %w", err)
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

// moveInactive moves one batch in a transaction (PostgreSQL; on MariaDB
// every statement stands alone, as the tables were MyISAM until schema
// version 19). The
// order keeps every state in between valid, also after a crash: a player
// is in hlstats_Players, or in hlstats_PlayersInactive, or for a moment in
// both with identical rows (hlstats_Players wins, as in Player). The
// conditions are checked again in every statement, so a player who came
// back meanwhile stays; a visit start (Join) that misses its row moves it
// back.
func (s *Store) moveInactive(ctx context.Context, ids []int64, cutoff time.Time) (int64, error) {
	in := "?" + strings.Repeat(", ?", len(ids)-1) // only placeholders
	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	withCutoff := append(append([]any{}, idArgs...), cutoff.Unix())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // a no-op after Commit
	exec := func(q string, args ...any) (int64, error) {
		res, err := tx.ExecContext(ctx, s.db.Q(q), args...)
		if err != nil {
			f := strings.Fields(q)
			return 0, fmt.Errorf("%s: %w", strings.Join(f[:min(4, len(f))], " "), err)
		}
		return res.RowsAffected()
	}
	// PostgreSQL: concurrent updates of these rows wait until the batch is
	// done and then find no row (a visit start then moves it back).
	rows, err := tx.QueryContext(ctx, s.db.Q(`SELECT playerId FROM hlstats_Players WHERE playerId IN (`+in+`) FOR UPDATE`), idArgs...)
	if err != nil {
		return 0, fmt.Errorf("lock hlstats_Players: %w", err)
	}
	rows.Close()
	steps := []struct {
		q    string
		args []any
	}{
		// Leftover copies of these players (a crash between the steps of a
		// move or of a return): hlstats_Players holds the current row.
		{`DELETE FROM hlstats_PlayersInactive WHERE playerId IN (` + in + `)`, idArgs},
		{`INSERT INTO hlstats_PlayersInactive SELECT p.* FROM hlstats_Players p WHERE p.playerId IN (` + in + `) AND ` + inactive, withCutoff},
		{`DELETE FROM hlstats_Players WHERE playerId IN (` + in + `) AND last_event < ?
			AND playerId NOT IN (SELECT player_id FROM hlstats_Livestats)
			AND playerId IN (SELECT playerId FROM hlstats_PlayersInactive)`, withCutoff},
		// Players who came back between the copy and the delete.
		{`DELETE FROM hlstats_PlayersInactive WHERE playerId IN (` + in + `) AND playerId IN (SELECT playerId FROM hlstats_Players)`, idArgs},
	}
	var moved int64
	for i, st := range steps {
		n, err := exec(st.q, st.args...)
		if err != nil {
			return 0, err
		}
		if i == 2 {
			moved = n
		}
	}
	return moved, tx.Commit()
}
