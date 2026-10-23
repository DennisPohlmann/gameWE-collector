// SPDX-License-Identifier: GPL-3.0-or-later

package geoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// dbipServer serves a gzipped city database for the months in files.
func dbipServer(t *testing.T, months map[string][]byte) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var got []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path)
		mu.Unlock()
		for m, b := range months {
			if r.URL.Path == "/dbip-city-lite-"+m+".mmdb.gz" {
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	return s, &got
}

func gz(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestUpdater(t *testing.T) {
	db, err := os.ReadFile(writeDB(t)) // built now
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	this := now.Format("2006-01")
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
	dir := t.TempDir()
	dest := filepath.Join(dir, "geoip", "dbip-city-lite.mmdb")

	// Nothing yet, this month's file missing: last month's is taken.
	srv, got := dbipServer(t, map[string][]byte{last: gz(t, db)})
	u := &Updater{Dest: dest, URL: srv.URL + "/dbip-city-lite-", Client: srv.Client()}
	ctx := context.Background()
	if ok, err := u.Check(ctx); err != nil || !ok {
		t.Fatalf("first check: %v %v (requests %v)", ok, err, *got)
	}
	if _, err := BuildTime(dest); err != nil {
		t.Fatal(err)
	}
	// The file is built this month: nothing to do.
	n := len(*got)
	if ok, err := u.Check(ctx); err != nil || ok || len(*got) != n {
		t.Errorf("current file downloaded again: %v %v", ok, err)
	}
	// Next month: only next month's file is tried, at most once a day.
	u.Now = func() time.Time { return now.AddDate(0, 1, 0) }
	if ok, err := u.Check(ctx); ok || !errors.Is(err, ErrNotPublished) {
		t.Errorf("next month without a file: %v %v", ok, err)
	}
	n = len(*got)
	if _, err := u.Check(ctx); err != nil || len(*got) != n {
		t.Errorf("tried again within a day: %v", *got)
	}
	_ = this

	// Not a database / not a city database / too big: the old file stays.
	before, _ := os.ReadFile(dest)
	for name, body := range map[string][]byte{"garbage": gz(t, []byte("not a database")), "not gzip": []byte("plain")} {
		bad, _ := dbipServer(t, map[string][]byte{"2030-01": body})
		u2 := &Updater{Dest: dest, URL: bad.URL + "/dbip-city-lite-", Client: bad.Client()}
		if err := u2.Download(ctx, "2030-01"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	after, _ := os.ReadFile(dest)
	if !bytes.Equal(before, after) {
		t.Error("a bad download replaced the database")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dbip-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
}

// Auto uses an explicit path, else DefaultPath.
func TestAuto(t *testing.T) {
	a := writeDB(t)
	auto := &Auto{DB: New(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	if auto.Path() != DefaultPath {
		t.Errorf("path %q, want %q", auto.Path(), DefaultPath)
	}
	auto.Explicit = func() string { return a }
	if auto.Path() != a {
		t.Error("explicit path not used")
	}
	if _, ok := auto.Lookup("/ignored", netip.MustParseAddr("203.0.113.136")); !ok {
		t.Error("no location")
	}
}

// The update does not overwrite another database (MaxMind) at Dest.
func TestUpdaterForeign(t *testing.T) {
	dest := writeDBType(t, "GeoLite2-City")
	before, _ := os.ReadFile(dest)
	srv, got := dbipServer(t, nil)
	u := &Updater{Dest: dest, URL: srv.URL + "/dbip-city-lite-", Client: srv.Client(),
		Now: func() time.Time { return time.Now().AddDate(0, 2, 0) }}
	if ok, err := u.Check(context.Background()); ok || !errors.Is(err, ErrNotDBIP) {
		t.Errorf("foreign database: %v %v", ok, err)
	}
	after, _ := os.ReadFile(dest)
	if len(*got) != 0 || !bytes.Equal(before, after) {
		t.Errorf("foreign database touched (requests %v)", *got)
	}
}

// CopyOld takes the database of the old location when it is newer.
func TestCopyOld(t *testing.T) {
	old := writeDB(t)
	dest := filepath.Join(t.TempDir(), "collector", "dbip-city-lite.mmdb")
	if ok, err := CopyOld(dest, old); err != nil || !ok {
		t.Fatalf("copy: %v %v", ok, err)
	}
	if _, err := BuildTime(dest); err != nil {
		t.Fatal(err)
	}
	if ok, err := CopyOld(dest, old); err != nil || ok {
		t.Errorf("copied again: %v %v", ok, err)
	}
	// The shipped file is newer than the old update: it stays.
	shipped := filepath.Join(t.TempDir(), "dbip-city-lite.mmdb")
	os.WriteFile(shipped, mustRead(t, writeDB(t)), 0o644)
	stale := writeDBBuilt(t, "DBIP-City-Lite", time.Now().AddDate(0, -2, 0).Unix())
	if ok, err := CopyOld(shipped, stale); err != nil || ok {
		t.Errorf("older file copied: %v %v", ok, err)
	}
	// The old update is newer than the shipped file: it is taken.
	os.WriteFile(shipped, mustRead(t, stale), 0o644)
	if ok, err := CopyOld(shipped, old); err != nil || !ok {
		t.Errorf("newer old file not copied: %v %v", ok, err)
	}
	none := filepath.Join(t.TempDir(), "x", "dbip-city-lite.mmdb")
	if ok, err := CopyOld(none, filepath.Join(t.TempDir(), "missing.mmdb")); err != nil || ok {
		t.Errorf("no old file: %v %v", ok, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.mmdb")
	os.WriteFile(bad, []byte("not a database"), 0o644)
	if ok, err := CopyOld(none, bad); err == nil || ok {
		t.Errorf("bad old file copied: %v %v", ok, err)
	}
	if _, err := os.Stat(none); err == nil {
		t.Error("bad old file left a database")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
