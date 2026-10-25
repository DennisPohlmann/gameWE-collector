// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"context"
	"strings"
)

// step is one table (or group of tables) of the conversion, in the order
// of docs/migration/hlstatsx-ce-to-gamewe.sql.
type step struct {
	name  string   // for people
	from  string   // the CE table(s), for people
	table string   // the gameWE table
	count string   // the CE table whose rows the plan shows
	needs []string // CE tables it reads besides count
	note  string
	parts []part
	after func(ctx context.Context, c *conv) error // counters, id maps
}

func (s step) missing(p *Plan) []string {
	var out []string
	for _, t := range append([]string{s.count}, s.needs...) {
		if !p.hasTable(t) {
			out = append(out, t)
		}
	}
	return out
}

func (s step) available(p *Plan) bool { return len(s.missing(p)) == 0 }

// eventTables are the event tables that are converted.
var eventTables = []string{"hlstats_Events_Frags", "hlstats_Events_Teamkills", "hlstats_Events_Suicides",
	"hlstats_Events_PlayerActions", "hlstats_Events_PlayerPlayerActions", "hlstats_Events_TeamBonuses",
	"hlstats_Events_Connects", "hlstats_Events_Entries", "hlstats_Events_Disconnects", "hlstats_Events_ChangeTeam",
	"hlstats_Events_ChangeRole", "hlstats_Events_ChangeName", "hlstats_Events_Chat", "hlstats_Events_Latency",
	"hlstats_Events_Admin"}

// codes inserts (game, code, name = code) for the codes that only appear
// in the events of a CE table (column col), as the SQL script does.
func codes(table, events, col string) part {
	return part{
		query:  "SELECT DISTINCT s.game, e." + col + " FROM " + events + " e JOIN hlstats_Servers s USING (serverId) WHERE e." + col + " <> ''",
		table:  table,
		cols:   []string{"game", "code", "name"},
		ignore: true,
		row: func(_ context.Context, c *conv, v []*string) ([]any, bool, error) {
			code := c.text(v[1], events)
			return []any{c.text(v[0], "hlstats_Servers"), code, code}, true, nil
		},
	}
}

// event is the start of an event row: timestamp_id, game_id and, with
// server, server_id; with m the map id. v starts with ts(eventTime),
// s.game, e.serverId and (with m) e.map. It returns the CE server's game.
func (c *conv) event(ctx context.Context, v []*string, t string, server, m bool) ([]any, string, error) {
	game := c.textKey(v[1], "hlstats_Servers")
	gid, err := c.id(ctx, "game", game, "")
	if err != nil {
		return nil, "", err
	}
	out := []any{c.unix(v[0]), gid}
	if server {
		out = append(out, str(v[2]))
	}
	if m {
		mid, err := c.id(ctx, "map", game, c.textKey(v[3], t))
		if err != nil {
			return nil, "", err
		}
		out = append(out, mid)
	}
	return out, game, nil
}

// evQuery is the SELECT of a CE event table: ts(eventTime), s.game,
// e.serverId, e.map (with m) and the further columns; events of servers CE
// no longer has are left out (JOIN), as in the SQL script.
func (c *conv) evQuery(t string, m bool, more ...string) string {
	cols := []string{c.ts("e.eventTime"), "s.game", "e.serverId"}
	if m {
		cols = append(cols, "e.map")
	}
	return "SELECT " + strings.Join(append(cols, more...), ", ") + " FROM " + t + " e JOIN hlstats_Servers s USING (serverId)"
}

// plainEvent is an event table whose further columns are copied: target
// columns cols after timestamp_id, game_id[, server_id][, map]; text marks
// the further columns that are text.
func (c *conv) plainEvent(t string, server, m bool, src, cols []string, text []bool) part {
	head := []string{"timestamp_id", "game_id"}
	if server {
		head = append(head, "server_id")
	}
	if m {
		head = append(head, "map")
	}
	first := 3
	if m {
		first = 4
	}
	return part{
		query: c.evQuery(t, m, src...),
		table: t,
		cols:  append(head, cols...),
		row: func(ctx context.Context, c *conv, v []*string) ([]any, bool, error) {
			out, _, err := c.event(ctx, v, t, server, m)
			if err != nil {
				return nil, false, err
			}
			for i := range src {
				if text[i] {
					out = append(out, c.text(v[first+i], t))
				} else {
					out = append(out, str(v[first+i]))
				}
			}
			return out, true, nil
		},
	}
}

