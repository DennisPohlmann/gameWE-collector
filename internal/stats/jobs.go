// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Job states in hlstats_gamewe_jobs.status.
const (
	JobPending = "pending"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// Job types: a ranking reset (game and mode), the move of inactive players
// (features.inactive_players; no game, no mode), the scheduled reset of a
// ranking (game; hall of fame first, mode from its schedule), the daily
// awards (features.daily_awards; no game, the day YYYY-MM-DD as mode).
const (
	JobReset          = "reset"
	JobInactive       = "inactive"
	JobScheduledReset = "scheduled-reset"
	JobAwards         = "awards"
)

// JobBySchedule is requested_by of the jobs the collector queues itself
// (daily awards, daily move of inactive players, scheduled resets).
const JobBySchedule = "schedule"

// Job origins (hlstats_gamewe_jobs.origin): the feature that queued a job.
// Empty for jobs queued before schema version 22 (job-origin).
const (
	OriginScheduledResets = "scheduled_resets" // features.scheduled_resets
	OriginDailyAwards     = "daily_awards"     // features.daily_awards
	OriginInactive        = "inactive_players" // features.inactive_players
	OriginCLI             = "cli"              // a gamewe command
	OriginAdmin           = "admin"            // the web admin (written by the web)
	OriginAPI             = "api"              // the web's admin API (written by the web)
)

// JobOrigin is why a job was queued: Feature goes into the column origin,
// the rest as JSON into origin_detail (only the fields that apply).
type JobOrigin struct {
	Feature string `json:"-"`
	// Startup: queued by the collector's first look at its schedule after
	// it started (a period or day that ended while it was not running, or
	// at the first start with that schedule).
	Startup bool `json:"startup,omitempty"`
	// The period of a scheduled reset that ended: [From, To) as Unix
	// times, the schedule (as in gamewe_reset_schedule), whether the
	// ranking follows the schedule for all rankings, and the time zone.
	From      int64  `json:"from,omitempty"`
	To        int64  `json:"to,omitempty"`
	Schedule  string `json:"schedule,omitempty"`
	Inherited bool   `json:"inherited,omitempty"`
	Zone      string `json:"zone,omitempty"`
	// Day of the daily awards (YYYY-MM-DD).
	Day string `json:"day,omitempty"`
	// Command: the gamewe command that queued the job (built from its
	// checked arguments, never the raw command line).
	Command string `json:"command,omitempty"`
	// Token: the API token's name (written by the web).
	Token string `json:"token,omitempty"`
	// Version of the collector that queued the job.
	Version string `json:"version,omitempty"`
}

// Detail is the value of origin_detail ("" without details).
func (o JobOrigin) Detail() string {
	o.Feature = ""
	if o == (JobOrigin{}) {
		return ""
	}
	b, _ := json.Marshal(o)
	if len(b) > 1024 {
		return ""
	}
	return string(b)
}

// Summary is the origin in one line for the console: the feature, the
// period of a scheduled reset (in its zone, last day included), whether it
// was queued at the collector's start, the command or the API token.
func (o JobOrigin) Summary() string {
	parts := []string{cmp.Or(o.Feature, "-")}
	if o.From > 0 && o.To > o.From {
		loc := time.UTC
		if z, err := time.LoadLocation(o.Zone); err == nil && o.Zone != "" {
			loc = z
		}
		parts = append(parts, "period "+time.Unix(o.From, 0).In(loc).Format(time.DateOnly)+" - "+time.Unix(o.To-1, 0).In(loc).Format(time.DateOnly))
	}
	if o.Day != "" {
		parts = append(parts, "day "+o.Day)
	}
	if o.Startup {
		parts = append(parts, "at the collector's start")
	}
	if o.Command != "" {
		parts = append(parts, o.Command)
	}
	if o.Token != "" {
		parts = append(parts, "token "+o.Token)
	}
	return strings.Join(parts, ", ")
}

// ParseJobOrigin reads origin and origin_detail of a row (a broken detail
// only loses the details).
func ParseJobOrigin(feature, detail string) JobOrigin {
	var o JobOrigin
	if strings.TrimSpace(detail) != "" {
		_ = json.Unmarshal([]byte(detail), &o)
	}
	o.Feature = feature
	return o
}

// Job is a row of hlstats_gamewe_jobs.
type Job struct {
	ID                    int64
	Type, Game, Mode      string
	Status, RequestedBy   string
	RequestedAt           int64
	StartedAt, FinishedAt int64 // 0 if not yet
	Result                string
	Origin                JobOrigin
}

// HeartbeatKey is the hlstats_Options key the running collector writes
// every HeartbeatInterval (Unix time).
const HeartbeatKey = "gamewe_heartbeat"

// VersionKey is the hlstats_Options key with the running collector's
// version, written at its first heartbeat; the web admin compares it with
// the latest release.
const VersionKey = "gamewe_collector_version"

// HeartbeatInterval and HeartbeatFresh: a heartbeat younger than
// HeartbeatFresh means a collector runs and will do queued jobs.
const (
	HeartbeatInterval = time.Minute
	HeartbeatFresh    = 2 * time.Minute
)

// jobCols are the columns scanJobs reads; without the origin columns
// (NoJobOrigin) the origin is read as empty.
func (s *Store) jobCols() string {
	origin := `origin, origin_detail`
	if s.NoJobOrigin {
		origin = `'', ''`
	}
	return `id, job_type, game, mode, status, requested_by, requested_at, COALESCE(started_at, 0), COALESCE(finished_at, 0), result, ` + origin
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var origin, detail string
		if err := rows.Scan(&j.ID, &j.Type, &j.Game, &j.Mode, &j.Status, &j.RequestedBy, &j.RequestedAt, &j.StartedAt, &j.FinishedAt, &j.Result, &origin, &detail); err != nil {
			return nil, err
		}
		j.Origin = ParseJobOrigin(origin, detail)
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobsByStatus returns the jobs with status, oldest first.
func (s *Store) JobsByStatus(ctx context.Context, status string) ([]Job, error) {
	rows, err := s.query(ctx, `SELECT `+s.jobCols()+` FROM hlstats_gamewe_jobs WHERE status = ? ORDER BY id`, status)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// RecentJobs returns the newest n jobs, newest first.
func (s *Store) RecentJobs(ctx context.Context, n int) ([]Job, error) {
	return s.ListJobs(ctx, JobFilter{Limit: n})
}

// JobFilter selects jobs for ListJobs: only these types and statuses
// (empty: all), at most Limit (at least 1), newest first or, with
// OldestFirst, in the order the collector does them.
type JobFilter struct {
	Types, Statuses []string
	Limit           int
	OldestFirst     bool
}

// ListJobs returns the jobs that match f.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	var where []string
	var args []any
	for _, c := range []struct {
		col  string
		vals []string
	}{{"job_type", f.Types}, {"status", f.Statuses}} {
		if len(c.vals) == 0 {
			continue
		}
		where = append(where, c.col+" IN (?"+strings.Repeat(", ?", len(c.vals)-1)+")") // only placeholders
		for _, v := range c.vals {
			args = append(args, v)
		}
	}
	q := `SELECT ` + s.jobCols() + ` FROM hlstats_gamewe_jobs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	order := " DESC"
	if f.OldestFirst {
		order = ""
	}
	rows, err := s.query(ctx, q+` ORDER BY id`+order+` LIMIT `+strconv.Itoa(max(1, f.Limit)), args...)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// Job returns one job.
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	rows, err := s.query(ctx, `SELECT `+s.jobCols()+` FROM hlstats_gamewe_jobs WHERE id = ?`, id)
	if err != nil {
		return Job{}, err
	}
	jobs, err := scanJobs(rows)
	if err != nil {
		return Job{}, err
	}
	if len(jobs) == 0 {
		return Job{}, sql.ErrNoRows
	}
	return jobs[0], nil
}

// AddJob queues a job and returns its id; o says which feature queued it
// and why (not stored with NoJobOrigin).
func (s *Store) AddJob(ctx context.Context, typ, game, mode, by string, o JobOrigin, at time.Time) (int64, error) {
	if s.NoJobOrigin {
		return s.insertID(ctx, `INSERT INTO hlstats_gamewe_jobs (job_type, game, mode, status, requested_by, requested_at, result)
		VALUES (?, ?, ?, ?, ?, ?, '')`, "id", typ, clip(game, 32), clip(mode, 16), JobPending, clip(by, 64), at.Unix())
	}
	return s.insertID(ctx, `INSERT INTO hlstats_gamewe_jobs (job_type, game, mode, status, requested_by, requested_at, result, origin, origin_detail)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, "id", typ, clip(game, 32), clip(mode, 16), JobPending, clip(by, 64), at.Unix(), clip(o.Feature, 32), o.Detail())
}

// ClaimJob moves a job from status from to running; false if another
// collector (or a second loop) took it first.
func (s *Store) ClaimJob(ctx context.Context, id int64, from string, at time.Time) (bool, error) {
	res, err := s.exec(ctx, `UPDATE hlstats_gamewe_jobs SET status = ?, started_at = ? WHERE id = ? AND status = ?`, JobRunning, at.Unix(), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// JobProgress stores text as the result of a running job, so the command
// that queued it can show how far it is; FinishJob replaces it.
func (s *Store) JobProgress(ctx context.Context, id int64, text string) error {
	return s.run(ctx, step{`UPDATE hlstats_gamewe_jobs SET result = ? WHERE id = ? AND status = ?`, []any{clip(text, 255), id, JobRunning}})
}

// FinishJob stores the end of a job (done or failed) and its result.
func (s *Store) FinishJob(ctx context.Context, id int64, status, result string, at time.Time) error {
	if len(result) > 1024 {
		result = result[:1021] + "..."
		for !utf8.ValidString(result) {
			result = result[:len(result)-4] + "..."
		}
	}
	return s.run(ctx, step{`UPDATE hlstats_gamewe_jobs SET status = ?, finished_at = ?, result = ? WHERE id = ?`, []any{status, at.Unix(), result, id}})
}

// JobKeep is how long finished jobs stay in hlstats_gamewe_jobs.
const JobKeep = 30 * 24 * time.Hour

// DeleteOldJobs removes finished jobs requested before t.
func (s *Store) DeleteOldJobs(ctx context.Context, before time.Time) error {
	return s.run(ctx, step{`DELETE FROM hlstats_gamewe_jobs WHERE requested_at < ? AND status IN (?, ?)`, []any{before.Unix(), JobDone, JobFailed}})
}

// CountOldJobs counts the finished jobs requested before t.
func (s *Store) CountOldJobs(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM hlstats_gamewe_jobs WHERE requested_at < ? AND status IN (?, ?)`, before.Unix(), JobDone, JobFailed).Scan(&n)
	return n, err
}

// Heartbeat writes the time to hlstats_Options gamewe_heartbeat.
func (s *Store) Heartbeat(ctx context.Context, at time.Time) error {
	return s.setOption(ctx, HeartbeatKey, strconv.FormatInt(at.Unix(), 10))
}

// SetVersion writes the collector's version to hlstats_Options
// gamewe_collector_version.
func (s *Store) SetVersion(ctx context.Context, v string) error {
	return s.setOption(ctx, VersionKey, v)
}

// SetOption writes one hlstats_Options row (see setOption).
func (s *Store) SetOption(ctx context.Context, key, v string) error {
	return s.setOption(ctx, key, v)
}

// setOption updates or inserts one hlstats_Options row. An update that
// keeps the value counts no row on MariaDB; the insert then skips the
// existing key.
func (s *Store) setOption(ctx context.Context, key, v string) error {
	res, err := s.exec(ctx, `UPDATE hlstats_Options SET value = ? WHERE keyname = ?`, v, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.exec(ctx, s.ignore(`INSERT INTO hlstats_Options (keyname, value) VALUES (?, ?)`), key, v)
	return err
}

// DeleteOption removes one hlstats_Options row.
func (s *Store) DeleteOption(ctx context.Context, key string) error {
	return s.run(ctx, step{`DELETE FROM hlstats_Options WHERE keyname = ?`, []any{key}})
}

// Plan keys: the running collector writes when its daily jobs run next, so
// the web admin can show it (the times are in config.yaml, not in the
// database). NextAwardsKey and NextInactiveKey hold a JobPlan as JSON,
// NextResetsKey the time of day and zone of the scheduled resets (a JobPlan
// without Next), NextResetKey(game) the Unix time the running period of a
// ranking's reset schedule ends.
const (
	NextAwardsKey   = "gamewe_next_awards"
	NextInactiveKey = "gamewe_next_inactive"
	NextResetsKey   = "gamewe_next_resets"
)

// NextResetKey is the hlstats_Options key with the end of the running
// period of game's reset schedule (Unix time).
func NextResetKey(game string) string { return "gamewe_next_reset_" + game }

// JobPlan is when a daily job of the collector runs: on or off in
// config.yaml, the time of day (HH:MM) in Zone (IANA name) and the next run
// (Unix time; 0 while off).
type JobPlan struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`
	Zone    string `json:"zone"`
	Next    int64  `json:"next,omitempty"`
}

// LastHeartbeat returns the collector's last heartbeat (zero if none).
func (s *Store) LastHeartbeat(ctx context.Context) (time.Time, error) {
	var v string
	err := s.queryRow(ctx, `SELECT value FROM hlstats_Options WHERE keyname = ?`, HeartbeatKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, nil
	}
	return time.Unix(n, 0), nil
}
