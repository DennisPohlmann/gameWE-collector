# gameWE - collector

gameWE is a statistics system for Source, GoldSrc and Source 2 game servers
(Counter-Strike: Source, CS:GO, CS 1.6, CS: Condition Zero, TF2, Counter-Strike 2, ...), in the style of gameME /
HLstatsX. The **collector** is its core: one static Go binary that

- receives the logs of many game servers at once over UDP (`logaddress_add`)
  and, for Counter-Strike 2, over HTTP (`logaddress_add_http`); both inputs
  can be switched on and off. Only servers registered in `hlstats_Servers` are
  accepted, on Source servers with a log secret (`sv_logsecret`) per server set
  in the CLI or the web admin,
- tracks connects, disconnects, team changes, kills (weapon, headshot), suicides,
  teamkills, round wins and team scores, plus per-weapon stats from the gameWE plugins,
- writes players, kills, skill, rounds, weapon and action statistics into an
  **HLstatsX database** (the gameME-style HLstatsX schema) on **MariaDB / MySQL**
  or **PostgreSQL**, with optional TLS that never falls back to plaintext,
- answers gameME's in-game commands (**!rank, !kpd, !session, !next, !top10,
  !statsme, !hideranking, !gamewe_reset, ...**, in 9 languages) over RCON
  through the gameWE SourceMod plugin (AMX Mod X plugin on GoldSrc), opens
  the web frontend's pages in the in-game browser and can announce points of
  kills and actions,
- runs daily awards, ranking resets queued by the web admin, scheduled
  ranking resets (daily to yearly, one schedule for all rankings or per
  ranking, from any start day) that keep the best of every period in a hall of
  fame, GeoIP locations
  (DB-IP database included), the RCON status of every server and, if
  enabled, the daily move of inactive players to `hlstats_PlayersInactive`;
  each of these jobs can also be run from the command line (`gamewe awards`,
  `gamewe inactive`, `gamewe reset`, `gamewe halloffame`, `gamewe serverinfo`, ...).
