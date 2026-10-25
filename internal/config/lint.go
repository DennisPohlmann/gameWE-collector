// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Severity of a Finding: an error stops gamewe run (or makes the start
// fail later, e.g. when a port cannot be opened); with a warning the
// collector runs, but something is probably not what was meant or is
// weaker than it should be.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
)

// Finding is one result of Lint.
type Finding struct {
	Severity Severity
	Key      string // config key (listen.http.port), a flag (-listen) or "" for the whole file
	Line     int    // line in the file, 0 if the key is not in it
	Msg      string // what is wrong and what to do; never a secret value
}

// LintOptions are the inputs of Lint besides the file.
type LintOptions struct {
	// Listen is -listen of gamewe run (IP:port): it overrides the UDP
	// address and port and switches UDP on.
	Listen string
	// GeoIPDefault is the GeoIP database used when geoip.path is empty.
	GeoIPDefault string
}

// Report is the result of Lint.
type Report struct {
	// Config is the configuration as gamewe run would use it; valid only
	// with Decoded.
	Config Config
	// Decoded: the file could be read into Config, so the values were
	// checked too (not after a YAML syntax error).
	Decoded bool
	// SyntaxOK: no YAML syntax error, unknown key, key of an older format
	// or value of the wrong type.
	SyntaxOK bool
	Findings []Finding
}

// Errors and Warnings count the findings.
func (r Report) Errors() int   { return r.count(SevError) }
func (r Report) Warnings() int { return r.count(SevWarning) }

func (r Report) count(s Severity) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == s {
			n++
		}
	}
	return n
}

// Hooks for the tests.
var (
	interfaceAddrs = net.InterfaceAddrs
	statFile       = os.Stat
	openFile       = func(name string) error {
		f, err := os.Open(name)
		if err == nil {
			f.Close()
		}
		return err
	}
)

// LintFile reads the config file at path and lints it (see Lint); it also
// warns when the file holds the database password and every user may read
// it.
func LintFile(path string, opt LintOptions) Report {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{Findings: []Finding{{Severity: SevError, Msg: fmt.Sprintf("cannot read the file: %v", cause(err))}}}
	}
	r := Lint(data, opt)
	if fi, err := statFile(path); err == nil && fi.Mode().Perm()&0o004 != 0 && r.Decoded && r.Config.Database.PasswordFile == "" &&
		r.Config.Database.Password != "" {
		r.Findings = append(r.Findings, Finding{Severity: SevWarning, Key: "database.password", Line: 0,
			Msg: fmt.Sprintf("the file holds the database password and every user may read it (mode %04o); chown root:gamewe and chmod 640", fi.Mode().Perm())})
	}
	return r
}

// Lint checks a config file the way gamewe run loads it, without opening
// a port, connecting to the database or changing anything: YAML syntax,
// unknown keys and keys of older formats, wrong types, every value checked
// by Parse (all problems, not only the first), and on top of that what Parse
// does not check (port clashes, missing or weak secrets, settings that have
// no effect, files of database.tls, ...). The findings are sorted by line.
func Lint(data []byte, opt LintOptions) Report {
	l := &linter{lines: map[string]int{}, valueLines: map[int]string{}, set: map[string]bool{}}
	r := l.lint(data, opt)
	sort.SliceStable(l.findings, func(i, j int) bool {
		a, b := l.findings[i].Line, l.findings[j].Line
		return a != 0 && (b == 0 || a < b)
	})
	r.Findings = l.findings
	return r
}

type linter struct {
	lines      map[string]int  // key path -> line of the key
	valueLines map[int]string  // line -> key path whose value starts there (the deepest)
	set        map[string]bool // key paths present in the file
	findings   []Finding
	udpForm    bool // the file has listen.udp (not the older keys under listen)
	listenFlag bool // -listen overrides the UDP address
}

func (l *linter) add(sev Severity, key, format string, args ...any) {
	l.findings = append(l.findings, Finding{Severity: sev, Key: key, Line: l.line(key), Msg: fmt.Sprintf(format, args...)})
}

