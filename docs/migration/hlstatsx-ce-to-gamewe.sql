-- gameWE - collector: copy the data of an HLstatsX:CE database into a new,
-- empty gameWE database (MariaDB). Guide: docs/MIGRATION-HLSTATSX.md.
--
--   mariadb -u root -p gamewe < hlstatsx-ce-to-gamewe.sql
--
-- * Run it in the gameWE database (here `gamewe`), created by
--   schema/init/init-db.sh just before. It stops at once if that database
--   already has players or servers.
-- * The HLstatsX:CE data is read from the database `hlstatsx_ce` on the same
--   server: a copy of the CE database (never the live one), converted to
--   utf8mb4 (step 3 of the guide). Other name: replace `hlstatsx_ce.`
--   everywhere in this file.
-- * CE stores event times as local date/time (`eventTime`), gameWE as Unix
--   seconds. Set time_zone below to the zone CE wrote them in (the time zone
--   of the CE database server, usually the system zone: leave SYSTEM then).
-- * The CE database is only read. Running the script twice on the same
--   gameWE database is refused by the check below; start again with a new
--   database (init-db.sh) instead.

SET NAMES utf8mb4;
SET time_zone = 'SYSTEM';     -- e.g. '+01:00', or 'Europe/Berlin' if the time zone tables are loaded

-- Stop before changing anything if this database already has players or
-- servers, or if the CE copy is missing.
DELIMITER //
BEGIN NOT ATOMIC
  IF (SELECT COUNT(*) FROM hlstats_Players) > 0 OR (SELECT COUNT(*) FROM hlstats_Servers) > 0 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'this gameWE database is not new: create a new one with init-db.sh';
  END IF;
  IF (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Players) = 0 THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'hlstatsx_ce.hlstats_Players is empty: is the CE copy loaded?';
  END IF;
END //
DELIMITER ;

-- ---------------------------------------------------------------------------
-- 1. Games, servers, admin users
-- ---------------------------------------------------------------------------

-- Games of the gameWE reference data keep their gameWE values (realgame,
-- name); games only CE knows (clones such as css2) are added with CE's
-- realgame and hidden flag.
INSERT IGNORE INTO hlstats_Games (code, name, hidden, realgame)
SELECT code, name, hidden, realgame FROM hlstatsx_ce.hlstats_Games;

-- Servers keep their serverId (the event tables refer to it). The location
-- (lat, lng, city, country) is looked up again by the collector.
INSERT INTO hlstats_Servers (serverId, address, port, name, game, rcon_password,
    publicaddress, statusurl, kills, players, rounds, suicides, headshots,
    bombs_planted, bombs_defused, ct_wins, ts_wins, max_players, act_map,
    map_rounds, map_ct_wins, map_ts_wins, map_started, map_changes,
    ct_shots, ct_hits, ts_shots, ts_hits,
    map_ct_shots, map_ct_hits, map_ts_shots, map_ts_hits, display_id)
SELECT serverId, address, port, name, game, rcon_password,
    publicaddress, IFNULL(statusurl, ''), GREATEST(kills, 0), GREATEST(players, 0),
    GREATEST(rounds, 0), GREATEST(suicides, 0), GREATEST(headshots, 0),
    GREATEST(bombs_planted, 0), GREATEST(bombs_defused, 0), GREATEST(ct_wins, 0),
    GREATEST(ts_wins, 0), GREATEST(max_players, 0), act_map,
    GREATEST(map_rounds, 0), GREATEST(map_ct_wins, 0), GREATEST(map_ts_wins, 0),
    GREATEST(map_started, 0), GREATEST(map_changes, 0),
    GREATEST(ct_shots, 0), GREATEST(ct_hits, 0), GREATEST(ts_shots, 0), GREATEST(ts_hits, 0),
    GREATEST(map_ct_shots, 0), GREATEST(map_ct_hits, 0),
    GREATEST(map_ts_shots, 0), GREATEST(map_ts_hits, 0), sortorder
FROM hlstatsx_ce.hlstats_Servers;

