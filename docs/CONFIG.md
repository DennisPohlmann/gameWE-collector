# gameWE - collector: configuration

The collector has three sources of settings:

| What | Where | Changed by |
|------|-------|------------|
| UDP listen address, database connection | config file `/etc/gamewe/collector/config.yaml` | the admin of the host; restart needed |
| Game servers | `hlstats_Servers` | the web admin; picked up within a minute |
| Points, commands, privacy switches | `hlstats_Options` (keys `gamewe_*`) | the web admin; picked up within a minute |
| Rewards and weapon modifiers | `hlstats_Actions`, `hlstats_Weapons` | the web admin; picked up within 10 minutes (cache) |

`gamewe run` re-reads `hlstats_Servers` and `hlstats_Options` every minute
(`-reload` changes the interval). `gamewe check` shows what it finds there.

Fixed (not configurable): the uniqueId format is HLstatsX
style (`STEAM_1:0:123` → `0:123`), the plugin command prefix is `gamewe_`, the
database connection uses utf8mb4, and the queue holds 4096 lines per server.

## Config file

YAML, default path `/etc/gamewe/collector/config.yaml` (`-config` overrides it).
Unknown keys are an error. Keys of an older format (`servers`, `stats`,
`commands`, `log`, `listen.udp`, ...) are rejected with a hint where the setting
went. The file contains the database password, so keep it at `640 root:gamewe`.

### listen

Where the game servers send their logs (`logaddress_add <ip>:<port>`). Both keys
are optional.

| Key | Default | Meaning |
|-----|---------|---------|
| `address` | `0.0.0.0` | IP to listen on (`0.0.0.0` = all IPv4, `::` = all) |
| `port` | `41337` | UDP port |
| `public_address` | – | `ip:port` the game servers send their logs to, registered with `logaddress_add` (`features.log_address`). Default: the collector's address of the RCON connection to the game server, with `port` (or the port of `-listen`). Set it when the servers reach the collector at another address (NAT, several IPs). |

`gamewe run -listen IP:PORT` overrides `address` and `port`, e.g. for a test.

### features

Optional jobs, all on by default. They need the game server's `rcon_password`
in `hlstats_Servers` (except `daily_awards`). The switches are only in the
config file (changing them needs a restart); the old key `gamewe_daily_awards` of
`hlstats_Options` is not read.

| Key | Default | Meaning |
|-----|---------|---------|
| `rcon_status.enabled` | `true` | RCON `status` to each server every `interval`, over one connection that stays open: fills the live view, `act_players`, `act_map`, `max_players` and the name if none is set also when no log lines arrive (empty server, collector restart); see DB-SEMANTICS.md, "RCON status". The old form `rcon_status: true` / `false` still works and means `enabled`. |
| `rcon_status.interval` | `10s` | how often, a duration (`10s`, `1m`) or seconds; at least `2s`. While a server does not answer, the wait doubles up to one minute. |
| `log_address.enabled` | `true` | RCON `logaddress_list` to each server every `interval`; if the collector's address (`listen.public_address` or automatic) is missing, `logaddress_add <ip:port>` and `log on`, logged once. Also `log on` if the server stopped logging. Addresses of other tools are left alone. The old form `log_address: true` / `false` still works and means `enabled`. |
| `log_address.interval` | `1m` | how often, a duration (`30s`, `5m`) or seconds; at least `10s`. Only `logaddress_list` is sent each time (`logaddress_add` only when the address is missing), so a game server that restarted without the collector in its `server.cfg` sends logs again within this time. A shorter interval loses fewer log lines after such a restart, a longer one sends fewer RCON commands. |
| `log_secret.enabled` | `auto` | the log secret (`sv_logsecret`) in the log packets, see "Log secret" below. `false`: not checked (a secret in the packet is cut off). `true`: a server with `hlstats_Servers.log_secret` set must send exactly that secret, other packets are rejected; you set `sv_logsecret` on the game server yourself. `auto`: as `true`, and the collector sets and rotates `sv_logsecret` itself over RCON (Source servers with an `rcon_password`). The short form `log_secret: auto` (or `true` / `false`) works too. |
| `log_secret.interval` | `24h` | `auto`: a new secret this often, a duration (`12h`, `30m`) or seconds; at least `1m` |
| `daily_awards.enabled` | `true` | decide the daily awards of the previous day (DB-SEMANTICS.md) every day at `time`; `gamewe awards -date YYYY-MM-DD` works also when it is off. The old form `daily_awards: true` / `false` still works and means `enabled`. |
| `daily_awards.timezone` | `UTC` | IANA time zone (`UTC`, `Europe/Berlin`, ...) of `time` and of the day: the awards of a day count from 00:00 to 24:00 in this zone (23 or 25 hours on daylight saving days) |
| `daily_awards.time` | `"00:00"` | when the previous day is decided, `HH:MM` in `timezone` (on the day summer time starts, a time that does not exist runs an hour later) |

