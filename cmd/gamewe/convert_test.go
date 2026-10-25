// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"gamewe-collector/internal/config"
)

func TestSourceConfig(t *testing.T) {
	d := config.Defaults().Database
	d.Host, d.Port, d.Name, d.User, d.Password = "db.example", 3307, "gamewe", "gamewe", "colpw"
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	s, err := sourceConfig(d, "hlstatsx_ce", getenv)
	if err != nil || s.Name != "hlstatsx_ce" || s.Host != "db.example" || s.Port != 3307 || s.User != "gamewe" || s.Password != "colpw" {
		t.Errorf("name: %+v, %v", s, err)
	}
	s, err = sourceConfig(d, "mysql://old.example/hlstatsx", getenv)
	if err != nil || s.Host != "old.example" || s.Port != 3306 || s.Name != "hlstatsx" || s.User != "gamewe" || s.Password != "colpw" {
		t.Errorf("url without user: %+v, %v", s, err)
	}
	if _, err := sourceConfig(d, "mysql://ce@old.example:3310/hlstatsx", getenv); err == nil || !strings.Contains(err.Error(), fromPasswordEnv) {
		t.Errorf("user without password: %v", err)
	}
	env[fromPasswordEnv] = "cepw"
	s, err = sourceConfig(d, "mariadb://ce@old.example:3310/hlstatsx", getenv)
	if err != nil || s.Port != 3310 || s.User != "ce" || s.Password != "cepw" {
		t.Errorf("url with user: %+v, %v", s, err)
	}
	for _, bad := range []string{"", "a b", "hlstatsx;drop", "mysql://ce:secret@old.example/hlstatsx", "mysql://old.example/",
		"mysql://old.example:99999/x", "postgres://old.example/x", "mysql:///x", strings.Repeat("x", 65)} {
		if _, err := sourceConfig(d, bad, getenv); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := sourceConfig(d, "mysql://ce:secret@old.example/hlstatsx", getenv); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("password in the URL: %v", err)
	}
}

func TestParseConvertFlags(t *testing.T) {
	f, err := parseConvertFlags([]string{"--from", "hlstatsx_ce", "-source-timezone", "Europe/Berlin", "-n"})
	if err != nil || f.from != "hlstatsx_ce" || f.zone != "Europe/Berlin" || !f.dryRun || f.mojibake != "auto" {
		t.Errorf("%+v, %v", f, err)
	}
	if _, err := parseConvertFlags([]string{"-yes", "-dry-run"}); err == nil {
		t.Error("-yes with -dry-run accepted")
	}
	if _, err := parseConvertFlags([]string{"extra"}); err == nil {
		t.Error("argument accepted")
	}
}