-- Admin logins: the web accepts the old MD5 hashes and replaces them with
-- scrypt at the next sign-in.
INSERT IGNORE INTO hlstats_Users (username, password, acclevel, playerId)
SELECT username, password, acclevel, playerId FROM hlstatsx_ce.hlstats_Users;

INSERT INTO hlstats_HostGroups (id, pattern, name)
SELECT id, pattern, name FROM hlstatsx_ce.hlstats_HostGroups;

-- Server and game of every CE server, used below.
CREATE TEMPORARY TABLE mig_srv (PRIMARY KEY (serverId)) AS
SELECT s.serverId, s.game, IFNULL(g.id, 0) AS game_id
FROM hlstatsx_ce.hlstats_Servers s LEFT JOIN hlstats_Games g ON g.code = s.game;

-- ---------------------------------------------------------------------------
-- 2. Reference data: teams, roles, weapons, actions, awards, maps
-- ---------------------------------------------------------------------------
-- The gameWE reference data stays as it is (its rewards, modifiers and
-- names win); codes only CE has are added with CE's values. CE events name
-- weapons, teams, roles and maps by code; gameWE stores their ids.

INSERT IGNORE INTO hlstats_Teams (game, code, name, hidden)
SELECT game, code, name, hidden FROM hlstatsx_ce.hlstats_Teams;
INSERT IGNORE INTO hlstats_Teams (game, code, name)
SELECT DISTINCT s.game, e.team, e.team
FROM hlstatsx_ce.hlstats_Events_ChangeTeam e JOIN mig_srv s USING (serverId) WHERE e.team <> '';

INSERT IGNORE INTO hlstats_Roles (game, code, name, hidden)
SELECT game, code, name, hidden FROM hlstatsx_ce.hlstats_Roles;
INSERT IGNORE INTO hlstats_Roles (game, code, name)
SELECT DISTINCT s.game, e.role, e.role
FROM hlstatsx_ce.hlstats_Events_ChangeRole e JOIN mig_srv s USING (serverId) WHERE e.role <> '';
UPDATE hlstats_Roles r JOIN hlstatsx_ce.hlstats_Roles c ON c.game = r.game AND c.code = r.code
SET r.kills = c.kills, r.deaths = c.deaths;

INSERT IGNORE INTO hlstats_Weapons (game, code, name, modifier)
SELECT game, code, name, modifier FROM hlstatsx_ce.hlstats_Weapons;
INSERT IGNORE INTO hlstats_Weapons (game, code, name)
SELECT DISTINCT s.game, e.weapon, e.weapon
FROM hlstatsx_ce.hlstats_Events_Frags e JOIN mig_srv s USING (serverId) WHERE e.weapon <> '';
INSERT IGNORE INTO hlstats_Weapons (game, code, name)
SELECT DISTINCT s.game, e.weapon, e.weapon
FROM hlstatsx_ce.hlstats_Events_Teamkills e JOIN mig_srv s USING (serverId) WHERE e.weapon <> '';
INSERT IGNORE INTO hlstats_Weapons (game, code, name)
SELECT DISTINCT s.game, e.weapon, e.weapon
FROM hlstatsx_ce.hlstats_Events_Suicides e JOIN mig_srv s USING (serverId) WHERE e.weapon <> '';
UPDATE hlstats_Weapons w JOIN hlstatsx_ce.hlstats_Weapons c ON c.game = w.game AND c.code = w.code
SET w.kills = c.kills, w.headshots = c.headshots;

INSERT IGNORE INTO hlstats_Actions (game, code, reward_player, reward_team, team,
    description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions)
SELECT game, code, reward_player, reward_team, team,
    description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions
FROM hlstatsx_ce.hlstats_Actions ORDER BY id;
-- CE may have one code for several teams; gameWE has one row per code.
CREATE TEMPORARY TABLE mig_action (PRIMARY KEY (ce_id)) AS
SELECT c.id AS ce_id, a.id AS gw_id
FROM hlstatsx_ce.hlstats_Actions c JOIN hlstats_Actions a ON a.game = c.game AND a.code = c.code;
UPDATE hlstats_Actions a
JOIN (SELECT game, code, SUM(count) AS n FROM hlstatsx_ce.hlstats_Actions GROUP BY game, code) c
  ON c.game = a.game AND c.code = a.code