### `geoip`

| Key | Default | Meaning |
|-----|---------|---------|
| `path` | `/var/lib/gamewe/collector/dbip-city-lite.mmdb` | the city database (`.mmdb`): the DB-IP City Lite file shipped in the packages, or e.g. `/var/lib/GeoIP/GeoLite2-City.mmdb` (MaxMind, then `auto_update: false`); wins over `gamewe_geoip_db` |
| `auto_update` | `true` | replace `path` with the new month's DB-IP City Lite (and download it at start when missing) (checked daily until it is out), through `HTTPS_PROXY` if set; size-limited, checked, replaced in one step. Another database at `path` (MaxMind) is not overwritten. `false` and no file: no locations, one warning. `gamewe geoip-update` downloads by hand |

The RCON load data of `hlstats_server_load` (CPU, FPS, uptime) are switched with
`gamewe_server_load` in `hlstats_Options`.

### database

| Key | Default | Meaning |
|-----|---------|---------|
| `type` | `mariadb` | `mariadb` or `mysql` (the same), `postgres` (also `postgresql`, `pgsql`) |
| `host` | – | host name or IP of the database server |
| `port` | `3306` / `5432` | default by `type`: 3306 for MariaDB / MySQL, 5432 for PostgreSQL |
| `user`, `name` | – | database user and database name |
| `password` | – | password, or: |
| `password_file` | – | file containing the password (one of the two) |
| `max_open_conns` | `8` | connection pool size, shared by all game servers |
| `connect_timeout` | `10` | seconds |
| `tls.mode` | `disable` | `disable`, `require`, `verify-ca`, `verify-full`, see DATABASE.md. Set `verify-full` for a database on another host. |
| `tls.ca_file` | system CAs | CA certificate(s), PEM |
| `tls.cert_file`, `tls.key_file` | – | client certificate and key, PEM (set both or neither) |
| `tls.server_name` | `host` | name expected in the server certificate |

With `mode: disable`, setting any certificate option is an error. This stops a
config from looking encrypted when it is not.

## GeoIP database (player and server locations)

The collector fills the locations of players (country, flag, city, state,
coordinates) and of the game servers from a MaxMind-format city database
(`.mmdb`). **It comes with one:** the free DB-IP "IP to City Lite" database
is part of the tar.gz and the Docker image, in one place:
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

## Command-line flags of `gamewe run`

