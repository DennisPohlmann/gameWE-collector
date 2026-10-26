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

// Scheduled ranking resets and the hall of fame.
//
// A ranking can be reset on a schedule: every N days, weeks, months or
// years (hlstats_Options gamewe_reset_schedule_<game>, e.g. "month",
// "2 month", "week", "1 day"; empty or "off": none). The periods follow the
// calendar of the collector's time zone: a day starts at 00:00, a week on
// Monday, a month on the 1st, a year on 1 January, and "every N" counts
// from a fixed start (days and weeks since 1970, months since year 0), so
// "2 month" always ends on 1 March, 1 May, ... whenever it was set up.
//
// When a period ends, the reset job first copies the best three players of
// the period into hlstats_gamewe_hall_of_fame (with their name, country,
// rank and values of that moment, so the entry never changes later), then
// resets the ranking in the mode gamewe_reset_mode_<game> (default
// player-stats) and stores the end of the period in
// gamewe_reset_last_<game>. A period that ended while no collector ran is
// done at the next start; several missed periods become one entry.

// Units of a reset schedule.
const (
	UnitDay   = "day"
	UnitWeek  = "week"
	UnitMonth = "month"
	UnitYear  = "year"
)

// MaxEvery is the largest N of "every N units".
const MaxEvery = 120

// ResetSchedule is "every Every Unit"; the zero value is no schedule.
type ResetSchedule struct {
	Unit  string
	Every int
}

// On reports whether a schedule is set.
func (s ResetSchedule) On() bool { return s.Unit != "" && s.Every > 0 }

// String is the option value: "month", "2 month", "" (none).
func (s ResetSchedule) String() string {
	switch {
	case !s.On():
		return ""
	case s.Every == 1:
		return s.Unit
	}
	return strconv.Itoa(s.Every) + " " + s.Unit
}

// ParseResetSchedule reads an option value: "", "off" (none), "UNIT" or
// "N UNIT" with UNIT day, week, month, year (also plural, also "quarter"
// for 3 months), N 1 to MaxEvery.
func ParseResetSchedule(v string) (ResetSchedule, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(v)))
	if len(f) == 0 || (len(f) == 1 && (f[0] == "off" || f[0] == "0")) {
		return ResetSchedule{}, nil
	}
	n, unit := 1, f[0]
	if len(f) == 2 {
		var err error
		if n, err = strconv.Atoi(f[0]); err != nil || n < 1 || n > MaxEvery {
			return ResetSchedule{}, fmt.Errorf("reset schedule %q: the number must be 1 to %d", v, MaxEvery)
		}
		unit = f[1]
	} else if len(f) > 2 {
		return ResetSchedule{}, fmt.Errorf("reset schedule %q: want UNIT or N UNIT (day, week, month, year)", v)
	}
	unit = strings.TrimSuffix(unit, "s")
	switch unit {
	case UnitDay, UnitWeek, UnitMonth, UnitYear:
	case "quarter":
		unit, n = UnitMonth, n*3
	default:
		return ResetSchedule{}, fmt.Errorf("reset schedule %q: unknown unit %q (day, week, month, year)", v, unit)
	}
	if n > MaxEvery {
		return ResetSchedule{}, fmt.Errorf("reset schedule %q: the number must be 1 to %d", v, MaxEvery)
	}
	return ResetSchedule{Unit: unit, Every: n}, nil
}

// epochMonday is the Monday the week count starts from.
var epochMonday = time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC)

