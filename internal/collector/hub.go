// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

var (
	ErrUnknownSource = errors.New("log packet from a source that is not in hlstats_Servers")
	// ErrUnknownPort: the IP has a server in hlstats_Servers, but not on
	// this port (e.g. a second game server on the same host that has no
	// row yet). Rejected, never attributed to the server on the other port.
	ErrUnknownPort = errors.New("log packet from a known server IP but a port that is not in hlstats_Servers")
	ErrQueueFull   = errors.New("queue full, log line dropped")
)

// QueueSize is the number of lines buffered per server while the database
// is slow.
const QueueSize = 4096

// Hub routes log packets to one worker per game server in hlstats_Servers.
// Each worker owns a Processor and a queue and runs in its own goroutine,
// so servers are processed in parallel while the lines of one server stay
// in order. Reload re-reads hlstats_Servers and hlstats_Options and starts
// or stops workers for added or removed servers.
type Hub struct {
	store Store
	set   *settings.Live
	reply Replier
	log   *slog.Logger

	// LineLog receives every accepted log line as "[name ip:port] line\n"
	// while gamewe_log_lines is 1 (nil: never). 'gamewe run' sets it to
	// stderr, i.e. the journal. Set it before the first SetServers.
	LineLog io.Writer
	// Geo looks up player and server locations (nil: no GeoIP). Set it
	// before the first SetServers.
	Geo GeoLookup
	// Monitor are the RCON jobs per server (features in the config file);
	// none if all are off. Set it before the first SetServers.
	Monitor MonitorConfig
	// Secret is features.log_secret (default off): packets must carry the
	// server's hlstats_Servers.log_secret if one is stored. Set it before
	// the first SetServers.
	Secret SecretConfig
	// Source2Hits is features.cs2_hitgroups (Processor.Source2Hits). Set it
	// before the first SetServers.
	Source2Hits bool
	// Bans is features.sourcebans (Processor.Bans; nil: off). Set it
	// before the first SetServers.
	Bans BanNotifier
	// VAC is told every connect (Processor.VAC; nil: off). Set it before
	// the first SetServers.
	VAC VACNotifier

	mu      sync.RWMutex
	byID    map[int64]*worker
	exact   map[netip.AddrPort]*worker
	byIP    map[netip.Addr][]*worker
	wg      sync.WaitGroup
	closed  bool
	lastLog map[string]bool // reload messages already logged
}

type worker struct {
	id    int64
	addr  string
	srv   stats.Server
	queue chan packet
	proc  *Processor
	stop  context.CancelFunc // the RCON monitor
	gate  *secretGate        // features.log_secret
}

type packet struct {
	line   string
	at     time.Time
	update *stats.Server     // changed name / RCON password, applied in order
	tick   bool              // end idle visits (Processor.Tick at time at)
	status *Status           // RCON status polled at time at
	info   *stats.ServerInfo // A2S_INFO answer
	pause  *pause            // stop here (between two lines) until released
}

// pause stops a worker for a ranking reset: it drops the processor's
// in-memory players, closes paused and waits until resume is closed.
type pause struct {
	paused, resume chan struct{}
}

// TickInterval is how often idle players are checked.
const TickInterval = 30 * time.Second

func NewHub(store Store, set *settings.Live, reply Replier, log *slog.Logger) *Hub {
	return &Hub{
		store: store, set: set, reply: reply, log: log,
		byID: map[int64]*worker{}, exact: map[netip.AddrPort]*worker{}, byIP: map[netip.Addr][]*worker{},
		lastLog: map[string]bool{},
	}
}

