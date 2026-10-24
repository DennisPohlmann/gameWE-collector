# Installing gameWE - collector

Two ways to run the collector:

- [A. Container](#a-container-docker--podman) (Docker / Podman, optional database in the same Compose project)
- [B. Release archive](#b-release-archive-targz) (tar.gz with systemd)

Both need a database ([C](#c-database)) and game servers that send their logs
to the collector ([D](#d-game-servers)). [E](#e-update) and
[F](#f-uninstall) cover updates and removal, [G](#g-troubleshooting) the
usual problems.

All addresses and passwords below are placeholders: `203.0.113.5` is the
collector host, `198.51.100.7` a game server, `change-me` stands for your own
passwords.

| What | Default |
|------|---------|
| UDP port for game-server logs (Source, GoldSrc) | `41337` (`listen.port`) |
| Config file | `/etc/gamewe/collector/config.yaml` |
| State directory (GeoIP database) | `/var/lib/gamewe/collector` |
| Binary, docs, SQL | `/opt/gamewe/collector/bin/gamewe`, `/opt/gamewe/collector/doc/` |
| Service | `gamewe-collector` (systemd), user `gamewe` |

**Supported databases** (tested in CI with every release):

| Database | Versions | Notes |
|----------|----------|-------|
| MariaDB | 11.4 (LTS), 12.3 (LTS) | recommended; `schema/init/` |
| PostgreSQL | 17, 18 | `schema/postgres/init/` |
| MySQL 8.x | not tested | the collector speaks the MySQL protocol, but `03_grants.sql` uses MariaDB syntax (`BEGIN NOT ATOMIC`) and does not run on MySQL. Use MariaDB or PostgreSQL. |

**Folder layout** (release archive; the container has the same paths inside):

| Path | Owner | Mode | Content |
|------|-------|------|---------|
| `/etc/gamewe/collector/` | `root:gamewe` | `0750` | config directory |
| `/etc/gamewe/collector/config.yaml` | `root:gamewe` | `0640` | the config (database password) |
| `/etc/gamewe/collector/db-password` (optional) | `root:gamewe` | `0640` | the database password alone (`database.password_file`) |
| `/etc/gamewe/collector/config.example.yaml` | `root:root` | `0640` | the example of the release |
| `/opt/gamewe/collector/bin/gamewe` | `root:root` | `0755` | the binary |
| `/opt/gamewe/collector/doc/` | `root:root` | `0755` / files `0644` | docs, licenses, SQL |
| `/var/lib/gamewe/` | `gamewe:gamewe` | `0755` | state |
| `/var/lib/gamewe/collector/` | `gamewe:gamewe` | `0755` | GeoIP database and its monthly update (systemd `StateDirectory`) |
| `/etc/systemd/system/gamewe-collector.service` | `root:root` | `0644` | the unit |

## A. Container (Docker / Podman)

The image `ghcr.io/dennispohlmann/gamewe-collector` (tags of a release: the
version, e.g. `0.1.0`, `<major>.<minor>` and `latest`; `edge` is the latest
commit on `main`) contains the static binary, the docs
and SQL in `/opt/gamewe/collector/doc/` and the GeoIP database. It runs as the
non-root user `nonroot` (uid 65532). Pull it with `docker pull`, or
build it yourself: `make docker`.

**Use the host network.** The collector recognises a game server by the source
IP and port of its log packets. With published ports (`-p 41337:41337/udp`)
Docker's or Podman's port forwarder can replace that source address, and every
packet is rejected. `network_mode: host` always keeps it; the ports are then
the host's ports.

### With Compose

```bash
mkdir gamewe && cd gamewe
cp /path/to/collector/docker-compose.example.yml docker-compose.yml
mkdir config && cp /path/to/collector/config.example.yaml config/config.yaml
$EDITOR config/config.yaml        # database: type, host 127.0.0.1, user gamewe, password (C)
docker compose up -d && docker compose logs -f collector
```

| Volume / file | In the container | Content |
|---------------|------------------|---------|
| `./config/config.yaml` | `/etc/gamewe/collector/config.yaml` (read only) | listen address, database connection |
| named volume `gamewe-data` | `/var/lib/gamewe` | the GeoIP database and its monthly update; keep it, otherwise every new container starts with the database of the image |
| `./config/db-ca.pem` (optional) | `/etc/gamewe/collector/db-ca.pem` | CA for `tls.mode: verify-ca` / `verify-full` |

A database in the same Compose project is optional: the profiles `mariadb`
(MariaDB 12.3) and `postgres` (PostgreSQL 18) start one that listens on
`127.0.0.1` only and keeps its data in its own named volume (`mariadb` →
`/var/lib/mysql`, `postgres` → `/var/lib/postgresql`), separate from the
collector's `gamewe-data`. A database elsewhere works the same way; then
leave the profiles out.

`.env` next to `docker-compose.yml` (`chmod 600 .env`). Compose itself only
reads `DB_ROOT_PASSWORD`; the other three are for the init script below:

```bash
# .env - gameWE collector with Docker Compose (placeholders: set your own)
DB_ROOT_PASSWORD=change-me-root          # root of the MariaDB / postgres of the PostgreSQL container
COLLECTOR_PASSWORD=change-me-collector   # database user gamewe (also in config/config.yaml)
WEB_PASSWORD=change-me-web               # database user gamewe_web (web frontend, public pages)
WEB_ADMIN_PASSWORD=change-me-web-admin   # database user gamewe_web_admin (web frontend, admin pages)
```

Create the gameWE database once, from the directory of `docker-compose.yml`,
with the init scripts of the release archive (or the repository's `schema/`):

```bash
tar -xzf gamewe-collector-0.1.0-linux-amd64.tar.gz --strip-components=4 opt/gamewe/collector/doc/schema   # -> ./schema
set -a; . ./.env; set +a

# MariaDB 12.3
docker compose --profile mariadb up -d
(cd schema/init && MYSQL='docker compose -f ../../docker-compose.yml exec -T mariadb mariadb' \
  ./init-db.sh -uroot -p"$DB_ROOT_PASSWORD")

# or PostgreSQL 18
docker compose --profile postgres up -d
(cd schema/postgres/init && PSQL='docker compose -f ../../../docker-compose.yml exec -T postgres psql' \
  ./init-db.sh -U postgres)

docker compose up -d collector
docker compose run --rm collector check     # config, database, schema, servers
```

In `config/config.yaml` then: `database.type` (`mariadb` or `postgres`),
`host: 127.0.0.1`, `user: gamewe`, `password:` the `COLLECTOR_PASSWORD`
(the collector reads no environment variables; a password file can be mounted
read-only and named in `password_file`).

PostgreSQL 18 keeps its data in `/var/lib/postgresql/18/docker`, so the example
mounts its volume at `/var/lib/postgresql`. An existing `postgres:17` volume
(mounted at `/var/lib/postgresql/data`) is not converted: keep `postgres:17`
and that mount, or move the data with `pg_dumpall` and a restore.

### With docker run

```bash
docker run -d --name gamewe-collector --restart unless-stopped --network host \
  -v /etc/gamewe/collector/config.yaml:/etc/gamewe/collector/config.yaml:ro \
  -v gamewe-data:/var/lib/gamewe \
  --log-opt max-size=50m --log-opt max-file=5 \
  ghcr.io/dennispohlmann/gamewe-collector:latest
```

Every received log line goes to the container log (`gamewe_log_lines`), hence
the log limits. Other commands run the same way, e.g.
`docker run --rm --network host -v ...config.yaml:ro ghcr.io/dennispohlmann/gamewe-collector:latest check`.

Open the port on the host's firewall for your game servers only: UDP 41337,
for example:

```bash
ufw allow from 198.51.100.7 to any port 41337 proto udp            # ufw
firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=198.51.100.7 port port=41337 protocol=udp accept' \
  && firewall-cmd --reload                                          # firewalld
```

## B. Release archive (tar.gz)

The archive `gamewe-collector-<version>-linux-amd64.tar.gz` (Linux x86_64,
static binary) is a tree for `/`:

```
etc/gamewe/collector/config.example.yaml        the config to copy to config.yaml (none is shipped: copying never overwrites yours)
etc/systemd/system/gamewe-collector.service     systemd unit (hardened, user gamewe, StateDirectory=gamewe/collector)
opt/gamewe/collector/bin/gamewe                 the binary
opt/gamewe/collector/doc/                       README, CHANGELOG, INSTALLATION, CONFIG, USAGE, INTERFACE, DATABASE,
                                                DB-SEMANTICS, GAMES, DEVELOPMENT,
                                                LICENSE, THIRD-PARTY-LICENSES.md, licenses/, GEOIP-NOTICE
opt/gamewe/collector/doc/schema/                MariaDB: hlstatsx.sql, init/ (00_create_database, 01_schema, 02_seed, 03_grants, init-db.sh)
opt/gamewe/collector/doc/schema/postgres/       PostgreSQL: hlstatsx.sql, init/ (the same files)
var/lib/gamewe/collector/dbip-city-lite.mmdb    GeoIP database (DB-IP City Lite, CC BY 4.0), updated there monthly
```

Check it and install it as root:

```bash
sha256sum -c gamewe-collector-0.1.0-linux-amd64.tar.gz.sha256
mkdir gamewe && tar -xzf gamewe-collector-0.1.0-linux-amd64.tar.gz -C gamewe && cd gamewe
cp -r etc opt /
cp -rn var /                                   # -n: never replace an updated GeoIP database
useradd --system --no-create-home --shell /usr/sbin/nologin gamewe
cp -n /etc/gamewe/collector/config.example.yaml /etc/gamewe/collector/config.yaml
chown root:gamewe /etc/gamewe/collector /etc/gamewe/collector/config.yaml
chmod 750 /etc/gamewe/collector && chmod 640 /etc/gamewe/collector/config.yaml
chown -R gamewe:gamewe /var/lib/gamewe
/opt/gamewe/collector/bin/gamewe -V            # gamewe 0.1.0
```

Step by step, the commands above do:

1. copy the tree (`etc`, `opt`; `var` without replacing an existing GeoIP file),
2. create the system user `gamewe` (no home, no login shell),
3. create `config.yaml` from the example,
4. set owners and modes as in the folder layout table at the top.

Then edit `/etc/gamewe/collector/config.yaml`. All keys, with their defaults
([CONFIG.md](CONFIG.md) explains each one):

```yaml
# /etc/gamewe/collector/config.yaml  (root:gamewe 0640)
listen:
  address: 0.0.0.0              # IP for the log packets (0.0.0.0 = all IPv4, :: = all)
  port: 41337                   # UDP port (Source, GoldSrc)
  # public_address: 203.0.113.5:41337   # address the servers send to, if not the collector's RCON address (NAT)

database:
  type: mariadb                 # mariadb (= mysql) or postgres
  host: 127.0.0.1
  # port: 3306                  # default 3306 (mariadb), 5432 (postgres)
  user: gamewe
  password_file: /etc/gamewe/collector/db-password   # or: password: "change-me"
  name: gamewe
  max_open_conns: 8
  connect_timeout: 10           # seconds
  tls:
    mode: disable               # disable | require | verify-ca | verify-full (another host: verify-full)
    # ca_file: /etc/gamewe/collector/db-ca.pem
    # cert_file: /etc/gamewe/collector/client.pem
    # key_file: /etc/gamewe/collector/client.key
    # server_name: db.internal

geoip:
  path: /var/lib/gamewe/collector/dbip-city-lite.mmdb
  auto_update: true             # monthly DB-IP update into path

features:
  rcon_status:
    enabled: true               # RCON status of every server
    interval: 10s
  log_address: true             # re-register the collector (logaddress_add, log on) when missing
  daily_awards:
    enabled: true
    timezone: UTC               # IANA zone, e.g. Europe/Berlin
    time: "00:00"
```

Everything else (game servers, points, commands, bots, ...) is in the database
(`hlstats_Servers`, `hlstats_Options`), changed in the web admin or with
`gamewe server` / `gamewe option`.

A password file keeps the password out of the YAML:

```bash
install -m 640 -o root -g gamewe /dev/null /etc/gamewe/collector/db-password
$EDITOR /etc/gamewe/collector/db-password      # the password only, one line
```

Create the database (C), register the game servers (D), then check and start:

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check   # config, database, TLS, schema, servers, settings
systemctl daemon-reload
systemctl enable --now gamewe-collector
journalctl -u gamewe-collector -f
```

The unit is in `/etc/systemd/system/` (units installed by hand); change it with
`systemctl edit gamewe-collector`, not in place.

## C. Database

MariaDB / MySQL (tested: MariaDB 11.4 and 12.3) or PostgreSQL (tested: 17 and
18). Three database users:

| User | For | Rights |
|------|-----|--------|
| `gamewe` | the collector (`config.yaml`) | read and write rows, never the structure |
| `gamewe_web` | the public web pages | SELECT, without users, RCON events, chat, `hlstats_Servers.rcon_password`, `hlstats_Events_Connects.ipAddress` |
| `gamewe_web_admin` | the web admin pages | SELECT on all; writes settings, games, servers, definitions, admin users; queues ranking resets |

### A new database

The init script creates the database, the three users with their passwords,
the tables, the reference data (games, teams, roles, actions, weapons,
awards, ranks, settings) and the rights. It stops without changing anything if
the database already has tables.

```bash
# MariaDB / MySQL, as a database admin
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -u root -p

# PostgreSQL, as a superuser
cd /opt/gamewe/collector/doc/schema/postgres/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -U postgres
```

What the script runs, in order (by hand: replace the `CHANGE_ME_*`
placeholders in `00_create_database.sql` first):

```bash
# MariaDB / MySQL protocol (in /opt/gamewe/collector/doc/schema/init/)
mysql -u root -p < 00_create_database.sql      # CREATE DATABASE gamewe CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci,
                                               # users gamewe, gamewe_web, gamewe_web_admin
mysql -u root -p gamewe < 01_schema.sql        # tables (and the reference data)
mysql -u root -p gamewe < 02_seed.sql          # reference data (only inserts what is missing)
mysql -u root -p gamewe < 03_grants.sql        # table and column rights of gamewe_web and gamewe_web_admin

# PostgreSQL (in /opt/gamewe/collector/doc/schema/postgres/init/)
psql -U postgres -v db=gamewe -v collector_pw=change-me -v web_pw=change-me -v admin_pw=change-me \
     -f 00_create_database.sql                 # roles and CREATE DATABASE gamewe ENCODING 'UTF8' TEMPLATE template0
psql -U postgres -d gamewe -v ON_ERROR_STOP=1 -f 01_schema.sql
psql -U postgres -d gamewe -v ON_ERROR_STOP=1 -f 02_seed.sql
psql -U postgres -d gamewe -v ON_ERROR_STOP=1 -f 03_grants.sql
```

The rights (`03_grants.sql`; the collector user gets
`SELECT, INSERT, UPDATE, DELETE` on all tables in `00_create_database.sql`):

| User | Tables |
|------|--------|
| `gamewe` | SELECT, INSERT, UPDATE, DELETE on all tables (PostgreSQL: also the sequences) |
| `gamewe_web` | SELECT on every table except `hlstats_Users`, `hlstats_Events_Rcon`, `hlstats_Events_Chat`; on `hlstats_Servers` without `rcon_password`, on `hlstats_Events_Connects` without `ipAddress` (column rights) |
| `gamewe_web_admin` | SELECT on all tables (PostgreSQL: and the sequences); INSERT, UPDATE, DELETE on `hlstats_Options`, `_Games`, `_Servers`, `_Clans`, `_Weapons`, `_Actions`, `_Teams`, `_Roles`, `_Awards`, `_Definition_Ranks_Ribbons`, `_Users`; UPDATE of `skill`, `hideranking`, `clan` on `hlstats_Players`; INSERT on `hlstats_gamewe_jobs` |

`DB_NAME=name` chooses another database name. On MariaDB the users are created
for the host `'%'`: restrict them to the collector's and the web server's
addresses, and require TLS (`REQUIRE SSL`) when the database is on another host
([DATABASE.md](DATABASE.md), also for doing the four steps by hand).
No web admin login is created; the web frontend does that
(`npm run create-admin -- <name>`).

### New reference data

There are no migrations: the scripts above always create the complete current
schema. `gamewe check` shows what an existing database lacks
(`schema incomplete: missing ...`); the collector runs on with fallbacks, but
without those parts (DATABASE.md, "Incomplete schemas"). New reference data
of a later release (games, awards, ...) is added with its `02_seed.sql`:

```bash
mysqldump --single-transaction gamewe > gamewe-backup.sql     # PostgreSQL: pg_dump -Fc gamewe > gamewe.dump
mysql -u root -p gamewe < /opt/gamewe/collector/doc/schema/init/02_seed.sql   # only adds missing rows
# PostgreSQL: psql -U postgres -d gamewe -v ON_ERROR_STOP=1 -f /opt/gamewe/collector/doc/schema/postgres/init/02_seed.sql
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check         # schema: ok
```

`02_seed.sql` only inserts rows that are missing (games, teams, roles,
actions, weapons, awards, ranks, ribbons, settings); it never changes
existing rows or rankings of your own (such as `css1`).

### GeoIP

Nothing to do: the DB-IP City Lite database comes with the archive and the
image (`/var/lib/gamewe/collector/dbip-city-lite.mmdb`) and the
collector replaces it there monthly (`geoip.auto_update`, through
`HTTPS_PROXY` if set). Pages that show locations must credit DB-IP ("IP
Geolocation by DB-IP", https://db-ip.com). Another database (MaxMind
GeoLite2): `geoip.path` and `auto_update: false`
([CONFIG.md](CONFIG.md#geoip-database-player-and-server-locations)).

## D. Game servers

Every game server is a row of `hlstats_Servers`: its IP and game port (where
the log packets come from), its game (an `hlstats_Games` code) and its RCON
password. Add it in the web admin or with the CLI; the collector picks it up
within a minute:

```bash
gamewe game list                                         # the game codes
printf '%s\n' 'change-me' > /root/rcon.txt               # the server's rcon_password
gamewe server add 198.51.100.7:27015 -game css -name "My CS:S server" -rcon-password-file /root/rcon.txt
gamewe server list
```

`-rcon-password-file -` reads it from stdin; `-rcon-password` works too but is
visible in the process list. The RCON password is never printed.

**Source games (CS:S, CS:GO, TF2, DoD:S, HL2:DM, L4D, ...)**, in `server.cfg`:

```
log on
mp_logdetail 3
logaddress_add 203.0.113.5:41337     // CS:GO: logaddress_add "203.0.113.5:41337"
rcon_password "change-me"            // the same as in hlstats_Servers
```

With the RCON password the collector also registers itself (`logaddress_add`,
`log on`) when a server restarted without it, every minute. Install the
[gameWE SourceMod plugin](https://github.com/DennisPohlmann/gameWE-plugin-sourcemod)
for the in-game replies (`!rank`, `!top10`, menus) and the extra statistics
(weapon accuracy, positions, roles).

**GoldSrc (CS 1.6, DoD, TFC, HL1, NS)**: `log on`,
`logaddress_add 203.0.113.5 41337` and `rcon_password`; in-game replies through
the gameWE AMX Mod X plugin ([GAMES.md](GAMES.md)).

Counter-Strike 2 is not supported (no UDP logs, no SourceMod).

Restrict the collector's port (UDP 41337) with a firewall to your
game servers: packets from addresses that are not in `hlstats_Servers` are
rejected anyway (`rejected log packet` in the journal). Test the whole chain
from the collector host with the sample game:

```bash
/opt/gamewe/collector/bin/gamewe simulate -send 127.0.0.1:41337
```

## E. Update

Read the release's entry in [CHANGELOG.md](../CHANGELOG.md) first. New reference
data of a release (games, awards, ...) comes with `init/02_seed.sql`: run it
after the update (C, "New reference data"); `gamewe check` says if the schema
itself lacks something.

```bash
# archive
mkdir gamewe && tar -xzf gamewe-collector-<version>-linux-amd64.tar.gz -C gamewe && cd gamewe
cp -r etc opt / && cp -rn var / && chown -R gamewe:gamewe /var/lib/gamewe
systemctl daemon-reload && systemctl restart gamewe-collector
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check

# container
docker compose pull && docker compose up -d        # or docker pull + docker run again
```

`config.yaml` is never in the archive, so it is kept.

## F. Uninstall

```bash
systemctl disable --now gamewe-collector
rm -r /opt/gamewe/collector /etc/systemd/system/gamewe-collector.service
systemctl daemon-reload
rm -r /etc/gamewe/collector /var/lib/gamewe     # config (with the database password) and GeoIP data
userdel gamewe
```

Container: `docker compose down` (add `-v` to delete the volumes, also a
database volume of the example). The statistics stay in the database until you
drop it (`DROP DATABASE gamewe;` and the three users).

## G. Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `gamewe check` fails with `certificate signed by unknown authority` | Set `database.tls.ca_file` to the CA that signed the database certificate. |
| `... certificate is valid for X, not Y` | Connect by the name in the certificate, set `tls.server_name`, or use `verify-ca`. |
| `server refused TLS connection` / `server does not support TLS` | TLS is not enabled on the database server, see [DATABASE.md](DATABASE.md). gameWE never falls back to plaintext. |
| `table hlstats_...: ...` | Wrong database (`database.name`), or the DB user lacks rights on it. |
| `config key "servers" is no longer used` | An old-style config: keep only `listen:`, `database:`, `geoip:` and `features:`, register the servers in the web admin. |
| `hlstats_Servers serverId N is ignored: ...` | The row's address is not an IP, the port is invalid, or its game is not in `hlstats_Games`. Fix it in the web admin. |
| No `game server active` in the log | Check `logaddress_add` and the firewall (UDP port). |
| `rejected log packet ... not in hlstats_Servers` | Register the shown source `IP:port` in the web admin. |
| Commands get no answer | `rcon_password` is empty or wrong (`rcon authentication failed`), the plugin is missing, or the RCON port is blocked (TCP). `gamewe check` shows servers without a password. |
| Service does not start, `status=31/SYS` | A hardening option in the unit blocks something on your system. See `journalctl -u gamewe-collector` and relax `SystemCallFilter` with `systemctl edit gamewe-collector`. |

**What you see in `journalctl -u gamewe-collector -f`:** every log line a game
server sends, as `[name ip:port] L 10/22/2026 - 12:00:00: "Player<2><STEAM_1:0:123><CT>" killed …`.
If those lines appear, data arrives and is accepted. Control characters in
player names are shown as `\xNN`. Besides those:

- `game servers loaded from hlstats_Servers`: after start and when servers change
- `game server active`: the first accepted line of a server
- `rejected log packet … source=IP:port`: a sender that is not in `hlstats_Servers`
  (first time, then every 1000th)
- `processing log line failed`: a database error

Every line in the journal can be a lot: a busy server sends several lines per
second. Set `gamewe_log_lines` to `0` in `hlstats_Options` to switch it off (within
a minute, no restart), and limit the journal size, e.g. `SystemMaxUse=1G` in
`/etc/systemd/journald.conf`.

Use `systemctl edit gamewe-collector` to add `-log-level debug` to `ExecStart` for
details about every reply. Test against your database with `gamewe check` and
`gamewe simulate`. `simulate` without `-send` writes the sample game into the
database (`-game css` by default): test players `STEAM_1:x:900000x` and server
`127.0.0.1:27015`, which it adds to `hlstats_Servers` itself. It prints the RCON
commands it would send.
