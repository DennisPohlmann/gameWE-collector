// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads the gameWE collector's YAML configuration: the
// database connection and the inputs for game-server logs (UDP, and HTTP
// for Counter-Strike 2). Game
// servers and all statistics settings are read from the database
// (hlstats_Servers, hlstats_Options, ...).
package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
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

// DefaultTablePrefix is the prefix of the table names (hlstats_Players),
// as DB_PREFIX of HLstatsX:CE.
const DefaultTablePrefix = "hlstats"

// MaxTablePrefix is the longest table prefix: the longest index name of
// the schema must stay within PostgreSQL's 63 characters.
const MaxTablePrefix = 16

// NormalizeTablePrefix returns the table prefix without one trailing "_"
// (hlstats_ and hlstats are the same) and the default for "".
func NormalizeTablePrefix(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "_")
	if s == "" {
		return DefaultTablePrefix
	}
	return s
}

// CheckTablePrefix checks a normalized table prefix: a letter, then
// letters, digits and "_", at most MaxTablePrefix characters. It may not
// start with hlstats_ (the names of the code are rewritten by prefix,
// internal/database/prefix.go) nor be gamewe.
func CheckTablePrefix(p string) error {
	if p == "" || len(p) > MaxTablePrefix {
		return fmt.Errorf("%q: want 1 to %d characters", p, MaxTablePrefix)
	}
	for i, c := range p {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || c != '_' && (c < '0' || c > '9')) {
			return fmt.Errorf("%q: only letters, digits and _, starting with a letter", p)
		}
	}
	if strings.HasSuffix(p, "_") {
		return fmt.Errorf("%q: may end with one _ only", p)
	}
	l := strings.ToLower(p)
	if strings.HasPrefix(l, DefaultTablePrefix+"_") || l == "gamewe" {
		return fmt.Errorf("%q: may not start with %s_ or be gamewe", p, DefaultTablePrefix)
	}
	return nil
}

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
	// Timezone is the collector's time zone (IANA name) for the daily jobs
	// without one of their own; empty: the server's (TZ, /etc/localtime).
	Timezone string `yaml:"timezone"`
}

// Location is the collector's time zone (Timezone, else the server's),
// e.g. for the scheduled ranking resets.
func (c Config) Location() (*time.Location, error) {
	return zone("", c.Timezone)
}

// zone resolves the time zone of a daily job: its own, else the
// collector's, else the server's local zone.
func zone(job, global string) (*time.Location, error) {
	name := cmp.Or(job, global)
	if name == "" {
		return time.Local, nil // time.LoadLocation("") would be UTC
	}
	return time.LoadLocation(name)
}

// clock reads HH:MM.
func clock(v string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", v)
	return t.Hour(), t.Minute(), err
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
	// InactivePlayers moves players without play for a while to
	// hlstats_PlayersInactive (off by default).
	InactivePlayers InactivePlayers `yaml:"inactive_players"`
	// UpdateCheck asks GitHub for the latest release: at start and daily in
	// "run" (log), before the other commands (stderr). Never installs.
	UpdateCheck Toggle `yaml:"update_check"`
	// SourceBans records the SourceBans bans of players kicked as banned
	// (hlstats_Players_Bans) and takes them out of the ranking until the
	// ban ends; it needs the address of the ban list (web admin).
	SourceBans SourceBans `yaml:"sourcebans"`
	// ScheduledResets is the time of day of the scheduled ranking resets
	// (the schedules themselves are in the database, set in the web admin).
	ScheduledResets ScheduledResets `yaml:"scheduled_resets"`
}

// ScheduledResets: the time of day (HH:MM in the collector's timezone) a
// period of a reset schedule ends and the next one starts.
type ScheduledResets struct {
	Time string `yaml:"time"`
}

// At returns Time as hour and minute.
func (s ScheduledResets) At() (hour, minute int, err error) { return clock(s.Time) }

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

