// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/config"
)

func TestTestFlag(t *testing.T) {
	cases := []struct {
		in   []string
		rest []string
		test bool
	}{
		{[]string{"-config", "c.yaml"}, []string{"-config", "c.yaml"}, false},
		{[]string{"-t"}, []string{}, true},
		{[]string{"-config", "c.yaml", "--config-test"}, []string{"-config", "c.yaml"}, true},
		{[]string{"-config-test=true", "-listen", "127.0.0.1:1"}, []string{"-listen", "127.0.0.1:1"}, true},
		{[]string{"--config-test=false", "-config", "c.yaml"}, []string{"-config", "c.yaml"}, false},
		{[]string{"--", "-t"}, []string{"--", "-t"}, false},
	}
	for _, c := range cases {
		in := slices.Clone(c.in)
		rest, test := testFlag(in)
		if test != c.test || !slices.Equal(rest, c.rest) {
			t.Errorf("testFlag(%q) = %q %v, want %q %v", c.in, rest, test, c.rest, c.test)
		}
		if !slices.Equal(in, c.in) {
			t.Errorf("testFlag(%q) changed its argument to %q", c.in, in)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

const testDB = "database:\n  host: 127.0.0.1\n  user: gamewe\n  password: a-long-database-password\n  name: gamewe\n"

func TestConfigTestFlags(t *testing.T) {
	p := writeConfig(t, testDB)
	opt := config.LintOptions{}
	cases := []struct {
		name   string
		level  string
		reload time.Duration
		key    string // the error, "" for none
	}{
		{"defaults", "info", time.Minute, ""},
		{"reload too short", "info", time.Second, "-reload"},
		{"log level", "loud", time.Minute, "-log-level"},
	}
	for _, c := range cases {
		r := configTest(p, opt, c.level, c.reload)
		switch {
		case c.key == "" && r.Errors() != 0:
			t.Errorf("%s: %+v", c.name, r.Findings)
		case c.key != "" && (r.Errors() != 1 || r.Findings[len(r.Findings)-1].Key != c.key):
			t.Errorf("%s: %+v, want an error for %s", c.name, r.Findings, c.key)
		}
	}
}

// The certificates of database.tls are loaded as the connection would.
func TestConfigTestTLS(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := writeConfig(t, testDB+"  tls:\n    mode: verify-full\n    ca_file: "+ca+"\n")
	r := configTest(p, config.LintOptions{}, "info", time.Minute)
	if r.Errors() != 1 || r.Findings[0].Key != "database.tls.ca_file" || !strings.Contains(r.Findings[0].Msg, "no PEM certificates") {
		t.Errorf("bad CA file: %+v", r.Findings)
	}
	// A missing file is reported once, by the lint.
	p = writeConfig(t, testDB+"  tls:\n    mode: verify-full\n    ca_file: "+filepath.Join(dir, "missing.pem")+"\n")
	if r := configTest(p, config.LintOptions{}, "info", time.Minute); r.Errors() != 1 || !strings.Contains(r.Findings[0].Msg, "file not found") {
		t.Errorf("missing CA file: %+v", r.Findings)
	}
}

func TestWriteConfigTest(t *testing.T) {
	cases := []struct {
		name string
		doc  configTestDoc
		want string
	}{
		{"successful", configTestDoc{Config: "/c.yaml", OK: true, SyntaxOK: true},
			"gamewe: the configuration file /c.yaml syntax is ok\ngamewe: configuration file /c.yaml test is successful\n"},
		{"warning", configTestDoc{Config: "/c.yaml", OK: true, SyntaxOK: true, Warnings: 1,
			Findings: []configFinding{{"warning", "listen.http.token", 7, "not set"}}},
			"gamewe: [warning] listen.http.token: not set in /c.yaml:7\ngamewe: the configuration file /c.yaml syntax is ok\n" +
				"gamewe: configuration file /c.yaml test is successful (1 warning)\n"},
		{"failed", configTestDoc{Config: "/c.yaml", Errors: 2, Warnings: 0,
			Findings: []configFinding{{"error", "", 3, "YAML syntax: x"}, {"error", "-reload", 0, "must be at least 5s"}}},
			"gamewe: [error] YAML syntax: x in /c.yaml:3\ngamewe: [error] -reload: must be at least 5s\n" +
				"gamewe: configuration file /c.yaml test failed (2 errors, 0 warnings)\n"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		writeConfigTest(&b, c.doc)
		if b.String() != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, b.String(), c.want)
		}
	}
}

// Every spelling runs the test, with exit code 0, 1 on errors, 2 on wrong
// usage; nothing is started (no database, no port).
func TestConfigTestCommand(t *testing.T) {
	good := writeConfig(t, testDB)
	bad := writeConfig(t, testDB+"listen:\n  udp:\n    enabled: false\n")
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"--config-test", "-config", good}, 0, "test is successful"},
		{[]string{"-t", "-config", good}, 0, "syntax is ok"},
		{[]string{"config", "test", "-config", good}, 0, "test is successful"},
		{[]string{"run", "-t", "-config", good}, 0, "test is successful"},
		{[]string{"run", "--config-test", "-config", bad, "-listen", "127.0.0.1:41337"}, 0, "test is successful"},
		{[]string{"config", "test", "-config", bad}, 1, "[error] listen.udp.enabled: udp and http are both off"},
		{[]string{"-o", "json", "config", "test", "-config", bad}, 1, `"severity": "error"`},
		{[]string{"config", "test", "-config", filepath.Join(t.TempDir(), "missing.yaml")}, 1, "cannot read the file"},
		{[]string{"config"}, 2, ""},
		{[]string{"config", "lint"}, 2, ""},
		{[]string{"-o", "csv", "config", "test", "-config", good}, 2, ""},
	}
	for _, c := range cases {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestVersionFlags$", "--"}, c.args...)...)
		cmd.Env = append(os.Environ(), "GAMEWE_VERSION_CHILD=1")
		out, err := cmd.Output()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if code != c.code || !strings.Contains(string(out), c.out) {
			t.Errorf("%v: exit code %d, output %q; want %d and %q", c.args, code, out, c.code, c.out)
		}
	}
}
