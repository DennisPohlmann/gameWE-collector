// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads the gameWE collector's YAML configuration: the
// database connection and the inputs for game-server logs (UDP, and HTTP
// for Counter-Strike 2). Game
// servers and all statistics settings are read from the database
// (hlstats_Servers, hlstats_Options, ...).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultPath is used when no -config flag is given.
const DefaultPath = "/etc/gamewe/collector/config.yaml"

// Database types. MariaDB and MySQL use the same driver and SQL.
const (
	MySQL    = "mysql"
	Postgres = "postgres"
)

// TLS modes for the database connection.
const (
	TLSDisable    = "disable"
	TLSRequire    = "require"
	TLSVerifyCA   = "verify-ca"
	TLSVerifyFull = "verify-full"
)

type Config struct {
	Listen   Listen   `yaml:"listen"`
	Database Database `yaml:"database"`
	Features Features `yaml:"features"`
	GeoIP    GeoIP    `yaml:"geoip"`
}

// GeoIP is the city database for player and server locations.
type GeoIP struct {
	// Path of the database (default /var/lib/gamewe/collector/dbip-city-lite.mmdb).
	// It wins over hlstats_Options gamewe_geoip_db.
	Path string `yaml:"path"`
	// AutoUpdate downloads DB-IP City Lite into Path when it is missing (at
	// start) and once a month; a Path that is another database (MaxMind)
	// is not overwritten.
	AutoUpdate bool `yaml:"auto_update"`
}

// Listen is where the collector receives game-server logs: UDP
// (logaddress_add; Source 1 and GoldSrc) and HTTP (logaddress_add_http;
// Counter-Strike 2). In the file the UDP input is listen.udp, or (older
// form, still read) address, port and public_address directly under
// listen.
type Listen struct {
	// The UDP input, from listen.udp or the older keys; Parse fills them.
	Address string `yaml:"address"` // IP to listen on, default 0.0.0.0
	Port    int    `yaml:"port"`    // UDP port, default 41337
	// PublicAddress is the ip:port the game servers send their logs to
	// (logaddress_add); empty: the collector's address of the RCON
	// connection and Port.
	PublicAddress string `yaml:"public_address"`
	// UDPEnabled is listen.udp.enabled (default true).
	UDPEnabled bool       `yaml:"-"`
	UDPConf    *ListenUDP `yaml:"udp"`
	HTTP       ListenHTTP `yaml:"http"`
}

// ListenUDP is listen.udp.
type ListenUDP struct {
	Enabled       *bool  `yaml:"enabled"`
	Address       string `yaml:"address"`
	Port          int    `yaml:"port"`
	PublicAddress string `yaml:"public_address"`
}

// ListenHTTP is listen.http: the HTTP input for the logs of Counter-Strike
// 2 (logaddress_add_http "http://<ip>:<port>/<game server port>"). Off by
// default.
type ListenHTTP struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // IP to listen on, default 0.0.0.0
	Port    int    `yaml:"port"`    // TCP port, default 41338
	// PublicURL is the http://host:port the game servers post their logs
	// to (logaddress_add_http); empty: http://, the collector's address of
	// the RCON connection and Port.
	PublicURL string `yaml:"public_url"`
	// Token, if set, is a secret first path segment: only posts to
	// /<token>/<port> are accepted.
	Token string `yaml:"token"`
	// MaxBody is the largest request body ("128KiB", "1MiB" or bytes).
	MaxBody string `yaml:"max_body"`
	// ReadTimeout is how long reading one request may take ("10s").
	ReadTimeout string `yaml:"read_timeout"`
	// MaxConnections and MaxConnectionsPerIP bound the open connections.
	MaxConnections      int `yaml:"max_connections"`
	MaxConnectionsPerIP int `yaml:"max_connections_per_ip"`
	// TrustedProxies are the IPs or networks (CIDR) of reverse proxies in
	// front of the collector: only for their requests the sender is read
	// from X-Forwarded-For.
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// DefaultHTTPPort is the default TCP port of the HTTP input.
const DefaultHTTPPort = 41338