func (l *linter) errorf(key, format string, args ...any) { l.add(SevError, key, format, args...) }
func (l *linter) warnf(key, format string, args ...any)  { l.add(SevWarning, key, format, args...) }

// line is the line of key in the file, or of the closest key above it.
func (l *linter) line(key string) int {
	for k := key; k != ""; {
		if n, ok := l.lines[k]; ok {
			return n
		}
		i := strings.LastIndexByte(k, '.')
		if i < 0 {
			break
		}
		k = k[:i]
	}
	return 0
}

// udpKey turns the key of the UDP input as Validate names it
// (listen.port) into the one of the file (listen.udp.port) or the flag.
func (l *linter) udpKey(key string) string {
	switch key {
	case "listen.address", "listen.port":
		if l.listenFlag {
			return "-listen"
		}
		fallthrough
	case "listen.public_address":
		if l.udpForm {
			return "listen.udp." + strings.TrimPrefix(key, "listen.")
		}
	}
	return key
}

var reLine = regexp.MustCompile(`^(?:yaml: )?line (\d+): `)

// lineOf splits "line 7: ..." of a yaml error.
func lineOf(msg string) (int, string) {
	if m := reLine.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, msg[len(m[0]):]
	}
	return 0, strings.TrimPrefix(msg, "yaml: ")
}

func (l *linter) lint(data []byte, opt LintOptions) Report {
	var r Report
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		n, msg := lineOf(err.Error())
		l.findings = append(l.findings, Finding{Severity: SevError, Line: n, Msg: "YAML syntax: " + msg})
		return r
	}
	if len(doc.Content) == 0 {
		l.errorf("", "the file is empty: database.host, user and name are required")
		return r
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		l.findings = append(l.findings, Finding{Severity: SevError, Line: root.Line, Msg: "want a mapping with listen:, database:, geoip: and features:"})
		return r
	}
	legacyKeys := legacyProblems(data)
	drop := map[string]bool{}
	for _, p := range legacyKeys {
		drop[p.Key] = p.drop
	}
	l.walk(root, reflect.TypeFor[Config](), "", drop)
	for _, p := range legacyKeys {
		l.errorf(p.Key, "%s", p.Msg)
	}
	l.udpForm = l.set["listen.udp"]

	cfg := Defaults()
	if err := root.Decode(&cfg); err != nil {
		var te *yaml.TypeError
		if !errors.As(err, &te) {
			n, msg := lineOf(err.Error())
			l.findings = append(l.findings, Finding{Severity: SevError, Key: l.valueLines[n], Line: n, Msg: msg})
			return r
		}
		for _, m := range te.Errors {
			n, msg := lineOf(m)
			key := l.valueLines[n]
			l.findings = append(l.findings, Finding{Severity: SevError, Key: key, Line: n, Msg: typeMsg(key, msg)})
		}
	}
	r.SyntaxOK = len(l.findings) == 0
	r.Decoded = true
	if l.set["listen.udp.port"] && cfg.Listen.UDPConf != nil && cfg.Listen.UDPConf.Port == 0 {
		l.warnf("listen.udp.port", "0 means the default port %d; set the port or remove the key", DefaultListenPort)
	}
	for _, p := range cfg.finish() {
		l.errorf(p.Key, "%s", p.Msg)
	}
	if opt.Listen != "" {
		l.listenFlag = true
		ap, err := netip.ParseAddrPort(opt.Listen)
		if err != nil {
			l.errorf("-listen", "%q must be IP:port", opt.Listen)
		} else {
			cfg.Listen.Address, cfg.Listen.Port, cfg.Listen.UDPEnabled = ap.Addr().String(), int(ap.Port()), true
		}
	}
	for _, p := range cfg.Problems() {
		key := l.udpKey(p.Key)
		if key == "listen" && l.set["listen.udp.enabled"] {
			key = "listen.udp.enabled"
		}
		l.errorf(key, "%s", p.Msg)
	}
	if o := cfg.Features.LogSecret.Obsolete; len(o) > 0 {
		l.warnf("features.log_secret", "%s", cfg.Features.LogSecret.ObsoleteWarning())
	}
	l.listeners(cfg)
	l.unused(cfg)
	l.secrets(cfg)
	l.database(cfg)
	l.geoip(cfg, opt.GeoIPDefault)
	r.Config = cfg
	return r
}

