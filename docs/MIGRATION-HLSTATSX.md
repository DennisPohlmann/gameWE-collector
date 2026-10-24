# Moving from HLstatsX:CE to gameWE

gameWE replaces the HLstatsX:CE daemon (`hlstats.pl`) and its web pages. Its
database is the gameME-style HLstatsX schema, not the CE schema, so the CE
data is converted on the way: players, rankings, names, clans, daily history,
award winners, events, server load and trend move over, and the collector
carries on from there. Moving from gameME instead is
[MIGRATION-GAMEME.md](MIGRATION-GAMEME.md).

The conversion is one SQL script,
[migration/hlstatsx-ce-to-gamewe.sql](migration/hlstatsx-ce-to-gamewe.sql). It
reads a copy of the CE database and writes into a new gameWE database on the
same MariaDB server. Tested with the schema of HLstatsX:CE 1.7.0 (database
version 80) and MariaDB 11.4: `gamewe check` reports `schema: ok`, and new
games are ranked together with the converted players.

## What changes between the schemas

| | HLstatsX:CE | gameWE |
|---|---|---|
| event time | `eventTime` (local date and time) | `timestamp_id` (Unix seconds, UTC) |
| server, game of an event | `serverId` | `server_id` and `game_id` (`hlstats_Games.id`) |
| map, weapon, team, role of an event | the name / code as text | the id of `hlstats_Maps`, `_Weapons`, `_Teams`, `_Roles` |
| player IP | text (`ipAddress`, `Players.lastAddress`) | IPv4 as an integer in `hlstats_Events_Connects` only |
| player name, first seen | `Players.lastName`, `createdate` | `hlstats_Players_Data` (`lastName`, `first_connect`, `last_connect`) |
| player location | columns of `hlstats_Players` | `hlstats_Players_Locations`, referenced by `Players_Data.location_id` |
| map counters | `hlstats_Maps_Counts` | `hlstats_Maps` |
| award winners | `hlstats_Players_Awards` | `hlstats_Awards_History` |
| ranks, ribbons | `hlstats_Ranks`, `_Ribbons`, `_Players_Ribbons` | `hlstats_Definition_Ranks_Ribbons`; the web derives a player's rank and ribbons |
| character set | utf8mb4 (CE 1.7), older installations latin1 or utf8 | utf8mb4 |

