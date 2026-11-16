// SPDX-License-Identifier: GPL-3.0-or-later

package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

const hash = "0123456789abcdef0123456789abcdef"

func TestTargets(t *testing.T) {
	opts := map[string]string{
		"gamewe_notify_email": "1", "gamewe_notify_email_to": "a@example.org, b@example.org", "gamewe_notify_email_hof": "0",
		"gamewe_notify_telegram":                              "0", // no shared chat; off: the admin's own still go
		"gamewe_notify_telegram_token":                        "123456:ABCdefGHIjklMNOpqrSTUvwxYZ012345678",
		UserPrefix + hash + "_telegram":                       "update,reset,bogus|@gamewe_admins",
		UserPrefix + hash + "_ntfy":                           "reset|mytopic",        // ntfy is off: own ones need nothing
		UserPrefix + hash + "_email":                          "reset|me@example.org", // no SMTP server
		UserPrefix + "ffffffffffffffffffffffffffffffff_email": "|x@example.org",       // no event
		UserPrefix + "short_email":                            "reset|x@example.org",
	}
	got := Targets(opts)
	want := []Target{
		{Channel: Email, To: "a@example.org, b@example.org", Events: []string{EventReset}},
		{Channel: Telegram, User: hash, To: "@gamewe_admins", Events: []string{EventUpdate, EventReset}},
		{Channel: Ntfy, User: hash, To: "mytopic", Events: []string{EventReset}},
	}
	if len(got) != len(want) {
		t.Fatalf("targets %+v", got)
	}
	for i := range want {
		if got[i].Channel != want[i].Channel || got[i].User != want[i].User || got[i].To != want[i].To || !slices.Equal(got[i].Events, want[i].Events) {
			t.Errorf("target %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if w := Wanted(opts, EventHall, EventReset); len(w) != 3 {
		t.Errorf("hall or reset: %+v", w)
	}
	if w := Wanted(opts, EventUpdate); len(w) != 1 || w[0].User != hash {
		t.Errorf("update: %+v", w)
	}
	if w := Wanted(opts, EventHall); len(w) != 0 {
		t.Errorf("hall: %+v", w)
	}
	if got := want[1].StatusKey(); got != UserPrefix+hash+"_last_telegram" {
		t.Error(got)
	}
	if _, err := Find(opts, "", Telegram); err == nil {
		t.Error("shared telegram without a chat found")
	}
	if tg, err := Find(opts, hash, Telegram); err != nil || tg.To != "@gamewe_admins" {
		t.Errorf("own telegram: %+v %v", tg, err)
	}
	if n, err := Find(opts, hash, Ntfy); err != nil || n.To != "mytopic" {
		t.Errorf("own ntfy while it is off for the shared: %+v %v", n, err)
	}
	if _, err := Find(opts, "", Ntfy); err == nil {
		t.Error("shared ntfy is off")
	}
	if _, err := Find(opts, hash, Email); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Errorf("own e-mail without a server: %v", err)
	}
	opts["gamewe_notify_smtp_host"], opts["gamewe_notify_email_from"] = "smtp.example.org", "stats@example.org"
	if len(Targets(opts)) != 4 {
		t.Errorf("own e-mail with a server: %+v", Targets(opts))
	}
	if _, err := Find(opts, "../x", Email); err == nil {
		t.Error("bad hash")
	}
}

func TestOtherKey(t *testing.T) {
	for _, k := range []string{"gamewe_notify_email", "gamewe_notify_smtp_password", "gamewe_notify_ntfy_update", "gamewe_notify_last_email", "gamewe_notify_seen_web", UserPrefix + hash + "_ntfy", UserPrefix + hash + "_last_email", UserTokenKey(hash)} {
		if !OtherKey(k) {
			t.Errorf("%s not known", k)
		}
	}
	for _, k := range []string{"gamewe_notify_x", "gamewe_notify_last_sms", UserPrefix + "abc_email", UserPrefix + hash + "_sms", UserPrefix + hash + "_email_token"} {
		if OtherKey(k) {
			t.Errorf("%s known", k)
		}
	}
	if !IsUserSecret(UserTokenKey(hash)) || IsUserSecret(UserPrefix+"abc_ntfy_token") || IsUserSecret(UserPrefix+hash+"_ntfy") || IsUserSecret("x"+UserTokenKey(hash)) {
		t.Error("IsUserSecret")
	}
}

func TestRecipients(t *testing.T) {
	got, err := Recipients("a@example.org; b@example.org a@example.org")
	if err != nil || !slices.Equal(got, []string{"a@example.org", "b@example.org"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", "x", "A <a@example.org>", "a@example.org\r\nBcc: c@example.org"} {
		if _, err := Recipients(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	many := make([]string, MaxRecipients+1)
	for i := range many {
		many[i] = string(rune('a'+i)) + "@example.org"
	}
	if _, err := Recipients(strings.Join(many, ",")); err == nil {
		t.Error("too many accepted")
	}
}

// The errors never carry a secret, also not when a server echoes it.
func TestTelegramAndCleanErrors(t *testing.T) {
	const token = "123456:ABCdefGHIjklMNOpqrSTUvwxYZ012345678"
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/sendMessage" {
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		got = r.PostForm
		if got.Get("chat_id") == "@full_chat" {
			w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found for ` + token + `"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	s := &Sender{TelegramAPI: srv.URL}
	opts := map[string]string{"gamewe_notify_telegram_token": token}
	if err := s.Send(context.Background(), opts, Target{Channel: Telegram, To: "-1001234"}, Message{Title: "T", Body: "Ä <b>"}); err != nil {
		t.Fatal(err)
	}
	if got.Get("text") != "T\n\nÄ <b>" || got.Get("parse_mode") != "" {
		t.Errorf("sent %v", got)
	}
	err := s.Send(context.Background(), opts, Target{Channel: Telegram, To: "@full_chat"}, Message{})
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error %v", err)
	}
	// Unreachable: the URL with the token is not in the error.
	s.TelegramAPI = "http://127.0.0.1:1"
	if err := s.Send(context.Background(), opts, Target{Channel: Telegram, To: "-1"}, Message{}); err == nil || strings.Contains(err.Error(), "ABCdef") {
		t.Errorf("error %v", err)
	}
	if err := s.Send(context.Background(), map[string]string{"gamewe_notify_telegram_token": "nope"}, Target{Channel: Telegram, To: "-1"}, Message{}); err == nil {
		t.Error("bad token accepted")
	}
	if err := s.Send(context.Background(), opts, Target{Channel: Telegram, To: "chat; drop"}, Message{}); err == nil {
		t.Error("bad chat accepted")
	}
}

func TestNtfy(t *testing.T) {
	var body map[string]any
	var auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		if body["topic"] == "denied" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":40301,"error":"forbidden"}`))
			return
		}
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()
	s := &Sender{HTTP: srv.Client()}
	opts := map[string]string{"gamewe_notify_ntfy_url": srv.URL + "/", "gamewe_notify_ntfy_token": "tk_secretsecret"}
	if err := s.Send(context.Background(), opts, Target{Channel: Ntfy, To: "game_we"}, Message{Title: "Ruhmeshalle ä", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if body["topic"] != "game_we" || body["title"] != "Ruhmeshalle ä" || body["message"] != "b" || auth != "Bearer tk_secretsecret" {
		t.Errorf("sent %v %q", body, auth)
	}
	if err := s.Send(context.Background(), opts, Target{Channel: Ntfy, To: "denied"}, Message{}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("error %v", err)
	}
	for _, u := range []string{"http://ntfy.example.org", "https://u:p@ntfy.example.org", "ftp://x"} {
		if err := s.Send(context.Background(), map[string]string{"gamewe_notify_ntfy_url": u}, Target{Channel: Ntfy, To: "t"}, Message{}); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	if err := s.Send(context.Background(), opts, Target{Channel: Ntfy, To: "a/b"}, Message{}); err == nil {
		t.Error("bad topic accepted")
	}
	// An admin's own server: their topic there, never the site's token.
	own := Target{Channel: Ntfy, User: strings.Repeat("a", 32), To: srv.URL + "/mine"}
	if err := s.Send(context.Background(), opts, own, Message{Title: "t"}); err != nil || body["topic"] != "mine" || auth != "" {
		t.Errorf("own server: %v %v %q", err, body, auth)
	}
	// Their own token: on their own server and instead of the site's one;
	// never in an error.
	opts[UserTokenKey(own.User)] = "tk_mineminemine"
	if err := s.Send(context.Background(), opts, own, Message{}); err != nil || auth != "Bearer tk_mineminemine" {
		t.Errorf("own token: %v %q", err, auth)
	}
	if err := s.Send(context.Background(), opts, Target{Channel: Ntfy, User: own.User, To: "game_we"}, Message{}); err != nil || auth != "Bearer tk_mineminemine" {
		t.Errorf("own token on the site's server: %v %q", err, auth)
	}
	if err := s.Send(context.Background(), opts, Target{Channel: Ntfy, To: "game_we"}, Message{}); err != nil || auth != "Bearer tk_secretsecret" {
		t.Errorf("shared target took an admin's token: %v %q", err, auth)
	}
	if err := clean(errors.New("echo tk_mineminemine"), opts); strings.Contains(err.Error(), "tk_mine") {
		t.Errorf("own token in an error: %v", err)
	}
	delete(opts, UserTokenKey(own.User))
	// Not for the shared recipients (the server fields need a higher right).
	if err := s.Send(context.Background(), opts, Target{Channel: Ntfy, To: srv.URL + "/mine"}, Message{}); err == nil {
		t.Error("shared topic with a server accepted")
	}
	for _, to := range []string{"http://ntfy.example.org/t", "https://ntfy.example.org/", "https://ntfy.example.org/t?x"} {
		own.To = to
		if err := s.Send(context.Background(), opts, own, Message{}); err == nil {
			t.Errorf("%s accepted", to)
		}
	}
	// The default sender does not reach the collector's own network.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	own.To = srv.URL + "/mine"
	if err := (&Sender{}).Send(context.Background(), opts, own, Message{}); err == nil || !strings.Contains(err.Error(), "public address") {
		t.Errorf("loopback reached: %v", err)
	}
}

func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{"127.0.0.1:443": false, "10.1.2.3:443": false, "192.168.0.1:443": false, "169.254.169.254:80": false, "100.64.0.1:443": false, "[::1]:443": false, "[fd00::1]:443": false, "[::ffff:10.0.0.1]:443": false, "0.0.0.0:443": false, "93.184.216.34:443": true, "[2606:4700::1]:443": true} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// A minimal SMTP server on localhost without TLS (security none).
func TestMail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		var lines []string
		say := func(s string) { io.WriteString(c, s+"\r\n") }
		say("220 test")
		data := false
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				got <- lines
				return
			}
			l = strings.TrimRight(l, "\r\n")
			lines = append(lines, l)
			switch {
			case data && l == ".":
				data = false
				say("250 ok")
			case data:
			case strings.HasPrefix(l, "EHLO"):
				say("250-test")
				say("250 8BITMIME")
			case l == "DATA":
				data = true
				say("354 go")
			case l == "QUIT":
				say("221 bye")
				got <- lines
				return
			default:
				say("250 ok")
			}
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	opts := map[string]string{"gamewe_notify_smtp_host": host, "gamewe_notify_smtp_port": port, "gamewe_notify_smtp_security": "none", "gamewe_notify_email_from": "stats@example.org"}
	m := Message{Title: "Ruhmeshalle CS: Oktober", Body: "1. Spieler\n.\nEnde"}
	if err := (&Sender{}).Send(context.Background(), opts, Target{Channel: Email, To: "a@example.org,b@example.org"}, m); err != nil {
		t.Fatal(err)
	}
	var lines []string
	select {
	case lines = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no mail")
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"MAIL FROM:<stats@example.org>", "RCPT TO:<a@example.org>", "RCPT TO:<b@example.org>", "To: undisclosed-recipients:;", "Content-Transfer-Encoding: quoted-printable", "..\n"} {
		if !strings.Contains(all+"\n", want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	for _, l := range lines {
		if s, ok := strings.CutPrefix(l, "Subject: "); ok {
			if d, _ := new(mime.WordDecoder).DecodeHeader(s); d != m.Title {
				t.Errorf("subject %q", d)
			}
		}
		if strings.Contains(l, "b@example.org") && strings.HasPrefix(l, "To:") {
			t.Error("recipients visible to each other")
		}
	}
	// STARTTLS is required by default: this server has none.
	opts["gamewe_notify_smtp_security"] = ""
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln2.Close()
	go func() {
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		io.WriteString(c, "220 test\r\n")
		r.ReadString('\n')
		io.WriteString(c, "250 test\r\n")
		r.ReadString('\n')
	}()
	_, opts["gamewe_notify_smtp_port"], _ = net.SplitHostPort(ln2.Addr().String())
	if err := (&Sender{}).Send(context.Background(), opts, Target{Channel: Email, To: "a@example.org"}, m); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("without STARTTLS: %v", err)
	}
	opts["gamewe_notify_email_from"] = "x\r\nBcc: y@example.org"
	if err := (&Sender{}).Send(context.Background(), opts, Target{Channel: Email, To: "a@example.org"}, m); err == nil {
		t.Error("bad sender accepted")
	}
}

func TestStatus(t *testing.T) {
	now := time.Unix(100, 0)
	if Status(now, nil) != "100|ok" {
		t.Error(Status(now, nil))
	}
	s := Status(now, errors.New(strings.Repeat("ä", 150)))
	if !strings.HasPrefix(s, "100|error|ää") || len(s) > 255 || !strings.HasSuffix(s, "…") {
		t.Errorf("%q (%d)", s, len(s))
	}
}

func TestMessages(t *testing.T) {
	opts := map[string]string{"gamewe_default_lang": "swg", "gamewe_site_url": "https://stats.example.org/"}
	m := HallMessage(opts, "css", "Counter-Strike", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), []Place{{1, "Al\nice", 1500}})
	if m.Title != "Ruhmeshalle Counter-Strike: 2026-10-01 – 2026-10-31" || !strings.Contains(m.Body, "1. Al ice (1500 Punkte)") || !strings.HasSuffix(m.Body, "https://stats.example.org/css/hall-of-fame") {
		t.Errorf("%+v", m)
	}
	opts["gamewe_notify_lang"] = "ar"
	if m := ResetMessage(opts, "css", "CS", "admin"); !strings.Contains(m.Body, "admin") || strings.Contains(m.Title, "{") {
		t.Errorf("%+v", m)
	}
	opts["gamewe_notify_lang"] = "xx"
	opts["gamewe_default_lang"] = ""
	if m := UpdateMessage(opts, "web", "1.21.0", "", "https://github.com/x/y/releases/tag/v1.21.0"); m.Title != "gameWE web 1.21.0 is out" || strings.Contains(m.Body, "Running") {
		t.Errorf("%+v", m)
	}
	for l, tx := range texts {
		for k := range texts["en"] {
			if tx[k] == "" {
				t.Errorf("%s lacks %s", l, k)
			}
		}
	}
}
