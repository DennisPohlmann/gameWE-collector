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
)

func (e Engine) String() string {
	switch e {
	case GoldSrc:
		return "goldsrc"
	}
	return "source"
}

// engines are the known base games. The order matters for prefix guesses:
// longer codes first (dods before dod, tfc before tf, l4d2 before l4d).
var engines = []struct {
	code   string
	engine Engine
}{
	{"cstrike", GoldSrc}, {"csgo", Source}, {"css", Source},
	{"dods", Source}, {"dod", GoldSrc}, {"tfc", GoldSrc}, {"tf", Source},
	{"hl2mp", Source}, {"hl2ctf", Source}, {"l4d2", Source}, {"l4d", Source},
	{"insurgency", Source}, {"valve", GoldSrc}, {"ns", GoldSrc},
	{"ts", GoldSrc}, {"dmc", GoldSrc}, {"ricochet", GoldSrc}, {"gearbox", GoldSrc},
}

// Default is the realgame of a game whose realgame is empty and not
// guessable from its code.
const Default = "css"

// Resolve returns the base game and engine of a game: realgame if it is a
// known base game, else a guess from the code's prefix (css1 is css), else
// Default. warn says what was guessed ("" if realgame was known, or is
// empty and the code is a base game).
func Resolve(realgame, code string) (base string, engine Engine, warn string) {
	r := strings.ToLower(strings.TrimSpace(realgame))
	for _, e := range engines {
		if r == e.code {
			return e.code, e.engine, ""
		}
	}
	c := strings.ToLower(code)
	for _, e := range engines {
		if r == "" && c == e.code {
			return e.code, e.engine, "" // a base game of an old database without realgame
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

// Known returns the known base games.
func Known() []string {
	var out []string
	for _, e := range engines {
		out = append(out, e.code)
	}
	return out
}
