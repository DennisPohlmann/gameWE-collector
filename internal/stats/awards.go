// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// historyAwards are the player awards whose value is a column of the day's
// hlstats_Players_History row, whatever their awardType (HLstatsX:CE defines
// teamkills as W). Every other O code is an action
// code counted in hlstats_Events_PlayerActions, every other W code a weapon.
var historyAwards = map[string]string{
	"most_kills":         "kills",
	"most_deaths":        "deaths",
	"most_suicides":      "suicides",
	"most_headshots":     "headshots",
	"most_damage":        "damage",
	"most_improved":      "skill_change",
	"longest_playtime":   "connection_time", // seconds
	"longest_killstreak": "kill_streak",
	"teamkills":          "team_kills", // HLstatsX:CE
}

// LatencyMinSamples is how many ping samples (one per minute online) a player
// needs on a day to win the latency award: about half an hour, so that a
// short visit with a lucky ping does not win.
const LatencyMinSamples = 30

// LatencyKeep is how long ping samples are kept (hlstats_Events_Latency).
const LatencyKeep = 90 * 24 * time.Hour

// AwardResult is the outcome of one award for a day; WinnerID is 0 if
// nobody qualified.
type AwardResult struct {
	AwardID  int64
	Game     string
	Type     string // W or O
	Code     string
	WinnerID int64
	Count    int
}

// AwardDate is the form of hlstats_Awards_History.date: the calendar day
// of day in its own time zone.
func AwardDate(day time.Time) string { return day.Format("2006-01-02") }

// dayBounds returns the Unix times of the start of day's calendar day and
// of the next one, in day's time zone (23 or 25 hours apart on DST days).
func dayBounds(day time.Time) (from, to int64) {
	y, m, d := day.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, day.Location())
	return start.Unix(), time.Date(y, m, d+1, 0, 0, 0, 0, day.Location()).Unix()
}

// DailyAwards decides the awards of hlstats_Awards for one calendar day in
// day's time zone (the configured features.daily_awards.timezone), for one
// game or (game "") all games, and stores them: hlstats_Awards_History gets
// one row per award with a winner (rows of that day are replaced, so a
// rerun is safe) and hlstats_Awards.d_winner_* the winner of the latest day
// in the history. A run for all games also deletes ping samples older than
// LatencyKeep.
//
// W awards go to the player with the most kills with the weapon of that
// code (hlstats_Events_Frags); O awards to the player with the most
// actions of that code (hlstats_Events_PlayerActions); the player awards
// (historyAwards: most_kills, teamkills, ...) to the highest value in the
// hlstats_Players_History row of that date (history days are UTC days, so
// in another time zone these awards are off by the UTC offset). The W award
// "latency" goes to the player with the lowest average ping of the day
// (hlstats_Events_Latency, samples of 0 left out) among players with at least
// LatencyMinSamples samples; its count is the rounded average in ms.
// Only ranked players (hideranking = 0) win; ties go to the lowest
// playerId; a value of 0 wins nothing.
func (s *Store) DailyAwards(ctx context.Context, day time.Time, game string) ([]AwardResult, error) {
	return s.dailyAwards(ctx, day, game, true)
}

// AwardWinners decides the awards as DailyAwards does but stores nothing
// (gamewe awards --dry-run).
func (s *Store) AwardWinners(ctx context.Context, day time.Time, game string) ([]AwardResult, error) {
	return s.dailyAwards(ctx, day, game, false)
}

