// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// Replier delivers a console command to the game server it is given
// (RCON at the server's address and port). Implementations must be safe
// for concurrent use.
type Replier interface {
	Send(srv stats.Server, cmd string)
}

// RconReplier keeps one RCON connection and one send goroutine per game
// server, so replies always go to the server the command came from and a
// slow or unreachable server never delays the others or log processing.
type RconReplier struct {
	ctx    context.Context
	set    *settings.Live
	log    *slog.Logger
	mu     sync.Mutex
	queues map[string]chan rconJob // by address
}

type rconJob struct {
	password, cmd string
	engine        games.Engine
}

func NewRconReplier(ctx context.Context, set *settings.Live, log *slog.Logger) *RconReplier {
	return &RconReplier{ctx: ctx, set: set, log: log, queues: map[string]chan rconJob{}}
}

func (r *RconReplier) Send(srv stats.Server, cmd string) {
	addr := srv.AddrPort.String()
	r.mu.Lock()
	q := r.queues[addr]
	if q == nil {
		q = make(chan rconJob, 64)
		r.queues[addr] = q
		go r.loop(addr, q)
	}
	r.mu.Unlock()
	select {
	case q <- rconJob{srv.RconPassword, cmd, srv.Engine}:
	default:
		r.log.Warn("rcon queue full, reply dropped", "server", addr)
	}
}

func (r *RconReplier) loop(addr string, q chan rconJob) {
	var c rcon.Conn
	var key connKey
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	for {
		select {
		case <-r.ctx.Done():
			return
		case j := <-q:
			timeout := time.Duration(r.set.Get().RconTimeout) * time.Second
			if k := (connKey{addr, j.password, timeout, j.engine}); c == nil || k != key {
				if c != nil {
					c.Close()
				}
				c, key = k.dial(), k
			}
			if _, err := c.Exec(j.cmd); err != nil {
				r.log.Warn("rcon reply failed", "server", addr, "err", err)
			} else {
				r.log.Debug("rcon reply sent", "server", addr, "cmd", j.cmd)
			}
		}
	}
}

// PrintReplier writes commands to W instead of sending them (simulate).
type PrintReplier struct {
	W  io.Writer
	mu sync.Mutex
}

func (p *PrintReplier) Send(srv stats.Server, cmd string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.W, "  rcon %s > %s\n", srv.Address, cmd)
}
