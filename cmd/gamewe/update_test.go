// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/version"
)

func TestUpdateNoteFromCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GAMEWE_UPDATE_CHECK", "")
	old := version.Version
	version.Version = "0.16.0"
	defer func() { version.Version = old }()
	cfg := filepath.Join(t.TempDir(), "missing.yaml")

	saveUpdateCache(updateCache{Checked: time.Now().Unix(), OK: true, Latest: "0.17.0", URL: "https://github.com/x/y/releases/tag/v0.17.0"})
	var b bytes.Buffer
	updateNote(&b, "status", []string{"-config", cfg})
	if !strings.Contains(b.String(), "update available: 0.17.0 (running 0.16.0)") {
		t.Errorf("note = %q", b.String())
	}
	for _, cmd := range []string{"run", "version", "-V", "help", "check-update"} {
		b.Reset()
		updateNote(&b, cmd, nil)
		if b.Len() != 0 {
			t.Errorf("%s printed %q", cmd, b.String())
		}
	}
	t.Setenv("GAMEWE_UPDATE_CHECK", "off")
	b.Reset()
	updateNote(&b, "status", []string{"-config", cfg})
	if b.Len() != 0 {
		t.Errorf("GAMEWE_UPDATE_CHECK=off printed %q", b.String())
	}
	t.Setenv("GAMEWE_UPDATE_CHECK", "")

	// features.update_check: false in the command's config file.
	off := filepath.Join(t.TempDir(), "config.yaml")
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	conf := strings.Replace(string(data), "    enabled: true      # daily, the other", "    enabled: false     # daily, the other", 1)
	if conf == string(data) {
		t.Fatal("update_check not found in config.example.yaml")
	}
	if err := os.WriteFile(off, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	updateNote(&b, "status", []string{"-config=" + off})
	if b.Len() != 0 {
		t.Errorf("update_check off printed %q", b.String())
	}

	// The same or an older release, or a failed check: nothing.
	for _, c := range []updateCache{{OK: true, Latest: "0.16.0"}, {OK: false}} {
		c.Checked = time.Now().Unix()
		saveUpdateCache(c)
		b.Reset()
		updateNote(&b, "status", []string{"-config", cfg})
		if b.Len() != 0 {
			t.Errorf("%+v printed %q", c, b.String())
		}
	}
}

func TestUpdateCacheAge(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	saveUpdateCache(updateCache{Checked: now.Add(-5 * time.Hour).Unix(), OK: true, Latest: "1.0.0"})
	if _, ok := cachedRelease(now); !ok {
		t.Error("5 h old answer not reused")
	}
	if _, ok := cachedRelease(now.Add(2 * time.Hour)); ok {
		t.Error("7 h old answer reused")
	}
	saveUpdateCache(updateCache{Checked: now.Add(-2 * time.Hour).Unix()})
	if _, ok := cachedRelease(now); ok {
		t.Error("2 h old failure reused")
	}
}
