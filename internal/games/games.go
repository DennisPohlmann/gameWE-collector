// SPDX-License-Identifier: GPL-3.0-or-later

// Package games maps a game's realgame (hlstats_Games.realgame, as in
// HLstatsX:CE) to its engine, which decides the log format details, the
// RCON protocol and how in-game commands are answered.
package games

import "strings"

// Engine of a game server.
type Engine int

const (
	Source  Engine = iota // Source 1: TCP RCON, SourceMod plugin (gamewe_psay, ...)
	GoldSrc               // Half-Life 1: UDP challenge RCON, AMX Mod X plugin (gamewe_amx_psay, ...)
	Source2               // Source 2 (CS2): logs over HTTP (logaddress_add_http), TCP RCON, optional CounterStrikeSharp plugin (gamewe_psay, ...)
)

func (e Engine) String() string {
	switch e {
	case GoldSrc:
		return "goldsrc"
	case Source2:
		return "source2"
	}
	return "source"
}

// engines are the known base games. The order matters for prefix guesses:
// longer codes first (dods before dod, tfc before tf, l4d2 before l4d,
// cs2 and csgo before css).
// czero (Condition Zero) logs like cstrike and uses the same AMX Mod X plugin.
// retired: no longer supported (the game or mod is dead): not offered for a
// new ranking and not seeded, but a ranking of an existing database keeps
// running with its engine and data; the collector warns about it.
var engines = []struct {
	code    string
	engine  Engine
	retired bool
}{
	{"cstrike", GoldSrc, false}, {"czero", GoldSrc, false}, {"cs2", Source2, false}, {"csgo", Source, false}, {"css", Source, false},
	{"dods", Source, false}, {"dod", GoldSrc, false}, {"tfc", GoldSrc, true}, {"tf", Source, false},
	{"hl2mp", Source, false}, {"hl2ctf", Source, false}, {"l4d2", Source, false}, {"l4d", Source, false},
	{"insurgency", Source, true}, {"zps", Source, false}, {"ff", Source, true}, {"csp", Source, true},
	{"fof", Source, false}, {"nmrih", Source, false}, {"valve", GoldSrc, false}, {"ns", GoldSrc, true},
	{"ts", GoldSrc, false}, {"dmc", GoldSrc, false}, {"ricochet", GoldSrc, false}, {"gearbox", GoldSrc, false},
}

// Default is the realgame of a game whose realgame is empty and not
// guessable from its code.
const Default = "css"

// Resolve returns the base game and engine of a game: realgame if it is a
// known base game, else a guess from the code's prefix (css1 is css), else
// Default. warn says what was guessed ("" if realgame was known, or is
// empty and the code is a base game) or that the game is retired.
func Resolve(realgame, code string) (base string, engine Engine, warn string) {
	r := strings.ToLower(strings.TrimSpace(realgame))
	for _, e := range engines {
		if r == e.code {
			return e.code, e.engine, retiredWarn(e.retired, code, e.code)
		}
	}
	c := strings.ToLower(code)
	for _, e := range engines {
		if r == "" && c == e.code {
			return e.code, e.engine, retiredWarn(e.retired, code, e.code) // a base game of an old database without realgame
		}
	}
	for _, e := range engines {
		if strings.HasPrefix(c, e.code) {
			return e.code, e.engine, "hlstats_Games " + code + ": realgame " + quote(realgame) + " is not a known game, using " + e.code + " (from the code); set realgame (migration 010, GAMES.md)"
		}
	}
	return Default, Source, "hlstats_Games " + code + ": realgame " + quote(realgame) + " is not a known game, using " + Default + "; set realgame (GAMES.md)"
}

func quote(s string) string { return `"` + s + `"` }

func retiredWarn(retired bool, code, base string) string {
	if !retired {
		return ""
	}
	return "hlstats_Games " + code + ": " + base + " is no longer supported (GAMES.md); the ranking keeps running as before and its data stays"
}

// Known returns the supported base games (not the retired ones).
func Known() []string {
	var out []string
	for _, e := range engines {
		if !e.retired {
			out = append(out, e.code)
		}
	}
	return out
}

// Retired returns the base games that are no longer supported.
func Retired() []string {
	var out []string
	for _, e := range engines {
		if e.retired {
			out = append(out, e.code)
		}
	}
	return out
}

// IsBase reports whether code is a supported base game (a default ranking),
// ignoring case. A retired game is not: its unused ranking can be deleted.
func IsBase(code string) bool {
	c := strings.ToLower(strings.TrimSpace(code))
	for _, e := range engines {
		if c == e.code && !e.retired {
			return true
		}
	}
	return false
}
