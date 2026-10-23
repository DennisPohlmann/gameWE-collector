// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"time"
)

// testLockName is the database lock of the integration tests.
const (
	testLockName = "gamewe_integration_tests"
	testLockKey  = 4711 // pg_advisory_lock key
)

// LockForTest waits until no other integration test (of any package: go
// test runs packages at the same time) works on the test database, and
// returns the function that releases it. The collector's tests wipe all
// tables, so two packages on one database would see each other's rows
// vanish.
func LockForTest(ctx context.Context, db *DB, wait time.Duration) (func(), error) {
	conn, err := db.Conn(ctx) // the lock belongs to this connection
	if err != nil {
		return nil, err
	}
	if db.Postgres() {
		ctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, testLockKey); err != nil {
			conn.Close()
			return nil, fmt.Errorf("test lock: %w", err)
		}
		return func() {
			conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, testLockKey)
			conn.Close()
		}, nil
	}
	var got int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, ?)`, testLockName, int(wait.Seconds())).Scan(&got); err != nil || got != 1 {
		conn.Close()
		return nil, fmt.Errorf("test lock: %v (got %d)", err, got)
	}
	return func() {
		conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK(?)`, testLockName)
		conn.Close()
	}, nil
}
