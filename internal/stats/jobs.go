// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
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
// (features.inactive_players; no game, no mode).
const (
	JobReset    = "reset"
	JobInactive = "inactive"
)

// Job is a row of hlstats_gamewe_jobs.
type Job struct {
	ID                    int64
	Type, Game, Mode      string
	Status, RequestedBy   string
	RequestedAt           int64
	StartedAt, FinishedAt int64 // 0 if not yet
	Result                string
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

const jobCols = `id, job_type, game, mode, status, requested_by, requested_at, COALESCE(started_at, 0), COALESCE(finished_at, 0), result`

func scanJobs(rows *sql.Rows) ([]Job, error) {
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Type, &j.Game, &j.Mode, &j.Status, &j.RequestedBy, &j.RequestedAt, &j.StartedAt, &j.FinishedAt, &j.Result); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobsByStatus returns the jobs with status, oldest first.
func (s *Store) JobsByStatus(ctx context.Context, status string) ([]Job, error) {
	rows, err := s.query(ctx, `SELECT `+jobCols+` FROM hlstats_gamewe_jobs WHERE status = ? ORDER BY id`, status)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// RecentJobs returns the newest n jobs, newest first.
func (s *Store) RecentJobs(ctx context.Context, n int) ([]Job, error) {
	rows, err := s.query(ctx, `SELECT `+jobCols+` FROM hlstats_gamewe_jobs ORDER BY id DESC LIMIT `+strconv.Itoa(max(1, n)))
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// Job returns one job.
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	rows, err := s.query(ctx, `SELECT `+jobCols+` FROM hlstats_gamewe_jobs WHERE id = ?`, id)
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

// AddJob queues a job and returns its id.
func (s *Store) AddJob(ctx context.Context, typ, game, mode, by string, at time.Time) (int64, error) {
	return s.insertID(ctx, `INSERT INTO hlstats_gamewe_jobs (job_type, game, mode, status, requested_by, requested_at, result)
		VALUES (?, ?, ?, ?, ?, ?, '')`, "id", typ, clip(game, 32), clip(mode, 16), JobPending, clip(by, 64), at.Unix())
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