// allowedKeys are keys Decode reads although no field has their name.
var allowedKeys = map[string]bool{
	"features.log_secret.interval": true, // obsolete, ignored with a warning
}

// walk records the lines of all keys under n (a mapping decoded into t),
// reports unknown keys and removes them and the dropped keys of older
// formats, so the rest of the file can still be decoded and checked.
func (l *linter) walk(n *yaml.Node, t reflect.Type, path string, drop map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || n.Kind != yaml.MappingNode {
		return
	}
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	kept := n.Content[:0:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		p := k.Value
		if path != "" {
			p = path + "." + k.Value
		}
		l.lines[p] = k.Line
		l.set[p] = true
		l.valueLines[k.Line] = p
		l.valueLines[v.Line] = p
		if drop[p] {
			continue // reported as a key of an older format
		}
		ft, ok := fields[k.Value]
		if !ok && !allowedKeys[p] {
			l.errorf(p, "unknown key%s", suggest(k.Value, slices.Sorted(maps.Keys(fields))))
			continue
		}
		kept = append(kept, k, v)
		if ok {
			l.walk(v, ft, p, drop)
		}
	}
	n.Content = kept
}

// suggest is "; did you mean "port"?" for a key close to a known one, else
// the list of the known keys.
func suggest(key string, known []string) string {
	best, dist := "", 3
	for _, k := range known {
		if d := editDistance(strings.ToLower(key), k); d < dist {
			best, dist = k, d
		}
	}
	if best != "" {
		return fmt.Sprintf("; did you mean %q?", best)
	}
	return " (known here: " + strings.Join(known, ", ") + ")"
}

// editDistance is the Levenshtein distance of a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

var reUnmarshal = regexp.MustCompile("^cannot unmarshal !!(\\w+) (?:`(.*)` )?into (\\S+)$")

// typeMsg rewrites a yaml type error ("cannot unmarshal !!str `x` into
// int") for people; the value is left out for secret keys.
func typeMsg(key, msg string) string {
	m := reUnmarshal.FindStringSubmatch(msg)
	if m == nil {
		return msg
	}
	want := map[string]string{"int": "a whole number", "bool": "true or false", "string": "a text value", "[]string": "a list"}[m[3]]
	if want == "" {
		want = "a " + strings.TrimPrefix(m[3], "config.")
	}
	got := map[string]string{"seq": "a list", "map": "a mapping", "str": "text", "int": "a number", "float": "a number", "bool": "true / false"}[m[1]]
	if got == "" {
		got = m[1]
	}
	if m[2] != "" && !secretName(key) {
		got = fmt.Sprintf("%q", m[2])
	}
	return fmt.Sprintf("want %s, not %s", want, got)
}

func secretName(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "password") || strings.Contains(k, "token") || strings.Contains(k, "secret")
}

// listener is one port the collector opens.
type listener struct {
	key   string // config key of the address
	proto string // udp, tcp
	addr  netip.Addr
	port  int
}

// overlaps reports whether a and b cannot both be bound: the same protocol
// and port, and the same address or a wildcard covering the other (:: also
// covers IPv4, as on Linux by default).
func overlaps(a, b listener) bool {
	if a.proto != b.proto || a.port != b.port {
		return false
	}
	x, y := a.addr.Unmap(), b.addr.Unmap()
	switch {
	case x == y:
		return true
	case x.IsUnspecified() && x.Is6(), y.IsUnspecified() && y.Is6():
		return true
	case x.IsUnspecified():
		return y.Is4()
	case y.IsUnspecified():
		return x.Is4()
	}
	return false
}

// clashes returns the pairs of ls that cannot both be bound.
func clashes(ls []listener) [][2]listener {
	var out [][2]listener
	for i := range ls {
		for j := i + 1; j < len(ls); j++ {
			if overlaps(ls[i], ls[j]) {
				out = append(out, [2]listener{ls[i], ls[j]})
			}
		}
	}
	return out
}

