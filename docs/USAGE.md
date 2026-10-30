# gameWE - collector: usage

How the collector works once it runs: what players can type in game, how
points are counted, and the `gamewe` command line for checks, games, servers,
settings and ranking resets. Installation: [INSTALLATION.md](INSTALLATION.md);
settings: [CONFIG.md](CONFIG.md).

## Contents

1. [In-game commands](#in-game-commands)
2. [Points](#points)
3. [Command line](#command-line)

## In-game commands

Players type these in chat (`!` or `/` prefix). They are gameME's commands
with gameWE names: every gameME word works, and the settings commands are
also accepted as `gameme_*`.

### Stats in chat and panels

Private answers are a panel on the left of the screen via `gamewe_msay`, as
gameME showed it, or chat lines via `gamewe_psay`, see
`gamewe_command_display`. With `gamewe_raw_messages` = 1, `!rank`, `!next`,
`!session` and `!top10` are sent as `gamewe_raw_message` for a message plugin
instead. The variants marked *to all* answer every player on the server (no
bots) in chat, each in the own language; a player can send one of them at
most every 15 seconds (more are ignored).

| Command | Answer |
|---------|--------|
| `!rank`, `!skill`, `!points` | panel *1 - Total*: position, points, frags (K:D), headshots, accuracy, time played; *2 - Session*: positions and points won or lost, frags, headshots, accuracy, favourite weapon, time of this visit. In chat: *You are ranked #3 of 50 with 1012 points.* |
| `!place` | *to all*: *Alice is ranked #3 of 50 with 1012 points.* |
| `!kpd`, `!kdratio` | the panel and your totals in chat: points, rank, hours played, kills, deaths, K:D, headshots, suicides, teamkills (what `!status` answered up to collector 0.8) |
| `!kdeath` | *to all*: the totals of `!kpd` |
| `!session`, `!sdata` | the panel and this visit in chat: kills, deaths, K:D, headshots, points, minutes |
| `!session_data` | *to all*: this visit of `!session` |
| `!next` | the player ranked just above you and how many points you are behind |
| `!top5`, `!top10` (`!top`), `!top20` | menu with the 5, 10 or 20 best players (`gamewe_msay`; long names are shortened so the menu fits) |
| `!help`, `!cmd`, `!cmds`, `!commands` | the list of commands, three chat lines |

### Web pages (in-game browser)

These open a page of the web frontend with `gamewe_browse <userid> "<url>"`
(`gamewe_amx_browse` on GoldSrc). The address is `gamewe_site_url` (the web
frontend's setting in `hlstats_Options`) with a fixed path, the game code and
ids or codes from the database; nothing the player typed goes into it.

| Command | Page |
|---------|------|
| `!load` | the start page of the site (`/`) |
| `!servers` | the servers of the ranking (`/<game>`) |
| `!status` | the server you are playing on (`/<game>/servers/<id>`) |
| `!clans` | the clan ranking (`/<game>/clans`) |
| `!cheaters` | the banned players (`/<game>/bans`) |
| `!maps`, `!map_stats` | the maps (`/<game>/maps`) |
| `!statsme` | your profile (`/<game>/players/<id>`) |
| `!weapons`, `!weapon` | your profile; with a weapon code (`!weapons ak47`, case does not matter) the weapon's page (`/<game>/weapons/<code>`), *Unknown weapon* if the game has none of that name |
| `!accuracy`, `!targets` (`!target`), `!kills` (`!kill`, `!player_kills`) | your profile, which holds the weapons, hit boxes, accuracy and kill statistics (the profile has no separate pages for them; arguments are ignored) |
| `!actions`, `!action` | the actions (`/<game>/actions`); with an action code (`!actions planted_the_bomb`) its page (`/<game>/actions/<id>`) |

- Players hidden from the ranking (by themselves, by an admin, or banned)
  have no profile page: `!statsme`, `!weapons`, `!accuracy`, `!targets` and
  `!kills` answer with a chat line instead. Nobody else's profile is ever
  opened.
- Without `gamewe_site_url`, or with a value that is not a plain `http(s)://`
  address (no user, query or fragment), the commands answer *The web address
  of the statistics is not configured on this server.* and `!statsme` shows
  the stats panel of `!session`.
- The MOTD window of GoldSrc games other than Counter-Strike 1.6 and Condition Zero shows text
  only: there the address is sent as a chat line and `!statsme` shows the
  stats panel. CS:GO shows no web pages; the SourceMod plugin prints the
  address in chat.
- Plain words without `!` or `/` only count as the whole chat message
  (`kill him` opens nothing); `rank`, `top`, `top10`, `next`, `help`,
  `session` and `statsme` also with more words, as before.

### Settings

| Command | Effect |
|---------|--------|
| `!hideranking [1\|0]` (also `/gamewe_hideranking`, `/gameme_hideranking`) | hide yourself from the ranking (`1`, `on`) or show yourself again (`0`, `off`); without an argument it switches. A private chat line says the new state. At most one change per 10 seconds. Players hidden by an admin or banned cannot change it. Stored as `hideranking` 3 (DB-SEMANTICS.md) |
| `!settings` (also `/gamewe_settings`) | your settings in chat: whether you are shown in the ranking, and how to change it. The plugins open their settings menu for `!gamewe_settings` instead; its one ranking entry hides or shows you, depending on your current state (`gamewe_hidestate`) |
| `!gamewe_reset` (also `!gameme_reset`) | only with `gamewe_player_reset` = 1: resets your own stats. The first call says what happens; `!gamewe_reset 1` within 30 seconds confirms, `!gamewe_reset 0` cancels. Your points go back to `gamewe_start_skill`, your totals, weekly and monthly weapon, action, map, team and role rows, daily history and name counters are deleted or zeroed; the events (kills, chat, ...) stay, they belong to the other players too. The reset is recorded in `hlstats_Players_Resets`; the next one is possible after `gamewe_player_reset_cooldown` hours. Players hidden by an admin or banned cannot reset |
| `!gamewe_display 0\|1`, `!gamewe_chat 0\|1` | kept by the plugins per player: whether you get the console events (`gamewe_console_events`) and the chat events (`gamewe_global_chat`); see the plugins' USAGE.md |
| `!gamewe_auto clear\|start\|end\|kill <command>` | kept by the plugins: a command that is sent for you at round start, round end or when you die (see the plugins' USAGE.md) |
| `!gamewe_lang en\|de\|ru\|es\|fr\|pt\|nl\|dk\|pl` | the language of the answers. The plugin keeps it and logs it (`setup "gamewe_lang"`); without a choice the game's language (`cl_language`, logged by the plugin) decides, otherwise English. The collector keeps it with the player's visit |

### Events

- `gamewe_console_events` = 1: after every kill the killer and the victim
  get the points in the console (*+5 points: you killed Bob with ak47.*), and
  after every action with points the player (*+3 points: Planted_The_Bomb.*),
  via `gamewe_console_event`; the plugin shows it only to players with
  `gamewe_display 1`.
- `gamewe_global_chat` = 1: player actions with points (not the headshot of a
  kill), player-vs-player actions and team wins are announced in chat (*Bob
  got +3 points for Planted_The_Bomb.*), via `gamewe_chat_event`; the plugin
  shows it only to players with `gamewe_chat 1`.

Both are off by default; every event is one RCON command per player or
language, and the RCON queue of a server drops commands when it is full.

The commands are read from the `say` lines in the log. The plugin logs them
(always with the verb `say`) even when it hides them from chat. Its menu sends
them with a `/` prefix (`/rank`), and both prefixes work. `!hideranking`,
`!settings` and `!gamewe_reset` need the `!` or `/` prefix (a chat line that
only contains the word does nothing).

## Points

Points are the HLstatsX **skill** (`hlstats_Players.skill`). Rankings are per
game (`hlstats_Players.game`) and leave out players with `hideranking`.

The values in `code style` are `hlstats_Options` keys (defaults in
[CONFIG.md](CONFIG.md#settings-in-hlstats_options)):

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
- Skill never drops below 0. Bots are recorded, and per ranking ranked or
  hidden (`gamewe_rank_bots_<game>`, default ranked); kills with bots give
  points unless the ranking turns that off (`gamewe_bot_points_<game>`),
  count for the career rank unless it turns that off (`gamewe_bot_kills_<game>`),
  and count in the players' stats (kills, deaths, headshots, K/D, streaks)
  only if it turns that on (`gamewe_bot_stats_<game>`, default off; Left 4
  Dead: kills of the AI always count).
- Per ranking, players with a VAC ban on record (also from another game)
  can be kept out (`gamewe_block_vac_<game>`, default off): kicked at
  connect, recorded as banned and out of the ranking (DB-SEMANTICS.md,
  "VAC block").
- VAC-banned players (disconnect reason) and permanent bans (`Banid: ... "permanently"`)
  are hidden from the rankings (`hideranking` = 2).

What the collector writes into which table, for anyone reading the database
(e.g. the web frontend), is in [DB-SEMANTICS.md](DB-SEMANTICS.md).

Inactive players: with `features.inactive_players.enabled: true` the
collector moves once a day (default 01:00 in `timezone`, else the server's
time zone) the players who have not played for `MinActivity` days (web
admin, default 28) and are not online to `hlstats_PlayersInactive`, and
logs how many. They come back unchanged with their next log line. It is on
by default; see [CONFIG.md](CONFIG.md#features). `gamewe inactive` does it
now, also when the feature is off
([jobs by hand](#collector-jobs-by-hand-and-from-cron)).

Daily awards (most kills with a weapon, most actions, most kills, longest play
time, ...) are decided every day for the previous day, by default at 00:00
in the server's time zone; time and time zone are `features.daily_awards` in the config file (or
the collector's top-level `timezone`); see
[DB-SEMANTICS.md](DB-SEMANTICS.md#daily-awards-ribbons-ranks). To decide a day
again (after changing the awards in the web admin):

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe awards -date 2026-10-21
```

## Command line

`gamewe <command>`; every command takes `-config FILE` (default
`/etc/gamewe/collector/config.yaml`), `gamewe help` lists them all. Every
command except `run` also takes `-o FORMAT` for scripts
([output formats](#output-formats-for-scripts)).

**Every command that changes something asks first.** It shows what it is
going to do (the plan, the rows per table, the players, the change) and asks
`[y/N]` (`y`, `yes`, `j`, `ja`; anything else changes nothing). The most
destructive ones (`db reset`, `db drop`, `reset`) want the database name or
game code typed instead.

- `--dry-run` (`-n`): only show it, change nothing (exit code 0; `db migrate
  -n`: 2 when a migration is pending).
- `--force` (`-f`): do not ask, for scripts and cron. The older `-yes` and
  `-y` still work and mean the same.
- Without a terminal on stdin (cron, systemd, CI, a pipe, `< /dev/null`)
  and without `--force` the command stops at once before changing
  anything, with exit code 1 and the hint to use `--force` or `--dry-run`.
- `--force` and `--dry-run` together are refused (exit code 2).

This holds for `db migrate`, `db init`, `db grants`, `db seed`, `db purge`,
`db optimize`, `db reset`, `db drop`, `db convert`, `reset`, `inactive`, `bans`,
`awards`, `serverinfo`, `logaddress -add`, `geoip-update`, `simulate` and
the changing `game`, `server` and `option` commands. Commands that only read
never ask: `check`, `status`, `config test`, `db status`, `version`,
`jobs`, `reset -list`, `inactive -list`, `check-update`, `game|server|option list`, `option get`, `logaddress`
without `-add`, `db grants|seed|drop -print`. `gamewe rcon` does not ask
either: the RCON command typed on its command line is the intent itself
(as in the server console), and there is nothing to show beforehand. `run`
is the service and never asks; its automatic migration is
`database.auto_migrate`.

| Command | What it does |
|---------|--------------|
| `gamewe run` | the collector (systemd starts it); migrates an outdated database schema at start (`database.auto_migrate`), with `auto_migrate: false` it refuses to start (exit code 78) until `gamewe db migrate` ran; reports "ready" to systemd (`Type=notify`) once the config, database and UDP port are fine, exit code 78 for a config error (not restarted by the unit), 1 for other errors |
| `gamewe config test` (also `gamewe --config-test`, `gamewe -t`, `gamewe run -t`) | check the config file without starting anything: no port, no database connection, no migration ([below](#checking-the-config-file)) |
| `gamewe check` | config, database connection and TLS, schema version and pending migrations, game servers and settings |
| `gamewe db migrate` | create the tables of a new database, bring an older one to this version (below); `gamewe run` does it at start with `database.auto_migrate` |
| `gamewe db status`, `gamewe db grants`, `gamewe db seed` | schema version and migrations; the web users' rights again; missing reference data |
| `gamewe db init`, `purge`, `optimize`, `reset`, `drop` | tables of a new database; delete old events; give the space back; empty everything; drop the tables (below) |
| `gamewe -V` / `--version` / `version` | the version |
| `gamewe status`, `gamewe logaddress`, `gamewe rcon` | RCON status, log address registration, any RCON command (below) |
| `gamewe serverinfo` | ask the servers for version, OS, VAC and slots (`A2S_INFO`) and store what changed, as `features.server_info` does |
| `gamewe game`, `gamewe server`, `gamewe option` | games, servers and settings like the web admin (below) |
| `gamewe reset GAME -mode player-stats\|all-stats\|all` | reset a ranking (below) |
| `gamewe awards [-date YYYY-MM-DD]` | decide the daily awards of a day (again), also with `features.daily_awards` off |
| `gamewe inactive` | move the inactive players to `hlstats_PlayersInactive` now, also with `features.inactive_players` off |
| `gamewe bans` | read the bans of all known players now (Steam Web API for every due account, the whole SourceBans list) into `hlstats_Bans`, as `features.bans_sync` does, also when it is off; `--dry-run` only counts the due accounts |
| `gamewe jobs [-queue]` | the jobs queued for the collector (resets, scheduled resets, inactive players, daily awards), their state and result ([below](#collector-jobs-by-hand-and-from-cron)) |
| `gamewe geoip-update` | download the DB-IP database now |
| `gamewe check-update` | ask GitHub for the latest release now and report on stderr whether it is newer (with `-o json` also the document on stdout); exit code 1 when GitHub cannot be reached. Nothing is installed ([`features.update_check`](CONFIG.md#features)) |
| `gamewe simulate` | replay a sample game into the database or to a running collector |

**Update note.** When a newer collector release is out on GitHub, every
command except `run`, `version` and `help` first prints one line on stderr,
`gamewe: update available: 0.17.0 (running 0.16.0), https://github.com/...`,
and then does its work as usual (stdout, exit code and `-o` output stay the
same). `gamewe run` logs it as a warning at start and once a day, and writes
its version to `hlstats_Options` (`gamewe_collector_version`) for the web
admin. Nothing is updated automatically; see
[`features.update_check`](CONFIG.md#features) to turn it off.

### Checking the config file

`gamewe config test` checks the config file the way `gamewe run` loads it, like
`nginx -t`, and reports every problem at once instead of stopping at the first.
It opens no port, does not connect to the database and changes nothing, so it
is safe on a running system and before a restart. `gamewe --config-test`,
`gamewe -t` and `gamewe run -t` (or `run --config-test`) are the same command;
it takes `-config FILE` and the flags `-log-level` and `-reload` of `run`, which
are only checked.
Run it as the user of the collector (`sudo -u gamewe ...`): whether a file is
readable is checked for the user running the test.

What it checks:

- **Syntax and keys:** YAML syntax, unknown or misspelled keys (with a
  suggestion, e.g. `did you mean "password"?`), keys of older formats (with
  where the setting went), values of the wrong type (`port: abc`). Each with
  its line.
- **Everything `gamewe run` checks at start**, but all problems instead of the
  first (the same rules, see [CONFIG.md](CONFIG.md)).
- **Inputs:** UDP and HTTP both off is an error (the collector would receive
  nothing). Invalid ports (0, above 65535, not a number) and listen addresses
  that are no IP are errors; `listen.udp.port: 0` (which means the default) is
  a warning, as are ports below 1024 (the gamewe user cannot open them) and an
  address that is not on this host.
- **Port clashes** between the enabled inputs: the same protocol and port on
  the same address, where `0.0.0.0` covers every IPv4 address and `::` every
  address (UDP and TCP on the same port are fine). A TCP input on the port of
  a database on this host (`localhost`, `127.0.0.1`) is an error too.
- **Secrets:** `listen.http` on without `token` (CS2 sends no log secret),
  a weak token, a database password that is missing, the example value or
  shorter than 12 characters are warnings; a token in the wrong form or
  `change-me` is an error (as at start). A config file, `password_file` or
  `tls.key_file` that every user may read is a warning. Secret values are never
  printed.
- **Switches that contradict each other** (warnings): a changed setting under
  an input or feature that is off (`listen.http.token` while `listen.http` is
  off, `features.rcon_status.interval` while `rcon_status` is off, ...;
  values written out with their default are fine), `features.log_address.http`
  while `log_address` is off, `listen.http.public_url` /
  `listen.udp.public_address` while nothing registers them, a public address of
  `0.0.0.0`, `trusted_proxies` that trust (almost) everyone, the removed
  `log_secret` auto mode.
- **Database:** type, host, user, name, port, pool size, timeout and TLS mode;
  a host with a port or a URL in it; the files of `tls.ca_file`, `cert_file`,
  `key_file` and `password_file` must exist and be readable, and the
  certificates are loaded as the connection would load them. `tls.mode:
  disable` to a database on another host (a public IP or a name with a dot;
  not `localhost`, private networks or a container name such as `db`) is a
  warning.
- **GeoIP:** a missing database that `auto_update` does not download, or a
  directory where the download cannot be stored (warning: no locations).

It does not connect to anything: the database login, the schema version and
the game servers are what `gamewe check` tests. Errors are what stops `gamewe
run` or makes its start fail; with warnings the collector runs.

```text
$ gamewe config test
gamewe: the configuration file /etc/gamewe/collector/config.yaml syntax is ok
gamewe: configuration file /etc/gamewe/collector/config.yaml test is successful

$ gamewe config test -config broken.yaml
gamewe: [error] listen.udp.enabled: udp and http are both off; enable at least one input for the game server logs in broken.yaml:3
gamewe: [warning] listen.http.port: has no effect while listen.http.enabled is false in broken.yaml:6
gamewe: [warning] listen.http.token: has no effect while listen.http.enabled is false in broken.yaml:7
gamewe: [warning] database.password: not set: the collector connects without a password; set password or password_file in broken.yaml:8
gamewe: [error] database.pasword: unknown key; did you mean "password"? in broken.yaml:12
gamewe: [warning] features.log_address.http: has no effect while features.log_address.enabled is false in broken.yaml:19
gamewe: configuration file broken.yaml test failed (2 errors, 4 warnings)

$ gamewe config test -config clash.yaml
gamewe: [error] listen.http.port: tcp port 3306 is the port of the local database (database.host 127.0.0.1): the collector cannot open it while the database runs in clash.yaml:6
gamewe: the configuration file clash.yaml syntax is ok
gamewe: configuration file clash.yaml test failed (1 error, 0 warnings)
```

Exit code 0: no errors (warnings are allowed); 1: at least one error (also
when the file cannot be read); 2: wrong usage (unknown flag or subcommand,
`-o csv`). `-o json` / `-o yaml` print `config`, `ok`, `syntax_ok`, `errors`,
`warnings` and `findings` (`severity`, `key`, `line`, `message`) with the same
exit codes:

```bash
gamewe config test -o json | jq -r '.findings[] | select(.severity == "error") | .key'
```

### Database schema

The schema is built into the binary. `gamewe run` brings an outdated database
up to date at start (`database.auto_migrate`, the default; not an HLstatsX
database without schema versions), otherwise `gamewe db migrate` does
([DATABASE.md](DATABASE.md#schema-versions-and-upgrades)). `gamewe db`
alone lists the subcommands.

```bash
gamewe db status                 # schema version, pending and applied migrations (with date)
gamewe db migrate -n             # --dry-run: the plan only; exit code 0 up to date, 2 pending, 1 error
gamewe db migrate                # plan, backup advice, then "Backup made? Migrate now? [y/N]" (y/yes/j/ja)
gamewe db migrate -f             # --force: no question (scripts, CI); the plan is printed anyway
gamewe db grants                 # grant the web users their table rights again
gamewe db grants -print | mariadb -u root -p gamewe   # the same SQL, run as a database admin
gamewe db seed                   # add missing reference data; existing rows stay as they are
gamewe db init                   # a new, empty database: tables, reference data, rights
gamewe db purge -older-than 90d  # delete the events older than 90 days (--dry-run: count only; asks [y/N], --force)
gamewe db optimize               # give the space of deleted rows back (OPTIMIZE TABLE / VACUUM ANALYZE)
gamewe db reset                  # empty every table, reference data again: as new (asks for the database name, --force)
gamewe db drop                   # drop all gameWE tables (-print: the SQL for a database admin; MariaDB needs DROP)
gamewe db convert -from hlstatsx -n   # HLstatsX:CE -> this new gameWE database: the plan only (MIGRATION-HLSTATSX.md)
gamewe db convert -from hlstatsx -source-timezone Europe/Berlin   # plan, then "Convert now? [y/N]" (--force)
gamewe db convert -from stats -source-prefix ce -n   # CE with DB_PREFIX ce (DATABASE.md, "Table prefix")
```

`migrate`, `convert`, `reset` and `drop` refuse while a collector runs on the database,
also one on another host or in a container (see below); `purge` keeps
players, totals, rankings and awards. What each one touches and the
rights they need: [DATABASE.md](DATABASE.md#maintenance-gamewe-db).

`-f` and `-n` together are an error. Without a terminal on stdin (cron,
scripts, `docker compose run -T`) `gamewe db migrate` does not wait for an
answer: it stops with a message unless `-f` is given. The migration takes a
lock, so a second run at the same time waits and then finds nothing to do.

**Stop the collector first** (`systemctl stop gamewe-collector`, `docker
compose stop collector`): a running collector writes into the tables a
migration changes. `gamewe db migrate` (also with `-f`: it only skips the
question), `convert`, `reset` and `drop` check this before they ask and change
nothing while one runs. A collector runs when

- it holds the *collector lock* of the database: a running `gamewe run`
  (0.7.2 and newer) holds it on a connection of its own (MariaDB `GET_LOCK`,
  PostgreSQL advisory lock); the database releases it when the collector
  ends, so this also tells a collector on another host or in a container;
- a `gamewe run` process runs on this host (found in `/proc`, whatever the
  path of the binary; also collectors up to 0.7.1, which hold no lock), or
  the systemd unit `gamewe-collector` is active.

The refusal names the PID, its command line and its systemd unit or
container, and lists the commands to run, one per line, ready to copy: stop
it (`systemctl stop UNIT` when the collector is the main process of an active
system unit, `docker stop CONTAINER`, otherwise `kill PID`, e.g. for one
started by hand, from a script or a user unit), the same `gamewe db ...` command again (the path of
this binary, the same flags), start it again. For example:

```text
gamewe db: a collector is running on this host:
  PID 1234: /opt/gamewe/collector/bin/gamewe run -config /etc/gamewe/collector/config.yaml (systemd unit gamewe-collector.service)
Stop it first, then migrate, then start it again:

  systemctl stop gamewe-collector
  /opt/gamewe/collector/bin/gamewe db migrate -f
  systemctl start gamewe-collector
```

A collector seen only by its lock runs elsewhere: the commands are then
`docker compose stop collector` (or `docker stop CONTAINER`, or `systemctl
stop gamewe-collector` on its host). Run in a container (`docker compose
exec`/`run`), the commands are the `docker compose` ones. Nothing waits for
the heartbeat (`gamewe_heartbeat`): a heartbeat younger than 2 minutes
without lock and process is only noted (a collector stopped a moment ago,
or one up to 0.7.1 on another host, which only you can stop) and the command
goes on. Every `gamewe run` on this host counts, also one of another
database: stop it as well, or run the command in a container or on another
host.

While it migrates, every step is a line `[n/m] ...`; a step that takes long
prints `still running after 15s ...` every 15 seconds. A step waits at most
30 seconds for a table another session holds, then stops with "the table is
locked by another session" (look at `SHOW PROCESSLIST` / `pg_stat_activity`).
Ctrl-C stops it at any time: the waiting statement is cancelled on the
server too, the message says which steps are done; every step is
idempotent, so `gamewe db migrate` again goes on from there.

### RCON jobs and checks by hand

With the game server's `rcon_password` in `hlstats_Servers` the collector talks
to every server over RCON (switches in `features:` of the config file, all on by
default):

- **status** every 10 seconds (`features.rcon_status.interval`): players and
  bots in the live view, `act_players` and `act_bots` (also on an empty server and right
  after a collector restart, without any log line), the map, maximum players,
  the name if none is set, pings. One connection per server stays open.
- **log address** every minute (`features.log_address.interval`): `logaddress_list`; if the collector is missing,
  `logaddress_add <ip:port>` and `log on`. The address is
  `listen.public_address`, or the collector's address of the RCON connection
  with the listen port. So a game server that restarted without
  `logaddress_add` in its config is back within that interval. Counter-Strike 2
  servers instead get `logaddress_list_http` and, if the collector's URL is
  missing, `logaddress_add_http "<url>"` (`listen.http` on,
  `features.log_address.http`).
- **stats** every 10 minutes for `hlstats_server_load` (`gamewe_server_load`).

Without RCON, every 5 minutes (`features.server_info.interval`) the collector
also sends an `A2S_INFO` query (the server browser's query) to each server's
game port and stores the server version, operating system and VAC state
(`hlstats_Servers.server_version`, `server_os`, `server_vac`, shown on the
web's server lists); with RCON the `version` line of `status` fills in what
`A2S_INFO` does not tell.

Everything can be checked by hand (`-config` as for `run`):

```bash
gamewe status                                 # RCON status of all servers, as the collector reads it
gamewe status -server 3                       # one server: serverId or ip:port
gamewe logaddress                             # is the collector registered on every server?
gamewe logaddress -add                        # register it where it is missing
gamewe logaddress -server 198.51.100.7:27015 -address 203.0.113.4:41337 -add
gamewe logaddress -server 198.51.100.8:27015 -add  # a CS2 server: logaddress_add_http with listen.http (token shown as <token>)
gamewe rcon -server 3 sv_maxplayers           # any RCON command, prints the answer
gamewe serverinfo                             # A2S_INFO of all servers, store version, OS, VAC, max players (-dry-run: only show)
gamewe awards -date 2026-10-21                # decide the daily awards of a day again
```

### Collector jobs by hand and from cron

Every job of the collector that works on the database alone can be run from
the command line, whether its feature in the config file is on or off (each
asks first, `--dry-run` shows what it would do, `--force` does not ask):

| Job of the running collector | When | By hand |
|---|---|---|
| daily awards (`features.daily_awards`), with the cleanup of ping samples older than 90 days | daily at `time` | `gamewe awards [-date YYYY-MM-DD] [-game CODE] [-tz ZONE]` (default: yesterday; the ping cleanup only without `-game`) |
| move inactive players (`features.inactive_players`) | daily at `time` | `gamewe inactive` |
| bans of all known players into `hlstats_Bans` (`features.bans_sync`; needs Steam and the SourceBans list besides the database) | every minute, each account every `steam_recheck`, the list every `sourcebans.interval` | `gamewe bans` (one complete pass) |
| ranking reset queued by the web admin | every 10 s | `gamewe reset GAME -mode ... --force` ([below](#resetting-a-ranking)) |
| scheduled ranking reset with the hall of fame, when a period of a ranking's schedule has ended | checked every minute | `gamewe halloffame run GAME` ([below](#scheduled-resets-and-the-hall-of-fame)) |
| delete finished jobs older than 30 days | hourly | part of `gamewe db purge -older-than DAYS` |
| server info `A2S_INFO` (`features.server_info`) | every 5 min | `gamewe serverinfo [-server ...]` |
| log address (`features.log_address`) | every minute | `gamewe logaddress -add` |
| GeoIP update (`geoip.auto_update`) | monthly | `gamewe geoip-update` |

The daily awards, the daily move of inactive players and the scheduled
resets go through `hlstats_gamewe_jobs` like the jobs of the web admin
(`requested_by` `schedule`), so `gamewe jobs` and the web admin (page
Jobs) list every run with its result and the feature that started it
(column ORIGIN: e.g. `scheduled_resets, period 2026-10-01 - 2026-12-31,
at the collector's start` for a reset the collector queued at its start
because the period ended while it was stopped, `cli, gamewe reset css -mode
all` for a command; [DB-SEMANTICS.md](DB-SEMANTICS.md)). The web admin also shows when they
run next: the collector writes it with its heartbeat (`gamewe_next_*` in
`hlstats_Options`, [CONFIG.md](CONFIG.md)), because their times are in the
config file.

Not by hand, because they only make sense with the collector's own state in
memory (the players it tracks): the live view and `act_players` from RCON
status (`gamewe status` only shows it), `hlstats_server_load` every 10
minutes, ending idle visits, the heartbeat, reloading servers and settings.
`gamewe db purge` and `gamewe db optimize` exist only on the command line.

**One job at a time.** A ranking reset (also a scheduled one), the move of inactive players and the
daily awards take the same lock in the database (`GET_LOCK` on MariaDB /
MySQL, `pg_advisory_lock` on PostgreSQL), in the collector and in the
commands; a command waits for a job in progress up to `-wait` (default 10
minutes, then exit code 1 and nothing changed). So a cron run of
`gamewe awards` at the minute of the collector's own run cannot write a day
twice. `gamewe reset`, `gamewe inactive` and `gamewe halloffame run` hand the job
to a running collector (heartbeat less than 2 minutes old) through
`hlstats_gamewe_jobs` and wait for its result; without one they run it
themselves. While they wait they say what happens: the place in the queue
and the jobs ahead, when the collector started the job, its progress (a
move of inactive players writes "N of M players moved so far" into the job
every 5 s; run here, it prints the same) and, every 15 s without news, how
long it waits or runs. After `-wait` (default 10 minutes) the command gives
up waiting with exit code 1, but the collector goes on with the job.

The queue and the results:

```bash
gamewe jobs                       # the latest 20 jobs of all types, newest first
gamewe jobs -queue                # only pending and running, in the order the collector takes them
gamewe jobs -type inactive -n 5   # -type reset|scheduled-reset|inactive|awards, -status pending|running|done|failed (comma separated)
gamewe inactive -list             # the same as gamewe jobs -type inactive
gamewe reset -list                # the same as gamewe jobs -type reset,scheduled-reset
```

Finished jobs stay 30 days (the collector deletes older ones hourly, as
`gamewe db purge` does).

Cron (instead of, or in addition to, the features in the config file; with
`--force`, since cron has no terminal to ask on), as
`/etc/cron.d/gamewe`; the user `gamewe` can read the config file (`640
root:gamewe`), the output goes to the journal (`journalctl -t gamewe-awards`):

```
SHELL=/bin/sh
PATH=/usr/sbin:/usr/bin:/sbin:/bin
# the daily awards of yesterday at 00:30 (with features.daily_awards.enabled: false)
30 0 * * * gamewe /opt/gamewe/collector/bin/gamewe awards --force -config /etc/gamewe/collector/config.yaml 2>&1 | systemd-cat -t gamewe-awards
# inactive players at 01:00 (with features.inactive_players.enabled: false)
0 1 * * * gamewe /opt/gamewe/collector/bin/gamewe inactive --force -config /etc/gamewe/collector/config.yaml 2>&1 | systemd-cat -t gamewe-inactive
# reset the ranking css on the 1st of every month at 05:00, the players stay
# (without a hall of fame; for one use `gamewe halloffame schedule css month`)
0 5 1 * * gamewe /opt/gamewe/collector/bin/gamewe reset css -mode player-stats --force -config /etc/gamewe/collector/config.yaml 2>&1 | systemd-cat -t gamewe-reset
# the scheduled reset of css (hall of fame) if no collector runs at midnight
5 0 * * * gamewe /opt/gamewe/collector/bin/gamewe halloffame run css --force -config /etc/gamewe/collector/config.yaml 2>&1 | systemd-cat -t gamewe-halloffame
# events older than 180 days and old jobs, every Sunday at 04:00
0 4 * * 0 gamewe /opt/gamewe/collector/bin/gamewe db purge -older-than 180 --force -config /etc/gamewe/collector/config.yaml 2>&1 | systemd-cat -t gamewe-purge
```

Cron uses the system's time zone (cronie also reads `CRON_TZ=Europe/Berlin`).

### Games, servers and settings

The `gamewe` binary also changes the database like the web admin does (with
the collector's database user; `-config` as for `run`, `-dry-run` shows what
would change). The collector picks up every change within 60 seconds.
A server's ranking must exist before the server is added: create it with
`game add` first, then `server add -game CODE`.

```bash
gamewe game list                                   # games (rankings): realgame, engine, servers, players
gamewe game add css3 "CS:S GunGame" -template css  # a ranking with copies of css's teams, roles, actions,
                                                   #   weapons, awards, ranks and ribbons (and its realgame)
gamewe game add css3 "CS:S GunGame" -template css  # again: adds only what is missing
gamewe game set css3 -name "CS:S GunGame 24/7"    # only the given fields change
gamewe game set css3 -realgame css -order 2        # base game, position in the web's game list
gamewe game set css3 -hidden=true                  # the same as game hide css3
gamewe game set css3 -rank-bots=false              # bots of css3 out of the ranking (hides its bots now)
gamewe game set css3 -bot-points=false             # kills with bots give no points in css3
gamewe game set css3 -bot-kills=false              # kills of bots do not count for the career rank
gamewe game set css3 -bot-stats=true               # kills with bots count in the stats (kills, deaths, K/D)
gamewe game set css3 -block-vac=true               # kick players with a VAC ban on record (needs the Steam key)
gamewe game hide css                               # hide / show in the web's game list
gamewe game delete css3                            # only a game without servers and players;
                                                   #   default games (css, cstrike, tf, ...) are refused: hide them
gamewe server list                                 # the RCON password and the log secret only as set / -
gamewe server add 198.51.100.7:27315 -game css3 -name "GunGame" -rcon-password-file /root/rcon.txt
gamewe server set 198.51.100.7:27315 -game css1    # or the serverId; only the given fields change
gamewe server set 3 -logsecret 1234567890          # the log secret, a number 1..2147483647; put the same
                                                   #   sv_logsecret in the server's server.cfg
gamewe server set 3 -logsecret ""                  # no secret: all log packets of the server are accepted
gamewe server remove 3                             # players and statistics are kept
gamewe option list                                 # the collector settings with values and defaults
gamewe option get gamewe_command_display
gamewe option set gamewe_command_display both      # gamewe_* values are checked
```

### Output formats for scripts

`-o FORMAT` (or `--output FORMAT`) chooses how a command prints its result. It
can stand before the command (`gamewe -o json server list`) or among its flags
(`gamewe server list -o json`, `gamewe db status --output=yaml`); for `rcon`
it must come before the RCON command.

| Format | What it prints | Commands |
|--------|----------------|----------|
| `text` | the normal output (default) | all |
| `json` | one indented JSON document | all except `run` |
| `yaml` | the same document as YAML (`yml` works too) | all except `run` |
| `csv` | a header line and one line per item (RFC 4180) | the lists: `game list`, `server list`, `option list`, `status` (one line per player), `logaddress`, `awards`, `jobs`, `reset -list`, `inactive -list` |
| `tsv` | as `csv`, tab separated (tabs and line breaks in values become spaces) | the lists |
| `name` | only the first column, one per line: server id, game code, option key, job id | the lists |

```bash
gamewe server list -o json                     # [{"id": 3, "address": "198.51.100.7:27015", "rcon_password_set": true, ...}]
gamewe check -o yaml                           # config, database, schema, settings and servers as one document
gamewe config test -o json                     # the findings of the config file test; exit code 1 on errors
gamewe -o json db status                       # schema version, applied and pending migrations, steps
gamewe db migrate -n -o json                   # the plan; exit code 0 up to date, 2 pending, 1 error
gamewe status -o csv > players.csv             # the players of all servers, one line each
for id in $(gamewe server list -o name); do gamewe logaddress -server "$id" -o json; done
gamewe option get gamewe_kill_points -o json   # {"key": ..., "value": "5", "default": false, ...}
```

- Lists and `check`, `status`, `logaddress`, `awards`, `rcon` (`server_id`,
  `address`, `output`; the RCON command itself is not repeated), `db status`
  and `option get` print their data. Commands that change something (`game
  add`, `server set`, `option set`, `db migrate`, `db purge`, `reset`, ...)
  print `{"ok": ..., "error": ..., "messages": [...]}` with the lines of the
  text output, and where it applies the migration `plan`, the rows per table
  (`tables`) or the SQL of `-print` (`sql`).
- Questions (`[y/N]`, type the database name) and notes such as the root
  warning go to stderr then, so stdout holds only the document. Without a
  terminal give `--force` (`-f`) as with the text output.
- Errors go to stderr and the exit codes stay the same; `check`, `status`
  and `logaddress` print their document also when a server fails (with
  `error` per server, or `ok: false` and `error` for `check`).
- **Secrets are never printed, in no format:** the database password is not
  part of any output, the RCON password and the log secret only as
  `rcon_password_set` / `log_secret_set`. As a second guard every string under
  a key that names a password, secret, token or DSN is written as `***`.
- `csv` and `tsv` start a value beginning with `=`, `+`, `-` or `@` that is
  not a number with `'`, so a spreadsheet does not run player or server
  names as formulas.
- `csv`, `tsv` and `name` for a command that is not a list is an error (exit
  code 2) before the command does anything.

### Resetting a ranking

```bash
gamewe reset css3 -mode player-stats -dry-run  # rows per table that would be deleted / updated, nothing changes
gamewe reset css3 -mode player-stats   # player statistics only: players stay with the start skill, server statistics stay
gamewe reset css3 -mode all-stats      # player and server statistics; the players stay
gamewe reset css3 -mode all            # all statistics, and the players of css3 are deleted too
gamewe reset -list                     # the latest resets and their results (gamewe jobs: all jobs)
```

A template, a ranking without servers in `hlstats_Servers` (e.g. the
reference games `css`, `cstrike`, `tf` that new rankings copy their
definitions from), is not reset: `gamewe reset` refuses it, and so does the
collector for a reset job of a template (queued by an older web admin).

What each mode clears:

| | `player-stats` | `all-stats` | `all` |
| --- | --- | --- | --- |
| Player statistics: `hlstats_Events_*`, `hlstats_Players_History`, `hlstats_Players_{Weapons,Actions,Maps,Roles,Teams}` and their `_History_Months`, `hlstats_Awards_History`, the award winners, the totals summed from player events in `hlstats_Weapons`, `_Actions`, `_Teams`, `_Roles`, `_Maps`; skill (back to the start skill) and counters of `hlstats_Players`, `hlstats_PlayersInactive`, `hlstats_PlayerNames` | yes | yes | yes |
| Server statistics: the counters of `hlstats_Servers` (`kills`, `players`, `rounds`, `suicides`, `headshots`, `bombs_*`, `ct_wins`/`ts_wins`, `act_players`, `map_*`, shots and hits), `hlstats_server_load` (load history), `hlstats_Trend` | no | yes | yes |
| Players: `hlstats_Players`, `hlstats_PlayersInactive`, `hlstats_PlayerNames`, `hlstats_PlayerUniqueIds`, `hlstats_Players_Data`, `hlstats_Players_Resets`, `hlstats_Clans` | kept | kept | deleted |

The live view (`hlstats_Livestats`) is cleared in every mode: the collector
forgets the players of the paused servers and adds them again with their next
line. `stats`, the name of `player-stats` before collector 0.2, still works
(also for jobs queued by an older web); before, it reset the server
statistics too.

Games, servers, teams, weapons, actions, awards and settings stay; other
games are not touched. It asks for the game code to confirm (`--force` skips
that). With a running collector (table `hlstats_gamewe_jobs` present, heartbeat
less than 2 minutes old) the reset is queued for it in `hlstats_gamewe_jobs` (as the web
admin does): it pauses the servers of that game between two log lines,
forgets their players in memory, resets, clears the live view and goes on;
`gamewe reset` waits for the result. Without one it runs directly (then no
collector may run: it keeps player ids in memory). A reset can be run again
if it was interrupted.

### Scheduled resets and the hall of fame

A ranking can be reset on a schedule: every day, week, month, quarter or
year, or every N of them (every 2 months, every 3 days; at most 120). One
schedule can hold for all rankings (e.g. every quarter); a ranking follows it
unless it has its own or is set to `off`. When a period ends, the collector
first stores the best five players of the period in
`hlstats_gamewe_hall_of_fame` and then resets the ranking. The web shows the
first three on the page "Hall of Fame" of the ranking, with stars for every
place (gold, silver, bronze) on the page and in the player profiles; places
4 and 5 move up when an admin removes an entry or hides its player. The
entries stay; every period adds up to five rows (collector 0.19: three).

```bash
gamewe halloffame schedule css                      # the schedule of css, the period now running and the next reset
gamewe halloffame schedule css month                # monthly; also: day, week, quarter, year, "2 month", "3 day"
gamewe halloffame schedule css "2 month" -mode all-stats   # every two months, players and server statistics
gamewe halloffame schedule css month@15             # monthly from the 15th; also "quarter@15", "week@5" (Friday), "year@07-01"
gamewe halloffame schedule css off                  # no scheduled resets for css, even with a schedule for all (the entries stay)
gamewe halloffame schedule css inherit              # follow the schedule for all rankings again
gamewe halloffame schedule -all quarter             # every quarter for all rankings without their own; -all alone shows it
gamewe halloffame run css -dry-run                  # the period that has ended, its best five and the rows the reset clears
gamewe halloffame run css                           # do it now (also from cron, see above)
gamewe halloffame                                   # all entries, newest period first; `gamewe halloffame css` for one ranking
```

- **The periods** follow the calendar in the collector's `timezone`
  ([CONFIG.md](CONFIG.md#timezone)): a day starts at midnight, a week on
  Monday, a month on the 1st, a quarter on January, April, July and October
  1st, a year on January 1st. A start day after `@` moves that: `month@15`
  runs from the 15th to the 14th, `quarter@15` from January, April, July and
  October 15th, `week@3` from Wednesday, `year@07-01` from July 1st (days 1 to
  28, so every month has it). The time of day is
  `features.scheduled_resets.time` in config.yaml (default 00:00,
  [CONFIG.md](CONFIG.md#features)). Every N of them is counted from fixed
  dates, so every 2 months always ends with even months (Jan–Feb, Mar–Apr,
  ...), every 2 weeks on the same Mondays, no matter when the schedule was
  set.
- **A new schedule** starts with the period now running: the first reset is
  at its end, the statistics before count for that period. That holds for a
  changed schedule for all rankings too: every ranking that follows it starts
  again, none is reset at once. A changed `features.scheduled_resets.time`
  or `timezone` keeps the period now running as well: the collector writes
  its start again with the new time of day (log "reset time or time zone
  changed, the running period stays") and resets at its end, not at once.
- **The best five** have the most points (skill), then the most kills. Only
  players with a kill or a death count; hidden players (`hideranking`, also
  hidden by an admin) and bots do not. A row keeps the values of the player
  at the end of the period: name, flag, points, kills, deaths, headshots,
  shots and hits, best kill streak, connection time, favourite weapon and the
  rank (`hlstats_Definition_Ranks_Ribbons`, by kills). A player hidden later
  stays in the table but the web does not show the entry; the next one moves up.
- **Editing:** the web admin (ranking → hall of fame → "Edit entries") can
  remove an entry or a whole period and bring it back (column `removed`,
  migration 16); the rows themselves are never changed or deleted. The
  podium shows the first three entries that are neither removed nor hidden;
  with fewer the places stay empty. Stars are counted from the podiums.
- **The mode** (`-mode`, default `player-stats`) is that of `gamewe reset`
  ([above](#resetting-a-ranking)).
- **The collector** checks the schedules every minute and queues the reset as
  a job (`requested_by` = `schedule`, `gamewe reset -list` and `gamewe jobs` show it); a
  collector that was stopped over the end of a period does it at its next
  start, once, for the period that ended last (several missed periods are
  one reset, the best five then stand for all of them). Without a running
  collector `gamewe halloffame run` does it.
- **Templates** (rankings without servers, they only hold the definitions
  for new rankings) have no schedule: the schedule for all rankings leaves
  them out, a schedule of their own is ignored, `gamewe halloffame schedule`
  and `run` refuse them, and the job plan (`gamewe_next_reset_<game>`) does
  not list them. A template that gets its first server follows the schedule
  from then on, starting with the period then running.
- **Turned off and on again:** while a ranking has no schedule (its own is
  `off`, or the schedule for all rankings is off) the collector forgets its
  running period. Turning the schedule on again starts with the period then
  running; the time it was off is never reset afterwards. The web admin
  (Reset ranking → "Scheduled reset for all rankings"; before web 1.4.0
  under Rankings) has a switch for this and
  keeps the schedule while it is off (`gamewe_reset_paused`).
- **Where it is stored:** `hlstats_Options` (`gamewe_reset_schedule`,
  `gamewe_reset_mode` for all rankings, `gamewe_reset_schedule_<game>`,
  `gamewe_reset_mode_<game>`, `gamewe_reset_last_<game>`;
  [CONFIG.md](CONFIG.md#settings-in-hlstats_options)), set by `gamewe
  halloffame schedule` or the web admin. `gamewe_hof_<game>` turns the web
  page on (`1`) or off (`0`); without it the page is shown when the ranking
  has a schedule or entries.

Codes are 1-10 characters `a-z 0-9 _`. `-rcon-password PW` works too but is
visible in the process list; `-rcon-password-file -` reads it from stdin. The
RCON password is never printed.
