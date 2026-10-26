# gameWE - collector: database

gameWE writes into an **HLstatsX database** (the gameME-style HLstatsX
variant) on **MariaDB / MySQL** or **PostgreSQL**. Choose with `database.type`
in the config (`mariadb`, the default, or `postgres`; see [CONFIG.md](CONFIG.md)).
The HLstatsX web pages and any other tool reading that database keep working.
The table structure, the reference data and the rights of the web users are
**compiled into the collector** (`internal/schema/` in the source:
`mysql/schema.sql`, `seed.sql`, `grants.sql` and the PostgreSQL files generated
from them with `make schema`, so both always have the same tables, columns,
defaults and reference data). The collector never reads SQL files from the
disk: at its start (`database.auto_migrate`, the default) or with `gamewe db
migrate` it creates the tables of a new database and brings an older one to
the current version ([Schema versions and
upgrades](#schema-versions-and-upgrades)). The only SQL you run by hand is
the init script (database, users, rights). Emptying, dropping and cleaning up
the database: [Maintenance](#maintenance-gamewe-db).

What the collector writes into which table and column is described in
[DB-SEMANTICS.md](DB-SEMANTICS.md).

Tested with every release: MariaDB 11.4 and 12.3, PostgreSQL 17 and 18. MySQL
8 is not supported (the schema and the rights use MariaDB syntax). How the
PostgreSQL schema differs (types, names, zero dates) is listed in
[DB-SEMANTICS.md](DB-SEMANTICS.md#postgresql).

## A new database (MariaDB / MySQL)

Two steps: the init script creates the database and its users (as a MySQL /
MariaDB admin), then the collector creates the tables, loads the reference
data and grants the web users their table rights (as the collector's user):
at its first start (`database.auto_migrate`), or before with `gamewe db init`.

```bash
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='...' WEB_PASSWORD='...' WEB_ADMIN_PASSWORD='...' ./init-db.sh -u root -p
# other name: DB_NAME=stats ./init-db.sh ...
# set database.name, user gamewe and its password in /etc/gamewe/collector/config.yaml, then:
sudo systemctl start gamewe-collector                      # creates the tables at the first start
# or first by hand: sudo -u gamewe /opt/gamewe/collector/bin/gamewe db init
```

`init-db.sh` runs `00_create_database.sql` with the passwords and the name
filled in; it stops without changing anything if the database already
contains tables, so it can never touch an existing (production) database. By
hand: replace the placeholders `CHANGE_ME_COLLECTOR` / `CHANGE_ME_WEB` /
`CHANGE_ME_WEB_ADMIN` (and the name `gamewe`) in `00_create_database.sql`
and run it with `mysql -u root -p < 00_create_database.sql`. Restrict the
hosts (`'%'`) to the collector's and web server's addresses, and enable
`REQUIRE SSL` (prepared as a comment) when the database is not on the same
host.

| User | Rights | For |
|------|--------|-----|
| `gamewe` | SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX on the database, WITH GRANT OPTION | the collector, and `gamewe db migrate` |
| `gamewe_web` | SELECT on all tables except `hlstats_Users`, `hlstats_Events_Rcon` and `hlstats_Events_Chat`; on `hlstats_Servers` not on `rcon_password`, `log_secret`, `log_secret_changed`, on `hlstats_Events_Connects` not on `ipAddress` | the public web pages |
| `gamewe_web_admin` | SELECT on all tables (including `rcon_password`, to manage servers); INSERT, UPDATE, DELETE on `hlstats_Options`, `_Games`, `_Servers`, `_Clans`, `_Weapons`, `_Actions`, `_Teams`, `_Roles`, `_Awards`, `_Definition_Ranks_Ribbons`, `_Users`; on `hlstats_Players` only UPDATE of `skill`, `hideranking`, `clan`; INSERT on `hlstats_gamewe_jobs` | the web admin pages |

The collector itself only reads and writes rows (`DELETE` is needed for
`hlstats_Livestats`, `hlstats_PlayersInactive`, the daily move of inactive
players from `hlstats_Players` to `hlstats_PlayersInactive`
(`features.inactive_players`, off by default) and a rare race when two
servers register the same new player). CREATE, ALTER and INDEX are for
the migration (at the start with `database.auto_migrate`, or `gamewe db
migrate`); it has no DROP right, so it cannot drop a table (`gamewe db drop`
is then run as a database admin with `-print`, see
[Maintenance](#maintenance-gamewe-db)). The
web users' rights are per table, and MariaDB only grants them on tables that
exist: `gamewe db migrate` grants them after it created or changed tables,
which needs `WITH GRANT OPTION` (it gives only the rights listed above). If
you do not want the collector user to hold it, leave it out: the migration
then warns, and a database admin grants the rights with
`gamewe db grants -print | mariadb -u root -p gamewe`.

The reference data: games `css`, `csgo`, `tf`, `dods`, `hl2mp`, `l4d`,
`l4d2`, `insurgency`, `zps`, `ff`, `csp`, `fof`, `nmrih` and the GoldSrc games `cstrike`, `czero`, `dod`, `tfc`, `valve`,
`ns`, with their teams (and roles where the game has them), actions with
rewards, weapons with modifiers, awards, ranks and ribbons (GAMES.md), and the
`gamewe_*` options. Change them in the web admin or with `gamewe game` /
`gamewe option`.

No admin login is created in `hlstats_Users`. Create one with the web
frontend: `npm run create-admin -- <name>` (it stores the password as
`scrypt$salt$hash`).

Then register the game servers in the web admin and run `gamewe check`.

## Schema versions and upgrades

The table `gamewe_schema_migrations` records the schema versions (version,
name, time, checksum, note) of a database. The collector checks the
database at every start against the schema compiled into it (a version is
missing, or a table, column or index of the current schema):

- **`database.auto_migrate: true`** (the default): it **migrates the
  database itself**, before it touches anything else, exactly as `gamewe db
  migrate -f` would (same plan, lock, rights check and steps; each step is a
  line `migrate: [n/m] ...` in the journal), then starts. Nothing to do after
  an update but restarting the collector. Make a backup before you install a
  new version: the start does not ask. If the migration fails (e.g. missing
  rights), the collector does not start and logs what to do; every step is
  idempotent, so the next start (or `gamewe db migrate`) goes on from there.
  Several collectors starting at once wait for each other (lock); a
  collector does not migrate while another one runs on the database (the
  start fails with "a collector is running on this database" and is
  retried): stop the other one for the update.
- **An HLstatsX database without schema versions** (taken over from an old
  HLstatsX) is never migrated automatically: that is done once by hand, with
  a backup ([MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md)); afterwards it has
  versions and later updates run automatically.
- **`database.auto_migrate: false`**: the collector **does not start**
  (exit code 78, the systemd unit fails and is not restarted) and names the command to run:

```
gamewe run: the database structure does not match the layout this collector needs (schema version 0, needs 4; 8 steps pending; database.auto_migrate is false). Make a backup, then run: /opt/gamewe/collector/bin/gamewe db migrate
```

By hand (also to see the plan before an update starts the collector):

```bash
gamewe db status        # schema version, pending and applied migrations
gamewe db migrate -n    # the plan only (exit code 0: up to date, 2: pending)
gamewe db migrate       # plan, backup advice, "Backup made? Migrate now? [y/N]"
gamewe db migrate -f    # no question (scripts); without a terminal -f is required
```

`gamewe db migrate` refuses (and changes nothing) while a collector runs on
the database: stop it first. A running collector holds the *collector lock*
on a connection of its own (MariaDB `GET_LOCK('gamewe_collector.<database>')`,
PostgreSQL a session advisory lock; the database releases it when the
process ends or the connection breaks), so this also works for a collector on
another host or in a container; a `gamewe run` process on this host (also up
to 0.7.1, without the lock) or the active systemd unit `gamewe-collector`
counts too. The refusal lists the commands to stop it, migrate and start it
again ([USAGE.md](USAGE.md)). Each step waits at most 30 seconds for a table another
session holds (`lock_wait_timeout`, PostgreSQL `lock_timeout`), Ctrl-C stops
the migration (the waiting statement too) and says which steps are done.

What it does:

- **New database** (no `hlstats_*` table): all tables, the reference data,
  the web users' rights; every version is recorded as part of the new schema.
- **Existing gameWE database without versions** (0.1 - 0.2.1) or an
  **HLstatsX database of the variant gameWE uses** (events with
  `timestamp_id`, as the database gameWE started from): recorded as baseline
  (version 1), then the later versions add what is missing. An HLstatsX:CE
  database (events with `eventTime`) is refused: convert it into a new
  database instead, `gamewe db convert` ([MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md)).
- Every version only **adds** tables, columns, indexes and missing reference
  rows (`INSERT IGNORE`: existing rows stay as they are). It never deletes
  rows, never drops a table or column and never changes a column type or
  character set (an old latin1 database stays latin1). The only indexes it
  removes are ones a newer index replaces, after the new one exists.
- On MariaDB all changes of one table are one `ALTER TABLE` (the table is
  rebuilt once). MyISAM locks the table meanwhile: stop the collector first.
  Steps on tables with 500,000 rows or more are marked in the plan; measured
  on MariaDB: about 3 s per million rows. On PostgreSQL each version runs in
  one transaction.
- Every step is idempotent: a migration that stopped half way (power cut,
  Ctrl-C) is simply run again. Two runs at the same time are serialized by a
  database lock (`GET_LOCK`, `pg_advisory_lock`); the second waits, then
  finds nothing to do.
- Before the first change it checks the rights of the database user by
  trying them (MariaDB: it creates and alters its version table;
  PostgreSQL: CREATE on the schema and the owner of the `hlstats_*` tables).
  A user with `ALL PRIVILEGES ON gamewe.*`, or on PostgreSQL the owner of the
  database and of its tables, passes without a message. MariaDB has no
  database owner: only the user's GRANTs count (`SHOW GRANTS;`). Missing
  rights stop it with the statement to run as a database admin, e.g.
  `GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX ON `gamewe`.* TO 'gamewe'@'%' WITH GRANT OPTION;`
  (databases created with the init script before this version have only
  SELECT, INSERT, UPDATE, DELETE).
- Afterwards it grants the web users their table rights again. That needs
  `WITH GRANT OPTION` on MariaDB (PostgreSQL: the table owner may grant); a
  user without it gets one warning and the step is skipped (the schema is up
  to date anyway). Grant them then as a database admin:
  `gamewe db grants -print | mariadb -u root -p gamewe`.

| Version | Name | What |
|---------|------|------|
| 1 | `baseline` | a new database: all tables and the reference data; an existing one is recorded as it is |
| 2 | `gamewe-0.2.1` | tables `hlstats_Events_Latency`, `hlstats_gamewe_jobs`; columns `hlstats_Servers.rcon_password`, `hlstats_Games.realgame`, `server_id` on the nine event tables, kill positions on `hlstats_Events_Frags`; indexes `server_id` on the event tables, `map`, `killer_time`, `victim_time` on Frags (they replace `killerId`, `victimId`), `date` and `award_id` on `hlstats_Awards_History`; missing reference data |
| 3 | `awards-indexes` | indexes for the awards page: `time_player` on `hlstats_Events_Latency` (timestamp_id, playerId, ping), `time_action` on `hlstats_Events_PlayerActions` (timestamp_id, actionId, playerId) |
| 4 | `log-secret` | columns `hlstats_Servers.log_secret` (varchar(64), `''` = none) and `log_secret_changed` (Unix time the secret was last changed) for `features.log_secret`; `gamewe_web` cannot read them |
| 5 | `server-info` | columns `hlstats_Servers.server_version` (varchar(64)), `server_os` (varchar(8): `linux`, `windows`, `mac`) and `server_vac` (varchar(8): `secure`, `insecure`), `''` = unknown, for `features.server_info`; `gamewe_web` may read them (the rights step of the migration grants them) |
| 6 | `act-bots` | column `hlstats_Servers.act_bots` (int unsigned, default 0): the bots among `act_players`, per server; `gamewe_web` may read it (the rights step of the migration grants it) |
| 7 | `cs2` | Counter-Strike 2: column `neck` (smallint unsigned, default 0) after `head` in `hlstats_Players_Weapons` and `hlstats_Players_Weapons_History_Months` (readable by `gamewe_web` like the whole table); the reference data of the game `cs2` (game, teams, actions, weapons, awards, ranks, ribbons; `INSERT IGNORE`, existing rows are kept) |
| 8 | `czero` | Counter-Strike: Condition Zero: the reference data of the game `czero`, a copy of the cstrike definitions (game, teams, actions, weapons, awards, ranks, ribbons, ribbon classes; `INSERT IGNORE`, existing rows are kept); no structure change |
| 11 | `hlstatsx-awards` | the HLstatsX:CE awards `mvp` (css, CE's `round_mvp`) and `teamkills` (every team game: not valve, hl2mp, fof) with one ribbon each (CE's `mostkills`, `suicide`, `headshot` are not added: `most_kills`, `most_suicides`, `most_headshots` cover them); rankings (games whose `realgame` is another game, e.g. css1 of css) get them from their template where they lack an award or ribbon of that code; existing rows are kept; no structure change |

The SQL files of older packages (`/opt/gamewe/collector/doc/schema/hlstatsx.sql`,
`init/01_schema.sql`, `02_seed.sql`, `03_grants.sql` and the PostgreSQL
ones, and the `upgrade-*.sql` scripts) are obsolete: the collector ignores
them, `gamewe check` lists the ones it finds, they can be deleted.

For developers: a new column, index or table goes into
`internal/schema/mysql/schema.sql` (then `make schema`), plus a new entry in
`internal/database/migrations.go` (`Sync: true`); a new table also needs its
grant decision in `grants.sql`. Released migrations are never changed.

## Maintenance (gamewe db)

All take `-config FILE`; those that change something show what they would
do first (`--dry-run` / `-n` only that), ask, and accept `--force` / `-f`
for scripts (older name `-yes`; without a terminal on stdin they stop and
change nothing unless `--force` is given). They only touch the
tables of the gameWE schema (`hlstats_*` of the compiled schema and
`gamewe_schema_migrations`); other tables in the database, e.g. left over
from an old HLstatsX, are listed and left alone.

| Command | What it does | Rights |
|---------|--------------|--------|
| `gamewe db init` | creates the tables, the reference data and the web users' rights of a **new, empty** database (what the first start does with `auto_migrate`); refuses a database that has tables | the collector user |
| `gamewe db purge -older-than 90d` | deletes the **events** (`hlstats_Events_*`: frags, connects, chat, actions, ...) older than that many days (at least 7, plain `90` works too). Players, their totals, rankings, awards, the history per day and the server statistics stay; the event lists of the web pages get shorter. Asks `[y/N]`; on MariaDB in chunks of 20,000 rows, so a table is locked only shortly and the collector can keep running | the collector user |
| `gamewe db optimize` | gives the space of deleted rows back to the disk and renews the table statistics (MariaDB `OPTIMIZE TABLE`, PostgreSQL `VACUUM ANALYZE`), e.g. after `purge`. MyISAM tables are locked while they are rebuilt: run it at a quiet time | the collector user |
| `gamewe db reset` | **empties every table** and loads the reference data again, the AUTO_INCREMENT / identity counters start at 1: the database is as new. Everything goes, also the game servers, the settings (`hlstats_Options`), the admin logins (`hlstats_Users`) and own games or changed definitions; the tables and schema versions stay. Asks to type the database name. For one ranking only: `gamewe reset GAME -mode all` | the collector user (PostgreSQL: owner of the tables, for `TRUNCATE`) |
| `gamewe db convert -from CE` | copies an **HLstatsX:CE** database into this gameWE database, which must be new (no tables) or have no data yet (no players, servers, clans, frags): creates the schema, converts every table (event times to UTC, names and codes to ids, character sets to utf8mb4) and compares the rows of both at the end. The CE database is only read (a read-only session). Shows the plan and asks `[y/N]`; `--dry-run`, `--force`, `-source-timezone`, `-mojibake`. MariaDB / MySQL only. [MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md) | the collector user; SELECT on the CE database |
| `gamewe db drop` | **drops all tables** of the gameWE schema. Afterwards `gamewe db init` (or the next start) creates them again. Asks to type the database name | DROP: on MariaDB a database admin, `gamewe db drop -print \| mariadb -u root -p gamewe` (`-print` only prints the `DROP TABLE IF EXISTS ...`); on PostgreSQL the collector (owner) |

`migrate`, `convert`, `reset` and `drop` refuse while a collector runs on the database
(it holds the collector lock, or a `gamewe run` process runs on this host):
stop it first; the message lists the commands.
Make a backup before them; they print an example `mariadb-dump` /
`pg_dump` command.

```bash
gamewe db purge -older-than 180d -dry-run    # rows per table, nothing changed
gamewe db purge -older-than 180d             # the same, then "Delete N rows? [y/N]"
gamewe db optimize
systemctl stop gamewe-collector
gamewe db reset                              # "Type the database name (gamewe) to reset it:"
systemctl start gamewe-collector
```

## Game servers and settings

The collector reads its game servers from `hlstats_Servers` and its settings
from `hlstats_Options` (keys `gamewe_*`) every minute. It never creates server
rows: register servers in the web admin with IP (`address`), game port (`port`),
`game` and `rcon_password`. The `game` of the row decides which
`hlstats_Players` rows the players get. See [CONFIG.md](CONFIG.md).

Existing gameME databases, and HLstatsX:CE databases (events with
`eventTime`), are not upgraded in place: create a new gameWE database and
copy the data into it (HLstatsX:CE: `gamewe db convert`), as described in
[MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md) and
[MIGRATION-GAMEME.md](MIGRATION-GAMEME.md).

The read-only web user `gamewe_web` cannot read `rcon_password`, `log_secret`
and `log_secret_changed` (column rights, `grants.sql`): `SELECT *` on
`hlstats_Servers` fails for it, so the public web pages must name the columns.
The collector and `gamewe_web_admin` can read (and the web admin write) them.

`log_secret` is the server's `sv_logsecret` (a number 1..2147483647, `''` =
packets not checked), set by hand with `gamewe server set -logsecret` or in
the web admin; the collector only reads it. `log_secret_changed` is the Unix
time it was last changed there (0 = unknown); it is informational only.

`server_version`, `server_os` and `server_vac` are what the game server says
about itself (`features.server_info`: `A2S_INFO`, RCON `status`), written by
the collector only; public information that `gamewe_web` may read.

`act_bots` is the number of bots among `act_players` on that server, written
with it by the collector (see DB-SEMANTICS.md). The web shows real players
as `act_players - act_bots`. It is per server, unlike the bots' rows in
`hlstats_Livestats`: a bot is one player per name and game, so servers of one
ranking with the same bot names share its live row.

## PostgreSQL

The same two steps: `schema/postgres/init/` (`/opt/gamewe/collector/doc/schema/postgres/init/`
in the package) has `00_create_database.sql` and `init-db.sh`. Run it as a
PostgreSQL superuser, then start the collector (it creates the tables) or
run `gamewe db init` as the collector:

```bash
cd /opt/gamewe/collector/doc/schema/postgres/init
COLLECTOR_PASSWORD='...' WEB_PASSWORD='...' WEB_ADMIN_PASSWORD='...' ./init-db.sh -U postgres -h db.example.org
# other name: DB_NAME=stats ./init-db.sh ...   (lower-case letters, digits, _)
# in a container: PSQL='docker exec -i postgres psql' ./init-db.sh -U postgres
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db init   # or: start the collector
```

`init-db.sh` creates the roles `gamewe`, `gamewe_web` and `gamewe_web_admin`
(or sets their passwords, if they exist), the database (UTF8, owned by
`postgres`), CONNECT for the three roles, USAGE on the schema `public` for
all and CREATE for `gamewe`. Passwords are passed to psql as variables, so
they may contain any character. Like the MariaDB script it stops without
changing anything if the database already has tables.

The migration then creates the tables as `gamewe`, which therefore owns
them (only the owner may alter a table) and grants the web roles their
rights:

| Role | Rights |
|------|--------|
| `gamewe` | owner of the tables; CREATE on the schema `public` |
| `gamewe_web` | SELECT on all tables except `hlstats_Users`, `hlstats_Events_Rcon` and `hlstats_Events_Chat`; on `hlstats_Servers` not on `rcon_password`, `log_secret`, `log_secret_changed`, on `hlstats_Events_Connects` not on `ipAddress` (column rights: name the columns, `SELECT *` fails) |
| `gamewe_web_admin` | SELECT on all tables and USAGE on all sequences; INSERT, UPDATE, DELETE on the same tables as on MariaDB; on `hlstats_Players` only UPDATE of `skill`, `hideranking`, `clan` |

A database created with the init scripts before this version belongs to
`postgres`: the migration (at the start or `gamewe db migrate`) stops and prints what a superuser runs once
(`GRANT USAGE, CREATE ON SCHEMA public TO gamewe;` and a `DO` block that
makes `gamewe` the owner of the `hlstats_*` tables).

The seed inserts no explicit ids, so the identity sequences start right after
the seeded rows; the integration test inserts a row into every table the web
admin manages as `gamewe_web_admin` to check this.

`hlstats_Livestats` is an `UNLOGGED` table (the MEMORY table of MariaDB): fast,
and emptied after a crash of the database server, like the MEMORY table after a
restart. Its rows are rebuilt as players connect.

TLS on the server, in `postgresql.conf` (and `hostssl` lines in `pg_hba.conf` to
refuse plaintext):

```
ssl = on
ssl_cert_file = '/etc/postgresql/server.crt'
ssl_key_file  = '/etc/postgresql/server.key'
ssl_ca_file   = '/etc/postgresql/ca.crt'     # for client certificates
```

## Character set: utf8mb4

gameWE reads and writes **utf8mb4** only (`utf8mb4_unicode_ci`). Names with
umlauts, Cyrillic, emoji and so on are stored exactly as the game logs them.
Every reader of the database (the web frontend, scripts) must connect with
`utf8mb4` as well.

PostgreSQL databases are created with `ENCODING UTF8`; the collector connects
with `client_encoding UTF8` and `timezone UTC`.

## No transactions

The MariaDB tables are MyISAM, so there are no transactions and no row locks. The
collector therefore changes every counter with a relative update
(`kills = kills + 1`), which is atomic per statement, so several servers can
write at the same time without losing counts. The integration test checks this
with 400 concurrent kills from two servers. One event can consist of several
statements. If the database fails in the middle, an event can be stored
partially. The error is logged. On PostgreSQL the collector works the same
way (every statement on its own), so both databases hold the same data.

## TLS on the server

For PostgreSQL see [PostgreSQL](#postgresql). For MariaDB / MySQL, in `my.cnf` / `mariadb.conf.d`:

```
[mysqld]
ssl_cert = /etc/mysql/server.crt
ssl_key  = /etc/mysql/server.key
ssl_ca   = /etc/mysql/ca.crt
require_secure_transport = ON      # optional: refuse all plaintext logins
```

## TLS in gameWE

Set the `database.tls` section. The default is `disable`: fine for a database
on the same host or a private network, use `verify-full` for anything else.
The modes work the same for both database types (on PostgreSQL they match
`sslmode`).

| mode | Encrypted | Certificate checked | Host name checked |
|------|-----------|---------------------|-------------------|
| `disable` (default) | no | – | – |
| `require` | yes | no | no |
| `verify-ca` | yes | against `ca_file` (or system CAs) | no |
| `verify-full` | yes | against `ca_file` (or system CAs) | yes: `host`, or `server_name` |

- `cert_file` / `key_file` send a client certificate (`REQUIRE X509`).
- Use `server_name` when you connect by IP but the certificate carries a DNS name.
- If TLS is configured and the server cannot do TLS, the connection fails. There
  is never a silent plaintext fallback.
- `gamewe check` shows the TLS state in one line: the TLS version and cipher, as
  reported by the server (`Ssl_version`, `Ssl_cipher` on MariaDB, `pg_stat_ssl`
  on PostgreSQL), and the configured mode, e.g.
  `tls: on, TLSv1.3 TLS_AES_256_GCM_SHA384 (mode verify-full)` or
  `tls: off (plaintext connection, mode disable)`.