// listeners checks the ports the collector opens: clashes, privileged
// ports, addresses that are not on this host, and the local database.
func (l *linter) listeners(cfg Config) {
	var ls []listener
	if a, err := netip.ParseAddr(cfg.Listen.Address); err == nil && cfg.Listen.UDPEnabled && cfg.Listen.Port > 0 && cfg.Listen.Port <= 65535 {
		ls = append(ls, listener{l.udpKey("listen.address"), "udp", a, cfg.Listen.Port})
	}
	h := cfg.Listen.HTTP
	if a, err := netip.ParseAddr(h.Address); err == nil && h.Enabled && h.Port > 0 && h.Port <= 65535 {
		ls = append(ls, listener{"listen.http.address", "tcp", a, h.Port})
	}
	for _, c := range clashes(ls) {
		l.errorf(portKey(c[1].key), "%s %s:%d clashes with %s (%s %s:%d): use another port", c[1].proto, c[1].addr, c[1].port, portKey(c[0].key), c[0].proto, c[0].addr, c[0].port)
	}
	var local []netip.Addr
	if addrs, err := interfaceAddrs(); err == nil {
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				local = append(local, p.Addr().Unmap())
			}
		}
	}
	for _, x := range ls {
		if x.port < 1024 {
			l.warnf(portKey(x.key), "%s port %d is privileged: the gamewe user cannot open it without CAP_NET_BIND_SERVICE; use a port from 1024 on", x.proto, x.port)
		}
		if !x.addr.IsUnspecified() && local != nil && !slices.Contains(local, x.addr.Unmap()) {
			l.warnf(x.key, "%s is not an address of this host: opening the %s port fails unless the address is added here", x.addr, x.proto)
		}
	}
	d := cfg.Database
	if localHost(d.Host) {
		for _, x := range ls {
			if x.proto == "tcp" && x.port == d.Port {
				l.errorf(portKey(x.key), "tcp port %d is the port of the local database (database.host %s): the collector cannot open it while the database runs", x.port, d.Host)
			}
		}
	}
}

// portKey is the port key next to an address key.
func portKey(addrKey string) string {
	if addrKey == "-listen" {
		return addrKey
	}
	return strings.TrimSuffix(addrKey, "address") + "port"
}

