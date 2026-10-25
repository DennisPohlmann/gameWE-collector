// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamewe-collector/internal/games"
)

// ErrNotSource2: an HTTP log post for a server of hlstats_Servers that is
// not a Source 2 game. Only Counter-Strike 2 logs over HTTP.
var ErrNotSource2 = errors.New("HTTP log post for a server that is not Source 2 (CS2); other games log over UDP")

// MaxHTTPLine is the longest log line taken from an HTTP post; longer
// lines are dropped.
const MaxHTTPLine = 8 << 10

// HTTPConfig is the HTTP input (listen.http).
type HTTPConfig struct {
	Addr                string
	Token               string // secret first path segment ("": none)
	MaxBody             int64
	ReadTimeout         time.Duration
	MaxConnections      int
	MaxConnectionsPerIP int
	TrustedProxies      []netip.Prefix // senders whose X-Forwarded-For is used
}

// CheckHTTP reports why HTTP logs claiming to come from src (the sender's
// IP and the game port of the URL) are not accepted (nil: accepted).
func (h *Hub) CheckHTTP(src netip.AddrPort) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, err := h.httpWorker(src)
	return err
}

// httpWorker returns src's worker if it may send HTTP logs; h.mu must be
// held.
func (h *Hub) httpWorker(src netip.AddrPort) (*worker, error) {
	w := h.resolve(src)
	if w == nil {
		if len(h.byIP[src.Addr().Unmap()]) > 0 {
			return nil, ErrUnknownPort
		}
		return nil, ErrUnknownSource
	}
	if w.srv.Engine != games.Source2 {
		return nil, ErrNotSource2
	}
	return w, nil
}

// DispatchLines queues the log lines of one HTTP post for src's worker.
// The lines carry no log secret (CS2 sends none). It stops at the first
// problem and returns how many lines were queued.
func (h *Hub) DispatchLines(src netip.AddrPort, lines []string, now time.Time) (int, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	w, err := h.httpWorker(src)
	if err != nil {
		return 0, err
	}
	for i, line := range lines {
		if err := w.enqueue(packet{line: line, at: now}); err != nil {
			return i, err
		}
	}
	return len(lines), nil
}

