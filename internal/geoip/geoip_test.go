// SPDX-License-Identifier: GPL-3.0-or-later

package geoip

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

// writeDB writes a small city database in the GeoLite2-City / DB-IP Lite layout.
func writeDB(t *testing.T) string {
	t.Helper()
	return writeDBType(t, "DBIP-City-Lite")
}

func writeDBType(t *testing.T, typ string) string {
	t.Helper()
	return writeDBBuilt(t, typ, 0)
}

// writeDBBuilt writes a test database of type typ built at epoch (0: now).
func writeDBBuilt(t *testing.T, typ string, epoch int64) string {
	t.Helper()
	w, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: typ, RecordSize: 24, IncludeReservedNetworks: true, BuildEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	_, n, _ := net.ParseCIDR("203.0.113.0/24")
	err = w.Insert(n, mmdbtype.Map{
		"city":         mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String("Falkenstein"), "de": mmdbtype.String("Falkenstein")}},
		"country":      mmdbtype.Map{"iso_code": mmdbtype.String("DE"), "names": mmdbtype.Map{"en": mmdbtype.String("Germany")}},
		"subdivisions": mmdbtype.Slice{mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String("Saxony")}}},
		"location":     mmdbtype.Map{"latitude": mmdbtype.Float64(50.4779), "longitude": mmdbtype.Float64(12.3713)},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "city.mmdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

func TestLookup(t *testing.T) {
	var buf bytes.Buffer
	d := New(slog.New(slog.NewTextHandler(&buf, nil)))
	path := writeDB(t)
	loc, ok := d.Lookup(path, netip.MustParseAddr("203.0.113.136"))
	if !ok || loc.Flag != "de" || loc.Country != "Germany" || loc.City != "Falkenstein" || loc.State != "Saxony" || loc.Lat != 50.4779 || loc.Lng != 12.3713 {
		t.Errorf("Lookup = %+v, %v", loc, ok)
	}
	if _, ok := d.Lookup(path, netip.MustParseAddr("192.168.1.2")); ok {
		t.Error("a LAN address has a location")
	}
	if _, ok := d.Lookup(path, netip.MustParseAddr("::ffff:203.0.113.1")); !ok {
		t.Error("IPv4-mapped address not found")
	}
}

func TestMissingDatabase(t *testing.T) {
	var buf bytes.Buffer
	d := New(slog.New(slog.NewTextHandler(&buf, nil)))
	missing := filepath.Join(t.TempDir(), "none.mmdb")
	for i := 0; i < 3; i++ {
		if _, ok := d.Lookup(missing, netip.MustParseAddr("203.0.113.136")); ok {
			t.Fatal("location without database")
		}
	}
	if n := strings.Count(buf.String(), "GeoIP database not available"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, buf.String())
	}
}

// A replaced file (geoipupdate) is used after the next check.
func TestReloadChangedFile(t *testing.T) {
	d := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	path := writeDB(t)
	if _, ok := d.Lookup(path, netip.MustParseAddr("203.0.113.1")); !ok {
		t.Fatal("no location")
	}
	// An empty database of the same name, newer.
	w, _ := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "DBIP-City-Lite", RecordSize: 24})
	f, _ := os.Create(path + ".new")
	w.WriteTo(f)
	f.Close()
	os.Rename(path+".new", path)
	os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if _, ok := d.Lookup(path, netip.MustParseAddr("203.0.113.1")); !ok {
		t.Fatal("reloaded before the next check")
	}
	d.mu.Lock()
	d.tried = time.Time{} // the next check is due
	d.mu.Unlock()
	if _, ok := d.Lookup(path, netip.MustParseAddr("203.0.113.1")); ok {
		t.Error("the new file was not loaded")
	}
}
