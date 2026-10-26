// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"strings"

	"gamewe-collector/internal/games"
	"gamewe-collector/internal/logparse"
	"gamewe-collector/internal/stats"
)

// source2Weapon maps the weapon codes CS2 logs for one weapon in two
// states to the code of the weapon (the CS:GO names of the reference
// data): the USP-S and the M4A1-S without their silencer. CS2 logs the fire
// of both fire grenades as "inferno"; the Counter-Terrorists' one (team CT)
// is the incendiary grenade, "incgrenade" in the reference data.
func source2Weapon(code, team string) string {
	switch code {
	case "usp_silencer_off":
		return "usp_silencer"
	case "m4a1_silencer_off":
		return "m4a1_silencer"
	case "inferno":
		if team == "CT" {
			return "incgrenade"
		}
	}
	return code
}

// source2Weapons applies source2Weapon to the weapon of a kill, suicide or
// hit of a Source 2 server (with the team of the one who used it).
func source2Weapons(e logparse.Event) logparse.Event {
	switch v := e.(type) {
	case logparse.Kill:
		v.Weapon = source2Weapon(v.Weapon, v.Killer.Team)
		return v
	case logparse.Suicide:
		v.Weapon = source2Weapon(v.Weapon, v.Player.Team)
		return v
	case logparse.Attacked:
		v.Weapon = source2Weapon(v.Weapon, v.Attacker.Team)
		return v
	}
	return e
}

// tracks reports whether a player reference is tracked on this server:
// Trackable, and on Source 2 not the SourceTV (GOTV) bot, which CS2 logs
// like a player ("SourceTV<0><BOT><>") but which never plays.
func (srv *serverState) tracks(ref logparse.Player) bool {
	if !ref.Trackable() {
		return false
	}
	return srv.info.Engine != games.Source2 || !ref.IsBot() || (ref.Name != "SourceTV" && ref.Name != "GOTV")
}

// maxHitWeapons bounds the weapons a player's unsaved hits are kept for.
const maxHitWeapons = 64

// attacked counts a hit of a Source 2 server for the attacker: the hit
// group (hlstats_Players_Weapons head ... rightleg, neck) and the damage.
// Hits and shots are not counted: CS2 logs no shots, so an accuracy would
// be wrong (the CS2 plugin's weaponstats bring shots and hits). Other
// engines are skipped: there the plugin's weaponstats2 lines carry the hit
// groups.
func (p *Processor) attacked(ctx context.Context, ev stats.Ev, e logparse.Attacked) error {
	if !p.Source2Hits || p.srv.info.Engine != games.Source2 || e.Weapon == "" {
		return nil
	}
	if e.Attacker.Key() == e.Victim.Key() {
		return nil // fall damage, own grenade
	}
	st, err := p.player(ctx, ev, e.Attacker)
	if st == nil || err != nil {
		return err
	}
	w := hitWeapon(st, e.Weapon)
	if w == nil {
		return nil
	}
	w.Damage += min(e.Damage, 1000)
	switch strings.ToLower(e.Hitgroup) {
	case "head":
		w.Head++
	case "neck":
		w.Neck++
	case "chest":
		w.Chest++
	case "stomach":
		w.Stomach++
	case "left arm", "leftarm":
		w.LeftArm++
	case "right arm", "rightarm":
		w.RightArm++
	case "left leg", "leftleg":
		w.LeftLeg++
	case "right leg", "rightleg":
		w.RightLeg++
	}
	return nil
}

// hitWeapon is st's unsaved counters for weapon, nil when st already has
// maxHitWeapons weapons.
func hitWeapon(st *playerState, weapon string) *stats.WeaponStats {
	if st.hitStats == nil {
		st.hitStats = map[string]*stats.WeaponStats{}
	}
	w := st.hitStats[weapon]
	if w == nil {
		if len(st.hitStats) >= maxHitWeapons {
			return nil
		}
		w = &stats.WeaponStats{}
		st.hitStats[weapon] = w
	}
	return w
}

// source2Kill counts a kill of a Source 2 server per weapon
// (hlstats_Players_Weapons kills, headshots, deaths), as the plugin's
// weaponstats lines do: for the killer the kill (or team kill) and the
// headshot, for the victim a death by that weapon. With the CS2 plugin
// these counts are dropped at the flush.
func (p *Processor) source2Kill(k, v *playerState, weapon string, headshot, teamkill bool) {
	if !p.Source2Hits || p.srv.info.Engine != games.Source2 || weapon == "" {
		return
	}
	if w := hitWeapon(k, weapon); w != nil {
		if teamkill {
			w.Teamkills++
		} else {
			w.Kills++
			if headshot {
				w.Headshots++
			}
		}
	}
	if w := hitWeapon(v, weapon); w != nil {
		w.Deaths++
	}
}

// pluginSeen marks a Source 2 server as running the gameWE plugin (start
// line with its version, or "" for a weaponstats line).
func (p *Processor) pluginSeen(version string) {
	if p.srv.info.Engine != games.Source2 || p.srv.plugin {
		return
	}
	p.srv.plugin = true
	p.log.Info("CS2 server runs the gameWE plugin: replies on, damage, kills and deaths per weapon from its weaponstats",
		"server", p.srv.info.Name, "version", version)
}

// flushHits stores a player's counted hits. With the gameWE plugin on the
// server its weaponstats lines already carry the damage, kills, headshots
// and deaths per weapon, so only the hit groups are stored here (else they
// would count twice). The decision is made at the flush, so hits counted
// before the plugin's first line of a round are not doubled either.
func (p *Processor) flushHits(ctx context.Context, ev stats.Ev, st *playerState) error {
	hits := st.hitStats
	st.hitStats = nil
	for weapon, w := range hits {
		if p.srv.plugin {
			w = &stats.WeaponStats{Head: w.Head, Neck: w.Neck, Chest: w.Chest, Stomach: w.Stomach,
				LeftArm: w.LeftArm, RightArm: w.RightArm, LeftLeg: w.LeftLeg, RightLeg: w.RightLeg}
			if *w == (stats.WeaponStats{}) {
				continue
			}
		}
		if err := p.store.AddWeaponStats(ctx, ev, st.live, weapon, *w); err != nil {
			return err
		}
	}
	return nil
}

// flushAllHits stores the counted hits of every player (round end).
func (p *Processor) flushAllHits(ctx context.Context, ev stats.Ev) error {
	for _, st := range p.srv.players {
		if err := p.flushHits(ctx, ev, st); err != nil {
			return err
		}
	}
	return nil
}
