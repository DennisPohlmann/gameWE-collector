# gameWE - collector

gameWE is a statistics system for Source and GoldSrc game servers
(Counter-Strike: Source, CS:GO, CS 1.6, TF2, ...), in the style of gameME /
HLstatsX. The **collector** is its core: one static Go binary that

- receives the logs of many game servers at once over UDP (`logaddress_add`);
  only servers registered in `hlstats_Servers` are accepted,
- tracks connects, disconnects, team changes, kills (weapon, headshot), suicides,
  teamkills, round wins and team scores, plus per-weapon stats from the gameWE plugin,
- writes players, kills, skill, rounds, weapon and action statistics into an
  **HLstatsX database** (the gameME-style HLstatsX schema) on **MariaDB / MySQL**
  (`schema/hlstatsx.sql`) or **PostgreSQL** (`schema/postgres/hlstatsx.sql`),
  with optional TLS that never falls back to plaintext, so the existing HLstatsX
  web pages keep working,
- answers the in-game commands **!rank, !top10, !status, !session, !hideranking**
  over RCON through the gameWE SourceMod plugin (AMX Mod X plugin on GoldSrc),
- runs daily awards, ranking resets queued by the web admin, GeoIP locations
  (DB-IP database included) and the RCON status of every server.

The config file holds only the UDP listen address and the database connection.
Game servers (`hlstats_Servers`) and all point and command settings
(`hlstats_Options`, `hlstats_Actions`, `hlstats_Weapons`) come from the database,
so they are managed in the web admin. The collector re-reads them every minute,
without a restart. It runs as a systemd service.

Current version: see `VERSION` and [CHANGELOG.md](CHANGELOG.md). Built with Go
1.27; tested with MariaDB 11.4 and 12.3 and PostgreSQL 17 and 18.

## Contents

