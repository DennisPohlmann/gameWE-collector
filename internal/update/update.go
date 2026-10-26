// SPDX-License-Identifier: GPL-3.0-or-later

// Package update asks GitHub for the latest release of a gameWE component
// and compares it with the running version. It only reports; nothing is
// downloaded or installed.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repo is the collector's GitHub repository.
const Repo = "DennisPohlmann/gameWE-collector"

// API is the GitHub REST API (unauthenticated: 60 requests per hour and IP).
var API = "https://api.github.com"

// Release is the latest published release of a repository.
type Release struct {
	Version string // without the leading v, e.g. 0.17.0
	URL     string // its page on github.com
}

// Result of a check: the running and the latest version and whether the
// latest one is newer.
type Result struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	URL     string `json:"url"`
	Update  bool   `json:"update_available"`
}

// Client honours HTTPS_PROXY / NO_PROXY and gives up after 10 s.
func Client() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
}

// Check compares current with the latest release of repo. A development
// build ("dev", no x.y.z) is never older than a release.
func Check(ctx context.Context, c *http.Client, repo, current string) (Result, error) {
	rel, err := Latest(ctx, c, repo)
	if err != nil {
		return Result{Current: current}, err
	}
	return Result{Current: current, Latest: rel.Version, URL: rel.URL, Update: Newer(rel.Version, current)}, nil
}

var releaseURL = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/releases/tag/[A-Za-z0-9_.+-]+$`)

// Latest reads GET /repos/{repo}/releases/latest (drafts and pre-releases
// are left out by GitHub).
func Latest(ctx context.Context, c *http.Client, repo string) (Release, error) {
	if c == nil {
		c = Client()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gamewe-collector")
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var body struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("GitHub release: %w", err)
	}
	v := strings.TrimPrefix(body.Tag, "v")
	if _, ok := parse(v); !ok {
		return Release{}, errors.New("GitHub release: no version in the tag")
	}
	// Only a link to the release page is passed on, nothing else from the answer.
	if !releaseURL.MatchString(body.URL) {
		body.URL = "https://github.com/" + repo + "/releases/tag/v" + v
	}
	return Release{Version: v, URL: body.URL}, nil
}

// Newer reports whether version latest is newer than current (x.y.z, an
// optional leading v and a -suffix are ignored).
func Newer(latest, current string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

var semver = regexp.MustCompile(`^v?(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:[-+].*)?$`)

func parse(v string) ([3]int, bool) {
	m := semver.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}
