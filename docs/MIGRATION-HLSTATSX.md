# Moving from HLstatsX:CE to gameWE

gameWE replaces the HLstatsX:CE daemon (`hlstats.pl`) and its web pages. Its
database is the gameME-style HLstatsX schema, not the CE schema, so the CE
data is converted on the way: players, rankings, names, clans, daily history,
award winners, events, server load and trend move over, and the collector
carries on from there. Moving from gameME instead is
[MIGRATION-GAMEME.md](MIGRATION-GAMEME.md).

The conversion is one command, `gamewe db convert`. It reads the CE
database over a connection of its own (only reading, in a read-only
session; on the same or another MariaDB / MySQL server, no copy needed),
creates the gameWE schema in a new database and converts every table into
it. Tested with the schema of HLstatsX:CE 1.7.0 (database version 80) and
MariaDB 11.4: `gamewe check` reports `schema: ok`, and new games are ranked
together with the converted players. The SQL script of the earlier
releases is kept as a fallback ([Appendix: the SQL
script](#appendix-the-sql-script)).

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

- The CE database and a way to dump it (`mariadb-dump` / `mysqldump`) for
  the backup.
- The database server for gameWE: MariaDB 11.4 or newer. The CE database
  can be on the same server or on another one. `gamewe db convert` does not
  write into PostgreSQL.
- The gameWE collector (the `gamewe` binary, `schema/init/`), see
  [INSTALLATION.md](INSTALLATION.md).

The commands are for `bash`. `change-me` stands for passwords you choose.

## 1. Stop CE and back up

Stop what writes into the CE database, so nothing is lost between the
conversion and the switch:

```bash
# the daemon and the cron jobs (hlstats-awards.pl, ...), however you run them
pkill -f hlstats.pl
crontab -e        # comment out the HLstatsX lines
```

The collector takes over their jobs: the daily awards (`features.daily_awards`)
and, if you want it, moving players without play for `MinActivity` days to
`hlstats_PlayersInactive` (`features.inactive_players`, off by default; see
CONFIG.md).

Dump the CE database (the name is often `hlstatsx`) and keep the dump as
your backup:

```bash
mariadb-dump --single-transaction hlstatsx > hlstatsx-ce.sql
```

If the game servers keep running meanwhile, their log lines are lost until
they send to the gameWE collector (step 5). Plan the move for a quiet time.

## 2. Create the gameWE database

```bash
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -u root -p
```

`init-db.sh` creates the database `gamewe` with its three users
([INSTALLATION.md](INSTALLATION.md), section C). Point `database.*` of
`/etc/gamewe/collector/config.yaml` to it and **do not start the collector
yet**. `gamewe db convert` creates the tables itself; a database that
`gamewe db init` (or a collector start) already set up is fine too, as long
as it has no players, servers, clans or frags.

Let the collector user read the CE database (it only reads):

```bash
mariadb -u root -p -e "GRANT SELECT ON hlstatsx.* TO 'gamewe'@'%'"   # host as in init-db.sh
```

On another server, use a user of that server instead (step 3,
`mysql://user@host/name`); it needs `SELECT` on the CE database.

## 3. Convert

First the plan only (`-n`, `--dry-run`):

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db convert -from hlstatsx -n
```

`-from` names the CE database: a name (on the server of the gameWE database,
with the collector's user) or `mysql://user@host:port/name` for another
server or user; its password goes into `GAMEWE_FROM_PASSWORD`, never on the
command line. TLS and timeouts are those of `database.*`.

`-source-prefix` is the `DB_PREFIX` of CE when it is not `hlstats` (CE's
tables are then `<prefix>_Players`, ...). The gameWE tables get
`database.table_prefix` of the config ([DATABASE.md](DATABASE.md#table-prefix)).
With different prefixes CE and gameWE may be in the same database:
`-from` then names that database.

The plan shows (example with test data, shortened):

```
source:      hlstatsx on 127.0.0.1:3306 (11.4.13-MariaDB-ubu2404, HLstatsX:CE 1.7.0 (database version 80)), only read
target:      gamewe on 127.0.0.1:3306 (new: the gameWE schema (version 6) is created first)
time zone:   eventTime is local time in Europe/Berlin (-source-timezone; the CE server has SYSTEM = UTC)
             e.g. the first frag: 2024-03-29 00:00:01 -> 2024-03-28 23:00:01 UTC
characters:  latin1_swedish_ci (4 tables), utf8mb4_unicode_ci (50 tables) -> utf8mb4_unicode_ci; double-encoded UTF-8: -mojibake auto
             2 player names look double-encoded (MÃ¼ller) and are repaired
plan (35 steps, rows in CE):
   1.  games                  29      hlstats_Games -> hlstats_Games      games gameWE has keep their gameWE values; the others are added
   2.  servers                3       hlstats_Servers -> hlstats_Servers  same serverId, RCON password included; the collector looks the location up again
   ...
  19.  frags                  200000  hlstats_Events_Frags -> hlstats_Events_Frags  weapon -> id; positions kept; killerRole, victimRole left out
   ...
not converted:
      hlstats_Events_Rcon             1     RCON commands with the RCON passwords in clear text
      hlstats_Options                 66    CE's settings; gameWE has its own (web admin, gamewe option set)
   ...
```

Check above all the **time zone**. CE stored `eventTime` as the local time
of its database server; gameWE stores Unix seconds (UTC). By default
(`-source-timezone server`) the CE server converts in its own time zone,
which is right as long as it still has the zone CE ran with (the plan shows
it: `SYSTEM = CET`, ...). Otherwise give the zone:
`-source-timezone Europe/Berlin` (summer time included; a time in the hour
skipped in spring becomes the first second after it, a time of the hour
that exists twice in autumn the first of the two, as MariaDB does it),
`UTC` or a fixed offset such as `+01:00`. The example line converts the
first frag, to check.

**Character sets**: CE 1.7 tables are utf8mb4, older installations latin1
or utf8; the CE server delivers every text as UTF-8, so nothing has to be
converted beforehand. Old installations often stored UTF-8 text in latin1
tables (shown as `MÃ¼ller` instead of `Müller`): `-mojibake auto` (default)
repairs such text in latin1 tables; `-mojibake repair` also in utf8 tables
(the plan warns when it sees such names there), `keep` takes the text as
it is. Player names that are equal in gameWE's `utf8mb4_unicode_ci` (case,
accents: `Müller` and `muller` of one player) are merged into one row
with the counters added.

Then convert:

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db convert -from hlstatsx -source-timezone Europe/Berlin
```

It shows the plan again and asks `Convert now? [y/N]` (`--force` for scripts);
it refuses while a collector runs on the gameWE database and holds the
collector lock meanwhile, so none starts. Large event tables take a while: it copies in batches of 500 rows and
builds the indexes of the event tables once at the end of each table,
about 13,000 frags a second on MariaDB 11.4 (200,000 frags and 260,000
other events: 30 seconds); run it in `tmux` or `screen`. At the end it
compares the rows:

```
rows in CE and in gameWE:
        CE     gameWE
        40         40  players
    103041     103041  kills of all players
    200000     199200  hlstats_Events_Frags (differs)
     20000      20000  hlstats_Events_Connects
   ...
```

Both columns should be equal. Fewer rows in gameWE mean events of servers
that are no longer in CE's `hlstats_Servers` or of actions CE no longer
has (they cannot be assigned; above: frags of a deleted server), history
rows without a date, or merged player names.

If it fails halfway (for example a value out of range in an unusual
installation), fix the cause, empty the gameWE database
(`gamewe db reset --force`) and convert again. The CE database is not
changed. The indexes of the event tables are switched off while a table is
copied and built again right after it, also after an error or Ctrl-C; only
a process killed hard (`kill -9`, power loss) can leave them off, and the
next `gamewe db convert` builds them again.

## 4. Finish the database

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
  over your CE rewards and weapon modifiers instead (CE database `hlstatsx`
  on the same server):

  ```sql
  UPDATE hlstats_Actions a JOIN hlstatsx.hlstats_Actions c ON c.game = a.game AND c.code = a.code
  SET a.reward_player = c.reward_player, a.reward_team = c.reward_team;
  UPDATE hlstats_Weapons w JOIN hlstatsx.hlstats_Weapons c ON c.game = w.game AND c.code = w.code
  SET w.modifier = c.modifier;
  ```

When everything is checked, take the right back
(`REVOKE SELECT ON hlstatsx.* FROM 'gamewe'@'%'`).

## 5. Check and switch the game servers

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
   address. A `sv_logsecret` can stay: store the same number with
   `gamewe server set <id> -logsecret <n>` or in the web admin, or remove it
   (a server without a stored secret is not checked; CONFIG.md, "Log secret").
3. Start the collector; `gamewe logaddress -server all` shows whether every
   server sends to it.

Keep `hlstatsx-ce.sql` as the backup.

(An HLstatsX database of the variant gameWE writes, with `timestamp_id` in
the event tables instead of CE's `eventTime`, needs no conversion: `gamewe
db migrate` upgrades it in place, once by hand with a backup (the
collector's start does not do it automatically),
[DATABASE.md](DATABASE.md#schema-versions-and-upgrades). For CE it refuses
and points to `gamewe db convert`.)

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
| `hlstats_PlayerNames` | the same | copied; names equal in `utf8mb4_unicode_ci` (case, accents) merged, counters added; no `lastuse` → `0000-00-00 00:00:00` |
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
| `hlstats_Games_Defaults`, `_Games_Supported`, `_Mods_*`, `hlstats_Heatmap_Config`, `hlstats_Countries`, `geoLiteCity_*` | CE's own reference data; gameWE ships its own (`gamewe db migrate` / `gamewe db seed`, GeoIP database) |
| `hlstats_Ranks`, `hlstats_Ribbons`, `hlstats_Ribbons_Triggers`, `hlstats_Players_Ribbons` | gameWE has its own rank and ribbon definitions and derives them per player |
| `hlstats_ClanTags` | gameWE ships its own clan tag patterns |
| `Players.lastAddress` | the last IP; gameWE keeps IPs only in `hlstats_Events_Connects` |
| `Players.activity`, `mmrank`, `displayEvents`, `blockavatar`, `Awards.g_winner_*`, `Clans.hidden`, `mapregion` | no such columns in gameWE |
| per-player weapon, map, team and role statistics | CE has no such tables; gameWE fills `hlstats_Players_Weapons` and `_Actions`, and shots and hits of `hlstats_Players_Maps`, from the switch on |

## Appendix: the SQL script

[migration/hlstatsx-ce-to-gamewe.sql](migration/hlstatsx-ce-to-gamewe.sql)
does the same conversion in SQL, inside one MariaDB server, for when the
`gamewe` binary cannot reach the CE database. It differs from
`gamewe db convert` in three points: it needs a copy of the CE database
in utf8mb4 on the gameWE server, it converts the times in the session time
zone set at its top (`time_zone`), and it stops at player names that are
equal in `utf8mb4_unicode_ci` instead of merging them. Get the script from
the collector's source (repository or source archive of the release).

1. Steps 1 and 2 above (`init-db.sh`, then `gamewe db init`).
2. Load a copy of the CE dump next to gameWE:

   ```bash
   mariadb -e "CREATE DATABASE hlstatsx_ce CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
   mariadb hlstatsx_ce < hlstatsx-ce.sql
   ```

   The script expects the name `hlstatsx_ce` (another name: replace
   `hlstatsx_ce.` in the script, `sed -i 's/hlstatsx_ce\./mycopy./g' hlstatsx-ce-to-gamewe.sql`).
   Never run it against the live CE database.
3. The copy must use `utf8mb4_unicode_ci` like gameWE. Check:

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

   - **`latin1`:** first look at the names
     (`SELECT name FROM hlstatsx_ce.hlstats_PlayerNames WHERE name REGEXP 'Ã|Â' LIMIT 10;`).
     No such names: convert as above. Such names (UTF-8 stored in latin1):
     load the copy again byte for byte, then convert:

     ```bash
     mariadb -e "DROP DATABASE hlstatsx_ce; CREATE DATABASE hlstatsx_ce"
     mariadb-dump --default-character-set=latin1 hlstatsx \
       | sed '1,40s/SET NAMES latin1/SET NAMES utf8mb4/' \
       | sed 's/CHARSET=latin1 COLLATE=latin1_swedish_ci/CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci/; s/CHARSET=latin1/CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci/' \
       | mariadb --default-character-set=utf8mb4 hlstatsx_ce
     ```

   A `Duplicate entry` error while converting means two names that were
   different before are equal in `utf8mb4_unicode_ci`; delete or rename
   one of them in the copy and run the command again.
4. Set `time_zone` at the top of the script to the zone CE wrote `eventTime`
   in (`SYSTEM` if the gameWE server has the same zone; else e.g.
   `'+01:00'`, or `'Europe/Berlin'` with the time zone tables loaded,
   `mariadb-tzinfo-to-sql /usr/share/zoneinfo | mariadb mysql`), then run it:

   ```bash
   mariadb gamewe < hlstatsx-ce-to-gamewe.sql
   ```

   It stops before changing anything if `gamewe` already has players or
   servers or the copy is empty, and prints the same comparison of rows at
   the end. Then steps 4 and 5 above; drop the copy afterwards
   (`DROP DATABASE hlstatsx_ce;`).