// actionEvent is an event table with a CE action id: rows whose action
// has no gameWE action are left out (JOIN mig_action of the SQL script).
func (c *conv) actionEvent(t string, src, cols []string) part {
	return part{
		query: c.evQuery(t, true, append([]string{"e.actionId"}, src...)...),
		table: t,
		cols:  append([]string{"timestamp_id", "game_id", "server_id", "map", "actionId"}, cols...),
		row: func(ctx context.Context, c *conv, v []*string) ([]any, bool, error) {
			aid, ok := c.actions[deref(v[4])]
			if !ok {
				return nil, false, nil
			}
			out, _, err := c.event(ctx, v, t, true, true)
			if err != nil {
				return nil, false, err
			}
			out = append(out, aid)
			for i := range src {
				out = append(out, str(v[5+i]))
			}
			return out, true, nil
		},
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// codeEvent is an event table whose column col (weapon, team, role) is a
// code that becomes the id of kind; before are the CE columns between map
// and the code, after the ones behind it.
func (c *conv) codeEvent(t string, server bool, kind, col string, before, after, cols []string) part {
	return part{
		query: c.evQuery(t, true, append(append(append([]string{}, before...), "e."+col), after...)...),
		table: t,
		cols:  cols,
		row: func(ctx context.Context, c *conv, v []*string) ([]any, bool, error) {
			out, game, err := c.event(ctx, v, t, server, true)
			if err != nil {
				return nil, false, err
			}
			for i := range before {
				out = append(out, str(v[4+i]))
			}
			id, err := c.id(ctx, kind, game, c.textKey(v[4+len(before)], t))
			if err != nil {
				return nil, false, err
			}
			out = append(out, id)
			for i := range after {
				out = append(out, str(v[5+len(before)+i]))
			}
			return out, true, nil
		},
	}
}

// copyRows is a part that copies the columns of a query; text marks the
// text columns of the CE table t.
func copyRows(t, query, table string, cols []string, text []bool, ignore bool) part {
	return part{
		query:  query,
		table:  table,
		cols:   cols,
		ignore: ignore,
		row: func(_ context.Context, c *conv, v []*string) ([]any, bool, error) {
			out := make([]any, len(v))
			for i := range v {
				if text[i] {
					out[i] = c.text(v[i], t)
				} else {
					out[i] = str(v[i])
				}
			}
			return out, true, nil
		},
	}
}

// update runs an UPDATE per CE row (counters of the reference data); the
// query's columns are the SET values followed by game and code. The
// queries leave out zero counters: the gameWE reference data has 0.
func update(t, query, stmt string, text []bool) part {
	p := copyRows(t, query, "", nil, text, false)
	p.exec = stmt
	return p
}

func flags(s string) []bool { // "tt.." -> text, text, value, value
	out := make([]bool, len(s))
	for i := range s {
		out[i] = s[i] == 't'
	}
	return out
}

// idMap fills m with CE id -> gameWE id of kind by (game, code).
func idMap(ctx context.Context, c *conv, query, t, kind string, m map[string]int64) error {
	rows, err := c.src.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	type entry struct{ id, game, code string }
	var list []entry
	for rows.Next() {
		var id, game, code *string
		if err := rows.Scan(&id, &game, &code); err != nil {
			rows.Close()
			return err
		}
		list = append(list, entry{deref(id), c.textKey(game, t), c.textKey(code, t)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range list { // looked up after the source rows are closed (one query at a time on the CE session)
		id, err := c.id(ctx, kind, e.game, e.code)
		if err != nil {
			return err
		}
		if id != 0 {
			m[e.id] = id
		}
	}
	return nil
}

func (c *conv) steps() []step {
	return []step{
		{name: "games", from: "hlstats_Games", table: "hlstats_Games", count: "hlstats_Games",
			note: "games gameWE has keep their gameWE values; the others are added",
			parts: []part{copyRows("hlstats_Games", `SELECT code, name, hidden, realgame FROM hlstats_Games`,
				"hlstats_Games", []string{"code", "name", "hidden", "realgame"}, flags("tt.t"), true)}},

		{name: "servers", from: "hlstats_Servers", table: "hlstats_Servers", count: "hlstats_Servers",
			note: "same serverId, RCON password included; the collector looks the location up again",
			parts: []part{copyRows("hlstats_Servers", `SELECT serverId, address, port, name, game, rcon_password,
    publicaddress, IFNULL(statusurl, ''), GREATEST(kills, 0), GREATEST(players, 0),
    GREATEST(rounds, 0), GREATEST(suicides, 0), GREATEST(headshots, 0),
    GREATEST(bombs_planted, 0), GREATEST(bombs_defused, 0), GREATEST(ct_wins, 0),
    GREATEST(ts_wins, 0), GREATEST(max_players, 0), act_map,
    GREATEST(map_rounds, 0), GREATEST(map_ct_wins, 0), GREATEST(map_ts_wins, 0),
    GREATEST(map_started, 0), GREATEST(map_changes, 0),
    GREATEST(ct_shots, 0), GREATEST(ct_hits, 0), GREATEST(ts_shots, 0), GREATEST(ts_hits, 0),
    GREATEST(map_ct_shots, 0), GREATEST(map_ct_hits, 0),
    GREATEST(map_ts_shots, 0), GREATEST(map_ts_hits, 0), sortorder
FROM hlstats_Servers`, "hlstats_Servers", []string{"serverId", "address", "port", "name", "game", "rcon_password",
				"publicaddress", "statusurl", "kills", "players", "rounds", "suicides", "headshots",
				"bombs_planted", "bombs_defused", "ct_wins", "ts_wins", "max_players", "act_map",
				"map_rounds", "map_ct_wins", "map_ts_wins", "map_started", "map_changes",
				"ct_shots", "ct_hits", "ts_shots", "ts_hits",
				"map_ct_shots", "map_ct_hits", "map_ts_shots", "map_ts_hits", "display_id"},
				flags(".t.tt.tt"+strings.Repeat(".", 10)+"t"+strings.Repeat(".", 14)), false)}},

		{name: "admin logins", from: "hlstats_Users", table: "hlstats_Users", count: "hlstats_Users",
			note: "the web replaces the MD5 passwords with scrypt at the next sign-in",
			parts: []part{copyRows("hlstats_Users", `SELECT username, password, acclevel, playerId FROM hlstats_Users`,
				"hlstats_Users", []string{"username", "password", "acclevel", "playerId"}, flags("t..."), true)}},

		{name: "host groups", from: "hlstats_HostGroups", table: "hlstats_HostGroups", count: "hlstats_HostGroups",
			parts: []part{copyRows("hlstats_HostGroups", `SELECT id, pattern, name FROM hlstats_HostGroups`,
				"hlstats_HostGroups", []string{"id", "pattern", "name"}, flags(".tt"), false)}},

		{name: "teams", from: "hlstats_Teams, team codes of the events", table: "hlstats_Teams", count: "hlstats_Teams",
			needs: []string{"hlstats_Events_ChangeTeam"},
			parts: []part{
				copyRows("hlstats_Teams", `SELECT game, code, name, hidden FROM hlstats_Teams`,
					"hlstats_Teams", []string{"game", "code", "name", "hidden"}, flags("ttt."), true),
				codes("hlstats_Teams", "hlstats_Events_ChangeTeam", "team"),
			}},

		{name: "roles", from: "hlstats_Roles, role codes of the events", table: "hlstats_Roles", count: "hlstats_Roles",
			needs: []string{"hlstats_Events_ChangeRole"},
			parts: []part{
				copyRows("hlstats_Roles", `SELECT game, code, name, hidden FROM hlstats_Roles`,
					"hlstats_Roles", []string{"game", "code", "name", "hidden"}, flags("ttt."), true),
				codes("hlstats_Roles", "hlstats_Events_ChangeRole", "role"),
				update("hlstats_Roles", `SELECT kills, deaths, game, code FROM hlstats_Roles WHERE kills <> 0 OR deaths <> 0`,
					`UPDATE hlstats_Roles SET kills = ?, deaths = ? WHERE game = ? AND code = ?`, flags("..tt")),
			}},

		{name: "weapons", from: "hlstats_Weapons, weapon codes of the events", table: "hlstats_Weapons", count: "hlstats_Weapons",
			needs: []string{"hlstats_Events_Teamkills", "hlstats_Events_Suicides"},
			note:  "gameWE's modifiers stay; codes only CE has are added with CE's modifier",
			parts: []part{
				copyRows("hlstats_Weapons", `SELECT game, code, name, modifier FROM hlstats_Weapons`,
					"hlstats_Weapons", []string{"game", "code", "name", "modifier"}, flags("ttt."), true),
				codes("hlstats_Weapons", "hlstats_Events_Frags", "weapon"),
				codes("hlstats_Weapons", "hlstats_Events_Teamkills", "weapon"),
				codes("hlstats_Weapons", "hlstats_Events_Suicides", "weapon"),
				update("hlstats_Weapons", `SELECT kills, headshots, game, code FROM hlstats_Weapons WHERE kills <> 0 OR headshots <> 0`,
					`UPDATE hlstats_Weapons SET kills = ?, headshots = ? WHERE game = ? AND code = ?`, flags("..tt")),
			}},

		{name: "actions", from: "hlstats_Actions", table: "hlstats_Actions", count: "hlstats_Actions",
			note: "gameWE's rewards stay; one row per code (CE may have one per team)",
			parts: []part{
				copyRows("hlstats_Actions", `SELECT game, code, reward_player, reward_team, team,
    description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions
FROM hlstats_Actions ORDER BY id`, "hlstats_Actions", []string{"game", "code", "reward_player", "reward_team", "team",
					"description", "for_PlayerActions", "for_PlayerPlayerActions", "for_TeamActions", "for_WorldActions"},
					flags("tt..tt...."), true),
				update("hlstats_Actions", `SELECT SUM(count), game, code FROM hlstats_Actions GROUP BY game, code HAVING SUM(count) <> 0`,
					`UPDATE hlstats_Actions SET count = ? WHERE game = ? AND code = ?`, flags(".tt")),
			},
			after: func(ctx context.Context, c *conv) error {
				return idMap(ctx, c, `SELECT id, game, code FROM hlstats_Actions`, "hlstats_Actions", "action", c.actions)
			}},

		{name: "awards", from: "hlstats_Awards", table: "hlstats_Awards", count: "hlstats_Awards",
			parts: []part{copyRows("hlstats_Awards", `SELECT IF(awardType = 'O', 'O', 'W'), game, code, name, verb FROM hlstats_Awards`,
				"hlstats_Awards", []string{"awardType", "game", "code", "name", "verb"}, flags(".tttt"), true)},
			after: func(ctx context.Context, c *conv) error {
				return idMap(ctx, c, `SELECT awardId, game, code FROM hlstats_Awards`, "hlstats_Awards", "award", c.awards)
			}},

		{name: "maps", from: "hlstats_Maps_Counts, map names of the events", table: "hlstats_Maps", count: "hlstats_Maps_Counts",
			needs: []string{"hlstats_Events_Entries", "hlstats_Events_PlayerActions", "hlstats_Events_Connects", "hlstats_server_load"},
			parts: []part{
				copyRows("hlstats_Maps_Counts", `SELECT game, map, GREATEST(kills, 0), GREATEST(headshots, 0)
FROM hlstats_Maps_Counts WHERE map <> ''`, "hlstats_Maps", []string{"game", "map", "kills", "headshots"}, flags("tt.."), true),
				{
					query: `SELECT DISTINCT s.game, e.map FROM (
          SELECT serverId, map FROM hlstats_Events_Frags
    UNION SELECT serverId, map FROM hlstats_Events_Entries
    UNION SELECT serverId, map FROM hlstats_Events_PlayerActions
    UNION SELECT serverId, map FROM hlstats_Events_Connects
    UNION SELECT server_id, map FROM hlstats_server_load WHERE map IS NOT NULL
) e JOIN hlstats_Servers s USING (serverId) WHERE e.map <> ''`,
					table: "hlstats_Maps", cols: []string{"game", "map"}, ignore: true,
					row: func(_ context.Context, c *conv, v []*string) ([]any, bool, error) {
						return []any{c.text(v[0], "hlstats_Servers"), c.text(v[1], "hlstats_Events_Frags")}, true, nil
					},
				},
			}},

		{name: "clans", from: "hlstats_Clans", table: "hlstats_Clans", count: "hlstats_Clans",
			parts: []part{copyRows("hlstats_Clans", `SELECT clanId, tag, name, homepage, game FROM hlstats_Clans`,
				"hlstats_Clans", []string{"clanId", "tag", "name", "homepage", "game"}, flags(".tttt"), false)}},

		{name: "players", from: "hlstats_Players", table: "hlstats_Players", count: "hlstats_Players",
			note: "same playerId; hideranking 3 (CE: inactive) becomes 0",
			parts: []part{{
				query: `SELECT p.playerId, GREATEST(p.last_event, 0), p.connection_time, p.clan,
    LEAST(p.kills, 16777215), LEAST(p.deaths, 16777215), LEAST(p.suicides, 16777215),
    LEAST(p.skill, 16777215), p.shots, p.hits, LEAST(p.teamkills, 16777215),
    p.game, IF(p.hideranking = 3, 0, LEAST(p.hideranking, 255)),
    LEAST(p.headshots, 16777215), LEAST(GREATEST(p.last_skill_change, -8388608), 8388607),
    LEAST(GREATEST(p.kill_streak, 0), 65535), LEAST(GREATEST(p.death_streak, 0), 255),
    IF(p.kills > 0, LEAST(ROUND(p.headshots / p.kills, 2), 999.99), 0),
    ROUND(LEAST(p.kills / GREATEST(p.deaths, 1), 99999.99), 2)
FROM hlstats_Players p`,
				table: "hlstats_Players",
				cols: []string{"playerId", "last_event", "connection_time", "clan", "kills",
					"deaths", "suicides", "skill", "shots", "hits", "team_kills", "game", "game_id", "hideranking",
					"headshots", "last_skill_change", "kill_streak", "death_streak", "hpk", "kpd"},
				row: func(ctx context.Context, c *conv, v []*string) ([]any, bool, error) {
					game := c.textKey(v[11], "hlstats_Players")
					gid, err := c.id(ctx, "game", game, "")
					if err != nil {
						return nil, false, err
					}
					out := make([]any, 0, 20)
					for i := 0; i < 11; i++ {
						out = append(out, str(v[i]))
					}
					out = append(out, game, gid)
					for i := 12; i < len(v); i++ {
						out = append(out, str(v[i]))
					}
					return out, true, nil
				},
			}}},

		{name: "unique ids", from: "hlstats_PlayerUniqueIds", table: "hlstats_PlayerUniqueIds", count: "hlstats_PlayerUniqueIds",
			parts: []part{copyRows("hlstats_PlayerUniqueIds", `SELECT playerId, uniqueId, game FROM hlstats_PlayerUniqueIds`,
				"hlstats_PlayerUniqueIds", []string{"playerId", "uniqueId", "game"}, flags(".tt"), false)}},

		{name: "player names", from: "hlstats_PlayerNames", table: "hlstats_PlayerNames", count: "hlstats_PlayerNames",
			note: "names equal in utf8mb4_unicode_ci (case, accents) are merged",
			parts: []part{func() part {
				p := copyRows("hlstats_PlayerNames", `SELECT playerId, name, IFNULL(lastuse, '0000-00-00 00:00:00'), connection_time, LEAST(numuses, 16777215),
    LEAST(kills, 16777215), LEAST(deaths, 16777215), LEAST(suicides, 16777215),
    LEAST(headshots, 16777215), shots, hits
FROM hlstats_PlayerNames`, "hlstats_PlayerNames", []string{"playerId", "name", "lastuse", "connection_time", "numuses",
					"kills", "deaths", "suicides", "headshots", "shots", "hits"}, flags(".t........."), false)
				p.upsert = `lastuse = GREATEST(lastuse, VALUES(lastuse)), connection_time = connection_time + VALUES(connection_time),
    numuses = LEAST(numuses + VALUES(numuses), 16777215), kills = LEAST(kills + VALUES(kills), 16777215),
    deaths = LEAST(deaths + VALUES(deaths), 16777215), suicides = LEAST(suicides + VALUES(suicides), 16777215),
    headshots = LEAST(headshots + VALUES(headshots), 16777215), shots = shots + VALUES(shots), hits = hits + VALUES(hits)`
				return p
			}()}},

		{name: "locations", from: "hlstats_Players (city, state, country, flag, lat, lng)", table: "hlstats_Players_Locations", count: "hlstats_Players",
			note: "one row per place (lat/lng with 4 decimals)",
			parts: []part{copyRows("hlstats_Players", `SELECT city, state, country, LOWER(flag),
    CAST(CAST(lat AS DECIMAL(9,4)) AS CHAR), CAST(CAST(lng AS DECIMAL(9,4)) AS CHAR)
FROM hlstats_Players WHERE lat IS NOT NULL AND lng IS NOT NULL`, "hlstats_Players_Locations",
				[]string{"city", "state", "country", "flag", "lat", "lng"}, flags("tttt.."), true)}},

		{name: "player data", from: "hlstats_Players (name, first seen, location, profile)", table: "hlstats_Players_Data", count: "hlstats_Players",
			needs: []string{"hlstats_Events_Connects"},
			parts: []part{{
				query: `SELECT p.playerId, IF(p.createdate > 0, p.createdate, GREATEST(p.last_event, 0)),
    GREATEST(p.last_event, 0), LEAST(IFNULL(c.n, 0), 16777215), p.lastName,
    IF(p.lat IS NULL, NULL, CAST(CAST(p.lat AS DECIMAL(9,4)) AS CHAR)),
    IF(p.lat IS NULL, NULL, CAST(CAST(p.lng AS DECIMAL(9,4)) AS CHAR)),
    IFNULL(p.fullName, ''), IFNULL(p.email, ''), IFNULL(p.homepage, ''), IFNULL(CAST(p.icq AS CHAR), '')
FROM hlstats_Players p
LEFT JOIN (SELECT playerId, COUNT(*) AS n FROM hlstats_Events_Connects GROUP BY playerId) c ON c.playerId = p.playerId`,
				table: "hlstats_Players_Data",
				cols: []string{"playerId", "first_connect", "last_connect", "total_connects",
					"lastName", "location_id", "fullName", "email", "homepage", "icq"},
				row: func(ctx context.Context, c *conv, v []*string) ([]any, bool, error) {
					var loc int64
					if v[5] != nil && v[6] != nil {
						var err error
						if loc, err = c.id(ctx, "location", *v[5], *v[6]); err != nil {
							return nil, false, err
						}
					}
					return []any{str(v[0]), str(v[1]), str(v[2]), str(v[3]), c.text(v[4], "hlstats_Players"), loc,
						c.text(v[7], "hlstats_Players"), c.text(v[8], "hlstats_Players"), c.text(v[9], "hlstats_Players"), str(v[10])}, true, nil
				},
			}}},

		{name: "history per day", from: "hlstats_Players_History", table: "hlstats_Players_History", count: "hlstats_Players_History",
			note: "rows without a date are left out",
			parts: []part{copyRows("hlstats_Players_History", `SELECT playerId, eventTime, LEAST(connection_time, 16777215), LEAST(kills, 65535),
    LEAST(deaths, 65535), LEAST(suicides, 65535), LEAST(skill, 16777215),
    LEAST(shots, 16777215), LEAST(hits, 16777215), game, LEAST(headshots, 65535),
    LEAST(teamkills, 65535), LEAST(GREATEST(kill_streak, 0), 65535),
    LEAST(GREATEST(death_streak, 0), 255),
    LEAST(GREATEST(skill_change, -8388608), 8388607)
FROM hlstats_Players_History WHERE eventTime IS NOT NULL`, "hlstats_Players_History",
				[]string{"playerId", "eventTime", "connection_time", "kills", "deaths", "suicides", "skill", "shots", "hits",
					"game", "headshots", "team_kills", "kill_streak", "death_streak", "skill_change"},
				flags(".........t....."), false)}},

		{name: "award winners", from: "hlstats_Players_Awards", table: "hlstats_Awards_History", count: "hlstats_Players_Awards",
			parts: []part{{
				query: `SELECT awardId, playerId, DATE_FORMAT(awardTime, '%Y-%m-%d'), LEAST(count, 16777215) FROM hlstats_Players_Awards`,
				table: "hlstats_Awards_History",
				cols:  []string{"award_id", "winner_id", "date", "winner_count"},
				row: func(_ context.Context, c *conv, v []*string) ([]any, bool, error) {
					id, ok := c.awards[deref(v[0])]
					if !ok {
						return nil, false, nil
					}
					return []any{id, str(v[1]), str(v[2]), str(v[3])}, true, nil
				},
			}, {
				query: `SELECT awardId, d_winner_id, d_winner_count FROM hlstats_Awards WHERE d_winner_id IS NOT NULL`,
				exec:  `UPDATE hlstats_Awards SET d_winner_id = ?, d_winner_count = ? WHERE awardId = ?`,
				row: func(_ context.Context, c *conv, v []*string) ([]any, bool, error) {
					id, ok := c.awards[deref(v[0])]
					if !ok {
						return nil, false, nil
					}
					return []any{str(v[1]), str(v[2]), id}, true, nil
				},
			}}},

		{name: "frags", from: "hlstats_Events_Frags", table: "hlstats_Events_Frags", count: "hlstats_Events_Frags",
			note: "weapon -> id; positions kept; killerRole, victimRole left out",
			parts: []part{c.codeEvent("hlstats_Events_Frags", true, "weapon", "weapon",
				[]string{"e.killerId", "e.victimId"},
				[]string{"e.headshot", "e.pos_x", "e.pos_y", "e.pos_z", "e.pos_victim_x", "e.pos_victim_y", "e.pos_victim_z"},
				[]string{"timestamp_id", "game_id", "server_id", "map", "killerId", "victimId", "weapon", "headshot",
					"pos_x", "pos_y", "pos_z", "pos_victim_x", "pos_victim_y", "pos_victim_z"})}},

		{name: "team kills", from: "hlstats_Events_Teamkills", table: "hlstats_Events_Teamkills", count: "hlstats_Events_Teamkills",
			parts: []part{c.codeEvent("hlstats_Events_Teamkills", true, "weapon", "weapon",
				[]string{"e.killerId", "e.victimId"}, nil,
				[]string{"timestamp_id", "game_id", "server_id", "map", "killerId", "victimId", "weapon"})}},

		{name: "suicides", from: "hlstats_Events_Suicides", table: "hlstats_Events_Suicides", count: "hlstats_Events_Suicides",
			parts: []part{c.codeEvent("hlstats_Events_Suicides", true, "weapon", "weapon",
				[]string{"e.playerId"}, nil,
				[]string{"timestamp_id", "game_id", "server_id", "map", "playerId", "weapon"})}},

		{name: "player actions", from: "hlstats_Events_PlayerActions", table: "hlstats_Events_PlayerActions", count: "hlstats_Events_PlayerActions",
			note: "action -> gameWE action; positions left out",
			parts: []part{c.actionEvent("hlstats_Events_PlayerActions",
				[]string{"e.playerId", "LEAST(GREATEST(e.bonus, -32768), 32767)"}, []string{"playerId", "bonus"})}},

		{name: "player-player actions", from: "hlstats_Events_PlayerPlayerActions", table: "hlstats_Events_PlayerPlayerActions", count: "hlstats_Events_PlayerPlayerActions",
			parts: []part{c.actionEvent("hlstats_Events_PlayerPlayerActions",
				[]string{"e.playerId", "e.victimId", "LEAST(GREATEST(e.bonus, -32768), 32767)"}, []string{"playerId", "victimId", "bonus"})}},

		{name: "team bonuses", from: "hlstats_Events_TeamBonuses", table: "hlstats_Events_TeamBonuses", count: "hlstats_Events_TeamBonuses",
			parts: []part{c.actionEvent("hlstats_Events_TeamBonuses",
				[]string{"e.playerId", "LEAST(GREATEST(e.bonus, -128), 127)"}, []string{"playerId", "bonus"})}},

		{name: "connects", from: "hlstats_Events_Connects", table: "hlstats_Events_Connects", count: "hlstats_Events_Connects",
			note: "IPv4 -> integer, other addresses -> 0",
			parts: []part{c.plainEvent("hlstats_Events_Connects", false, true,
				[]string{"e.playerId", `IFNULL(INET_ATON(IF(e.ipAddress REGEXP '^[0-9]+([.][0-9]+){3}$', e.ipAddress, NULL)), 0)`, "e.hostname", "e.hostgroup"},
				[]string{"playerId", "ipAddress", "hostname", "hostgroup"}, flags("..tt"))}},

		{name: "entries", from: "hlstats_Events_Entries", table: "hlstats_Events_Entries", count: "hlstats_Events_Entries",
			parts: []part{c.plainEvent("hlstats_Events_Entries", true, true, []string{"e.playerId"}, []string{"playerId"}, flags("."))}},

		{name: "disconnects", from: "hlstats_Events_Disconnects", table: "hlstats_Events_Disconnects", count: "hlstats_Events_Disconnects",
			parts: []part{c.plainEvent("hlstats_Events_Disconnects", true, true, []string{"e.playerId"}, []string{"playerId"}, flags("."))}},

		{name: "team changes", from: "hlstats_Events_ChangeTeam", table: "hlstats_Events_ChangeTeam", count: "hlstats_Events_ChangeTeam",
			note: "team -> id",
			parts: []part{c.codeEvent("hlstats_Events_ChangeTeam", true, "team", "team", []string{"e.playerId"}, nil,
				[]string{"timestamp_id", "game_id", "server_id", "map", "playerId", "team"})}},

		{name: "role changes", from: "hlstats_Events_ChangeRole", table: "hlstats_Events_ChangeRole", count: "hlstats_Events_ChangeRole",
			note: "role -> id",
			parts: []part{c.codeEvent("hlstats_Events_ChangeRole", false, "role", "role", []string{"e.playerId"}, nil,
				[]string{"timestamp_id", "game_id", "map", "playerId", "role"})}},

		{name: "name changes", from: "hlstats_Events_ChangeName", table: "hlstats_Events_ChangeName", count: "hlstats_Events_ChangeName",
			parts: []part{c.plainEvent("hlstats_Events_ChangeName", false, true, []string{"e.playerId", "e.oldName", "e.newName"},
				[]string{"playerId", "oldName", "newName"}, flags(".tt"))}},

		{name: "chat", from: "hlstats_Events_Chat", table: "hlstats_Events_Chat", count: "hlstats_Events_Chat",
			parts: []part{c.plainEvent("hlstats_Events_Chat", true, false, []string{"e.playerId", "e.message_mode", "e.message"},
				[]string{"playerId", "message_mode", "message"}, flags("..t"))}},

		{name: "latency", from: "hlstats_Events_Latency", table: "hlstats_Events_Latency", count: "hlstats_Events_Latency",
			parts: []part{c.plainEvent("hlstats_Events_Latency", true, true, []string{"e.playerId", "LEAST(e.ping, 65535)"},
				[]string{"playerId", "ping"}, flags(".."))}},

		{name: "admin events", from: "hlstats_Events_Admin", table: "hlstats_Events_Admin", count: "hlstats_Events_Admin",
			parts: []part{c.plainEvent("hlstats_Events_Admin", false, true, []string{"e.type", "e.message", "e.playerName"},
				[]string{"type", "message", "playerName"}, flags("ttt"))}},

		{name: "server load", from: "hlstats_server_load", table: "hlstats_server_load", count: "hlstats_server_load",
			note: "map -> id, uptime and fps as numbers",
			parts: []part{{
				query: `SELECT l.server_id, l.timestamp, GREATEST(l.act_players, 0), GREATEST(l.min_players, 0),
    GREATEST(l.max_players, 0), s.game, l.map,
    LEAST(IF(l.uptime REGEXP '^[0-9]+$', CAST(l.uptime AS UNSIGNED), 0), 65535),
    LEAST(IF(l.fps REGEXP '^[0-9]+([.][0-9]+)?$', ROUND(l.fps), 0), 65535)
FROM hlstats_server_load l JOIN hlstats_Servers s ON s.serverId = l.server_id`,
				table: "hlstats_server_load",
				cols:  []string{"server_id", "timestamp", "act_players", "min_players", "max_players", "map", "uptime", "fps"},
				row: func(ctx context.Context, c *conv, v []*string) ([]any, bool, error) {
					var mid int64
					if v[6] != nil {
						var err error
						if mid, err = c.id(ctx, "map", c.textKey(v[5], "hlstats_Servers"), c.textKey(v[6], "hlstats_server_load")); err != nil {
							return nil, false, err
						}
					}
					return []any{str(v[0]), str(v[1]), str(v[2]), str(v[3]), str(v[4]), mid, str(v[7]), str(v[8])}, true, nil
				},
			}}},

		{name: "trend", from: "hlstats_Trend", table: "hlstats_Trend", count: "hlstats_Trend",
			parts: []part{copyRows("hlstats_Trend", `SELECT timestamp, game, GREATEST(players, 0), GREATEST(kills, 0), GREATEST(headshots, 0),
    LEAST(GREATEST(servers, 0), 65535), LEAST(GREATEST(act_slots, 0), 65535),
    LEAST(GREATEST(max_slots, 0), 65535)
FROM hlstats_Trend`, "hlstats_Trend", []string{"timestamp", "game", "players", "kills", "headshots", "servers", "act_slots", "max_slots"},
				flags(".t......"), false)}},
	}
}
