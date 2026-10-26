// SPDX-License-Identifier: GPL-3.0-or-later

package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"0.17.0", "0.16.0", true},
		{"v1.0.0", "0.99.9", true},
		{"0.16.1", "0.16.0", true},
		{"0.16.0", "0.16.0", false},
		{"0.15.9", "0.16.0", false},
		{"0.10.0", "0.9.0", true},
		{"0.17.0", "dev", false},
		{"garbage", "0.16.0", false},
		{"0.17.0", "0.16.0-3-gabc", true},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	body := `{"tag_name":"v0.17.0","html_url":"https://github.com/DennisPohlmann/gameWE-collector/releases/tag/v0.17.0"}`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repo+"/releases/latest" || r.Header.Get("User-Agent") == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	defer s.Close()
	old := API
	API = s.URL
	defer func() { API = old }()

	r, err := Check(context.Background(), s.Client(), Repo, "0.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Update || r.Latest != "0.17.0" || r.URL != "https://github.com/DennisPohlmann/gameWE-collector/releases/tag/v0.17.0" {
		t.Errorf("got %+v", r)
	}
	// A link elsewhere is replaced by the release page.
	body = `{"tag_name":"v0.17.0","html_url":"https://evil.example/x"}`
	if r, _ = Check(context.Background(), s.Client(), Repo, "0.17.0"); r.Update || r.URL != "https://github.com/"+Repo+"/releases/tag/v0.17.0" {
		t.Errorf("got %+v", r)
	}
	body = `{"tag_name":"nightly"}`
	if _, err := Check(context.Background(), s.Client(), Repo, "0.16.0"); err == nil {
		t.Error("tag without a version accepted")
	}
}