SET a.count = c.n;

INSERT IGNORE INTO hlstats_Awards (awardType, game, code, name, verb)
SELECT IF(awardType = 'O', 'O', 'W'), game, code, name, verb FROM hlstatsx_ce.hlstats_Awards;
CREATE TEMPORARY TABLE mig_award (PRIMARY KEY (ce_id)) AS
SELECT c.awardId AS ce_id, a.awardId AS gw_id
FROM hlstatsx_ce.hlstats_Awards c JOIN hlstats_Awards a ON a.game = c.game AND a.code = c.code;

-- Maps: the counters of hlstats_Maps_Counts, plus every map of the events.
INSERT IGNORE INTO hlstats_Maps (game, map, kills, headshots)
SELECT game, map, GREATEST(kills, 0), GREATEST(headshots, 0)
FROM hlstatsx_ce.hlstats_Maps_Counts WHERE map <> '';
INSERT IGNORE INTO hlstats_Maps (game, map)
SELECT DISTINCT s.game, e.map FROM (
          SELECT serverId, map FROM hlstatsx_ce.hlstats_Events_Frags
    UNION SELECT serverId, map FROM hlstatsx_ce.hlstats_Events_Entries
    UNION SELECT serverId, map FROM hlstatsx_ce.hlstats_Events_PlayerActions
    UNION SELECT serverId, map FROM hlstatsx_ce.hlstats_Events_Connects
    UNION SELECT server_id, map FROM hlstatsx_ce.hlstats_server_load WHERE map IS NOT NULL
) e JOIN mig_srv s USING (serverId) WHERE e.map <> '';

-- ---------------------------------------------------------------------------
-- 3. Clans and players
-- ---------------------------------------------------------------------------

INSERT INTO hlstats_Clans (clanId, tag, name, homepage, game)
SELECT clanId, tag, name, homepage, game FROM hlstatsx_ce.hlstats_Clans;

-- Players keep their playerId. hideranking 3 meant "inactive" in CE and
-- means "hidden by the player" in gameWE, so it becomes 0 (ranked).
INSERT INTO hlstats_Players (playerId, last_event, connection_time, clan, kills,
    deaths, suicides, skill, shots, hits, team_kills, game, game_id, hideranking,
    headshots, last_skill_change, kill_streak, death_streak, hpk, kpd)
SELECT p.playerId, GREATEST(p.last_event, 0), p.connection_time, p.clan,
    LEAST(p.kills, 16777215), LEAST(p.deaths, 16777215), LEAST(p.suicides, 16777215),
    LEAST(p.skill, 16777215), p.shots, p.hits, LEAST(p.teamkills, 16777215),
    p.game, IFNULL(g.id, 0), IF(p.hideranking = 3, 0, LEAST(p.hideranking, 255)),
    LEAST(p.headshots, 16777215), LEAST(GREATEST(p.last_skill_change, -8388608), 8388607),
    LEAST(GREATEST(p.kill_streak, 0), 65535), LEAST(GREATEST(p.death_streak, 0), 255),
    IF(p.kills > 0, LEAST(ROUND(p.headshots / p.kills, 2), 999.99), 0),
    ROUND(LEAST(p.kills / GREATEST(p.deaths, 1), 99999.99), 2)
FROM hlstatsx_ce.hlstats_Players p LEFT JOIN hlstats_Games g ON g.code = p.game;

INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game)
SELECT playerId, uniqueId, game FROM hlstatsx_ce.hlstats_PlayerUniqueIds;

INSERT INTO hlstats_PlayerNames (playerId, name, lastuse, connection_time, numuses,
    kills, deaths, suicides, headshots, shots, hits)
SELECT playerId, name, lastuse, connection_time, LEAST(numuses, 16777215),
    LEAST(kills, 16777215), LEAST(deaths, 16777215), LEAST(suicides, 16777215),
    LEAST(headshots, 16777215), shots, hits