// InactivePlayers moves players whose last event is older than the days of
// hlstats_Options MinActivity (default 28) and who are not online from
// hlstats_Players to hlstats_PlayersInactive, once a day at Time. A
// returning player is moved back. In the config file it is a mapping, or
// just true / false.
type InactivePlayers struct {
	Enabled bool   `yaml:"enabled"`
	Time    string `yaml:"time"` // HH:MM in Location
	// Timezone (IANA name) of Time; empty: the collector's timezone, else
	// the server's.
	Timezone string `yaml:"timezone"`
}

// UnmarshalYAML accepts "inactive_players: true" and the mapping.
func (p *InactivePlayers) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.inactive_players: want true / false or enabled, time, timezone", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "time", "timezone":
		default:
			return fmt.Errorf("line %d: features.inactive_players: unknown key %q (enabled, time, timezone)", n.Content[i].Line, k)
		}
	}
	type plain InactivePlayers
	return n.Decode((*plain)(p))
}

// Location is the time zone of the run: Timezone, else the collector's
// (global, Config.Timezone), else the server's.
func (p InactivePlayers) Location(global string) (*time.Location, error) {
	return zone(p.Timezone, global)
}

// At returns hour and minute of Time.
func (p InactivePlayers) At() (hour, minute int, err error) { return clock(p.Time) }

// SourceBans looks up players kicked with SourceBans' ban message (it names
// the ban list's host in every language, or one of Phrases) on the public
// ban list of hlstats_Options gamewe_sourcebans_url, stores a running ban in
// hlstats_Players_Bans and takes the player out of the ranking; every
// Interval the running bans are checked again and ended ones put the player
// back. In the config file it is a mapping, or just true / false.
type SourceBans struct {
	Enabled  bool     `yaml:"enabled"`
	Interval string   `yaml:"interval"` // Go duration ("30m") or seconds
	Phrases  []string `yaml:"phrases"`  // further kick texts that mean a ban (case insensitive)
	// AllowPrivate lets the address be on the LAN or localhost.
	AllowPrivate bool `yaml:"allow_private"`
}

// MinSourceBansInterval is the shortest features.sourcebans.interval.
const MinSourceBansInterval = 5 * time.Minute

// UnmarshalYAML accepts "sourcebans: true" and the mapping.
func (s *SourceBans) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&s.Enabled)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.sourcebans: want true / false or enabled, interval, phrases, allow_private", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "interval", "phrases", "allow_private":
		default:
			return fmt.Errorf("line %d: features.sourcebans: unknown key %q (enabled, interval, phrases, allow_private)", n.Content[i].Line, k)
		}
	}
	type plain SourceBans
	return n.Decode((*plain)(s))
}

// Every returns Interval as a duration ("30m" or plain seconds).
func (s SourceBans) Every() (time.Duration, error) { return interval(s.Interval) }

// Location is the time zone the ban list shows its dates in: the
// collector's (global, Config.Timezone), else the server's.
func (s SourceBans) Location(global string) (*time.Location, error) { return zone("", global) }

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

