# gameWE - collector

gameWE is a statistics system for Source and GoldSrc game servers
(Counter-Strike: Source, CS:GO, CS 1.6, TF2, ...), in the style of gameME /
HLstatsX. The **collector** is its core: one static Go binary that

- receives the logs of many game servers at once over UDP (`logaddress_add`);
  only servers registered in `hlstats_Servers` are accepted, on Source servers
  with a log secret (`sv_logsecret`) that the collector sets and rotates over RCON,
- tracks connects, disconnects, team changes, kills (weapon, headshot), suicides,
  teamkills, round wins and team scores, plus per-weapon stats from the gameWE plugins,
- writes players, kills, skill, rounds, weapon and action statistics into an
  **HLstatsX database** (the gameME-style HLstatsX schema) on **MariaDB / MySQL**
  or **PostgreSQL**, with optional TLS that never falls back to plaintext,
- answers the in-game commands **!rank, !top10, !status, !session, !hideranking**
  over RCON through the gameWE SourceMod plugin (AMX Mod X plugin on GoldSrc),
- runs daily awards, ranking resets queued by the web admin, GeoIP locations
  (DB-IP database included) and the RCON status of every server.

The config file holds only the UDP listen address and the database connection.
Game servers and all point and command settings come from the database, so
they are managed in the web admin; the collector re-reads them every minute,
without a restart.

Current version: see `VERSION` and [CHANGELOG.md](CHANGELOG.md).

## Requirements

- Linux x86_64 (static binary) or a container runtime (Docker / Podman)
- MariaDB 11.4 or 12.3, or PostgreSQL 17 or 18 (tested in CI)
- Game servers with a [supported game](#supported-games) that can send their
  logs over UDP to the collector

## Supported games

| Engine | Games | Logs | In-game replies |
|--------|-------|------|-----------------|
| Source | CS:S, CS:GO, TF2, DoD:S, HL2:DM, L4D, L4D2, Insurgency | UDP | gameWE SourceMod plugin |
| GoldSrc | CS 1.6, DoD, TFC, HL1, NS | UDP | gameWE AMX Mod X plugin |

Counter-Strike 2 is not supported: it sends no UDP logs and SourceMod does
not run on it. Details per game: [docs/GAMES.md](docs/GAMES.md).

## Quick start

1. Install the collector as a container (`ghcr.io/dennispohlmann/gamewe-collector`,
   host network) or from the release archive
   `gamewe-collector-<version>-linux-amd64.tar.gz` with systemd.
2. Create the database and its users with `schema/init/init-db.sh` (MariaDB)
   or `schema/postgres/init/init-db.sh` (PostgreSQL).
3. Put the database connection into `/etc/gamewe/collector/config.yaml`,
   create the tables with `gamewe db migrate` (or let the first start do it)
   and check with `gamewe check`. The schema is built into the binary: after
   an update the collector migrates the database at start
   (`database.auto_migrate`, the default; make a backup before updating), or
   with `auto_migrate: false` refuses to start until `gamewe db migrate` ran
   ([docs/DATABASE.md](docs/DATABASE.md#schema-versions-and-upgrades)).
4. Register the game servers (web admin or `gamewe server add`) and add to
   their `server.cfg`:

   ```
   log on
   logaddress_add 203.0.113.5:41337     // the collector's IP and UDP port
   rcon_password "change-me"            // the same as in hlstats_Servers
   ```

Every step in detail: [docs/INSTALLATION.md](docs/INSTALLATION.md).

## Documentation

| File | Content |
|------|---------|
| [docs/INSTALLATION.md](docs/INSTALLATION.md) | installation step by step: container, release archive, database, game servers, update, removal, troubleshooting |
| [docs/CONFIG.md](docs/CONFIG.md) | config file, GeoIP database, game servers and the settings in the database (`hlstats_Options`) |
| [docs/USAGE.md](docs/USAGE.md) | in-game commands, points, the `gamewe` command line |
| [docs/INTERFACE.md](docs/INTERFACE.md) | log lines of the plugins, RCON reply commands, raw messages |
| [docs/DATABASE.md](docs/DATABASE.md) | database users, rights, TLS, incomplete schemas |
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

## License

GPL-3.0-or-later, see [LICENSE](LICENSE). The third-party components (Go modules in
the binary, the DB-IP database, the Docker base images) and their licenses are
in [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) and `licenses/`
(installed in `/opt/gamewe/collector/doc/`).

The bundled GeoIP database is DB-IP City Lite (CC BY 4.0): pages that show
the locations must credit it, e.g. "IP Geolocation by DB-IP" linked to
https://db-ip.com ([docs/CONFIG.md](docs/CONFIG.md#geoip-database-player-and-server-locations)).
