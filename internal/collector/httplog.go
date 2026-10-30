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

// HTTPServer is the server an HTTP log post is for: only what the log
// may show (no RCON password, no log secret).
type HTTPServer struct {
	ID   int64
	Name string
}

// CheckHTTP reports why HTTP logs claiming to come from src (the sender's
// IP and the game port of the URL) are not accepted (nil: accepted, for
// the returned server).
func (h *Hub) CheckHTTP(src netip.AddrPort) (HTTPServer, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	w, err := h.httpWorker(src)
	if err != nil {
		return HTTPServer{}, err
	}
	return HTTPServer{ID: w.id, Name: Printable(w.srv.Name)}, nil
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
	rl := newRejectLog(log, "rejected HTTP log post")
	lim := newConnLimit(ln, cfg.MaxConnections, cfg.MaxConnectionsPerIP, rl)
	srv := &http.Server{
		Handler:           newHTTPHandler(cfg, hub, rl, log),
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
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	last map[netip.AddrPort]time.Time // last accepted post per source
}

func newHTTPHandler(cfg HTTPConfig, hub *Hub, rl *rejectLog, log *slog.Logger) *httpHandler {
	return &httpHandler{cfg: cfg, hub: hub, rl: rl, log: log, now: func() time.Time { return time.Now().UTC() },
		last: map[netip.AddrPort]time.Time{}}
}

// HTTPQuiet is how long a server may post no logs before its next post is
// logged again as "receiving HTTP logs".
const HTTPQuiet = 10 * time.Minute

// Why a post is answered with 404. The path itself is never logged: it
// may hold the token, or a token that is nearly right.
var (
	errHTTPMethod     = errors.New("HTTP method is not POST")
	errHTTPPath       = errors.New("HTTP path is not /<game port> (or /<token>/<game port>)")
	errHTTPPort       = errors.New("HTTP path does not end with a game port (1-65535)")
	errHTTPNoToken    = errors.New("HTTP path has no token, but listen.http.token is set")
	errHTTPToken      = errors.New("HTTP path has a wrong token (listen.http.token)")
	errHTTPExtraToken = errors.New("HTTP path has a token (or another segment), but listen.http.token is not set")
	errHTTPSender     = errors.New("HTTP sender unknown: X-Forwarded-For of a trusted proxy is not an IP address")
	errHTTPBody       = errors.New("HTTP body larger than listen.http.max_body")
	errHTTPRead       = errors.New("HTTP body could not be read")
)

func (hh *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := hh.sender(r)
	if r.Method != http.MethodPost {
		hh.rl.note(ip.String(), errHTTPMethod, status(http.StatusMethodNotAllowed))
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	port, err := hh.port(r.URL.Path)
	if err == nil && !ip.IsValid() {
		err = errHTTPSender
	}
	if err != nil {
		hh.rl.note(ip.String(), err, status(http.StatusNotFound))
		http.NotFound(w, r)
		return
	}
	src := netip.AddrPortFrom(ip, port)
	// Check the sender before reading the body: strangers cost no memory.
	srv, err := hh.hub.CheckHTTP(src)
	if err != nil {
		hh.rl.note(src.String(), err, func() []any { return append(status(http.StatusForbidden)(), hh.hub.httpHint(src, err)...) })
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hh.cfg.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			hh.rl.note(src.String(), errHTTPBody, status(http.StatusRequestEntityTooLarge))
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		hh.rl.note(src.String(), errHTTPRead, status(http.StatusBadRequest))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	lines := splitHTTPLines(string(body))
	n, err := hh.hub.DispatchLines(src, lines, hh.now())
	if err != nil {
		// Like a lost UDP packet: answered with 200 so the server does not
		// post the same lines again.
		hh.rl.note(src.String(), err, status(http.StatusOK))
	}
	hh.accepted(src, srv, n)
	w.WriteHeader(http.StatusOK)
}

// accepted logs a post that was taken: at info level the first one of a
// source, and the first after HTTPQuiet without posts; every post at
// debug level.
func (hh *httpHandler) accepted(src netip.AddrPort, srv HTTPServer, lines int) {
	now := hh.now()
	hh.mu.Lock()
	last, seen := hh.last[src]
	hh.last[src] = now
	hh.mu.Unlock()
	args := []any{"source", src.String(), "server_id", srv.ID, "name", srv.Name, "lines", lines}
	switch {
	case !seen:
		hh.log.Info("receiving HTTP logs", args...)
	case now.Sub(last) >= HTTPQuiet:
		hh.log.Info("receiving HTTP logs", append(args, "after", now.Sub(last).Round(time.Second).String())...)
	default:
		hh.log.Debug("HTTP log post", args...)
	}
}

// status is the HTTP answer to a rejected post, for the log line.
func status(code int) func() []any {
	return func() []any { return []any{"status", code} }
}

// httpHint says how to fix the rejection of an HTTP log post for src.
func (h *Hub) httpHint(src netip.AddrPort, err error) []any {
	switch {
	case errors.Is(err, ErrUnknownPort):
		return []any{"configured_ports", h.PortsAt(src.Addr()),
			"hint", "the port at the end of the logaddress_add_http URL must be the game port of the server in the web admin"}
	case errors.Is(err, ErrUnknownSource):
		return []any{"hint", "no server " + src.String() + " in the web admin: add it, or fix the port at the end of the logaddress_add_http URL"}
	case errors.Is(err, ErrNotSource2):
		return []any{"hint", "the ranking of this server must have the game cs2"}
	}
	return nil
}

// port reads the game port from the path: /<port>, or /<token>/<port>
// when a token is set (compared in constant time). The error says what
// is wrong with the path.
func (hh *httpHandler) port(path string) (uint16, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if hh.cfg.Token != "" {
		switch {
		case len(parts) == 1 && validPort(parts[0]):
			return 0, errHTTPNoToken
		case len(parts) == 1 && subtle.ConstantTimeCompare([]byte(parts[0]), []byte(hh.cfg.Token)) == 1:
			return 0, errHTTPPort // the token alone
		case len(parts) != 2:
			return 0, errHTTPPath
		case subtle.ConstantTimeCompare([]byte(parts[0]), []byte(hh.cfg.Token)) != 1:
			return 0, errHTTPToken
		}
		parts = parts[1:]
	} else if len(parts) == 2 && validPort(parts[1]) {
		return 0, errHTTPExtraToken
	}
	if len(parts) != 1 {
		return 0, errHTTPPath
	}
	if !validPort(parts[0]) {
		return 0, errHTTPPort
	}
	n, _ := strconv.ParseUint(parts[0], 10, 16)
	return uint16(n), nil
}

// validPort reports whether s is a port 1-65535 in plain digits.
func validPort(s string) bool {
	if len(s) == 0 || len(s) > 5 || s[0] < '0' || s[0] > '9' {
		return false
	}
	n, err := strconv.ParseUint(s, 10, 16)
	return err == nil && n != 0
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
			l.rl.note(ip.String(), errHTTPConns, nil)
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