// ObsoleteWarning says that the settings in Obsolete are ignored ("" if
// there are none).
func (l LogSecret) ObsoleteWarning() string {
	if len(l.Obsolete) == 0 {
		return ""
	}
	return strings.Join(l.Obsolete, ", ") + " ignored, the automatic log secret was removed (set it per server with gamewe server set -logsecret)"
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
	Enabled bool `yaml:"enabled"`
	// Timezone (IANA name, e.g. UTC or Europe/Berlin) of the day and Time;
	// empty: the collector's timezone, else the server's.
	Timezone string `yaml:"timezone"`
	Time     string `yaml:"time"` // HH:MM in Timezone
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

// Location is the time zone of the daily awards: Timezone, else the
// collector's (global, Config.Timezone), else the server's.
func (d DailyAwards) Location(global string) (*time.Location, error) {
	return zone(d.Timezone, global)
}

// At returns hour and minute of Time.
func (d DailyAwards) At() (hour, minute int, err error) { return clock(d.Time) }

// DefaultListenPort is the default UDP port for game-server logs.
const DefaultListenPort = 41337

// UDP returns the listen address as host:port.
func (l Listen) UDP() string {
	return net.JoinHostPort(l.Address, strconv.Itoa(l.Port))
}

// Database is the connection to the HLstatsX database: MariaDB / MySQL or
// PostgreSQL.
type Database struct {
	Type         string `yaml:"type"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	Name         string `yaml:"name"`
	// TablePrefix is the prefix of the table names: <prefix>_Players
	// (default hlstats, as DB_PREFIX of HLstatsX:CE; NormalizeTablePrefix).
	TablePrefix    string `yaml:"table_prefix"`
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
		TablePrefix:    DefaultTablePrefix,
		MaxOpenConns:   8,
		ConnectTimeout: 10,
		TLS:            TLS{Mode: TLSDisable},
		AutoMigrate:    true,
	}, GeoIP: GeoIP{AutoUpdate: true}, Features: Features{RconStatus: RconStatus{Enabled: true, Interval: "10s"},
		LogAddress:      LogAddress{Enabled: true, Interval: "1m", HTTP: true},
		LogSecret:       LogSecret{Enabled: true},
		DailyAwards:     DailyAwards{Enabled: true, Time: "00:00"},
		ScheduledResets: ScheduledResets{Time: "00:00"},
		ServerInfo:      ServerInfo{Enabled: true, Interval: "5m"},
		CS2Hitgroups:    Toggle{Enabled: true},
		UpdateCheck:     Toggle{Enabled: true},
		InactivePlayers: InactivePlayers{Time: "01:00"},
		SourceBans:      SourceBans{Enabled: true, Interval: "30m"}}}
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

// Problem is one invalid setting: its key (listen.http.port, ...) and what
// is wrong with it.
type Problem struct {
	Key string
	Msg string
	// drop: the key is of an older format and is left out when the rest of
	// the file is checked (Lint).
	drop bool
}

func (p Problem) Error() string { return p.Key + ": " + p.Msg }

func problem(key, format string, args ...any) Problem {
	return Problem{Key: key, Msg: fmt.Sprintf(format, args...)}
}

// legacyProblems finds the keys of older config formats in data (nil if it
// is no YAML mapping; the decoder reports that).
func legacyProblems(data []byte) []Problem {
	var top map[string]any
	if yaml.Unmarshal(data, &top) != nil {
		return nil
	}
	var out []Problem
	for _, k := range slices.Sorted(maps.Keys(top)) {
		if hint, ok := legacy[k]; ok {
			out = append(out, Problem{k, "no longer used: the config file only holds listen:, database:, geoip: and features:; " + hint, true})
		}
	}
	l, ok := top["listen"].(map[string]any)
	if !ok {
		return out
	}
	if _, ok := l["http_port"]; ok {
		out = append(out, Problem{"listen.http_port", "no longer used: the HTTP input for CS2 is listen.http (enabled, address, port)", true})
	}
	if v, ok := l["udp"]; ok && v != nil {
		if _, isMap := v.(map[string]any); !isMap {
			out = append(out, Problem{"listen.udp", "an address is no longer used here: listen.udp is a mapping (enabled, address, port, public_address)", true})
		} else {
			for _, k := range []string{"address", "port", "public_address"} {
				if _, ok := l[k]; ok {
					out = append(out, problem("listen."+k, "listen.%s and listen.udp are both set: move it to listen.udp.%s", k, k))
				}
			}
		}
	}
	for _, k := range []string{"log_secret", "allowed_sources", "queue_size"} {
		if _, ok := l[k]; ok {
			out = append(out, Problem{"listen." + k, "no longer used: listen has udp and http; game servers are allowed by hlstats_Servers, the log secret is features.log_secret", true})
		}
	}
	return out
}

// Parse decodes and validates YAML configuration data. Unknown keys are
// rejected so typos do not go unnoticed.
func Parse(data []byte) (Config, error) {
	if p := legacyProblems(data); len(p) > 0 {
		return Config{}, p[0]
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
	if p := cfg.finish(); len(p) > 0 {
		return Config{}, p[0]
	}
	return cfg, cfg.Validate()
}

// finish fills in what follows from the decoded values: listen.udp over
// the older listen keys, the database type and port, the password from
// password_file.
func (cfg *Config) finish() []Problem {
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
	d.TablePrefix = NormalizeTablePrefix(d.TablePrefix)
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
			return []Problem{problem("database.password_file", "set either password or password_file, not both")}
		}
		pw, err := os.ReadFile(d.PasswordFile)
		if err != nil {
			return []Problem{problem("database.password_file", "%v", err)}
		}
		d.Password = strings.TrimRight(string(pw), "\r\n")
	}
	return nil
}

// problems checks listen.http (also while it is off, so a mistake shows
// before it is switched on).
func (h ListenHTTP) problems() []Problem {
	var out []Problem
	if net.ParseIP(h.Address) == nil {
		out = append(out, problem("listen.http.address", "%q must be an IP address (0.0.0.0 for all)", h.Address))
	}
	if h.Port < 1 || h.Port > 65535 {
		out = append(out, problem("listen.http.port", "%d out of range (1 to 65535)", h.Port))
	}
	if h.PublicURL != "" {
		u, err := url.Parse(h.PublicURL)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
			u.RawQuery != "" || u.Fragment != "" {
			shown := strconv.Quote(h.PublicURL)
			if err == nil && u.User != nil {
				shown = "the URL" // user:password in it are not printed
			}
			out = append(out, problem("listen.http.public_url", "%s must be http://host:port (CS2 sends plain HTTP; no user, no path, the collector adds it)", shown))
		}
	}
	if h.Token != "" && (!reToken.MatchString(h.Token) || strings.EqualFold(h.Token, "change-me")) {
		out = append(out, problem("listen.http.token", "want 16 to 128 letters, digits, - or _ (it is part of the URL), not the example value"))
	}
	if n, err := h.BodyLimit(); err != nil || n < MinHTTPBody || n > MaxHTTPBody {
		out = append(out, problem("listen.http.max_body", "%q: want a size from 4KiB to 16MiB, e.g. \"128KiB\"", h.MaxBody))
	}
	if d, err := h.Timeout(); err != nil || d < MinHTTPReadTimeout || d > MaxHTTPReadTimeout {
		out = append(out, problem("listen.http.read_timeout", "%q: want a duration from %v to %v, e.g. \"10s\"", h.ReadTimeout, MinHTTPReadTimeout, MaxHTTPReadTimeout))
	}
	if h.MaxConnections < 1 || h.MaxConnections > MaxHTTPConnections {
		out = append(out, problem("listen.http.max_connections", "%d: want 1 to %d", h.MaxConnections, MaxHTTPConnections))
	} else if h.MaxConnectionsPerIP < 1 || h.MaxConnectionsPerIP > h.MaxConnections {
		out = append(out, problem("listen.http.max_connections_per_ip", "%d: want 1 to max_connections (%d)", h.MaxConnectionsPerIP, h.MaxConnections))
	}
	if _, err := h.Proxies(); err != nil {
		out = append(out, problem("listen.http.trusted_proxies", "%s", strings.TrimPrefix(err.Error(), "listen.http.trusted_proxies: ")))
	}
	return out
}

// Validate checks all values and returns the first problem found.
func (c Config) Validate() error {
	if p := c.Problems(); len(p) > 0 {
		return p[0]
	}
	return nil
}

// Problems checks all values and returns every problem found, in the
// order of the file's sections.
func (c Config) Problems() []Problem {
	var out []Problem
	add := func(key, format string, args ...any) { out = append(out, problem(key, format, args...)) }
	if net.ParseIP(c.Listen.Address) == nil {
		add("listen.address", "%q must be an IP address (0.0.0.0 for all)", c.Listen.Address)
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		add("listen.port", "%d out of range (1 to 65535)", c.Listen.Port)
	}
	out = append(out, c.Listen.HTTP.problems()...)
	if !c.Listen.UDPEnabled && !c.Listen.HTTP.Enabled {
		add("listen", "udp and http are both off; enable at least one input for the game server logs")
	}
	if pa := c.Listen.PublicAddress; pa != "" {
		if _, err := netip.ParseAddrPort(pa); err != nil {
			add("listen.public_address", "%q must be ip:port", pa)
		}
	}
	if d, err := c.Features.RconStatus.Every(); err != nil || d < MinStatusInterval {
		add("features.rcon_status.interval", "%q: want a duration of at least %v, e.g. \"10s\"", c.Features.RconStatus.Interval, MinStatusInterval)
	}
	if d, err := c.Features.LogAddress.Every(); err != nil || d < MinLogAddressInterval {
		add("features.log_address.interval", "%q: want a duration of at least %v, e.g. \"1m\"", c.Features.LogAddress.Interval, MinLogAddressInterval)
	}
	if d, err := c.Features.ServerInfo.Every(); err != nil || d < MinServerInfoInterval {
		add("features.server_info.interval", "%q: want a duration of at least %v, e.g. \"5m\"", c.Features.ServerInfo.Interval, MinServerInfoInterval)
	}
	if d, err := c.Features.SourceBans.Every(); err != nil || d < MinSourceBansInterval {
		add("features.sourcebans.interval", "%q: want a duration of at least %v, e.g. \"30m\"", c.Features.SourceBans.Interval, MinSourceBansInterval)
	}
	for i, p := range c.Features.SourceBans.Phrases {
		if l := len(strings.TrimSpace(p)); l < 4 || l > 200 {
			add(fmt.Sprintf("features.sourcebans.phrases[%d]", i), "%q: want 4 to 200 characters (a part of the kick message that means a ban)", p)
		}
	}
	const tzHint = "(an IANA name such as UTC or Europe/Berlin; empty: the server's time zone)"
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			add("timezone", "%q: %v %s", c.Timezone, err, tzHint)
		}
	}
	if tz := c.Features.DailyAwards.Timezone; tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			add("features.daily_awards.timezone", "%q: %v %s", tz, err, tzHint)
		}
	}
	ip := c.Features.InactivePlayers
	if ip.Timezone != "" {
		if _, err := time.LoadLocation(ip.Timezone); err != nil {
			add("features.inactive_players.timezone", "%q: %v %s", ip.Timezone, err, tzHint)
		}
	}
	if _, _, err := ip.At(); err != nil {
		add("features.inactive_players.time", "%q: want HH:MM, e.g. \"01:00\"", ip.Time)
	}
	if _, _, err := c.Features.ScheduledResets.At(); err != nil {
		add("features.scheduled_resets.time", "%q: want HH:MM, e.g. \"00:00\"", c.Features.ScheduledResets.Time)
	}
	if _, _, err := c.Features.DailyAwards.At(); err != nil {
		add("features.daily_awards.time", "%q: want HH:MM, e.g. \"00:00\"", c.Features.DailyAwards.Time)
	}
	d := c.Database
	if d.Type != MySQL && d.Type != Postgres {
		add("database.type", "%q must be mariadb (or mysql) or postgres", d.Type)
	}
	for _, f := range []struct{ key, v string }{{"host", d.Host}, {"user", d.User}, {"name", d.Name}} {
		if f.v == "" {
			add("database."+f.key, "required (database: host, user and name are required)")
		}
	}
	if err := CheckTablePrefix(d.TablePrefix); err != nil {
		add("database.table_prefix", "%v", err)
	}
	if d.Port < 1 || d.Port > 65535 {
		add("database.port", "%d out of range (1 to 65535)", d.Port)
	}
	if d.MaxOpenConns < 1 {
		add("database.max_open_conns", "must be at least 1")
	}
	if d.ConnectTimeout < 1 {
		add("database.connect_timeout", "must be at least 1 (seconds)")
	}
	t := d.TLS
	switch t.Mode {
	case TLSDisable:
		if t.CAFile != "" || t.CertFile != "" || t.KeyFile != "" || t.ServerName != "" {
			add("database.tls", "certificate options are set but mode is disable")
		}
	case TLSRequire, TLSVerifyCA, TLSVerifyFull:
	default:
		add("database.tls.mode", "%q must be disable, require, verify-ca or verify-full", t.Mode)
	}
	if (t.CertFile == "") != (t.KeyFile == "") {
		add("database.tls", "cert_file and key_file must be set together")
	}
	return out
}