// localHost reports whether host is this machine.
func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// unused warns about settings that have no effect with the switches as
// they are: a changed value under an input or feature that is off, and the
// public addresses while nothing registers them.
func (l *linter) unused(cfg Config) {
	def := Defaults()
	changed := func(key string, differs bool) string {
		if l.set[key] && differs {
			return key
		}
		return ""
	}
	off := func(parent string, isOff bool, keys ...string) {
		if !isOff {
			return
		}
		for _, k := range keys {
			if k != "" {
				l.warnf(k, "has no effect while %s is false", parent)
			}
		}
	}
	u, du := cfg.Listen, def.Listen
	if !u.UDPEnabled {
		parent := "listen.udp.enabled"
		uc := u.UDPConf
		if uc == nil {
			uc = &ListenUDP{}
		}
		off(parent, true,
			changed("listen.udp.address", uc.Address != "" && uc.Address != du.Address),
			changed("listen.udp.port", uc.Port != 0 && uc.Port != du.Port),
			changed("listen.udp.public_address", uc.PublicAddress != ""),
			changed("listen.address", !l.udpForm && u.Address != du.Address),
			changed("listen.port", !l.udpForm && u.Port != du.Port),
			changed("listen.public_address", !l.udpForm && u.PublicAddress != ""))
	}
	h, dh := u.HTTP, du.HTTP
	off("listen.http.enabled", !h.Enabled,
		changed("listen.http.address", h.Address != dh.Address),
		changed("listen.http.port", h.Port != dh.Port),
		changed("listen.http.public_url", h.PublicURL != ""),
		changed("listen.http.token", h.Token != ""),
		changed("listen.http.max_body", h.MaxBody != dh.MaxBody),
		changed("listen.http.read_timeout", h.ReadTimeout != dh.ReadTimeout),
		changed("listen.http.max_connections", h.MaxConnections != dh.MaxConnections),
		changed("listen.http.max_connections_per_ip", h.MaxConnectionsPerIP != dh.MaxConnectionsPerIP),
		changed("listen.http.trusted_proxies", len(h.TrustedProxies) > 0))
	f, df := cfg.Features, def.Features
	off("features.rcon_status.enabled", !f.RconStatus.Enabled, changed("features.rcon_status.interval", f.RconStatus.Interval != df.RconStatus.Interval))
	off("features.log_address.enabled", !f.LogAddress.Enabled,
		changed("features.log_address.interval", f.LogAddress.Interval != df.LogAddress.Interval))
	off("features.server_info.enabled", !f.ServerInfo.Enabled, changed("features.server_info.interval", f.ServerInfo.Interval != df.ServerInfo.Interval))
	off("features.daily_awards.enabled", !f.DailyAwards.Enabled,
		changed("features.daily_awards.timezone", f.DailyAwards.Timezone != df.DailyAwards.Timezone),
		changed("features.daily_awards.time", f.DailyAwards.Time != df.DailyAwards.Time))
	// The public addresses are only what features.log_address registers.
	if u.UDPEnabled && u.PublicAddress != "" && !f.LogAddress.Enabled {
		l.warnf(l.udpKey("listen.public_address"), "has no effect while features.log_address.enabled is false (nothing registers it); use it in logaddress_add by hand")
	}
	if h.Enabled && h.PublicURL != "" && (!f.LogAddress.Enabled || !f.LogAddress.HTTP) {
		l.warnf("listen.http.public_url", "has no effect while features.log_address (enabled and http) is off (nothing registers it); use it in logaddress_add_http by hand")
	}
	if l.set["features.log_address.http"] && f.LogAddress.HTTP && !f.LogAddress.Enabled {
		l.warnf("features.log_address.http", "has no effect while features.log_address.enabled is false")
	}
	if pa, err := netip.ParseAddrPort(u.PublicAddress); err == nil && u.UDPEnabled && pa.Addr().IsUnspecified() {
		l.warnf(l.udpKey("listen.public_address"), "%s is no address a game server can send to: set the collector's public IP", pa.Addr())
	}
	if pu, err := url.Parse(h.PublicURL); err == nil && h.Enabled && h.PublicURL != "" {
		if a, err := netip.ParseAddr(pu.Hostname()); err == nil && a.IsUnspecified() {
			l.warnf("listen.http.public_url", "%s is no address a game server can post to: set the collector's public IP or name", a)
		}
	}
}

// secrets checks the secrets of the enabled inputs and of the database;
// their values are never part of a message.
func (l *linter) secrets(cfg Config) {
	h := cfg.Listen.HTTP
	if h.Enabled {
		switch {
		case h.Token == "":
			l.warnf("listen.http.token", "not set while listen.http is on: CS2's HTTP logs carry no sv_logsecret, so only the sender's IP protects the input; set a random token of 16 to 128 letters, digits, - or _ (e.g. openssl rand -hex 24)")
		case reToken.MatchString(h.Token) && distinct(h.Token) < 8:
			l.warnf("listen.http.token", "weak: it has fewer than 8 different characters; use a random value (e.g. openssl rand -hex 24)")
		}
		if p, err := h.Proxies(); err == nil {
			for _, n := range p {
				if (n.Addr().Is4() && n.Bits() < 8) || (n.Addr().Is6() && n.Bits() < 16) {
					l.warnf("listen.http.trusted_proxies", "%s trusts X-Forwarded-For from (almost) everyone, so any sender can claim a game server's IP; list only your reverse proxies", n)
				}
			}
		}
	}
	d := cfg.Database
	key := "database.password"
	if d.PasswordFile != "" {
		key = "database.password_file"
	}
	switch {
	case d.Password == "" && d.PasswordFile == "":
		l.warnf(key, "not set: the collector connects without a password; set password or password_file")
	case d.Password == "":
		l.warnf(key, "the file is empty: the collector connects without a password")
	case strings.EqualFold(d.Password, "change-me"):
		l.warnf(key, "still the example value: set the password of the database user")
	case len(d.Password) < 12:
		l.warnf(key, "weak: shorter than 12 characters")
	}
	if d.PasswordFile != "" {
		l.worldReadable("database.password_file", d.PasswordFile)
	}
}

