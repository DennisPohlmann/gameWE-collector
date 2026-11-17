// SPDX-License-Identifier: GPL-3.0-or-later

package notify

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRender(t *testing.T) {
	vars := map[string]string{"name": "a\"b\n<c>&d=e", "n": "3"}
	for ct, want := range map[string]string{
		"application/json":                  `{"t":"a\"b\n<c>&d=e","n":3,"x":"{unknown}"}`,
		"application/vnd.api+json":          `{"t":"a\"b\n<c>&d=e","n":3,"x":"{unknown}"}`,
		"application/x-www-form-urlencoded": `{"t":"a%22b%0A%3Cc%3E%26d%3De","n":3,"x":"{unknown}"}`,
		"":                                  "{\"t\":\"a\"b\n<c>&d=e\",\"n\":3,\"x\":\"{unknown}\"}",
	} {
		if got := Render(`{"t":"{name}","n":{n},"x":"{unknown}"}`, ct, vars); got != want {
			t.Errorf("%q: %s", ct, got)
		}
	}
}

func TestCheckWebhook(t *testing.T) {
	ok := Webhook{Event: EventHall, Method: "POST", URL: "https://example.org/hook?x=1", Headers: "Authorization: Bearer abc\nX-A: b", Expect: 201}
	if err := CheckWebhook(ok); err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]Webhook{
		"event":    {Event: "x", Method: "POST", URL: ok.URL},
		"method":   {Event: EventHall, Method: "TRACE", URL: ok.URL},
		"scheme":   {Event: EventHall, Method: "POST", URL: "ftp://example.org/"},
		"user":     {Event: EventHall, Method: "POST", URL: "https://u:p@example.org/"},
		"no host":  {Event: EventHall, Method: "POST", URL: "https:///x"},
		"space":    {Event: EventHall, Method: "POST", URL: "https://example.org/a b"},
		"expect":   {Event: EventHall, Method: "POST", URL: ok.URL, Expect: 700},
		"header":   {Event: EventHall, Method: "POST", URL: ok.URL, Headers: "Bad Name: x"},
		"reserved": {Event: EventHall, Method: "POST", URL: ok.URL, Headers: "Host: evil.example"},
		"control":  {Event: EventHall, Method: "POST", URL: ok.URL, Headers: "X-A: a\x00b"},
		"no colon": {Event: EventHall, Method: "POST", URL: ok.URL, Headers: "X-A"},
	} {
		if err := CheckWebhook(w); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), "a\x00b") {
			t.Errorf("%s: value in the error", name)
		}
	}
}

func TestSendWebhook(t *testing.T) {
	var got struct {
		method, ct, auth, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.ct, got.auth, got.body = r.Method, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), string(b)
		switch r.URL.Path {
		case "/created":
			w.WriteHeader(http.StatusCreated)
		case "/moved":
			http.Redirect(w, r, "/created", http.StatusFound)
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Bearer secret-token-1 rejected"))
		}
	}))
	defer srv.Close()
	s := &Sender{HTTP: &http.Client{CheckRedirect: privateClient.CheckRedirect}}
	vars := WebhookVars(map[string]string{"gamewe_site_url": "https://stats.example.org/"}, EventHall, "css", "Counter-Strike", Message{Title: "T"}, time.Unix(0, 0),
		HallVars(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), []Place{{1, `Pl"ayer`, 1500}})...)
	w := Webhook{ID: "a", Event: EventHall, Method: "PUT", URL: srv.URL + "/created", Expect: 201,
		Headers: "Content-Type: application/json\nAuthorization: Bearer secret-token-1", Payload: `{"who":"{place1_name}","pts":{place1_points},"url":"{url}","to":"{to}"}`}
	status, err := s.SendWebhook(context.Background(), w, vars)
	if err != nil || status != 201 {
		t.Fatal(status, err)
	}
	if got.method != "PUT" || got.ct != "application/json" || got.auth != "Bearer secret-token-1" ||
		got.body != `{"who":"Pl\"ayer","pts":1500,"url":"https://stats.example.org/css/hall-of-fame","to":"2026-10-31"}` {
		t.Errorf("sent %+v", got)
	}
	// Another status than the expected one fails; a redirect is not followed.
	w.URL = srv.URL + "/moved"
	if status, err := s.SendWebhook(context.Background(), w, vars); status != 302 || err == nil || !strings.Contains(err.Error(), "HTTP 302, expected 201") {
		t.Errorf("redirect: %d %v", status, err)
	}
	w.URL, w.Expect = srv.URL+"/fail", 0
	if _, err := s.SendWebhook(context.Background(), w, vars); err == nil || !strings.Contains(err.Error(), "expected 2xx") || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("fail: %v", err)
	}
	// GET sends no body.
	w.Method, w.URL = "GET", srv.URL+"/"
	if _, err := s.SendWebhook(context.Background(), w, vars); err != nil || got.body != "" {
		t.Errorf("get: %v %q", err, got.body)
	}
	// The default sender does not reach the collector's own network, unless allowed.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if _, err := (&Sender{}).SendWebhook(context.Background(), w, vars); err == nil || !strings.Contains(err.Error(), "public address") {
		t.Errorf("loopback reached: %v", err)
	}
	w.AllowPrivate = true
	if status, err := (&Sender{}).SendWebhook(context.Background(), w, vars); err != nil || status != 200 {
		t.Errorf("allowed private: %d %v", status, err)
	}
	if v := WebhookStatus(time.Unix(5, 0), 201, nil); v != "5|ok|201" {
		t.Error(v)
	}
}
