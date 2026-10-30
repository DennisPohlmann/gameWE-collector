# gameWE - collector: plugin interface

What the collector reads from the game servers and what it sends back: the log
lines of the gameWE plugins ([SourceMod](https://github.com/DennisPohlmann/gameWE-plugin-sourcemod),
[AMX Mod X](https://github.com/DennisPohlmann/gameWE-plugin-amxmodx)), the
RCON reply commands and the answers to other plugins' queries. Games, engines
and log formats in detail: [GAMES.md](GAMES.md); what ends up in which table:
[DB-SEMANTICS.md](DB-SEMANTICS.md).

## Log lines

Input is standard HL2 log lines over UDP, for example:

```
L 10/22/2026 - 12:00:00: "Name<2><STEAM_1:0:123><CT>" killed "Other<3><STEAM_1:1:456><TERRORIST>" with "ak47" (headshot)
L 10/22/2026 - 12:00:05: "Name<2><STEAM_1:0:123><CT>" say "!rank"
```

Every UDP packet is `FF FF FF FF` and then one of:

| Type | Payload | Sent by |
|------|---------|---------|
| `0x52` `R` | `RL 10/22/2026 - 12:00:00: ...` | Source without `sv_logsecret` |
| `0x53` `S` | `S<secret>L 10/22/2026 - 12:00:00: ...` | Source with `sv_logsecret <secret>` (a number); the secret ends at the first `L ` |
| – | `log L 10/22/2026 - ...` | GoldSrc (HLDS) |

A trailing NUL and line break are cut off. With `features.log_secret` `true`
(default) a server with `hlstats_Servers.log_secret` set must send `S` packets
with exactly that secret; packets of a server without one, and every packet
with `false`, are accepted and the secret is cut off unchecked (CONFIG.md,
"Log secret"). The collector never sends `sv_logsecret` over RCON.

Counter-Strike 2 posts its log over HTTP instead (`listen.http`): `POST
/<game port>` (or `/<token>/<game port>`), one or more lines per body, each
`10/22/2026 - 12:00:00.250 - <event>` (no `L `, milliseconds). The answer is
`200` (also when a line was dropped, so that the server does not post it
again), `403` for a sender that is not a Source 2 server of
`hlstats_Servers`, `404` for a wrong path or token, `405` for another method
and `413` for a body over `listen.http.max_body`. The gameWE CounterStrikeSharp
plugin posts its lines to the same URL; the other lines are the game's own
([GAMES.md](GAMES.md#counter-strike-2)).

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
The CS2 (CounterStrikeSharp) plugin sends `weaponstats` (no `weaponstats2`),
`headshot`, `mvp`, `domination`, `revenge`, roles, `setup` and `requested`
lines, and when it loads and at every map start
`gameWE Plugin <version> (CounterStrikeSharp)`: with that line or its first
`weaponstats` the collector sends the replies to that CS2 server
(see [GAMES.md](GAMES.md#counter-strike-2)).

Client settings the plugin logs (`setup` lines):

```
"Name<2><STEAM_1:0:123><CT>" setup "cl_language" (value "german")
"Name<2><STEAM_1:0:123><CT>" setup "gamewe_lang" (value "de")
"Name<2><STEAM_1:0:123><CT>" setup "cl_connectmethod" (value "serverbrowser_internet")
```

`gamewe_lang` is the player's choice with the plugin's `!gamewe_lang`
(`en`, `de`, `ru`, `es`, `fr`, `pt`, `nl`, `dk` or `da`, `pl`; logged when
the player chooses and on every connect); it decides the language of the
collector's answers. Without one, `cl_language` (english, german, russian,
spanish, french, portuguese/brazilian, dutch, danish, polish) does; anything
else is English. Other values are ignored.

Field order: `weaponstats` = weapon, shots, hits, kills, headshots, tks, damage,
deaths. `weaponstats2` = weapon, head, chest, stomach, leftarm, rightarm, leftleg,
rightleg. The plugin flushes them when a player dies or disconnects and at round
end (L4D, L4D2 and Insurgency also every 180 seconds). Each line therefore holds
the counts since the previous line, and the collector adds them up per player and
weapon. TF2 writes no `weaponstats2`.

**Server query (A2S_INFO).** With `features.server_info` the collector sends
Valve's `A2S_INFO` query (`FF FF FF FF 54 "Source Engine Query" 00`, plus the
4 challenge bytes when the server answers with `S2C_CHALLENGE` `FF FF FF FF
41 <challenge>`) over UDP to the game port of every server in
`hlstats_Servers`, every `features.server_info.interval`. It reads the
server name, the version string, the max players byte, the environment byte
(`l` Linux, `w` Windows, `m`/`o` Mac) and the VAC byte of the Source answer
(`FF FF FF FF 49 ...`), and name, max players, environment and VAC of the old
GoldSrc answer (`FF FF FF FF 6D ...`, no version). Only one
packet of at most 1400 bytes from the server's own address is read (split
answers are not supported); the version is cut to printable ASCII, the name
loses control characters and is cut to 255 characters. The game
server needs nothing extra: the server browser uses the same query.

**SourceBans lookup.** With `features.sourcebans` and a ban list address
(`hlstats_Options` `gamewe_sourcebans_url`), a disconnect line whose reason
names that address's host or one of `features.sourcebans.phrases`, e.g.
`"Name<5><STEAM_1:1:123><>" disconnected (reason "You have been banned from this server. Access bans.example.org for more info.")`,
makes the collector read the public search page of the list:
`GET <address>index.php?p=banlist&advSearch=STEAM_0:1:123&advType=steamid`
(user agent `gameWE collector`, 8 s, at most 3 MiB, redirects not followed,
only public addresses unless `allow_private`). It reads the bans of exactly
that Steam ID from the SourceBans 1.x, SB++ 1.8 default and SB++ 2026 page
templates (player, Steam ID, invoked on, length, reason, and the state:
permanent, running, expired, lifted). The running bans are read again the
same way every `features.sourcebans.interval`. Nothing is sent to the game
server; SourceBans itself kicks the player.

**Bans of all players (bans-sync).** With `features.bans_sync` the
collector asks the same Steam Web API (below) for every known account,
100 SteamID64 per request with `features.bans_sync.steam_pause` between two
requests, and with `features.sourcebans` it reads the whole public list of
the ban list address page by page:
`GET <address>index.php?p=banlist&page=N` (from 1 until a page brings no new
ban, at most 1000 pages, 0.5 s between pages, the limits of the lookup
above). The results go into `hlstats_Bans` and `hlstats_Bans_Players`
(DB-SEMANTICS.md), which the website reads for its bans page, the player
profile and its API; the website no longer asks Steam or SourceBans for
bans itself.

**Steam Web API (VAC block).** For the rankings with
`gamewe_block_vac_<game>` = 1 the collector asks
`GET https://api.steampowered.com/ISteamUser/GetPlayerBans/v1/?key=<key>&steamids=<SteamID64,...>`
(user agent `gameWE collector`, 8 s, at most 1 MiB, redirects not followed;
key: `hlstats_Options` `gamewe_steam_api_key`, never logged; errors are
logged without the address). It is asked for a connecting player whose
answer is not cached (24 hours), and once a day for up to 100 recorded
players per request. A player with a VAC ban on record is kicked over RCON:
`kickid <userid> "VAC ban on record"` (Source, Source 2) or
`kick #<userid> "VAC ban on record"` (GoldSrc); see DB-SEMANTICS.md, "VAC
block".

Replies are console commands that the collector sends over **Source RCON** (TCP,
to the server's `address` and `port` with `rcon_password` from `hlstats_Servers`).
The plugin offers them. The collector uses the `gamewe_` prefix, which the gameWE
plugin accepts besides `gameme_`; for a server with the original gameME plugin set
`gamewe_command_prefix` to `gameme`.

| Command | Used for |
|---------|----------|
| `gamewe_psay <userid[,userid...]> [colored] "<msg>"` | private chat line (`!rank`, `!next`, `!kpd`, `!session`, `!help`, ...); the *to all* commands (`!place`, `!kdeath`, `!session_data`) list every player of one language |
| `gamewe_msay <time> <userid> [handler] "<menu text, \n separated>"` | stats panels and the `!top5` / `!top10` / `!top20` menus |
| `gamewe_browse <userid> "<url>"` | the browser commands (`!statsme`, `!status`, `!clans`, ...; USAGE.md): `gamewe_site_url` plus a fixed path, only `http(s)://` |
| `gamewe_console_event <userid> "<msg>"` | with `gamewe_console_events`: the points of a kill or action, printed in the player's console if the player's `gamewe_display` is on. Plugins before this command print "Unknown command" for it |
| `gamewe_chat_event <userid[,userid...]> "<msg>"` | with `gamewe_global_chat`: an action with points, as a chat line to the listed players whose `gamewe_chat` is on (one command per language). Plugins before this command print "Unknown command" for it |
| `gamewe_hidestate <userid> <hideranking>` | after `!hideranking` / `!settings` and for the `hideranking` request (below): the player's ranking state (`0` visible, `1` hidden by an admin or as a bot, `2` banned, `3` hidden by the player), so the plugin's settings menu shows the one entry that fits. Plugins before this command print "Unknown command" for it in the server console |
| `gamewe_csay "<msg>"` | center text to all (not used yet) |
| `gamewe_tsay <time> <userid> "<msg>"` | top text (not used yet) |
| `gamewe_hint <userid[,..]> "<msg>"`, `gamewe_khint <userid[,..]> "<msg>"` | hint boxes (not used yet) |
| `gamewe_raw_message <type> <query_id> <userid> ...` | answers to other plugins' queries, and with `gamewe_raw_messages` the chat command answers (below) |

Every chat line of `gamewe_psay` starts with the tag `[gameWE] `. The plugins
replace it with their `gamewe_message_prefix` (color tags included) when one is
set, so the server's own prefix shows on the collector's answers as well.

Players are addressed by the **userid**, the number in the first `<..>` of the
player reference (`2` in `"Name<2><STEAM_1:0:123><CT>"`). Message text is
sanitized before it goes into an RCON command. Double quotes become `'`,
semicolons become `,`, and control characters are removed. This means a player
name can never inject further console commands. Menu lines are joined with a
literal `\n` (backslash, n), which the plugin turns into line breaks.

## Queries of other plugins (raw messages)

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
| `hideranking` | `gamewe_hidestate <userid> <hideranking>` (no query id): asked by the plugin's settings menu |

kpd is kills/deaths, hpk headshots/kills (a ratio), accuracy hits/shots in
percent, all with two decimals. Names are shortened so that a command stays
below the engine's 512-character limit. With `gamewe_raw_messages` = 1 the
chat commands are answered the same way with the types 1 (`!rank`), 4
(`!session`), 5 (`!top10`) and 6 (`!next`) and query id 0, for a plugin that
shows them itself (the forwards `onGameWEStatsRank`, ...).

The RCON connection to each server is kept open and reopened after errors.
Replies are sent in the background, so a game server that is down never delays
the statistics.
