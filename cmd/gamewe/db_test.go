// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
)

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, " YES \n": true, "j\n": true, "ja\n": true, "Ja": true,
		"\n": false, "n\n": false, "no\n": false, "nein\n": false, "yy\n": false, "": false, "ok\n": false,
	} {
		var out bytes.Buffer
		got, err := confirm(strings.NewReader(in), &out, true)
		if err != nil || got != want {
			t.Errorf("answer %q: %v, %v; want %v", in, got, err, want)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("question %q", out.String())
		}
	}
	// A read error is no.
	if got, err := confirm(iotest.ErrReader(errors.New("boom")), io.Discard, true); got || err != nil {
		t.Errorf("read error: %v, %v", got, err)
	}
	// Without a terminal it does not wait for input.
	var out bytes.Buffer
	if got, err := confirm(strings.NewReader("y\n"), &out, false); got || !errors.Is(err, errNoTTY) || out.Len() != 0 {
		t.Errorf("no tty: %v, %v, %q", got, err, out.String())
	}
	if !strings.Contains(errNoTTY.Error(), "-force") {
		t.Error("the no-tty message does not name -force")
	}
}

func TestParseMigrateFlags(t *testing.T) {
	for _, c := range []struct {
		args          []string
		force, dryRun bool
		code          int // 0: no error
	}{
		{nil, false, false, 0},
		{[]string{"-f"}, true, false, 0},
		{[]string{"--force"}, true, false, 0},
		{[]string{"-force"}, true, false, 0},
		{[]string{"-n"}, false, true, 0},
		{[]string{"--dry-run"}, false, true, 0},
		{[]string{"-config", "/x.yaml", "-n"}, false, true, 0},
		{[]string{"-f", "-n"}, true, true, 2},
		{[]string{"--force", "--dry-run"}, true, true, 2},
		{[]string{"-x"}, false, false, 2},
		{[]string{"now"}, false, false, 2},
	} {
		f, err := parseMigrateFlags(c.args, io.Discard)
		var ee exitError
		switch {
		case c.code == 0 && err != nil:
			t.Errorf("%v: %v", c.args, err)
		case c.code != 0 && (!errors.As(err, &ee) || ee.code != c.code):
			t.Errorf("%v: %v, want exit %d", c.args, err, c.code)
		case c.code == 0 && (f.force != c.force || f.dryRun != c.dryRun):
			t.Errorf("%v: %+v", c.args, f)
		}
	}
	if f, _ := parseMigrateFlags([]string{"-config", "/x.yaml"}, io.Discard); f.config != "/x.yaml" {
		t.Errorf("config %q", f.config)
	}
	if _, err := parseMigrateFlags([]string{"-f", "-n"}, io.Discard); !strings.Contains(err.Error(), "cannot be used together") {
		t.Errorf("-f -n: %v", err)
	}
}

func TestPrintPlan(t *testing.T) {
	p := &database.Plan{Kind: database.KindHLstatsX, Latest: 2, Pending: database.Migrations,
		Steps: []database.Step{
			{Name: "setup", What: "create the table gamewe_schema_migrations (schema versions)"},
			{Version: 2, Name: "gamewe-0.2.1", What: "alter table hlstats_Events_Frags: add columns server_id", Table: "hlstats_Events_Frags", Rows: 2_500_000, Slow: true},
		}}
	var out bytes.Buffer
	printPlan(&out, p)
	s := out.String()
	for _, want := range []string{"existing HLstatsX database without schema versions, schema version 0, this collector needs 2",
		"pending:   v1 baseline", "pending:   v2 gamewe-0.2.1", "plan (2 steps):", "1. [setup] create the table",
		"2. [v2 gamewe-0.2.1] alter table hlstats_Events_Frags: add columns server_id (about 2500000 rows)",
		"it is locked while it is rebuilt", "backup"} {
		if !strings.Contains(s, want) {
			t.Errorf("plan lacks %q:\n%s", want, s)
		}
	}
	out.Reset()
	printPlan(&out, &database.Plan{Kind: database.KindGameWE, Versions: true, Current: 2, Latest: 2})
	if strings.Contains(out.String(), "plan") {
		t.Errorf("up to date plan: %s", out.String())
	}
}

func TestBackupAdvice(t *testing.T) {
	d := config.Defaults().Database
	d.Name, d.Host = "gamewe", "db.example.org"
	if s := backupAdvice(d, "20261024-1200"); !strings.Contains(s, "mariadb-dump") || !strings.Contains(s, "gamewe-before-migrate-20261024-1200.sql") {
		t.Errorf("mysql: %s", s)
	}
	d.Type = config.Postgres
	if s := backupAdvice(d, "x"); !strings.Contains(s, "pg_dump") || strings.Contains(s, d.Password+"@") && d.Password != "" {
		t.Errorf("postgres: %s", s)
	}
}

func TestMigrateHints(t *testing.T) {
	old := configPath
	defer func() { configPath = old }()
	configPath = config.DefaultPath
	p := &database.Plan{Current: 1, Latest: 2, Steps: make([]database.Step, 3)}
	err := schemaOutdated(p)
	if !strings.Contains(err.Error(), "schema version 1, needs 2; 3 steps pending") || !strings.Contains(err.Error(), "db migrate") ||
		strings.Contains(err.Error(), "-config") || !strings.Contains(err.Error(), "backup") {
		t.Errorf("default config: %v", err)
	}
	configPath = "/srv/gamewe.yaml"
	if err := schemaOutdated(p); !strings.HasSuffix(err.Error(), " db migrate -config /srv/gamewe.yaml") {
		t.Errorf("own config: %v", err)
	}
	if h := grantsHint(config.Database{Type: config.Postgres, Name: "stats"}); !strings.Contains(h, "db grants -config /srv/gamewe.yaml -print | psql -U postgres -d stats") {
		t.Errorf("grants hint: %s", h)
	}
}

func TestDBCmdUsage(t *testing.T) {
	if err := dbCmd(nil); err != nil {
		t.Errorf("gamewe db: %v", err)
	}
	var ee exitError
	if err := dbCmd([]string{"frobnicate"}); !errors.As(err, &ee) || ee.code != 2 || !strings.Contains(ee.msg, "db migrate") {
		t.Errorf("unknown: %v", err)
	}
	for _, want := range []string{"migrate", "status", "grants", "seed", "-f|--force", "-n|--dry-run", "2: migration pending"} {
		if !strings.Contains(dbUsage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}
