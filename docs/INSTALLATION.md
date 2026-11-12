# Installing gameWE - collector

Three ways to run the collector:

- [A. Container](#a-container-docker--podman) (Docker / Podman, optional database in the same Compose project)
- [B. Release archive](#b-release-archive-targz) (tar.gz with systemd)
- [B2. Windows](#b2-windows-zip-native) (zip with a Windows service)

All need a database ([C](#c-database)) and game servers that send their logs
to the collector ([D](#d-game-servers)). [E](#e-update) and
[F](#f-uninstall) cover updates and removal, [G](#g-troubleshooting) the
usual problems.

All addresses and passwords below are placeholders: `203.0.113.5` is the
collector host, `198.51.100.7` a game server, `change-me` stands for your own
passwords.

| What | Default |
|------|---------|
| UDP port for game-server logs (Source, GoldSrc) | `41337` (`listen.udp.port`) |
| TCP port for the HTTP logs of Counter-Strike 2 (off by default) | `41338` (`listen.http.port`) |
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

The shortest way, with a MariaDB container and the web on the same host
(`docker run` commands and `docker-compose.quickstart.yml`):
[Quick start with Docker](../README.md#quick-start-with-docker).

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

Every received log line goes to the container log, hence
the log limits. Other commands run the same way, e.g.
`docker run --rm --network host -v ...config.yaml:ro ghcr.io/dennispohlmann/gamewe-collector:latest check`.

Open the port on the host's firewall for your game servers only: UDP 41337,
and TCP 41338 if Counter-Strike 2 servers log over HTTP (`listen.http`), for
example:

```bash
ufw allow from 198.51.100.7 to any port 41337 proto udp            # ufw
ufw allow from 198.51.100.7 to any port 41338 proto tcp            # ufw, CS2 (HTTP)
firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=198.51.100.7 port port=41337 protocol=udp accept' \
  && firewall-cmd --reload                                          # firewalld
firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=198.51.100.7 port port=41338 protocol=tcp accept' \
  && firewall-cmd --reload                                          # firewalld, CS2 (HTTP)
```

### With Podman

The commands and Compose files above also run with Podman, rootless as well
(tested with Podman 5.7): write `podman` for `docker`. The host network works
rootless too and keeps the source address of the log packets. What is
different:

- **Compose:** use Docker Compose against the Podman socket
  (`systemctl --user enable --now podman.socket`,
  `export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock`, then
  `podman compose ...` or `docker-compose ...`). podman-compose (1.5) leaves
  out `configs:` with `content:` without a warning: in
  `docker-compose.quickstart.yml` the collector then has no config and the
  database no users.
- **Short image names:** `podman run` and `podman pull` can pull
  `mariadb:12.3` or `postgres:18` only if `/etc/containers/registries.conf`
  names a search registry. Write `docker.io/library/mariadb:12.3`. Through
  the socket (Compose) Podman takes Docker Hub itself.
- **The config file:** rootless, uid 65532 of the container is a different
  uid on the host, so a `config.yaml` with mode `0600` or `0640` gives
  `permission denied`. Hand it to that uid once, the mode stays:
  `podman unshare chown 65532:65532 config/config.yaml`; edit it from then on
  with `podman unshare $EDITOR config/config.yaml`. On hosts with
  SELinux (Fedora, RHEL) also add `:Z` to the bind mount (`...config.yaml:ro,Z`).
- **Start after a reboot:** there is no daemon. Rootless containers come back
  only through `podman-restart.service`, and only those with the restart
  policy `always` (not `unless-stopped`):
  `systemctl --user enable podman-restart.service` and
  `loginctl enable-linger <user>` (user services without a login).

### On Kubernetes (Helm)

The chart `charts/gamewe-collector` runs the same image; every release
publishes it in the same version as the collector (semantic-release sets
`version` and `appVersion` of `Chart.yaml`), as
`oci://ghcr.io/dennispohlmann/charts/gamewe-collector` and as a `.tgz` in
the GitHub release. The web has its own chart in gameWE-web.

```bash
kubectl create namespace gamewe
kubectl -n gamewe create secret generic gamewe-collector --from-file=config.yaml=/etc/gamewe/collector/config.yaml
helm install collector oci://ghcr.io/dennispohlmann/charts/gamewe-collector --version <version> -n gamewe \
  --set existingSecret=gamewe-collector
kubectl -n gamewe logs -f deploy/collector-gamewe-collector
```

The config file holds the database password, so it is a Secret with the
key `config.yaml` (`existingSecret`); `--set-file config=config.yaml`
creates it from the values instead. The collector recognises a game server
by the source address of its log packets, so by default it uses the host
network of its node (`hostNetwork: true`, like `--network host` above):
the game servers send their logs to that node's address (pin it with a
`nodeSelector`). With `hostNetwork: false` the chart creates a Service of
type LoadBalancer with `externalTrafficPolicy: Local` (UDP 41337, and TCP
41338 with `service.http.enabled`); check that the load balancer keeps the
source address. Exactly one collector runs (`replicas: 1`, strategy
`Recreate`, also while updating). A volume at `/var/lib/gamewe`
(`persistence`, on by default) keeps the GeoIP database; an empty one gets
it from the monthly download at the first start (`geoip.auto_update`). The
container runs as `nonroot` (uid 65532) with a read-only root file system.
Update: `helm upgrade collector oci://ghcr.io/dennispohlmann/charts/gamewe-collector
--version <new version> -n gamewe --reuse-values`. If the package on GHCR
is private, `helm registry login ghcr.io` first. All values:
[charts/gamewe-collector/values.yaml](../charts/gamewe-collector/values.yaml).

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
opt/gamewe/collector/doc/schema/move/           move-db.sh: copy the data from MariaDB to PostgreSQL or back (DATABASE.md)
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
  udp:                          # Source, GoldSrc (logaddress_add)
    enabled: true
    address: 0.0.0.0            # IP for the log packets (0.0.0.0 = all IPv4, :: = all)
    port: 41337                 # UDP port
    # public_address: 203.0.113.5:41337   # address the servers send to, if not the collector's RCON address (NAT)
  http:                         # Counter-Strike 2 (logaddress_add_http)
    enabled: false
    address: 0.0.0.0
    port: 41338                 # TCP port
    # public_url: http://203.0.113.5:41338   # URL the CS2 servers post to, if not the collector's RCON address (NAT)
    max_body: 128KiB
    read_timeout: 10s
    max_connections: 64
    max_connections_per_ip: 8
    # trusted_proxies: []       # reverse proxies whose X-Forwarded-For is used

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
    timezone: ""                # IANA zone, e.g. Europe/Berlin; empty: timezone, else the server's
    time: "00:00"
  inactive_players:
    enabled: true               # daily: players without play for MinActivity days to hlstats_PlayersInactive
    time: "01:00"
timezone: ""                    # the collector's time zone; empty: the server's
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
sudo -u gamewe /opt/gamewe/collector/bin/gamewe config test   # the config file only, nothing started
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

## B2. Windows (zip, native)

> **Not yet tested on a real Windows machine by hand.** CI builds the zip
> and runs the unit tests on a Windows Server 2025 runner, and installs,
> starts, stops and removes the service there once (without a database). A
> collector with a real database and game servers on Windows has not been
> run yet. Docker Desktop or WSL2 with the container ([A](#a-container-docker--podman))
> is the tried way on Windows.

The archive `gamewe-collector-<version>-windows-amd64.zip` (Windows x86_64,
Windows 10 / Server 2016 or newer) holds one folder:

```
gamewe.exe                  the collector (the same commands as on Linux)
config.example.yaml         the config to copy to config.yaml
data\dbip-city-lite.mmdb    GeoIP database (DB-IP City Lite, CC BY 4.0), updated monthly where it is copied to
doc\                        README, CHANGELOG, INSTALLATION, CONFIG, USAGE, ..., licenses, GEOIP-NOTICE,
                            schema\init and schema\postgres\init (00_create_database.sql; init-db.sh is for Linux)
```

| What | Windows |
|------|---------|
| Binary, docs | `C:\Program Files\gameWE\collector\` (any folder) |
| Config file (default of `-config`) | `%ProgramData%\gameWE\collector\config.yaml` (normally `C:\ProgramData\...`) |
| Data directory (GeoIP database, service log) | `%ProgramData%\gameWE\collector\data\` |
| Service log | `data\collector.log`, at 50 MiB renamed to `collector.log.1` (the older one is replaced) |
| Service | `gamewe-collector` ("gameWE collector"), virtual account `NT SERVICE\gamewe-collector`, automatic (delayed) start, restarted after a failure (5 s, 30 s, then every minute) |

In PowerShell **as administrator**, from the unpacked folder:

```powershell
(Get-FileHash .\gamewe-collector-0.1.0-windows-amd64.zip -Algorithm SHA256).Hash   # compare with the .sha256 file
Expand-Archive .\gamewe-collector-0.1.0-windows-amd64.zip -DestinationPath .
cd .\gamewe-collector-0.1.0-windows-amd64
$bin  = "C:\Program Files\gameWE\collector"
$conf = "$env:ProgramData\gameWE\collector"
New-Item -ItemType Directory -Force $bin, "$conf\data" | Out-Null
Copy-Item gamewe.exe, doc -Destination $bin -Recurse -Force
if (-not (Test-Path "$conf\data\dbip-city-lite.mmdb")) { Copy-Item data\dbip-city-lite.mmdb "$conf\data\" }
if (-not (Test-Path "$conf\config.yaml")) { Copy-Item config.example.yaml "$conf\config.yaml" }
notepad "$conf\config.yaml"                    # database.*, listen.* as on Linux (CONFIG.md)
& "$bin\gamewe.exe" config test                # the config file only, nothing started
& "$bin\gamewe.exe" check                      # config, database, TLS, schema, servers, settings
& "$bin\gamewe.exe" service install            # shows what it does, asks [y/N]
& "$bin\gamewe.exe" service start
Get-Content "$conf\data\collector.log" -Wait -Tail 50
```

`gamewe service install` creates the service with the command line
`"C:\Program Files\gameWE\collector\gamewe.exe" run -config "<config>"`
(the config given with `-config`, default the one above) and sets the
rights: `%ProgramData%\gameWE\collector` no longer inherits from
`ProgramData` (where every user may read), only Administrators and SYSTEM
(full) and the service (read) have access; the service may also write
`data\` (GeoIP update, log). A config outside that folder gets a read right
for the service. Check with `icacls "$conf\config.yaml"`.

- **Firewall:** the UDP port of the game-server logs (default 41337) and, for
  Counter-Strike 2 over HTTP, TCP 41338 must be open for the game servers:
  `New-NetFirewallRule -DisplayName "gameWE collector UDP" -Direction Inbound -Protocol UDP -LocalPort 41337 -Program "$bin\gamewe.exe" -Action Allow`.
- **Start and failure:** the service reports "running" as soon as it is
  started; whether the collector got as far as listening is in
  `collector.log`. After a failure it is restarted after 5 seconds, then 30
  seconds, then every minute; also after a config error (exit code 78):
  Windows has no "do not restart on exit code 78" like the systemd unit, so
  fix the config or stop the service. The exit code is in
  `sc.exe query gamewe-collector` (`SERVICE_EXIT_CODE`).
- **Stop:** `gamewe service stop` (or `sc.exe stop gamewe-collector`, the
  Services console) stops it cleanly, as `systemctl stop` on Linux. In a
  console, Ctrl+C stops a `gamewe run` cleanly.
- **Commands that refuse while a collector runs** (`db migrate`, `reset`,
  ...) name the service and `sc.exe stop gamewe-collector` / `sc.exe start
  gamewe-collector`. A `gamewe run` started by hand in a console is found by
  the collector lock of the database, as on Linux.
- **Update:** `gamewe service stop`, replace `gamewe.exe` (and `doc`), then
  `gamewe db migrate` if the release notes say so (or `database.auto_migrate`),
  `gamewe service start`. The config and `data\` stay.
- **Uninstall:** `gamewe service uninstall` (stops it first), then delete
  `C:\Program Files\gameWE\collector` and, with the config and the GeoIP
  database, `%ProgramData%\gameWE`.
- **Database:** MariaDB or PostgreSQL may run on Windows or elsewhere; create
  the database and the users with the SQL of `doc\schema\init\00_create_database.sql`
  (MariaDB) or `doc\schema\postgres\init\00_create_database.sql` (PostgreSQL)
  by hand, the passwords set in place of the placeholders (C), then
  `gamewe db init`.
- Not on Windows: `sd_notify` (systemd), the check of the file modes of the
  config (`gamewe config test` warns about readable files on Linux only;
  Windows has ACLs, see `icacls` above).

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
db migrate` (the start refuses it, also with `auto_migrate`); stop the
collector before `gamewe db migrate`, it refuses while one runs. Its collector
user has no CREATE / ALTER rights yet: the first migration stops and prints
the `GRANT` (MariaDB) or the ownership change (PostgreSQL) to run once as a
database admin.

The SQL files `hlstatsx.sql`, `01_schema.sql`, `02_seed.sql`, `03_grants.sql`
and `upgrade-*.sql` of older releases are obsolete (`gamewe check` lists the
ones still in `/opt/gamewe/collector/doc/schema/`; delete them).

### Coming from HLstatsX:CE or gameME

Create a new database as above, then copy the old statistics into it:
[MIGRATION-HLSTATSX.md](MIGRATION-HLSTATSX.md) converts an HLstatsX:CE
database (`gamewe db convert`), [MIGRATION-GAMEME.md](MIGRATION-GAMEME.md) imports the gameME data
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

`server add` shows the change and asks `[y/N]` (`--force` / `-f` does not
ask, `--dry-run` / `-n` only shows it). `-rcon-password-file -` reads it from
stdin, which then cannot answer the question: add `--force`. `-rcon-password`
works too but is visible in the process list. The RCON password is never
printed.

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

**GoldSrc (CS 1.6, CS: Condition Zero, DoD, HL1)**: `log on`,
`logaddress_add 203.0.113.5 41337` and `rcon_password`; in-game replies through
the gameWE AMX Mod X plugin ([GAMES.md](GAMES.md)).

**Counter-Strike 2**: CS2 logs only over HTTP. Switch on `listen.http` in
the config (and open TCP 41338 for the server), add the server with a cs2
game and a log token of its own
(`gamewe server add 198.51.100.7:27015 -game cs2 -logsecret <log token> ...`,
or the field "Log token" in the web admin),
start CS2 with `-usercon` (RCON is off without it) and put into `server.cfg`:

```
log on
logaddress_delall_http
logaddress_add_http "http://203.0.113.5:41338/<log token>"  // collector IP, HTTP port, log token of this server
rcon_password "change-me"                                      // the same as in hlstats_Servers
```

- `<log token>` is the token of this server, 16 to 64 letters, digits, `-` or
  `_`. It is stored per server (`hlstats_Servers.log_secret`) and is not
  `sv_logsecret`. It is required: the collector finds the server by this
  token, and the post must come from the server's IP in the web admin. Two
  servers cannot have the same token. How to make one:
  [Creating the log token](#creating-the-log-token-for-cs2). A server added or
  changed in the web admin is picked up within about a minute.
- `logaddress_delall_http` removes old URLs (the old `/<game port>` form of
  collector 1.10 and older, an old token), so the server posts to one URL only.
- `sv_logsecret` plays no part on CS2 (its HTTP logs carry none). The log
  token and the firewall protect the input instead.

With the RCON password the collector registers this URL itself
(`features.log_address.http`, every minute, then `log on`). Without the
[gameWE CounterStrikeSharp plugin](https://github.com/DennisPohlmann/gameWE-plugin-counterstrikesharp)
there are no in-game replies and no weapon accuracy; hits per body part come
from the game's own log. With the plugin (and `-usercon`, the RCON password
of `hlstats_Servers`) the collector answers in-game commands (`!rank`, ...)
and stores the plugin's weapon statistics ([GAMES.md](GAMES.md#counter-strike-2)).
`gamewe logaddress -server <id>` shows whether the URL is registered (the
log token as `<secret>`).

The collector's log says whether the posts arrive: `receiving HTTP logs`
(server ID, name) for the first post of a server and the first after 10
minutes without posts, and `rejected HTTP log post` with `status`, `reason`
and `hint` for a rejected one, repeated every 5 minutes while it keeps
coming (collector 1.6.0 and newer). 404: the path of the URL is wrong (the
old `/<game port>` form, log token missing or wrong, or no CS2 server of the
web admin at the sender's IP has this token: the server not added yet, another
IP, or its ranking's game is not `cs2`); see
[Troubleshooting](#g-troubleshooting). The token itself is never logged.

Restrict the collector's ports (UDP 41337, TCP 41338 for CS2) with a firewall
to your game servers: packets from addresses that are not in `hlstats_Servers` are
rejected anyway (`rejected log packet` in the journal). Test the whole chain
from the collector host with the sample game:

```bash
/opt/gamewe/collector/bin/gamewe simulate -send 127.0.0.1:41337
```

### Creating the log token for CS2

The log token is a random string you make yourself, one per CS2 server:
16 to 64 characters, only `A-Z`, `a-z`, `0-9`, `-` and `_` (no `/`, `+`
or `=`, it is part of the URL path). Neither the collector nor the web admin
generates it.

1. Make a random token on any machine:

   ```bash
   openssl rand -hex 24                           # 48 hex characters
   ```

   Without OpenSSL (Linux) or on Windows (PowerShell 7):

   ```bash
   LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 48; echo
   ```

   ```powershell
   [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24))
   ```

   Do not use a word, a password you use elsewhere or the RCON password.

2. Store it for the server: web admin, Game servers, the server, "Log
   token"; or `gamewe server set <id> -logsecret <token>`. The collector
   picks it up within about a minute; no restart.

3. With the RCON password stored, the collector registers
   `http://<collector IP>:41338/<log token>` itself within a
   minute. Run `logaddress_delall_http` on the game server once, so that it
   no longer posts to the old URL. A URL in `server.cfg` has to be changed
   by hand.

Treat the token like a password: anyone who knows it and can send from the
CS2 server's IP can post log lines for it. Keep it out of chats, tickets,
screenshots and forum posts; the collector never logs it (`gamewe
logaddress` and the log show `<secret>`). If it leaked, store a new one
(steps 1 to 3); the old URL then gets 404.

Collector 1.10 and older took `/<game port>` and `/<game port>/<log token>`
(1.9.0 and older also a `listen.http.token` for all servers). These URLs are
refused now; the collector logs once per server that it still uses one. Give
every CS2 server a log token, run `logaddress_delall_http` on it and put the
new URL into `server.cfg` (or let the collector register it over RCON). A
`listen.http.token` in the config is ignored with a warning.

## E. Update

Whether a newer release is out: `gamewe check-update`; `gamewe run` logs it at
start and daily, the other commands print a note on stderr, and the web admin
shows it on its overview (`features.update_check`, nothing is installed
automatically). Read the release's entry in [CHANGELOG.md](../CHANGELOG.md) first. An update
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

**Update to 1.7 (schema versions 24 and 25): together with the website 1.8.**
Stop both, update the collector first (it migrates at start), then the website.

- Version 25 moves the Steam Web API key, the metrics token and the TeamSpeak
  passwords from `hlstats_Options` to `hlstats_gamewe_secrets`, which
  `gamewe_web` cannot read. A website older than 1.8 no longer finds them until
  it is updated. The website reads them with `DB_ADMIN_USER`: check that its `web.env` has it. The
  backup above is the way back: this step removes the rows from `hlstats_Options`.
- Version 24 adds the column `log_only` to `hlstats_Events_PlayerActions`. On a
  large table with MyISAM this rewrites and locks the table; `gamewe db migrate -n`
  shows the plan, plan the time before the update.
- **Counter-Strike 2:** UDP lines for CS2 rows are now refused unless
  `listen.udp.source2` is on. A row with a stored log secret needs it as the
  last URL segment; the collector registers that URL, remove the old one on the
  game server with `logaddress_delall_http`. That value is the server's log
  token and may now be 16 to 64 letters, digits, `-` or `_`; a number stored
  before keeps working until you change it.
- **PostgreSQL only, by hand once:** `REVOKE SELECT (hostname, hostgroup) ON
  hlstats_Events_Connects FROM gamewe_web;` (MariaDB: done by the migration's
  grants).

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
| `rejected HTTP log post` (CS2) | `reason` says why, `status` is what the game server got back. 404: `HTTP path ...` names the problem of the `logaddress_add_http` URL: not `/<log token>`, the old `/<game port>` URL of collector 1.10 and older (also logged once per server as `old URL`), or no CS2 server at the sender's IP has this token (server not added, another IP, its game not `cs2`, a wrong token, or two servers with the same token). The log token is the server's, from the web admin, not `sv_logsecret`. Also `larger than listen.http.max_body` or `too many open HTTP connections`. Repeated every 5 minutes while the posts keep coming. |
| `rejected log packet ... missing or wrong log secret` | The server has a secret in `hlstats_Servers.log_secret` and the game server sends no or another `sv_logsecret`: set the same number in `server.cfg` and with `gamewe server set <id> -logsecret <n>` (or the web admin), or clear it with `-logsecret ""` to accept all its packets. |
| Commands get no answer | `rcon_password` is empty or wrong (`rcon authentication failed`), the plugin is missing, or the RCON port is blocked (TCP). `gamewe check` shows servers without a password. |
| `systemctl start` fails, `status=78/CONFIG` | The config file (or a `-` flag in `ExecStart`) is wrong, or the schema is not migrated automatically: the journal names it. Fix it, then `systemctl start gamewe-collector` (it is not restarted by itself). `gamewe config test` lists every problem of the config file with its line, `gamewe check` also tests the database, both without the service. |
| `systemctl start` fails, `status=1/FAILURE`, `activating (auto-restart)` | Usually the database is not reachable; it is retried every 5 seconds. The journal has the error. With `automatic migration ... a collector is running on this database`: another collector (another host, a container) runs on the same database and the schema needs a migration; stop the other one. |
| `gamewe db migrate` (or `convert`, `reset`, `drop`): `a collector is running on this host` / `on this database` | Run the commands the message lists, one per line: stop the collector (`systemctl stop gamewe-collector`, `docker stop CONTAINER`, `kill PID` for one started by hand), the same command again, start it again. `on this database ... but not here`: it runs on another host or in a container (`docker compose stop collector` there). `-f` does not skip this check. (0.8.1 and older waited up to 2 minutes for the heartbeat of a collector up to 0.7.1; now its process is checked.) |
| `gamewe db migrate`: `the table is locked by another session` | A session holds the table (a collector, the web, a `mariadb` / `psql` client with an open transaction or `LOCK TABLES`). Stop the collector, find the session with `SHOW PROCESSLIST` (PostgreSQL: `SELECT * FROM pg_stat_activity`), end it, migrate again. Nothing is half done: every step is idempotent. |
| The question `Backup made? Migrate now? [y/N]` (or another `[y/N]`) does not take Enter, the terminal shows `^M`, Ctrl-C does nothing (0.7.1 and older) | The terminal sends CR for Enter (`icrnl` off, left so by another program). Press `Ctrl-\` (quits without changing anything; not Ctrl-J or Ctrl-D, they would send the typed `y`), then `stty sane`. Newer versions switch `icrnl` on for the question (with a note) and stop on Ctrl-C. |
| `systemctl restart` does not report a broken config (units from 0.5.3 and older, `Type=simple`) | Install the unit of the current archive (section E) and `systemctl daemon-reload`. |
| Service does not start, `status=31/SYS` | A hardening option in the unit blocks something on your system. See `journalctl -u gamewe-collector` and relax `SystemCallFilter` with `systemctl edit gamewe-collector`. |

**What you see in `journalctl -u gamewe-collector -f`:** every log line a game
server sends, as `[name ip:port] L 10/22/2026 - 12:00:00: "Player<2><STEAM_1:0:123><CT>" killed …`.
If those lines appear, data arrives and is accepted. Control characters in
player names are shown as `\xNN`. Besides those:

- `game servers loaded from hlstats_Servers`: after start and when servers change
- `game server active`: the first accepted line of a server
- `rejected log packet … source=IP:port` (UDP) and `rejected HTTP log post`
  (CS2): a sender that is not in `hlstats_Servers`, or another reason (first
  time, then every 5 minutes while it keeps coming, with `count`, `new` and a
  `hint`)
- `receiving HTTP logs`: the first accepted HTTP post of a CS2 server, and the
  first after 10 minutes without posts
- `processing log line failed`: a database error

Every line in the journal can be a lot: a busy server sends several lines per
second. Limit the journal size, e.g. `SystemMaxUse=1G` in
`/etc/systemd/journald.conf`.

Use `systemctl edit gamewe-collector` to add `-log-level debug` to `ExecStart` for
details about every reply. Test against your database with `gamewe check` and
`gamewe simulate`. `simulate` without `-send` writes the sample game into the
database (`-game css` by default): test players `STEAM_1:x:900000x` and server
`127.0.0.1:27015`, which it adds to `hlstats_Servers` itself. It prints the RCON
commands it would send.
