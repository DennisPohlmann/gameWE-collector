# Games and engines

The collector handles Source and GoldSrc (Half-Life 1) servers.
Counter-Strike 2 (Source 2) is not supported: it sends no UDP logs and
SourceMod does not run on it. A game server belongs to a game (`hlstats_Servers.game` =
`hlstats_Games.code`, a ranking); the game's `realgame` is the base game,
and the base game decides the engine. Everything engine-specific follows from
it: log transport, RCON protocol, `status` format, how the collector
registers itself as log address, and the commands of in-game replies.

## realgame and engine

| realgame | Game | Engine |
|----------|------|--------|
| `css` | Counter-Strike: Source | Source |
| `csgo` | Counter-Strike: Global Offensive | Source |
| `tf` | Team Fortress 2 | Source |
| `dods` | Day of Defeat: Source | Source |
| `hl2mp`, `hl2ctf` | Half-Life 2: Deathmatch | Source |
| `l4d`, `l4d2`, `insurgency` | Left 4 Dead 1/2, Insurgency | Source |
| `cstrike` | Counter-Strike 1.6 | GoldSrc |
| `dod` | Day of Defeat | GoldSrc |
| `tfc` | Team Fortress Classic | GoldSrc |
| `valve` | Half-Life 1 Multiplayer | GoldSrc |
| `ns` | Natural Selection | GoldSrc |
| `ts`, `dmc`, `ricochet`, `gearbox` | other GoldSrc mods (no default data) | GoldSrc |

A ranking cloned from a game keeps the template's realgame (`css1` ... `css6`
have `realgame = css`; `gamewe game add ... -template css` and the web admin
copy it). An empty or unknown realgame is guessed from the code's prefix
(`css3` → css, `tf2fun` → tf), else css; the collector logs a warning once.
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
  `teamkills`, `bonuspoints`, `allsentrykills` (the history awards cover
  them);
- CE's `hlstats_Ribbons` (several levels per award, 80-612 rows per game)
  are one `ribbons2` row per award plus the bronze/silver/gold classes of
  `hlstats_Definition_Ribbons_Classes` in this schema; CE's
  `hlstats_Games_Defaults` (server config defaults of its daemon) and
  `hlstats_Ranks` (one global list) have no counterpart: the collector's
  settings are `hlstats_Options`, every game gets the css ranks;
- csgo: see [CS:GO](#csgo) below;
- insurgency, l4d, l4d2: no weapons and actions (HLstatsX:CE has none; weapons
  are created with modifier 1.00 when first used), but the game-independent
  awards, the ranks and the ribbon classes like every game;
- ranks and ribbon classes as css, one ribbon per daily award.

**Limit:** `hlstats_Teams.teamId` and `hlstats_Roles.roleId` are tinyint in
the HLstatsX schema: at most 255 teams and 255 roles over all games and
rankings. The seed uses about 30 teams and 98 roles; a ranking cloned from
css adds 3 and 8, one cloned from dod 2 and 35.

## Log transport

| Engine | Transport | Packet / line |
|--------|-----------|---------------|
| Source | UDP to `listen.port` | `\xff\xff\xff\xffRL 10/23/2026 - 12:00:00: <event>` |
| GoldSrc | UDP to `listen.port` | `\xff\xff\xff\xfflog L 10/23/2026 - 12:00:00: <event>` |

UDP packets are accepted only from servers in `hlstats_Servers` (exactly the
source ip:port).

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
plugin on cstrike, dod and tfc. valve and ns have none.

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

## RCON

| Engine | Protocol | `status` | Log address |
|--------|----------|----------|-------------|
| Source | TCP on the game port | `# userid name uniqueid connected ping loss state adr` | `logaddress_list`, `logaddress_add ip:port`, `log on` |
| GoldSrc | UDP on the game port: `challenge rcon`, then `rcon <challenge> "<password>" <command>` | `# slot "name" userid uniqueid frag time ping loss adr` | `logaddress_add ip port` (space), `log on` |

GoldSrc RCON passwords must not contain `"`. A GoldSrc server bans addresses
after too many wrong passwords; the error says so.

## In-game replies

| Engine | Plugin | Commands (prefix `gamewe`, or `gameme` with `gamewe_command_prefix` = gameme) |
|--------|--------|------|
| Source | gameWE SourceMod plugin | `gamewe_psay <userid> 0 "<msg>"`, `gamewe_msay <time> <userid> 0 "<line>\n<line>"`, raw messages `gamewe_raw_message` (`gamewe_raw_messages` = 1) |
| GoldSrc | gameWE AMX Mod X plugins (`gamewe_cstrike`, `_dod`, `_tfc`, `_ns`, `_hlmp`) | `gamewe_amx_psay <userid> 0 "<msg>"`, `gamewe_amx_msay <time> <userid> 0 "<lines>"`; no raw messages |

The AMX plugin also offers `gamewe_amx_psay2`, `_say`, `_csay`, `_hint`,
`_browse`, `_swap` and `gamewe_block_commands_values` (and the same as
`gameme_amx_*` and `gamewe_*`); the collector uses psay and msay. Its
`say` lines for hidden chat triggers and `selected` lines arrive in the log
like the SourceMod plugin's.
