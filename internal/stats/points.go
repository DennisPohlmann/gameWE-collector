// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"math"

	"gamewe-collector/internal/settings"
)

// KillDelta returns the skill a kill moves from victim to killer, before
// the headshot bonus. Killing a stronger player is worth more, and the
// weapon's hlstats_Weapons.modifier scales it:
// round(kill_points * modifier * victim/killer), clamped to
// 1..4*kill_points.
func KillDelta(killerSkill, victimSkill int, modifier float64, cfg settings.Settings) int {
	k := max(killerSkill, 1)
	v := max(victimSkill, 1)
	if modifier <= 0 {
		modifier = 1
	}
	d := int(math.Round(float64(cfg.KillPoints) * modifier * float64(v) / float64(k)))
	return min(max(d, 1), 4*cfg.KillPoints)
}

// KillResult holds the skill changes of one kill after clamping at zero,
// and the resulting skills.
type KillResult struct {
	KillerDelta, VictimDelta int
	KillerSkill, VictimSkill int
}

// ScoreKill computes the skill changes for a kill. Teamkills only cost the
// killer teamkill_penalty; the victim keeps its skill.
func ScoreKill(killerSkill, victimSkill int, headshot, teamkill bool, modifier float64, cfg settings.Settings) KillResult {
	var kd, vd int
	if teamkill {
		kd = -cfg.TeamkillPenalty
	} else {
		d := KillDelta(killerSkill, victimSkill, modifier, cfg)
		kd, vd = d, -d
		if headshot {
			kd += cfg.HeadshotBonus
		}
	}
	return KillResult{
		KillerDelta: max(killerSkill+kd, 0) - killerSkill,
		VictimDelta: max(victimSkill+vd, 0) - victimSkill,
	}
}

// ScoreOneSidedKill scores a kill only the killer gains from: a Left 4
// Dead player's kill of the AI, as HLstatsX:CE does. The AI is not ranked,
// so its skill does not count: round(kill_points * modifier), clamped to
// 1..4*kill_points, plus the headshot bonus; the victim keeps its skill.
func ScoreOneSidedKill(killerSkill int, headshot bool, modifier float64, cfg settings.Settings) KillResult {
	kd := KillDelta(1, 1, modifier, cfg)
	if headshot {
		kd += cfg.HeadshotBonus
	}
	return KillResult{KillerDelta: max(killerSkill+kd, 0) - killerSkill}
}
