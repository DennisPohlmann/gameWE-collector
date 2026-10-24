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

A trailing NUL and line break are cut off. With `features.log_secret` `true` or
`auto` an `S` packet must carry `hlstats_Servers.log_secret` of its server
(CONFIG.md, "Log secret"); with `false` the secret is cut off unchecked. The
collector sets the secret with the RCON command `sv_logsecret <n>` and reads it
with `sv_logsecret` (answer `"sv_logsecret" = "<n>" ( def. "0" )`).

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

kpd is kills/deaths, hpk headshots/kills (a ratio), accuracy hits/shots in
percent, all with two decimals. Names are shortened so that a command stays
below the engine's 512-character limit. With `gamewe_raw_messages` = 1 the
chat commands are answered the same way with the types 1 (`!rank`), 4
(`!session`), 5 (`!top10`) and 6 (`!next`) and query id 0, for a plugin that
shows them itself (the forwards `onGameWEStatsRank`, ...).

The RCON connection to each server is kept open and reopened after errors.
Replies are sent in the background, so a game server that is down never delays
the statistics.
