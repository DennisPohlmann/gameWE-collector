// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads the gameWE collector's YAML configuration: the
// database connection and the UDP address for game-server logs. Game
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
	"os"
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

// Listen is where the collector receives game-server logs (logaddress_add).
type Listen struct {
	Address string `yaml:"address"` // IP to listen on, default 0.0.0.0
	Port    int    `yaml:"port"`    // UDP port, default 41337
	// PublicAddress is the ip:port the game servers send their logs to
	// (logaddress_add); empty: the collector's address of the RCON
	// connection and Port.
	PublicAddress string `yaml:"public_address"`
}

// Features switches the collector's optional jobs on or off; all are on
// by default.
type Features struct {
	RconStatus  RconStatus  `yaml:"rcon_status"`  // poll every server's "status" over RCON
	LogAddress  LogAddress  `yaml:"log_address"`  // register the collector with logaddress_add
	LogSecret   LogSecret   `yaml:"log_secret"`   // check sv_logsecret of the log packets
	DailyAwards DailyAwards `yaml:"daily_awards"` // the daily awards job
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
		case "enabled", "interval":
		default:
			return fmt.Errorf("line %d: features.log_address: unknown key %q (enabled, interval)", n.Content[i].Line, k)
		}
	}
	type plain LogAddress
	return n.Decode((*plain)(l))
}

// Every returns Interval as a duration ("1m", "30s" or plain seconds).
func (l LogAddress) Every() (time.Duration, error) { return interval(l.Interval) }

// Log secret modes (features.log_secret.enabled).
const (
	SecretOff  = "false" // packets are not checked
	SecretOn   = "true"  // packets must carry hlstats_Servers.log_secret
	SecretAuto = "auto"  // as true, and the collector sets and rotates sv_logsecret over RCON
)

// LogSecret checks the secret (sv_logsecret) in the log packets against
// hlstats_Servers.log_secret. In the config file it is a mapping, or just
// false / true / auto.
type LogSecret struct {
	Enabled  SecretMode `yaml:"enabled"`
	Interval string     `yaml:"interval"` // rotation in auto mode: Go duration ("24h") or seconds
}

// SecretMode is SecretOff, SecretOn or SecretAuto; YAML false / true / auto.
type SecretMode string

// UnmarshalYAML accepts the booleans and auto.
func (m *SecretMode) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && strings.EqualFold(strings.TrimSpace(n.Value), SecretAuto) {
		*m = SecretAuto
		return nil
	}
	var b bool
	if n.Kind != yaml.ScalarNode || n.Decode(&b) != nil {
		return fmt.Errorf("line %d: features.log_secret.enabled: want false, true or auto", n.Line)
	}
	*m = SecretOff
	if b {
		*m = SecretOn
	}
	return nil
}

// MinLogSecretInterval is the shortest features.log_secret.interval.
const MinLogSecretInterval = time.Minute

// UnmarshalYAML accepts "log_secret: auto" (or true / false) and the mapping.
func (l *LogSecret) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return l.Enabled.UnmarshalYAML(n)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.log_secret: want false / true / auto or enabled, interval", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "enabled", "interval":
		default:
			return fmt.Errorf("line %d: features.log_secret: unknown key %q (enabled, interval)", n.Content[i].Line, k)
		}
	}
	type plain LogSecret
	return n.Decode((*plain)(l))
}

// Every returns Interval as a duration ("24h", "1m" or plain seconds).
func (l LogSecret) Every() (time.Duration, error) { return interval(l.Interval) }

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
	return Config{Listen: Listen{Address: "0.0.0.0", Port: DefaultListenPort}, Database: Database{
		Type:           MySQL,
		MaxOpenConns:   8,
		ConnectTimeout: 10,
		TLS:            TLS{Mode: TLSDisable},
	}, GeoIP: GeoIP{AutoUpdate: true}, Features: Features{RconStatus: RconStatus{Enabled: true, Interval: "10s"},
		LogAddress:  LogAddress{Enabled: true, Interval: "1m"},
		LogSecret:   LogSecret{Enabled: SecretAuto, Interval: "24h"},
		DailyAwards: DailyAwards{Enabled: true, Timezone: "UTC", Time: "00:00"}}}
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
				return Config{}, fmt.Errorf("listen.http_port is no longer used: the HTTP logs of CS2 are not supported; remove it")
			}
			for _, k := range []string{"udp", "log_secret", "allowed_sources", "queue_size"} {
				if _, ok := l[k]; ok {
					return Config{}, fmt.Errorf("listen.%s is no longer used: listen has address and port; game servers are allowed by hlstats_Servers, the log secret is features.log_secret", k)
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

// Validate checks all values and returns the first problem found.
func (c Config) Validate() error {
	if net.ParseIP(c.Listen.Address) == nil {
		return fmt.Errorf("listen.address %q: must be an IP address (0.0.0.0 for all)", c.Listen.Address)
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("listen.port %d out of range", c.Listen.Port)
	}
	if d, err := c.Features.RconStatus.Every(); err != nil || d < MinStatusInterval {
		return fmt.Errorf("features.rcon_status.interval %q: want a duration of at least %v, e.g. \"10s\"", c.Features.RconStatus.Interval, MinStatusInterval)
	}
	if d, err := c.Features.LogAddress.Every(); err != nil || d < MinLogAddressInterval {
		return fmt.Errorf("features.log_address.interval %q: want a duration of at least %v, e.g. \"1m\"", c.Features.LogAddress.Interval, MinLogAddressInterval)
	}
	switch c.Features.LogSecret.Enabled {
	case SecretOff, SecretOn, SecretAuto:
	default:
		return fmt.Errorf("features.log_secret.enabled %q: want false, true or auto", c.Features.LogSecret.Enabled)
	}
	if d, err := c.Features.LogSecret.Every(); err != nil || d < MinLogSecretInterval {
		return fmt.Errorf("features.log_secret.interval %q: want a duration of at least %v, e.g. \"24h\"", c.Features.LogSecret.Interval, MinLogSecretInterval)
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
