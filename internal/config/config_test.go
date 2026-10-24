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
	if f := cfg.Features; !f.RconStatus.Enabled || f.RconStatus.Interval != "10s" || !f.LogAddress.Enabled || f.LogAddress.Interval != "1m" || !f.DailyAwards.Enabled || f.DailyAwards.Timezone != "UTC" || f.DailyAwards.Time != "00:00" {
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
	loc, _ := d.Location()
	h, m, _ := d.At()
	if !d.Enabled || loc.String() != "Europe/Berlin" || h != 3 || m != 30 {
		t.Errorf("parsed %+v %v %d:%d", d, loc, h, m)
	}
	cfg, err = Parse([]byte(base + "    enabled: false\n")) // the others keep their defaults
	if err != nil || cfg.Features.DailyAwards.Enabled || cfg.Features.DailyAwards.Timezone != "UTC" || cfg.Features.DailyAwards.Time != "00:00" {
		t.Errorf("partial: %+v %v", cfg.Features.DailyAwards, err)
	}
	for _, bad := range []string{"    timezone: Mars/Base\n", "    time: \"25:00\"\n", "    when: daily\n"} {
		if _, err := Parse([]byte(base + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLogSecret(t *testing.T) {
	base := "database:\n  host: db\n  user: u\n  name: n\n"
	cfg, err := Parse([]byte(base))
	if d, _ := cfg.Features.LogSecret.Every(); err != nil || cfg.Features.LogSecret.Enabled != SecretAuto || d != 24*time.Hour {
		t.Errorf("default %+v %v, want auto every 24h", cfg.Features.LogSecret, err)
	}
	for in, want := range map[string]SecretMode{
		"  log_secret: false\n":                                SecretOff,
		"  log_secret: true\n":                                 SecretOn,
		"  log_secret: auto\n":                                 SecretAuto,
		"  log_secret:\n    enabled: false\n":                  SecretOff,
		"  log_secret:\n    enabled: yes\n":                    SecretOn,
		"  log_secret:\n    enabled: \"AUTO\"\n":               SecretAuto,
		"  log_secret:\n    interval: 1h\n":                    SecretAuto,
		"  log_secret:\n    enabled: true\n    interval: 60\n": SecretOn,
	} {
		cfg, err := Parse([]byte(base + "features:\n" + in))
		if err != nil || cfg.Features.LogSecret.Enabled != want {
			t.Errorf("%q: %+v %v, want %s", in, cfg.Features.LogSecret, err, want)
		}
	}
	for _, bad := range []string{
		"  log_secret: sometimes\n",
		"  log_secret:\n    enabled: 2\n",
		"  log_secret:\n    enabled: [true]\n",
		"  log_secret:\n    interval: 30s\n",
		"  log_secret:\n    interval: daily\n",
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
