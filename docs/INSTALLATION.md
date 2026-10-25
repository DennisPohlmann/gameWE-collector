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
| Binary, docs, init scripts | `/opt/gamewe/collector/bin/gamewe`, `/opt/gamewe/collector/doc/` |
| Service | `gamewe-collector` (systemd), user `gamewe` |

**Supported databases** (tested in CI with every release):

| Database | Versions | Notes |
|----------|----------|-------|
| MariaDB | 11.4 (LTS), 12.3 (LTS) | recommended; `schema/init/` |
| PostgreSQL | 17, 18 | `schema/postgres/init/` |
| MySQL 8.x | not supported | the collector speaks the MySQL protocol, but the schema (`USING HASH`) and the web users' rights (`BEGIN NOT ATOMIC`) use MariaDB syntax. Use MariaDB or PostgreSQL. |

**Folder layout** (release archive; the container has the same paths inside):

| Path | Owner | Mode | Content |
|------|-------|------|---------|
| `/etc/gamewe/collector/` | `root:gamewe` | `0750` | config directory |
| `/etc/gamewe/collector/config.yaml` | `root:gamewe` | `0640` | the config (database password) |
| `/etc/gamewe/collector/db-password` (optional) | `root:gamewe` | `0640` | the database password alone (`database.password_file`) |
| `/etc/gamewe/collector/config.example.yaml` | `root:root` | `0640` | the example of the release |
| `/opt/gamewe/collector/bin/gamewe` | `root:root` | `0755` | the binary |
| `/opt/gamewe/collector/doc/` | `root:root` | `0755` / files `0644` | docs, licenses, init scripts |
| `/var/lib/gamewe/` | `gamewe:gamewe` | `0755` | state |
| `/var/lib/gamewe/collector/` | `gamewe:gamewe` | `0755` | GeoIP database and its monthly update (systemd `StateDirectory`) |
| `/etc/systemd/system/gamewe-collector.service` | `root:root` | `0644` | the unit |

## A. Container (Docker / Podman)

The image `ghcr.io/dennispohlmann/gamewe-collector` (tags of a release: the
version, e.g. `0.1.0`, `<major>.<minor>` and `latest`; `edge` is the latest
commit on `main`) contains the static binary (with the database schema built
in), the docs and init scripts in `/opt/gamewe/collector/doc/` and the GeoIP
database. It runs as the
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

docker compose up -d collector               # creates the tables at the first start (database.auto_migrate)
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
opt/gamewe/collector/doc/schema/init/           MariaDB: 00_create_database.sql, init-db.sh (database and users; the tables are built into gamewe)
opt/gamewe/collector/doc/schema/postgres/init/  PostgreSQL: the same two files
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
  log_address:
    enabled: true               # re-register the collector (logaddress_add, log on) when missing
    interval: 1m
  server_info:
    enabled: true               # version, OS, VAC of every server (A2S_INFO, no RCON needed)
    interval: 5m
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

The unit is `Type=notify`: `systemctl start` and `restart` wait until the
collector has read its config, connected to the database (and migrated it),
loaded the game servers and opened the UDP port. If any of that fails, the
command itself fails (`Job for gamewe-collector.service failed ...`) and the
reason is in `journalctl -u gamewe-collector`. A config error (exit code
78) leaves the unit `failed` until you fix it and start it again; a database
that is not reachable yet is retried every 5 seconds.

## C. Database

MariaDB (tested: 11.4 and 12.3) or PostgreSQL (tested: 17 and 18). Three
database users:

| User | For | Rights |
|------|-----|--------|
| `gamewe` | the collector (`config.yaml`), its migration at start and `gamewe db` | read and write rows; create and alter tables (never drop); grant the web users their table rights |
| `gamewe_web` | the public web pages | SELECT, without users, RCON events, chat, `hlstats_Servers.rcon_password` and `log_secret`, `hlstats_Events_Connects.ipAddress` |
| `gamewe_web_admin` | the web admin pages | SELECT on all; writes settings, games, servers, definitions, admin users; queues ranking resets |

### A new database

Two steps. The init script creates the database and the three users with
their passwords (it stops without changing anything if the database already
has tables); the collector then creates the tables, the reference data
(games, teams, roles, actions, weapons, awards, ranks, settings) and the web
users' rights at its first start (`database.auto_migrate`, the default), or
before with `gamewe db init`.

```bash
# MariaDB, as a database admin
cd /opt/gamewe/collector/doc/schema/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -u root -p

# PostgreSQL, as a superuser
cd /opt/gamewe/collector/doc/schema/postgres/init
COLLECTOR_PASSWORD='change-me' WEB_PASSWORD='change-me' WEB_ADMIN_PASSWORD='change-me' ./init-db.sh -U postgres

# then, with database.* set in /etc/gamewe/collector/config.yaml:
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db init         # the tables (or let the first start do it)
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check
systemctl enable --now gamewe-collector
```

`DB_NAME=name` chooses another database name. On MariaDB the users are created
for the host `'%'`: restrict them to the collector's and the web server's
addresses, and require TLS (`REQUIRE SSL`) when the database is on another host.
The rights of each user, and doing the init step by hand, are in
[DATABASE.md](DATABASE.md). No web admin login is created; the web frontend
does that (`npm run create-admin -- <name>`).

### Schema upgrades

