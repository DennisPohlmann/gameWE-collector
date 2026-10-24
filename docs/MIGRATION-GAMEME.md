# Moving from gameME to gameWE

gameME hands out the statistics of an account as a ZIP file (the data export).
This guide turns that ZIP into a gameWE database: players, rankings, history,
awards, clans, events and server load stay, and the collector carries on from
there. Moving from a self-hosted HLstatsX:CE instead is
[MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md).

Tested with a real gameME export (CS:S, three servers, about 400 000
players and 1.2 million events) and MariaDB 11.4: the import takes about a minute,
`gamewe check` reports `schema: ok`, and new games are ranked together with the
imported players.

## What is in the ZIP

The ZIP holds no SQL dump. It is the database directory of a MySQL server:

```
upload/<account>/hlstatsx_<id>/hlstats_Players.frm    table definition
                               hlstats_Players.MYD    data (MyISAM)
                               hlstats_Players.MYI    indexes
                               ... (54 tables)
```

What this means for the import:

- The files have to be opened by a MariaDB server (they are copied into its
  data directory), then copied into gameWE with `mariadb-dump`. A temporary
  MariaDB container does that without touching your real database server.
- The tables use the gameME-style HLstatsX schema, the one gameWE was built
  on: the same tables and columns, only a few columns are new in gameWE (see
  [Table mapping](#table-mapping)). No values have to be converted.
- The tables are declared `latin1`, but the text in them (player names, chat,
  server names) is UTF-8. It is dumped byte for byte (`--default-character-set=latin1`)
  and read back as utf8mb4, so `MÃ¼ller` becomes `Müller` again and emoji survive.

## What you need

- The gameME ZIP, and a copy of it you keep unchanged.
- The database server for gameWE: MariaDB 11.4 or newer (MySQL 8 is not
  supported by the gameWE scripts). This guide does not cover PostgreSQL as
  the target.
- Docker or Podman for the temporary MariaDB (any MariaDB 10.6+ that you can
  copy files into works too).
- Free disk space: about three times the unpacked size of the ZIP.
- The gameWE collector package (`schema/init/` and the `gamewe` binary), see
  [INSTALLATION.md](INSTALLATION.md).

The commands are for `bash` (in `zsh` the table lists would not be split into
names). `change-me` stands for passwords you choose.

## 1. Backup

The gameME data is only read. Keep the ZIP as it is. If the gameWE database
server already holds data, back it up before you start
(`mariadb-dump --single-transaction --all-databases > before-gameme.sql`).

## 2. Open the ZIP in a temporary MariaDB

```bash
mkdir -p ~/gameme && cd ~/gameme
unzip /path/to/export.zip
ls upload/*/                                   # hlstatsx_<id>: the database directory

docker run -d --name gameme-import -e MARIADB_ROOT_PASSWORD=change-me \
    -v "$PWD/upload":/import:ro mariadb:11.4
sleep 20                                       # until the server accepts connections

# copy the directory into the data directory as the database gameme_import
docker exec gameme-import sh -c \
    'cp -r /import/*/hlstatsx_* /var/lib/mysql/gameme_import && chown -R mysql:mysql /var/lib/mysql/gameme_import'

docker exec gameme-import mariadb -uroot -pchange-me gameme_import \
    -e "SHOW TABLE STATUS" | awk '{print $1, $2, $5}'
```

Every table shows the engine `MyISAM` and a row count (`hlstats_Livestats` is
a `MEMORY` table and always empty). If a table reports an error, run
`CHECK TABLE <name>` and `REPAIR TABLE <name>` in `gameme_import`.

## 3. Create the gameWE database

On the gameWE database server, with the init script of the collector package
(as in [INSTALLATION.md](INSTALLATION.md), section C):

```bash
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -u root -p
```

It creates the database `gamewe` with its three users. Then create the
tables, the reference data and the web users' rights with the collector
(`database.*` in its config pointing to this database):

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db migrate -f
```

Use a new, empty database; the import replaces the content of most tables.

## 4. Copy the data

`mariadb-dump` writes the data of the export (no table definitions) with the
column names, so the columns that are new in gameWE keep their defaults.

```bash
cd ~/gameme
SKIP="'hlstats_Events_Rcon','hlstats_Livestats','hlstats_Options'"
TABLES=$(docker exec gameme-import mariadb -uroot -pchange-me -N -e \
  "SELECT table_name FROM information_schema.tables WHERE table_schema = 'gameme_import' AND table_name NOT IN ($SKIP) ORDER BY 1")

# the data, as UTF-8
docker exec gameme-import mariadb-dump -uroot -pchange-me --default-character-set=latin1 \
    --no-create-info --complete-insert --skip-triggers gameme_import $TABLES \
  | sed '1,40s/SET NAMES latin1/SET NAMES utf8mb4/' > gameme-data.sql

# the site settings (sitename, siteurl, ...): replace the gameWE defaults,
# the gamewe_* settings stay
docker exec gameme-import mariadb-dump -uroot -pchange-me --default-character-set=latin1 \
    --no-create-info --complete-insert --replace gameme_import hlstats_Options \
  | sed '1,40s/SET NAMES latin1/SET NAMES utf8mb4/' > gameme-options.sql

grep -c 'SET NAMES utf8mb4' gameme-data.sql gameme-options.sql    # 1 each
```

Copy both files to the gameWE database server. There, empty the tables the
dump fills (they hold the gameWE reference data, with other ids than
gameME's), then load the files. As a database admin who signs in without a
password prompt (socket, or `~/.my.cnf`):

```bash
TABLES=$(mariadb -N -e "SELECT table_name FROM information_schema.tables WHERE table_schema = 'gamewe'
  AND table_name NOT IN ('hlstats_Events_Rcon','hlstats_Livestats','hlstats_Options','hlstats_Events_Latency','hlstats_gamewe_jobs')")
for t in $TABLES; do mariadb gamewe -e "TRUNCATE TABLE \`$t\`"; done
mariadb --default-character-set=utf8mb4 gamewe < gameme-data.sql
mariadb --default-character-set=utf8mb4 gamewe < gameme-options.sql
```

These are the same tables as in the dump (gameWE has every table of the
export, plus `hlstats_Events_Latency` and `hlstats_gamewe_jobs`). The rights
of the web users stay: `TRUNCATE` keeps the tables. `ERROR 1062 Duplicate
entry` means a table was not emptied; `Incorrect string value` means the text
was not dumped with `--default-character-set=latin1`.

## 5. Finish the database

```bash
# add the gameWE reference data that gameME did not have (other games,
# weapons, actions, awards, settings); existing rows are not changed
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db seed
```

Then, in `mariadb gamewe`:

```sql
-- realgame: the base game of every gameME ranking (gameME has no such column).
-- All CS:S rankings (css, css2, css5, ...) are 'css'; for others see GAMES.md.
SELECT id, code, name, realgame FROM hlstats_Games;
UPDATE hlstats_Games SET realgame = 'css' WHERE realgame = '' AND code LIKE 'css%';

-- servers: address and port must be where the game server's log packets
-- come from; the RCON passwords are not in the export
SELECT serverId, address, port, game, name FROM hlstats_Servers;
```

- Set the RCON password of every server in the web admin, or with
  `gamewe server set <id> -rcon-password-file <file>` ([INSTALLATION.md](INSTALLATION.md), section D).
- `hlstats_Users` holds the gameME web logins with MD5 passwords. The gameWE
  web accepts them and replaces them with scrypt at the next sign-in. Better:
  delete them (`DELETE FROM hlstats_Users;`) and create your own login with
  `npm run create-admin -- <name>` in the web frontend.

Optional: the events of gameME have no `server_id` (it is 0, so the web shows
them per game but not per server). For games with exactly one server, it can
be filled in:

```sql
UPDATE hlstats_Events_Frags e
JOIN hlstats_Games g ON g.id = e.game_id
JOIN (SELECT game, MIN(serverId) AS serverId FROM hlstats_Servers
      GROUP BY game HAVING COUNT(*) = 1) s ON s.game = g.code
SET e.server_id = s.serverId
WHERE e.server_id = 0;
-- the same for hlstats_Events_Teamkills, _Suicides, _PlayerActions,
-- _PlayerPlayerActions, _TeamBonuses, _Entries, _Disconnects, _ChangeTeam
```

## 6. Check

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check
```

Expected: `schema: ok`, every server listed with its game, and no
`missing` lines. Compare the row counts with the export:

```bash
docker exec gameme-import mariadb -uroot -pchange-me -N -e \
  "SELECT table_name, table_rows FROM information_schema.tables WHERE table_schema = 'gameme_import' ORDER BY 1" > rows-gameme.txt
mariadb -u root -p -N -e \
  "SELECT table_name, table_rows FROM information_schema.tables WHERE table_schema = 'gamewe' ORDER BY 1" > rows-gamewe.txt
diff rows-gameme.txt rows-gamewe.txt
```

The only differences should be `hlstats_Events_Rcon` (0, not copied),
`hlstats_Options` and the tables the seed added rows to
(`hlstats_Games`, `_Weapons`, `_Actions`, `_Teams`, `_Roles`, `_Awards`,
`_Definition_*`, `_ClanTags`), and the new gameWE tables
(`hlstats_Events_Latency`, `hlstats_gamewe_jobs`). In the web: the rankings
show the gameME players with their points, names with umlauts and symbols are
readable, and the awards history has the old days.

## 7. Switch the game servers

Until now gameME keeps collecting. Switch every server over:

1. Install the gameWE SourceMod plugin (or AMX Mod X plugin for GoldSrc) and
   remove the gameME plugin; or keep the gameME plugin for now and set
   `gamewe_command_prefix` to `gameme` ([CONFIG.md](CONFIG.md)).
2. In `server.cfg`: replace gameME's `logaddress_add` with the collector's
   address and set the `rcon_password` you entered above
   ([INSTALLATION.md](INSTALLATION.md), section D).
3. Start the collector and watch the journal: `gamewe logaddress -server all`
   shows whether every server sends its logs.

Events between the date of the export and the switch are not in gameWE. Ask
gameME for the export as late as possible, ideally on the day you switch.

When everything is checked, remove the temporary MariaDB and the files:
`docker rm -f gameme-import`, `rm -r ~/gameme` (keep the ZIP).

## Table mapping

Every table of the export goes into the table of the same name with the same
columns. Differences:

| gameWE table | Difference | After the import |
|--------------|------------|------------------|
| `hlstats_Events_Frags`, `_Teamkills`, `_Suicides`, `_PlayerActions`, `_PlayerPlayerActions`, `_TeamBonuses`, `_Entries`, `_Disconnects`, `_ChangeTeam` | new column `server_id` | 0 for gameME events (see the optional step in 5) |
| `hlstats_Events_Frags` | new columns `pos_x` … `pos_victim_z` | NULL for gameME kills (no heatmap for them) |
| `hlstats_Games` | new column `realgame` | set in step 5 |
| `hlstats_Servers` | new column `rcon_password` | empty, set in step 5 |
| `hlstats_PlayerUniqueIds.uniqueId` | `varchar(64)` instead of 255 | SteamIDs fit |
| `hlstats_ClanTags.pattern` | `varchar(250)` instead of 255 | patterns fit |
| `hlstats_Events_Latency`, `hlstats_gamewe_jobs` | new tables | filled by the collector from now on |

## What is not migrated

| What | Why |
|------|-----|
| `hlstats_Events_Rcon` | the RCON commands of the old servers, with the RCON passwords in clear text |
| `hlstats_Livestats` | who is online now; empty in the export, rebuilt by the collector |
| RCON passwords | not in the export |
| gameME's web settings for its own pages (styles, colors in `hlstats_Options` / `hlstats_Style`) | copied, but the gameWE web does not use them |
| Events after the export | gameME still collected them; see step 7 |
| Ping awards for old days | the export has no latency samples; `latency` awards start with the switch |
