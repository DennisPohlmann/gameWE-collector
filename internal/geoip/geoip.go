// SPDX-License-Identifier: GPL-3.0-or-later

// Package geoip looks up the location of an IP address in a MaxMind-format
// city database (GeoLite2-City or DB-IP Lite City, .mmdb).
package geoip

import (
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"gamewe-collector/internal/stats"
)

// retry is how often a missing database is tried again and an open one
// checked for a newer file (geoipupdate, a monthly download).
const retry = 10 * time.Minute

// DB opens the database on first use, and again when the path changes, when
// the file was replaced (checked every few minutes) or, while it is
// missing, every few minutes. It is safe for concurrent use.
type DB struct {
	log *slog.Logger

	mu     sync.Mutex
	path   string
	r      *maxminddb.Reader
	mtime  time.Time // of the open file
	tried  time.Time
	warned map[string]bool
}

func New(log *slog.Logger) *DB { return &DB{log: log, warned: map[string]bool{}} }

type record struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

// reader returns the open database for path, or nil; d.mu must be held
// (also while the reader is used: a newer file closes it).
func (d *DB) reader(path string) *maxminddb.Reader {
	if path != d.path {
		if d.r != nil {
			d.r.Close()
		}
		d.path, d.r, d.tried = path, nil, time.Time{}
	}
	if path == "" || time.Since(d.tried) < retry {
		return d.r
	}
	d.tried = time.Now()
	fi, statErr := os.Stat(path)
	if d.r != nil && (statErr != nil || fi.ModTime().Equal(d.mtime)) {
		return d.r // unchanged (or gone: keep using the open copy)
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		if !d.warned[path] {
			d.warned[path] = true
			d.log.Warn("GeoIP database not available, player and server locations are not filled (see README, GeoIP)", "path", path, "err", err)
		}
		return d.r // keep a copy that works (e.g. a file being replaced)
	}
	if d.r != nil {
		d.r.Close()
	}
	d.r = r
	if statErr == nil {
		d.mtime = fi.ModTime()
	}
	d.log.Info("GeoIP database loaded", "path", path, "type", r.Metadata.DatabaseType,
		"built", time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC().Format("2006-01-02"))
	return r
}

// Reload makes the next lookup open the file again (after an update).
func (d *DB) Reload() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tried = time.Time{}
}

// Check opens the database (to log a missing one at start).
func (d *DB) Check(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reader(path)
}

// Lookup returns the location of ip in the database at path; ok is false
// if the database is missing or knows no location for ip (e.g. a LAN address).
func (d *DB) Lookup(path string, ip netip.Addr) (loc stats.Location, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.reader(path)
	if r == nil || !ip.IsValid() {
		return loc, false
	}
	var rec record
	if err := r.Lookup(net.IP(ip.Unmap().AsSlice()), &rec); err != nil {
		return loc, false
	}
	if rec.Country.ISOCode == "" && rec.Location.Latitude == 0 && rec.Location.Longitude == 0 {
		return loc, false
	}
	loc = stats.Location{
		City:    rec.City.Names["en"],
		Country: rec.Country.Names["en"],
		Flag:    strings.ToLower(rec.Country.ISOCode),
		Lat:     rec.Location.Latitude,
		Lng:     rec.Location.Longitude,
	}
	if len(rec.Subdivisions) > 0 {
		loc.State = rec.Subdivisions[0].Names["en"]
	}
	return loc, true
}