| Flag | Default | Meaning |
|------|---------|---------|
| `-config` | `/etc/gamewe/collector/config.yaml` | config file |
| `-listen` | `listen:` in the config | UDP address, overrides the config |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` (to stderr / the journal) |
| `-reload` | `1m` | how often `hlstats_Servers` and `hlstats_Options` are re-read |

## Game servers (hlstats_Servers)

Every row of `hlstats_Servers` is a game server the collector accepts logs from.
**Rows are created in the web admin; the collector never creates them.** Only
`gamewe simulate` adds its own test server.

| Column | Meaning |
|--------|---------|
| `address` | IP of the game server (an IP address, not a host name) |
| `port` | game port: the source port of the log packets, and the RCON port |
| `game` | `hlstats_Games.code`; decides which `hlstats_Players` rows its players get |
| `name` | display name |
| `rcon_password` | RCON password for replies to in-game commands; empty = no replies. |

Rows whose address is not an IP, whose port is invalid or whose game is not in
`hlstats_Games` are ignored, with one warning in the log. A removed row stops
its worker: the visits of its players are ended. A changed address, port or
game restarts the worker. A changed name or RCON password is applied directly.

### How packets are attributed to a server

The source `IP:port` of the packet must equal a row's `address` and `port`.
There is no match by IP alone: several game servers on one host send from the
same IP, and a server without its own row must not end up in the ranking of
another server on that host. Such packets are rejected with `known server IP
but a port that is not in hlstats_Servers`; add the server in the web admin.

Anything else is rejected and logged: the first time per source, then every
1000th time. Use a firewall on the UDP port to keep strangers from spoofing
packets; the log secret (next section) also stops packets with a forged
source address.

### Log secret

With `sv_logsecret <n>` a Source game server sends its log packets as
`0x53` (`S<n>L ...`) instead of `0x52` (`RL ...`), see INTERFACE.md. With
`features.log_secret.enabled` `true` or `auto` the collector compares that
secret with `hlstats_Servers.log_secret` of the server the packet's address
belongs to (constant time; the secret is never logged):

- **No secret stored** (`log_secret` empty): the server's packets are not
  checked. Logged once, and `gamewe check` warns (`log_secret enabled, server
  X has no secret`); in `auto` only for servers without `rcon_password`
  (the others get one within a `log_address.interval`).
- **GoldSrc servers** (cstrike, valve, dod, tfc, ns, ... by the game's engine,
  GAMES.md) are never checked and never sent `sv_logsecret`; logged once as a
  note. (Assumption: HLDS has no `sv_logsecret`; the collector also stops
  managing a Source server whose answer to `sv_logsecret` is not a cvar value.)
- **`true`**: a packet without the secret, or with another one, is rejected
  (`missing or wrong log secret`, logged the first time per source and
  reason, then every 1000th time). Set the same value in the game server's
  `server.cfg` (`sv_logsecret <n>`) and with `gamewe server set -logsecret <n>`
  or in the web admin. After a change in `hlstats_Servers` (read every
  minute) the previous secret is still accepted for two `log_address`
  intervals, at least two minutes.
- **`auto`** (default): as `true`; in addition, every `log_address.interval`
  the collector asks each Source server with an `rcon_password` for
  `sv_logsecret`:
  - no secret stored yet, or the last one older than `log_secret.interval`:
    a new random number (`crypto/rand`) is first accepted, then set with
    `sv_logsecret <n>` and read back, and only then stored in
    `log_secret` and `log_secret_changed` (Unix time). If RCON or the database
    fails, the game server gets the old value back and nothing is stored. The
    previous secret stays valid for the grace time above, so packets on the
    way are not lost.
  - the game server has another value (it restarted, or someone changed it):
    the stored one is set again.
  - packets without the right secret are not dropped at once but held in a
    small quarantine per server (at most 2000 lines and 1 MiB, at most 30
    seconds; valid lines that arrive meanwhile wait behind them, so the order
    stays). The collector asks the server for `sv_logsecret` right away (at
    most every 10 seconds per server). If the server has no secret or another
    one, it restarted: the held lines are processed in order, the secret is
    set again, and for 5 seconds packets without it are accepted (still on the
    way). If the server has the expected secret, the packets were forged:
    they are dropped, valid lines go on, and a warning is logged (at most once a
    minute per server, with the number of lines). Without an answer
    (no RCON, timeout) the quarantine runs out after 30 seconds the same way.
    A full quarantine drops further packets without the secret.
  - A server without `rcon_password` is checked as with `true` when it has a
    secret (set by hand), and not at all otherwise.
  - Clearing `log_secret` (`gamewe server set -logsecret ""`) makes the
    collector set a new one; to stop checking a server, use `false`.

The secret travels in plain text in every UDP packet: it protects against
packets with a forged sender address from elsewhere, not against someone who
can read the traffic between game server and collector. Source reads
`sv_logsecret` as a number (a value that is not a number, or 0, sends no
secret), so secrets are numbers from 1 to 2147483647.

Every server has its own worker with its own queue and player state, so lines of
one server are processed in order, servers are processed in parallel, and the
same userid on two servers never mixes. Replies to in-game commands go over RCON
to the server the command came from, each server through its own connection.

## Settings in hlstats_Options

Keys starting with `gamewe_`. A missing key uses the default. An invalid value
keeps the default and is logged as a warning, as is an unknown `gamewe_*` key
(probably a typo). `gamewe db migrate` creates all keys with their defaults in
a new database (the reference data, `internal/schema/mysql/seed.sql`); the
seed only adds missing keys and never changes values that are already set.

| Key | Default | Meaning |
|-----|---------|---------|
| `gamewe_start_skill` | `1000` | skill of a new player |
| `gamewe_kill_points` | `5` | base skill per kill, scaled by victim/killer skill and `hlstats_Weapons.modifier` (at least 1) |
| `gamewe_headshot_bonus` | `0` | extra skill per headshot on top of the `headshot` action in `hlstats_Actions` (which rewards headshots, as in HLstatsX) |
| `gamewe_suicide_penalty` | `5` | skill lost per suicide |
| `gamewe_teamkill_penalty` | `10` | skill lost per teamkill |
| `gamewe_rank_bots` | `0` | Bots are always recorded (live view, server counts, their own statistics); this decides only the rankings. 0 = bots are (live view, server counts, their own statistics) but hidden from the rankings (`hideranking` = 1) and kills or player-vs-player actions with a bot change nobody's skill (no points, no headshot action, no kill streak); 1 = bots are ranked players like everybody else. The old key `gamewe_ignore_bots` is obsolete: a stored 1 is read as `gamewe_rank_bots` = 0 (and logged); delete the row |
| `gamewe_store_player_ip` | `1` | 1 = store IPv4 addresses in `hlstats_Events_Connects.ipAddress` (personal data: only `gamewe_web_admin` can read the column, not `gamewe_web`) |
| `gamewe_store_chat` | `1` | 1 = store chat in `hlstats_Events_Chat` (personal data: only `gamewe_web_admin` can read the table, not `gamewe_web`) |
| `gamewe_win_triggers` | `CTs_Win,Terrorists_Win,Target_Bombed,Target_Saved,Bomb_Defused,All_Hostages_Rescued,Hostages_Not_Rescued,VIP_Escaped,VIP_Assassinated,VIP_Not_Escaped,Round_Win` | log triggers that end a round with a winner (wins/losses; the bonus is `hlstats_Actions.reward_team`) |
| `gamewe_commands` | `1` | 1 = answer `!rank`, `!top10`, ... in game |
| `gamewe_menu_time` | `15` | seconds the panels (`!top10`, `!rank`, ...) stay open |
| `gamewe_rcon_timeout` | `5` | seconds for RCON connect, send and receive |
| `gamewe_log_lines` | `1` | 1 = write every received game-server log line to the collector's log (the journal), as `[name ip:port] L 10/22/2026 - …`. That can be many lines; see [INSTALLATION.md](INSTALLATION.md#g-troubleshooting). |
| `gamewe_idle_timeout` | `300` | seconds without any log line of a player after which the collector ends the visit, as for a disconnect but without an `hlstats_Events_Disconnects` row: the connection time up to now is stored and the live row deleted. Catches lost disconnect lines (server crash or restart, lost packets). A player who shows up again starts a new visit. Bots end after at most 3 minutes without a line (bot_quota kicks them without a disconnect line; RCON status keeps listed bots). 0 = never |
| `gamewe_command_display` | `panel` | how `!rank`, `!statsme`, `!session`, `!status` and `!next` are answered: `panel` = a menu panel on the left of the screen for `gamewe_menu_time` seconds (as gameME showed it), `chat` = private chat lines, `both` |
| `gamewe_command_prefix` | `gamewe` | prefix of the plugin's server commands: `gamewe` (the gameWE plugin also knows `gameme_`), or `gameme` for a server that still runs the original gameME plugin |
| `gamewe_raw_messages` | `0` | 1 = answer `!rank`, `!session`, `!top10` and `!next` with `gamewe_raw_message` (types 1, 4, 5, 6) for a plugin that shows them, instead of chat text ([INTERFACE.md](INTERFACE.md#queries-of-other-plugins-raw-messages)) |
| `gamewe_store_locations` | `1` | 1 = store the kill and death positions of the plugin's location lines in `hlstats_Events_Locations` (one row per position; the plugin's `gamewe_log_locations` must be 1) |
| `gamewe_geoip_db` | `/var/lib/gamewe/GeoLite2-City.mmdb` | **Legacy.** Used only when config.yaml has no `geoip.path` and the value is not this old default; otherwise the DB-IP database of `geoip.path` is used ([GeoIP database](#geoip-database-player-and-server-locations)). Old meaning: MaxMind-format city database for player and server locations, skipped with one warning if missing |
| `gamewe_server_load` | `1` | 1 = write a row of `hlstats_server_load` every 10 minutes per server (DB-SEMANTICS.md) |
| `gamewe_heartbeat` | – | **written by the collector** (Unix time, every 60 s), not a setting: the web and `gamewe reset` see from it whether a collector runs |

`gamewe_default_game` and `gamewe_page_size` are settings of the web frontend;
the collector ignores them.