// Limits of listen.http.
const (
	MinHTTPBody        = 4 << 10
	MaxHTTPBody        = 16 << 20
	MinHTTPReadTimeout = time.Second
	MaxHTTPReadTimeout = 2 * time.Minute
	MaxHTTPConnections = 4096
)

var reToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// Addr returns the listen address of the HTTP input as host:port.
func (h ListenHTTP) Addr() string {
	return net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
}

// BodyLimit returns MaxBody in bytes.
func (h ListenHTTP) BodyLimit() (int64, error) { return parseSize(h.MaxBody) }

// Timeout returns ReadTimeout as a duration.
func (h ListenHTTP) Timeout() (time.Duration, error) { return interval(h.ReadTimeout) }

// Proxies returns TrustedProxies as networks (a plain IP is a /32 or /128).
func (h ListenHTTP) Proxies() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, p := range h.TrustedProxies {
		p = strings.TrimSpace(p)
		if pf, err := netip.ParsePrefix(p); err == nil {
			out = append(out, pf.Masked())
			continue
		}
		ip, err := netip.ParseAddr(p)
		if err != nil {
			return nil, fmt.Errorf("listen.http.trusted_proxies: %q is not an IP address or network", p)
		}
		ip = ip.Unmap()
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// parseSize reads "128KiB", "1MiB", "64KB" or a number of bytes.
func parseSize(v string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	mul := int64(1)
	for _, u := range []struct {
		suffix string
		mul    int64
	}{{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"KB", 1000}, {"MB", 1000 * 1000}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mul = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.mul
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > (1<<40)/mul {
		return 0, fmt.Errorf("%q is not a size", v)
	}
	return n * mul, nil
}

// Features switches the collector's optional jobs on or off; all are on
// by default.
type Features struct {
	RconStatus RconStatus `yaml:"rcon_status"` // poll every server's "status" over RCON
	// CS2Hitgroups counts the hit groups of Counter-Strike 2's attacked
	// lines (CS2 has no plugin that reports them).
	CS2Hitgroups Toggle      `yaml:"cs2_hitgroups"`
	LogAddress   LogAddress  `yaml:"log_address"`  // register the collector with logaddress_add
	LogSecret    LogSecret   `yaml:"log_secret"`   // check sv_logsecret of the log packets
	DailyAwards  DailyAwards `yaml:"daily_awards"` // the daily awards job
	ServerInfo   ServerInfo  `yaml:"server_info"`  // version, OS and VAC of every server (A2S_INFO, status)
}

// Toggle is a feature without settings: a mapping with enabled, or just
// true / false.
type Toggle struct {
	Enabled bool `yaml:"enabled"`
}

// UnmarshalYAML accepts "x: true" and the mapping.
func (t *Toggle) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&t.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: want true / false or enabled", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "enabled" {
			return fmt.Errorf("line %d: unknown key %q (enabled)", n.Content[i].Line, k)
		}
	}
	type plain Toggle
	return n.Decode((*plain)(t))
}

// RconStatus polls every game server's "status" over RCON. In the config
// file it is a mapping, or (legacy form) just true / false.
type RconStatus struct {
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"` // Go duration ("10s") or seconds
}

// MinStatusInterval is the shortest features.rcon_status.interval.
const MinStatusInterval = 2 * time.Second

// UnmarshalYAML accepts "rcon_status: true" and the mapping.
func (r *RconStatus) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&r.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.rcon_status: want true / false or enabled, interval", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "interval":
		default:
			return fmt.Errorf("line %d: features.rcon_status: unknown key %q (enabled, interval)", n.Content[i].Line, k)
		}
	}
	type plain RconStatus
	return n.Decode((*plain)(r))
}

// Every returns Interval as a duration ("10s", "1m" or plain seconds).
func (r RconStatus) Every() (time.Duration, error) { return interval(r.Interval) }

// interval reads a Go duration ("10s", "1m") or plain seconds.
func interval(v string) (time.Duration, error) {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return time.Duration(s) * time.Second, nil
	}
	return time.ParseDuration(strings.TrimSpace(v))
}