FROM hlstatsx_ce.hlstats_PlayerNames;

-- Locations: CE keeps them per player, gameWE in a table of places
-- (lat/lng as text with 4 decimals, flag in lower case).
INSERT IGNORE INTO hlstats_Players_Locations (city, state, country, flag, lat, lng)
SELECT city, state, country, LOWER(flag),
    CAST(CAST(lat AS DECIMAL(9,4)) AS CHAR), CAST(CAST(lng AS DECIMAL(9,4)) AS CHAR)
FROM hlstatsx_ce.hlstats_Players WHERE lat IS NOT NULL AND lng IS NOT NULL;

CREATE TEMPORARY TABLE mig_connects (PRIMARY KEY (playerId)) AS
SELECT playerId, COUNT(*) AS n FROM hlstatsx_ce.hlstats_Events_Connects GROUP BY playerId;

INSERT INTO hlstats_Players_Data (playerId, first_connect, last_connect, total_connects,
    lastName, location_id, fullName, email, homepage, icq)
SELECT p.playerId, IF(p.createdate > 0, p.createdate, GREATEST(p.last_event, 0)),
    GREATEST(p.last_event, 0), LEAST(IFNULL(c.n, 0), 16777215), p.lastName,
    IFNULL(l.id, 0), IFNULL(p.fullName, ''), IFNULL(p.email, ''), IFNULL(p.homepage, ''),
    IFNULL(CAST(p.icq AS CHAR), '')
FROM hlstatsx_ce.hlstats_Players p
LEFT JOIN mig_connects c ON c.playerId = p.playerId
LEFT JOIN hlstats_Players_Locations l
  ON p.lat IS NOT NULL
 AND l.lat = CAST(CAST(p.lat AS DECIMAL(9,4)) AS CHAR) COLLATE utf8mb4_unicode_ci
 AND l.lng = CAST(CAST(p.lng AS DECIMAL(9,4)) AS CHAR) COLLATE utf8mb4_unicode_ci;

INSERT INTO hlstats_Players_History (playerId, eventTime, connection_time, kills,
    deaths, suicides, skill, shots, hits, game, headshots, team_kills, kill_streak,
    death_streak, skill_change)
SELECT playerId, eventTime, LEAST(connection_time, 16777215), LEAST(kills, 65535),
    LEAST(deaths, 65535), LEAST(suicides, 65535), LEAST(skill, 16777215),
    LEAST(shots, 16777215), LEAST(hits, 16777215), game, LEAST(headshots, 65535),
    LEAST(teamkills, 65535), LEAST(GREATEST(kill_streak, 0), 65535),
    LEAST(GREATEST(death_streak, 0), 255),
    LEAST(GREATEST(skill_change, -8388608), 8388607)
FROM hlstatsx_ce.hlstats_Players_History WHERE eventTime IS NOT NULL;

-- Daily award winners (CE hlstats_Players_Awards) and the latest winner.
INSERT INTO hlstats_Awards_History (award_id, winner_id, date, winner_count)
SELECT m.gw_id, a.playerId, DATE_FORMAT(a.awardTime, '%Y-%m-%d'), LEAST(a.count, 16777215)
FROM hlstatsx_ce.hlstats_Players_Awards a JOIN mig_award m ON m.ce_id = a.awardId;
UPDATE hlstats_Awards w JOIN mig_award m ON m.gw_id = w.awardId
JOIN hlstatsx_ce.hlstats_Awards c ON c.awardId = m.ce_id
SET w.d_winner_id = c.d_winner_id, w.d_winner_count = c.d_winner_count
WHERE c.d_winner_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 4. Events
-- ---------------------------------------------------------------------------
-- eventTime -> timestamp_id (Unix seconds in the time_zone set above),
-- serverId -> server_id and game_id, map name -> hlstats_Maps.id,
-- weapon / team / role code -> id, CE action id -> gameWE action id.

