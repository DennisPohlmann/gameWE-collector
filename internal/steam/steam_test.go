// SPDX-License-Identifier: GPL-3.0-or-later

package steam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const testKey = "0123456789ABCDEF0123456789ABCDEF" // not a real key

func TestPlayerBans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ISteamUser/GetPlayerBans/v1/" || r.URL.Query().Get("key") != testKey ||
			r.URL.Query().Get("steamids") != "76561197960265739,76561197960265740" {
			t.Errorf("request %s", r.URL)
		}
		w.Write([]byte(`{"players":[{"SteamId":"76561197960265739","CommunityBanned":false,"VACBanned":true,
			"NumberOfVACBans":2,"DaysSinceLastBan":30,"NumberOfGameBans":0,"EconomyBan":"none"},
			{"SteamId":"76561197960265740","VACBanned":false,"NumberOfVACBans":0,"NumberOfGameBans":1}]}`))
	}))
	defer srv.Close()
	c := &Client{API: srv.URL}
	m, err := c.PlayerBans(context.Background(), testKey, []string{"76561197960265739", "76561197960265740"})
	if err != nil {
		t.Fatal(err)
	}
	if b := m["76561197960265739"]; !b.VAC() || b.NumberOfVACBans != 2 {
		t.Errorf("banned %+v", b)
	}
	if b := m["76561197960265740"]; b.VAC() || b.NumberOfGameBans != 1 {
		t.Errorf("game ban only %+v", b)
	}
	if _, err := c.PlayerBans(context.Background(), testKey, []string{"1:5"}); err == nil {
		t.Error("no SteamID64 accepted")
	}
	if _, err := c.PlayerBans(context.Background(), testKey, make([]string, MaxIDs+1)); err == nil {
		t.Error("too many ids accepted")
	}
}

// Errors never carry the key: not on a refused key, a bad status, an
// unreachable host or a redirect.
func TestPlayerBansErrorsHideKey(t *testing.T) {
	status := http.StatusForbidden
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusFound {
			http.Redirect(w, r, "/elsewhere?"+r.URL.RawQuery, status)
			return
		}
		if status == http.StatusOK {
			w.Write([]byte("not json"))
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	ctx := context.Background()
	ids := []string{"76561197960265739"}
	check := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("no error")
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("key in %q", err)
		}
	}
	_, err := (&Client{API: srv.URL}).PlayerBans(ctx, testKey, ids)
	check(err)
	if !errors.Is(err, ErrKey) {
		t.Errorf("403: %v", err)
	}
	for _, status = range []int{http.StatusInternalServerError, http.StatusFound, http.StatusOK} {
		_, err = (&Client{API: srv.URL}).PlayerBans(ctx, testKey, ids)
		check(err)
	}
	srv.Close()
	_, err = (&Client{API: srv.URL}).PlayerBans(ctx, testKey, ids)
	check(err)
}

func TestID64(t *testing.T) {
	for in, want := range map[string]string{
		"1:5": "76561197960265739", "0:6": "76561197960265740", "STEAM_1:1:5": "76561197960265739",
		"STEAM_0:0:6": "76561197960265740", "[U:1:11]": "76561197960265739", "1:0": "76561197960265729",
	} {
		if got, ok := ID64(in); !ok || got != want {
			t.Errorf("%s: %s %v, want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "BOT:Ann", "BOT", "STEAM_ID_LAN", "STEAM_ID_PENDING", "0:0", "2:5", "1:x", "[U:1:0]", "1:99999999999"} {
		if got, ok := ID64(in); ok {
			t.Errorf("%s accepted as %s", in, got)
		}
	}
}

// Steam IDs come from log lines: ID64 never panics and returns only a
// SteamID64 in the individual-account range.
func FuzzID64(f *testing.F) {
	for _, s := range []string{"1:5", "STEAM_0:0:6", "[U:1:11]", "BOT:x", "", "1:", ":1", "[U:1:]", "STEAM_1:1:4294967295", "1:-1", "1:+1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		id, ok := ID64(s)
		if !ok {
			return
		}
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil || n < id64Base || n-id64Base >= 1<<33 {
			t.Fatalf("%q -> %q", s, id)
		}
	})
}

func TestBusy(t *testing.T) {
	for code, want := range map[int]bool{429: true, 500: true, 503: true, 404: false, 400: false} {
		if got := Busy(&StatusError{Code: code}); got != want {
			t.Errorf("%d: %v", code, got)
		}
	}
	if Busy(errors.New("x")) || Busy(ErrKey) {
		t.Error("other errors are not busy")
	}
}
