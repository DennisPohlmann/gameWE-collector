// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import "testing"

func TestResetMode(t *testing.T) {
	for in, want := range map[string]string{
		ResetPlayerStats: ResetPlayerStats,
		ResetAllStats:    ResetAllStats,
		ResetAll:         ResetAll,
		ResetStatsOld:    ResetPlayerStats, // the name before the split
	} {
		if got, err := ResetMode(in); err != nil || got != want {
			t.Errorf("ResetMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ALL", "players", "all; DROP TABLE x"} {
		if _, err := ResetMode(in); err == nil {
			t.Errorf("ResetMode(%q) accepted", in)
		}
	}
}

// The tables of each mode: server statistics only with all-stats and all,
// players deleted only with all.
func TestResetStepsByMode(t *testing.T) {
	type use struct{ deleted, updated bool }
	tables := func(mode string) map[string]use {
		m := map[string]use{}
		for _, st := range resetSteps(mode) {
			u := m[st.table]
			if st.set == "" {
				u.deleted = true
			} else {
				u.updated = true
			}
			m[st.table] = u
		}
		return m
	}
	server := []string{"hlstats_server_load", "hlstats_Servers"}
	player := []string{"hlstats_Events_Frags", "hlstats_Players_History", "hlstats_Players_Weapons", "hlstats_Weapons", "hlstats_Maps", "hlstats_Livestats"}
	for _, c := range []struct {
		mode         string
		server, gone bool
	}{
		{ResetPlayerStats, false, false},
		{ResetAllStats, true, false},
		{ResetAll, true, true},
	} {
		m := tables(c.mode)
		for _, tb := range server {
			if _, ok := m[tb]; ok != c.server {
				t.Errorf("%s: %s in the reset: %v, want %v", c.mode, tb, ok, c.server)
			}
		}
		for _, tb := range player {
			if _, ok := m[tb]; !ok {
				t.Errorf("%s: %s not in the reset", c.mode, tb)
			}
		}
		if p := m["hlstats_Players"]; p.deleted != c.gone || p.updated == c.gone {
			t.Errorf("%s: hlstats_Players %+v, players deleted: %v", c.mode, p, c.gone)
		}
		if _, ok := m["hlstats_PlayerUniqueIds"]; ok != c.gone {
			t.Errorf("%s: hlstats_PlayerUniqueIds in the reset: %v", c.mode, ok)
		}
	}
}