// PeriodStart returns the start of the period of s that contains t, in loc.
func (s ResetSchedule) PeriodStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	y, m, d := t.Date()
	n := max(1, s.Every)
	// Days and weeks are counted on the calendar date (as UTC), so a
	// daylight saving change never shifts a boundary.
	days := int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Sub(epochMonday).Hours() / 24)
	switch s.Unit {
	case UnitDay:
		return time.Date(y, m, d-mod(days, n), 0, 0, 0, 0, loc)
	case UnitWeek:
		weekday := mod(days, 7) // 0 = Monday
		weeks := (days - weekday) / 7
		return time.Date(y, m, d-weekday-7*mod(weeks, n), 0, 0, 0, 0, loc)
	case UnitYear:
		return time.Date(y-mod(y, n), 1, 1, 0, 0, 0, 0, loc)
	}
	months := y*12 + int(m) - 1
	months -= mod(months, n)
	return time.Date(months/12, time.Month(months%12+1), 1, 0, 0, 0, 0, loc)
}

func mod(a, n int) int { return ((a % n) + n) % n }

// Next returns the start of the period after the one that contains t.
func (s ResetSchedule) Next(t time.Time, loc *time.Location) time.Time {
	start := s.PeriodStart(t, loc)
	y, m, d := start.Date()
	n := max(1, s.Every)
	switch s.Unit {
	case UnitDay:
		return time.Date(y, m, d+n, 0, 0, 0, 0, loc)
	case UnitWeek:
		return time.Date(y, m, d+7*n, 0, 0, 0, 0, loc)
	case UnitYear:
		return time.Date(y+n, 1, 1, 0, 0, 0, 0, loc)
	}
	return time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, loc)
}

// Option keys of a ranking's schedule. game is a code from hlstats_Games.
func ScheduleKey(game string) string  { return "gamewe_reset_schedule_" + game }
func ModeKey(game string) string      { return "gamewe_reset_mode_" + game }
func LastResetKey(game string) string { return "gamewe_reset_last_" + game }

// ScheduledRanking is the schedule of one ranking.
type ScheduledRanking struct {
	Game     string
	Schedule ResetSchedule
	Mode     string // as from ResetMode
	Last     int64  // Unix time of the end of the last period done; 0: none yet
}

// ScheduledRankings returns the rankings of hlstats_Games with a schedule.
// A broken value is returned as error for its game and skipped.
func (s *Store) ScheduledRankings(ctx context.Context) ([]ScheduledRanking, []error, error) {
	opts, err := s.Options(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.query(ctx, `SELECT code FROM hlstats_Games ORDER BY code`)
	if err != nil {
		return nil, nil, err
	}
	var games []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return nil, nil, err
		}
		games = append(games, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var out []ScheduledRanking
	var bad []error
	for _, g := range games {
		r, err := scheduleOf(g, opts)
		if err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", g, err))
			continue
		}
		if r.Schedule.On() {
			out = append(out, r)
		}
	}
	return out, bad, nil
}

// ScheduledRanking returns the schedule of one ranking (Schedule not On
// when it has none).
func (s *Store) ScheduledRanking(ctx context.Context, game string) (ScheduledRanking, error) {
	opts, err := s.Options(ctx)
	if err != nil {
		return ScheduledRanking{}, err
	}
	return scheduleOf(game, opts)
}

func scheduleOf(game string, opts map[string]string) (ScheduledRanking, error) {
	sch, err := ParseResetSchedule(opts[ScheduleKey(game)])
	if err != nil {
		return ScheduledRanking{Game: game}, err
	}
	mode := ResetPlayerStats
	if v := strings.TrimSpace(opts[ModeKey(game)]); v != "" {
		if mode, err = ResetMode(v); err != nil {
			return ScheduledRanking{Game: game}, err
		}
	}
	last, _ := strconv.ParseInt(strings.TrimSpace(opts[LastResetKey(game)]), 10, 64)
	return ScheduledRanking{Game: game, Schedule: sch, Mode: mode, Last: max(0, last)}, nil
}

// ScheduledResult is what a scheduled reset did (or would do).
type ScheduledResult struct {
	// Due: a period ended. Started: the schedule had no last reset yet;
	// the current period was recorded as the first one and nothing reset.
	Due, Started bool
	From, To     time.Time // the period that ended [From, To)
	Next         time.Time // end of the period now running
	Top          []HallEntry
	Tables       []TableCount
}

