// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Location is a GeoIP location (see package geoip). Flag is the lower-case
// ISO country code, as HLstatsX and gameME store it.
type Location struct {
	City, State, Country, Flag string
	Lat, Lng                   float64
}

// locationID returns the hlstats_Players_Locations id of loc, creating the
// row. Rows are unique by lat/lng (4 decimals, about 11 m).
func (s *Store) locationID(ctx context.Context, loc Location) (int64, error) {
	lat, lng := strconv.FormatFloat(loc.Lat, 'f', 4, 64), strconv.FormatFloat(loc.Lng, 'f', 4, 64)
	v, err := s.cached("hlstats_Players_Locations|"+lat+"|"+lng, func() (any, error) {
		if _, err := s.exec(ctx, s.ignore(`INSERT INTO hlstats_Players_Locations (city, state, country, flag, lat, lng) VALUES (?, ?, ?, ?, ?, ?)`),
			clip(loc.City, 255), clip(loc.State, 255), clip(loc.Country, 255), clip(loc.Flag, 255), lat, lng); err != nil {
			return nil, err
		}
		var id int64
		err := s.queryRow(ctx, `SELECT id FROM hlstats_Players_Locations WHERE lat = ? AND lng = ?`, lat, lng).Scan(&id)
		return id, err
	})
	if err != nil {
		return 0, fmt.Errorf("hlstats_Players_Locations: %w", err)
	}
	return v.(int64), nil
}

// SetLocation stores where a player connects from: hlstats_Players_Data.location_id
// and the live row's cli_location_id.
func (s *Store) SetLocation(ctx context.Context, ev Ev, p Live, loc Location) error {
	id, err := s.locationID(ctx, loc)
	if err != nil {
		return err
	}
	return s.run(ctx,
		step{`UPDATE hlstats_Players_Data SET location_id = ? WHERE playerId = ?`, []any{id, p.ID}},
		step{`UPDATE hlstats_Livestats SET cli_location_id = ? WHERE player_id = ? AND server_id = ?`, []any{id, p.ID, ev.Srv.ID}})
}

// ServerLocation stores the location of a game server's address in
// hlstats_Servers, unless fixed_location is set (a location entered in the
// web admin).
func (s *Store) ServerLocation(ctx context.Context, serverID int64, loc Location) error {
	return s.run(ctx, step{`UPDATE hlstats_Servers SET lat = ?, lng = ?, country_code = ?, country_name = ?, city = ?, state = ?
		WHERE serverId = ? AND fixed_location = 0`,
		[]any{clip(strconv.FormatFloat(loc.Lat, 'f', 3, 64), 8), clip(strconv.FormatFloat(loc.Lng, 'f', 3, 64), 8),
			clip(loc.Flag, 4), clip(loc.Country, 32), clip(loc.City, 32), clip(loc.State, 32), serverID}})
}

// Load is one row of hlstats_server_load.
type Load struct {
	ServerID                        int64
	Time                            int64 // Unix time of the sample
	Players, MinPlayers, MaxPlayers int   // now, lowest and highest in the interval
	MapID                           int
	CPU, FPS                        int // from RCON stats, 0 if unknown
	Uptime                          int // minutes
}

