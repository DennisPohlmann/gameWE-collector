# gameWE - collector: what is written where

For everybody who reads the HLstatsX database that the collector writes, such as
the web frontend. The structure is
`internal/schema/mysql/schema.sql` (MariaDB / MySQL) or
`internal/schema/postgres/schema.sql` (PostgreSQL), compiled into the
collector and created by `gamewe db migrate`. Both hold the same data; the differences of the PostgreSQL
variant are listed in [PostgreSQL](#postgresql) at the end.

## Conventions

- **Hidden from the web user:** the read-only web user `gamewe_web` cannot read
  `hlstats_Servers.rcon_password`, `log_secret`, `log_secret_changed`, `hlstats_Events_Connects.ipAddress` (column
  rights: name the columns you need, `SELECT *` on those tables is refused),
  `hlstats_Users` and `hlstats_Events_Rcon`. `gamewe_web_admin` reads all of them.
- **Character set:** all tables are **utf8mb4** (`utf8mb4_unicode_ci`). Readers must connect with
  `utf8mb4`. Player names, chat and map names can contain any Unicode
  character, including emoji. PostgreSQL databases are UTF8.

- **`timestamp_id`** in all `hlstats_Events_*` tables is the **Unix time in
  seconds (UTC)** of the event, as received by the collector. This is confirmed
  for the production data (e.g. 1729343366 = 2024-10-19). There is no timestamps
  table. Rows are **not** stored strictly in time order (several servers write
  in parallel), so always sort by `timestamp_id` and never by row order.
- **`game_id`** is `hlstats_Games.id`. **`game`** (char) is `hlstats_Games.code`.
  Both come from the server's `hlstats_Servers.game`.
- **`map`** in the event tables is `hlstats_Maps.id` of the map running on the
  server (from `Loading map` / `Started map` lines, or `hlstats_Servers.act_map`
  at startup). It is `0` until the map is known.
- **`weapon`** in the event tables is `hlstats_Weapons.weaponId`.
- **`server_id`** (`hlstats_Servers.serverId`) is in most event tables, see
  [Tables the collector writes](#tables-the-collector-writes).
- **Players are per game.** One SteamID gets one `hlstats_Players` row per game,
  linked through `hlstats_PlayerUniqueIds (uniqueId, game)`. `uniqueId` is the
  SteamID without `STEAM_x:` (`STEAM_1:0:12345` and `STEAM_0:0:12345` both become
  `0:12345`), the format used in the production database (confirmed). Steam3
  ids get the same form (`[U:1:24691]` = `STEAM_0:1:12345` → `1:12345`). It is at
  most 64 characters. Bots are `BOT:<name>` and are ignored by default.
- **hideranking:** 0 = ranked; 1 = hidden by an admin, or set by the
  collector for new bots (unless `gamewe_rank_bots` = 1); 2 = banned, set by
  the collector for VAC-ban disconnects and permanent bans
  (`Banid: ... banned "permanently"`); 3 = hidden by the player with
  `!hideranking` (HLstatsX used 3 for "inactive", which gameWE
  does not set). The player switches only between 0 and 3; 1 and 2 are never
  changed by the command, and a ban turns 3 into 2. Every value but 0 leaves
  the player out of the rankings, rank numbers and awards; the value stays
  when the player comes back.
- **Bots** (always recorded; `gamewe_rank_bots` decides only the rankings): uniqueId `BOT:<name>`, so one
  row per bot name and game. They are in the live view, `act_players` (and
  `act_bots`) and their own statistics, and get `hideranking` = 1 when created. With `gamewe_rank_bots`
  = 0 kills with a bot (either side) and player-vs-player actions with a bot
  change nobody's skill (stored with 0 points, no headshot action, no kill
  streak); kills and deaths are still counted. Awards skip them (hideranking).
  Changing `gamewe_rank_bots` later does not change existing bot rows: set their
  `hideranking` in the web admin. Servers of one game with the same bot names
  share these rows, also the live row (one per player): the bot is live on the
  server that saw it last; `act_bots` counts per server. `hlstats_Servers.players` counts the game's players without
  `hideranking = 2`.
- **Day / week:** `hlstats_Players_History.eventTime` is the UTC date.
  `yearweek_id` is MySQL `YEARWEEK(date)` (default mode 0, weeks start on Sunday).
- Counters only ever grow by relative updates, and skill never goes below 0.

## Skill (points)

`hlstats_Players.skill` is the ranking value. New players start with
`gamewe_start_skill` from `hlstats_Options` (default 1000).

| Event | Skill change |
|-------|--------------|
| Kill | killer `+round(gamewe_kill_points × weapon.modifier × victim.skill / killer.skill)`, clamped to 1 … 4 × gamewe_kill_points, `+gamewe_headshot_bonus` for a headshot. The victim loses the same amount, without the headshot bonus. |
| Teamkill | killer `−gamewe_teamkill_penalty`, victim unchanged |
| Suicide | `−gamewe_suicide_penalty`; not counted at all within 2 s after the player changed teams (CS:S logs a suicide for a team switch) |
| Player action | `+hlstats_Actions.reward_player`, if the action exists for the game with `for_PlayerActions = '1'`. Besides the logged triggers (Planted_The_Bomb, Defused_The_Bomb, Got_The_Bomb, mvp, gg_levelup, ...), the collector generates, as HLstatsX does: `headshot` once per headshot kill (the plugin's own `triggered "headshot"` line and the kill line's `(headshot)` count once), and `kill_streak_<n>` once when a streak of n ≥ 2 kills in one life ends (death, suicide, disconnect, round end, map change; n at most 12) |
| Player vs. player action | the player gets `+hlstats_Actions.reward_player`, the victim loses it (not below 0), for lines like `"A<..>" triggered "domination" against "B<..>"` (also revenge), if the action exists with `for_PlayerPlayerActions = '1'` |
| Round won | each player of the winning team `+hlstats_Actions.reward_team` of the win trigger, if it exists with `for_TeamActions = '1'` |

The `gamewe_*` values are `hlstats_Options` keys. Defaults: kill_points 5,
headshot_bonus 0 (headshots are rewarded by the `headshot` action), teamkill_penalty 10, suicide_penalty 5 (see CONFIG.md). The
collector re-reads them every minute.
`last_skill_change` is the change of the latest event.
`hlstats_Players_History.skill_change` is the sum for the day.

Ranks shown in game: players of the same `game` with `hideranking = 0`, ordered
by `skill`. The collector does not write `hlstats_Players.rank`, `sort_skill` or
`sort_kpd` (see below).

## Tables the collector writes

**`server_id`** (= `hlstats_Servers.serverId`) is in these event tables:
`hlstats_Events_Frags`, `_Teamkills`, `_Suicides`,
`_PlayerActions`, `_PlayerPlayerActions`, `_TeamBonuses`, `_Entries`,
`_Disconnects`, `_ChangeTeam` (with an index
`(server_id, timestamp_id)` for the latest events of a server), `_Chat` and
`_Latency` (not in `_Connects`, `_ChangeName`, `_ChangeRole`, `_Locations`);
join it on `hlstats_Servers.serverId`. Rows written by HLstatsX have `0`; on a
database without the column the collector writes the events without it
(`gamewe check` reports the missing column).

| Table | Written when | Columns / meaning |
|-------|--------------|-------------------|
| `hlstats_Servers` | read every minute: the list of accepted game servers (`address`, `port`, `game`, `name`, `rcon_password`, `log_secret`). Never created by the collector (only `gamewe simulate` adds its test server). Counters written on kills, suicides, rounds, map change, weaponstats, connect/disconnect | `kills`, `headshots`, `suicides`, `rounds`, `ct_wins`, `ts_wins`, `bombs_planted`, `bombs_defused`, `act_players` (players currently tracked: one per SteamID / bot name, never more than `max_players` of the last RCON status; the longest silent visits end when more come), `act_bots` (the bots among them, counted the same way and written with `act_players`; held with it during a map change), `players` (players of the game without `hideranking = 2`, every minute), `act_map`, `map_started` (Unix time), `map_changes`, `map_rounds`, `map_ct_wins`, `map_ts_wins`, `ct_shots`/`ct_hits`/`ts_shots`/`ts_hits` and the `map_` variants (from weaponstats, by the player's team; the `map_` values reset on map change). `server_version`, `server_os`, `server_vac` from `A2S_INFO` and RCON status (see "Server info" below). `name`, `game`, `address`, `port`, `rcon_password`, `log_secret` and `log_secret_changed` are never written (the log secret is set by hand, CONFIG.md, "Log secret"). |
| `hlstats_Games` | read only (`gamewe game` writes) | `code` → `id`; `realgame` → the base game and engine (GAMES.md) |
| `hlstats_Options` | read every minute; `gamewe_heartbeat` written every 60 s | the collector's settings (`gamewe_*` keys); `gamewe_heartbeat` = Unix time of the running collector's last poll (fresh = less than 2 minutes: queued jobs will be done) |
| `hlstats_gamewe_jobs` | queued by the web admin / `gamewe reset` / `gamewe inactive`, polled every 10 s | `job_type` `reset` with `game` and `mode` `player-stats`, `all-stats` or `all` (the old `stats` is run as `player-stats`; see [USAGE](USAGE.md#resetting-a-ranking) for the tables of each mode), or `inactive` (the move of inactive players of all games, `game` and `mode` empty; `result` = how many were moved); one job at a time, under the job lock that `gamewe awards` takes too ([USAGE](USAGE.md#collector-jobs-by-hand-and-from-cron)), `status` `pending` → `running` → `done` / `failed`, `started_at`, `finished_at` (Unix time), `result` (rows per table, or the error). A second job for a game with one pending or running is `failed` ("refused"); a job left `running` by a stopped collector is done again at the next start; finished jobs are deleted after 30 days |
| `hlstats_Maps` | map change, kill, round | a row per (game, map) is created when missing; `played`, `kills`, `headshots`, `teamkills`, `rounds` |
| `hlstats_Weapons` | kill | a row per (game, code) is created when missing (`name` = code, `modifier` 1.00); `kills`, `headshots` |
| `hlstats_Teams` | team change, kill, round, end of a visit | a row per (game, code) is created when missing (`name` = code); `time_played` (seconds players spent in the team, stored on a team change and when the visit ends: disconnect, map change, idle, status, shutdown; not spectator / unassigned), `kills`, `headshots` (killer's team), `deaths` (victim's team), `rounds`, `wins`, `losses` (once per team and won round), `survived` (players of the team alive at the round end). Not written: `kill_assists`, `flags_captured`, `custom_wins`. "Joined" is `COUNT(*)` of `hlstats_Events_ChangeTeam` per team |
| `hlstats_Roles` | role change (plugin: CS:S/CS:GO player models, TF2 classes), kill, round, end of a visit | a row per (game, code) is created when missing (`name` = code); `time_played` (seconds in the role, as for teams; not while spectator), `kills`, `headshots` of the killer's current role, `deaths` of the victim's, `rounds`, `wins`, `losses`, `survived` (per player in the role and won round). Not written: `kill_assists`, `flags_captured`, `custom_wins`. Picks are `COUNT(*)` of `hlstats_Events_ChangeRole` |
| `hlstats_Clans` | a player with a clan tag in the name is first seen or renames | a clan per (game, tag) is created when missing: `tag` as matched (`[ZOOM]`), `name` the letters of the pattern (`ZOOM`); from the CS:GO plugin's `clantag` trigger `name` = `tag` |
| `hlstats_Actions` | player action, round win | read for rewards; `count` + 1 per use |
| `hlstats_Players` | every player event | created per game with `skill` = start_points; `clan` = `hlstats_Clans.clanId` when the name has a clan tag matching `hlstats_ClanTags` (set on first sight and rename; a name without tag leaves the clan as it is); `hideranking` = 2 for banned players; `kills`, `deaths`, `suicides`, `headshots`, `death_headshots`, `team_kills`, `team_killed`, `skill`, `last_skill_change`, `kill_streak` / `death_streak` (best streak, current streak tracked in memory), `hpk` = headshots/kills, `kpd` = kills/deaths, `death_hpk`, `shots`, `hits` (weaponstats), `wins`, `losses`, `rounds`, `connection_time` (seconds, added on disconnect), `last_event` (Unix time of the player's latest event, connect, visit start or end) |
| `hlstats_PlayersInactive` | daily with `features.inactive_players` (off by default); a known player returns | the same columns as `hlstats_Players`. Once a day at `features.inactive_players.time` the rows of players whose `last_event` is older than `MinActivity` days (`hlstats_Options`, default 28) and who have no `hlstats_Livestats` row (bots included) are moved here unchanged, 500 per batch; nothing else of the player is deleted (`hlstats_PlayerUniqueIds`, `_Players_Data`, `_PlayerNames`, events and history stay). When the player is seen again (first log line, RCON status) the row is moved back to `hlstats_Players` unchanged, also when the move happened while the player was being looked up (the visit start checks it). If a player is in both tables (a crash between the steps), the row in `hlstats_Players` counts and the copy is removed at the next move. Readers of a player by id look in both tables; rankings, totals and `hlstats_Servers.players` count only `hlstats_Players`. |
| `hlstats_PlayerUniqueIds` | new player | (`playerId`, `uniqueId`, `game`) |
| `hlstats_Players_Data` | new player, connect, rename | `first_connect`, `last_connect` (Unix time), `total_connects`, `lastName` (the name shown in game) |
| `hlstats_PlayerNames` | first seen on a server, rename, kills, disconnect | per (player, name): `numuses` + 1 per visit, `lastuse` (UTC datetime), `connection_time`, `kills`, `deaths`, `suicides`, `headshots`, `shots`, `hits` |
| `hlstats_Players_History` | every scoring event, round end, disconnect | one row per (player, UTC day, game): `kills`, `deaths`, `suicides`, `headshots`, `team_kills`, `shots`, `hits`, `connection_time`, `rounds`, `wins`, `losses` added; `kill_streak`, `death_streak` = best of the day; `skill` = latest; `skill_change` = sum |
| `hlstats_Players_Weapons` | plugin `weaponstats` / `weaponstats2` | one row per (player, `YEARWEEK`, game, weapon): `shots`, `hits`, `headshots`, `damage`, `kills`, `deaths`, `head` … `rightleg` added (the plugin sends deltas). The plugin's `tks` has no column and is dropped. Counter-Strike 2 (no plugin): `damage` and the areas, plus `neck` (only CS2 reports it), summed from the game's `attacked` lines per round (`features.cs2_hitgroups`), and `kills`, `headshots`, `deaths` from its kill lines (the victim's death under the killer's weapon); `shots` and `hits` stay 0. |
| `hlstats_Players_Maps` | plugin `weaponstats` / `weaponstats2` on a known map | one row per (player, `YEARWEEK`, game, map): only `shots` and `hits` added (the web's accuracy per map); the other columns are not written |
| `hlstats_Players_Actions` | player action with a reward | one row per (player, `YEARWEEK`, game, action): `count`, `points` |
| `hlstats_Livestats` | first seen, kill, team change, weaponstats, round, disconnect | **who is online now**: one row per player on a server (`player_id`, `server_id`, `unique_id`, `name`, `team`, `connected` = Unix time of the first sighting, or of the connect from RCON status), `is_dead` (1 after the player was killed or killed himself, 0 at `Round_Start`), `has_bomb` (1 after `Got_The_Bomb`, 0 after `Dropped_The_Bomb` / `Planted_The_Bomb`, death, round start), with the visit's `kills`, `deaths`, `suicides`, `headshots`, `death_headshots`, `team_kills`, `team_killed`, `shots`, `hits`, `skill`, `skill_change`, `kill_streak`, `death_streak`, `wins`, `losses`, `rounds`. The row is also created from RCON status (see there). The rows of a server are deleted at a map change (the players come back with their next line or status poll). The row is deleted when the player disconnects, when RCON status has not listed the player twice in a row, or when the collector has seen no line of the player for `gamewe_idle_timeout` seconds (lost disconnect). All rows of a server are deleted when the collector starts or stops. It is a MEMORY table, so it is also empty after a database restart. The primary key is `player_id` alone, so a player is live on one server at a time: if the same player shows up on a second server before leaving the first (e.g. when switching servers), the row moves to the newer server, and leaving the old server does not delete it. |
| `hlstats_Events_Connects` | `connected` line, or RCON status for a player already on the server whose connect line the collector did not see (collector started or server added meanwhile; once until the player's disconnect line, not again after a map change) | `ipAddress` = IPv4 as an integer with `gamewe_store_player_ip` = 1 (the default), otherwise 0 (IPv6: 0). It is the only place the collector stores a player's IP; the latest row of a player is the last IP. `gamewe_web` cannot read the column, `gamewe_web_admin` can. `hlstats_Players_Data.last_address_id` is never written; `hostname`, `hostgroup` empty |
| `hlstats_Events_Entries` | `entered the game` line | |
| `hlstats_Events_Disconnects` | `disconnected` line | not written when the collector shuts down |
| `hlstats_Events_ChangeName` | `changed name to` | `oldName`, `newName` |
| `hlstats_Events_ChangeTeam` | `joined team` (not for Unassigned / Spectator) | `team` = `hlstats_Teams.teamId` |
| `hlstats_Players` rounds | a round **with a winner** (a team trigger of `gamewe_win_triggers`, e.g. `CTs_Win`, `Terrorists_Win`, `Target_Bombed`; TF2 `Round_Win`) | every player on a team (not spectators or unassigned) gets `rounds` + 1 and `wins` or `losses` + 1; `survived_count` + 1 for those who did not die since `Round_Start`, `survived` = `survived_count` / `rounds` in percent. The same in `hlstats_Livestats` and `hlstats_Players_History` (per day). Rounds without a winner (draws, a GunGame or deathmatch server that logs no win trigger) are not counted. |
| `hlstats_Events_Frags` | kill (not teamkill) | `killerId`, `victimId`, `weapon`, `headshot`, `map` (`hlstats_Maps.id`, a number, not the name); `pos_x`, `pos_y`, `pos_z` (killer) and `pos_victim_x`, `pos_victim_y`, `pos_victim_z` (victim): map units (Hammer units), rounded, NULL when no position is logged. Sources: the kill line (CS:GO `[x y z]`; TF2 and the AMX plugin on cstrike `(attacker_position ...)`) or the SourceMod plugin's `located on "kill"` line right after the kill (CS:S, DoD:S, HL2DM, Insurgency; `gamewe_log_locations` of the plugin). Teamkills (`hlstats_Events_Teamkills`) and suicides have no position columns; their positions are only in `hlstats_Events_Locations`. Heatmap of a map: `SELECT FLOOR(pos_victim_x / 64.0) AS gx, FLOOR(pos_victim_y / 64.0) AS gy, COUNT(*) AS n FROM hlstats_Events_Frags WHERE map = ? AND pos_victim_x IS NOT NULL GROUP BY 1, 2` (killer positions: `pos_x`, `pos_y`); uses the index on `map`. |
| `hlstats_Events_Teamkills` | teamkill (same, non-neutral team) | `killerId`, `victimId`, `weapon` |
| `hlstats_Events_Suicides` | suicide (also a kill with killer = victim) | `weapon` |
| `hlstats_Events_PlayerActions` | player action defined in `hlstats_Actions` (also the generated `headshot` and `kill_streak_<n>`) | `actionId`, `bonus` |
| `hlstats_Events_PlayerPlayerActions` | `triggered "…" against "…"` with an action defined for player-vs-player | `playerId`, `victimId`, `actionId`, `bonus` |
| `hlstats_Events_TeamBonuses` | round won, if the trigger has a non-zero `reward_team` | one row per winning player: `actionId`, `bonus` |
| `hlstats_Events_ChangeRole` | `changed role to` (plugin) | `role` = `hlstats_Roles.roleId` |
| `hlstats_Events_Latency` | RCON status | one ping sample per human player and minute online, if the ping is above 0 (no bots, no samples of 0 such as LAN): `timestamp_id`, `game_id`, `server_id`, `map` (`hlstats_Maps.id`), `playerId`, `ping` (ms). Each sample also updates the player's running average `hlstats_Players_Data.average_ping` / `average_ping_count` (the count stops at 65535). Samples older than 90 days are deleted by the daily awards run. |
| `hlstats_Events_Locations` | plugin location lines (`located on "kill"` / `"suicide"`), with `gamewe_store_locations` = 1 | one row per position: `day` = days since 1970-01-01 (UTC), `map` = `hlstats_Maps.id` (0 if unknown), `type` 1 = where a killer stood, 2 = where a player died (killed or suicide), `playerId`, `loc_1` = x, `loc_2` = y (map units, rounded) |
| `hlstats_Events_Chat` | chat, with `gamewe_store_chat` = 1 (the default); readable by `gamewe_web_admin` only | `server_id`, `message_mode` 1 = say, 2 = say_team, `message` (commands such as `!rank` included) |

## RCON status

With `features.rcon_status` (config file, default every 10 s) the collector
polls `status` on every server with an `rcon_password` and applies it in
order with the server's log lines:

| Field | From status |
|-------|-------------|
| `hlstats_Servers.act_map`, `map_started` | the map, when it differs from the current one; counted as a map start (`map_changes`, `hlstats_Maps.played`) once, the `Started map` log line of the same change is not counted again |
| `hlstats_Servers.max_players` | the `(N max)` of the players line, written when it changes |
| `hlstats_Servers.name` | the hostname, only while the name is empty |
| `hlstats_Servers.act_players` | humans and bots listed. While the last status is at most 2 minutes old and of the current map, its count is the player count (also for `hlstats_server_load`); players the log lines bring back between two polls do not add to it |
| `hlstats_Servers.act_bots` | the listed players with uniqueid `BOT` (any case; Source, CS:GO and GoldSrc list bots so, and SourceTV too, as the engine's "N bots" does). Without a fresh status: the tracked players with the `BOT` tag of their log lines |
| `hlstats_Livestats` | a row for every player and bot listed that has none yet, as on a first log line: `hlstats_Players` / `hlstats_PlayerUniqueIds` are created if needed (bots `BOT:<name>`), `connected` = now minus the connected time of status (bots: now). `ping` is written when it changes. `team` is not in status: it is empty until the player's next log line, whose player tag sets it. |
| `hlstats_Events_Connects`, `hlstats_Players_Data.total_connects`, `last_connect` | a connect with the `adr` address for a player whose `connected` line was not seen (joined before the collector started or before the server was added), once until the player's disconnect line; bots and players without an address get none |
| `hlstats_Events_Latency`, `hlstats_Players_Data.average_ping` | the ping, one sample per player and minute (see the table above) |
| `hlstats_Players_Data.location_id`, `hlstats_Livestats.cli_location_id` | from the address in the `adr` column (see Locations), once per visit and address, also for players who joined before the collector started |
| end of a visit | a player missing from 2 polls in a row (about 20 s) is ended like an idle player: connection time counted, live row deleted, no `hlstats_Events_Disconnects` row. A player whose disconnect was logged after a poll that still listed them is not added again. |

Status counts as a sign of life for `hlstats_server_load`, so an empty server
gets its rows too. Nothing is written when a poll changes nothing. Without
`rcon_password` or with `rcon_status` off only the log lines fill these fields.

## Server info

With `features.server_info` (config file, default on) the collector sends an
`A2S_INFO` query to every server's game port every 5 minutes (no RCON
password needed) and reads the `version` / `os` lines of each RCON status:

| Field | Values | From |
|-------|--------|------|
| `hlstats_Servers.server_version` | the version string up to the first `/`, printable ASCII, at most 64 characters (`9540945`, `1.40.2.2`, GoldSrc `1.1.2.7`) | `A2S_INFO` (Source answer); status `version :` line (GoldSrc: the part after the protocol number) |
| `hlstats_Servers.server_os` | `linux`, `windows`, `mac` | `A2S_INFO` environment byte; status `os :` / `os/type :` line where the game lists one |
| `hlstats_Servers.server_vac` | `secure`, `insecure` | `A2S_INFO` VAC byte; `secure` / `insecure` in the status `version :` line |

`''` is unknown. Where both sources answer, `A2S_INFO` wins; a value no source
reports any more stays as it is. The row is written only when a value
changes. With `server_info` off nothing is written.

## Locations (GeoIP)

With the city database (bundled DB-IP Lite or config.yaml `geoip.path`, see CONFIG.md) the collector looks up the
address of a `connected, address "ip:port"` line or of RCON status (also when
`gamewe_store_player_ip` = 0; the IP itself is stored only with that setting)
and the game server's address when the server becomes active:

| Table | Columns |
|-------|---------|
| `hlstats_Players_Locations` | one row per location, unique by `lat`/`lng` (text, 4 decimals): `city`, `state` (name of the first subdivision; a row stored without one, empty or with a legacy region code of at most 3 characters such as HLstatsX's `02`, gets the name on the next lookup of that point), `country` (English name), `flag` (lower-case ISO 3166 code, e.g. `de`, as HLstatsX/gameME), `lat`, `lng` |
| `hlstats_Players_Data.location_id` | the player's location of the latest connect line or RCON status address (`hlstats_Players_Locations.id`); bots, LAN and loopback addresses get none. Lookups are cached per address for an hour. Without the GeoIP database (warning `GeoIP database not available` in the journal) no location is written at all. |
| `hlstats_Livestats.cli_location_id` | the same for the live view |
| `hlstats_Servers` | `lat`, `lng` (3 decimals), `country_code` (lower-case ISO code), `country_name`, `city`, `state`, not if `fixed_location` = 1 (set in the web admin) |

Names are English. Private addresses (LAN) have no location: the columns keep
their old value.

## Connect method (`hlstats_Players_Data.connect_type`)

The SourceMod plugin asks every human client for `cl_connectmethod` on connect
(CS:S, TF2, DoD:S, HL2DM; not CS:GO) and logs
`"Name<2><STEAM_..><>" setup "cl_connectmethod" (value "serverbrowser_internet")`.
The collector stores how the player connected last:

| `connect_type` | `cl_connectmethod` | gameME label |
|---:|---|---|
| 0 | none logged, or empty (e.g. `connect` in the console) | not shown |
| 1 | `serverbrowser_internet`, `serverbrowser` | Serverbrowser - Internet Tab (SB INET) |
| 2 | `serverbrowser_favorites` | Serverbrowser - Favorites Tab (SB FAV) |
| 3 | `serverbrowser_history` | Serverbrowser - History Tab (SB HIST) |
| 4 | `serverbrowser_lan` | Serverbrowser - LAN Tab (SB LAN) |
| 5 | `serverbrowser_friends` | Serverbrowser - Friends Tab (SB FRIENDS) |
| 6 | `steam`, `steam_*` (join from the Steam friends list, invites) | Steam (STEAM) |
| 7 | `serverbrowser_spectate` | Serverbrowser - Spectate Tab (SB SPEC) |
| 8 | `quickplay*`, `quickpick*`, `matchmaking*` | Matchmaking |
| 9 | `redirect` (sent by another server) | Redirect |
| 10 | any other value | Other |

Without the plugin (or its lines in the log) the column stays 0.

## Server load (`hlstats_server_load`)

Every 10 minutes per server (`gamewe_server_load`), while the server sent log
lines in that interval:

| Column | Meaning |
|--------|---------|
| `server_id` | `hlstats_Servers.serverId` |
| `timestamp` | Unix time of the sample (UTC) |
| `act_players` | players on the server at that time (as `hlstats_Servers.act_players`, bots included unless ignored) |
| `min_players`, `max_players` | lowest and highest number of players during the 10 minutes (not the server's slot settings) |
| `map` | `hlstats_Maps.id` of the current map (0 if unknown) |
| `cpu` | CPU load of the game server in percent, `fps` its frames per second: from RCON `stats`, 0 if RCON is not available |
| `uptime` | **minutes** since the game server process started (RCON `stats`); without RCON the minutes since the first log line after a silence of 10 minutes (an estimate). At most 65535 |

The values are capped to the column types (players 255, the rest 65535).

Map change: the players connect again and the bots are added anew, so for
2 minutes after a map change (4 minutes without RCON status) the player count
from before the change stands, in `act_players`, `min_players`/`max_players`
and `hlstats_Servers.act_players` (and its bots in `hlstats_Servers.act_bots`,
while the server's live rows are already cleared). A sample during the change shows neither 0
nor old and new bots counted at once; a server that really emptied shows 0
once that time is over. After it, with RCON status, the count is what the
last status listed: bots of the old map that late log lines bring back are
tracked until the next poll misses them, but never counted next to the new
ones.

## Daily awards, ribbons, ranks

The collector decides the awards of `hlstats_Awards` for every calendar day
in the time zone of `features.daily_awards` in the config file (else the
top-level `timezone`, else the server's),
at its `time` (default 00:00) on the next day; also at start if the day of
the latest run has none yet. `gamewe awards -date YYYY-MM-DD [-tz ZONE]`
decides a day again, e.g. after changing the awards. A day runs from 00:00 to
24:00 in that time zone (23 or 25 hours when daylight saving time starts or
ends). For each award of the game:

| awardType | code | winner: the player with the highest value of the day |
|-----------|------|-------------------------------------------------------------|
| `W` | a weapon code (`ak47`, `knife`, ...) | kills with that weapon (`hlstats_Events_Frags` joined to `hlstats_Weapons` by id, same game) |
| `W` | `latency` | the **lowest** average ping of the day in `hlstats_Events_Latency` (samples of 0 left out), among players with at least 30 samples of the day (about 30 minutes online); `winner_count` = the average in ms, rounded (at least 1). Needs RCON status (`features.rcon_status`). |
| `O` | `most_kills`, `most_deaths`, `most_suicides`, `most_headshots`, `most_damage`, `most_improved`, `longest_playtime`, `longest_killstreak` | the `hlstats_Players_History` row of that date (history days are **UTC days**, so with another time zone these awards are shifted by the UTC offset, e.g. 01:00 to 01:00 in Berlin in winter): `kills`, `deaths`, `suicides`, `headshots`, `damage`, `skill_change`, `connection_time` (seconds), `kill_streak` |
| `O` | any other code | actions with that code (`hlstats_Events_PlayerActions` joined to `hlstats_Actions`, same game) |

Only ranked players (`hideranking = 0`) win; ties go to the lowest
`playerId`; a value of 0 wins nothing (no row). Results:

- `hlstats_Awards_History`: one row per award and day with a winner:
  `award_id`, `winner_id` (`playerId`), `date` (`YYYY-MM-DD`, the day in the
  configured time zone),
  `winner_count` (the value). Deciding a day again replaces that day's rows.
- `hlstats_Awards.d_winner_id`, `d_winner_count`: the winner of the latest day
  in the history (a day without winner does not clear them).

**Ribbons** (`hlstats_Definition_Ranks_Ribbons` with `definition_type =
'ribbons2'`, `restriction_type = 'daily_awards'`): one ribbon per award `code`,
earned with `minimum` daily awards of that code. The levels are
`hlstats_Definition_Ribbons_Classes` (`class` 1 bronze, 2 silver, 3 gold,
`maximum` 8, `steps` 1). The web counts a player's awards per code in
`hlstats_Awards_History`; nothing is stored per player.

**Ranks** (`definition_type = 'ranks'`, `restriction_type = 'kills'`): the
player's rank is the row with the highest `minimum` not above
`hlstats_Players.kills`, from Private (0) to General of the Army (30000) for
css. The web computes it; nothing is stored per player.

## Default data

The reference data (`internal/schema/mysql/seed.sql`, loaded by
`gamewe db migrate`) fills the definition
tables, not runtime data: for CS:S (`css`) the HLstatsX definitions of the
old production database (46 actions with rewards, weapons with modifiers,
teams, roles, 39 awards, ranks and ribbons, ribbon classes), generic clan tag
patterns, more games with their teams, and the `gamewe_*` options. No
players, clans, counters or award winners.

## Tables the collector does not fill

These are left to the web frontend or the web admin, or not filled at all
(a ranking reset may still clear some of them):

- **Ranks and ribbons** are not stored per player: the web derives them (see
  [Daily awards, ribbons, ranks](#daily-awards-ribbons-ranks)). The `daily_award`
  columns of `hlstats_Players_Weapons` / `_Actions` are not written.
- **Clans:** `homepage` and `flag` of `hlstats_Clans`.
- **Roles and teams per player:** `hlstats_Players_Roles*`,
  `hlstats_Players_Teams*`; `kill_assists`, `flags_captured`, `custom_wins` of
  `hlstats_Roles` and `hlstats_Teams`.
- **Per-map player stats:** `hlstats_Players_Maps` apart from `shots` and
  `hits`, `hlstats_Players_Maps_History_Months`; `hlstats_Maps.teama_wins`
  / `teamb_wins`.
- **Monthly histories:** all `*_History_Months` tables.
- **Ranking helpers:** `hlstats_Players.rank`, `sort_skill`, `sort_kpd`.
  `hlstats_Players_Resets` gets a row (`player_id`, `timestamp`) only when a
  player resets the own stats in game (`!gamewe_reset` with
  `gamewe_player_reset` = 1, USAGE.md); `sort_skill` is set with it.
- **Trends:** `hlstats_Trend`.
- **Other events:** `hlstats_Events_Admin`, `hlstats_Events_Rcon`.
- **Web and admin:** `hlstats_Style`, `hlstats_Users`, `hlstats_HostGroups`;
  `hlstats_Options` only `gamewe_heartbeat` (the collector reads the
  `gamewe_*` keys, the web admin writes them).
- **Never written:** `hlstats_Players_Data` profile fields (`fullName`, `email`,
  `homepage`, `icq`): there is no in-game command to set them (no `hlx_set` /
  `gamewe_set`), and the collector ignores such chat lines. Also never written:
  `bans_on_record`, `custom_ban_*`, `last_address_id`, and in `hlstats_Players`
  `kill_assists_medic`, `kills_assisted`, `custom_wins`,
  `points_healed`, `flags_captured`.

`hlstats_Players.hideranking` is set by the collector to 1 for new bots
(with `gamewe_rank_bots` = 0), to 2 for banned players and to 3 / 0 when a
player hides or shows themselves (`!hideranking`); 1 for humans is the web
admin's.

## PostgreSQL

The PostgreSQL schema is generated from the MariaDB one: the same tables and
columns with the same names, NOT NULLs and defaults, the same primary keys
(`hlstats_Options.keyname`, `hlstats_Users.username`, ...) and unique keys, and
the same reference data. The collector writes the same values into both (the
CI tests run the same game against both databases and compare the schemas).
What a reader has to know:

| Topic | MariaDB / MySQL | PostgreSQL |
|-------|-----------------|------------|
| Names | `hlstats_Players.playerId` | the same, unquoted, so PostgreSQL folds them to lower case: `hlstats_players.playerid`. Queries work unchanged as long as they never quote names. **Result column names are lower case** (`playerid`) unless the query gives an alias in double quotes (`playerId AS "playerId"`). |
| Quoting | backticks | no backticks; never quote table or column names in the schema (they would become case-sensitive) |
| Integer types | `tinyint`, `smallint unsigned`, `mediumint`, `int unsigned` | the next larger signed type: `smallint`, `integer`, `integer`, `bigint`. `timestamp_id` is `bigint`, still Unix seconds (UTC). Drivers may return `bigint` as a string or BigInt. |
| ENUM | `enum('0','1')` | `varchar(1)` with `CHECK (col IN ('0','1'))`; no `CREATE TYPE`. The values are the same strings. |
| `char(n)` | blank-padded type | `varchar(n)` (the collector never writes trailing blanks, so values are equal) |
| `float` | `float` | `real` (also 4 bytes); `decimal` stays `numeric` |
| `datetime` | `datetime` | `timestamp` (without time zone), UTC as written by the collector. `hlstats_Players_History.eventTime` is a `date` in both. |
| Zero dates | `'0000-00-00'`, `'0000-00-00 00:00:00'` as defaults | `'1970-01-01'`, `'1970-01-01 00:00:00'`: test for those instead of the zero date |
| AUTO_INCREMENT | `AUTO_INCREMENT` | `GENERATED BY DEFAULT AS IDENTITY`: insert without the id column (`INSERT ... RETURNING id` gives the new id). Inserting an explicit id does not move the sequence; after that, run `SELECT setval(pg_get_serial_sequence('hlstats_clans', 'clanid'), (SELECT MAX(clanId) FROM hlstats_Clans))`. The seed inserts no explicit ids. |
| `hlstats_Livestats` | MEMORY table (empty after a restart) | `UNLOGGED` table (empty after a crash of the server) |
| Text comparison | `utf8mb4_unicode_ci`: case-insensitive, `'bob' = 'BOB'` | case-sensitive: use `ILIKE` or `LOWER(a) = LOWER(b)` for name searches; `ORDER BY name` sorts by the database's collation |
| Division | `kills / deaths` gives a decimal | integer division for integer columns: write `kills * 1.0 / deaths`, and guard against 0 (`NULLIF(deaths, 0)`) |
| Insert or update | `INSERT IGNORE`, `ON DUPLICATE KEY UPDATE`, `REPLACE` | `ON CONFLICT DO NOTHING`, `ON CONFLICT (key) DO UPDATE SET col = EXCLUDED.col`; no `REPLACE` (e.g. for `hlstats_Options`: `ON CONFLICT (keyname)`) |
| Functions | `IF()`, `IFNULL()`, `GROUP_CONCAT()`, `UNIX_TIMESTAMP()`, `FROM_UNIXTIME()`, `YEARWEEK()`, `NOW()` | `CASE`, `COALESCE()`, `string_agg()`, `EXTRACT(EPOCH FROM ...)`, `to_timestamp()`, no `YEARWEEK` (read `yearweek_id` as stored), `now()` |
| Paging | `LIMIT 20, 10` | `LIMIT 10 OFFSET 20` (works on both) |
| `GROUP BY` | lenient | every selected column must be grouped or aggregated (or depend on the grouped primary key) |
| Strings | `\'` and `\\` escapes | standard SQL: `''` for a quote, a backslash is a normal character; use bound parameters (`$1`) |
| Rights | `gamewe_web` cannot read `hlstats_Servers.rcon_password` | the same (column rights); `gamewe_web_admin` also has USAGE on the sequences, so its inserts get ids |

`yearweek_id` is computed by the collector (MySQL `YEARWEEK` mode 0, weeks
start on Sunday) and has the same values on both databases.