// Due returns, without changing anything, what a scheduled reset of r at
// now would do (Top: the players who would enter the hall of fame).
func (s *Store) Due(ctx context.Context, r ScheduledRanking, now time.Time, loc *time.Location) (ScheduledResult, error) {
	if !r.Schedule.On() {
		return ScheduledResult{}, fmt.Errorf("ranking %s has no reset schedule (%s)", r.Game, ScheduleKey(r.Game))
	}
	cur := r.Schedule.PeriodStart(now, loc)
	res := ScheduledResult{Next: r.Schedule.Next(now, loc)}
	if r.Last == 0 {
		res.Started = true
		return res, nil
	}
	if r.Last >= cur.Unix() {
		return res, nil
	}
	res.Due = true
	res.From = time.Unix(r.Last, 0).In(loc)
	res.To = cur
	top, err := s.HallTop(ctx, r.Game)
	res.Top = top
	return res, err
}

// ScheduledReset does the scheduled reset of r if one is due at now: the
// best three into the hall of fame, the reset, the new last reset. Without
// a last reset it only records the current period's start. Only under the
// job lock and while the game's servers are paused (like Reset).
func (s *Store) ScheduledReset(ctx context.Context, r ScheduledRanking, now time.Time, loc *time.Location) (ScheduledResult, error) {
	res, err := s.Due(ctx, r, now, loc)
	if err != nil {
		return res, err
	}
	cur := r.Schedule.PeriodStart(now, loc)
	if res.Started {
		return res, s.setOption(ctx, LastResetKey(r.Game), strconv.FormatInt(cur.Unix(), 10))
	}
	if !res.Due {
		return res, nil
	}
	if err := s.archive(ctx, r, res, now); err != nil {
		return res, fmt.Errorf("hall of fame: %w", err)
	}
	if res.Tables, err = s.Reset(ctx, r.Game, r.Mode); err != nil {
		return res, err
	}
	return res, s.setOption(ctx, LastResetKey(r.Game), strconv.FormatInt(cur.Unix(), 10))
}

// HallEntry is one place of the hall of fame.
type HallEntry struct {
	Place                                int
	PlayerID                             int64
	Name, Flag, Country, Rank, FavWeapon string
	Skill                                int64
	Kills, Deaths, Headshots, Streak     int64
	Shots, Hits, ConnectionTime          int64
}

// hallPlayers are the players who count: shown in the ranking (hideranking
// 0), not bots, with a kill or a death in the period.
const hallPlayers = `p.game = ? AND p.hideranking = 0 AND (p.kills > 0 OR p.deaths > 0)
	AND NOT EXISTS (SELECT 1 FROM hlstats_PlayerUniqueIds u WHERE u.playerId = p.playerId AND u.uniqueId LIKE 'BOT%')`

