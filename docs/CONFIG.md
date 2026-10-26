# gameWE - collector: configuration

The collector has three sources of settings:

| What | Where | Changed by |
|------|-------|------------|
| Log inputs (UDP, HTTP), database connection | config file `/etc/gamewe/collector/config.yaml` | the admin of the host; restart needed |
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
`commands`, `log`, `listen.http_port`, `listen.udp` as an address, ...) are rejected with a hint where the setting
went. The file contains the database password, so keep it at `640 root:gamewe`.
`gamewe config test` (also `gamewe -t`) checks the file without starting
anything and lists every error and warning with its line
([USAGE.md](USAGE.md#checking-the-config-file)). `gamewe run` and every
other command that reads the file stop on an invalid one and list all its
errors the same way, each with its line (`gamewe run` exits with 78).
A key set twice in the same section is an error too.

### listen

Where the game servers send their logs. There are two inputs; at least one must
be on, and both can run at the same time.

- `listen.udp`: GoldSrc and Source servers (`logaddress_add <ip>:<port>`). On
  by default.
- `listen.http`: Counter-Strike 2 (Source 2), which logs only over HTTP
  (`logaddress_add_http "http://<ip>:<port>/<game port>"`). Off by default.
  CS2 sends plain HTTP; HTTPS is not supported by the game.

All keys are optional.

#### listen.udp

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `true` | receive UDP logs; `false` closes the UDP port and stops `logaddress_add` |
| `address` | `0.0.0.0` | IP to listen on (`0.0.0.0` = all IPv4, `::` = all) |
| `port` | `41337` | UDP port |
| `public_address` | – | `ip:port` the game servers send their logs to, registered with `logaddress_add` (`features.log_address`). Default: the collector's address of the RCON connection to the game server, with `port` (or the port of `-listen`). Set it when the servers reach the collector at another address (NAT, several IPs). |

The older form, `address`, `port` and `public_address` directly under `listen`,
is still read as the UDP input. Using it together with `listen.udp` is an error.
`gamewe run -listen IP:PORT` overrides the UDP `address` and `port` and switches
UDP on, e.g. for a test.

#### listen.http

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `false` | receive the HTTP logs of Counter-Strike 2 |
| `address` | `0.0.0.0` | IP to listen on |
| `port` | `41338` | TCP port |
| `public_url` | – | `http://host:port` (no path) the CS2 servers post to, registered with `logaddress_add_http` (`features.log_address.http`). The collector adds `/<token>/<game port>`. Default: `http://`, the collector's address of the RCON connection and `port`. |
| `token` | – | a secret first path segment, 16 to 128 letters, digits, `-` or `_`. When it is set, only posts to `/<token>/<game port>` are accepted and everything else gets 404. Recommended, because CS2's HTTP logs carry no `sv_logsecret`. The token is never logged; `gamewe logaddress` and the log show it as `<token>`. |
| `max_body` | `128KiB` | largest post, `4KiB` to `16MiB` (`KiB`, `MiB`, `KB`, `MB` or bytes); a larger one gets 413 |
| `read_timeout` | `10s` | time to read one post, `1s` to `2m` |
| `max_connections` | `64` | open connections at most (1 to 4096); more are closed at once |
| `max_connections_per_ip` | `8` | open connections from one IP at most (1 to `max_connections`) |
| `trusted_proxies` | – | IPs or networks (CIDR) of reverse proxies in front of the collector. Only for a request from one of them the sender is taken from `X-Forwarded-For` (the right-most address that is not itself a trusted proxy). |

A post is checked before its body is read. The sender's IP and the game port
from the URL must be a server in `hlstats_Servers` whose game is a Source 2
game (`cs2`); every other post gets 403. Only `POST` is accepted (405
otherwise). Lines longer than 8 KiB are dropped. Rejected posts are logged the
first time per sender and reason, then every 1000th time.

### features

Optional jobs, all on by default except `inactive_players`. They need the
game server's `rcon_password` in `hlstats_Servers` (except `daily_awards`
and `inactive_players`). The switches are only in the
config file (changing them needs a restart); the old key `gamewe_daily_awards` of
`hlstats_Options` is not read, and migration 13 deletes it ([DATABASE.md](DATABASE.md)).

| Key | Default | Meaning |
|-----|---------|---------|
| `rcon_status.enabled` | `true` | RCON `status` to each server every `interval`, over one connection that stays open: fills the live view, `act_players`, `act_map`, `max_players` and the name if none is set also when no log lines arrive (empty server, collector restart); see DB-SEMANTICS.md, "RCON status". The old form `rcon_status: true` / `false` still works and means `enabled`. |
| `rcon_status.interval` | `10s` | how often, a duration (`10s`, `1m`) or seconds; at least `2s`. While a server does not answer, the wait doubles up to one minute. |
| `log_address.enabled` | `true` | RCON `logaddress_list` to each server every `interval`; if the collector's address (`listen.public_address` or automatic) is missing, `logaddress_add <ip:port>` and `log on`, logged once. Also `log on` if the server stopped logging. Addresses of other tools are left alone. The old form `log_address: true` / `false` still works and means `enabled`. |
| `log_address.http` | `true` | Counter-Strike 2 servers (when `listen.http` is on): `logaddress_list_http`, and if the collector's URL is missing, `logaddress_add_http "<url>"` and `log on`. If the server's list does not show the URL after adding it, it is added again at most once an hour. `false`: register CS2 servers by hand. GoldSrc and Source servers are not affected, and nothing is registered over UDP for CS2. |
| `log_address.interval` | `1m` | how often, a duration (`30s`, `5m`) or seconds; at least `10s`. Only `logaddress_list` is sent each time (`logaddress_add` only when the address is missing), so a game server that restarted without the collector in its `server.cfg` sends logs again within this time. A shorter interval loses fewer log lines after such a restart, a longer one sends fewer RCON commands. |
| `log_secret.enabled` | `true` | the log secret (`sv_logsecret`) in the log packets, see "Log secret" below. `true`: a server with `hlstats_Servers.log_secret` set must send exactly that secret, other packets are rejected; a server without one is not checked. You set the secret yourself, per server with `gamewe server set -logsecret` or in the web admin, and the same `sv_logsecret` in the game server's `server.cfg`. `false`: never checked (a secret in the packet is cut off). The short form `log_secret: true` / `false` works too. The automatic mode of 0.4.0 to 0.5.1 is gone: an old `auto` counts as `true` and an old `interval` is ignored, both with a warning at start and in `gamewe check`. |
| `server_info.enabled` | `true` | ask every server for its version, operating system and VAC state every `interval`: an `A2S_INFO` query (Valve's server browser query, UDP to the game port in `hlstats_Servers`, no `rcon_password` needed; one answer of at most 1400 bytes, 3 s timeout, a challenge answer is answered once), and with `rcon_status` also the `version` (and `os` / `os/type`) line of each RCON `status`. Stored in `hlstats_Servers.server_version`, `server_os`, `server_vac` only when a value changes (by hand, also when this is off: `gamewe serverinfo`); `A2S_INFO` wins where both answer. A server that does not answer is logged once (warning) until it answers again. `false`: neither is stored. The short form `server_info: true` / `false` works too. |
| `server_info.interval` | `5m` | how often `A2S_INFO` is sent, a duration (`1m`, `10m`) or seconds; at least `30s` |
| `daily_awards.enabled` | `true` | decide the daily awards of the previous day (DB-SEMANTICS.md) every day at `time`; `gamewe awards [-date YYYY-MM-DD]` works also when it is off (and waits for a run of the collector in progress). The old form `daily_awards: true` / `false` still works and means `enabled`. |
| `daily_awards.timezone` | empty | IANA time zone (`UTC`, `Europe/Berlin`, ...) of `time` and of the day: the awards of a day count from 00:00 to 24:00 in this zone (23 or 25 hours on daylight saving days). Empty: the collector's [`timezone`](#timezone), else the server's time zone. Before 0.14 an empty or missing value meant `UTC`: a config without `timezone` here now counts the day in the server's zone (the container image stays UTC unless `TZ` is set); write `timezone: UTC` to keep the old day. |
| `daily_awards.time` | `"00:00"` | when the previous day is decided, `HH:MM` in `timezone` (on the day summer time starts, a time that does not exist runs an hour later) |
| `scheduled_resets.time` | `"00:00"` | when the periods of the scheduled ranking resets begin and end ([USAGE.md](USAGE.md#scheduled-resets-and-the-hall-of-fame)), `HH:MM` in the collector's [`timezone`](#timezone): with `"05:00"` a monthly period runs from the 1st 05:00 to the next 1st 04:59 and the reset is at 05:00. The schedules themselves are set in the web admin or with `gamewe halloffame schedule` |
| `inactive_players.enabled` | `false` | once a day at `time`, move the players without play for `MinActivity` days (the `hlstats_Options` key of the web admin, "days until 0 % activity", default 28) from `hlstats_Players` to `hlstats_PlayersInactive`, as the maintenance job of HLstatsX did. A player counts as inactive when `last_event` is older than that and the player is not online (no row in `hlstats_Livestats`); bots are treated like players. All columns move unchanged in batches of 500; no other table is touched (names, unique ids, events, history stay). A returning player is moved back with the same values at the first log line (DB-SEMANTICS.md). A missing or invalid `MinActivity` (not 1 to 3650) uses 28 and logs a warning. The number of moved players is logged. The rankings, `hlstats_Servers.players` and the web admin's player list only read `hlstats_Players`, so the moved players drop out of them; their profile page still works. Off by default so that an update changes no existing database. The first run is at the next `time` after the start (no run at start). `gamewe inactive` does it at once, also when this is off. The short form `inactive_players: true` / `false` works too. |
| `inactive_players.time` | `"01:00"` | when, `HH:MM` in `timezone`; exactly one run per calendar day, also on the days daylight saving time starts (a time that does not exist runs an hour later) or ends |
| `inactive_players.timezone` | empty | IANA time zone of `time`; empty: the collector's [`timezone`](#timezone), else the server's time zone |
| `update_check.enabled` | `true` | ask GitHub (`api.github.com`, without a token) for the latest collector release. `gamewe run` does it at start and then once a day and logs a newer release as a warning (`update available`, with the version and the link); every other command except `version` and `help` first prints `gamewe: update available: X (running Y), <link>` on stderr and then does its work as usual. The CLI keeps the answer for 6 hours (after a failure 1 hour) in `~/.cache/gamewe/update.json` and waits at most 3 s; offline nothing is printed. Nothing is downloaded or installed. `false`, or the environment variable `GAMEWE_UPDATE_CHECK=off`, turns it off; `gamewe check-update` always asks. The short form `update_check: true` / `false` works too. |
| `sourcebans.enabled` | `true` | record SourceBans bans and take the banned players out of the ranking while the ban runs. Needs the address of the public ban list, set in the web admin under Settings → Bans (`hlstats_Options` `gamewe_sourcebans_url`, read every minute); without it nothing happens. A player who disconnects with a reason that names that address's host (SourceBans++ puts its website into the kick message in every language: "You have been banned from this server. Access … for more info.", "Sie wurden von diesem Server gebannt, weitere Informationen auf …", …) or one of `phrases` is looked up on the ban list (`index.php?p=banlist&advSearch=STEAM_0:X:Y&advType=steamid`, one HTTPS request, 8 s, at most 3 MiB, no redirects, the same player at most every 5 minutes, at most 256 lookups waiting). Only a running ban there counts (a matched kick without one changes nothing): it is stored in `hlstats_Players_Bans` (type `sourcebans`, reason, length, start and end from the list, dates in the collector's [`timezone`](#timezone)) and the player gets `hideranking = 2` if it was 0 (DB-SEMANTICS.md). Every `interval` the running bans are checked again: one whose end has passed, or which the list shows as expired, lifted or no longer at all, ends, and the player is back in the ranking (`hideranking` 2 → 0, only if this ban had set it and no other running ban holds it; a player an admin hid meanwhile stays hidden). When the list cannot be read, nothing changes. The short form `sourcebans: true` / `false` works too. |
| `sourcebans.interval` | `30m` | how often the running bans are checked again, a duration (`15m`, `1h`) or seconds; at least `5m`. One Steam ID per second is looked up. |
| `sourcebans.phrases` | empty | further parts of kick messages that mean a ban, case insensitive, 4 to 200 characters each (e.g. a ban plugin of your own that does not name the address). The lookup still decides. |
| `sourcebans.allow_private` | `false` | allow a ban list on the LAN or localhost; off, only public addresses are contacted (checked when connecting) |
| `cs2_hitgroups.enabled` | `true` | Counter-Strike 2: count hits per body part (`head`, `neck`, `chest`, `stomach`, arms, legs) and damage per weapon from the `attacked` lines and kills, headshots and deaths per weapon from the kill lines, stored in `hlstats_Players_Weapons` at the end of every round and when a player leaves. CS2 has no plugin that reports `weaponstats`, so shots and accuracy stay empty. The short form `cs2_hitgroups: true` / `false` works too. |

### timezone

`timezone: ""` (top level): the collector's time zone, an IANA name such as
`Europe/Berlin`, for the daily jobs (`features.daily_awards`,
`features.inactive_players`) that have no `timezone` of their own and for
the periods of the scheduled ranking resets ([USAGE.md](USAGE.md#scheduled-resets-and-the-hall-of-fame)). Empty (the
default): the server's time zone (`TZ`, `/etc/localtime`; in the container
image UTC unless `TZ` is set). The order is: the job's `timezone`, then
this one, then the server's. An unknown name is a config error:
`gamewe run` exits with 78, `gamewe config test` reports the line.

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
| `table_prefix` | `hlstats` | prefix of the table names, as `DB_PREFIX` of HLstatsX:CE: `hlstats` gives `hlstats_Players`, `gw` gives `gw_Players` (`gw_` is the same as `gw`). A letter, then letters, digits and `_`, at most 16 characters; not `gamewe` and not starting with `hlstats_`. The web frontend needs the same value (`DB_TABLE_PREFIX`). See [DATABASE.md](DATABASE.md#table-prefix) |
| `password` | – | password, or: |
| `password_file` | – | file containing the password (one of the two) |
| `max_open_conns` | `8` | connection pool size, shared by all game servers; the collector uses one connection more for its collector lock (see [DATABASE.md](DATABASE.md#schema-versions-and-upgrades)) |
| `connect_timeout` | `10` | seconds |
| `tls.mode` | `disable` | `disable`, `require`, `verify-ca`, `verify-full`, see DATABASE.md. Set `verify-full` for a database on another host. |
| `tls.ca_file` | system CAs | CA certificate(s), PEM |
| `tls.cert_file`, `tls.key_file` | – | client certificate and key, PEM (set both or neither) |
| `tls.server_name` | `host` | name expected in the server certificate |
| `auto_migrate` | `true` | `gamewe run` brings an outdated schema up to date at start (as `gamewe db migrate -f`, steps in the log); `false`: it refuses to start until `gamewe db migrate` ran. An HLstatsX database without schema versions is always taken over by hand. See [DATABASE.md](DATABASE.md#schema-versions-and-upgrades) |

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
`features.log_secret.enabled` `true` (default) the collector compares that
secret with `hlstats_Servers.log_secret` of the server the packet's address
belongs to (constant time; the secret is never logged). The collector never
sets, reads or changes `sv_logsecret` itself: the secret is set by hand, in
two places that must match:

1. in `hlstats_Servers.log_secret`: `gamewe server set <id> -logsecret <n>`
   (or `gamewe server add ... -logsecret <n>`), or the "Log secret" field of
   the server in the web admin;
2. on the game server: `sv_logsecret <n>` in its `server.cfg` (a value set
   only over RCON is lost at the next restart).

Then:

- **No secret stored** (`log_secret` empty): every packet of the server is
  accepted, with or without a secret (a secret in the packet is cut off).
  Logged once as a note, and `gamewe check` lists it (`log_secret: server X
  has no log secret, all its log packets are accepted`).
- **Secret stored**: a packet without the secret, or with another one, is
  rejected (`missing or wrong log secret`, logged the first time per source
  and reason, then every 1000th time), with or without `rcon_password`.
  After a change in `hlstats_Servers` (read every minute) the previous secret
  is still accepted for two `log_address` intervals, at least two minutes, so
  there is time to change `server.cfg` too. Clearing the secret
  (`gamewe server set <id> -logsecret ""`, or `-` in the web admin) makes the
  collector accept all packets of the server again.
- **GoldSrc servers** (cstrike, czero, valve, dod, tfc, ns, ... by the game's engine,
  GAMES.md) are never checked; logged once as a note. (Assumption: HLDS has
  no `sv_logsecret`.)
- **Source 2 servers** (cs2) are never checked either: their HTTP logs carry
  no secret. Logged once as a note. Protect the HTTP input with
  `listen.http.token` and the firewall instead (only TCP connections from the
  server's own IP and game port in `hlstats_Servers` are accepted; TCP cannot
  forge its sender address the way UDP can).

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
| `gamewe_store_player_ip` | `1` | 1 = store IPv4 addresses in `hlstats_Events_Connects.ipAddress` (personal data: only `gamewe_web_admin` can read the column, not `gamewe_web`) |
| `gamewe_store_chat` | `1` | 1 = store chat in `hlstats_Events_Chat` (personal data: only `gamewe_web_admin` can read the table, not `gamewe_web`) |
| `gamewe_win_triggers` | `CTs_Win,Terrorists_Win,Target_Bombed,Target_Saved,Bomb_Defused,All_Hostages_Rescued,Hostages_Not_Rescued,VIP_Escaped,VIP_Assassinated,VIP_Not_Escaped,Round_Win` | log triggers that end a round with a winner (wins/losses; the bonus is `hlstats_Actions.reward_team`) |
| `gamewe_commands` | `1` | 1 = answer `!rank`, `!top10`, ... in game |
| `gamewe_menu_time` | `15` | seconds the panels (`!top10`, `!rank`, ...) stay open |
| `gamewe_rcon_timeout` | `5` | seconds for RCON connect, send and receive |
| `gamewe_log_lines` | `1` | 1 = write every received game-server log line to the collector's log (the journal), as `[name ip:port] L 10/22/2026 - …`. That can be many lines; see [INSTALLATION.md](INSTALLATION.md#g-troubleshooting). |
| `gamewe_idle_timeout` | `300` | seconds without any log line of a player after which the collector ends the visit, as for a disconnect but without an `hlstats_Events_Disconnects` row: the connection time up to now is stored and the live row deleted. Catches lost disconnect lines (server crash or restart, lost packets). A player who shows up again starts a new visit. Bots end after at most 3 minutes without a line (bot_quota kicks them without a disconnect line; RCON status keeps listed bots). 0 = never |
| `gamewe_command_display` | `panel` | how `!rank`, `!kpd`, `!session` and `!next` (and `!statsme` without a browser) are answered: `panel` = a menu panel on the left of the screen for `gamewe_menu_time` seconds (as gameME showed it), `chat` = private chat lines, `both` |
| `gamewe_command_prefix` | `gamewe` | prefix of the plugin's server commands: `gamewe` (the gameWE plugin also knows `gameme_`), or `gameme` for a server that still runs the original gameME plugin |
| `gamewe_raw_messages` | `0` | 1 = answer `!rank`, `!session`, `!top10` and `!next` with `gamewe_raw_message` (types 1, 4, 5, 6) for a plugin that shows them, instead of chat text ([INTERFACE.md](INTERFACE.md#queries-of-other-plugins-raw-messages)) |
| `gamewe_store_locations` | `1` | 1 = store the kill and death positions of the plugin's location lines in `hlstats_Events_Locations` (one row per position; the plugin's `gamewe_log_locations` must be 1) |
| `gamewe_geoip_db` | – | **Legacy, no longer a setting** (not in a new database, not settable with `gamewe option set`, not on the web admin's settings pages): a stored path other than the old default `/var/lib/gamewe/GeoLite2-City.mmdb` is still used while config.yaml has no `geoip.path`; set `geoip.path` instead and delete the row ([GeoIP database](#geoip-database-player-and-server-locations)). Migration 13 deletes the row when it is empty or the old default |
| `gamewe_server_load` | `1` | 1 = write a row of `hlstats_server_load` every 10 minutes per server (DB-SEMANTICS.md) |
| `gamewe_player_reset` | `0` | 1 = players may reset their own stats in game with `!gamewe_reset`, confirmed with `!gamewe_reset 1` within 30 seconds ([USAGE.md](USAGE.md#settings)); never players hidden by an admin or banned. The events stay. Like every chat command it is read from the log: a faked log packet could reset any player's stats, so turn it on only where the servers send their logs with `sv_logsecret` ([Log secret](#log-secret)) |
| `gamewe_player_reset_cooldown` | `24` | hours before a player may reset the own stats again (from `hlstats_Players_Resets`); 0 = no wait |
| `gamewe_console_events` | `0` | 1 = after every kill the killer and the victim, after every action with points the player, get the points in the console (`gamewe_console_event`; players turn it off with `!gamewe_display 0`). One RCON command per player and event |
| `gamewe_global_chat` | `0` | 1 = player actions with points, player-vs-player actions and team wins are announced in chat (`gamewe_chat_event`; players turn it off with `!gamewe_chat 0`). One RCON command per event and language |
| `gamewe_rank_bots_<game>` | `1` | bots of the ranking `<game>` (always recorded: live view, server counts, their own statistics): 1 = ranked players like everybody else; 0 = hidden from the ranking and awards (new bots get `hideranking` = 1). Set by `gamewe game set CODE -rank-bots=true\|false` or the web admin (ranking page, card "Bots"), which also hide or show the ranking's existing bots (`hideranking` 0 ↔ 1, banned ones stay) |
| `gamewe_bot_points_<game>` | `1` | 1 = kills and player-vs-player actions with a bot (either side) change skill in the ranking `<game>` like any other; 0 = they are counted (kills, deaths, weapons) but change nobody's skill: no points, no headshot action, no kill streak. Left 4 Dead: a player's kill of the AI gives the player alone points; while the AI is not ranked (`gamewe_rank_bots_<game>` = 0) its own kills and actions change nobody's skill. Set by `gamewe game set CODE -bot-points=true\|false` or the web admin |
| `gamewe_bot_kills_<game>` | `1` | 1 = a kill of a bot counts in the killer's kills and headshots like any other; 0 = the kill is stored (events, weapons, maps, server and team counters, the bot's death) but not counted in the killer's `hlstats_Players.kills` / `headshots`, the name's counts and the history: it does not raise the kill ranking, K/D or the kill-based ranks (career). Points are `gamewe_bot_points_<game>`. Set by `gamewe game set CODE -bot-kills=true\|false` or the web admin |
| `gamewe_block_vac_<game>` | `0` | 1 = players with a VAC ban on record (Steam Web API, also bans from other games) are kicked at connect in the ranking `<game>`, recorded in `hlstats_Players_Bans` (type `vac`) and kept out of the ranking; 0 = nothing is checked. Needs `gamewe_steam_api_key` (web admin, Settings → Steam) and an RCON password per server; without a key or when Steam does not answer nobody is blocked. Switched back to 0, the ranking's VAC records end within a minute. Set in the web admin (Rankings → ranking) or with `gamewe game set CODE -block-vac=true`; see DB-SEMANTICS.md, "VAC block". |
| `gamewe_steam_api_key` | – | the Steam Web API key of the web admin (Settings → Steam), read for `gamewe_block_vac_<game>`; 32 hex characters, never logged or shown by `gamewe option` |
| `gamewe_rank_bots`, `gamewe_ignore_bots` | – | **obsolete** (collector 0.20 and older: one bot setting for all rankings). Migration 15 copies the value to `gamewe_rank_bots_<game>` and `gamewe_bot_points_<game>` of every ranking (Left 4 Dead keeps its points for kills of the AI) and deletes the rows; a row set by hand later is not read and logged |
| `gamewe_reset_schedule` | – | scheduled reset for all rankings without a schedule of their own, with the hall of fame ([USAGE.md](USAGE.md#scheduled-resets-and-the-hall-of-fame)): `day`, `week`, `month`, `quarter`, `year`, or a number and one of them (`2 month`), optionally with the start day after `@`: `month@15` (day of the month 1-28, also for quarters), `week@3` (weekday, Monday 1), `year@07-01` (MM-DD, day 1-28); empty or `off` = none. Set by `gamewe halloffame schedule -all` or the web admin (Rankings) |
| `gamewe_reset_mode` | `player-stats` | the mode of that reset: `player-stats`, `all-stats` or `all` |
| `gamewe_reset_schedule_<game>` | – | the ranking's own schedule (same values); `off` = no scheduled reset for this ranking; missing or empty = the schedule for all rankings (`gamewe_reset_schedule` and `gamewe_reset_mode`). Set by `gamewe halloffame schedule` or the web admin |
| `gamewe_reset_mode_<game>` | `player-stats` | the mode of the ranking's own schedule |
| `gamewe_reset_last_<game>` | – | **written by the collector**: the start (Unix time) of the period now running and the schedule it belongs to (`1790805600 month@15`; collector 0.19 wrote the time alone, the next run adds the schedule); a reset is due when a newer period has begun; when the ranking's schedule has changed, the new one starts with the period now running |
| `gamewe_hof_<game>` | – | web: `1` = show the page "Hall of Fame" of the ranking, `0` = hide it; missing = shown when the ranking has a schedule or entries |
| `gamewe_heartbeat` | – | **written by the collector** (Unix time, every 60 s), not a setting: the web and `gamewe reset` see from it whether a collector runs |
| `gamewe_collector_version` | – | **written by the collector** at start (its version, e.g. `0.17.0`), not a setting: the web admin compares it with the latest release |

The other `gamewe_*` keys the web admin writes are settings of the web frontend
(`gamewe_page_size`, site, header, SEO, map, voice and
TeamSpeak settings, the header links `gamewe_link_<n>` and the admins' TOTP
secrets `gamewe_totp_<hash>`); the collector ignores them and `gamewe check`
does not report them. One exception: the collector reads the web's
`gamewe_site_url` (the public address of the site, set in the web admin) for
the in-game browser commands (`!statsme`, `!status`, ...); it must be a plain
`http://` or `https://` address, otherwise those commands answer that the web
address is not configured. The list is `WebKeys` in `internal/settings/settings.go`;
a new web setting needs an entry there.