// Reload reads hlstats_Options and hlstats_Servers and applies them. Rows
// that cannot be used are logged once.
func (h *Hub) Reload(ctx context.Context) error {
	opts, err := h.store.Options(ctx)
	if err != nil {
		return fmt.Errorf("read hlstats_Options: %w", err)
	}
	s, warnings := settings.FromOptions(opts)
	h.set.Set(s)
	servers, err := h.store.Servers(ctx)
	if err != nil {
		return fmt.Errorf("read hlstats_Servers: %w", err)
	}
	seen := map[string]bool{}
	for _, w := range warnings {
		seen[w] = true
	}
	var usable []stats.Server
	info := map[string]bool{}
	for _, srv := range servers {
		if srv.Warning != "" && srv.Err == nil {
			seen[srv.Warning] = true
		}
		if msg, isInfo := SecretNote(h.Secret.Mode, srv); msg != "" && srv.Err == nil {
			seen[msg], info[msg] = true, isInfo
		}
		if srv.Err != nil {
			seen[fmt.Sprintf("hlstats_Servers serverId %d is ignored: %v", srv.ID, srv.Err)] = true
			continue
		}
		usable = append(usable, srv)
	}
	for msg := range seen {
		switch {
		case h.lastLog[msg]:
		case info[msg]:
			h.log.Info(msg)
		default:
			h.log.Warn(msg)
		}
	}
	h.lastLog = seen
	if err := h.store.UpdateServerPlayers(ctx); err != nil {
		h.log.Warn("update hlstats_Servers.players", "err", err)
	}
	added, removed := h.SetServers(usable)
	if added > 0 || removed > 0 {
		h.log.Info("game servers loaded from hlstats_Servers", "active", h.Count(), "added", added, "removed", removed)
	}
	return nil
}

// RunReloader calls Reload every interval, and Tick every TickInterval,
// until ctx is done.
func (h *Hub) RunReloader(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	tick := time.NewTicker(TickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			h.Tick(now.UTC())
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := h.Reload(rctx); err != nil {
				h.log.Error("reload from database failed, keeping the current servers", "err", err)
			}
			cancel()
		}
	}
}

// SetServers makes the given servers the active ones. New servers get a
// worker; workers of servers that are gone, or whose address or game
// changed, are stopped (their players' visits are ended). If two rows have
// the same address and port, only the first is used.
func (h *Hub) SetServers(list []stats.Server) (added, removed int) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, 0
	}
	want := map[int64]stats.Server{}
	taken := map[netip.AddrPort]int64{}
	for _, srv := range list {
		if id, dup := taken[srv.AddrPort]; dup {
			h.log.Warn("hlstats_Servers has the same address twice; using the first row", "address", srv.AddrPort, "used", id, "ignored", srv.ID)
			continue
		}
		taken[srv.AddrPort] = srv.ID
		want[srv.ID] = srv
	}
	now := time.Now()
	grace := secretGrace(h.logEvery())
	var stop []*worker
	for id, w := range h.byID {
		srv, ok := want[id]
		if ok && srv.AddrPort == w.srv.AddrPort && srv.GameID == w.srv.GameID && srv.Game == w.srv.Game && srv.Engine == w.srv.Engine {
			h.applySecret(w, srv, now, grace)
			w.srv.LogSecret = srv.LogSecret
			if srv.Name != w.srv.Name || srv.RconPassword != w.srv.RconPassword || srv.MaxPlayers != w.srv.MaxPlayers {
				u := srv
				select {
				case w.queue <- packet{update: &u}:
					w.srv = srv
				default: // queue full: try again on the next reload
				}
			}
			continue
		}
		stop = append(stop, w)
		h.remove(w)
	}
	for id, srv := range want {
		if h.byID[id] != nil {
			continue
		}
		w := &worker{id: id, addr: srv.Address, srv: srv, queue: make(chan packet, QueueSize), proc: NewProcessor(h.store, h.set, srv, h.reply, h.log),
			gate: newSecretGate()}
		w.proc.Geo = h.Geo
		w.proc.Source2Hits = h.Source2Hits
		w.proc.Bans = h.Bans
		w.proc.VAC = h.VAC
		h.applySecret(w, srv, now, grace)
		if h.Monitor.Status || h.Monitor.LogAddress || h.Monitor.ServerInfo {
			ctx, cancel := context.WithCancel(context.Background())
			w.stop = cancel
			id := srv.ID
			send := func(pk packet) {
				h.mu.RLock()
				defer h.mu.RUnlock()
				if h.byID[id] == w { // not stopped
					select {
					case w.queue <- pk:
					default: // queue full: the next poll comes soon
					}
				}
			}
			deliver := func(st Status, at time.Time) {
				if !h.Monitor.ServerInfo {
					st.Info = stats.ServerInfo{}
				}
				send(packet{status: &st, at: at})
			}
			info := func(si stats.ServerInfo) { send(packet{info: &si}) }
			mlog := h.log.With("server", srv.Address, "server_id", srv.ID)
			go monitor(ctx, func() stats.Server { return h.server(id, srv) }, h.Monitor, deliver, info,
				func() time.Duration { return time.Duration(h.set.Get().RconTimeout) * time.Second }, mlog)
		}
		h.byID[id] = w
		h.exact[srv.AddrPort] = w
		h.byIP[srv.AddrPort.Addr()] = append(h.byIP[srv.AddrPort.Addr()], w)
		h.wg.Add(1)
		go h.run(w, srv.Address)
		added++
	}
	h.mu.Unlock()
	// Dispatch only sends while holding the read lock, so after removing
	// the workers from the maps nobody can send to their queues any more.
	for _, w := range stop {
		close(w.queue)
	}
	return added, len(stop)
}

