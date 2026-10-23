// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"testing"

	"gamewe-collector/internal/config"
)

func TestBuildTLSModes(t *testing.T) {
	c, err := BuildTLS(config.TLS{Mode: config.TLSDisable}, "db")
	if err != nil || c != nil {
		t.Fatalf("disable: %v %v", c, err)
	}
	c, _ = BuildTLS(config.TLS{Mode: config.TLSRequire}, "db")
	if !c.InsecureSkipVerify || c.VerifyConnection != nil {
		t.Error("require: expected encryption without verification")
	}
	c, _ = BuildTLS(config.TLS{Mode: config.TLSVerifyCA}, "db")
	if c.VerifyConnection == nil {
		t.Error("verify-ca: chain verification missing")
	}
	c, _ = BuildTLS(config.TLS{Mode: config.TLSVerifyFull, ServerName: "pg.internal"}, "10.0.0.5")
	if c.InsecureSkipVerify || c.ServerName != "pg.internal" {
		t.Errorf("verify-full: skip=%v name=%q", c.InsecureSkipVerify, c.ServerName)
	}
	if _, err := BuildTLS(config.TLS{Mode: config.TLSVerifyFull, CAFile: "/nonexistent"}, "db"); err == nil {
		t.Error("missing ca_file should fail")
	}
}

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"a = ? AND b = ?", "a = $1 AND b = $2"},
		{"COALESCE(NULLIF(name, ''), '?') WHERE id = ?", "COALESCE(NULLIF(name, ''), '?') WHERE id = $1"},
		{"x = 'it''s ?' AND y = ?", "x = 'it''s ?' AND y = $1"},
		{`SELECT "odd?col" FROM t WHERE a = ?`, `SELECT "odd?col" FROM t WHERE a = $1`},
		{"SELECT `q?` FROM t WHERE a = ?", "SELECT `q?` FROM t WHERE a = $1"},
		{"a = ? -- why?\nAND b = ?", "a = $1 -- why?\nAND b = $2"},
		{"a = ? /* ? */ AND b = ?", "a = $1 /* ? */ AND b = $2"},
		{"unterminated '?", "unterminated '?"},
		{"VALUES (?, ?, ?)", "VALUES ($1, $2, $3)"},
	}
	for _, c := range cases {
		if got := Rebind(c.in); got != c.want {
			t.Errorf("Rebind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
