// SPDX-License-Identifier: GPL-3.0-or-later

package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"gamewe-collector/internal/stats"
)

const (
	// DefaultPath is the DB-IP City Lite database, shipped in the packages
	// and updated monthly there (the service's state directory).
	DefaultPath = "/var/lib/gamewe/collector/dbip-city-lite.mmdb"
	// OldPath is where earlier development versions kept the update; copied to DefaultPath
	// when it is newer (CopyOld), the old directory can be deleted then.
	OldPath = "/var/lib/gamewe/geoip/dbip-city-lite.mmdb"
	// LegacyPath is the old default of hlstats_Options gamewe_geoip_db
	// (that value is ignored).
	LegacyPath = "/var/lib/gamewe/GeoLite2-City.mmdb"

	// DBIPURL is the monthly download, followed by YYYY-MM.mmdb.gz.
	DBIPURL = "https://download.db-ip.com/free/dbip-city-lite-"

	maxGzip     = 256 << 20  // the download (about 60 MiB)
	maxDatabase = 1024 << 20 // unpacked (about 130 MiB)
)

// Auto is the database file in use: Explicit (config.yaml geoip.path, or
// a gamewe_geoip_db other than the old default), else DefaultPath.
type Auto struct {
	DB       *DB
	Explicit func() string // "" for DefaultPath
}

// Lookup implements the collector's GeoLookup; the path argument (the
// gamewe_geoip_db setting) is ignored, Explicit decides.
func (a *Auto) Lookup(_ string, ip netip.Addr) (stats.Location, bool) {
	return a.DB.Lookup(a.Path(), ip)
}

// Path returns the database file in use.
func (a *Auto) Path() string {
	if a.Explicit != nil {
		if p := a.Explicit(); p != "" {
			return p
		}
	}
	return DefaultPath
}

// BuildTime is the build date stored in a database file.
func BuildTime(path string) (time.Time, error) {
	t, _, err := info(path)
	return t, err
}

// info is the build date and database type of a database file.
func info(path string) (time.Time, string, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return time.Time{}, "", err
	}
	defer r.Close()
	return time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC(), r.Metadata.DatabaseType, nil
}

// CopyOld copies a database at old to dest when dest is missing or older
// (the upgrade from the old layout; the service can only write its own
// state directory, so old stays). It returns whether it copied.
func CopyOld(dest, old string) (bool, error) {
	if _, err := os.Stat(old); err != nil {
		return false, nil
	}
	if err := validate(old); err != nil {
		return false, fmt.Errorf("%s: %w", old, err)
	}
	oldT, _ := BuildTime(old)
	if destT, err := BuildTime(dest); err == nil && !oldT.After(destT) {
		return false, nil
	}
	in, err := os.Open(old)
	if err != nil {
		return false, err
	}
	defer in.Close()
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".dbip-*.mmdb")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(in, maxDatabase+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if n > maxDatabase {
		return false, fmt.Errorf("%s: larger than %d MiB", old, maxDatabase>>20)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), dest)
}

// ErrNotDBIP: Dest is another database (e.g. MaxMind GeoLite2), which the
// update does not overwrite.
var ErrNotDBIP = errors.New("not a DB-IP database, not updated (set geoip.auto_update: false)")

// Updater downloads DB-IP City Lite (CC BY 4.0) once a month into Dest.
type Updater struct {
	Dest     string // the file to write (geoip.path, default DefaultPath)
	Client   *http.Client
	URL      string // DBIPURL; tests use a local server
	Log      *slog.Logger
	Now      func() time.Time
	OnUpdate func() // after Dest was replaced

	lastTry time.Time
}

// client honours HTTPS_PROXY / NO_PROXY and limits the whole download.
func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func month(t time.Time) string { return t.UTC().Format("2006-01") }

// Check downloads this month's database when Dest is older (at most once a
// day), or, when Dest is missing, this month's or else last month's. A Dest
// that is not a DB-IP database is left alone (ErrNotDBIP). It returns
// whether Dest was replaced.
func (u *Updater) Check(ctx context.Context) (bool, error) {
	now := u.now().UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	newest, typ, err := info(u.Dest)
	if err != nil {
		newest = time.Time{} // missing or broken: download
	} else if !strings.Contains(strings.ToLower(typ), "dbip") {
		return false, fmt.Errorf("%s (%q): %w", u.Dest, typ, ErrNotDBIP)
	}
	if !newest.IsZero() && !newest.Before(first) {
		return false, nil // current
	}
	if !u.lastTry.IsZero() && now.Sub(u.lastTry) < 24*time.Hour {
		return false, nil
	}
	u.lastTry = now
	months := []time.Time{now}
	if newest.IsZero() {
		months = append(months, first.AddDate(0, -1, 0)) // nothing at all: last month will do
	}
	var errs []error
	for _, m := range months {
		err := u.Download(ctx, month(m))
		if err == nil {
			return true, nil
		}
		errs = append(errs, err)
	}
	return false, errors.Join(errs...)
}

// ErrNotPublished: DB-IP has no file for the month yet (early in a month).
var ErrNotPublished = errors.New("not published yet")

// Download fetches the database of month (YYYY-MM), checks that it is a
// city database and replaces Dest in one step (a new file renamed over the
// old one, so a running reader never sees a half-written file).
func (u *Updater) Download(ctx context.Context, month string) error {
	base := u.URL
	if base == "" {
		base = DBIPURL
	}
	url := base + month + ".mmdb.gz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "gamewe-collector")
	resp, err := u.client().Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("download %s: %w", url, ErrNotPublished)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	dir := filepath.Dir(u.Dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dbip-*.mmdb")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // after a successful rename it is gone already
	gz, err := gzip.NewReader(io.LimitReader(resp.Body, maxGzip))
	if err != nil {
		tmp.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	n, err := io.Copy(tmp, io.LimitReader(gz, maxDatabase+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxDatabase {
		return fmt.Errorf("download %s: larger than %d MiB", url, maxDatabase>>20)
	}
	if err := validate(tmp.Name()); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), u.Dest); err != nil {
		return err
	}
	if u.Log != nil {
		u.Log.Info("GeoIP database updated", "path", u.Dest, "month", month, "bytes", n)
	}
	if u.OnUpdate != nil {
		u.OnUpdate()
	}
	return nil
}

// validate checks that path is a city database that finds a location.
func validate(path string) error {
	r, err := maxminddb.Open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	if !strings.Contains(strings.ToLower(r.Metadata.DatabaseType), "city") {
		return fmt.Errorf("not a city database (%q)", r.Metadata.DatabaseType)
	}
	if r.Metadata.NodeCount == 0 {
		return errors.New("empty database")
	}
	return nil
}

// Run checks for an update now and every interval until ctx is done.
func (u *Updater) Run(ctx context.Context, every time.Duration) {
	warnedForeign := false
	for {
		if _, err := u.Check(ctx); err != nil && u.Log != nil {
			if errors.Is(err, ErrNotPublished) {
				u.Log.Debug("GeoIP update", "err", err)
			} else if errors.Is(err, ErrNotDBIP) {
				if !warnedForeign {
					warnedForeign = true
					u.Log.Warn("GeoIP update skipped", "err", err)
				}
			} else {
				u.Log.Warn("GeoIP update failed, keeping the current database", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