// LogAddress keeps the collector registered on every game server
// (logaddress_list, logaddress_add when missing). In the config file it is a
// mapping, or (legacy form) just true / false.
type LogAddress struct {
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"` // Go duration ("1m") or seconds
	// HTTP registers Counter-Strike 2 servers with logaddress_add_http
	// (when listen.http is on; default true).
	HTTP bool `yaml:"http"`
}

// MinLogAddressInterval is the shortest features.log_address.interval.
const MinLogAddressInterval = 10 * time.Second

// UnmarshalYAML accepts "log_address: true" and the mapping.
func (l *LogAddress) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&l.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.log_address: want true / false or enabled, interval", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "interval", "http":
		default:
			return fmt.Errorf("line %d: features.log_address: unknown key %q (enabled, interval, http)", n.Content[i].Line, k)
		}
	}
	type plain LogAddress
	return n.Decode((*plain)(l))
}

// Every returns Interval as a duration ("1m", "30s" or plain seconds).
func (l LogAddress) Every() (time.Duration, error) { return interval(l.Interval) }

// ServerInfo asks every game server for its version, OS and VAC state
// (A2S_INFO on the game port, no RCON password needed; also read from the
// RCON status) and stores them in hlstats_Servers. In the config file it is
// a mapping, or just true / false.
type ServerInfo struct {
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"` // Go duration ("5m") or seconds
}

// MinServerInfoInterval is the shortest features.server_info.interval.
const MinServerInfoInterval = 30 * time.Second

// UnmarshalYAML accepts "server_info: true" and the mapping.
func (s *ServerInfo) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&s.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.server_info: want true / false or enabled, interval", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "interval":
		default:
			return fmt.Errorf("line %d: features.server_info: unknown key %q (enabled, interval)", n.Content[i].Line, k)
		}
	}
	type plain ServerInfo
	return n.Decode((*plain)(s))
}

// Every returns Interval as a duration ("5m" or plain seconds).
func (s ServerInfo) Every() (time.Duration, error) { return interval(s.Interval) }

// LogSecret checks the secret (sv_logsecret) in the log packets against
// hlstats_Servers.log_secret: a server with a stored secret must send it,
// a server without one is not checked. The secret is set by hand (gamewe
// server set -logsecret or the web admin), never by the collector. In the
// config file it is a mapping, or just false / true.
type LogSecret struct {
	Enabled bool `yaml:"enabled"`
	// Obsolete names the settings of the removed auto mode found in the
	// file ("enabled: auto", "interval"); they are ignored and logged at
	// start. Not read from YAML.
	Obsolete []string `yaml:"-"`
}

// decodeEnabled reads false / true and the removed auto (= true).
func (l *LogSecret) decodeEnabled(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && strings.EqualFold(strings.TrimSpace(n.Value), "auto") {
		l.Enabled = true
		l.Obsolete = append(l.Obsolete, "enabled: auto (now true)")
		return nil
	}
	if n.Kind != yaml.ScalarNode || n.Decode(&l.Enabled) != nil {
		return fmt.Errorf("line %d: features.log_secret.enabled: want false or true", n.Line)
	}
	return nil
}

// UnmarshalYAML accepts "log_secret: true" (or false) and the mapping.
func (l *LogSecret) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return l.decodeEnabled(n)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.log_secret: want false / true or enabled", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled":
			if err := l.decodeEnabled(n.Content[i+1]); err != nil {
				return err
			}
		case "interval":
			l.Obsolete = append(l.Obsolete, "interval")
		default:
			return fmt.Errorf("line %d: features.log_secret: unknown key %q (enabled)", n.Content[i].Line, k)
		}
	}
	return nil
}

// DailyAwards is when the daily awards of the previous day are decided.
// In the config file it is a mapping, or (legacy form) just true / false.
type DailyAwards struct {
	Enabled  bool   `yaml:"enabled"`
	Timezone string `yaml:"timezone"` // IANA name, e.g. UTC or Europe/Berlin
	Time     string `yaml:"time"`     // HH:MM in Timezone
}