// logEvery is features.log_address.interval.
func (h *Hub) logEvery() time.Duration {
	if h.Monitor.LogInterval > 0 {
		return h.Monitor.LogInterval
	}
	return DefaultLogAddressInterval
}

// applySecret hands the log secret of srv's row to w's gate. GoldSrc
// servers have no sv_logsecret, and the HTTP logs of Source 2 (CS2) carry
// none: never checked.
func (h *Hub) applySecret(w *worker, srv stats.Server, now time.Time, grace time.Duration) {
	secret := srv.LogSecret
	if h.Secret.Mode == SecretOff || srv.Engine == games.GoldSrc || srv.Engine == games.Source2 {
		secret = ""
	}
	w.gate.fromDB(secret, now, grace)
}

// server returns the current hlstats_Servers row of a worker (def if it
// is gone).
func (h *Hub) server(id int64, def stats.Server) stats.Server {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if w := h.byID[id]; w != nil {
		return w.srv
	}
	return def
}

// remove deletes w from the routing maps and stops its RCON monitor; h.mu
// must be held.
func (h *Hub) remove(w *worker) {
	if w.stop != nil {
		w.stop()
	}
	delete(h.byID, w.srv.ID)
	delete(h.exact, w.srv.AddrPort)
	ip := w.srv.AddrPort.Addr()
	ws := h.byIP[ip][:0]
	for _, o := range h.byIP[ip] {
		if o != w {
			ws = append(ws, o)
		}
	}
	if len(ws) == 0 {
		delete(h.byIP, ip)
	} else {
		h.byIP[ip] = ws
	}
}

// Tick asks every worker to end the visits of idle players. A worker whose
// queue is full skips this tick.
func (h *Hub) Tick(now time.Time) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, w := range h.byID {
		select {
		case w.queue <- packet{tick: true, at: now}:
		default:
		}
	}
}

// Count returns the number of active servers.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byID)
}

