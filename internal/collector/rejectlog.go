// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"log/slog"
	"sync"
	"time"
)

// RejectRepeat is how often a source whose logs are still rejected is
// logged again (with the count since the last line).
const RejectRepeat = 5 * time.Minute

// rejectLog logs rejected log packets and posts: the first time per source
// and reason at once, then at most every RejectRepeat while they keep
// coming, so a wrong port or token stays visible in the log. Memory is
// bounded.
type rejectLog struct {
	log   *slog.Logger
	msg   string
	every time.Duration
	now   func() time.Time
	mu    sync.Mutex
	seen  map[rejectKey]*rejectCount
}

type rejectKey struct {
	src    string
	reason error
}

type rejectCount struct {
	total, since uint64    // all, and since the last line
	last         time.Time // last line logged
}

func newRejectLog(log *slog.Logger, msg string) *rejectLog {
	return &rejectLog{log: log, msg: msg, every: RejectRepeat, now: time.Now, seen: map[rejectKey]*rejectCount{}}
}

// note counts a rejection of src for reason. hint (nil: none) returns
// more key, value pairs for the line, such as how to fix it; it is only
// called when a line is logged.
func (rl *rejectLog) note(src string, reason error, hint func() []any) {
	now := rl.now()
	rl.mu.Lock()
	if len(rl.seen) > 10000 {
		rl.seen = map[rejectKey]*rejectCount{} // bound memory under a flood of sources
	}
	k := rejectKey{src, reason}
	c := rl.seen[k]
	if c == nil {
		c = &rejectCount{}
		rl.seen[k] = c
	}
	c.total++
	c.since++
	first := c.total == 1
	if !first && now.Sub(c.last) < rl.every {
		rl.mu.Unlock()
		return
	}
	total, since, last := c.total, c.since, c.last
	c.since, c.last = 0, now
	rl.mu.Unlock()
	args := []any{"source", src, "reason", reason, "count", total}
	if !first {
		args = append(args, "new", since, "since", now.Sub(last).Round(time.Second).String())
	}
	if hint != nil {
		args = append(args, hint()...)
	}
	rl.log.Warn(rl.msg, args...)
}