// UnmarshalYAML accepts "daily_awards: true" and the mapping.
func (d *DailyAwards) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&d.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.daily_awards: want true / false or enabled, timezone, time", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "timezone", "time":
		default:
			return fmt.Errorf("line %d: features.daily_awards: unknown key %q (enabled, timezone, time)", n.Content[i].Line, k)
		}
	}
	type plain DailyAwards
	return n.Decode((*plain)(d))
}

// Location is the time zone of the daily awards.
func (d DailyAwards) Location() (*time.Location, error) { return time.LoadLocation(d.Timezone) }

// At returns hour and minute of Time.
func (d DailyAwards) At() (hour, minute int, err error) {
	t, err := time.Parse("15:04", d.Time)
	return t.Hour(), t.Minute(), err
}

// DefaultListenPort is the default UDP port for game-server logs.
const DefaultListenPort = 41337

// UDP returns the listen address as host:port.
func (l Listen) UDP() string {
	return net.JoinHostPort(l.Address, strconv.Itoa(l.Port))
}

// Database is the connection to the HLstatsX database: MariaDB / MySQL or
// PostgreSQL.
type Database struct {
	Type           string `yaml:"type"`
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	User           string `yaml:"user"`
	Password       string `yaml:"password"`
	PasswordFile   string `yaml:"password_file"`
	Name           string `yaml:"name"`
	MaxOpenConns   int    `yaml:"max_open_conns"`
	ConnectTimeout int    `yaml:"connect_timeout"`
	TLS            TLS    `yaml:"tls"`
	// AutoMigrate lets `gamewe run` bring an outdated schema up to date
	// itself (what `gamewe db migrate -f` does) instead of refusing to
	// start. An HLstatsX database without schema versions is always
	// taken over by hand.
	AutoMigrate bool `yaml:"auto_migrate"`
}

