// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"strings"
	"testing"
	"time"
)

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
	if cfg.Database.Name == "" {
		t.Error("example has no database name")
	}
}

const minimal = `
database:
  host: db.example.org
  user: gamewe
  password: secret
  name: gamewe
  tls:
    mode: require
`

func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Type != MySQL || cfg.Database.Port != 3306 || cfg.Database.ConnectTimeout != 10 || !cfg.Database.AutoMigrate {
		t.Errorf("defaults not applied: %+v", cfg.Database)
	}
	cfg, err = Parse([]byte(strings.Replace(minimal, "database:\n", "database:\n  type: PostgreSQL\n", 1)))
	if err != nil || cfg.Database.Type != Postgres || cfg.Database.Port != 5432 {
		t.Errorf("postgres defaults: %v %+v", err, cfg.Database)
	}
	cfg, err = Parse([]byte(strings.Replace(minimal, "database:\n", "database:\n  auto_migrate: false\n", 1)))
	if err != nil || cfg.Database.AutoMigrate {
		t.Errorf("auto_migrate: false: %v %+v", err, cfg.Database)
	}
	cfg, _ = Parse([]byte(strings.Replace(minimal, "    mode: require\n", "", 1)))
	if cfg.Database.TLS.Mode != TLSDisable {
		t.Errorf("default tls mode %q, want disable", cfg.Database.TLS.Mode)
	}
	if cfg.Listen.UDP() != "0.0.0.0:41337" {
		t.Errorf("listen default %s", cfg.Listen.UDP())
	}
	cfg, err = Parse([]byte(minimal + "listen:\n  port: 27500\n"))
	if err != nil || cfg.Listen.UDP() != "0.0.0.0:27500" {
		t.Errorf("listen port only: %v %s", err, cfg.Listen.UDP())
	}
	cfg, err = Parse([]byte(minimal + "listen:\n  address: \"::\"\n"))
	if err != nil || cfg.Listen.UDP() != "[::]:41337" {
		t.Errorf("listen IPv6: %v %s", err, cfg.Listen.UDP())
	}
}

func TestRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":   minimal + "  typo_key: 1\n",
		"bad tls mode":  strings.Replace(minimal, "mode: require", "mode: prefer", 1),
		"cert w/o key":  minimal + "    cert_file: /tmp/c.pem\n",
		"tls disabled+": strings.Replace(minimal, "mode: require", "mode: disable\n    ca_file: /x", 1),
		"no host":       strings.Replace(minimal, "host: db.example.org", "", 1),
		"bad db type":   strings.Replace(minimal, "database:\n", "database:\n  type: sqlite\n", 1),
		"bad listen ip": minimal + "listen:\n  address: localhost\n",
		"bad port":      minimal + "listen:\n  port: 70000\n",
		"empty":         "",
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// Keys of the old config format get an explanation of where they went.
func TestLegacyKeys(t *testing.T) {
	old := []string{"servers:\n  x: 1\n", "stats:\n  x: 1\n", "commands:\n  x: 1\n", "log:\n  x: 1\n", "listen:\n  udp: 0.0.0.0:27500\n", "listen:\n  http_port: 41338\n"}
	for _, k := range old {
		if _, err := Parse([]byte(minimal + k)); err == nil || !strings.Contains(err.Error(), "no longer used") {
			t.Errorf("%q: got %v", k, err)
		}
	}
}

