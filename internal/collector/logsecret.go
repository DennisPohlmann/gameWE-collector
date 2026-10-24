// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"strconv"
	"sync"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/stats"
)

// SecretMode is features.log_secret.enabled.
type SecretMode int

const (
	SecretOff  SecretMode = iota // packets are not checked (the secret is cut off)
	SecretOn                     // packets must carry hlstats_Servers.log_secret
	SecretAuto                   // as SecretOn; the collector sets and rotates sv_logsecret over RCON
)

func (m SecretMode) String() string {
	switch m {
	case SecretOn:
		return "true"
	case SecretAuto:
		return "auto"
	}
	return "false"
}

// SecretConfig is features.log_secret.
type SecretConfig struct {
	Mode     SecretMode
	Interval time.Duration // auto: a new secret this often (default DefaultSecretInterval)
}

// DefaultSecretInterval is features.log_secret.interval by default.
const DefaultSecretInterval = 24 * time.Hour

// The quarantine of one server (auto): packets without the right secret
// wait there until RCON tells whether the server lost its secret (restart)
// or the packets are forged (variables for tests).
var (
	quarantineLines  = 2000             // at most this many lines
	quarantineBytes  = 1 << 20          // and this many bytes
	quarantineTime   = 30 * time.Second // then valid lines go on, the others are dropped
	secretCheckEvery = 10 * time.Second // sv_logsecret asked at most this often per server
	restartGrace     = 5 * time.Second  // after a restart: packets without the secret still accepted
)

// ErrQuarantineFull: a packet without the right secret while the
// server's quarantine is full.
var ErrQuarantineFull = errors.New("missing or wrong log secret, quarantine full")

// errSecretUnsupported: the game server does not know sv_logsecret.
var errSecretUnsupported = errors.New("the game server has no sv_logsecret: its log secret is not managed")

// secretGrace is how long the previous secret is still accepted after a
// change: two log address intervals, at least two minutes.
func secretGrace(logEvery time.Duration) time.Duration {
	return max(2*logEvery, 2*time.Minute)
}

// secretGate decides per server which log packets are accepted (Hub.admit)
// and holds the quarantine. The secret values are never logged.
type secretGate struct {
	check chan struct{} // cap 1: asks the RCON monitor for sv_logsecret

	mu        sync.Mutex
	current   string    // the expected secret, "" = not checked
	changed   time.Time // when current was set by the collector (log_secret_changed)
	db        string    // hlstats_Servers.log_secret at the last reload
	old       string    // the previous secret, accepted until oldUntil
	oldUntil  time.Time
	pending   string    // being set over RCON (accepted already)
	openUntil time.Time // after a restart: any packet accepted until then
	verify    bool      // quarantine instead of rejecting (auto, Source, rcon_password)

	q       []qline
	qBytes  int
	qTimer  *time.Timer
	dropped int       // lines dropped since the last warning
	warned  time.Time // last warning about dropped lines
}

type qline struct {
	line string
	at   time.Time
	ok   bool // carried an accepted secret (queued behind the others to keep the order)
}

func newSecretGate() *secretGate { return &secretGate{check: make(chan struct{}, 1)} }

// fromDB applies hlstats_Servers.log_secret of a reload. Only a change of
// the stored value counts: a reload that read the row before the collector
// stored a new secret keeps the new one.
func (g *secretGate) fromDB(secret string, changed time.Time, verify bool, now time.Time, grace time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.verify = verify
	if secret == g.db {
		return
	}
	g.db, g.changed = secret, changed
	if secret == g.current {
		return
	}
	if g.current != "" {
		g.old, g.oldUntil = g.current, now.Add(grace)
	}
	g.current = secret
}

// accepts reports whether a packet's secret is one of the accepted ones;
// g.mu must be held.
func (g *secretGate) accepts(secret string, has bool, now time.Time) bool {
	if g.current == "" || now.Before(g.openUntil) {
		return true
	}
	return has && g.known(secret, now)
}

// known reports whether s is the current, pending or (in the grace time)
// previous secret; g.mu must be held.
func (g *secretGate) known(s string, now time.Time) bool {
	ok := logparse.SecretEqual(s, g.current)
	if g.pending != "" && logparse.SecretEqual(s, g.pending) {
		ok = true
	}
	if g.old != "" && now.Before(g.oldUntil) && logparse.SecretEqual(s, g.old) {
		ok = true
	}
	return ok
}