// run processes w's queue. It must not read w.srv: SetServers updates it.
func (h *Hub) run(w *worker, addr string) {
	defer h.wg.Done()
	for pk := range w.queue {
		if pk.pause != nil {
			w.proc.Discard()
			close(pk.pause.paused)
			<-pk.pause.resume
			continue
		}
		if pk.update != nil {
			w.proc.Update(*pk.update)
			continue
		}
		if pk.status != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := w.proc.Status(ctx, *pk.status, pk.at); err != nil {
				h.log.Error("applying rcon status failed", "server", addr, "err", err)
			}
			cancel()
			continue
		}
		if pk.info != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := w.proc.ServerInfo(ctx, *pk.info); err != nil {
				h.log.Error("storing the server info failed", "server", addr, "err", err)
			}
			cancel()
			continue
		}
		if pk.tick {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := w.proc.Tick(ctx, pk.at); err != nil {
				h.log.Error("ending idle visits failed", "server", addr, "err", err)
			}
			cancel()
			continue
		}
		if h.LineLog != nil && h.set.Get().LogLines {
			// One Write per line, so lines of different servers do not mix.
			h.LineLog.Write([]byte("[" + w.proc.Label() + "] " + Printable(pk.line) + "\n"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := w.proc.Handle(ctx, pk.line, pk.at); err != nil {
			h.log.Error("processing log line failed", "server", addr, "line", pk.line, "err", err)
		}
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	w.proc.Shutdown(ctx, time.Now().UTC())
	cancel()
}

// resolve finds the server for a packet source: only the exact ip:port.
// h.mu must be held.
func (h *Hub) resolve(src netip.AddrPort) *worker {
	return h.exact[netip.AddrPortFrom(src.Addr().Unmap(), src.Port())]
}

// Known reports whether src (ip:port) is a server of hlstats_Servers.
func (h *Hub) Known(src netip.AddrPort) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.resolve(src) != nil
}

// PortsAt lists the ports of the servers of hlstats_Servers at ip, in
// ascending order.
func (h *Hub) PortsAt(ip netip.Addr) []uint16 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var ports []uint16
	for _, w := range h.byIP[ip.Unmap()] {
		ports = append(ports, w.srv.AddrPort.Port())
	}
	slices.Sort(ports)
	return ports
}

// PauseGame stops the workers of every server of game between two lines
// and drops their players from memory (nothing written: the ranking is
// reset next). Lines arriving meanwhile wait in the queues (or are dropped
// when a queue is full). The returned function lets the workers go on;
// it must be called also after an error.
func (h *Hub) PauseGame(ctx context.Context, game string) (resume func(), servers []int64, err error) {
	release := make(chan struct{})
	var once sync.Once
	resume = func() { once.Do(func() { close(release) }) }
	h.mu.RLock()
	var waits []chan struct{}
	for _, w := range h.byID {
		if w.srv.Game != game {
			continue
		}
		pk := packet{pause: &pause{paused: make(chan struct{}), resume: release}}
		select {
		case w.queue <- pk:
		case <-ctx.Done():
			h.mu.RUnlock()
			resume()
			return resume, nil, ctx.Err()
		}
		waits = append(waits, pk.pause.paused)
		servers = append(servers, w.srv.ID)
	}
	h.mu.RUnlock()
	for _, c := range waits {
		select {
		case <-c:
		case <-ctx.Done():
			resume()
			return resume, nil, fmt.Errorf("pausing the servers of %s: %w", game, ctx.Err())
		}
	}
	return resume, servers, nil
}

// Dispatch checks a packet's source and queues its line for the server's
// worker. It returns why a packet was not accepted.
func (h *Hub) Dispatch(src netip.AddrPort, payload []byte, now time.Time) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	w := h.resolve(src)
	if w == nil {
		if len(h.byIP[src.Addr().Unmap()]) > 0 {
			return ErrUnknownPort
		}
		return ErrUnknownSource
	}
	if h.Secret.Mode == SecretOff {
		line, err := logparse.ParsePacket(payload, "")
		if err != nil {
			return err
		}
		return w.enqueue(packet{line: line, at: now})
	}
	line, secret, has, err := logparse.SplitPacket(payload)
	if err != nil {
		return err
	}
	return h.admit(w, line, secret, has, now)
}

// Close stops accepting lines, waits until every queued line is processed
// and ends the visits of the players still online.
func (h *Hub) Close(ctx context.Context) {
	h.mu.Lock()
	h.closed = true
	var all []*worker
	for _, w := range h.byID {
		all = append(all, w)
		h.remove(w)
	}
	h.mu.Unlock()
	for _, w := range all {
		close(w.queue)
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		h.log.Warn("shutdown timed out before all queued lines were processed")
	}
}

// Printable makes a log line safe for one journal line: control characters
// (newlines, ANSI escapes, ...) become \xNN and invalid UTF-8 is replaced.
func Printable(s string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		if r < 0x20 || r == 0x7f {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