The full mapping per table is in [Table mapping](#table-mapping).

## What you need

- The CE database and a way to dump it (`mariadb-dump` / `mysqldump`).
- The database server for gameWE: MariaDB 11.4 or newer. The CE copy and
  the gameWE database have to be on the same server for the script. This
  guide does not cover PostgreSQL as the target.
- Free disk space for the CE dump, the copy and the gameWE database (about
  three times the size of the CE database).
- The gameWE collector package (for `schema/init/`), see
  [INSTALLATION.md](INSTALLATION.md), and the script
  `docs/migration/hlstatsx-ce-to-gamewe.sql` from the collector's source
  (repository or source archive of the release).

The commands are for `bash`. `change-me` stands for passwords you choose.

## 1. Stop CE and back up

Stop what writes into the CE database, so the dump is complete and nothing
is lost between the dump and the switch:

```bash
# the daemon and the cron jobs (hlstats-awards.pl, ...), however you run them
pkill -f hlstats.pl
crontab -e        # comment out the HLstatsX lines
```

Dump the CE database (the name is often `hlstatsx`). The dump is your backup
and the source of the copy; keep it:

```bash
mariadb-dump --default-character-set=utf8mb4 hlstatsx > hlstatsx-ce.sql
```

If the game servers keep running meanwhile, their log lines are lost until
they send to the gameWE collector (step 7). Plan the move for a quiet time.

## 2. Load a copy next to gameWE

On the gameWE database server, as a database admin:

```bash
mariadb -e "CREATE DATABASE hlstatsx_ce CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
mariadb hlstatsx_ce < hlstatsx-ce.sql
```

The script expects the name `hlstatsx_ce`. With another name, replace
`hlstatsx_ce.` in the script (`sed -i 's/hlstatsx_ce\./mycopy./g' hlstatsx-ce-to-gamewe.sql`).
Never run it against the live CE database: it only reads it, but step 3 changes
the copy.

## 3. Character set of the copy

The script compares names and codes of both databases, so the copy must use
`utf8mb4_unicode_ci` like gameWE. Check:

```sql
SELECT table_collation, COUNT(*) FROM information_schema.tables
WHERE table_schema = 'hlstatsx_ce' GROUP BY 1;
```

- **Only `utf8mb4_unicode_ci`** (CE 1.7 installs): nothing to do.
- **Other collations or `utf8` / `utf8mb3`:** convert the tables of the copy:

  ```bash
  mariadb -N -e "SELECT CONCAT('ALTER TABLE \`', table_name, '\` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;')
    FROM information_schema.tables WHERE table_schema = 'hlstatsx_ce' AND table_collation <> 'utf8mb4_unicode_ci'" \
    | mariadb hlstatsx_ce
  ```

- **`latin1`:** first look at the names. Old installations often stored UTF-8
  text in latin1 tables; it then shows as `MÃ¼ller` instead of `Müller`:

  ```sql
  SELECT name FROM hlstatsx_ce.hlstats_PlayerNames WHERE name REGEXP 'Ã|Â' LIMIT 10;
  ```

  No such names: convert as above. Such names: do not convert, load the copy
  again byte for byte instead, then convert:

  ```bash
  mariadb -e "DROP DATABASE hlstatsx_ce; CREATE DATABASE hlstatsx_ce"
  mariadb-dump --default-character-set=latin1 hlstatsx \
    | sed '1,40s/SET NAMES latin1/SET NAMES utf8mb4/' \
    | sed 's/CHARSET=latin1 COLLATE=latin1_swedish_ci/CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci/; s/CHARSET=latin1/CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci/' \
    | mariadb --default-character-set=utf8mb4 hlstatsx_ce
  ```

  (run against the CE database server, or against a copy of `hlstatsx-ce.sql`
  loaded as `hlstatsx`; then the `ALTER TABLE` command above for what is left).

A `Duplicate entry` error while converting means two names that were
different before are equal in `utf8mb4_unicode_ci` (case, accents); delete
or rename one of them in the copy and run the command again.

## 4. Create the gameWE database

```bash
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -u root -p
```

It creates the database `gamewe` with its three users, the tables, the
gameWE reference data (games, teams, roles, actions with rewards, weapons with
modifiers, awards, ranks, ribbons, settings) and the rights
([INSTALLATION.md](INSTALLATION.md), section C). The script needs this new,
empty database.

## 5. Run the conversion

At the top of the script, set the time zone in which CE wrote `eventTime`:
the time zone of the CE database server (CE's events were stored in the
server's local time). If the gameWE database server has the same zone, keep
`SYSTEM`; otherwise e.g. `SET time_zone = '+01:00';` (or a name like
`'Europe/Berlin'` if the time zone tables are loaded,
`mariadb-tzinfo-to-sql /usr/share/zoneinfo | mariadb mysql`).

```bash
mariadb gamewe < hlstatsx-ce-to-gamewe.sql
```

It stops before changing anything if `gamewe` already has players or
servers (`this gameWE database is not new`) or the copy is empty. Large
event tables take a while (it copies row by row in SQL); run it in `tmux` or
`screen`. At the end it prints a summary:

```
what                  ce      gamewe
players               ...     ...
uniqueids             ...     ...
frags                 ...     ...
```

Both columns should be equal. Fewer rows in gameWE mean events of servers
that are no longer in CE's `hlstats_Servers` (they cannot be assigned to a
game) or history rows without a date; both are left out.

If the script fails halfway (for example a value out of range in an
unusual installation), fix the cause, drop the gameWE database and start
again with step 4. The copy is not changed by the script.

## 6. Finish the database

In `mariadb gamewe`:

```sql
-- every game (ranking) of your servers needs its base game; CE's games keep
-- their realgame, clones (css2, ...) included
SELECT g.code, g.name, g.realgame, COUNT(s.serverId) AS servers
FROM hlstats_Games g LEFT JOIN hlstats_Servers s ON s.game = g.code GROUP BY g.code;

-- the servers, with their RCON passwords from CE
SELECT serverId, address, port, game, name, rcon_password <> '' AS has_rcon FROM hlstats_Servers;
```

- Check `realgame` against [GAMES.md](GAMES.md); gameWE supports the games
  listed there. Rankings of other games (CE knew many mods) stay in the
  database but are not collected.
- Admin logins (`hlstats_Users`) are copied. The gameWE web accepts the CE
  MD5 passwords and replaces them with scrypt at the next sign-in. Create new
  ones with `npm run create-admin -- <name>` in the web frontend.
- CE's settings (`hlstats_Options`) are not copied. Set the gameWE settings in
  the web admin or with `gamewe option set` ([CONFIG.md](CONFIG.md)), e.g. the
  site name and `gamewe_start_skill`.
- The gameWE reference data was kept where both have a code (rewards,
  modifiers, names); codes only CE had were added with CE's values. To take
  over your CE rewards and weapon modifiers instead:

  ```sql
  UPDATE hlstats_Actions a JOIN hlstatsx_ce.hlstats_Actions c ON c.game = a.game AND c.code = a.code
  SET a.reward_player = c.reward_player, a.reward_team = c.reward_team;
  UPDATE hlstats_Weapons w JOIN hlstatsx_ce.hlstats_Weapons c ON c.game = w.game AND c.code = w.code
  SET w.modifier = c.modifier;
  ```

## 7. Check and switch the game servers

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check
```

Expected: `schema: ok`, every server listed with its game. Then in the web:
the rankings show the CE players with their points, kills and names; the
player pages have their daily history and awards; maps and weapons have
their counters.

Switch every game server ([INSTALLATION.md](INSTALLATION.md), section D):

1. Replace the CE SourceMod or AMX Mod X plugin (and SuperLogs, if used) with
   the gameWE plugin.
2. In `server.cfg`, replace CE's `logaddress_add` with the collector's
   address. Remove `sv_logsecret`: gameWE accepts log packets by the server
   address in `hlstats_Servers`, not by a log secret.
3. Start the collector; `gamewe logaddress -server all` shows whether every
   server sends to it.

When everything is checked, drop the copy (`DROP DATABASE hlstatsx_ce;`),
and keep `hlstatsx-ce.sql` as the backup.

## Table mapping

| HLstatsX:CE | gameWE | How |
|-------------|--------|-----|
| `hlstats_Games` | `hlstats_Games` | games gameWE does not have are added (code, name, hidden, realgame); gameWE's own games keep their values |
| `hlstats_Servers` | `hlstats_Servers` | same `serverId`; address, port, name, game, RCON password, counters; `sortorder` → `display_id`; location is looked up again by the collector |
| `hlstats_Users`, `hlstats_HostGroups` | the same | copied |
| `hlstats_Teams`, `_Roles`, `_Weapons`, `_Actions`, `_Awards` | the same | by (game, code): missing codes added, counters (`kills`, `headshots`, `count`) taken over; codes that only appear in events are added |
| `hlstats_Maps_Counts` and the map names of the events | `hlstats_Maps` | one row per (game, map) with `kills`, `headshots` |
| `hlstats_Clans` | `hlstats_Clans` | same `clanId`; `hidden`, `mapregion` dropped |
| `hlstats_Players` | `hlstats_Players`, `_Players_Data`, `_Players_Locations` | same `playerId`; `teamkills` → `team_kills`, `hpk` and `kpd` computed, `game_id` set; `lastName`, `createdate`, `last_event`, profile fields → `Players_Data`; city, state, country, flag, lat/lng → a location; `hideranking` 3 (CE: inactive) → 0 |
| `hlstats_PlayerUniqueIds` | the same | `merge` dropped; the uniqueId format (`0:12345`) is the same |
| `hlstats_PlayerNames` | the same | copied |
| `hlstats_Players_History` | the same | `teamkills` → `team_kills`; rows without a date left out |
| `hlstats_Players_Awards` | `hlstats_Awards_History` | per day: award (by code), winner, count; `Awards.d_winner_*` taken over |
| `hlstats_Events_Frags`, `_Teamkills`, `_Suicides` | the same | weapon → id; Frags: positions kept, `killerRole`, `victimRole` dropped |
| `hlstats_Events_PlayerActions`, `_PlayerPlayerActions`, `_TeamBonuses` | the same | action → gameWE action id; positions dropped |
| `hlstats_Events_Connects` | the same | IPv4 → integer (IPv6 and others → 0); `eventTime_Disconnect` dropped |
| `hlstats_Events_Entries`, `_Disconnects`, `_ChangeName`, `_Admin`, `_Chat`, `_Latency` | the same | time, server, game, map converted |
| `hlstats_Events_ChangeTeam`, `_ChangeRole` | the same | team / role → id |
| `hlstats_server_load` | the same | map → id, `uptime` and `fps` as numbers |
| `hlstats_Trend` | the same | copied |

## What is not migrated

| CE table / data | Why |
|-----------------|-----|
| `hlstats_Events_Rcon` | RCON commands with the RCON passwords in clear text |
| `hlstats_Events_Statsme`, `_Statsme2`, `_StatsmeLatency`, `_StatsmeTime` | no such tables in gameWE; the weapon statistics come from the gameWE plugin (`hlstats_Players_Weapons`) from now on |
| `hlstats_Livestats` | who is online now; rebuilt by the collector |
| `hlstats_Options`, `_Options_Choices`, `hlstats_Servers_Config*`, `_Servers_VoiceComm` | CE's settings and server configuration; gameWE has its own (`gamewe_*`) |
| `hlstats_Games_Defaults`, `_Games_Supported`, `_Mods_*`, `hlstats_Heatmap_Config`, `hlstats_Countries`, `geoLiteCity_*` | CE's own reference data; gameWE ships its own (`02_seed.sql`, GeoIP database) |
| `hlstats_Ranks`, `hlstats_Ribbons`, `hlstats_Players_Ribbons` | gameWE has its own rank and ribbon definitions and derives them per player |
| `Players.lastAddress` | the last IP; gameWE keeps IPs only in `hlstats_Events_Connects` |
| `Players.activity`, `mmrank`, `displayEvents`, `blockavatar`, `Awards.g_winner_*`, `Clans.hidden`, `mapregion` | no such columns in gameWE |
| per-player weapon, map, team and role statistics | CE has no such tables; gameWE fills `hlstats_Players_Weapons` and `_Actions` from the switch on |