func (g *secretGate) snapshot() (current string, changed time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current, g.changed
}

func (g *secretGate) setPending(s string) {
	g.mu.Lock()
	g.pending = s
	g.mu.Unlock()
}

// promote makes s the current secret (stored in the database at now).
func (g *secretGate) promote(s string, now time.Time, grace time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current != "" && g.current != s {
		g.old, g.oldUntil = g.current, now.Add(grace)
	}
	g.current, g.changed, g.pending = s, now, ""
}

func (g *secretGate) quarantined() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.q) > 0
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
	g := w.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	ok := g.accepts(secret, has, now)
	if len(g.q) == 0 {
		if ok {
			return w.enqueue(packet{line: line, at: now})
		}
		if !g.verify {
			return logparse.ErrBadSecret
		}
	}
	if len(g.q) >= quarantineLines || g.qBytes+len(line) > quarantineBytes {
		if !ok {
			g.dropped++
			return ErrQuarantineFull
		}
		g.release(w, false)
		return w.enqueue(packet{line: line, at: now})
	}
	if len(g.q) == 0 {
		g.qTimer = time.AfterFunc(quarantineTime, func() { h.endQuarantine(w, false, "no answer over RCON") })
		select {
		case g.check <- struct{}{}:
		default:
		}
	}
	g.q = append(g.q, qline{line: line, at: now, ok: ok})
	g.qBytes += len(line)
	return nil
}

// release empties the quarantine in order: every line if all, otherwise
// only the lines with an accepted secret. g.mu and h.mu (read) must be held.
func (g *secretGate) release(w *worker, all bool) {
	for _, l := range g.q {
		if !l.ok && !all {
			g.dropped++
			continue
		}
		if w.enqueue(packet{line: l.line, at: l.at}) != nil {
			g.dropped++
		}
	}
	g.q, g.qBytes = nil, 0
	if g.qTimer != nil {
		g.qTimer.Stop()
		g.qTimer = nil
	}
}

// endQuarantine ends w's quarantine: genuine (the server lost its secret,
// e.g. restarted) processes every line and accepts packets without the
// secret for restartGrace; otherwise only lines with the secret go on.
func (h *Hub) endQuarantine(w *worker, genuine bool, why string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.byID[w.id] != w {
		return // stopped
	}
	g := w.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if genuine {
		g.openUntil = time.Now().Add(restartGrace)
	}
	if len(g.q) > 0 {
		g.release(w, genuine)
	}
	// Dropped lines are reported at most once a minute per server.
	if g.dropped > 0 && time.Since(g.warned) >= time.Minute {
		h.log.Warn("log packets without the right log secret dropped", "server", w.addr, "server_id", w.id, "lines", g.dropped, "why", why)
		g.dropped, g.warned = 0, time.Now()
	}
}

// secretJob is the auto mode of features.log_secret in one server's RCON
// monitor.
type secretJob struct {
	gate     *secretGate
	interval time.Duration // rotation
	grace    time.Duration // the previous secret is accepted this long
	store    func(ctx context.Context, secret string, changed time.Time) error
	verdict  func(genuine bool, why string) // ends the quarantine
	log      *slog.Logger
}

// "sv_logsecret" = "123" ( def. "0" ) / sv_logsecret = 123
var reLogSecret = regexp.MustCompile(`(?m)^\s*"?sv_logsecret"?\s*=\s*"?([^"\s]*)"?`)

// querySecret asks the game server for sv_logsecret; "" if none (0). The
// answer is never logged: it contains the secret.
func querySecret(c rcon.Conn) (string, error) {
	out, err := c.Exec("sv_logsecret")
	if err != nil {
		return "", err
	}
	m := reLogSecret.FindStringSubmatch(out)
	if m == nil {
		return "", errSecretUnsupported
	}
	if m[1] == "0" {
		return "", nil
	}
	return m[1], nil
}

// ApplySecret sets sv_logsecret on a game server (gamewe server set
// -logsecret).
func ApplySecret(c rcon.Conn, s string) error { return setSecret(c, s) }

