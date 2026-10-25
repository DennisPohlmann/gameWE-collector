// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/stats"
)

// MonitorInterval is the longest wait between status polls while the server
// does not answer.
const MonitorInterval = time.Minute

// DefaultLogAddressInterval is how often the log address is checked by
// default (features.log_address.interval).
const DefaultLogAddressInterval = time.Minute

// DefaultStatusInterval is how often "status" is polled by default
// (features.rcon_status.interval).
const DefaultStatusInterval = 10 * time.Second

// When the monitor first polls status and checks the log address after
// start (variables for tests).
var firstStatus, firstLogAddress = 2 * time.Second, 5 * time.Second

// StatusPlayer is a player line of the RCON "status" answer.
type StatusPlayer struct {
	UserID    int
	Name      string
	SteamID   string        // as listed; BOT for bots
	UniqueID  string        // hlstats_PlayerUniqueIds form (logparse.NormalizedID), BOT:<name> for bots
	Ping      int           // 0 for bots
	Connected time.Duration // 0 for bots
	Address   netip.Addr    // the adr column without port; invalid for bots and loopback/LAN entries without one
}

// Status is the answer of the RCON command "status".
type Status struct {
	Hostname   string
	Map        string
	MaxPlayers int
	Players    []StatusPlayer
	Info       stats.ServerInfo // version, OS and VAC state where status lists them
}

var (
	reStatusHost = regexp.MustCompile(`(?m)^hostname\s*:\s*(.*?)\s*$`)
	reStatusMap  = regexp.MustCompile(`(?m)^map\s*:\s*(\S+)`)
	reStatusMax  = regexp.MustCompile(`(?m)^players\s*:.*\((\d+)(?:/\d+)?\s*max\)`)
	// #  userid name uniqueid connected ping loss state [rate] adr
	// (CS:GO and newer have a slot number after the userid; bots have no
	// time, ping and loss).
	// GoldSrc (HLDS): "#      name userid uniqueid frag time ping loss adr"
	reStatusGoldSrcHead = regexp.MustCompile(`(?m)^#\s+name\s+userid\s+uniqueid`)
	reStatusGoldSrc     = regexp.MustCompile(`(?m)^#\s*(\d+)\s+"(.*)"\s+(\d+)\s+(\S+)\s+-?\d+\s+(\d+:\d+(?::\d+)?)\s+(\d+)\s+\d+(?:[ \t]+(\S+))?`)
	reStatusPlayer      = regexp.MustCompile(`(?m)^#\s*(\d+)\s+(?:\d+\s+)?"(.*)"\s+(\S+)\s+(?:(\d+:\d+(?::\d+)?)\s+(\d+)\s+\d+\s+)?(\w+)(?:[ \t]+(?:\d+[ \t]+)?(\S+))?`)
	// Source 2 (CS2): "  id     time ping loss      state   rate adr name",
	// no SteamIDs; bots "1 BOT 0 0 active 0 'Bot'", humans
	// "2 04:12 25 0 active 786432 192.0.2.1:27005 'Name'"; slot 65535 is a
	// connection still being set up. The map is in "loaded spawngroup".
	reStatusS2Head   = regexp.MustCompile(`(?m)^\s*id\s+time\s+ping\s+loss\s+state\s+rate`)
	reStatusS2Player = regexp.MustCompile(`(?m)^\s*(\d+)\s+(BOT|\d+:\d+(?::\d+)?|\[[^\]]*\]|\S+)\s+(\d+)\s+\d+\s+(\w+)\s+\d+\S*(?:[ \t]+(\S+))?[ \t]+'(.*)'[ \t]*$`)
	reStatusS2Map    = regexp.MustCompile(`(?m)spawngroup\s*\(\s*\d+\)\s*:\s*SV:\s*\[\d+:\s*([^\s|\]]+)`)
)

