// SPDX-License-Identifier: GPL-3.0-or-later

// Package steam asks the Steam Web API for the bans of players
// (ISteamUser/GetPlayerBans). The API key never appears in an error.
package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Timeout of one request.
	Timeout = 8 * time.Second
	// MaxBody is the largest answer read (100 players are about 20 KiB).
	MaxBody = 1 << 20
	// MaxIDs is how many Steam IDs one GetPlayerBans request may ask for.
	MaxIDs = 100

	id64Base = 76561197960265728
)

// DefaultAPI is the Steam Web API.
const DefaultAPI = "https://api.steampowered.com"

// PlayerBans is the answer of GetPlayerBans for one player.
type PlayerBans struct {
	SteamID          string `json:"SteamId"`
	CommunityBanned  bool   `json:"CommunityBanned"`
	VACBanned        bool   `json:"VACBanned"`
	NumberOfVACBans  int    `json:"NumberOfVACBans"`
	DaysSinceLastBan int    `json:"DaysSinceLastBan"`
	NumberOfGameBans int    `json:"NumberOfGameBans"`
	EconomyBan       string `json:"EconomyBan"`
}

// VAC reports whether the player has a VAC ban on record.
func (b PlayerBans) VAC() bool { return b.VACBanned || b.NumberOfVACBans > 0 }

// Client asks the Steam Web API. The zero value uses DefaultAPI and
// http.DefaultTransport.
type Client struct {
	API  string            // tests; empty: DefaultAPI
	HTTP http.RoundTripper // tests; nil: http.DefaultTransport
}

// ErrKey: the API refused the key (HTTP 401 or 403).
var ErrKey = errors.New("steam web api: key refused (check gamewe_steam_api_key)")

// PlayerBans returns the bans of up to MaxIDs players (SteamID64), keyed
// by SteamID64; players the API does not know are missing.
func (c *Client) PlayerBans(ctx context.Context, key string, ids []string) (map[string]PlayerBans, error) {
	if len(ids) == 0 {
		return map[string]PlayerBans{}, nil
	}
	if len(ids) > MaxIDs {
		return nil, fmt.Errorf("steam web api: %d ids, at most %d per request", len(ids), MaxIDs)
	}
	for _, id := range ids {
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			return nil, fmt.Errorf("steam web api: %q is no SteamID64", id)
		}
	}
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	q := url.Values{"key": {key}, "steamids": {strings.Join(ids, ",")}}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+"/ISteamUser/GetPlayerBans/v1/?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("steam web api: bad request address")
	}
	req.Header.Set("User-Agent", "gameWE collector")
	rt := c.HTTP
	if rt == nil {
		rt = http.DefaultTransport
	}
	// No redirects (RoundTrip does not follow them), and the error of the
	// transport is reported without the URL, which holds the key.
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("steam web api: %w", cause(err, key))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrKey
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("steam web api: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Players []PlayerBans `json:"players"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&out); err != nil {
		return nil, errors.New("steam web api: unreadable answer")
	}
	m := make(map[string]PlayerBans, len(out.Players))
	for _, p := range out.Players {
		m[p.SteamID] = p
	}
	return m, nil
}

// cause strips the URL from a transport error and, to be sure, the key.
func cause(err error, key string) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[hidden]")
	}
	return errors.New(msg)
}

// ID64 converts a gameWE unique id ("1:12345" of STEAM_0:1:12345, or the
// Steam3 account "[U:1:24691]" as some logs write it) to a SteamID64.
func ID64(uniqueID string) (string, bool) {
	s := strings.TrimSpace(uniqueID)
	if a, ok := strings.CutPrefix(s, "[U:1:"); ok {
		a, ok = strings.CutSuffix(a, "]")
		n, err := strconv.ParseUint(a, 10, 32)
		if !ok || err != nil || n == 0 {
			return "", false
		}
		return strconv.FormatUint(id64Base+n, 10), true
	}
	if t, ok := strings.CutPrefix(s, "STEAM_"); ok {
		_, s, ok = strings.Cut(t, ":")
		if !ok {
			return "", false
		}
	}
	y, z, ok := strings.Cut(s, ":")
	if !ok || (y != "0" && y != "1") {
		return "", false
	}
	n, err := strconv.ParseUint(z, 10, 32)
	if err != nil || (n == 0 && y == "0") {
		return "", false
	}
	return strconv.FormatUint(id64Base+n*2+uint64(y[0]-'0'), 10), true
}
