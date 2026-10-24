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
- **log address** every minute (`features.log_address.interval`): `logaddress_list`; if the collector is missing,
  `logaddress_add <ip:port>` and `log on`. The address is
  `listen.public_address`, or the collector's address of the RCON connection
  with the listen port. So a game server that restarted without
  `logaddress_add` in its config is back within that interval.
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