// RecordServerLoad writes a row of hlstats_server_load.
func (s *Store) RecordServerLoad(ctx context.Context, l Load) error {
	c := func(v, hi int) int { return max(min(v, hi), 0) }
	return s.run(ctx, step{`INSERT INTO hlstats_server_load (server_id, timestamp, act_players, min_players, max_players, map, cpu, fps, uptime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		[]any{l.ServerID, l.Time, c(l.Players, 255), c(l.MinPlayers, 255), c(l.MaxPlayers, 255), c(l.MapID, 65535),
			c(l.CPU, 65535), c(l.FPS, 65535), c(l.Uptime, 65535)}})
}

// ServerStatus stores what the RCON "status" of a game server says:
// hlstats_Servers.max_players, the name if none is set, and the players'
// pings in the live view (by uniqueId).
func (s *Store) ServerStatus(ctx context.Context, serverID int64, hostname string, maxPlayers int, pings map[string]int) error {
	steps := []step{}
	if maxPlayers > 0 {
		steps = append(steps, step{`UPDATE hlstats_Servers SET max_players = ? WHERE serverId = ?`, []any{maxPlayers, serverID}})
	}
	if hostname != "" {
		steps = append(steps, step{`UPDATE hlstats_Servers SET name = ? WHERE serverId = ? AND name = ''`, []any{clip(hostname, 255), serverID}})
	}
	for uid, ping := range pings {
		steps = append(steps, step{`UPDATE hlstats_Livestats SET ping = ? WHERE server_id = ? AND unique_id = ?`,
			[]any{max(min(ping, 65535), 0), serverID, clip(uid, 32)}})
	}
	return s.run(ctx, steps...)
}

// Connect types of hlstats_Players_Data.connect_type: how the player
// connected last, from the client setting cl_connectmethod that the
// SourceMod plugin logs (`"P" setup "cl_connectmethod" (value "...")`).
// 0 = unknown (no plugin line, or an empty value such as "connect" in the
// console).
const (
	ConnectInternet    = 1  // serverbrowser_internet: server browser, Internet tab
	ConnectFavorites   = 2  // serverbrowser_favorites
	ConnectHistory     = 3  // serverbrowser_history
	ConnectLAN         = 4  // serverbrowser_lan
	ConnectFriends     = 5  // serverbrowser_friends
	ConnectSteam       = 6  // steam: "Join game" in the Steam friends list, invites
	ConnectSpectate    = 7  // serverbrowser_spectate
	ConnectMatchmaking = 8  // quickplay, quickpick, matchmaking (TF2 and others)
	ConnectRedirect    = 9  // redirect: sent by another server
	ConnectOther       = 10 // any other value
)

// ConnectType maps a cl_connectmethod value to a connect type (0 if empty).
func ConnectType(value string) int {
	v := strings.ToLower(strings.TrimSpace(value))
	switch {
	case v == "":
		return 0
	case v == "serverbrowser_internet" || v == "serverbrowser":
		return ConnectInternet
	case v == "serverbrowser_favorites":
		return ConnectFavorites
	case v == "serverbrowser_history":
		return ConnectHistory
	case v == "serverbrowser_lan":
		return ConnectLAN
	case v == "serverbrowser_friends":
		return ConnectFriends
	case v == "steam" || strings.HasPrefix(v, "steam_"):
		return ConnectSteam
	case v == "serverbrowser_spectate":
		return ConnectSpectate
	case strings.HasPrefix(v, "quickplay") || strings.HasPrefix(v, "quickpick") || strings.HasPrefix(v, "matchmaking"):
		return ConnectMatchmaking
	case v == "redirect":
		return ConnectRedirect
	}
	return ConnectOther
}

// SetConnectType stores how a player connected (hlstats_Players_Data.connect_type).
func (s *Store) SetConnectType(ctx context.Context, playerID int64, t int) error {
	return s.run(ctx, step{`UPDATE hlstats_Players_Data SET connect_type = ? WHERE playerId = ?`, []any{max(min(t, 255), 0), playerID}})
}

// RecordLatency stores a ping sample of a player (hlstats_Events_Latency)
// and adds it to the player's running average (hlstats_Players_Data
// average_ping, average_ping_count; the count stops at 65535, so the average
// then follows newer samples more).
func (s *Store) RecordLatency(ctx context.Context, ev Ev, p Live, ping int) error {
	ping = max(min(ping, 65535), 0)
	return s.run(ctx,
		step{`INSERT INTO hlstats_Events_Latency (timestamp_id, game_id, server_id, map, playerId, ping) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{ev.ts(), ev.Srv.GameID, ev.Srv.ID, ev.MapID, p.ID, ping}},
		step{`UPDATE hlstats_Players_Data SET average_ping = (average_ping * average_ping_count + ?) / (average_ping_count + 1),
			average_ping_count = LEAST(average_ping_count + 1, 65535) WHERE playerId = ?`, []any{ping, p.ID}})
}

// PruneLatency deletes ping samples older than before.
func (s *Store) PruneLatency(ctx context.Context, before time.Time) error {
	return s.run(ctx, step{`DELETE FROM hlstats_Events_Latency WHERE timestamp_id < ?`, []any{before.Unix()}})
}