// setSecret sets sv_logsecret (0: none). s is checked, so it is safe in
// the command.
func setSecret(c rcon.Conn, s string) error {
	if err := logparse.CheckSecret(s); err != nil {
		return err
	}
	if s == "" {
		s = "0"
	}
	_, err := c.Exec("sv_logsecret " + s)
	return err
}

// RandomSecret returns a new log secret from crypto/rand: a number
// 1..logparse.MaxSecret.
func RandomSecret() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(logparse.MaxSecret))
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n.Int64()+1, 10), nil
}

// manage runs every log address interval: it gives a server without a
// secret one, rotates it after interval, and sets it again where the game
// server has another one (after a restart).
func (j *secretJob) manage(c rcon.Conn, s stats.Server) error {
	if s.Engine == games.GoldSrc {
		return nil
	}
	have, err := querySecret(c)
	if err != nil {
		return err
	}
	cur, changed := j.gate.snapshot()
	now := time.Now()
	if cur == "" || now.Sub(changed) >= j.interval {
		return j.rotate(c, s, cur, now)
	}
	if have != cur {
		if err := setSecret(c, cur); err != nil {
			return fmt.Errorf("sv_logsecret: %w", err)
		}
		j.log.Info("sv_logsecret set again: the game server had another one (restart?)")
	}
	return nil
}

// rotate sets a new secret: accepted first, then set over RCON, then
// stored. If a step fails, the game server gets cur back and nothing is
// stored.
func (j *secretJob) rotate(c rcon.Conn, s stats.Server, cur string, now time.Time) error {
	next, err := RandomSecret()
	if err != nil {
		return err
	}
	j.gate.setPending(next)
	undo := func() {
		setSecret(c, cur)
		j.gate.setPending("")
	}
	if err := setSecret(c, next); err != nil {
		j.gate.setPending("")
		return fmt.Errorf("sv_logsecret: %w", err)
	}
	if got, err := querySecret(c); err != nil || got != next {
		undo()
		if err == nil {
			err = errors.New("the game server did not take the new value")
		}
		return fmt.Errorf("sv_logsecret: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := j.store(ctx, next, now); err != nil {
		undo()
		return fmt.Errorf("store hlstats_Servers.log_secret: %w", err)
	}
	j.gate.promote(next, now, j.grace)
	if cur == "" {
		j.log.Info("log secret set (sv_logsecret), log packets are checked now")
	} else {
		j.log.Info("log secret rotated (sv_logsecret)", "next_in", j.interval.String())
	}
	return nil
}

// check answers the quarantine: does the game server still have its
// secret (then the packets without it are forged) or not (restart: the
// secret is set again and the lines are processed)?
func (j *secretJob) check(c rcon.Conn) error {
	if c == nil || !j.gate.quarantined() {
		return nil
	}
	have, err := querySecret(c)
	if err != nil {
		return err // the quarantine runs out
	}
	j.gate.mu.Lock()
	forged := have != "" && j.gate.known(have, time.Now())
	cur := j.gate.current
	j.gate.mu.Unlock()
	if forged {
		j.verdict(false, "the game server has the secret: forged packets")
		return nil
	}
	err = setSecret(c, cur)
	j.verdict(true, "game server without its secret (restart)")
	if err != nil {
		return fmt.Errorf("sv_logsecret: %w", err)
	}
	j.log.Info("the game server sent its log without the log secret (restart?): lines kept, sv_logsecret set again")
	return nil
}

// SecretNote is what the log and gamewe check say about a server's log
// secret ("" if nothing); info is true for a note, false for a warning.
func SecretNote(mode SecretMode, s stats.Server) (msg string, info bool) {
	switch {
	case mode == SecretOff:
		return "", false
	case s.Engine == games.GoldSrc:
		return fmt.Sprintf("log_secret %s: server %d %s is GoldSrc (no sv_logsecret), its log packets are not checked", mode, s.ID, s.Address), true
	case s.LogSecret != "":
		return "", false
	case mode == SecretOn:
		return fmt.Sprintf("log_secret enabled, server %d %s has no secret: its log packets are not checked (gamewe server set -logsecret)", s.ID, s.Address), false
	case s.RconPassword == "":
		return fmt.Sprintf("log_secret auto, server %d %s has no secret and no rcon_password: its log packets are not checked", s.ID, s.Address), false
	}
	return "", false // auto: the collector sets one
}