type TLS struct {
	Mode       string `yaml:"mode"`
	CAFile     string `yaml:"ca_file"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	ServerName string `yaml:"server_name"`
}

// Defaults returns a configuration with all optional values filled in.
func Defaults() Config {
	return Config{Listen: Listen{Address: "0.0.0.0", Port: DefaultListenPort, UDPEnabled: true,
		HTTP: ListenHTTP{Address: "0.0.0.0", Port: DefaultHTTPPort, MaxBody: "128KiB", ReadTimeout: "10s",
			MaxConnections: 64, MaxConnectionsPerIP: 8}}, Database: Database{
		Type:           MySQL,
		MaxOpenConns:   8,
		ConnectTimeout: 10,
		TLS:            TLS{Mode: TLSDisable},
		AutoMigrate:    true,
	}, GeoIP: GeoIP{AutoUpdate: true}, Features: Features{RconStatus: RconStatus{Enabled: true, Interval: "10s"},
		LogAddress:   LogAddress{Enabled: true, Interval: "1m", HTTP: true},
		LogSecret:    LogSecret{Enabled: true},
		DailyAwards:  DailyAwards{Enabled: true, Timezone: "UTC", Time: "00:00"},
		ServerInfo:   ServerInfo{Enabled: true, Interval: "5m"},
		CS2Hitgroups: Toggle{Enabled: true}}}
}

// Load reads, decodes and validates the YAML file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

// legacy names top-level keys of an old config format, which now live elsewhere.
var legacy = map[string]string{
	"servers":  "game servers are read from hlstats_Servers",
	"commands": "in-game command settings are hlstats_Options keys (gamewe_*)",
	"stats":    "points and rewards come from hlstats_Options, hlstats_Actions and hlstats_Weapons",
	"log":      "the log level is the -log-level flag of 'gamewe run'",
}

// Parse decodes and validates YAML configuration data. Unknown keys are
// rejected so typos do not go unnoticed.
func Parse(data []byte) (Config, error) {
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err == nil {
		for k := range top {
			if hint, ok := legacy[k]; ok {
				return Config{}, fmt.Errorf("config key %q is no longer used: the config file only holds listen:, database: and features:; %s", k, hint)
			}
		}
		if l, ok := top["listen"].(map[string]any); ok {
			if _, ok := l["http_port"]; ok {
				return Config{}, fmt.Errorf("listen.http_port is no longer used: the HTTP input for CS2 is listen.http (enabled, address, port)")
			}
			if v, ok := l["udp"]; ok && v != nil {
				if _, isMap := v.(map[string]any); !isMap {
					return Config{}, fmt.Errorf("listen.udp as an address is no longer used: listen.udp is a mapping (enabled, address, port, public_address)")
				}
				for _, k := range []string{"address", "port", "public_address"} {
					if _, ok := l[k]; ok {
						return Config{}, fmt.Errorf("listen.%s and listen.udp are both set: move it to listen.udp.%s", k, k)
					}
				}
			}
			for _, k := range []string{"log_secret", "allowed_sources", "queue_size"} {
				if _, ok := l[k]; ok {
					return Config{}, fmt.Errorf("listen.%s is no longer used: listen has udp and http; game servers are allowed by hlstats_Servers, the log secret is features.log_secret", k)
				}
			}
		}
	}
	cfg := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, errors.New("config is empty")
		}
		return Config{}, fmt.Errorf("parse yaml: %w", err)
	}
	if u := cfg.Listen.UDPConf; u != nil {
		if u.Enabled != nil {
			cfg.Listen.UDPEnabled = *u.Enabled
		}
		if u.Address != "" {
			cfg.Listen.Address = u.Address
		}
		if u.Port != 0 {
			cfg.Listen.Port = u.Port
		}
		cfg.Listen.PublicAddress = u.PublicAddress
	}
	d := &cfg.Database
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "mariadb", "mysql":
		d.Type = MySQL
	case "postgres", "postgresql", "pgsql":
		d.Type = Postgres
	}
	if d.Port == 0 {
		d.Port = 3306
		if d.Type == Postgres {
			d.Port = 5432
		}
	}
	d.TLS.Mode = strings.ToLower(strings.TrimSpace(d.TLS.Mode))
	if d.PasswordFile != "" {
		if d.Password != "" {
			return Config{}, errors.New("database: set either password or password_file, not both")
		}
		pw, err := os.ReadFile(d.PasswordFile)
		if err != nil {
			return Config{}, fmt.Errorf("database.password_file: %w", err)
		}
		d.Password = strings.TrimRight(string(pw), "\r\n")
	}
	return cfg, cfg.Validate()
}

// validate checks listen.http (also while it is off, so a mistake shows
// before it is switched on).
func (h ListenHTTP) validate() error {
	if net.ParseIP(h.Address) == nil {
		return fmt.Errorf("listen.http.address %q: must be an IP address (0.0.0.0 for all)", h.Address)
	}
	if h.Port < 1 || h.Port > 65535 {
		return fmt.Errorf("listen.http.port %d out of range", h.Port)
	}
	if h.PublicURL != "" {
		u, err := url.Parse(h.PublicURL)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
			u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("listen.http.public_url %q: must be http://host:port (CS2 sends plain HTTP; no path, the collector adds it)", h.PublicURL)
		}
	}
	if h.Token != "" && (!reToken.MatchString(h.Token) || strings.EqualFold(h.Token, "change-me")) {
		return errors.New("listen.http.token: want 16 to 128 letters, digits, - or _ (it is part of the URL), not the example value")
	}
	if n, err := h.BodyLimit(); err != nil || n < MinHTTPBody || n > MaxHTTPBody {
		return fmt.Errorf("listen.http.max_body %q: want a size from 4KiB to 16MiB, e.g. \"128KiB\"", h.MaxBody)
	}
	if d, err := h.Timeout(); err != nil || d < MinHTTPReadTimeout || d > MaxHTTPReadTimeout {
		return fmt.Errorf("listen.http.read_timeout %q: want a duration from %v to %v, e.g. \"10s\"", h.ReadTimeout, MinHTTPReadTimeout, MaxHTTPReadTimeout)
	}
	if h.MaxConnections < 1 || h.MaxConnections > MaxHTTPConnections {
		return fmt.Errorf("listen.http.max_connections %d: want 1 to %d", h.MaxConnections, MaxHTTPConnections)
	}
	if h.MaxConnectionsPerIP < 1 || h.MaxConnectionsPerIP > h.MaxConnections {
		return fmt.Errorf("listen.http.max_connections_per_ip %d: want 1 to max_connections (%d)", h.MaxConnectionsPerIP, h.MaxConnections)
	}
	_, err := h.Proxies()
	return err
}

// Validate checks all values and returns the first problem found.
func (c Config) Validate() error {
	if net.ParseIP(c.Listen.Address) == nil {
		return fmt.Errorf("listen.address %q: must be an IP address (0.0.0.0 for all)", c.Listen.Address)
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("listen.port %d out of range", c.Listen.Port)
	}
	if err := c.Listen.HTTP.validate(); err != nil {
		return err
	}
	if !c.Listen.UDPEnabled && !c.Listen.HTTP.Enabled {
		return errors.New("listen: udp and http are both off; enable at least one input for the game server logs")
	}
	if d, err := c.Features.RconStatus.Every(); err != nil || d < MinStatusInterval {
		return fmt.Errorf("features.rcon_status.interval %q: want a duration of at least %v, e.g. \"10s\"", c.Features.RconStatus.Interval, MinStatusInterval)
	}
	if d, err := c.Features.LogAddress.Every(); err != nil || d < MinLogAddressInterval {
		return fmt.Errorf("features.log_address.interval %q: want a duration of at least %v, e.g. \"1m\"", c.Features.LogAddress.Interval, MinLogAddressInterval)
	}
	if d, err := c.Features.ServerInfo.Every(); err != nil || d < MinServerInfoInterval {
		return fmt.Errorf("features.server_info.interval %q: want a duration of at least %v, e.g. \"5m\"", c.Features.ServerInfo.Interval, MinServerInfoInterval)
	}
	if _, err := c.Features.DailyAwards.Location(); err != nil {
		return fmt.Errorf("features.daily_awards.timezone %q: %v (an IANA name such as UTC or Europe/Berlin)", c.Features.DailyAwards.Timezone, err)
	}
	if _, _, err := c.Features.DailyAwards.At(); err != nil {
		return fmt.Errorf("features.daily_awards.time %q: want HH:MM, e.g. \"00:00\"", c.Features.DailyAwards.Time)
	}
	if pa := c.Listen.PublicAddress; pa != "" {
		if _, err := netip.ParseAddrPort(pa); err != nil {
			return fmt.Errorf("listen.public_address %q: must be ip:port", pa)
		}
	}
	d := c.Database
	if d.Type != MySQL && d.Type != Postgres {
		return fmt.Errorf("database.type %q: must be mariadb (or mysql) or postgres", d.Type)
	}
	if d.Host == "" || d.User == "" || d.Name == "" {
		return errors.New("database: host, user and name are required")
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("database.port %d out of range", d.Port)
	}
	if d.MaxOpenConns < 1 {
		return errors.New("database.max_open_conns must be at least 1")
	}
	if d.ConnectTimeout < 1 {
		return errors.New("database.connect_timeout must be at least 1 (seconds)")
	}
	t := d.TLS
	switch t.Mode {
	case TLSDisable:
		if t.CAFile != "" || t.CertFile != "" || t.KeyFile != "" || t.ServerName != "" {
			return errors.New("database.tls: certificate options are set but mode is disable")
		}
	case TLSRequire, TLSVerifyCA, TLSVerifyFull:
	default:
		return fmt.Errorf("database.tls.mode %q: must be disable, require, verify-ca or verify-full", t.Mode)
	}
	if (t.CertFile == "") != (t.KeyFile == "") {
		return errors.New("database.tls: cert_file and key_file must be set together")
	}
	return nil
}