1. [Installation](#installation)
2. [Supported games](#supported-games)
3. [Game server setup](#game-server-setup)
4. [In-game commands](#in-game-commands)
5. [Plugin interface](#plugin-interface)
6. [Points](#points)
7. [Command line](#command-line)
8. [GeoIP](#geoip-player-and-server-locations)
9. [Troubleshooting](#troubleshooting)
10. [Building](#building)
11. [License](#license)

More documentation (in the package in `/opt/gamewe/collector/doc/`):

| File | Content |
|------|---------|
| [INSTALLATION.md](INSTALLATION.md) | installation step by step: container, release archive, database, game servers, update, removal |
| [docs/INSTALL-ARCH.md](docs/INSTALL-ARCH.md) | Arch Linux package |
| [docs/CONFIG.md](docs/CONFIG.md) | config file and the settings in the database (`hlstats_Options`) |
| [docs/DATABASE.md](docs/DATABASE.md) | database users, rights, TLS, incomplete schemas |
| [docs/DB-SEMANTICS.md](docs/DB-SEMANTICS.md) | what the collector writes into which table (for the web frontend) |
| [docs/GAMES.md](docs/GAMES.md) | games, engines, log formats, plugins |
| [CHANGELOG.md](CHANGELOG.md) | changes per version |
| [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) | third-party components and their licenses |

## Installation

**[INSTALLATION.md](INSTALLATION.md)** has every step. In short:

- **Container:** `ghcr.io/dennispohlmann/gamewe-collector` (distroless, non-root,
  host network so the game servers' source addresses are kept),
  `docker-compose.example.yml` with an optional MariaDB or PostgreSQL.
- **Release archive:** `gamewe-collector-<version>-linux-amd64.tar.gz` is a tree
  for `/` (`etc/`, `opt/`, `var/`); copy it, create the user `gamewe`,
  `config.yaml` from the example, `systemctl enable --now gamewe-collector`.
  Arch Linux: `packaging/arch/PKGBUILD`, [docs/INSTALL-ARCH.md](docs/INSTALL-ARCH.md).
- **Database:** `schema/init/init-db.sh` (MariaDB) or
  `schema/postgres/init/init-db.sh` creates database, users, tables, reference
  data and rights (there are no migrations); `gamewe check` says whether the
  schema is complete.

Every release has a `.sha256` file next to the archive. The installed version:
`gamewe -V`.

## Supported games

| Engine | Games | Logs | In-game replies |
|--------|-------|------|-----------------|
| Source | CS:S, CS:GO, TF2, DoD:S, HL2:DM, L4D, L4D2, Insurgency | UDP | gameWE SourceMod plugin |
| GoldSrc | CS 1.6, DoD, TFC, HL1, NS | UDP | gameWE AMX Mod X plugin |

Counter-Strike 2 is not supported: it sends no UDP logs and SourceMod does
not run on it.

**CS:GO:** kills with positions, headshots and the kill flags (wallbang,
no-scope, through smoke, blind) as actions, assists and flash assists, bomb
deaths, match accolades, bomb and hostage actions, round MVPs, round wins,
roles (player models), and with the gameWE SourceMod plugin shots, hits,
damage and hit areas per weapon. Details: [docs/GAMES.md](docs/GAMES.md#csgo).
Reference data for every game is in the initial database.

## Game server setup

Add this to `server.cfg` (CS:S, CS:GO, TF2 and other Source games; GoldSrc:
[INSTALLATION.md](INSTALLATION.md#d-game-servers)):

```
log on
mp_logdetail 3                       // log attacks, needed by some games for full detail
logaddress_add 203.0.113.5:41337     // the collector's IP and listen port
rcon_password "change-me"            // the same as rcon_password in hlstats_Servers
```

On CS:GO, `logaddress_add` needs quotes around the address.

Then register the server in the web admin, i.e. as a row of `hlstats_Servers`:
its **IP** (`address`), **game port** (`port`), **game** (an `hlstats_Games`
code) and **RCON password** (`rcon_password`). The collector picks it up within a
minute, without a restart.

The IP and port must be where the log packets come from, which is normally the
game port. Packets from any source that is not in `hlstats_Servers` are rejected
and show up as `rejected log packet` in `journalctl -u gamewe-collector`, together
with the source address. Log secrets (`sv_logsecret`) are not used: the server
list decides who may send. Restrict the UDP port with a firewall to your game
servers. One collector handles many servers at once: each server has its own
worker, and replies always go back to the server where the command was typed.
See [CONFIG.md](docs/CONFIG.md#how-packets-are-attributed-to-a-server).

Then install the **gameWE SourceMod plugin** on the game server. It provides the
reply commands and writes the extra log lines, see [Plugin interface](#plugin-interface).

Test it from the collector host:

```bash
# send the built-in sample game as UDP packets to the running collector; the
# simulated server 127.0.0.1:27015 is added to hlstats_Servers if needed
/opt/gamewe/collector/bin/gamewe simulate -send 127.0.0.1:41337
```

## In-game commands

Players type these in chat (`!` or `/` prefix):

| Command    | Answer (private: a panel on the left of the screen via `gamewe_msay`, as gameME showed it, or chat lines via `gamewe_psay`, see `gamewe_command_display`; with `gamewe_raw_messages` = 1 `!rank`, `!next`, `!session` and `!top10` are sent as `gamewe_raw_message` for a message plugin instead) |
|------------|------------------------------------------------------------------------|
| `!rank`, `!statsme`, `!session`, `!status` | panel *1 - Total*: position, points, frags (K:D), headshots, accuracy, time played; *2 - Session*: positions and points won or lost, frags, headshots, accuracy, favourite weapon, time of this visit. In chat: *You are ranked #3 of 50 with 1012 points.* (`!rank`), the visit (`!session`, `!statsme`), the totals (`!status`) |
| `!next`    | the player ranked just above you and how many points you are behind    |
| `!top10`   | menu with the 10 best players (`gamewe_msay`)                          |
| `!hideranking [1\|0]` (also `/gamewe_hideranking`) | hide yourself from the ranking (`1`, `on`) or show yourself again (`0`, `off`); without an argument it switches. A private chat line says the new state. At most one change per 10 seconds. Players hidden by an admin or banned cannot change it. Stored as `hideranking` 3 (DB-SEMANTICS.md) |
| `!settings` (also `/gamewe_settings`) | your settings in chat: whether you are shown in the ranking, and how to change it. The SourceMod plugin opens a menu for `!gamewe_settings` instead |
| `!help`    | the list of commands                                                   |

The commands are read from the `say` lines in the log. The plugin logs them
(always with the verb `say`) even when it hides them from chat. Its menu sends
them with a `/` prefix (`/rank`), and both prefixes work. The plugin's other menu
commands (`/clans`, `/weapons`, `/accuracy`, `/targets`, `/kills`, `/cheaters`,
`/gamewe_reset`, `/gamewe_auto`, `/gamewe_display`, `/gamewe_chat`) are ignored
for now. `!hideranking` and `!settings` need the `!` or `/` prefix (a chat
line that only contains the word does nothing).

## Plugin interface

Input is standard HL2 log lines over UDP, for example:

```
L 10/22/2026 - 12:00:00: "Name<2><STEAM_1:0:123><CT>" killed "Other<3><STEAM_1:1:456><TERRORIST>" with "ak47" (headshot)
L 10/22/2026 - 12:00:05: "Name<2><STEAM_1:0:123><CT>" say "!rank"
```

The gameWE plugin adds these lines:

```
"Name<2><STEAM_1:0:123><CT>" triggered "weaponstats" (weapon "ak47") (shots "14") (hits "6") (kills "1") (headshots "1") (tks "0") (damage "412") (deaths "0")
"Name<2><STEAM_1:0:123><CT>" triggered "weaponstats2" (weapon "ak47") (head "1") (chest "3") (stomach "1") (leftarm "0") (rightarm "1") (leftleg "0") (rightleg "0")
```

and, for CS:S and CS:GO, `triggered "headshot"` for every headshot kill (the
collector rewards one `headshot` action per kill, not two), position lines
(`located on "kill" (position "x y z") against ... (victim_position "x y z")`,
stored in `hlstats_Events_Locations`), player models (`changed role to "gign"`),
`triggered "mvp"` / `"clantag"` and the queries of other plugins (see below).

Field order: `weaponstats` = weapon, shots, hits, kills, headshots, tks, damage,
deaths. `weaponstats2` = weapon, head, chest, stomach, leftarm, rightarm, leftleg,
rightleg. The plugin flushes them when a player dies or disconnects and at round
end (L4D, L4D2 and Insurgency also every 180 seconds). Each line therefore holds
the counts since the previous line, and the collector adds them up per player and
weapon. TF2 writes no `weaponstats2`.

Replies are console commands that the collector sends over **Source RCON** (TCP,
to the server's `address` and `port` with `rcon_password` from `hlstats_Servers`).
The plugin offers them. The collector uses the `gamewe_` prefix, which the gameWE
plugin accepts besides `gameme_`; for a server with the original gameME plugin set
`gamewe_command_prefix` to `gameme`.

| Command | Used for |
|---------|----------|
| `gamewe_psay <userid[,userid...]> [colored] "<msg>"` | private chat line (`!rank`, `!next`, `!status`, `!session`, `!help`) |
| `gamewe_msay <time> <userid> [handler] "<menu text, \n separated>"` | `!top10` menu |
| `gamewe_csay "<msg>"` | center text to all (not used yet) |
| `gamewe_tsay <time> <userid> "<msg>"` | top text (not used yet) |
| `gamewe_hint <userid[,..]> "<msg>"`, `gamewe_khint <userid[,..]> "<msg>"` | hint boxes (not used yet) |
| `gamewe_browse <userid> "<url>"` | open a URL (not used yet) |
| `gamewe_raw_message <type> <query_id> <userid> ...` | answers to other plugins' queries, and with `gamewe_raw_messages` the chat command answers (below) |

Players are addressed by the **userid**, the number in the first `<..>` of the
player reference (`2` in `"Name<2><STEAM_1:0:123><CT>"`). Message text is
sanitized before it goes into an RCON command. Double quotes become `'`,
semicolons become `,`, and control characters are removed. This means a player
name can never inject further console commands. Menu lines are joined with a
literal `\n` (backslash, n), which the plugin turns into line breaks.

### Queries of other plugins (raw messages)

Other SourceMod plugins ask for statistics through the plugin's natives
(`QueryGameWEStats`, `QueryGameWEStatsTop10`, `QueryGameWEStatsNext`,
`QueryIntGameWEStats`, and the gameME names). The plugin logs
`"Name<2><...>" requested "playerinfo" (value "17")` (or `"Server" requested
"top10" ...`) and the collector answers with the same `query_id`, in the field
order the plugin (gameWE and gameME) reads:

| Request | Answer |
|---------|--------|
| `playerinfo` | `gamewe_raw_message 101 <query_id> <userid>` and 45 values: rank, players, skill, kills, deaths, kpd, suicides, headshots, hpk, accuracy, connection time (s), kill assists, kills assisted, points healed, flags captured, custom wins, best kill streak, best death streak; then the same for this visit (rank change since the start of the visit, skill change, ..., time in s, favourite weapon of the visit or `-`); then rank, players, skill, kills, deaths, kpd, headshots, hpk once more as the "global" values (gameWE has no network-wide ranking, so they are the game's); then the country code (`""` if unknown) |
| `top10` | `gamewe_raw_message 102 <query_id> -1` and per player skill, "name", kpd, hpk (best first; nothing if nobody is ranked) |
| `next` | `gamewe_raw_message 103 <query_id> <userid>` and per player rank, skill, "name", kpd, hpk: the 5 players ranked directly above, best first |
| `spectatorinfo` | `gamewe_raw_message 1001 <query_id> <userids of all players> <userid> "<text>"`: the text shown to who spectates the player |

kpd is kills/deaths, hpk headshots/kills (a ratio), accuracy hits/shots in
percent, all with two decimals. Names are shortened so that a command stays
below the engine's 512-character limit. With `gamewe_raw_messages` = 1 the
chat commands are answered the same way with the types 1 (`!rank`), 4
(`!session`), 5 (`!top10`) and 6 (`!next`) and query id 0, for a plugin that
shows them itself (the forwards `onGameWEStatsRank`, ...).

The RCON connection to each server is kept open and reopened after errors.
Replies are sent in the background, so a game server that is down never delays
the statistics.

## Points

Points are the HLstatsX **skill** (`hlstats_Players.skill`). Rankings are per
game (`hlstats_Players.game`) and leave out players with `hideranking`.

The values in `code style` are `hlstats_Options` keys (defaults in
[CONFIG.md](docs/CONFIG.md#settings-in-hlstats_options)):

- New players start with `gamewe_start_skill` (1000).
- **Kill:** the killer wins `round(gamewe_kill_points × weapon modifier × victim skill / killer skill)`,
  at least 1 and at most 4 × `gamewe_kill_points`. The modifier is `hlstats_Weapons.modifier`.
  The victim loses the same amount.
  A headshot also triggers the `headshot` action (`hlstats_Actions`, +1 by default),
  once per kill even though the plugin logs its own headshot line.
- **Kill streak:** when a streak of n ≥ 2 kills in one life ends (death,
  suicide, disconnect, round end, map change), the action `kill_streak_<n>`
  (n at most 12), as HLstatsX does: one reward per streak.
- **Teamkill:** the killer loses `gamewe_teamkill_penalty`, the victim loses nothing.
- **Suicide:** `gamewe_suicide_penalty`. Not within 2 s after the player changed
  teams: CS:S logs a suicide when a living player switches teams.
- **Player actions** (bomb planted, flag captured, ...): `reward_player` of the
  action in `hlstats_Actions`. Player-vs-player actions (domination, revenge):
  the player gets `reward_player`, the victim loses it.
- **Round won:** every player of the winning team gets `reward_team` of the
  win trigger's team action in `hlstats_Actions`, and a win; the other team a loss.
- Skill never drops below 0. Bots are recorded but not ranked, and kills with
  bots change nobody's skill (`gamewe_rank_bots`).
- VAC-banned players (disconnect reason) and permanent bans (`Banid: ... "permanently"`)
  are hidden from the rankings (`hideranking` = 2).

What the collector writes into which table, for anyone reading the database
(e.g. the web frontend), is in [DB-SEMANTICS.md](docs/DB-SEMANTICS.md).

Daily awards (most kills with a weapon, most actions, most kills, longest play
time, ...) are decided every day for the previous day, by default at 00:00
UTC; time and time zone are `features.daily_awards` in the config file; see
[DB-SEMANTICS.md](docs/DB-SEMANTICS.md#daily-awards-ribbons-ranks). To decide a day
again (after changing the awards in the web admin):

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe awards -date 2026-10-21
```

## Command line

`gamewe <command>`; every command takes `-config FILE` (default
`/etc/gamewe/collector/config.yaml`), `gamewe help` lists them all.

| Command | What it does |
|---------|--------------|
| `gamewe run` | the collector (systemd starts it) |
| `gamewe check` | config, database connection and TLS, schema completeness, game servers and settings |
| `gamewe -V` / `--version` / `version` | the version |
| `gamewe status`, `gamewe logaddress`, `gamewe rcon` | RCON status, log address registration, any RCON command (below) |
| `gamewe game`, `gamewe server`, `gamewe option` | games, servers and settings like the web admin (below) |
| `gamewe reset GAME -mode stats\|all` | reset a ranking (below) |
| `gamewe awards -date YYYY-MM-DD` | decide the daily awards of a day again |
| `gamewe geoip-update` | download the DB-IP database now |
| `gamewe simulate` | replay a sample game into the database or to a running collector |

### RCON jobs and checks by hand

With the game server's `rcon_password` in `hlstats_Servers` the collector talks
to every server over RCON (switches in `features:` of the config file, all on by
default):

- **status** every 10 seconds (`features.rcon_status.interval`): players and
  bots in the live view and `act_players` (also on an empty server and right
  after a collector restart, without any log line), the map, maximum players,
  the name if none is set, pings. One connection per server stays open.
- **log address** every minute: `logaddress_list`; if the collector is missing,
  `logaddress_add <ip:port>` and `log on`. The address is
  `listen.public_address`, or the collector's address of the RCON connection
  with the listen port. So a game server that restarted without
  `logaddress_add` in its config is back within a minute.
- **stats** every 10 minutes for `hlstats_server_load` (`gamewe_server_load`).

Everything can be checked by hand (`-config` as for `run`):

```bash
gamewe status                                 # RCON status of all servers, as the collector reads it
gamewe status -server 3                       # one server: serverId or ip:port
gamewe logaddress                             # is the collector registered on every server?
gamewe logaddress -add                        # register it where it is missing
gamewe logaddress -server 198.51.100.7:27015 -address 203.0.113.4:41337 -add
gamewe rcon -server 3 sv_maxplayers           # any RCON command, prints the answer
gamewe awards -date 2026-10-21                # decide the daily awards of a day again
```

### Games, servers and settings

The `gamewe` binary also changes the database like the web admin does (with
the collector's database user; `-config` as for `run`, `-dry-run` shows what
would change). The collector picks up every change within 60 seconds.

```bash
gamewe game list                                   # games (rankings): realgame, engine, servers, players
gamewe game add css3 "CS:S GunGame" -template css  # a ranking with copies of css's teams, roles, actions,
                                                   #   weapons, awards, ranks and ribbons (and its realgame)
gamewe game add css3 "CS:S GunGame" -template css  # again: adds only what is missing
gamewe game hide css                               # hide / show in the web's game list
gamewe game delete css3                            # only a game without servers and players
gamewe server list                                 # the RCON password only as set / -
gamewe server add 198.51.100.7:27315 -game css3 -name "GunGame" -rcon-password-file /root/rcon.txt
gamewe server set 198.51.100.7:27315 -game css1    # or the serverId; only the given fields change
gamewe server remove 3                             # players and statistics are kept
gamewe option list                                 # the collector settings with values and defaults
gamewe option get gamewe_command_display
gamewe option set gamewe_command_display both      # gamewe_* values are checked
```

### Resetting a ranking

```bash
gamewe reset css3 -mode stats -dry-run   # rows per table that would be deleted / updated, nothing changes
gamewe reset css3 -mode stats            # players stay, back to the start skill, statistics empty
gamewe reset css3 -mode all              # the players of css3 are deleted too
gamewe reset -list                       # the latest resets and their results
```

Games, servers, teams, weapons, actions, awards and settings stay; other
games are not touched. It asks for the game code to confirm (`-yes` skips
that). With a running collector (table `hlstats_gamewe_jobs` present, heartbeat
less than 2 minutes old) the reset is queued for it in `hlstats_gamewe_jobs` (as the web
admin does): it pauses the servers of that game between two log lines,
forgets their players in memory, resets, clears the live view and goes on;
`gamewe reset` waits for the result. Without one it runs directly (then no
collector may run: it keeps player ids in memory). A reset can be run again
if it was interrupted.

Codes are 1-10 characters `a-z 0-9 _`. `-rcon-password PW` works too but is
visible in the process list; `-rcon-password-file -` reads it from stdin. The
RCON password is never printed.

## GeoIP (player and server locations)

The collector fills the locations of players (country, flag, city, state,
coordinates) and of the game servers from a MaxMind-format city database
(`.mmdb`). **It comes with one:** the free DB-IP "IP to City Lite" database
is part of the tar.gz, the Arch package and the Docker image, in one place:
`/var/lib/gamewe/collector/dbip-city-lite.mmdb`. The collector replaces it
there with the new month's file (checked daily until it is out) and notices a
new file within 10 minutes. The journal says `GeoIP database loaded` and
`GeoIP database updated`.

Licence: DB-IP Lite is CC BY 4.0. Pages that show these locations must credit
DB-IP, e.g. with the link "IP Geolocation by DB-IP" to https://db-ip.com
(`/opt/gamewe/collector/doc/GEOIP-NOTICE`).

config.yaml:

```yaml
geoip:
  path: /var/lib/gamewe/collector/dbip-city-lite.mmdb   # default
  auto_update: true   # monthly update of that file (default)
```

The download honours `HTTPS_PROXY`; it is limited in size, checked (a city
database that opens) and replaces the old file in one step (a new file in the
same directory, renamed over it). A missing file is downloaded at start. A
`path` with another database (MaxMind GeoLite2) is never overwritten. With
`auto_update: false` and no file the collector runs without locations and
says so once in the journal. Update by hand:

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe geoip-update
```

For MaxMind's GeoLite2-City (free account, `geoipupdate`) set `path` to its
file and `auto_update: false`. `path` wins over `gamewe_geoip_db` of
`hlstats_Options` (a legacy setting, still read when config.yaml has no
`geoip.path` and it is not the old default `/var/lib/gamewe/GeoLite2-City.mmdb`).

With Docker the database is in the volume `/var/lib/gamewe` (owned by the
image's user `nonroot`, uid 65532; a host directory must be writable by it).
A new named volume gets the file from the image; a volume without it gets it
downloaded on start.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `gamewe check` fails with `certificate signed by unknown authority` | Set `database.tls.ca_file` to the CA that signed the database certificate. |
| `... certificate is valid for X, not Y` | Connect by the name in the certificate, set `tls.server_name`, or use `verify-ca`. |
| `server refused TLS connection` / `server does not support TLS` | TLS is not enabled on the database server, see DATABASE.md. gameWE never falls back to plaintext. |
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

## Building

You need Go (version in `go.mod`, currently 1.27), `make` and GNU tar.

```bash
make test vet                 # tests and static checks
make build                    # build/linux-amd64/gamewe
make package                  # dist/gamewe-collector-<version>-linux-amd64.tar.gz (+ .sha256)
make schema                   # after editing schema/hlstatsx.sql or schema/init/: regenerate schema/postgres/
make docker                   # container image gamewe-collector:<version>
```

The version comes from a `v*` git tag on HEAD, otherwise from `VERSION`. The
binary is static (`CGO_ENABLED=0`). GitHub Actions (`.github/workflows/build.yml`)
builds and tests every push to `main`: the unit tests, govulncheck,
actionlint, the integration tests against MariaDB 11.4 + PostgreSQL 17 and
MariaDB 12.3 + PostgreSQL 18 service containers, the amd64 package (kept as a
workflow artifact) and the container image, which it pushes to
`ghcr.io/dennispohlmann/gamewe-collector` with the tag `edge`.

### Releases

Releases are made by [semantic-release](https://semantic-release.gitbook.io/)
(`.releaserc.json`) in the same workflow; nobody tags by hand. To make a
release, push commits written as [Conventional Commits](https://www.conventionalcommits.org/)
to `main`. semantic-release reads the commits since the last tag and decides
the next version:

| Commit | Release |
|---|---|
| `feat: ...` | minor (0.1.0 → 0.2.0) |
| `fix: ...` (also `fix(security): ...`), `perf: ...` | patch (0.1.0 → 0.1.1) |
| `feat!: ...`, `fix!: ...` or a `BREAKING CHANGE:` footer | major (0.1.0 → 1.0.0) |
| `docs`, `chore`, `ci`, `build`, `test`, `refactor`, `style` | no release |

For a release it writes `VERSION`, `packaging/arch/PKGBUILD` and
`CHANGELOG.md`, commits them as `chore(release): vX.Y.Z [skip ci]`, tags
`vX.Y.Z` and creates the GitHub release. The workflow then builds that tag,
uploads the archive and its `.sha256` to the release and pushes the image tags
`<version>`, `<major>.<minor>` and `latest`. Never edit `VERSION` or
`CHANGELOG.md` by hand.

## License

GPL-3.0-or-later, see [LICENSE](LICENSE). The third-party components (Go modules in
the binary, the DB-IP database, the Docker base images) and their licenses are
in [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) and `licenses/`
(installed in `/opt/gamewe/collector/doc/`).
