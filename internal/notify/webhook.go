// SPDX-License-Identifier: GPL-3.0-or-later

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A webhook calls an address when an event happens, for the whole site
// (Settings → Notifications → Webhooks in the web, right
// settings.protected): its own method, headers, payload and expected
// status. One row of hlstats_gamewe_webhooks per webhook, for one or more
// events; the table holds the headers (tokens), so gamewe_web cannot read it.
type Webhook struct {
	ID, Name string
	// Events it is called for (the column event: "hof,reset,update").
	Events      []string
	Enabled     bool
	Method, URL string
	// Headers: one "Name: value" per line (the values are secrets).
	Headers string
	// Payload is the body with placeholders {name} (WebhookVars).
	Payload string
	// Expect is the status the server must answer; 0: any 2xx.
	Expect int
	// AllowPrivate lets it reach loopback and private addresses (a service
	// on the collector's machine or network).
	AllowPrivate bool
}

// Webhook limits (the columns of hlstats_gamewe_webhooks).
const (
	MaxWebhookURL     = 2048
	MaxWebhookText    = 4000 // headers, payload
	MaxWebhookHeaders = 20
	maxWebhookEvents  = 16       // the column event, varchar(16)
	maxWebhookBody    = 64 << 10 // the payload with the values filled in
)

// WebhookMethods are the methods a webhook may use.
var WebhookMethods = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodGet, http.MethodDelete}

var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")