func TestFeatures(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\n"
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if f := cfg.Features; !f.RconStatus.Enabled || f.RconStatus.Interval != "10s" || !f.LogAddress.Enabled || f.LogAddress.Interval != "1m" || !f.DailyAwards.Enabled || f.DailyAwards.Timezone != "" || f.DailyAwards.Time != "00:00" {
		t.Errorf("defaults %+v, want all on", f)
	}
	cfg, err = Parse([]byte(base + "features:\n  rcon_status: false\n  daily_awards: false\nlisten:\n  public_address: 203.0.113.4:41337\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f := cfg.Features; f.RconStatus.Enabled || f.RconStatus.Interval != "10s" || !f.LogAddress.Enabled || f.DailyAwards.Enabled || cfg.Listen.PublicAddress != "203.0.113.4:41337" {
		t.Errorf("parsed %+v %q", f, cfg.Listen.PublicAddress)
	}
	if _, err := Parse([]byte(base + "listen:\n  public_address: example.org\n")); err == nil {
		t.Error("public_address without port accepted")
	}
	cfg, err = Parse([]byte(base + "features:\n  rcon_status:\n    interval: 3s\n"))
	if d, _ := cfg.Features.RconStatus.Every(); err != nil || !cfg.Features.RconStatus.Enabled || d != 3*time.Second {
		t.Errorf("rcon_status mapping: %+v %v", cfg.Features.RconStatus, err)
	}
	cfg, err = Parse([]byte(base + "features:\n  rcon_status:\n    enabled: true\n    interval: 15\n"))
	if d, _ := cfg.Features.RconStatus.Every(); err != nil || d != 15*time.Second {
		t.Errorf("rcon_status seconds: %+v %v", cfg.Features.RconStatus, err)
	}
	for _, bad := range []string{"1s", "fast", "0"} {
		if _, err := Parse([]byte(base + "features:\n  rcon_status:\n    interval: " + bad + "\n")); err == nil {
			t.Errorf("rcon_status.interval %q accepted", bad)
		}
	}
	cfg, err = Parse([]byte(base + "features:\n  log_address: false\n"))
	if err != nil || cfg.Features.LogAddress.Enabled || cfg.Features.LogAddress.Interval != "1m" {
		t.Errorf("log_address legacy form: %+v %v", cfg.Features.LogAddress, err)
	}
	cfg, err = Parse([]byte(base + "features:\n  log_address:\n    interval: 5m\n"))
	if d, _ := cfg.Features.LogAddress.Every(); err != nil || !cfg.Features.LogAddress.Enabled || d != 5*time.Minute {
		t.Errorf("log_address mapping: %+v %v", cfg.Features.LogAddress, err)
	}
	cfg, err = Parse([]byte(base + "features:\n  log_address:\n    enabled: true\n    interval: 30\n"))
	if d, _ := cfg.Features.LogAddress.Every(); err != nil || d != 30*time.Second {
		t.Errorf("log_address seconds: %+v %v", cfg.Features.LogAddress, err)
	}
	for _, bad := range []string{"5s", "often", "0"} {
		if _, err := Parse([]byte(base + "features:\n  log_address:\n    interval: " + bad + "\n")); err == nil {
			t.Errorf("log_address.interval %q accepted", bad)
		}
	}
	if f := cfg.Features.ServerInfo; !f.Enabled || f.Interval != "5m" {
		t.Errorf("server_info default: %+v", f)
	}
	cfg, err = Parse([]byte(base + "features:\n  server_info: false\n"))
	if err != nil || cfg.Features.ServerInfo.Enabled {
		t.Errorf("server_info short form: %+v %v", cfg.Features.ServerInfo, err)
	}
	cfg, err = Parse([]byte(base + "features:\n  server_info:\n    interval: 10m\n"))
	if d, _ := cfg.Features.ServerInfo.Every(); err != nil || !cfg.Features.ServerInfo.Enabled || d != 10*time.Minute {
		t.Errorf("server_info mapping: %+v %v", cfg.Features.ServerInfo, err)
	}
	for _, bad := range []string{"10s", "x", "0"} {
		if _, err := Parse([]byte(base + "features:\n  server_info:\n    interval: " + bad + "\n")); err == nil {
			t.Errorf("server_info.interval %q accepted", bad)
		}
	}
	if _, err := Parse([]byte(base + "features:\n  server_info:\n    every: 1m\n")); err == nil {
		t.Error("unknown server_info key accepted")
	}
	if _, err := Parse([]byte(base + "features:\n  log_address:\n    every: 1m\n")); err == nil {
		t.Error("unknown log_address key accepted")
	}
	if _, err := Parse([]byte(base + "features:\n  rcon_stats: true\n")); err == nil {
		t.Error("unknown feature accepted")
	}
}

func TestDailyAwardsSchedule(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\nfeatures:\n  daily_awards:\n"
	cfg, err := Parse([]byte(base + "    enabled: true\n    timezone: Europe/Berlin\n    time: \"03:30\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Features.DailyAwards
	loc, _ := d.Location("America/New_York") // its own zone wins
	h, m, _ := d.At()
	if !d.Enabled || loc.String() != "Europe/Berlin" || h != 3 || m != 30 {
		t.Errorf("parsed %+v %v %d:%d", d, loc, h, m)
	}
	cfg, err = Parse([]byte(base + "    enabled: false\n")) // the others keep their defaults
	if err != nil || cfg.Features.DailyAwards.Enabled || cfg.Features.DailyAwards.Timezone != "" || cfg.Features.DailyAwards.Time != "00:00" {
		t.Errorf("partial: %+v %v", cfg.Features.DailyAwards, err)
	}
	for _, bad := range []string{"    timezone: Mars/Base\n", "    time: \"25:00\"\n", "    when: daily\n"} {
		if _, err := Parse([]byte(base + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestInactivePlayers(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\n"
	cfg, err := Parse([]byte(base))
	ip := cfg.Features.InactivePlayers
	if h, m, _ := ip.At(); err != nil || ip.Enabled || h != 1 || m != 0 || ip.Timezone != "" {
		t.Errorf("default %+v %v, want off, 01:00", ip, err)
	}
	cfg, err = Parse([]byte(base + "features:\n  inactive_players:\n    enabled: true\n    time: \"04:15\"\n    timezone: Europe/Berlin\n"))
	ip = cfg.Features.InactivePlayers
	if h, m, _ := ip.At(); err != nil || !ip.Enabled || h != 4 || m != 15 || ip.Timezone != "Europe/Berlin" {
		t.Errorf("parsed %+v %v", ip, err)
	}
	if cfg, err = Parse([]byte(base + "features:\n  inactive_players: true\n")); err != nil || !cfg.Features.InactivePlayers.Enabled {
		t.Errorf("short form: %+v %v", cfg.Features.InactivePlayers, err)
	}
	for _, bad := range []string{"    after_days: 60\n", "    interval: 1h\n", "    time: \"24:00\"\n", "    time: \"1:00 am\"\n", "    timezone: Mars/Base\n"} {
		if _, err := Parse([]byte(base + "features:\n  inactive_players:\n" + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The zone of a daily job: its own, else the global timezone, else the
// server's.
func TestTimezones(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\n"
	for _, c := range []struct {
		yaml, awards, inactive string
	}{
		{"", "Local", "Local"},
		{"timezone: Europe/Berlin\n", "Europe/Berlin", "Europe/Berlin"},
		{"timezone: Europe/Berlin\nfeatures:\n  daily_awards:\n    timezone: UTC\n  inactive_players:\n    timezone: Asia/Tokyo\n", "UTC", "Asia/Tokyo"},
		{"features:\n  inactive_players:\n    timezone: America/New_York\n", "Local", "America/New_York"},
	} {
		cfg, err := Parse([]byte(base + c.yaml))
		if err != nil {
			t.Fatalf("%q: %v", c.yaml, err)
		}
		a, _ := cfg.Features.DailyAwards.Location(cfg.Timezone)
		i, _ := cfg.Features.InactivePlayers.Location(cfg.Timezone)
		name := func(l *time.Location) string {
			if l == time.Local { // its name is TZ when that is set
				return "Local"
			}
			return l.String()
		}
		if name(a) != c.awards || name(i) != c.inactive {
			t.Errorf("%q: awards %s, inactive %s; want %s, %s", c.yaml, a, i, c.awards, c.inactive)
		}
	}
	for _, bad := range []string{"timezone: Mars/Base\n", "features:\n  daily_awards:\n    timezone: Nowhere\n"} {
		if _, err := Parse([]byte(base + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLogSecret(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\n"
	cfg, err := Parse([]byte(base))
	if err != nil || !cfg.Features.LogSecret.Enabled || len(cfg.Features.LogSecret.Obsolete) != 0 {
		t.Errorf("default %+v %v, want enabled", cfg.Features.LogSecret, err)
	}
	for in, want := range map[string]bool{
		"  log_secret: false\n":               false,
		"  log_secret: true\n":                true,
		"  log_secret:\n    enabled: false\n": false,
		"  log_secret:\n    enabled: yes\n":   true,
	} {
		cfg, err := Parse([]byte(base + "features:\n" + in))
		if err != nil || cfg.Features.LogSecret.Enabled != want || len(cfg.Features.LogSecret.Obsolete) != 0 {
			t.Errorf("%q: %+v %v, want %v", in, cfg.Features.LogSecret, err, want)
		}
	}
	// The removed auto mode still loads (an old config must not stop the
	// collector): auto is true, interval is ignored, both are reported.
	for in, want := range map[string]string{
		"  log_secret: auto\n":                                     "enabled: auto (now true)",
		"  log_secret:\n    enabled: \"AUTO\"\n":                   "enabled: auto (now true)",
		"  log_secret:\n    interval: 1h\n":                        "interval",
		"  log_secret:\n    enabled: auto\n    interval: 24h\n":    "enabled: auto (now true), interval",
		"  log_secret:\n    enabled: false\n    interval: daily\n": "interval",
	} {
		cfg, err := Parse([]byte(base + "features:\n" + in))
		if got := strings.Join(cfg.Features.LogSecret.Obsolete, ", "); err != nil || got != want {
			t.Errorf("%q: obsolete %q %v, want %q", in, got, err, want)
		}
		if strings.Contains(in, "false") == cfg.Features.LogSecret.Enabled {
			t.Errorf("%q: enabled %v", in, cfg.Features.LogSecret.Enabled)
		}
	}
	for _, bad := range []string{
		"  log_secret: sometimes\n",
		"  log_secret:\n    enabled: 2\n",
		"  log_secret:\n    enabled: [true]\n",
		"  log_secret:\n    secret: 123\n",
	} {
		if _, err := Parse([]byte(base + "features:\n" + bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// The old listen.log_secret points to the feature.
	if _, err := Parse([]byte(base + "listen:\n  log_secret: x\n")); err == nil || !strings.Contains(err.Error(), "features.log_secret") {
		t.Errorf("listen.log_secret: %v", err)
	}
}

func TestListenInputs(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\n"
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if l := cfg.Listen; !l.UDPEnabled || l.Port != DefaultListenPort || l.HTTP.Enabled || l.HTTP.Port != DefaultHTTPPort ||
		l.HTTP.MaxConnections != 64 || l.HTTP.MaxConnectionsPerIP != 8 {
		t.Errorf("defaults %+v", l)
	}
	if !cfg.Features.CS2Hitgroups.Enabled {
		t.Error("cs2_hitgroups off by default")
	}
	cfg, err = Parse([]byte(base + "listen:\n  udp:\n    enabled: false\n    port: 27600\n    public_address: 203.0.113.5:27600\n" +
		"  http:\n    enabled: true\n    port: 8080\n    public_url: http://stats.example.org:8080\n    token: abcdefghijklmnop1234\n" +
		"    max_body: 1MiB\n    read_timeout: 30s\n    max_connections: 10\n    max_connections_per_ip: 2\n    trusted_proxies: [10.0.0.1, 192.168.0.0/16]\n" +
		"features:\n  cs2_hitgroups: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.Listen
	if l.UDPEnabled || l.Port != 27600 || l.PublicAddress != "203.0.113.5:27600" || !l.HTTP.Enabled || l.HTTP.Addr() != "0.0.0.0:8080" {
		t.Errorf("parsed %+v", l)
	}
	if n, _ := l.HTTP.BodyLimit(); n != 1<<20 {
		t.Errorf("max_body %d", n)
	}
	if p, err := l.HTTP.Proxies(); err != nil || len(p) != 2 || p[0].Bits() != 32 {
		t.Errorf("proxies %v %v", p, err)
	}
	if cfg.Features.CS2Hitgroups.Enabled {
		t.Error("cs2_hitgroups: false ignored")
	}
	bad := map[string]string{
		"both off":      "listen:\n  udp:\n    enabled: false\n",
		"udp twice":     "listen:\n  port: 1\n  udp:\n    port: 2\n",
		"https url":     "listen:\n  http:\n    public_url: https://x:1\n",
		"url path":      "listen:\n  http:\n    public_url: http://x:1/a\n",
		"short token":   "listen:\n  http:\n    token: abc\n",
		"token chars":   "listen:\n  http:\n    token: abcdefghijklmnop/x\n",
		"example token": "listen:\n  http:\n    token: change-me\n",
		"body small":    "listen:\n  http:\n    max_body: 1KiB\n",
		"body huge":     "listen:\n  http:\n    max_body: 1GiB\n",
		"timeout":       "listen:\n  http:\n    read_timeout: 1h\n",
		"per ip":        "listen:\n  http:\n    max_connections: 4\n    max_connections_per_ip: 5\n",
		"proxy":         "listen:\n  http:\n    trusted_proxies: [nope]\n",
		"http port":     "listen:\n  http:\n    port: 0\n",
		"unknown":       "listen:\n  http:\n    tls: true\n",
	}
	for name, in := range bad {
		if _, err := Parse([]byte(base + in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
