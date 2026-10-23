// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"testing"

	"gamewe-collector/internal/settings"
)

func TestScoreKill(t *testing.T) {
	cfg := settings.Defaults() // kill 5, teamkill 10
	cfg.HeadshotBonus = 1
	cases := []struct {
		k, v   int
		hs, tk bool
		wantK  int
		wantV  int
	}{
		{1000, 1000, false, false, 5, -5},
		{1000, 1000, true, false, 6, -5},
		{1000, 2000, false, false, 10, -10},
		{2000, 1000, false, false, 3, -3},     // 2.5 rounds to 3
		{1000, 100000, false, false, 20, -20}, // clamped to 4x
		{100000, 10, false, false, 1, -1},     // at least 1
		{1000, 3, false, false, 1, -1},
		{1000, 0, false, false, 1, 0}, // victim cannot go below 0
		{1000, 1000, false, true, -10, 0},
		{4, 1000, false, true, -4, 0},
	}
	for _, c := range cases {
		got := ScoreKill(c.k, c.v, c.hs, c.tk, 1, cfg)
		if got.KillerDelta != c.wantK || got.VictimDelta != c.wantV {
			t.Errorf("ScoreKill(%d,%d,hs=%v,tk=%v) = %+v, want %d/%d", c.k, c.v, c.hs, c.tk, got, c.wantK, c.wantV)
		}
	}
}

func TestWeaponModifier(t *testing.T) {
	cfg := settings.Defaults()
	if d := KillDelta(1000, 1000, 1.5, cfg); d != 8 { // 7.5 rounds to 8
		t.Errorf("modifier 1.5: %d", d)
	}
	if d := KillDelta(1000, 1000, 0, cfg); d != 5 { // invalid modifier counts as 1
		t.Errorf("modifier 0: %d", d)
	}
}