// reservedHeaders are set by the HTTP client; a webhook must not.
var reservedHeaders = []string{"host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "trailer", "keep-alive", "proxy-connection", "expect"}

// WebhookHeader is one header of a webhook.
type WebhookHeader struct{ Name, Value string }

// ParseHeaders reads the header lines of a webhook ("Name: value"); a bad
// name, a reserved header or a control character is an error, which holds
// no value.
func ParseHeaders(v string) ([]WebhookHeader, error) {
	var out []WebhookHeader
	for i, line := range strings.Split(strings.ReplaceAll(v, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !headerName.MatchString(name) {
			return nil, fmt.Errorf("header %d: no valid name", i+1)
		}
		if slices.Contains(reservedHeaders, strings.ToLower(name)) {
			return nil, fmt.Errorf("header %s is set by the collector", name)
		}
		if strings.ContainsFunc(value, func(r rune) bool { return r != '\t' && (r < 0x20 || r == 0x7f) }) || !utf8.ValidString(value) {
			return nil, fmt.Errorf("header %s: control characters in the value", name)
		}
		out = append(out, WebhookHeader{name, value})
	}
	if len(out) > MaxWebhookHeaders {
		return nil, fmt.Errorf("more than %d headers", MaxWebhookHeaders)
	}
	return out, nil
}

// CheckWebhook reports what is wrong with w (the web checks the same
// before it stores one).
func CheckWebhook(w Webhook) error {
	if len(w.Events) == 0 || len(JoinEvents(w.Events)) > maxWebhookEvents {
		return errors.New("no or too many events")
	}
	for _, e := range w.Events {
		if !slices.Contains(Events, e) {
			return fmt.Errorf("unknown event %q", e)
		}
	}
	if !slices.Contains(WebhookMethods, w.Method) {
		return fmt.Errorf("method %q is not allowed", w.Method)
	}
	if w.Expect != 0 && (w.Expect < 100 || w.Expect > 599) {
		return errors.New("the expected status must be 100 to 599 (0: any 2xx)")
	}
	if len(w.URL) > MaxWebhookURL || len(w.Payload) > MaxWebhookText || len(w.Headers) > MaxWebhookText {
		return errors.New("address, headers or payload too long")
	}
	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(w.URL, " \t\r\n") {
		return errors.New("the address must be http(s)://host/... without user or fragment")
	}
	_, err = ParseHeaders(w.Headers)
	return err
}

// SplitEvents reads the column event ("hof,reset"); JoinEvents writes it.
func SplitEvents(v string) []string {
	var out []string
	for _, e := range strings.Split(v, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func JoinEvents(events []string) string { return strings.Join(events, ",") }

// eventVars are the placeholders only some events have; a webhook of
// several events gets them empty for the others (and placeN_name,
// placeN_points, see Render).
var eventVars = map[string][]string{
	EventReset:  {"by"},
	EventHall:   {"from", "to", "places"},
	EventUpdate: {"component", "version", "current", "release_url"},
}

// WebhookVars are the placeholders of a payload: those of every event
// (event, event_label, game, ranking, title, body, url, time, test) and kv
// of the event's own (docs/CONFIG.md, Webhooks). event_label, title and
// body are in the site's language.
func WebhookVars(opts map[string]string, event, game, ranking string, m Message, now time.Time, kv ...string) map[string]string {
	link := ""
	if base := strings.TrimRight(strings.TrimSpace(opts["gamewe_site_url"]), "/"); event != EventUpdate && (strings.HasPrefix(base, "https://") || strings.HasPrefix(base, "http://")) {
		link = base + "/" + game
		if event == EventHall {
			link += "/hall-of-fame"
		}
	}
	out := map[string]string{}
	for _, vs := range eventVars {
		for _, v := range vs {
			out[v] = ""
		}
	}
	maps.Copy(out, map[string]string{"event": event, "event_label": t(Lang(opts), "event_"+event), "game": game, "ranking": ranking, "title": m.Title, "body": m.Body, "url": link, "time": now.UTC().Format(time.RFC3339), "test": "0"})
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// HallVars are the hall of fame's own placeholders.
func HallVars(from, to time.Time, top []Place) []string {
	kv := []string{"from", from.Format(time.DateOnly), "to", to.AddDate(0, 0, -1).Format(time.DateOnly)}
	var lines []string
	for _, p := range top {
		n := strconv.Itoa(p.Place)
		kv = append(kv, "place"+n+"_name", oneLine(p.Name), "place"+n+"_points", strconv.FormatInt(p.Points, 10))
		lines = append(lines, fmt.Sprintf("%d. %s (%d)", p.Place, oneLine(p.Name), p.Points))
	}
	return append(kv, "places", strings.Join(lines, "\n"))
}

var (
	placeholder = regexp.MustCompile(`\{([a-z0-9_]{1,32})\}`)
	placeVar    = regexp.MustCompile(`^\{place[0-9]+_(name|points)\}$`)
)

// Render fills the placeholders of payload with vars, escaped for the
// body's Content-Type: inside a JSON string (application/json, …+json),
// form-encoded (application/x-www-form-urlencoded) or as they are. An
// unknown placeholder stays as it is; a place not stored
// ({place9_name}, or any place outside a hall of fame) is empty.
func Render(payload, contentType string, vars map[string]string) string {
	ct := strings.ToLower(contentType)
	esc := func(v string) string { return v }
	switch {
	case strings.Contains(ct, "json"):
		esc = func(v string) string {
			var b strings.Builder
			e := json.NewEncoder(&b)
			e.SetEscapeHTML(false)
			_ = e.Encode(v)
			out := strings.TrimSuffix(b.String(), "\n")
			return out[1 : len(out)-1] // without the quotes
		}
	case strings.Contains(ct, "x-www-form-urlencoded"):
		esc = url.QueryEscape
	}
	return placeholder.ReplaceAllStringFunc(payload, func(p string) string {
		v, ok := vars[p[1:len(p)-1]]
		if !ok && placeVar.MatchString(p) {
			return ""
		}
		if !ok {
			return p
		}
		return esc(v)
	})
}

// privateClient is publicClient without the address check (AllowPrivate).
var privateClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// SendWebhook calls w with its payload filled with vars and returns the
// status the server answered. An answer other than the expected one is an
// error. No redirect is followed (a 3xx is the answer). The error holds no
// header value and no address (its query may carry a token).
func (s *Sender) SendWebhook(ctx context.Context, w Webhook, vars map[string]string) (int, error) {
	status, err := s.sendWebhook(ctx, w, vars)
	if err != nil {
		msg := err.Error()
		if hs, perr := ParseHeaders(w.Headers); perr == nil {
			for _, h := range hs {
				if len(h.Value) >= 4 {
					msg = strings.ReplaceAll(msg, h.Value, "[hidden]")
				}
			}
		}
		err = errors.New("webhook: " + msg)
	}
	return status, err
}

func (s *Sender) sendWebhook(ctx context.Context, w Webhook, vars map[string]string) (int, error) {
	if err := CheckWebhook(w); err != nil {
		return 0, err
	}
	headers, _ := ParseHeaders(w.Headers)
	ct := ""
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			ct = h.Value
		}
	}
	body := Render(w.Payload, ct, vars)
	if len(body) > maxWebhookBody {
		return 0, errors.New("the payload is larger than 64 KiB")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var rd io.Reader
	if body != "" && w.Method != http.MethodGet {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, w.Method, w.URL, rd)
	if err != nil {
		return 0, errors.New("bad request")
	}
	req.Header.Set("User-Agent", "gameWE-collector")
	if rd != nil && ct == "" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	for _, h := range headers {
		req.Header.Add(h.Name, h.Value)
	}
	client := publicClient
	if w.AllowPrivate {
		client = privateClient
	}
	if s != nil && s.HTTP != nil {
		client = s.HTTP
	}
	status, err := s.do(client, req, nil)
	if err != nil {
		return status, err
	}
	if (w.Expect == 0 && status/100 != 2) || (w.Expect != 0 && status != w.Expect) {
		want := "2xx"
		if w.Expect != 0 {
			want = strconv.Itoa(w.Expect)
		}
		return status, fmt.Errorf("HTTP %d, expected %s", status, want)
	}
	return status, nil
}

// WebhookStatus is the value of last_status: like Status, with the HTTP
// status the server answered ("<unix>|ok|200", "<unix>|error|<text>").
func WebhookStatus(now time.Time, status int, err error) string {
	if err == nil {
		return Status(now, nil) + "|" + strconv.Itoa(status)
	}
	return Status(now, err)
}