func (s *Store) dailyAwards(ctx context.Context, day time.Time, game string, store bool) ([]AwardResult, error) {
	from, to := dayBounds(day)
	date := AwardDate(day)
	q := `SELECT awardId, game, awardType, code FROM hlstats_Awards`
	var args []any
	if game != "" {
		q += ` WHERE game = ?`
		args = append(args, game)
	}
	rows, err := s.query(ctx, q+` ORDER BY game, awardId`, args...)
	if err != nil {
		return nil, err
	}
	var out []AwardResult
	for rows.Next() {
		var a AwardResult
		if err := rows.Scan(&a.AwardID, &a.Game, &a.Type, &a.Code); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		a := &out[i]
		var q string
		var args []any
		switch col, player := historyAwards[a.Code]; {
		case player:
			q = `SELECT h.playerId, h.` + col + ` FROM hlstats_Players_History h JOIN hlstats_Players p ON p.playerId = h.playerId
				WHERE h.game = ? AND h.eventTime = ? AND p.hideranking = 0 AND h.` + col + ` > 0
				ORDER BY h.` + col + ` DESC, h.playerId LIMIT 1`
			args = []any{a.Game, date}
		case a.Type == "O":
			q = `SELECT e.playerId, COUNT(*) AS n FROM hlstats_Events_PlayerActions e
				JOIN hlstats_Actions x ON x.id = e.actionId JOIN hlstats_Players p ON p.playerId = e.playerId
				WHERE x.game = ? AND x.code = ? AND e.timestamp_id >= ? AND e.timestamp_id < ? AND p.hideranking = 0
				GROUP BY e.playerId ORDER BY n DESC, e.playerId LIMIT 1`
			args = []any{a.Game, a.Code, from, to}
		case a.Code == "latency":
			var avg float64
			err := s.queryRow(ctx, `SELECT e.playerId, AVG(e.ping) FROM hlstats_Events_Latency e
				JOIN hlstats_Players p ON p.playerId = e.playerId
				WHERE p.game = ? AND e.timestamp_id >= ? AND e.timestamp_id < ? AND e.ping > 0 AND p.hideranking = 0
				GROUP BY e.playerId HAVING COUNT(*) >= ? ORDER BY AVG(e.ping), e.playerId LIMIT 1`,
				a.Game, from, to, LatencyMinSamples).Scan(&a.WinnerID, &avg)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("award %s %q: %w", a.Game, a.Code, err)
			}
			if a.WinnerID > 0 {
				a.Count = max(int(math.Round(avg)), 1)
			}
		default:
			q = `SELECT f.killerId, COUNT(*) AS n FROM hlstats_Events_Frags f
				JOIN hlstats_Weapons w ON w.weaponId = f.weapon JOIN hlstats_Players p ON p.playerId = f.killerId
				WHERE w.game = ? AND w.code = ? AND f.timestamp_id >= ? AND f.timestamp_id < ? AND p.hideranking = 0
				GROUP BY f.killerId ORDER BY n DESC, f.killerId LIMIT 1`
			args = []any{a.Game, a.Code, from, to}
		}
		if q != "" {
			err := s.queryRow(ctx, q, args...).Scan(&a.WinnerID, &a.Count)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("award %s %q: %w", a.Game, a.Code, err)
			}
		}
		if !store {
			continue
		}
		steps := []step{{`DELETE FROM hlstats_Awards_History WHERE award_id = ? AND date = ?`, []any{a.AwardID, date}}}
		if a.WinnerID > 0 {
			steps = append(steps, step{`INSERT INTO hlstats_Awards_History (award_id, winner_id, date, winner_count) VALUES (?, ?, ?, ?)`,
				[]any{a.AwardID, a.WinnerID, date, min(a.Count, 16777215)}})
		}
		if err := s.run(ctx, steps...); err != nil {
			return nil, err
		}
		if err := s.latestAwardWinner(ctx, a.AwardID); err != nil {
			return nil, err
		}
	}
	if game == "" && store {
		// The daily run also drops old ping samples.
		if err := s.PruneLatency(ctx, time.Unix(from, 0).Add(-LatencyKeep)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// latestAwardWinner sets hlstats_Awards.d_winner_* to the winner of the
// latest day in the history (NULL if there is none).
func (s *Store) latestAwardWinner(ctx context.Context, awardID int64) error {
	var winner, count sql.NullInt64
	err := s.queryRow(ctx, `SELECT winner_id, winner_count FROM hlstats_Awards_History WHERE award_id = ? ORDER BY date DESC, id DESC LIMIT 1`,
		awardID).Scan(&winner, &count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.run(ctx, step{`UPDATE hlstats_Awards SET d_winner_id = ?, d_winner_count = ? WHERE awardId = ?`, []any{winner, count, awardID}})
}

// AwardsDone reports whether the history has any award for the day.
func (s *Store) AwardsDone(ctx context.Context, day time.Time) (bool, error) {
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM hlstats_Awards_History WHERE date = ?`, AwardDate(day)).Scan(&n)
	return n > 0, err
}