// ParseStatus reads the answer of "status".
func ParseStatus(out string) (Status, error) {
	var st Status
	if m := reStatusHost.FindStringSubmatch(out); m != nil {
		st.Hostname = m[1]
	}
	if m := reStatusMap.FindStringSubmatch(out); m != nil {
		st.Map = m[1]
	}
	if m := reStatusMax.FindStringSubmatch(out); m != nil {
		st.MaxPlayers, _ = strconv.Atoi(m[1])
	}
	st.Info = parseStatusInfo(out)
	if reStatusS2Head.MatchString(out) {
		if st.Map == "" {
			if m := reStatusS2Map.FindStringSubmatch(out); m != nil {
				st.Map = m[1]
			}
		}
		for _, m := range reStatusS2Player.FindAllStringSubmatch(out, -1) {
			if m[1] == "65535" || strings.EqualFold(m[4], "challenging") {
				continue
			}
			// No SteamID: a bot is BOT, a human is matched to the player of
			// the log lines by userid (Processor.Status).
			id, connected, ping := "", m[2], m[3]
			if m[2] == "BOT" {
				id, connected, ping = "BOT", "", "0"
			}
			st.Players = append(st.Players, statusPlayer(m[6], m[1], id, connected, ping, m[5]))
		}
	} else if reStatusGoldSrcHead.MatchString(out) {
		for _, m := range reStatusGoldSrc.FindAllStringSubmatch(out, -1) {
			// # slot "name" userid uniqueid frag time ping loss [adr]
			st.Players = append(st.Players, statusPlayer(m[2], m[3], m[4], m[5], m[6], m[7]))
		}
	} else {
		for _, m := range reStatusPlayer.FindAllStringSubmatch(out, -1) {
			st.Players = append(st.Players, statusPlayer(m[2], m[1], m[3], m[4], m[5], m[7]))
		}
	}
	if st.Map == "" && st.Hostname == "" {
		return st, errors.New("unexpected status output")
	}
	return st, nil
}

// statusPlayer makes a player of the columns of a status line.
func statusPlayer(name, userid, uniqueid, connected, ping, adr string) StatusPlayer {
	uid, _ := strconv.Atoi(userid)
	pg, _ := strconv.Atoi(ping)
	ref := logparse.Player{Name: name, UserID: uid, SteamID: uniqueid}
	if strings.EqualFold(uniqueid, "BOT") {
		ref.SteamID = "BOT"
	}
	p := StatusPlayer{UserID: uid, Name: name, SteamID: ref.SteamID, UniqueID: ref.Key(), Ping: pg, Connected: parseConnected(connected)}
	if ap, err := netip.ParseAddrPort(adr); err == nil {
		p.Address = ap.Addr().Unmap()
	} else if ip, err := netip.ParseAddr(adr); err == nil {
		p.Address = ip.Unmap()
	}
	return p
}

// parseConnected reads the connected column of status: [h:]mm:ss.
func parseConnected(s string) time.Duration {
	var d time.Duration
	for _, f := range strings.Split(s, ":") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return 0
		}
		d = d*60 + time.Duration(n)
	}
	return d * time.Second
}

