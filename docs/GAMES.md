# Games and engines

The collector handles three engines: GoldSrc (Half-Life 1), Source
(Half-Life 2) and Source 2 (Counter-Strike 2). CS2 is read from its own logs
only, over HTTP (`listen.http`); there is no gameWE plugin for it, so no
in-game replies (see [Counter-Strike 2](#counter-strike-2)). A game server belongs to a game (`hlstats_Servers.game` =
`hlstats_Games.code`, a ranking); the game's `realgame` is the base game,
and the base game decides the engine. Everything engine-specific follows from
it: log transport, RCON protocol, `status` format, how the collector
registers itself as log address, and the commands of in-game replies.

## realgame and engine

| realgame | Game | Engine |
|----------|------|--------|
| `cs2` | Counter-Strike 2 | Source 2 |
| `css` | Counter-Strike: Source | Source |
| `csgo` | Counter-Strike: Global Offensive | Source |
| `tf` | Team Fortress 2 | Source |
| `dods` | Day of Defeat: Source | Source |
| `hl2mp`, `hl2ctf` | Half-Life 2: Deathmatch | Source |
| `l4d`, `l4d2`, `insurgency` | Left 4 Dead 1/2 (see [Left 4 Dead](#left-4-dead)), Insurgency | Source |
| `zps`, `ff`, `csp`, `fof`, `nmrih` | Zombie Panic! Source, Fortress Forever, CSPromod, Fistful of Frags, No More Room in Hell | Source |
| `cstrike` | Counter-Strike 1.6 | GoldSrc |
| `czero` | Counter-Strike: Condition Zero | GoldSrc |
| `dod` | Day of Defeat | GoldSrc |
| `tfc` | Team Fortress Classic | GoldSrc |
| `valve` | Half-Life 1 Multiplayer | GoldSrc |
| `ns` | Natural Selection | GoldSrc |
| `ts`, `dmc`, `ricochet`, `gearbox` | other GoldSrc mods (no default data) | GoldSrc |

A ranking cloned from a game keeps the template's realgame (`css1` ... `css6`
have `realgame = css`; `gamewe game add ... -template css` and the web admin
copy it). An empty or unknown realgame is guessed from the code's prefix
(`css3` → css, `tf2fun` → tf, `cs2_retake` → cs2), else css; the collector logs a warning once.
A database without the realgame column (plain HLstatsX): the collector then
guesses every game from its code.

`gamewe game list` shows realgame and engine of every game.

## Default data

The reference data (`internal/schema/mysql/seed.sql`) has teams, roles, actions (rewards), weapons (modifiers),
awards, ranks and ribbons for css (gameME values), and for cstrike, dod,
dods, tfc, valve, ns, tf, hl2mp and csgo the definitions of HLstatsX:CE
(`sql/install.sql` of github.com/A1mDev/hlstatsx-community-edition) with these
gameWE changes:

- every game gets the player-history awards (`most_kills`, `most_deaths`,
  `most_suicides`, `most_headshots`, `most_damage`, `most_improved`,
  `longest_playtime`, `longest_killstreak`) and `latency`;
- CE awards the collector cannot decide are left out: the player-vs-player
  award types `P`/`V` (tf: 10) and CE's meta awards `mostkills`, `suicide`,
  `bonuspoints`, `allsentrykills` (the history awards cover them); CE's
  `teamkills` (every team game) is decided like the history awards
  (migration 11, with `mvp` for css);
- CE's `hlstats_Ribbons` (several levels per award, 80-612 rows per game)
  are one `ribbons2` row per award plus the bronze/silver/gold classes of
  `hlstats_Definition_Ribbons_Classes` in this schema; CE's
  `hlstats_Games_Defaults` (server config defaults of its daemon) and
  `hlstats_Ranks` (one global list) have no counterpart: the collector's
  settings are `hlstats_Options`, every game gets the css ranks;
- csgo: see [CS:GO](#csgo) below;
- cs2: the csgo definitions for the game cs2, see
  [Counter-Strike 2](#counter-strike-2) below;
- czero: Counter-Strike: Condition Zero multiplayer logs like Counter-Strike
  1.6 (the same teams, triggers and weapon names, the same AMX Mod X plugin
  `gamewe_cstrike`), so czero gets a copy of the cstrike definitions.
  HLstatsX:CE has no Condition Zero data of its own (it runs CZ servers as
  cstrike); a CZ server can still use a ranking with realgame cstrike;
- zps, ff, csp, fof: teams, roles (ff), actions, weapons and awards of
  HLstatsX:CE; a few crude ff award names are replaced by neutral ones, and
  the ff pseudo weapon award `BOOM_HEADSHOT` is left out (`most_headshots`
  covers it). The SourceMod plugin detects all four (`ZPS`, `FF`, `CSP`,
  `FOF`). Not checked against live logs of these games yet;
- l4d, l4d2: teams, roles (survivors and infected classes), actions,
  weapons and awards of HLstatsX:CE; CE's player-vs-player awards are left
  out, two l4d2 copy errors are fixed (`killed_jockey` was named "Hunter
  Punter", `protect_teammate` counted "hunter punts"). See
  [Left 4 Dead](#left-4-dead) below;
- nmrih: like insurgency below (HLstatsX:CE has no data for it), no teams
  (it is a co-op game);
- insurgency: no weapons and actions (HLstatsX:CE has none; weapons
  are created with modifier 1.00 when first used), but the game-independent
  awards, the ranks and the ribbon classes like every game;
- ranks and ribbon classes as css, one ribbon per daily award.

**Limit:** `hlstats_Teams.teamId` and `hlstats_Roles.roleId` are tinyint in
the HLstatsX schema: at most 255 teams and 255 roles over all games and
rankings. The seed uses about 60 teams and 135 roles; a ranking cloned from
css adds 3 and 8, one cloned from dod 2 and 35.

## Log transport

| Engine | Transport | Packet / line |
|--------|-----------|---------------|
| Source | UDP to `listen.port` | `\xff\xff\xff\xffRL 10/23/2026 - 12:00:00: <event>` |
| GoldSrc | UDP to `listen.port` | `\xff\xff\xff\xfflog L 10/23/2026 - 12:00:00: <event>` |
| Source 2 | HTTP `POST` to `listen.http` `/<game port>` (or `/<token>/<game port>`) | one or more lines per post: `10/23/2026 - 12:00:00.250 - <event>` |

UDP packets are accepted only from servers in `hlstats_Servers` (exactly the
source ip:port). An HTTP post is accepted only from a Source 2 server in
`hlstats_Servers`: the sender's IP plus the game port from the URL (the TCP
source port of a post is random). A Source 2 server may also send UDP lines
(e.g. through a forwarder from the server's own ip:port); they are read the
same way.

## Log formats

All engines use `"Name<userid><SteamID><Team>"` player tags and the HLstatsX
events (`killed`, `committed suicide with`, `triggered`, `joined team`,
`say`, ...). Differences the collector handles:

| | Source (CS:S, DoD:S, HL2DM) | CS:GO | TF2 | GoldSrc |
|---|---|---|---|---|
| SteamIDs | `STEAM_0:x:y` / `[U:1:n]` | `STEAM_1:x:y` | `[U:1:n]` | `STEAM_0:x:y`; `VALVE_ID_LAN`, `STEAM_ID_LAN`, `VALVE_ID_PENDING` are not tracked |
| positions | plugin `located on` lines | `[x y z]` after the player tag | `(attacker_position "x y z")` | cstrike AMX plugin: `(attacker_position ...)` |
| headshot | `(headshot)` | `(headshot)`, `(headshot penetrated)` | `(customkill "headshot")` | CS 1.6 logs none: the AMX plugin logs the kill line, then `triggered "headshot"` (see below) |
| team change | `joined team` | `switched from team <A> to <B>` | `joined team` | `joined team` |
| round win | `Team "CT" triggered "CTs_Win"` | `SFUI_Notice_CTs_Win` (prefix removed) | `Round_Win` | `CTs_Win` |
| kill assists | – (CS:S has no assister; Insurgency: plugin `triggered "kill_assist" against "V"`) | `"A" assisted killing "V"`, the plugin also `triggered "kill_assist" against "V"` | `triggered "kill assist" against "V"` | – |
| map | `Started map "x"` | same | same | `Started map "x"` |

**GoldSrc kills are held for one line:** the AMX plugin writes a headshot
kill as the kill line followed by `triggered "headshot"` of the killer. The
collector records a GoldSrc kill when the server's next line arrives: if that
line is the killer's headshot trigger, the kill is stored as a headshot
(`Events_Frags.headshot`, headshot counters, the `headshot` action once),
otherwise as a normal kill. A kill that is the last line is recorded at the
next tick (30 s) or at shutdown.

Kill assists add to `kill_assists` (players, live view, history) and reward
the action `kill_assist` / `kill assist` where the game defines it. On CS:GO
the engine's `assisted killing` line and the SourceMod plugin's
`triggered "kill_assist" against` line describe the same assist: the same
assister and victim within a second is counted once (each line's action is
scored as before: the native line as a player action, the plugin line as a
player-vs-player action). `kills_assisted` (the killer's side) is not written.

**uniqueId** (`hlstats_PlayerUniqueIds.uniqueId`) is the same form for every
engine: `STEAM_x:y:z` → `y:z`; Steam3 `[U:1:n]` → `n%2:n/2`; bots
`BOT:<name>`. So a player has one id whether a server logs Steam2 or Steam3.

**Hitgroups** (`hlstats_Players_Weapons` hit areas) come from `weaponstats2`
lines: the SourceMod plugin on CS:S, CS:GO, DoD:S, HL2DM (not TF2), the AMX
plugin on cstrike, czero, dod and tfc. valve and ns have none. On CS2 they come from
the game's own `attacked` lines (`features.cs2_hitgroups`), with `neck` as an
extra area.

## CS:GO

The base data of `csgo` (CE values; rows of gameWE's own csgo block at the
top of `seed.sql` win where both define a code):

| Data | Rows | Content |
|------|------|---------|
| teams | 2 | `CT`, `TERRORIST` |
| roles | 15 | the player models the SourceMod plugin logs (`leet`, `phoenix`, `balkan`, `professional`, `pirate`, `anarchist`, `separatist`, `zombie`; `st6`, `gign`, `gsg9`, `fbi`, `idf`, `sas`, `swat`) |
| weapons | 81 | every weapon CS:GO logs in kill lines and the plugin's weapon_fire list: rifles, SMGs, pistols, heavy, all knives (`knife`, `knife_t`, `bayonet`, `knife_karambit`, ... `knife_css`, `knifegg`, `knife_ghost`), grenades (`hegrenade`, `inferno` for molotov/incendiary kills, `molotov`, `incgrenade`, `flashbang`, `smokegrenade`, `decoy`, `tagrenade`), `taser`, `healthshot`, Danger Zone (`fists`, `axe`, `hammer`, `spanner`, `melee`, `shield`, `bumpmine`, `breachcharge`, `snowball`), `c4`, `planted_c4` (bomb deaths) and `world` |
| actions | 52 | bomb (plant, defuse, begin defuse, got/dropped/spawned with), hostages (touch, rescue, kill), VIP, `headshot`, `mvp` (the plugin's round MVP), kill streaks 2-12, `domination`, `revenge`, team wins (`CTs_Win`, `Terrorists_Win`, `Target_Bombed`, `Bomb_Defused`, `Target_Saved`, `All_Hostages_Rescued`, `Hostages_Not_Rescued`), and with reward 0 (set them in the web admin): the kill flags `kill_penetrated`, `kill_noscope`, `kill_throughsmoke`, `kill_attackerblind`, `kill_assist`, `flash_assist`, the accolades `accolade_mvp`, `accolade_3k`, `accolade_4k`, `accolade_5k`, `accolade_hsp` |
| awards | 54 | a weapon award per common weapon, headshot, latency, MVP, bomb and hostage awards, the history awards, the kill flags, flash assists and the match MVP |
| ribbons, ranks, classes | 54, 33, 3 | one ribbon per award, the css ranks, bronze/silver/gold |

CE's award codes `firebomb`, `sg553` and `round_mvp` never matched what CS:GO
and the plugin log; they are `inferno`, `sg556` and `mvp`.

The log lines the collector reads on CS:GO (`mp_logdetail 3` is not needed):

| Line | Stored |
|------|--------|
| `"A<2><STEAM_1:0:1><CT>" [x y z] killed "V<3><STEAM_1:1:2><TERRORIST>" [x y z] with "ak47" (headshot penetrated)` | the kill with both positions; headshot; each other flag (`penetrated`, `noscope`, `throughsmoke`, `attackerblind`) is the killer's action `kill_<flag>` (not for kills with unranked bots) |
| `"A" [x y z] killed other "chicken<99>" [x y z] with "knife"` | ignored |
| `"A" assisted killing "V"` / `"A" flash-assisted killing "V"` | a kill assist (`kill_assist`); the flash assist also `flash_assist` |
| `"A" triggered "kill_assist" against "V"` (SourceMod plugin) | the same kill assist, counted once with the line above; a player-vs-player action `kill_assist` |
| `"P" [x y z] committed suicide with "world"` | a suicide |
| `"P" [x y z] was killed by the bomb.` | a death of P with the weapon `planted_c4` (no suicide, no skill change) |
| `"P" triggered "Planted_The_Bomb"` / `"Defused_The_Bomb"` / `"Got_The_Bomb"` / ... | player actions |
| `Team "CT" triggered "SFUI_Notice_CTs_Win" (CT "1") (T "0")` | team action `CTs_Win`, team scores |
| `"P" switched from team <Unassigned> to <CT>` | team change |
| `ACCOLADE, FINAL: {mvp}, Name<12>, VALUE: 3.000000, POS: 1, SCORE: 40.000000` | the winner (POS 1) gets `accolade_<name>`; the player is found by userid and name |
| plugin: `"P" triggered "mvp"`, `"weaponstats"`, `"weaponstats2"`, `"headshot"`, roles from the player model | round MVP, shots/hits/damage/hit areas per weapon, roles |

## Counter-Strike 2

CS2 runs on Source 2. It sends its log only over HTTP (`logaddress_add_http`,
plain HTTP; the game offers no HTTPS), and SourceMod and AMX Mod X do not run
on it. Set up:

1. Switch on `listen.http` in the collector's config (TCP port 41338 by
   default; a `token` is recommended) and open the port for the CS2 servers.
2. Add the server in the web admin or with `gamewe server add` with a game
   whose realgame is `cs2` (the seed has `cs2`).
3. With `rcon_password` and `features.log_address.http` (default) the
   collector registers itself (`logaddress_add_http "http://<collector>:41338/<game port>"`
   and `log on`). By hand, in the server's `server.cfg` or start line:
   `log on; logaddress_add_http "http://203.0.113.5:41338/27015"` (with a
   token: `.../<token>/27015`).

The base data of `cs2` is the csgo data (see [CS:GO](#csgo)) without the VIP
actions, the old weapon code `galil` (CS2 logs `galilar`) and the player-model
roles, plus the team actions `SFUI_Notice_Target_Saved` and
`SFUI_Notice_Hostages_Not_Rescued` and the knives `knife_kukri` and
`knife_twinblade`. The CS2 rows were cross-checked against HLstatsX:CE by
lovasatt (github.com/lovasatt/hlstatsx-community-edition, GPL-2.0-or-later).

What the collector reads on CS2, besides the CS:GO lines above:

| Line | Stored |
|------|--------|
| `10/23/2026 - 12:00:00.250 - <event>` (HTTP), also with `L ` and the milliseconds over UDP | the event; milliseconds are ignored |
| `"A<2><[U:1:42]><CT>" [x y z] attacked "V<3><[U:1:43]><TERRORIST>" [x y z] with "ak47" (damage "27") (damage_armor "3") (health "73") (armor "97") (hitgroup "neck")` | with `features.cs2_hitgroups`: damage and one hit of the area (`head`, `neck`, `chest`, `stomach`, `left arm`, ...) for A's weapon, written to `hlstats_Players_Weapons` at the end of the round and when A leaves. Hits on oneself are not counted, damage is capped at 1000 per hit. Shots and hits stay empty (no `weaponstats`), so CS2 has no accuracy. The kill lines add, per weapon, the killer's kill (or team kill) and headshot and the victim's death, like the plugin's `weaponstats` on the other engines. |
| weapon names `usp_silencer_off`, `m4a1_silencer_off` (silencer removed) | counted as `usp_silencer`, `m4a1_silencer` |
| `inferno` of a Counter-Terrorist (CS2 logs the fire of both fire grenades as `inferno`) | counted as `incgrenade` (the CT incendiary grenade); a Terrorist's stays `inferno` |
| the bots `<BOT>`, SourceTV (`SourceTV<0><BOT><>`) and the demo recorder (`<UNKNOWN>`) | bots as on CS:GO; SourceTV and the demo recorder are not tracked |

RCON `status` of CS2 shows no SteamIDs: its players are matched to the
tracked players by userid (a player not seen in the log yet is skipped), the
map comes from the `loaded spawngroup` line, and `BOT` rows count as bots.
In-game commands (`!rank`, ...) are recorded as chat, but not answered.

## Left 4 Dead

Left 4 Dead 1 and 2 (realgame `l4d`, `l4d2`) log their players with more
fields than other Source games, mark kills with `(DEATH)` and have lines of
their own. The collector reads them as HLstatsX:CE's daemon does (not
checked against live L4D logs yet):

| Line | Stored |
|------|--------|
| `(DEATH)"Nick<3><STEAM_1:0:1><Survivor><Gambler><ALIVE><100+0><setpos_exact 1.00 2.00 3.00; ...><Area 12>" killed "Smoker<5><BOT><Infected><SMOKER><DEAD>...>" with "autoshotgun" (headshot)` | the kill with both positions (rounded `setpos_exact`); the fifth field is the role (survivor or infected class), counted as a role change |
| `(INCAP)"V<...>" was incapped by "A<...>" with "w"` | a kill of V by A (an incapacitated survivor is not dead yet); incapacitated infected are ignored |
| `(TONGUE) Tongue grab starting.  Smoker:"A<...>". Victim:"V<...>".` | the player-vs-player action `tongue_grab` |
| `"infected<...><><Infected>..."`, `"witch<...><><Infected>..."` (no SteamID) | the bots `Infected Horde` and `Witch` |
| the SourceMod plugin's `triggered` lines (`healed_teammate`, `revived_teammate`, `pounce`, `jockey_ride`, ...) and team wins (`versus_win`, `scavenge_win`) | player, player-vs-player and team actions |

Points: a player's kill of the AI (a bot) gives the player
round(`gamewe_kill_points` x weapon modifier), clamped to 1..4x, plus the
headshot bonus, and takes nothing from the AI, as long as the ranking gives
points for bots (`gamewe_bot_points_<game>`, default 1), so a co-op server
ranks its survivors. Kills between players (versus) are scored as usual;
kills and actions by the AI change skill only while the ranking ranks bots
(`gamewe_rank_bots_<game>`, default 1; migration 15 sets 0 where the old
global `gamewe_rank_bots` was 0). Infected killing infected are ignored.
Each kill of a special infected or the Witch is also the killer's action
`killed_<class>` (`killed_smoker`, `killed_tank`, `killed_witch`, on L4D 1
`killed_gas`, `killed_exploding`), each kill or incapacitation of a survivor
the player-vs-player action `killed_survivor`.

## RCON

| Engine | Protocol | `status` | Log address |
|--------|----------|----------|-------------|
| Source | TCP on the game port | `# userid name uniqueid connected ping loss state adr` | `logaddress_list`, `logaddress_add ip:port`, `log on` |
| GoldSrc | UDP on the game port: `challenge rcon`, then `rcon <challenge> "<password>" <command>` | `# slot "name" userid uniqueid frag time ping loss adr` | `logaddress_add ip port` (space), `log on` |
| Source 2 | TCP on the game port (as Source) | `id time ping loss state rate adr name` (no SteamID), map from `loaded spawngroup` | `logaddress_list_http`, `logaddress_add_http "<url>"`, `log on` |

GoldSrc RCON passwords must not contain `"`. A GoldSrc server bans addresses
after too many wrong passwords; the error says so.

## In-game replies

| Engine | Plugin | Commands (prefix `gamewe`, or `gameme` with `gamewe_command_prefix` = gameme) |
|--------|--------|------|
| Source | gameWE SourceMod plugin | `gamewe_psay <userid> 0 "<msg>"`, `gamewe_msay <time> <userid> 0 "<line>\n<line>"`, `gamewe_browse`, `gamewe_console_event`, `gamewe_chat_event`, raw messages `gamewe_raw_message` (`gamewe_raw_messages` = 1) |
| Source 2 | none | no replies: the collector sends nothing to CS2 servers |
| GoldSrc | gameWE AMX Mod X plugins (`gamewe_cstrike`, `_dod`, `_tfc`, `_ns`, `_hlmp`) | `gamewe_amx_psay <userid> 0 "<msg>"`, `gamewe_amx_msay <time> <userid> 0 "<lines>"`, `gamewe_amx_browse` (Counter-Strike 1.6 and Condition Zero only: the other games show the MOTD as text, the address goes to chat), `gamewe_amx_console_event`, `gamewe_amx_chat_event`; no raw messages |

The AMX plugin also offers `gamewe_amx_psay2`, `_say`, `_csay`, `_hint`,
`_browse`, `_swap` and `gamewe_block_commands_values` (and the same as
`gameme_amx_*` and `gamewe_*`); the collector uses psay and msay. Its
`say` lines for hidden chat triggers and `selected` lines arrive in the log
like the SourceMod plugin's.
