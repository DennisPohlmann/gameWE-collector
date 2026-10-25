// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lintBase is a config without findings: a local database with a good
// password, the UDP input on.
const lintBase = "database:\n  host: 127.0.0.1\n  user: gamewe\n  password: a-long-database-password\n  name: gamewe\n"

// The same base with the database section left for the case.
const lintDB = "  user: gamewe\n  password: a-long-database-password\n  name: gamewe\n"

const goodToken = "Zx81-qPv_3mT7wKd0aLs"

type wantFinding struct {
	sev Severity
	key string
	msg string // substring of the message
}

func (w wantFinding) in(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == w.sev && f.Key == w.key && strings.Contains(f.Msg, w.msg) {
			return true
		}
	}
	return false
}

func stubInterfaces(t *testing.T) {
	t.Helper()
	old := interfaceAddrs
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)}}, nil
	}
	t.Cleanup(func() { interfaceAddrs = old })
}

func TestLint(t *testing.T) {
	stubInterfaces(t)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "client.key")
	certFile := filepath.Join(dir, "client.pem")
	pwFile := filepath.Join(dir, "db-password")
	for name, mode := range map[string]os.FileMode{keyFile: 0o644, certFile: 0o644, pwFile: 0o644} {
		if err := os.WriteFile(name, []byte("a-long-database-password\n"), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(name, mode)
	}
	httpOn := "listen:\n  http:\n    enabled: true\n    token: " + goodToken + "\n"
	cases := []struct {
		name  string
		in    string
		opt   LintOptions
		want  []wantFinding
		clean bool // no findings at all
	}{
		{name: "valid", in: lintBase, clean: true},
		{name: "valid with both inputs", in: lintBase + httpOn, clean: true},
		{name: "example values written out while http is off", in: lintBase +
			"listen:\n  udp:\n    enabled: true\n    address: 0.0.0.0\n    port: 41337\n  http:\n    enabled: false\n    address: 0.0.0.0\n    port: 41338\n    max_body: 128KiB\n" +
			"features:\n  log_address:\n    enabled: true\n    interval: 1m\n    http: true\n  cs2_hitgroups:\n    enabled: true\n", clean: true},

		// syntax, keys, types
		{name: "yaml syntax", in: "listen: [\n", want: []wantFinding{{SevError, "", "YAML syntax"}}},
		{name: "empty", in: "", want: []wantFinding{{SevError, "", "empty"}}},
		{name: "no mapping", in: "- a\n", want: []wantFinding{{SevError, "", "want a mapping"}}},
		{name: "misspelled key", in: lintBase + "  pasword: x\n", want: []wantFinding{{SevError, "database.pasword", `did you mean "password"`}}},
		{name: "misspelled section", in: lintBase + "listn:\n  port: 1\n", want: []wantFinding{{SevError, "listn", `did you mean "listen"`}}},
		{name: "unknown key, list of known keys", in: lintBase + "features:\n  rcon_status:\n    frequency: 5s\n",
			want: []wantFinding{{SevError, "features.rcon_status.frequency", "known here: enabled, interval"}}},
		{name: "several unknown keys", in: lintBase + "  typo1: 1\nlisten:\n  http:\n    tls: true\n",
			want: []wantFinding{{SevError, "database.typo1", "unknown key"}, {SevError, "listen.http.tls", "unknown key"}}},
		{name: "old top-level key", in: lintBase + "servers:\n  x: 1\n", want: []wantFinding{{SevError, "servers", "hlstats_Servers"}}},
		{name: "old listen.http_port", in: lintBase + "listen:\n  http_port: 41338\n", want: []wantFinding{{SevError, "listen.http_port", "listen.http"}}},
		{name: "udp in both forms", in: lintBase + "listen:\n  port: 1\n  udp:\n    port: 2\n", want: []wantFinding{{SevError, "listen.port", "move it to listen.udp.port"}}},
		{name: "port not a number", in: lintBase + "listen:\n  udp:\n    port: abc\n", want: []wantFinding{{SevError, "listen.udp.port", `want a whole number, not "abc"`}}},
		{name: "secret value not echoed", in: lintBase + httpOn + "    max_connections: [1]\n",
			want: []wantFinding{{SevError, "listen.http.max_connections", "want a whole number, not a list"}}},

		// inputs and ports
		{name: "all inputs off", in: lintBase + "listen:\n  udp:\n    enabled: false\n", want: []wantFinding{{SevError, "listen.udp.enabled", "both off"}}},
		{name: "all inputs off, http explicitly", in: lintBase + "listen:\n  http:\n    enabled: false\n  udp:\n    enabled: false\n",
			want: []wantFinding{{SevError, "listen.udp.enabled", "enable at least one input"}}},
		{name: "-listen switches udp on", in: lintBase + "listen:\n  udp:\n    enabled: false\n", opt: LintOptions{Listen: "127.0.0.1:27500"}, clean: true},
		{name: "-listen invalid", in: lintBase, opt: LintOptions{Listen: "nowhere"}, want: []wantFinding{{SevError, "-listen", "IP:port"}}},
		{name: "udp port out of range", in: lintBase + "listen:\n  udp:\n    port: 70000\n", want: []wantFinding{{SevError, "listen.udp.port", "out of range"}}},
		{name: "old form port 0", in: lintBase + "listen:\n  port: 0\n", want: []wantFinding{{SevError, "listen.port", "out of range"}}},
		{name: "udp port 0 is the default", in: lintBase + "listen:\n  udp:\n    port: 0\n", want: []wantFinding{{SevWarning, "listen.udp.port", "default port 41337"}}},
		{name: "http port 0", in: lintBase + httpOn + "    port: 0\n", want: []wantFinding{{SevError, "listen.http.port", "out of range"}}},
		{name: "http port too big", in: lintBase + httpOn + "    port: 65536\n", want: []wantFinding{{SevError, "listen.http.port", "out of range"}}},
		{name: "listen address no IP", in: lintBase + "listen:\n  udp:\n    address: localhost\n", want: []wantFinding{{SevError, "listen.udp.address", "must be an IP address"}}},
		{name: "http address no IP", in: lintBase + httpOn + "    address: all\n", want: []wantFinding{{SevError, "listen.http.address", "must be an IP address"}}},
		{name: "listen address not on this host", in: lintBase + "listen:\n  udp:\n    address: 198.51.100.9\n",
			want: []wantFinding{{SevWarning, "listen.udp.address", "not an address of this host"}}},
		{name: "listen address on this host", in: lintBase + "listen:\n  udp:\n    address: 192.0.2.10\n", clean: true},
		{name: "privileged port", in: lintBase + "listen:\n  udp:\n    port: 514\n", want: []wantFinding{{SevWarning, "listen.udp.port", "privileged"}}},
		{name: "udp and tcp on the same port", in: lintBase + httpOn + "    port: 41337\n", clean: true},
		{name: "http on the port of the local database", in: lintBase + httpOn + "    port: 3306\n",
			want: []wantFinding{{SevError, "listen.http.port", "port of the local database"}}},

		// secrets
		{name: "http without token", in: lintBase + "listen:\n  http:\n    enabled: true\n", want: []wantFinding{{SevWarning, "listen.http.token", "not set"}}},
		{name: "weak token", in: lintBase + "listen:\n  http:\n    enabled: true\n    token: aaaaaaaabbbbbbbb\n", want: []wantFinding{{SevWarning, "listen.http.token", "weak"}}},
		{name: "example token", in: lintBase + "listen:\n  http:\n    enabled: true\n    token: change-me\n", want: []wantFinding{{SevError, "listen.http.token", "not the example value"}}},
		{name: "everyone a trusted proxy", in: lintBase + httpOn + "    trusted_proxies: [0.0.0.0/0]\n",
			want: []wantFinding{{SevWarning, "listen.http.trusted_proxies", "almost) everyone"}}},
		{name: "trusted proxy", in: lintBase + httpOn + "    trusted_proxies: [127.0.0.1]\n", clean: true},
		{name: "no database password", in: "database:\n  host: 127.0.0.1\n  user: gamewe\n  name: gamewe\n", want: []wantFinding{{SevWarning, "database.password", "not set"}}},
		{name: "example database password", in: strings.Replace(lintBase, "a-long-database-password", "change-me", 1),
			want: []wantFinding{{SevWarning, "database.password", "example value"}}},
		{name: "short database password", in: strings.Replace(lintBase, "a-long-database-password", "secret", 1),
			want: []wantFinding{{SevWarning, "database.password", "shorter than 12"}}},
		{name: "password and password_file", in: lintBase + "  password_file: " + pwFile + "\n",
			want: []wantFinding{{SevError, "database.password_file", "not both"}}},
		{name: "password_file missing", in: "database:\n  host: 127.0.0.1\n  user: u\n  name: n\n  password_file: /nonexistent/pw\n",
			want: []wantFinding{{SevError, "database.password_file", "no such file"}}},
		{name: "password_file readable by everyone", in: "database:\n  host: 127.0.0.1\n  user: u\n  name: n\n  password_file: " + pwFile + "\n",
			want: []wantFinding{{SevWarning, "database.password_file", "every user may read"}}},

		// switches that contradict each other
		{name: "http settings while http is off", in: lintBase + "listen:\n  http:\n    port: 8080\n    token: " + goodToken + "\n",
			want: []wantFinding{{SevWarning, "listen.http.port", "no effect while listen.http.enabled is false"}, {SevWarning, "listen.http.token", "no effect"}}},
		{name: "udp settings while udp is off", in: lintBase + httpOn + "  udp:\n    enabled: false\n    port: 27500\n    public_address: 203.0.113.4:27500\n",
			want: []wantFinding{{SevWarning, "listen.udp.port", "no effect while listen.udp.enabled is false"}, {SevWarning, "listen.udp.public_address", "no effect"}}},
		{name: "interval while the feature is off", in: lintBase + "features:\n  rcon_status:\n    enabled: false\n    interval: 30s\n",
			want: []wantFinding{{SevWarning, "features.rcon_status.interval", "no effect while features.rcon_status.enabled is false"}}},
		{name: "daily award time while off", in: lintBase + "features:\n  daily_awards:\n    enabled: false\n    time: \"03:00\"\n",
			want: []wantFinding{{SevWarning, "features.daily_awards.time", "no effect"}}},
		{name: "log_address.http while log_address is off", in: lintBase + "features:\n  log_address:\n    enabled: false\n    http: true\n",
			want: []wantFinding{{SevWarning, "features.log_address.http", "no effect while features.log_address.enabled is false"}}},
		{name: "public_url that nothing registers", in: lintBase + httpOn + "    public_url: http://203.0.113.4:41338\nfeatures:\n  log_address:\n    http: false\n",
			want: []wantFinding{{SevWarning, "listen.http.public_url", "nothing registers it"}}},
		{name: "public_address that nothing registers", in: lintBase + "listen:\n  udp:\n    public_address: 203.0.113.4:41337\nfeatures:\n  log_address: false\n",
			want: []wantFinding{{SevWarning, "listen.udp.public_address", "nothing registers it"}}},
		{name: "public_address 0.0.0.0", in: lintBase + "listen:\n  udp:\n    public_address: 0.0.0.0:41337\n",
			want: []wantFinding{{SevWarning, "listen.udp.public_address", "no address a game server can send to"}}},
		{name: "public_url 0.0.0.0", in: lintBase + httpOn + "    public_url: http://0.0.0.0:41338\n",
			want: []wantFinding{{SevWarning, "listen.http.public_url", "no address"}}},
		{name: "old log_secret auto", in: lintBase + "features:\n  log_secret: auto\n", want: []wantFinding{{SevWarning, "features.log_secret", "ignored"}}},

		// database
		{name: "database fields missing", in: "database:\n  password: a-long-database-password\n",
			want: []wantFinding{{SevError, "database.host", "required"}, {SevError, "database.user", "required"}, {SevError, "database.name", "required"}}},
		{name: "database type", in: lintBase + "  type: sqlite\n", want: []wantFinding{{SevError, "database.type", "mariadb"}}},
		{name: "database port", in: lintBase + "  port: 99999\n", want: []wantFinding{{SevError, "database.port", "out of range"}}},
		{name: "database host with port", in: "database:\n  host: db.example.org:3306\n" + lintDB, want: []wantFinding{{SevError, "database.host", "database.port"}}},
		{name: "database host as URL", in: "database:\n  host: mysql://db.example.org\n" + lintDB, want: []wantFinding{{SevError, "database.host", "no URL"}}},
		{name: "credentials in public_url not printed", in: lintBase + httpOn + "    public_url: http://u:a-long-database-password@203.0.113.4:41338\n",
			want: []wantFinding{{SevError, "listen.http.public_url", "no user"}}},
		{name: "credentials in host not printed", in: "database:\n  host: u:a-long-database-password@db\n" + lintDB,
			want: []wantFinding{{SevError, "database.host", "no valid host"}}},
		{name: "tls mode", in: lintBase + "  tls:\n    mode: prefer\n", want: []wantFinding{{SevError, "database.tls.mode", "verify-full"}}},
		{name: "plaintext to a remote database", in: "database:\n  host: db.example.org\n" + lintDB, want: []wantFinding{{SevWarning, "database.tls.mode", "plaintext"}}},
		{name: "plaintext in a private network", in: "database:\n  host: 10.0.0.5\n" + lintDB, clean: true},
		{name: "plaintext to a container", in: "database:\n  host: db\n" + lintDB, clean: true},
		{name: "tls options with mode disable", in: lintBase + "  tls:\n    ca_file: /x.pem\n", want: []wantFinding{{SevError, "database.tls", "mode is disable"}}},
		{name: "tls file missing", in: lintBase + "  tls:\n    mode: verify-full\n    ca_file: /nonexistent/ca.pem\n",
			want: []wantFinding{{SevError, "database.tls.ca_file", "file not found"}}},
		{name: "tls file is a directory", in: lintBase + "  tls:\n    mode: verify-ca\n    ca_file: " + dir + "\n",
			want: []wantFinding{{SevError, "database.tls.ca_file", "is a directory"}}},
		{name: "tls cert without key", in: lintBase + "  tls:\n    mode: require\n    cert_file: " + certFile + "\n",
			want: []wantFinding{{SevError, "database.tls", "set together"}}},
		{name: "tls key readable by everyone", in: lintBase + "  tls:\n    mode: require\n    cert_file: " + certFile + "\n    key_file: " + keyFile + "\n",
			want: []wantFinding{{SevWarning, "database.tls.key_file", "every user may read"}}},

		// geoip
		{name: "geoip file missing, no download", in: lintBase + "geoip:\n  path: /nonexistent/city.mmdb\n  auto_update: false\n",
			want: []wantFinding{{SevWarning, "geoip.path", "no player or server locations"}}},
		{name: "geoip directory missing", in: lintBase + "geoip:\n  path: /nonexistent/city.mmdb\n", want: []wantFinding{{SevWarning, "geoip.path", "directory does not exist"}}},
		{name: "geoip default", in: lintBase, opt: LintOptions{GeoIPDefault: filepath.Join(dir, "city.mmdb")}, clean: true},

		// all problems, not only the first
		{name: "every value error", in: lintBase + "  type: sqlite\nlisten:\n  udp:\n    port: 70000\n  http:\n    port: 0\n",
			want: []wantFinding{{SevError, "listen.udp.port", "out of range"}, {SevError, "listen.http.port", "out of range"}, {SevError, "database.type", "mariadb"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Lint([]byte(c.in), c.opt)
			if c.clean && len(r.Findings) > 0 {
				t.Errorf("want no findings, got %+v", r.Findings)
			}
			for _, w := range c.want {
				if !w.in(r.Findings) {
					t.Errorf("missing %s %s %q in %+v", w.sev, w.key, w.msg, r.Findings)
				}
			}
			for _, f := range r.Findings {
				for _, s := range []string{"a-long-database-password", goodToken} {
					if strings.Contains(f.Msg, s) {
						t.Errorf("finding shows a secret: %+v", f)
					}
				}
			}
		})
	}
}

// Findings carry the line of their key and come sorted by line, the
// value checks after the syntax.
func TestLintLines(t *testing.T) {
	stubInterfaces(t)
	in := lintBase + "listen:\n  udp:\n    port: 70000\n  bogus: 1\n"
	r := Lint([]byte(in), LintOptions{})
	if r.SyntaxOK || !r.Decoded || r.Errors() != 2 {
		t.Fatalf("report %+v", r)
	}
	if f := r.Findings[0]; f.Key != "listen.udp.port" || f.Line != 8 {
		t.Errorf("first %+v, want listen.udp.port line 8", f)
	}
	if f := r.Findings[1]; f.Key != "listen.bogus" || f.Line != 9 {
		t.Errorf("second %+v, want listen.bogus line 9", f)
	}
	r = Lint([]byte("database:\n  host: [\n"), LintOptions{})
	if r.Decoded || len(r.Findings) != 1 || r.Findings[0].Line == 0 {
		t.Errorf("syntax error: %+v", r)
	}
}

// The example config has no errors (only warnings about its placeholder
// values).
func TestLintExample(t *testing.T) {
	stubInterfaces(t)
	r := LintFile("../../config.example.yaml", LintOptions{})
	if r.Errors() != 0 || !r.SyntaxOK {
		t.Errorf("config.example.yaml: %+v", r.Findings)
	}
}

func TestLintFile(t *testing.T) {
	stubInterfaces(t)
	if r := LintFile("/nonexistent/config.yaml", LintOptions{}); r.Errors() != 1 || !strings.Contains(r.Findings[0].Msg, "cannot read") {
		t.Errorf("missing file: %+v", r)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(lintBase), 0o640); err != nil {
		t.Fatal(err)
	}
	if r := LintFile(p, LintOptions{}); len(r.Findings) != 0 {
		t.Errorf("0640: %+v", r.Findings)
	}
	os.Chmod(p, 0o644)
	if r := LintFile(p, LintOptions{}); !(wantFinding{SevWarning, "database.password", "chmod 640"}).in(r.Findings) {
		t.Errorf("0644: %+v", r.Findings)
	}
}

func TestClashes(t *testing.T) {
	ip := netip.MustParseAddr
	cases := []struct {
		name  string
		a, b  listener
		clash bool
	}{
		{"same", listener{"a", "udp", ip("0.0.0.0"), 1}, listener{"b", "udp", ip("0.0.0.0"), 1}, true},
		{"wildcard covers one IP", listener{"a", "udp", ip("0.0.0.0"), 1}, listener{"b", "udp", ip("127.0.0.1"), 1}, true},
		{"one IP under the wildcard", listener{"a", "tcp", ip("192.0.2.1"), 1}, listener{"b", "tcp", ip("0.0.0.0"), 1}, true},
		{":: covers IPv4", listener{"a", "tcp", ip("::"), 1}, listener{"b", "tcp", ip("192.0.2.1"), 1}, true},
		{"0.0.0.0 does not cover IPv6", listener{"a", "tcp", ip("0.0.0.0"), 1}, listener{"b", "tcp", ip("::1"), 1}, false},
		{"mapped IPv4", listener{"a", "udp", ip("::ffff:127.0.0.1"), 1}, listener{"b", "udp", ip("127.0.0.1"), 1}, true},
		{"udp and tcp", listener{"a", "udp", ip("0.0.0.0"), 1}, listener{"b", "tcp", ip("0.0.0.0"), 1}, false},
		{"other port", listener{"a", "udp", ip("0.0.0.0"), 1}, listener{"b", "udp", ip("0.0.0.0"), 2}, false},
		{"other IPs", listener{"a", "udp", ip("192.0.2.1"), 1}, listener{"b", "udp", ip("192.0.2.2"), 1}, false},
	}
	for _, c := range cases {
		if got := len(clashes([]listener{c.a, c.b})) == 1; got != c.clash {
			t.Errorf("%s: clash %v, want %v", c.name, got, c.clash)
		}
	}
}

func TestSuggest(t *testing.T) {
	for in, want := range map[string]string{
		"pasword": `; did you mean "password"?`,
		"PORT":    `; did you mean "port"?`,
		"zzzzzz":  " (known here: password, port)",
	} {
		if got := suggest(in, []string{"password", "port"}); got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
}
