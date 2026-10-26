// SPDX-License-Identifier: GPL-3.0-or-later

package settings

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"strings"
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

// Keys of the web frontend are expected in hlstats_Options and are no typo.
func TestWebKeysNotReported(t *testing.T) {
	opts := map[string]string{
		"gamewe_link_1":  "Forum|https://example.com",
		"gamewe_link_12": "",
		"gamewe_totp_8c6976e5b5410415bde908bd4dee15df":  "secret",
		"gamewe_totpp_c2fca2aa3a976ccd7b980318e0416b0d": "secret",
		"gamewe_heartbeat":          "1792889000",
		"gamewe_reset_schedule_css": "2 month",
		"gamewe_reset_mode_css":     "player-stats",
		"gamewe_reset_last_css":     "1791504000",
		"gamewe_hof_tf2_x":          "1",
		"gamewe_reset_schedule":     "quarter@15",
		"gamewe_reset_mode":         "all-stats",
	}
	for _, k := range WebKeys {
		opts[k] = "x"
	}
	if _, warn := FromOptions(opts); len(warn) != 0 {
		t.Errorf("web keys reported: %q", warn)
	}
	for _, k := range []string{"gamewe_link_", "gamewe_link_x", "gamewe_totp_", "gamewe_totp_abc", "gamewe_ts_hots", "gamewe_kill_point", "gamewe_hof_", "gamewe_reset_mode_CSS"} {
		if _, warn := FromOptions(map[string]string{k: "1"}); len(warn) != 1 {
			t.Errorf("%s: warnings %q, want one", k, warn)
		}
	}
	for _, k := range Keys() {
		if webKeys[k.Name] {
			t.Errorf("%s is a collector and a web key", k.Name)
		}
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

// The gamewe_* rows in internal/schema/mysql/seed.sql must match the defaults.
func TestSeedMatchesDefaults(t *testing.T) {
	data, err := os.ReadFile("../../internal/schema/mysql/seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	opts := map[string]string{}
	for _, m := range regexp.MustCompile(`\('(gamewe_[a-z_]+)', '([^']*)'\)`).FindAllStringSubmatch(string(data), -1) {
		opts[m[1]] = m[2]
	}
	for _, k := range Keys() {
		if _, ok := opts[k.Name]; !ok {
			t.Errorf("seed.sql lacks %s", k.Name)
		}
	}
	s, warn := FromOptions(opts)
	if len(warn) != 0 || !reflect.DeepEqual(s, Defaults()) {
		t.Errorf("seed differs from defaults: %+v %q", s, warn)
	}
}

// The Steam Web API key is read but never printed: not by fmt, slog or
// `gamewe option list` (not in Keys), and an invalid one is not repeated.
func TestSteamAPIKey(t *testing.T) {
	const key = "0123456789abcdef0123456789ABCDEF" // not a real key
	s, warn := FromOptions(map[string]string{"gamewe_steam_api_key": " " + key + " "})
	if string(s.SteamAPIKey) != key || len(warn) != 0 {
		t.Fatalf("key %q, warnings %v", s.SteamAPIKey, warn)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "settings", s, "key", s.SteamAPIKey)
	for _, out := range []string{fmt.Sprintf("%v %+v %#v %s", s, s, s, s.SteamAPIKey), buf.String()} {
		if strings.Contains(out, key) {
			t.Errorf("key printed: %s", out)
		}
	}
	for _, k := range Keys() {
		if k.Name == "gamewe_steam_api_key" {
			t.Error("the key is listed in Keys")
		}
	}
	s, warn = FromOptions(map[string]string{"gamewe_steam_api_key": "secret-but-wrong"})
	if s.SteamAPIKey != "" || len(warn) != 0 {
		t.Errorf("invalid key: %q %v", s.SteamAPIKey, warn)
	}
}

// gamewe_block_vac_<game>: off without a key and for 0/off/empty, no
// warning for the per-ranking key.
func TestBlockVAC(t *testing.T) {
	s, warn := FromOptions(map[string]string{"gamewe_block_vac_css": "1", "gamewe_block_vac_tf": "off", "gamewe_block_vac_dod": ""})
	if len(warn) != 0 || !s.BlockVAC("css") || s.BlockVAC("tf") || s.BlockVAC("dod") || s.BlockVAC("cstrike") {
		t.Errorf("%v %v", s.VACBlocked, warn)
	}
	if g := s.VACBlockGames(); len(g) != 1 || !g["css"] {
		t.Errorf("games %v", g)
	}
	if BlockVACKey("css") != "gamewe_block_vac_css" {
		t.Error(BlockVACKey("css"))
	}
}
