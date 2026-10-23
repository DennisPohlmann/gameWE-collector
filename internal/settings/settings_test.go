// SPDX-License-Identifier: GPL-3.0-or-later

package settings

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

func TestFromOptions(t *testing.T) {
	s, warn := FromOptions(map[string]string{
		"gamewe_kill_points":  "7",
		"gamewe_store_chat":   "1",
		"gamewe_win_triggers": " CTs_Win, Round_Win ,",
		"gamewe_menu_time":    "zero",   // invalid: default kept
		"gamewe_typo":         "1",      // reported
		"gamewe_page_size":    "50",     // web setting: silently ignored
		"sitename":            "gameWE", // not ours
	})
	if s.KillPoints != 7 || !s.StoreChat || s.MenuTime != 15 {
		t.Errorf("got %+v", s)
	}
	if !reflect.DeepEqual(s.WinTriggers, []string{"CTs_Win", "Round_Win"}) {
		t.Errorf("win triggers %q", s.WinTriggers)
	}
	if len(warn) != 2 {
		t.Errorf("warnings %q", warn)
	}
}

func TestDefaultsMatchKeys(t *testing.T) {
	opts := map[string]string{}
	for _, k := range Keys() {
		opts[k.Name] = k.Default
	}
	s, warn := FromOptions(opts)
	if len(warn) != 0 || !reflect.DeepEqual(s, Defaults()) {
		t.Errorf("documented defaults differ from Defaults(): %+v %q", s, warn)
	}
}

func TestLive(t *testing.T) {
	l := NewLive(Defaults())
	s := Defaults()
	s.KillPoints = 9
	l.Set(s)
	if l.Get().KillPoints != 9 {
		t.Error("Set not visible")
	}
}

// The gamewe_* rows in schema/init/02_seed.sql must match the defaults.
func TestSeedMatchesDefaults(t *testing.T) {
	data, err := os.ReadFile("../../schema/init/02_seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	opts := map[string]string{}
	for _, m := range regexp.MustCompile(`\('(gamewe_[a-z_]+)', '([^']*)'\)`).FindAllStringSubmatch(string(data), -1) {
		opts[m[1]] = m[2]
	}
	for _, k := range Keys() {
		if _, ok := opts[k.Name]; !ok {
			t.Errorf("02_seed.sql lacks %s", k.Name)
		}
	}
	s, warn := FromOptions(opts)
	if len(warn) != 0 || !reflect.DeepEqual(s, Defaults()) {
		t.Errorf("seed differs from defaults: %+v %q", s, warn)
	}
}