- tells you when a newer release is out (log at start and daily, a note on
  stderr before the other commands, `gamewe check-update`); it never updates
  itself ([`features.update_check`](docs/CONFIG.md#features)).

The config file holds only the log inputs (UDP, HTTP) and the database connection.
Game servers and all point and command settings come from the database, so
they are managed in the web admin; the collector re-reads them every minute,
without a restart.

Current version: see `VERSION` and [CHANGELOG.md](CHANGELOG.md).

## Requirements

- Linux x86_64 (static binary), a container runtime (Docker / Podman) or
  Kubernetes with Helm (`charts/gamewe-collector`,
  [INSTALLATION](docs/INSTALLATION.md#on-kubernetes-helm))
- MariaDB 11.4 or 12.3, or PostgreSQL 17 or 18 (tested in CI)
- Game servers with a [supported game](#supported-games) that can send their
  logs to the collector (UDP; HTTP for Counter-Strike 2)

## Supported games

| Engine | Games | Logs | In-game replies |
|--------|-------|------|-----------------|
| Source | CS:S, CS:GO, TF2, DoD:S, HL2:DM, L4D, L4D2, Insurgency, Zombie Panic! Source, Fortress Forever, CSPromod, Fistful of Frags, No More Room in Hell | UDP | gameWE SourceMod plugin |
| GoldSrc | CS 1.6, CS: Condition Zero, DoD, TFC, HL1, NS | UDP | gameWE AMX Mod X plugin |
| Source 2 | Counter-Strike 2 | HTTP (`listen.http`) | gameWE CounterStrikeSharp plugin (optional; without it the stats come from the game's own logs and there are no replies) |

Details per game: [docs/GAMES.md](docs/GAMES.md).

## Quick start with Docker

Database, collector and web on one Docker host, ready in a few minutes. Every
`change-me` is a placeholder: replace it with your own password (no `'`, `\`
or `$`), the same value everywhere it appears. The database only listens on
`127.0.0.1` and in the Docker network; the admin login is signed with a
random secret (step 4), never with a value from this page.

| Port | Container | For |
|------|-----------|-----|
| `41337/udp` | collector (host network) | the logs of the game servers (`logaddress_add`); open it in the firewall for the game servers |
| `3000/tcp` | web | the website and the admin area: `http://<docker host>:3000` |
| `3306/tcp` | database | only on `127.0.0.1`, for the collector |

The collector runs in the host network: it recognises a game server by the
source address of its log packets, which a published port (`-p`) can replace.

**1. Network and database** (MariaDB 12.3)

```bash
docker network create gamewe

docker run -d --name gamewe-db --restart unless-stopped \
  --network gamewe \
  -p 127.0.0.1:3306:3306 \
  -e MARIADB_ROOT_PASSWORD=change-me-root \
  -v gamewe-db:/var/lib/mysql \
  mariadb:12.3
```

**2. Database users** (once; waits until the database is up)

```bash
until docker exec gamewe-db healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1; do sleep 2; done
docker exec -i gamewe-db mariadb -uroot -pchange-me-root <<'SQL'
CREATE DATABASE gamewe CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'gamewe'@'%' IDENTIFIED BY 'change-me-collector';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX ON gamewe.* TO 'gamewe'@'%' WITH GRANT OPTION;
CREATE USER 'gamewe_web'@'%' IDENTIFIED BY 'change-me-web';
CREATE USER 'gamewe_web_admin'@'%' IDENTIFIED BY 'change-me-web-admin';
GRANT SELECT ON gamewe.* TO 'gamewe_web_admin'@'%';
SQL
```

`gamewe` is the collector, `gamewe_web` the public pages (read only),
`gamewe_web_admin` the admin area. The collector creates the tables and
grants the two web users their table rights at its first start.

**3. Collector**

The collector reads its database connection and log port only from a config
file, not from environment variables, so it needs this short one:

```bash
mkdir -p ~/gamewe && cat > ~/gamewe/collector.yaml <<'EOF'
listen:
  udp:
    port: 41337
database:
  type: mariadb
  host: 127.0.0.1
  port: 3306
  name: gamewe
  user: gamewe
  password: "change-me-collector"
EOF

docker run -d --name gamewe-collector --restart unless-stopped \
  --network host \
  -e TZ=Europe/Berlin \
  -v ~/gamewe/collector.yaml:/etc/gamewe/collector/config.yaml:ro \
  -v gamewe-collector:/var/lib/gamewe \
  --log-opt max-size=50m --log-opt max-file=5 \
  ghcr.io/dennispohlmann/gamewe-collector:latest

docker logs gamewe-collector    # "database migrated", "listening for game server logs"
```

**4. Web**

`ADMIN_SECRET` signs the admin logins: a random value of at least 32
characters, created once and kept, also when you recreate the container
(a new one signs everybody out and makes stored TOTP keys unusable):

```bash
openssl rand -hex 32 > ~/gamewe/admin-secret && chmod 600 ~/gamewe/admin-secret

docker run -d --name gamewe-web --restart unless-stopped \
  --network gamewe \
  -p 3000:3000 \
  -e DB_TYPE=mariadb \
  -e DB_HOST=gamewe-db \
  -e DB_NAME=gamewe \
  -e DB_USER=gamewe_web \
  -e DB_PASSWORD=change-me-web \
  -e DB_ADMIN_USER=gamewe_web_admin \
  -e DB_ADMIN_PASSWORD=change-me-web-admin \
  -e ADMIN_SECRET="$(cat ~/gamewe/admin-secret)" \
  -e TZ=Europe/Berlin \
  ghcr.io/dennispohlmann/gamewe-web:latest
```

**Or all in one with Docker Compose** (instead of steps 1 to 4):
[`docker-compose.quickstart.yml`](docker-compose.quickstart.yml) (the same
file in gameWE-collector and gameWE-web) starts database, collector and web
in one go. The database users and the collector's config are written into
the file itself; the passwords and the admin secret come from a `.env` file
next to it with random values (the file has no defaults and does not start
without them):

```bash
mkdir -p ~/gamewe && cd ~/gamewe
curl -O https://raw.githubusercontent.com/DennisPohlmann/gameWE-collector/main/docker-compose.quickstart.yml
cat > .env <<EOF
DB_ROOT_PASSWORD=$(openssl rand -hex 16)
COLLECTOR_PASSWORD=$(openssl rand -hex 16)
WEB_PASSWORD=$(openssl rand -hex 16)
WEB_ADMIN_PASSWORD=$(openssl rand -hex 16)
ADMIN_SECRET=$(openssl rand -hex 32)
EOF
chmod 600 .env
docker compose -f docker-compose.quickstart.yml up -d
docker compose -f docker-compose.quickstart.yml logs collector   # "database migrated", "listening for game server logs"
```

**5. Admin user, rankings and servers**

Create the first admin user; it prints the password once:

```bash
docker exec -it gamewe-web node scripts/create-admin.mjs admin
# Compose: docker compose -f docker-compose.quickstart.yml exec web node scripts/create-admin.mjs admin
```

Open `http://<docker host>:3000` and sign in at `http://<docker host>:3000/admin`
with `admin` and that password. One ranking per game is already there (`css`,
`cs2`, `csgo`, `tf`, `dods`, `l4d2`, `cstrike`, ... ; `gamewe game list`
shows all). Then add your game servers, in the web admin or with the
collector's command line; both write the same database and the collector
picks the change up within a minute.

- **Web admin:** Rankings, pick the game's ranking (or create your own, e.g.
  "CS:S Public", copying a game's weapons and actions), "Add server": address,
  game port, RCON password.
- **Command line** (the `gamewe` binary in the collector container; it asks
  before it changes something):

  ```bash
  # an own ranking, with the weapons, actions, awards and ranks of css (optional)
  docker exec -it gamewe-collector /opt/gamewe/collector/bin/gamewe game add css1 "CS:S Public" -template css
  # the game server: its address and game port, the ranking, the RCON password
  docker exec -it gamewe-collector /opt/gamewe/collector/bin/gamewe server add 203.0.113.7:27015 -game css1 -name "Public" -rcon-password change-me-rcon
  docker exec -it gamewe-collector /opt/gamewe/collector/bin/gamewe server list
  # Compose: docker compose -f docker-compose.quickstart.yml exec collector /opt/gamewe/collector/bin/gamewe ...
  ```

Everything else (points, awards, in-game commands, design, users) is set
in the web admin.

**6. Game servers: send the logs to the collector**

Add to the `server.cfg` of each game server (`<docker host>` is the address
the game server reaches the collector at):

```
log on
logaddress_add <docker host>:41337      // the collector's address and UDP port
rcon_password "change-me-rcon"          // the same as in the web admin / server add
```

and restart the map or the server. With the RCON password stored, the
collector also checks every minute that it is registered as log address
and adds itself (`logaddress_add`, `log on`) when it is missing; the lines
in `server.cfg` keep it there after a restart. The first log lines show up
in `docker logs gamewe-collector`, the players on the website. For in-game
commands (`!rank`, `!top10`, ...) and more statistics install the gameWE
plugin for the engine (see gameWE components below). Optional: a log secret
against faked log packets, `sv_logsecret` (collector docs, CONFIG.md).

Counter-Strike 2 sends its logs over HTTP: add to the collector's config
(`listen:` in `~/gamewe/collector.yaml` or in the Compose file) an
`http:` part with `enabled: true` and `port: 41338`, restart the collector,
open TCP 41338 for the game servers and use
`logaddress_add_http "http://<docker host>:41338/27015"` (the game port at
the end) instead of `logaddress_add`; details in
[Counter-Strike 2](https://github.com/DennisPohlmann/gameWE-collector/blob/main/docs/GAMES.md#counter-strike-2).

For production put a reverse proxy with TLS in front of the web (bind it
to `127.0.0.1:3000:3000`), keep the collector's config file private
(`chmod 600`, owner uid `65532`), and see the installation guides of the
[collector](https://github.com/DennisPohlmann/gameWE-collector/blob/main/docs/INSTALLATION.md)
and the [web](https://github.com/DennisPohlmann/gameWE-web/blob/main/docs/INSTALLATION.md)
(PostgreSQL, TLS to the database, own templates, updates).

## Quick start

1. Install the collector as a container (`ghcr.io/dennispohlmann/gamewe-collector`,
   host network) or from the release archive
   `gamewe-collector-<version>-linux-amd64.tar.gz` with systemd.
2. Create the database and its users with `schema/init/init-db.sh` (MariaDB)
   or `schema/postgres/init/init-db.sh` (PostgreSQL).
3. Put the database connection into `/etc/gamewe/collector/config.yaml`,
   create the tables with `gamewe db init` (or let the first start do it)
   and check with `gamewe config test` (the file, nothing started) and
   `gamewe check` (the database). The schema is built into the binary: after
   an update the collector migrates the database at start
   (`database.auto_migrate`, the default; make a backup before updating), or
   with `auto_migrate: false` refuses to start until `gamewe db migrate` ran
   ([docs/DATABASE.md](docs/DATABASE.md#schema-versions-and-upgrades)).
   `gamewe db` also empties (`reset`), drops (`drop`) and cleans up old
   events (`purge`, `optimize`).
4. Register the game servers (web admin or `gamewe server add`) and add to
   their `server.cfg`:

   ```
   log on
   logaddress_add 203.0.113.5:41337     // the collector's IP and UDP port
   rcon_password "change-me"            // the same as in hlstats_Servers
   ```

   Counter-Strike 2: switch on `listen.http` in the config and use
   `logaddress_add_http "http://203.0.113.5:41338/27015"` (the collector's
   IP and HTTP port, then the game server's port) instead of `logaddress_add`.

Every step in detail: [docs/INSTALLATION.md](docs/INSTALLATION.md).

## Documentation

| File | Content |
|------|---------|
| [docs/INSTALLATION.md](docs/INSTALLATION.md) | installation step by step: container, release archive, database, game servers, update, removal, troubleshooting |
| [docs/CONFIG.md](docs/CONFIG.md) | config file, GeoIP database, game servers and the settings in the database (`hlstats_Options`) |
| [docs/USAGE.md](docs/USAGE.md) | in-game commands, points, the `gamewe` command line |
| [docs/INTERFACE.md](docs/INTERFACE.md) | log lines of the plugins, RCON reply commands, raw messages |
| [docs/DATABASE.md](docs/DATABASE.md) | database users, rights, TLS, incomplete schemas, moving to PostgreSQL or back |
| [docs/DB-SEMANTICS.md](docs/DB-SEMANTICS.md) | what the collector writes into which table (for the web frontend) |
| [docs/GAMES.md](docs/GAMES.md) | games, engines, log formats |
| [docs/MIGRATION-HLSTATSX.md](docs/MIGRATION-HLSTATSX.md) | moving from HLstatsX:CE: converting its database into a new gameWE database |
| [docs/MIGRATION-GAMEME.md](docs/MIGRATION-GAMEME.md) | moving from gameME: importing the gameME data export (ZIP) |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | building, tests, CI and releases |
| [CHANGELOG.md](CHANGELOG.md) | changes per version |
| [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) | third-party components and their licenses |

The release archive and the image carry the same files in
`/opt/gamewe/collector/doc/`.

## gameWE components

| Repository | Part |
|------------|------|
| [gameWE-collector](https://github.com/DennisPohlmann/gameWE-collector) | receives the game server logs and fills the database (this repository) |
| [gameWE-web](https://github.com/DennisPohlmann/gameWE-web) | statistics website and admin area |
| [gameWE-plugin-sourcemod](https://github.com/DennisPohlmann/gameWE-plugin-sourcemod) | game server plugin for Source games |
| [gameWE-plugin-amxmodx](https://github.com/DennisPohlmann/gameWE-plugin-amxmodx) | game server plugin for GoldSrc games |

## Building

```bash
make test vet     # tests and static checks
make package      # dist/gamewe-collector-<version>-linux-amd64.tar.gz (+ .sha256)
```

Requirements, integration tests and the release process:
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).

## Credits

The Counter-Strike 2 support follows HLstatsX:CE by lovasatt
(https://github.com/lovasatt/hlstatsx-community-edition, GPL-2.0-or-later):
its CS2 game definitions and its handling of CS2's HTTP logs were the
reference for gameWE's Source 2 engine. The other games' default data comes
from HLstatsX:CE (https://github.com/A1mDev/hlstatsx-community-edition).
Thanks to both projects.

## License

GPL-3.0-or-later, see [LICENSE](LICENSE). The third-party components (Go modules in
the binary, the DB-IP database, the Docker base images) and their licenses are
in [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) and `licenses/`
(installed in `/opt/gamewe/collector/doc/`).

The bundled GeoIP database is DB-IP City Lite (CC BY 4.0): pages that show
the locations must credit it, e.g. "IP Geolocation by DB-IP" linked to
https://db-ip.com ([docs/CONFIG.md](docs/CONFIG.md#geoip-database-player-and-server-locations)).