// GoldSrc lists "ip port", Source "ip:port".
var reAddrPort = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3})[: ](\d{1,5})\b`)

// ParseLogAddresses reads the answer of "logaddress_list": every ip:port.
func ParseLogAddresses(out string) []netip.AddrPort {
	var list []netip.AddrPort
	for _, m := range reAddrPort.FindAllStringSubmatch(out, -1) {
		if ap, err := netip.ParseAddrPort(m[1] + ":" + m[2]); err == nil {
			list = append(list, ap)
		}
	}
	return list
}

// LogAddress returns the address to register on the game server: public
// (listen.public_address) if set, otherwise the collector's address of
// the RCON connection with the listen port.
func LogAddress(c rcon.Conn, public string, listenPort int) (netip.AddrPort, error) {
	if public != "" {
		return netip.ParseAddrPort(public)
	}
	var ipb net.IP
	switch a := c.LocalAddr().(type) {
	case *net.TCPAddr:
		ipb = a.IP
	case *net.UDPAddr:
		ipb = a.IP
	default:
		return netip.AddrPort{}, errors.New("no RCON connection")
	}
	ip, ok := netip.AddrFromSlice(ipb)
	if !ok {
		return netip.AddrPort{}, errors.New("no local address")
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(listenPort)), nil
}

// EnsureLogAddress checks with logaddress_list whether addr is registered
// on the game server and, if add is true and it is not, registers it with
// logaddress_add and switches logging on ("log on"; without it the server
// sends nothing). It returns the registered addresses (before adding).
// GoldSrc servers take "logaddress_add ip port".
func EnsureLogAddress(c rcon.Conn, engine games.Engine, addr netip.AddrPort, add bool) (list []netip.AddrPort, added bool, err error) {
	out, err := c.Exec("logaddress_list")
	if err != nil {
		return nil, false, err
	}
	list = ParseLogAddresses(out)
	for _, a := range list {
		if a == addr {
			if lg, err := c.Exec("log"); err == nil && strings.Contains(strings.ToLower(lg), "not currently logging") && add {
				_, err = c.Exec("log on")
				return list, true, err
			}
			return list, false, nil
		}
	}
	if !add {
		return list, false, nil
	}
	cmd := "logaddress_add " + addr.String()
	if engine == games.GoldSrc {
		cmd = fmt.Sprintf("logaddress_add %s %d", addr.Addr(), addr.Port())
	}
	if _, err := c.Exec(cmd); err != nil {
		return list, false, err
	}
	_, err = c.Exec("log on")
	return list, true, err
}

// DuplicateLogAddresses returns the entries of list other than the first
// copy of addr that use addr's port: another address of the collector's
// host (its public and its local one) or addr itself twice. The game server
// sends every line to each of them.
func DuplicateLogAddresses(list []netip.AddrPort, addr netip.AddrPort) []netip.AddrPort {
	var extra []netip.AddrPort
	first := true
	for _, a := range list {
		switch {
		case a == addr && first:
			first = false
		case a.Port() == addr.Port():
			extra = append(extra, a)
		}
	}
	return extra
}

// connKey identifies an RCON connection; a change of any field reconnects.
type connKey struct {
	addr, password string
	timeout        time.Duration
	engine         games.Engine
}

func (k connKey) dial() rcon.Conn {
	return rcon.New(k.engine == games.GoldSrc, k.addr, k.password, k.timeout)
}

// MonitorConfig are the jobs of the RCON monitor (features in the config file).
type MonitorConfig struct {
	Status         bool          // poll "status"
	StatusInterval time.Duration // how often (default DefaultStatusInterval)
	LogAddress     bool          // keep the collector registered with logaddress_add
	LogInterval    time.Duration // how often (default DefaultLogAddressInterval)
	ServerInfo     bool          // query A2S_INFO (version, OS, VAC); also read from status
	InfoInterval   time.Duration // how often (default DefaultServerInfoInterval)
	PublicAddress  string        // listen.public_address
	ListenPort     int
	NoUDP          bool // listen.udp.enabled is false: no logaddress_add
	// HTTP registers Source 2 (CS2) servers with logaddress_add_http
	// (listen.http on and features.log_address.http).
	HTTP          bool
	HTTPPublicURL string // listen.http.public_url
	HTTPPort      int    // listen.http.port
	HTTPToken     string // listen.http.token
}

// monitor polls one game server over RCON until ctx is done: "status" every
// StatusInterval (handed to deliver; after errors the wait doubles up to
// MonitorInterval) and the log address every LogInterval, over one
// connection that is kept open; and A2S_INFO on the game port every
// InfoInterval (handed to info; no RCON needed). srv returns the current
// hlstats_Servers row (the RCON password can change in the web admin).
func monitor(ctx context.Context, srv func() stats.Server, cfg MonitorConfig, deliver func(Status, time.Time), info func(stats.ServerInfo),
	timeout func() time.Duration, log *slog.Logger) {
	var c rcon.Conn
	var key connKey
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	lastErr := map[string]string{}
	report := func(job string, err error) {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != lastErr[job] {
			if err != nil {
				log.Warn("rcon monitor", "job", job, "err", err)
			} else if lastErr[job] != "" {
				log.Info("rcon monitor works again", "job", job)
			}
			lastErr[job] = msg
		}
	}
	interval := cfg.StatusInterval
	if interval <= 0 {
		interval = DefaultStatusInterval
	}
	wait := interval
	logEvery := cfg.LogInterval
	if logEvery <= 0 {
		logEvery = DefaultLogAddressInterval
	}
	infoEvery := cfg.InfoInterval
	if infoEvery <= 0 {
		infoEvery = DefaultServerInfoInterval
	}
	statusT := time.NewTimer(firstStatus) // first rounds shortly after start
	logT := time.NewTimer(firstLogAddress)
	infoT := time.NewTimer(firstServerInfo)
	if !cfg.Status {
		statusT.Stop()
	}
	if !cfg.LogAddress {
		logT.Stop()
	}
	if !cfg.ServerInfo || info == nil {
		infoT.Stop()
	}
	defer statusT.Stop()
	defer logT.Stop()
	defer infoT.Stop()
	client := func() rcon.Conn {
		s := srv()
		if s.RconPassword == "" {
			return nil
		}
		k := connKey{s.AddrPort.String(), s.RconPassword, timeout(), s.Engine}
		if c == nil || k != key {
			if c != nil {
				c.Close()
			}
			c, key = k.dial(), k
		}
		return c
	}
	noPassword := errors.New("no rcon_password in hlstats_Servers: status and logaddress skipped")
	var httpAdd httpAddState
	for {
		select {
		case <-ctx.Done():
			return
		case <-statusT.C:
			c := client()
			if c == nil {
				report("status", noPassword)
				statusT.Reset(MonitorInterval)
				continue
			}
			at := time.Now().UTC()
			st, err := pollStatus(c)
			report("status", err)
			if err != nil {
				wait = min(max(wait*2, interval), MonitorInterval)
			} else {
				wait = interval
				deliver(st, at)
			}
			statusT.Reset(wait)
		case <-infoT.C:
			infoT.Reset(infoEvery)
			s := srv()
			if !s.AddrPort.IsValid() {
				continue
			}
			got, err := QueryServerInfo(ctx, s.AddrPort, timeout())
			if ctx.Err() != nil {
				return
			}
			report("server_info", err)
			if err == nil {
				info(got)
			}
		case <-logT.C:
			logT.Reset(logEvery)
			c := client()
			if c == nil {
				if !cfg.Status {
					report("status", noPassword)
				}
				continue
			}
			if cfg.LogAddress {
				s := srv()
				report("logaddress", ensureLogAddress(c, s, cfg, &httpAdd, log))
			}
		}
	}
}

// pollStatus asks the game server for "status".
func pollStatus(c rcon.Conn) (Status, error) {
	out, err := c.Exec("status")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(out)
}

// ensureLogAddress keeps the collector registered on the game server:
// logaddress_add (UDP), or logaddress_add_http for Source 2 (CS2).
func ensureLogAddress(c rcon.Conn, srv stats.Server, cfg MonitorConfig, st *httpAddState, log *slog.Logger) error {
	engine := srv.Engine
	if engine == games.Source2 {
		if !cfg.HTTP {
			return nil // CS2 logs over HTTP only; registered by hand
		}
		return ensureHTTPLogAddress(c, srv, cfg, st, log)
	}
	if cfg.NoUDP {
		return nil
	}
	if _, err := c.Exec("echo"); err != nil { // connect, for the local address
		return err
	}
	addr, err := LogAddress(c, cfg.PublicAddress, cfg.ListenPort)
	if err != nil {
		return err
	}
	list, added, err := EnsureLogAddress(c, engine, addr, true)
	if err != nil {
		return fmt.Errorf("logaddress: %w", err)
	}
	if extra := DuplicateLogAddresses(list, addr); len(extra) > 0 {
		log.Warn("the game server sends its log to the collector's port more than once, every line arrives twice; remove the extra entries with logaddress_del",
			"log_address", addr.String(), "extra", fmt.Sprint(extra))
	}
	if added {
		log.Info("collector registered on the game server (logaddress_add, log on)", "log_address", addr.String())
	}
	return nil
}

// HTTPLogURL returns the URL to register on a CS2 server with
// logaddress_add_http: public (listen.http.public_url, http://host:port)
// or http:// and the collector's address of the RCON connection with the
// HTTP port, then the token (if set) and the game server's port.
func HTTPLogURL(c rcon.Conn, public string, httpPort int, token string, gamePort uint16) (string, error) {
	base := strings.TrimRight(public, "/")
	if base == "" {
		ap, err := LogAddress(c, "", httpPort)
		if err != nil {
			return "", err
		}
		base = "http://" + ap.String()
	}
	if token != "" {
		base += "/" + token
	}
	return fmt.Sprintf("%s/%d", base, gamePort), nil
}

// RedactURL hides the token of a log URL for logs and output.
func RedactURL(u, token string) string {
	if token == "" {
		return u
	}
	return strings.ReplaceAll(u, "/"+token+"/", "/<token>/")
}

// httpAddState remembers a logaddress_add_http that logaddress_list_http
// did not show afterwards, so that it is not repeated every interval.
type httpAddState struct {
	url  string
	last time.Time
}

// httpReAdd is how long an unconfirmed logaddress_add_http is not repeated.
const httpReAdd = time.Hour

// EnsureHTTPLogAddress checks with logaddress_list_http whether url is
// registered on a CS2 server and, if add is true and it is not, registers
// it with logaddress_add_http and switches logging on. listed reports
// whether the URL was in the list.
func EnsureHTTPLogAddress(c rcon.Conn, url string, add bool) (listed, added bool, err error) {
	out, err := c.Exec("logaddress_list_http")
	if err != nil {
		return false, false, err
	}
	if strings.Contains(out, url) {
		if lg, err := c.Exec("log"); err == nil && strings.Contains(strings.ToLower(lg), "not currently logging") && add {
			_, err = c.Exec("log on")
			return true, true, err
		}
		return true, false, nil
	}
	if !add {
		return false, false, nil
	}
	if _, err := c.Exec(`logaddress_add_http "` + url + `"`); err != nil {
		return false, false, err
	}
	_, err = c.Exec("log on")
	return false, true, err
}

func ensureHTTPLogAddress(c rcon.Conn, srv stats.Server, cfg MonitorConfig, st *httpAddState, log *slog.Logger) error {
	if _, err := c.Exec("echo"); err != nil { // connect, for the local address
		return err
	}
	url, err := HTTPLogURL(c, cfg.HTTPPublicURL, cfg.HTTPPort, cfg.HTTPToken, srv.AddrPort.Port())
	if err != nil {
		return err
	}
	shown := RedactURL(url, cfg.HTTPToken)
	// Added before but not listed since: the list may look different on
	// this server version; add again only after httpReAdd.
	add := st.url != url || time.Since(st.last) >= httpReAdd
	listed, added, err := EnsureHTTPLogAddress(c, url, add)
	if err != nil {
		return fmt.Errorf("logaddress_add_http: %w", err)
	}
	if listed {
		st.url = ""
	}
	if added && !listed {
		if st.url == url {
			log.Warn("logaddress_list_http does not show the collector's URL after logaddress_add_http; adding it again only every hour", "log_url", shown)
		}
		st.url, st.last = url, time.Now()
		log.Info("collector registered on the CS2 server (logaddress_add_http, log on)", "log_url", shown)
	}
	return nil
}