INSERT INTO hlstats_Events_Frags (timestamp_id, game_id, server_id, map, killerId,
    victimId, weapon, headshot, pos_x, pos_y, pos_z, pos_victim_x, pos_victim_y, pos_victim_z)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.killerId, e.victimId, IFNULL(w.weaponId, 0), e.headshot,
    e.pos_x, e.pos_y, e.pos_z, e.pos_victim_x, e.pos_victim_y, e.pos_victim_z
FROM hlstatsx_ce.hlstats_Events_Frags e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map
LEFT JOIN hlstats_Weapons w ON w.game = s.game AND w.code = e.weapon;

INSERT INTO hlstats_Events_Teamkills (timestamp_id, game_id, server_id, map, killerId, victimId, weapon)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.killerId, e.victimId, IFNULL(w.weaponId, 0)
FROM hlstatsx_ce.hlstats_Events_Teamkills e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map
LEFT JOIN hlstats_Weapons w ON w.game = s.game AND w.code = e.weapon;

INSERT INTO hlstats_Events_Suicides (timestamp_id, game_id, server_id, map, playerId, weapon)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.playerId, IFNULL(w.weaponId, 0)
FROM hlstatsx_ce.hlstats_Events_Suicides e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map
LEFT JOIN hlstats_Weapons w ON w.game = s.game AND w.code = e.weapon;

INSERT INTO hlstats_Events_PlayerActions (timestamp_id, game_id, server_id, map, playerId, actionId, bonus)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.playerId, a.gw_id, LEAST(GREATEST(e.bonus, -32768), 32767)
FROM hlstatsx_ce.hlstats_Events_PlayerActions e JOIN mig_srv s USING (serverId)
JOIN mig_action a ON a.ce_id = e.actionId
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_PlayerPlayerActions (timestamp_id, game_id, server_id, map,
    playerId, victimId, actionId, bonus)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.playerId, e.victimId, a.gw_id, LEAST(GREATEST(e.bonus, -32768), 32767)
FROM hlstatsx_ce.hlstats_Events_PlayerPlayerActions e JOIN mig_srv s USING (serverId)
JOIN mig_action a ON a.ce_id = e.actionId
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_TeamBonuses (timestamp_id, game_id, server_id, map, playerId, actionId, bonus)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.playerId, a.gw_id, LEAST(GREATEST(e.bonus, -128), 127)
FROM hlstatsx_ce.hlstats_Events_TeamBonuses e JOIN mig_srv s USING (serverId)
JOIN mig_action a ON a.ce_id = e.actionId
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

-- IPv4 addresses become integers (INET_ATON); anything else becomes 0.
INSERT INTO hlstats_Events_Connects (timestamp_id, game_id, map, playerId, ipAddress, hostname, hostgroup)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, IFNULL(m.id, 0), e.playerId,
    IFNULL(INET_ATON(IF(e.ipAddress REGEXP '^[0-9]+([.][0-9]+){3}$', e.ipAddress, NULL)), 0),
    e.hostname, e.hostgroup
FROM hlstatsx_ce.hlstats_Events_Connects e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_Entries (timestamp_id, game_id, server_id, map, playerId)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0), e.playerId
FROM hlstatsx_ce.hlstats_Events_Entries e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_Disconnects (timestamp_id, game_id, server_id, map, playerId)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0), e.playerId
FROM hlstatsx_ce.hlstats_Events_Disconnects e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_ChangeTeam (timestamp_id, game_id, server_id, map, playerId, team)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.playerId, IFNULL(t.teamId, 0)
FROM hlstatsx_ce.hlstats_Events_ChangeTeam e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map
LEFT JOIN hlstats_Teams t ON t.game = s.game AND t.code = e.team;

INSERT INTO hlstats_Events_ChangeRole (timestamp_id, game_id, map, playerId, role)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, IFNULL(m.id, 0),
    e.playerId, IFNULL(r.roleId, 0)
FROM hlstatsx_ce.hlstats_Events_ChangeRole e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map
LEFT JOIN hlstats_Roles r ON r.game = s.game AND r.code = e.role;

