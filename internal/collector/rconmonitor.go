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
	if reStatusGoldSrcHead.MatchString(out) {
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
	PublicAddress  string        // listen.public_address
	ListenPort     int
}

// monitor polls one game server over RCON until ctx is done: "status" every
// StatusInterval (handed to deliver; after errors the wait doubles up to
// MonitorInterval) and the log address every LogInterval, over one
// connection that is kept open. srv returns the current hlstats_Servers
// row (the RCON password can change in the web admin).
//
// sec (nil: off) is features.log_secret auto: every LogInterval the
// secret is checked and set (secretJob.manage), and a quarantine asks for
// sv_logsecret at once, at most every secretCheckEvery.
func monitor(ctx context.Context, srv func() stats.Server, cfg MonitorConfig, sec *secretJob, deliver func(Status, time.Time), timeout func() time.Duration, log *slog.Logger) {
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
	statusT := time.NewTimer(firstStatus) // first rounds shortly after start
	logT := time.NewTimer(firstLogAddress)
	if !cfg.Status {
		statusT.Stop()
	}
	if !cfg.LogAddress && sec == nil {
		logT.Stop()
	}
	defer statusT.Stop()
	defer logT.Stop()
	var checkC <-chan struct{}
	if sec != nil {
		checkC = sec.gate.check
	}
	checkT := time.NewTimer(time.Hour)
	checkT.Stop()
	defer checkT.Stop()
	var lastCheck time.Time
	checkArmed := false
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
	noPassword := errors.New("no rcon_password in hlstats_Servers: status, logaddress and log secret skipped")
	checkSecret := func() {
		lastCheck = time.Now()
		if c := client(); c != nil {
			report("logsecret", sec.check(c))
		}
	}
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
				report("logaddress", ensureLogAddress(c, srv().Engine, cfg, log))
			}
			if sec != nil {
				report("logsecret", sec.manage(c, srv()))
			}
		case <-checkC:
			if wait := secretCheckEvery - time.Since(lastCheck); wait > 0 {
				if !checkArmed {
					checkT.Reset(wait)
					checkArmed = true
				}
				continue
			}
			checkSecret()
		case <-checkT.C:
			checkArmed = false
			checkSecret()
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

// ensureLogAddress keeps the collector registered on the game server.
func ensureLogAddress(c rcon.Conn, engine games.Engine, cfg MonitorConfig, log *slog.Logger) error {
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