// cause is the reason of a file error without the path (which the
// message names already).
func cause(err error) error {
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}

// distinct counts the different characters of s.
func distinct(s string) int {
	seen := map[rune]bool{}
	for _, c := range s {
		seen[c] = true
	}
	return len(seen)
}

// worldReadable warns when every user may read the secret file name.
func (l *linter) worldReadable(key, name string) {
	if fi, err := statFile(name); err == nil && fi.Mode().Perm()&0o004 != 0 {
		l.warnf(key, "every user may read the file (mode %04o); chmod 640 (or 600)", fi.Mode().Perm())
	}
}

var reHostName = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?)*\.?$`)

// database checks what Validate does not: the host name, plaintext to a
// remote database, and the files of database.tls.
func (l *linter) database(cfg Config) {
	d := cfg.Database
	if d.Host != "" {
		if _, err := netip.ParseAddr(d.Host); err != nil && !reHostName.MatchString(d.Host) {
			hint := "a host name or IP address"
			if strings.Contains(d.Host, "/") {
				hint = "a host name or IP address, no URL or socket path (the collector connects over TCP)"
			} else if h, p, err := net.SplitHostPort(d.Host); err == nil && h != "" && p != "" {
				hint = "the host only: set the port in database.port"
			}
			l.errorf("database.host", "no valid host: want %s", hint) // not printed: it may hold user:password@
		} else if d.TLS.Mode == TLSDisable && remoteHost(d.Host) {
			l.warnf("database.tls.mode", "disable sends the password and all data in plaintext to a database on another host; use verify-full (DATABASE.md)")
		}
	}
	if d.TLS.Mode == TLSDisable {
		return // the certificate options are an error then (Validate)
	}
	for _, f := range []struct{ key, name string }{{"database.tls.ca_file", d.TLS.CAFile}, {"database.tls.cert_file", d.TLS.CertFile}, {"database.tls.key_file", d.TLS.KeyFile}} {
		if f.name == "" {
			continue
		}
		fi, err := statFile(f.name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			l.errorf(f.key, "%s: file not found", f.name)
		case err != nil:
			l.errorf(f.key, "%s: %v", f.name, cause(err))
		case fi.IsDir():
			l.errorf(f.key, "%s is a directory, want a PEM file", f.name)
		default:
			if err := openFile(f.name); err != nil {
				l.errorf(f.key, "%s: not readable for this user (%v); the collector runs as gamewe", f.name, cause(err))
			}
		}
	}
	if d.TLS.KeyFile != "" {
		l.worldReadable("database.tls.key_file", d.TLS.KeyFile)
	}
}

// remoteHost reports whether host is a database reached over a network
// that is not only this host or a private one: a public IP, or a name with
// a dot (a single label such as "db" is a container or local name).
func remoteHost(host string) bool {
	if a, err := netip.ParseAddr(host); err == nil {
		return !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast()
	}
	return !strings.EqualFold(host, "localhost") && strings.Contains(strings.TrimSuffix(host, "."), ".")
}

// geoip warns when there will be no locations: the database file is
// missing and is not downloaded, or cannot be stored.
func (l *linter) geoip(cfg Config, def string) {
	path := cfg.GeoIP.Path
	key := "geoip.path"
	if path == "" {
		path, key = def, "geoip"
	}
	if path == "" {
		return
	}
	_, err := statFile(path)
	switch {
	case err == nil:
	case !errors.Is(err, fs.ErrNotExist):
		l.warnf(key, "%s: %v", path, cause(err))
	case !cfg.GeoIP.AutoUpdate:
		l.warnf(key, "%s: file not found and geoip.auto_update is false: no player or server locations", path)
	default:
		if fi, err := statFile(filepath.Dir(path)); err != nil || !fi.IsDir() {
			l.warnf(key, "%s: the directory does not exist, so geoip.auto_update cannot store the download: no locations", filepath.Dir(path))
		}
	}
}