// HallTop returns the best three players of game now: most points, then
// most kills, then the lower id.
func (s *Store) HallTop(ctx context.Context, game string) ([]HallEntry, error) {
	rows, err := s.query(ctx, `SELECT p.playerId, COALESCE(d.lastName, ''), COALESCE(l.flag, ''), COALESCE(l.country, ''),
			p.skill, p.kills, p.deaths, p.headshots, p.shots, p.hits, p.kill_streak, p.connection_time
		FROM hlstats_Players p
		LEFT JOIN hlstats_Players_Data d ON d.playerId = p.playerId
		LEFT JOIN hlstats_Players_Locations l ON l.id = d.location_id
		WHERE `+hallPlayers+`
		ORDER BY p.skill DESC, p.kills DESC, p.playerId
		LIMIT 3`, game)
	if err != nil {
		return nil, err
	}
	var out []HallEntry
	for rows.Next() {
		e := HallEntry{Place: len(out) + 1}
		if err := rows.Scan(&e.PlayerID, &e.Name, &e.Flag, &e.Country, &e.Skill, &e.Kills, &e.Deaths, &e.Headshots,
			&e.Shots, &e.Hits, &e.Streak, &e.ConnectionTime); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		e := &out[i]
		if err := s.queryRow(ctx, `SELECT name FROM hlstats_Definition_Ranks_Ribbons
			WHERE game_code = ? AND definition_type IN ('ranks', 'rank') AND restriction_type = 'kills' AND minimum <= ?
			ORDER BY minimum DESC, id DESC LIMIT 1`, game, e.Kills).Scan(&e.Rank); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err := s.queryRow(ctx, `SELECT w.code FROM hlstats_Players_Weapons pw
			JOIN hlstats_Weapons w ON w.weaponId = pw.weapon_id
			WHERE pw.player_id = ?
			GROUP BY w.code ORDER BY SUM(pw.kills) DESC, w.code LIMIT 1`, e.PlayerID).Scan(&e.FavWeapon); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}

// archive stores the best three of the period res in the hall of fame. A
// place already stored (a reset that stopped and runs again) is kept.
func (s *Store) archive(ctx context.Context, r ScheduledRanking, res ScheduledResult, now time.Time) error {
	var players int64
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM hlstats_Players p WHERE `+hallPlayers, r.Game).Scan(&players); err != nil {
		return err
	}
	last := res.To.AddDate(0, 0, -1)
	for _, e := range res.Top {
		if _, err := s.exec(ctx, s.ignore(`INSERT INTO hlstats_gamewe_hall_of_fame
			(game, unit, every, period_start, period_end, reset_at, players, place, player_id, name, flag, country,
			 skill, kills, deaths, headshots, shots, hits, kill_streak, connection_time, fav_weapon, rank_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			r.Game, r.Schedule.Unit, r.Schedule.Every, res.From.Format(time.DateOnly), last.Format(time.DateOnly), now.Unix(), players,
			e.Place, e.PlayerID, clip(e.Name, 64), clip(strings.ToLower(e.Flag), 16), clip(e.Country, 64),
			e.Skill, e.Kills, e.Deaths, e.Headshots, e.Shots, e.Hits, e.Streak, e.ConnectionTime, clip(e.FavWeapon, 64), clip(e.Rank, 128)); err != nil {
			return err
		}
	}
	return nil
}

// HallRow is a stored hall of fame entry.
type HallRow struct {
	Game, Unit             string
	Every                  int
	PeriodStart, PeriodEnd string
	HallEntry
}

// HallOfFame returns the stored entries of game ("": all), newest period
// first.
func (s *Store) HallOfFame(ctx context.Context, game string) ([]HallRow, error) {
	q := `SELECT game, unit, every, period_start, period_end, place, player_id, name, flag, country, rank_name, fav_weapon,
		skill, kills, deaths, headshots, kill_streak, shots, hits, connection_time FROM hlstats_gamewe_hall_of_fame`
	var args []any
	if game != "" {
		q += ` WHERE game = ?`
		args = append(args, game)
	}
	rows, err := s.query(ctx, q+` ORDER BY period_start DESC, game, place`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HallRow
	for rows.Next() {
		var h HallRow
		var start, end any
		if err := rows.Scan(&h.Game, &h.Unit, &h.Every, &start, &end, &h.Place, &h.PlayerID, &h.Name, &h.Flag, &h.Country, &h.Rank, &h.FavWeapon,
			&h.Skill, &h.Kills, &h.Deaths, &h.Headshots, &h.Streak, &h.Shots, &h.Hits, &h.ConnectionTime); err != nil {
			return nil, err
		}
		h.PeriodStart, h.PeriodEnd = dateString(start), dateString(end)
		out = append(out, h)
	}
	return out, rows.Err()
}

// dateString formats a DATE column (time.Time or text, depending on the
// driver) as YYYY-MM-DD.
func dateString(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Format(time.DateOnly)
	case []byte:
		return string(t[:min(len(t), 10)])
	case string:
		return t[:min(len(t), 10)]
	}
	return ""
}
