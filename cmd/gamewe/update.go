// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/update"
	"gamewe-collector/internal/version"
)

// Update check: "run" logs a newer release at start and once a day, every
// other command prints a note on stderr first and then does its work, and
// "check-update" asks GitHub now. Nothing is downloaded or installed.
// features.update_check: false or GAMEWE_UPDATE_CHECK=off turns the
// automatic checks off; check-update always asks.

const (
	updateCacheOK   = 6 * time.Hour // a successful answer is reused this long
	updateCacheFail = time.Hour     // after a failure (offline) the CLI waits this long
	updateCLIWait   = 3 * time.Second
	updateEvery     = 24 * time.Hour
)

// noUpdateNote are the commands that print no note before their work.
var noUpdateNote = map[string]bool{"run": true, "check-update": true, "help": true, "-h": true, "-help": true, "--help": true}

func updateCheckOff() bool {
	v := strings.ToLower(os.Getenv("GAMEWE_UPDATE_CHECK"))
	return v == "off" || v == "0" || v == "false" || v == "no"
}

// updateNote writes "update available" to w when a newer release is known,
// from the cache or a short request. It never fails: offline, a slow
// answer or a broken cache just print nothing.
func updateNote(w io.Writer, cmd string, args []string) {
	if noUpdateNote[cmd] || isVersion(cmd) || updateCheckOff() || !updateCheckEnabled(args) {
		return
	}
	r, ok := cachedRelease(time.Now())
	if !ok {
		ctx, cancel := context.WithTimeout(context.Background(), updateCLIWait)
		rel, err := update.Latest(ctx, nil, update.Repo)
		cancel()
		r = updateCache{Checked: time.Now().Unix(), OK: err == nil, Latest: rel.Version, URL: rel.URL}
		saveUpdateCache(r)
	}
	if r.OK && update.Newer(r.Latest, version.Version) {
		fmt.Fprintln(w, updateLine(version.Version, r.Latest, r.URL))
	}
}

func updateLine(current, latest, url string) string {
	return fmt.Sprintf("gamewe: update available: %s (running %s), %s", latest, current, url)
}

// updateCheckEnabled reads features.update_check from the command's -config
// (or the default file). A file that cannot be read leaves the check on;
// the command itself reports it.
func updateCheckEnabled(args []string) bool {
	path := config.DefaultPath
	for i, a := range args {
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "config" {
			continue
		}
		if hasVal {
			path = val
		} else if i+1 < len(args) {
			path = args[i+1]
		}
	}
	cfg, err := config.Load(path)
	return err != nil || cfg.Features.UpdateCheck.Enabled
}

// updateCache is the last answer, in the user's cache folder
// (~/.cache/gamewe/update.json), so not every command asks GitHub.
type updateCache struct {
	Checked int64  `json:"checked"`
	OK      bool   `json:"ok"`
	Latest  string `json:"latest,omitempty"`
	URL     string `json:"url,omitempty"`
}

func updateCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "gamewe", "update.json")
}

func cachedRelease(now time.Time) (updateCache, bool) {
	var c updateCache
	p := updateCachePath()
	if p == "" {
		return c, false
	}
	data, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return updateCache{}, false
	}
	ttl := updateCacheFail
	if c.OK {
		ttl = updateCacheOK
	}
	age := now.Sub(time.Unix(c.Checked, 0))
	return c, age >= 0 && age < ttl
}

func saveUpdateCache(c updateCache) {
	p := updateCachePath()
	if p == "" || os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	data, _ := json.Marshal(c)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, p)
	}
}

// checkUpdateCmd is "gamewe check-update": ask GitHub now and report on
// stderr (the document of -o on stdout). Exit 1 when GitHub cannot be
// reached.
func checkUpdateCmd(args []string) error {
	fs := flag.NewFlagSet("check-update", flag.ExitOnError)
	addOutputFlag(fs)
	fs.Parse(args)
	if err := needList(false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, err := update.Check(ctx, nil, update.Repo, version.Version)
	if err != nil {
		saveUpdateCache(updateCache{Checked: time.Now().Unix()})
		return fmt.Errorf("latest release not available: %w", err)
	}
	saveUpdateCache(updateCache{Checked: time.Now().Unix(), OK: true, Latest: r.Latest, URL: r.URL})
	if r.Update {
		fmt.Fprintln(os.Stderr, updateLine(r.Current, r.Latest, r.URL))
	} else {
		fmt.Fprintf(os.Stderr, "gamewe: %s is up to date (latest release %s)\n", r.Current, r.Latest)
	}
	if structured() {
		return emit(os.Stdout, r)
	}
	return nil
}

// watchUpdates is the check of "run": at start and then once a day, a
// newer release is logged as a warning; failures only at debug level.
func watchUpdates(ctx context.Context, log *slog.Logger) {
	for {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		r, err := update.Check(cctx, nil, update.Repo, version.Version)
		cancel()
		switch {
		case err != nil:
			log.Debug("update check failed", "err", err)
		case r.Update:
			log.Warn("update available", "version", r.Current, "latest", r.Latest, "url", r.URL)
		default:
			log.Debug("update check: up to date", "version", r.Current, "latest", r.Latest)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateEvery):
		}
	}
}