The database schema is built into the binary. At every start the collector
compares the database with it; if the database is older (or something is
missing), it **migrates it itself** before it starts working
(`database.auto_migrate: true`, the default; the steps are in the journal).
With `auto_migrate: false` it **does not start** (exit code 78, `systemctl
start` fails, `systemctl status gamewe-collector` and the journal name the
command) until `gamewe db migrate` brought it up. `gamewe db status` shows the version, `gamewe db
migrate -n` the plan; details, what a migration may do and the list of
versions are in [DATABASE.md](DATABASE.md#schema-versions-and-upgrades).
Emptying, dropping and cleaning up old events: `gamewe db reset`, `drop`,
`purge`, `optimize` ([DATABASE.md](DATABASE.md#maintenance-gamewe-db)).

A database created before this version (with `01_schema.sql`, or an older
HLstatsX database of the variant gameWE uses) is upgraded the same way; an
HLstatsX database without schema versions only by hand, once, with `gamewe
db migrate` (the start refuses it, also with `auto_migrate`). Its collector
user has no CREATE / ALTER rights yet: the first migration stops and prints
the `GRANT` (MariaDB) or the ownership change (PostgreSQL) to run once as a
database admin.

The SQL files `hlstatsx.sql`, `01_schema.sql`, `02_seed.sql`, `03_grants.sql`
and `upgrade-*.sql` of older releases are obsolete (`gamewe check` lists the
ones still in `/opt/gamewe/collector/doc/schema/`; delete them).

### Coming from HLstatsX:CE or gameME

Create a new database as above, then copy the old statistics into it:
[MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md) converts an HLstatsX:CE
database, [MIGRATION-GAMEME.md](MIGRATION-GAMEME.md) imports the gameME data
export (ZIP).

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
`log on`) when a server restarted without it, every minute
(`features.log_address.interval`). A log secret is optional: add
`sv_logsecret <n>` (a number 1..2147483647) to `server.cfg` and store the same
number with `gamewe server set <id> -logsecret <n>` or in the web admin; a
server without a stored secret is not checked (CONFIG.md, "Log secret"). Install the
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

Read the release's entry in [CHANGELOG.md](../CHANGELOG.md) first. An update
is: make a backup, replace the binary, start the collector. New tables,
columns, indexes and reference data come with the binary; the collector
migrates the database at start (`database.auto_migrate`). With
`auto_migrate: false` run `gamewe db migrate` before the start (the collector
refuses to start until then). To see the plan first: `gamewe db migrate -n`
before the start.

```bash
# backup first (MariaDB / PostgreSQL)
mariadb-dump -u root -p --single-transaction --routines gamewe > gamewe-before-update.sql
# pg_dump -U postgres -Fc gamewe > gamewe-before-update.dump

# archive
systemctl stop gamewe-collector
mkdir gamewe && tar -xzf gamewe-collector-<version>-linux-amd64.tar.gz -C gamewe && cd gamewe
cp -r etc opt / && cp -rn var / && chown -R gamewe:gamewe /var/lib/gamewe
sudo -u gamewe /opt/gamewe/collector/bin/gamewe db migrate -n   # optional: the plan (exit code 2: migration pending)
systemctl daemon-reload && systemctl start gamewe-collector      # migrates at start
journalctl -u gamewe-collector -n 50 | grep -i migrat
sudo -u gamewe /opt/gamewe/collector/bin/gamewe check

# container
docker compose pull
docker compose up -d                          # migrates at start; auto_migrate: false: "docker compose run --rm collector db migrate -f" first
```

`config.yaml` is never in the archive, so it is kept.

**Log secret (schema version 4):** a server with
`hlstats_Servers.log_secret` set must send exactly that `sv_logsecret`; a
server without one is not checked. You set the secret yourself
(`gamewe server set <id> -logsecret <n>` or the web admin, and the same value
in `server.cfg`); the collector never sets or changes it.

**Update from 0.4.0 - 0.5.1 (automatic log secret removed):** the old
`features.log_secret: auto` counts as `true` and `interval` is ignored (a
warning at start and in `gamewe check`; change it to `enabled: true` or
remove the lines). The secrets the old auto mode stored are kept, but the
collector no longer sets them on the game server: after the game server's
next restart it would send without the secret and its packets would be
rejected. For every server that `gamewe server list` shows with a log secret
either clear it (`gamewe server set <id> -logsecret ""`) or give it a new
value of your own in both places (`-logsecret <n>` and `sv_logsecret <n>` in
`server.cfg`).

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
| `rejected log packet ... not in hlstats_Servers` | Register the shown source `IP:port` in the web admin. With `known server IP but a port ...` another game server on the same host sends logs: add it with its own port (logs are never attributed by IP alone). |
| `rejected log packet ... missing or wrong log secret` | The server has a secret in `hlstats_Servers.log_secret` and the game server sends no or another `sv_logsecret`: set the same number in `server.cfg` and with `gamewe server set <id> -logsecret <n>` (or the web admin), or clear it with `-logsecret ""` to accept all its packets. |
| Commands get no answer | `rcon_password` is empty or wrong (`rcon authentication failed`), the plugin is missing, or the RCON port is blocked (TCP). `gamewe check` shows servers without a password. |
| `systemctl start` fails, `status=78/CONFIG` | The config file (or a `-` flag in `ExecStart`) is wrong, or the schema is not migrated automatically: the journal names it. Fix it, then `systemctl start gamewe-collector` (it is not restarted by itself). `gamewe check` tests the config without the service. |
| `systemctl start` fails, `status=1/FAILURE`, `activating (auto-restart)` | Usually the database is not reachable; it is retried every 5 seconds. The journal has the error. |
| `systemctl restart` does not report a broken config (units from 0.5.3 and older, `Type=simple`) | Install the unit of the current archive (section E) and `systemctl daemon-reload`. |
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
