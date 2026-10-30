// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"fmt"
	"sync"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/stats"
)

// SecretMode is features.log_secret.enabled.
type SecretMode int

const (
	SecretOff SecretMode = iota // packets are not checked (the secret is cut off)
	SecretOn                    // packets must carry hlstats_Servers.log_secret, if one is stored
)

func (m SecretMode) String() string {
	if m == SecretOn {
		return "true"
	}
	return "false"
}

// SecretConfig is features.log_secret. The secret of a server is set by
// hand (gamewe server set -logsecret or the web admin) and must match
// sv_logsecret in the game server's server.cfg; the collector never sets
// or changes it.
type SecretConfig struct {
	Mode SecretMode
}

// secretGrace is how long the previous secret is still accepted after a
// change in hlstats_Servers: two log address intervals, at least two
// minutes, so the game server can be given the new value in that time.
func secretGrace(logEvery time.Duration) time.Duration {
	return max(2*logEvery, 2*time.Minute)
}

// secretGate decides per server which log packets are accepted (Hub.admit).
// The secret values are never logged.
type secretGate struct {
	mu       sync.Mutex
	current  string // the expected secret, "" = not checked
	db       string // hlstats_Servers.log_secret at the last reload
	old      string // the previous secret, accepted until oldUntil
	oldUntil time.Time
}

func newSecretGate() *secretGate { return &secretGate{} }

// fromDB applies hlstats_Servers.log_secret of a reload. Only a change of
// the stored value counts.
func (g *secretGate) fromDB(secret string, now time.Time, grace time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if secret == g.db {
		return
	}
	g.db = secret
	if g.current != "" {
		g.old, g.oldUntil = g.current, now.Add(grace)
	}
	g.current = secret
}

// accepts reports whether a packet's secret is accepted: always while no
// secret is stored, otherwise only the current one or (in the grace time)
// the previous one.
func (g *secretGate) accepts(secret string, has bool, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == "" {
		return true
	}
	if !has {
		return false
	}
	ok := logparse.SecretEqual(secret, g.current)
	if g.old != "" && now.Before(g.oldUntil) && logparse.SecretEqual(secret, g.old) {
		ok = true
	}
	return ok
}

// enqueue hands a line to the worker without blocking.
func (w *worker) enqueue(pk packet) error {
	select {
	case w.queue <- pk:
		return nil
	default:
		return ErrQueueFull
	}
}

// admit queues a line whose source is w's server, after checking the
// packet's log secret. h.mu (read) must be held.
func (h *Hub) admit(w *worker, line, secret string, has bool, now time.Time) error {
	if !w.gate.accepts(secret, has, now) {
		return logparse.ErrBadSecret
	}
	return w.enqueue(packet{line: line, at: now})
}

// SecretNote is what the log and gamewe check say about a server's log
// secret ("" if nothing); info is true for a note, false for a warning.
// GoldSrc and Source 2 (CS2) servers send no log secret, so they only get
// a note if one is stored for them anyway: it is ignored.
func SecretNote(mode SecretMode, s stats.Server) (msg string, info bool) {
	switch {
	case mode == SecretOff:
		return "", false
	case s.Engine == games.GoldSrc && s.LogSecret != "":
		return fmt.Sprintf("log_secret: server %d %s is GoldSrc (no sv_logsecret), its stored log secret is ignored (remove it with gamewe server set -logsecret \"\" or in the web admin)", s.ID, s.Address), true
	case s.Engine == games.Source2 && s.LogSecret != "":
		return fmt.Sprintf("log_secret: server %d %s is Source 2 (CS2), its HTTP logs carry no log secret, so the stored one is ignored (remove it with gamewe server set -logsecret \"\" or in the web admin; listen.http.token protects the HTTP input)", s.ID, s.Address), true
	case s.Engine == games.GoldSrc || s.Engine == games.Source2:
		return "", false
	case s.LogSecret == "":
		return fmt.Sprintf("log_secret: server %d %s has no log secret, all its log packets are accepted (set one with gamewe server set -logsecret or in the web admin, and the same sv_logsecret in server.cfg)", s.ID, s.Address), true
	}
	return "", false
}