INSERT INTO hlstats_Events_ChangeName (timestamp_id, game_id, map, playerId, oldName, newName)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, IFNULL(m.id, 0),
    e.playerId, e.oldName, e.newName
FROM hlstatsx_ce.hlstats_Events_ChangeName e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_Chat (timestamp_id, game_id, server_id, playerId, message_mode, message)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, e.playerId,
    e.message_mode, e.message
FROM hlstatsx_ce.hlstats_Events_Chat e JOIN mig_srv s USING (serverId);

INSERT INTO hlstats_Events_Latency (timestamp_id, game_id, server_id, map, playerId, ping)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, e.serverId, IFNULL(m.id, 0),
    e.playerId, LEAST(e.ping, 65535)
FROM hlstatsx_ce.hlstats_Events_Latency e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

INSERT INTO hlstats_Events_Admin (timestamp_id, game_id, map, type, message, playerName)
SELECT IFNULL(UNIX_TIMESTAMP(e.eventTime), 0), s.game_id, IFNULL(m.id, 0),
    e.type, e.message, e.playerName
FROM hlstatsx_ce.hlstats_Events_Admin e JOIN mig_srv s USING (serverId)
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = e.map;

-- hlstats_Events_Rcon is not copied: it holds RCON passwords in clear text.

-- ---------------------------------------------------------------------------
-- 5. Server load and trend
-- ---------------------------------------------------------------------------

INSERT INTO hlstats_server_load (server_id, timestamp, act_players, min_players,
    max_players, map, uptime, fps)
SELECT l.server_id, l.timestamp, GREATEST(l.act_players, 0), GREATEST(l.min_players, 0),
    GREATEST(l.max_players, 0), IFNULL(m.id, 0),
    LEAST(IF(l.uptime REGEXP '^[0-9]+$', CAST(l.uptime AS UNSIGNED), 0), 65535),
    LEAST(IF(l.fps REGEXP '^[0-9]+([.][0-9]+)?$', ROUND(l.fps), 0), 65535)
FROM hlstatsx_ce.hlstats_server_load l JOIN mig_srv s ON s.serverId = l.server_id
LEFT JOIN hlstats_Maps m ON m.game = s.game AND m.map = l.map;

INSERT INTO hlstats_Trend (timestamp, game, players, kills, headshots, servers, act_slots, max_slots)
SELECT timestamp, game, GREATEST(players, 0), GREATEST(kills, 0), GREATEST(headshots, 0),
    LEAST(GREATEST(servers, 0), 65535), LEAST(GREATEST(act_slots, 0), 65535),
    LEAST(GREATEST(max_slots, 0), 65535)
FROM hlstatsx_ce.hlstats_Trend;

-- ---------------------------------------------------------------------------
-- 6. Summary: rows in CE and in gameWE (both columns should match, except
--    where noted in the guide)
-- ---------------------------------------------------------------------------
SELECT 'players' AS what, (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Players) AS ce,
       (SELECT COUNT(*) FROM hlstats_Players) AS gamewe
UNION ALL SELECT 'uniqueids', (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_PlayerUniqueIds), (SELECT COUNT(*) FROM hlstats_PlayerUniqueIds)
UNION ALL SELECT 'frags', (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Events_Frags), (SELECT COUNT(*) FROM hlstats_Events_Frags)
UNION ALL SELECT 'player actions', (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Events_PlayerActions), (SELECT COUNT(*) FROM hlstats_Events_PlayerActions)
UNION ALL SELECT 'connects', (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Events_Connects), (SELECT COUNT(*) FROM hlstats_Events_Connects)
UNION ALL SELECT 'history days', (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Players_History WHERE eventTime IS NOT NULL), (SELECT COUNT(*) FROM hlstats_Players_History)
UNION ALL SELECT 'award winners', (SELECT COUNT(*) FROM hlstatsx_ce.hlstats_Players_Awards), (SELECT COUNT(*) FROM hlstats_Awards_History)
UNION ALL SELECT 'kills of all players', (SELECT SUM(kills) FROM hlstatsx_ce.hlstats_Players), (SELECT SUM(kills) FROM hlstats_Players);
