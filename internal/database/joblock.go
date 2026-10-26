// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrJobBusy: another job held the job lock for the whole wait.
var ErrJobBusy = errors.New("another job (a ranking reset, the move of inactive players or the daily awards) is running on this database")

// JobLock takes the job lock of the database: one of the jobs that change
// many rows (ranking reset, move of inactive players, daily awards) at a
// time, in the running collector and in the gamewe commands. It waits up
// to wait (calling waiting once when it has to wait). The lock is held on
// its own connection until release.
func JobLock(ctx context.Context, db *DB, wait time.Duration, waiting func()) (release func(), err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	unlock, err := namedLock(ctx, conn, db.Postgres(), jobLockPrefix, jobLockKey, wait, waiting)
	if err != nil {
		conn.Close()
		if errors.Is(err, errLockBusy) {
			return nil, fmt.Errorf("%w for more than %s", ErrJobBusy, wait)
		}
		return nil, fmt.Errorf("job lock: %w", err)
	}
	return func() {
		unlock()
		conn.Close()
	}, nil
}
