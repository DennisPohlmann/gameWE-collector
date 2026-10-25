// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"gamewe-collector/internal/config"
)

// A running collector (`gamewe run`) holds the collector lock of its
// database on a connection of its own for its whole life: GET_LOCK on
// MariaDB/MySQL (named per database), a session advisory lock on
// PostgreSQL (per database anyway). The database releases it when the
// process ends or the connection breaks, so it also tells a collector on
// another host or in a container. Migrate refuses while another session
// holds it: the collector writes into the tables a migration changes.
const (
	runLockPrefix = "gamewe_collector."
	runLockKey    = 0x67616d6577650002 // pg_advisory_lock, per database
)

// ErrCollectorRunning is returned by Migrate while a collector holds the
// collector lock of the database.
var ErrCollectorRunning = errors.New("a collector is running on this database (it holds the collector lock): stop it first (systemctl stop gamewe-collector, docker compose stop collector), then migrate")

// tryRunLock takes the collector lock on conn without waiting.
func tryRunLock(ctx context.Context, conn *sql.Conn, postgres bool) (bool, error) {
	if postgres {
		var ok bool
		err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, int64(runLockKey)).Scan(&ok)
		return ok, err
	}
	var got sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(LEFT(CONCAT(?, DATABASE()), 64), 0)`, runLockPrefix).Scan(&got)
	return got.Valid && got.Int64 == 1, err
}

// holdsRunLock reports whether conn (still) holds the collector lock.
func holdsRunLock(ctx context.Context, conn *sql.Conn, postgres bool) (bool, error) {
	var ok bool
	if postgres {
		err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()
			AND granted AND classid::bigint = $1 AND objid::bigint = $2 AND objsubid = 1)`, int64(runLockKey>>32), int64(runLockKey&0xffffffff)).Scan(&ok)
		return ok, err
	}
	err := conn.QueryRowContext(ctx, `SELECT COALESCE(IS_USED_LOCK(LEFT(CONCAT(?, DATABASE()), 64)) = CONNECTION_ID(), 0)`, runLockPrefix).Scan(&ok)
	return ok, err
}

// CollectorRunning reports whether a collector holds the collector lock of
// the database. Collectors up to 0.7.1 hold none: their heartbeat
// (hlstats_Options gamewe_heartbeat) tells them.
func CollectorRunning(ctx context.Context, db *DB) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if db.Postgres() {
		got, err := tryRunLock(ctx, conn, true)
		if err != nil || !got {
			return !got && err == nil, err
		}
		_, err = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, int64(runLockKey))
		return false, err
	}
	var id sql.NullInt64
	err = conn.QueryRowContext(ctx, `SELECT IS_USED_LOCK(LEFT(CONCAT(?, DATABASE()), 64))`, runLockPrefix).Scan(&id)
	return id.Valid, err
}

// HoldCollectorLock holds the collector lock of the database of cfg until
// ctx ends, on a connection pool of its own (one connection besides
// database.max_open_conns). It checks the lock every interval and takes it
// again after the connection broke. A lock another collector holds is
// logged once and tried again.
func HoldCollectorLock(ctx context.Context, cfg config.Database, interval time.Duration, log *slog.Logger) error {
	cfg.MaxOpenConns = 1
	db, err := Open(cfg)
	if err != nil {
		return err
	}
	db.SetMaxIdleConns(0) // a closed Conn ends its session (and the lock)
	go func() {
		defer db.Close()
		var conn *sql.Conn
		state := "" // "held", "other", "error": logged when it changes
		report := func(s string, err error) {
			if s == state {
				return
			}
			state = s
			switch s {
			case "held":
				log.Info("collector lock held: gamewe db migrate, reset and drop refuse while this collector runs")
			case "other":
				log.Warn("another collector holds the collector lock of this database (two collectors on one database); trying again")
			case "error":
				log.Warn("collector lock not held, trying again", "err", Explain(err))
			}
		}
		check := func() {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if conn != nil {
				if ok, err := holdsRunLock(cctx, conn, db.Postgres()); err == nil && ok {
					report("held", nil)
					return
				}
				conn.Close() // broken, or the lock is gone: start again
				conn = nil
			}
			c, err := db.Conn(cctx)
			if err != nil {
				report("error", err)
				return
			}
			got, err := tryRunLock(cctx, c, db.Postgres())
			switch {
			case err != nil:
				c.Close()
				report("error", err)
			case !got:
				c.Close()
				report("other", nil)
			default:
				conn = c
				report("held", nil)
			}
		}
		check()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				if conn != nil {
					conn.Close() // ends the session: the database releases the lock
				}
				return
			case <-t.C:
				check()
			}
		}
	}()
	return nil
}
