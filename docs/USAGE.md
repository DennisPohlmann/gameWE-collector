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
- Skill never drops below 0. Bots are recorded but not ranked, and kills with
  bots change nobody's skill (`gamewe_rank_bots`).
- VAC-banned players (disconnect reason) and permanent bans (`Banid: ... "permanently"`)
  are hidden from the rankings (`hideranking` = 2).

What the collector writes into which table, for anyone reading the database
(e.g. the web frontend), is in [DB-SEMANTICS.md](DB-SEMANTICS.md).

Daily awards (most kills with a weapon, most actions, most kills, longest play
time, ...) are decided every day for the previous day, by default at 00:00
UTC; time and time zone are `features.daily_awards` in the config file; see
[DB-SEMANTICS.md](DB-SEMANTICS.md#daily-awards-ribbons-ranks). To decide a day
again (after changing the awards in the web admin):

```bash
sudo -u gamewe /opt/gamewe/collector/bin/gamewe awards -date 2026-10-21
```

## Command line

`gamewe <command>`; every command takes `-config FILE` (default
`/etc/gamewe/collector/config.yaml`), `gamewe help` lists them all.

| Command | What it does |
|---------|--------------|
| `gamewe run` | the collector (systemd starts it); migrates an outdated database schema at start (`database.auto_migrate`), with `auto_migrate: false` it refuses to start (exit code 1) until `gamewe db migrate` ran |
| `gamewe check` | config, database connection and TLS, schema version and pending migrations, game servers and settings |
| `gamewe db migrate` | create the tables of a new database, bring an older one to this version (below); `gamewe run` does it at start with `database.auto_migrate` |
| `gamewe db status`, `gamewe db grants`, `gamewe db seed` | schema version and migrations; the web users' rights again; missing reference data |
| `gamewe db init`, `purge`, `optimize`, `reset`, `drop` | tables of a new database; delete old events; give the space back; empty everything; drop the tables (below) |
| `gamewe -V` / `--version` / `version` | the version |
| `gamewe status`, `gamewe logaddress`, `gamewe rcon` | RCON status, log address registration, any RCON command (below) |
| `gamewe game`, `gamewe server`, `gamewe option` | games, servers and settings like the web admin (below) |
| `gamewe reset GAME -mode player-stats\|all-stats\|all` | reset a ranking (below) |
| `gamewe awards -date YYYY-MM-DD` | decide the daily awards of a day again |
| `gamewe geoip-update` | download the DB-IP database now |
| `gamewe simulate` | replay a sample game into the database or to a running collector |

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
gamewe db purge -older-than 90d  # delete the events older than 90 days (-dry-run: count only; asks [y/N], -yes)
gamewe db optimize               # give the space of deleted rows back (OPTIMIZE TABLE / VACUUM ANALYZE)
gamewe db reset                  # empty every table, reference data again: as new (asks for the database name, -yes)
gamewe db drop                   # drop all gameWE tables (-print: the SQL for a database admin; MariaDB needs DROP)
```

`reset` and `drop` refuse while a collector runs on the database; `purge`
keeps players, totals, rankings and awards. What each one touches and the
rights they need: [DATABASE.md](DATABASE.md#maintenance-gamewe-db).

`-f` and `-n` together are an error. Without a terminal on stdin (cron,
scripts, `docker compose run -T`) `gamewe db migrate` does not wait for an
answer: it stops with a message unless `-f` is given. Stop the collector
while migrating; the migration takes a lock, so a second run at the same
time waits and then finds nothing to do.

### RCON jobs and checks by hand

With the game server's `rcon_password` in `hlstats_Servers` the collector talks
to every server over RCON (switches in `features:` of the config file, all on by
default):

- **status** every 10 seconds (`features.rcon_status.interval`): players and
  bots in the live view and `act_players` (also on an empty server and right
  after a collector restart, without any log line), the map, maximum players,
  the name if none is set, pings. One connection per server stays open.
- **log address** every minute (`features.log_address.interval`): `logaddress_list`; if the collector is missing,
  `logaddress_add <ip:port>` and `log on`. The address is
  `listen.public_address`, or the collector's address of the RCON connection
  with the listen port. So a game server that restarted without
  `logaddress_add` in its config is back within that interval.
- **log secret** (`features.log_secret: auto`, the default) at the same
  interval: `sv_logsecret` is read; a server without a secret (or with an
  older one than `features.log_secret.interval`, default 24 hours) gets a new
  random one (set over RCON first, then stored in `hlstats_Servers.log_secret`),
  a server that lost it (restart) gets it again. Packets without the secret
  wait up to 30 seconds while the collector asks the server whether it
  restarted (lines kept) or the packets are forged (dropped, warning in the
  journal); details in CONFIG.md, "Log secret". GoldSrc servers are not
  checked.
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
gamewe game hide css                               # hide / show in the web's game list
gamewe game delete css3                            # only a game without servers and players;
                                                   #   default games (css, cstrike, tf, ...) are refused: hide them
gamewe server list                                 # the RCON password and the log secret only as set / -
gamewe server add 198.51.100.7:27315 -game css3 -name "GunGame" -rcon-password-file /root/rcon.txt
gamewe server set 198.51.100.7:27315 -game css1    # or the serverId; only the given fields change
gamewe server set 3 -logsecret 1234567890          # the log secret (sv_logsecret), a number 1..2147483647
gamewe server set 3 -logsecret auto                # a random one, stored and set over RCON
gamewe server set 3 -logsecret ""                  # no secret (with features.log_secret auto the collector sets a new one)
gamewe server remove 3                             # players and statistics are kept
gamewe option list                                 # the collector settings with values and defaults
gamewe option get gamewe_command_display
gamewe option set gamewe_command_display both      # gamewe_* values are checked
```

### Resetting a ranking

```bash
gamewe reset css3 -mode player-stats -dry-run  # rows per table that would be deleted / updated, nothing changes
gamewe reset css3 -mode player-stats   # player statistics only: players stay with the start skill, server statistics stay
gamewe reset css3 -mode all-stats      # player and server statistics; the players stay
gamewe reset css3 -mode all            # all statistics, and the players of css3 are deleted too
gamewe reset -list                     # the latest resets and their results
```

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
