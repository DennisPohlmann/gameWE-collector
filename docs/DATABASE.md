# gameWE - collector: database

gameWE writes into an **HLstatsX database** (the gameME-style HLstatsX
variant) on **MariaDB / MySQL** or **PostgreSQL**. Choose with `database.type`
in the config (`mariadb`, the default, or `postgres`; see [CONFIG.md](CONFIG.md)).
The HLstatsX web pages and any other tool reading that database keep working.
The table structure is in `schema/hlstatsx.sql` (MariaDB / MySQL) and
`schema/postgres/hlstatsx.sql` (PostgreSQL), `/opt/gamewe/collector/doc/schema/`
in the package. The MariaDB file was taken from the production database; the
PostgreSQL files are generated from the MariaDB ones (`make schema`), so both
always have the same tables, columns, defaults and reference data.

What the collector writes into which table and column is described in
[DB-SEMANTICS.md](DB-SEMANTICS.md).

Tested with every release: MariaDB 11.4 and 12.3, PostgreSQL 17 and 18. MySQL
8 is not supported by the scripts (they use MariaDB syntax). A gameWE database
is always created new from `schema/init/` (MariaDB) or `schema/postgres/init/`
(PostgreSQL); there are no migrations (see
[Incomplete schemas](#incomplete-schemas)). How the
PostgreSQL schema differs (types, names, zero dates) is listed in
[DB-SEMANTICS.md](DB-SEMANTICS.md#postgresql).

## Database user (MariaDB / MySQL)

The collector needs to read and write rows, but never changes the structure
(`00_create_database.sql` creates it so; by hand):

```sql
CREATE USER 'gamewe'@'203.0.113.5' IDENTIFIED BY 'change-me' REQUIRE SSL;
GRANT SELECT, INSERT, UPDATE, DELETE ON gamewe.* TO 'gamewe'@'203.0.113.5';
```

`DELETE` is needed for `hlstats_Livestats` (players who left),
`hlstats_PlayersInactive` (a returning player is moved back) and a rare race when
two servers register the same new player at the same moment.

### A new, empty database

**`schema/hlstatsx.sql` alone creates a working database**: the tables plus the
reference data (games, teams, actions with rewards, weapons with modifiers,
`gamewe_*` options). It is all you need if you create the database and its users
yourself:

```bash
mysql -u root -p -e "CREATE DATABASE gamewe CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
mysql -u root -p gamewe < /opt/gamewe/collector/doc/schema/hlstatsx.sql
```

Games included: `css`, `csgo`, `tf`, `dods`, `hl2mp`, `l4d`, `l4d2`,
`insurgency` and the GoldSrc games `cstrike`, `dod`, `tfc`, `valve`, `ns`, with
their teams (and roles where the game has them), actions with rewards, weapons
with modifiers, awards, ranks
and ribbons (GAMES.md).

`schema/init/` (`/opt/gamewe/collector/doc/schema/init/` in the package) also
creates the database and its users:

| File | Content |
|------|---------|
| `00_create_database.sql` | database `gamewe` (utf8mb4) and three users (see below). Passwords are the placeholders `CHANGE_ME_COLLECTOR` / `CHANGE_ME_WEB` / `CHANGE_ME_WEB_ADMIN`, `REQUIRE SSL` is prepared as a comment. |
| `01_schema.sql` | identical to `schema/hlstatsx.sql`: tables and reference data (in the source tree the script uses that file directly) |
| `02_seed.sql` | the reference data alone (the same rows as at the end of `hlstatsx.sql`; `make schema` copies it there after you edit it). Only `INSERT IGNORE`: it can run on any database, any number of times. It adds missing rows and never changes existing ones. |
| `03_grants.sql` | the table and column rights of `gamewe_web` and `gamewe_web_admin` (they need the tables, so this runs last) |

### Adding missing reference data to an existing database

If `gamewe check` or the log says `game "css" is not in hlstats_Games`, or
actions or options are missing, run the seed. It only adds what is missing:

```bash
mysql -u root -p gamewe < /opt/gamewe/collector/doc/schema/init/02_seed.sql
```

The collector picks up the result within a minute, without a restart.

Run as a MySQL / MariaDB admin. `init-db.sh` replaces the placeholders and
runs the four files in order:

```bash
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='...' WEB_PASSWORD='...' WEB_ADMIN_PASSWORD='...' ./init-db.sh -u root -p
# other name: DB_NAME=stats ./init-db.sh ...
```

`init-db.sh` stops without changing anything if the database already contains
tables, so it can never touch an existing (production) database.

| User | Rights | For |
|------|--------|-----|
| `gamewe` | SELECT, INSERT, UPDATE, DELETE on all tables | the collector |
| `gamewe_web` | SELECT on all tables except `hlstats_Users` and `hlstats_Events_Rcon`; on `hlstats_Servers` not on `rcon_password`, on `hlstats_Events_Connects` not on `ipAddress` | the public web pages |
| `gamewe_web_admin` | SELECT on all tables (including `rcon_password`, to manage servers); INSERT, UPDATE, DELETE on `hlstats_Options`, `_Games`, `_Servers`, `_Clans`, `_Weapons`, `_Actions`, `_Teams`, `_Roles`, `_Awards`, `_Definition_Ranks_Ribbons`, `_Users`; on `hlstats_Players` only UPDATE of `skill`, `hideranking`, `clan` | the web admin pages |

No admin login is created in `hlstats_Users`. Create one with the web
frontend: `npm run create-admin -- <name>` (it stores the password as
`scrypt$salt$hash`).

Or by hand, after editing the passwords in `00_create_database.sql`:

```bash
mysql -u root -p < 00_create_database.sql
mysql -u root -p gamewe < 01_schema.sql
mysql -u root -p gamewe < 02_seed.sql
mysql -u root -p gamewe < 03_grants.sql
```

Then set `database.name: gamewe`, `user: gamewe` and the password in
`/etc/gamewe/collector/config.yaml`, register the game servers in the web admin
and run `gamewe check`. Restrict the hosts (`'%'`) to
the collector's and web server's addresses, and enable `REQUIRE SSL` when the
database is not on the same host.

## Game servers and settings

The collector reads its game servers from `hlstats_Servers` and its settings
from `hlstats_Options` (keys `gamewe_*`) every minute. It never creates server
rows: register servers in the web admin with IP (`address`), game port (`port`),
`game` and `rcon_password`. The `game` of the row decides which
`hlstats_Players` rows the players get. See [CONFIG.md](CONFIG.md).

### Incomplete schemas

gameWE has no migrations: `schema/init/` (and `schema/postgres/init/`) creates
the complete current schema, reference data and rights in one go.
`gamewe check` (and the collector at start) says what an existing database
lacks, e.g. `schema incomplete: missing hlstats_Games.realgame (...)`; the
collector keeps running with fallbacks, but without what is missing. Recreate
the database with `init-db.sh` (the statistics start from zero), or add what
is missing by hand and then run the current `init/02_seed.sql` (it only adds
missing reference data).

A database created by a development version before 0.1.0 may lack the column
`server_id` of the event tables (Frags, Teamkills, Suicides, PlayerActions,
PlayerPlayerActions, TeamBonuses, Entries, Disconnects, ChangeTeam); the
collector then stores the events without it. To add it by hand (MariaDB, as
root; the table is rebuilt):
`ALTER TABLE hlstats_Events_Frags ADD COLUMN server_id smallint(5) unsigned NOT NULL DEFAULT 0 AFTER game_id, ADD KEY server_id (server_id, timestamp_id);`
and the same for the other eight tables.

Existing HLstatsX or HLstatsX:CE databases (often latin1) are not converted;
create a new gameWE database.

The read-only web user `gamewe_web` cannot read `rcon_password` (column rights,
`03_grants.sql`): `SELECT *` on `hlstats_Servers` fails for it,
so the public web pages must name the columns. Because its rights are per
table, a table added to the database later needs its own `GRANT SELECT` for
`gamewe_web`. The collector and `gamewe_web_admin` can read the passwords.

## PostgreSQL

`schema/postgres/` (`/opt/gamewe/collector/doc/schema/postgres/` in the
package) has the same files as the MariaDB variant: `hlstatsx.sql` (tables and
reference data), and in `init/` the files `00_create_database.sql` (roles and
database, UTF8), `01_schema.sql`, `02_seed.sql` and `03_grants.sql`, plus
`init-db.sh`. Run it as a PostgreSQL superuser:

```bash
cd /opt/gamewe/collector/doc/schema/postgres/init
COLLECTOR_PASSWORD='...' WEB_PASSWORD='...' WEB_ADMIN_PASSWORD='...' ./init-db.sh -U postgres -h db.example.org
# other name: DB_NAME=stats ./init-db.sh ...   (lower-case letters, digits, _)
# in a container: PSQL='docker exec -i postgres psql' ./init-db.sh -U postgres
```

It creates the roles `gamewe`, `gamewe_web` and `gamewe_web_admin` (or sets
their passwords, if they exist), the database owned by `postgres`, the tables in
its `public` schema, the reference data and the rights. Passwords are passed to
psql as variables, so they may contain any character. Like the MariaDB script it
stops without changing anything if the database already has tables.

| Role | Rights |
|------|--------|
| `gamewe` | SELECT, INSERT, UPDATE, DELETE on all tables, USAGE on all sequences (identity columns) |
| `gamewe_web` | SELECT on all tables except `hlstats_Users` and `hlstats_Events_Rcon`; on `hlstats_Servers` not on `rcon_password`, on `hlstats_Events_Connects` not on `ipAddress` (column rights: name the columns, `SELECT *` fails) |
| `gamewe_web_admin` | SELECT on all tables and USAGE on all sequences; INSERT, UPDATE, DELETE on the same tables as on MariaDB; on `hlstats_Players` only UPDATE of `skill`, `hideranking`, `clan` |

`hlstatsx.sql` alone (in a database you created) also works, as on MariaDB:
`psql -d gamewe -f hlstatsx.sql`. Then grant the rights with `03_grants.sql`.
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
- `gamewe check` shows the TLS version and cipher, as reported by the server
  (`Ssl_version`, `Ssl_cipher` on MariaDB, `pg_stat_ssl` on PostgreSQL).