// ServeHTTP receives the HTTP logs of Counter-Strike 2 (logaddress_add_http
// "http://<collector>/<game port>", or ".../<token>/<game port>") on
// cfg.Addr and hands them to the hub until ctx is done. ready (if not nil)
// is called with the bound address once the socket is open.
func ServeHTTP(ctx context.Context, cfg HTTPConfig, hub *Hub, log *slog.Logger, ready func(net.Addr)) error {
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	rl := newRejectLog(log)
	lim := newConnLimit(ln, cfg.MaxConnections, cfg.MaxConnectionsPerIP, rl)
	srv := &http.Server{
		Handler:           newHTTPHandler(cfg, hub, rl),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.ReadTimeout + 5*time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		// Broken requests (port scans, TLS hellos) are not worth a warning.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	log.Info("listening for game server logs", "http", ln.Addr().String(), "token", cfg.Token != "")
	if ready != nil {
		ready(ln.Addr())
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	err = srv.Serve(lim)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type httpHandler struct {
	cfg HTTPConfig
	hub *Hub
	rl  *rejectLog
	now func() time.Time
}

func newHTTPHandler(cfg HTTPConfig, hub *Hub, rl *rejectLog) *httpHandler {
	return &httpHandler{cfg: cfg, hub: hub, rl: rl, now: func() time.Time { return time.Now().UTC() }}
}

var (
	errHTTPMethod = errors.New("HTTP method is not POST")
	errHTTPPath   = errors.New("HTTP path is not /<game port> (or the token is wrong)")
	errHTTPBody   = errors.New("HTTP body larger than listen.http.max_body")
	errHTTPRead   = errors.New("HTTP body could not be read")
)

func (hh *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := hh.sender(r)
	if r.Method != http.MethodPost {
		hh.rl.note(ip.String(), errHTTPMethod)
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	port, ok := hh.port(r.URL.Path)
	if !ok || !ip.IsValid() {
		hh.rl.note(ip.String(), errHTTPPath)
		http.NotFound(w, r)
		return
	}
	src := netip.AddrPortFrom(ip, port)
	// Check the sender before reading the body: strangers cost no memory.
	if err := hh.hub.CheckHTTP(src); err != nil {
		hh.rl.note(src.String(), err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hh.cfg.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			hh.rl.note(src.String(), errHTTPBody)
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		hh.rl.note(src.String(), errHTTPRead)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	lines := splitHTTPLines(string(body))
	if _, err := hh.hub.DispatchLines(src, lines, hh.now()); err != nil {
		// Like a lost UDP packet: answered with 200 so the server does not
		// post the same lines again.
		hh.rl.note(src.String(), err)
	}
	w.WriteHeader(http.StatusOK)
}

// port reads the game port from the path: /<port>, or /<token>/<port>
// when a token is set (compared in constant time).
func (hh *httpHandler) port(path string) (uint16, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if hh.cfg.Token != "" {
		if len(parts) != 2 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(hh.cfg.Token)) != 1 {
			return 0, false
		}
		parts = parts[1:]
	}
	if len(parts) != 1 || len(parts[0]) > 5 {
		return 0, false
	}
	n, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil || n == 0 || parts[0][0] == '+' {
		return 0, false
	}
	return uint16(n), true
}

// sender is the IP that posted the request: the TCP peer, or for a trusted
// proxy the right-most address of X-Forwarded-For that is not a trusted
// proxy itself.
func (hh *httpHandler) sender(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := peer.Addr().Unmap()
	if !hh.trusted(ip) {
		return ip
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		a, err := netip.ParseAddr(h)
		if err != nil {
			return netip.Addr{} // garbage from the proxy: refuse
		}
		a = a.Unmap()
		if !hh.trusted(a) {
			return a
		}
		ip = a
	}
	return ip
}

func (hh *httpHandler) trusted(ip netip.Addr) bool {
	for _, p := range hh.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// splitHTTPLines splits a CS2 log post into lines; empty lines and lines
// longer than MaxHTTPLine are dropped.
func splitHTTPLines(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimRight(l, "\r\x00 ")
		if l == "" || len(l) > MaxHTTPLine {
			continue
		}
		out = append(out, l)
	}
	return out
}

// rejectLog logs rejected requests the first time per source and reason,
// then every 1000th time, with bounded memory.
type rejectLog struct {
	log    *slog.Logger
	mu     sync.Mutex
	counts map[rejectKey]uint64
}

type rejectKey struct {
	src    string
	reason error
}

func newRejectLog(log *slog.Logger) *rejectLog {
	return &rejectLog{log: log, counts: map[rejectKey]uint64{}}
}

func (rl *rejectLog) note(src string, reason error) {
	rl.mu.Lock()
	if len(rl.counts) > 10000 {
		rl.counts = map[rejectKey]uint64{}
	}
	k := rejectKey{src, reason}
	rl.counts[k]++
	c := rl.counts[k]
	rl.mu.Unlock()
	if c == 1 || c%1000 == 0 {
		rl.log.Warn("rejected HTTP log post", "source", src, "reason", reason, "count", c)
	}
}

// connLimit is a listener that closes connections beyond max open ones, or
// beyond perIP from one address.
type connLimit struct {
	net.Listener
	max, perIP int
	rl         *rejectLog
	mu         sync.Mutex
	open       int
	byIP       map[netip.Addr]int
}

var errHTTPConns = errors.New("too many open HTTP connections (listen.http.max_connections / max_connections_per_ip)")

func newConnLimit(ln net.Listener, max, perIP int, rl *rejectLog) *connLimit {
	return &connLimit{Listener: ln, max: max, perIP: perIP, rl: rl, byIP: map[netip.Addr]int{}}
}

func (l *connLimit) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		var ip netip.Addr
		if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
			ip = ap.Addr().Unmap()
		}
		l.mu.Lock()
		if l.open >= l.max || l.byIP[ip] >= l.perIP {
			l.mu.Unlock()
			c.Close()
			l.rl.note(ip.String(), errHTTPConns)
			continue
		}
		l.open++
		l.byIP[ip]++
		l.mu.Unlock()
		return &limitedConn{Conn: c, l: l, ip: ip}, nil
	}
}

func (l *connLimit) release(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.open--
	if l.byIP[ip]--; l.byIP[ip] <= 0 {
		delete(l.byIP, ip)
	}
}

type limitedConn struct {
	net.Conn
	l    *connLimit
	ip   netip.Addr
	once sync.Once
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.l.release(c.ip) })
	return err
}
