// SPDX-License-Identifier: GPL-3.0-or-later

// Package notify sends short messages about what the collector did (a
// ranking reset, a hall of fame) by e-mail (SMTP), Telegram (a bot posts to
// a chat or channel) or ntfy (a topic on ntfy.sh or an own ntfy server).
// The web admin sets it up under Settings → Services → Notifications; it is
// stored in hlstats_Options (gamewe_notify_*), the passwords and tokens in
// hlstats_gamewe_secrets. Nothing here is logged with a secret: errors are
// cleaned of them before they leave the package.
//
// A new event: add it to Events (its key suffix), give it texts in
// texts.go, a switch per channel in the web's settings page
// (gamewe_notify_<channel>_<event>) and send it with Send.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Channels, in the order of the web's settings page.
const (
	Email    = "email"
	Telegram = "telegram"
	Ntfy     = "ntfy"
)

var Channels = []string{Email, Telegram, Ntfy}

// Events, as the suffix of gamewe_notify_<channel>_<event> and in the list
// of an admin's own notifications.
const (
	EventReset  = "reset"  // a ranking was reset (by an admin, the API, the CLI or a schedule)
	EventHall   = "hof"    // a period ended: the hall of fame was stored and the ranking reset
	EventUpdate = "update" // a new release of the collector or the web is out
)

var Events = []string{EventReset, EventHall, EventUpdate}

// The default of a shared target's event (gamewe_notify_<channel>_<event>
// not set): the resets and the hall of fame, no updates.
func eventDefault(e string) bool { return e != EventUpdate }

// MaxRecipients is the most e-mail addresses one target takes.
const MaxRecipients = 20

// Timeout of one message to one target.
const Timeout = 20 * time.Second

// UserPrefix starts the keys of an admin's own notifications:
// gamewe_notifyu_<hash>_<channel> = "<events>|<address>" (events comma
// separated, address: an e-mail address, a Telegram chat or an ntfy topic)
// and gamewe_notifyu_<hash>_last_<channel> (written by the collector, as
// StatusKey). hash is the one of the admin's rights (gamewe_perms_<hash>
// of the web): the first 32 hex digits of SHA-256 of the lower-case name.
const UserPrefix = "gamewe_notifyu_"

// SecretKeys are stored in hlstats_gamewe_secrets.
var SecretKeys = []string{"gamewe_notify_smtp_password", "gamewe_notify_telegram_token", "gamewe_notify_ntfy_token"}

// Key is one option this package reads (docs/CONFIG.md). They are set in
// the web admin, so like the web's keys they are not in settings.Keys
// (`gamewe option list` would print the secrets).
type Key struct{ Name, Default, Meaning string }

// Keys lists the options of the notifications, the secret ones included.
func Keys() []Key {
	k := []Key{
		{"gamewe_notify_lang", "", "language of the notifications (a code of the web, e.g. de); empty: gamewe_default_lang, else English"},
		{"gamewe_notify_email", "0", "1 = e-mail is on (for the shared recipients and the admins' own notifications)"},
		{"gamewe_notify_email_to", "", "shared e-mail recipients, separated by commas (at most 20); empty: none"},
		{"gamewe_notify_email_from", "", "sender address of the e-mails"},
		{"gamewe_notify_smtp_host", "", "SMTP server"},
		{"gamewe_notify_smtp_port", "587", "SMTP port"},
		{"gamewe_notify_smtp_security", "starttls", "starttls (port 587), tls (port 465) or none (only a server on the same machine)"},
		{"gamewe_notify_smtp_user", "", "SMTP user; empty: no login"},
		{"gamewe_notify_smtp_password", "", "SMTP password (hlstats_gamewe_secrets)"},
		{"gamewe_notify_telegram", "0", "1 = Telegram is on"},
		{"gamewe_notify_telegram_token", "", "token of the Telegram bot from @BotFather (hlstats_gamewe_secrets)"},
		{"gamewe_notify_telegram_chat", "", "shared Telegram chat: @channelname or the numeric chat id; empty: none"},
		{"gamewe_notify_ntfy", "0", "1 = ntfy is on"},
		{"gamewe_notify_ntfy_url", "https://ntfy.sh", "address of the ntfy server (https)"},
		{"gamewe_notify_ntfy_topic", "", "shared ntfy topic; empty: none"},
		{"gamewe_notify_ntfy_token", "", "ntfy access token; empty: none (hlstats_gamewe_secrets)"},
	}
	for _, c := range Channels {
		k = append(k, Key{"gamewe_notify_" + c + "_" + EventReset, "1", "1 = the shared " + c + " target gets every ranking reset"},
			Key{"gamewe_notify_" + c + "_" + EventHall, "1", "1 = the shared " + c + " target gets the hall of fame when a period ends"},
			Key{"gamewe_notify_" + c + "_" + EventUpdate, "0", "1 = the shared " + c + " target gets new releases of the collector and the web"})
	}
	return k
}

// OtherKey reports the keys of this package for `gamewe check`: Keys, the
// ones the collector writes (StatusKey, SeenKey) and the admins' own.
func OtherKey(k string) bool {
	if slices.ContainsFunc(Keys(), func(x Key) bool { return x.Name == k }) {
		return true
	}
	if c, ok := strings.CutPrefix(k, "gamewe_notify_last_"); ok {
		return slices.Contains(Channels, c)
	}
	if c, ok := strings.CutPrefix(k, "gamewe_notify_seen_"); ok {
		return c == "web" || c == "collector"
	}
	if r, ok := strings.CutPrefix(k, UserPrefix); ok {
		h, c, ok := strings.Cut(r, "_")
		c = strings.TrimPrefix(c, "last_")
		return ok && userHash(h) && slices.Contains(Channels, c)
	}
	return false
}

func userHash(h string) bool { return len(h) == 32 && strings.Trim(h, "0123456789abcdef") == "" }

// SeenKey holds the release of component (web or collector) last told
// about, so each one is sent once.
func SeenKey(component string) string { return "gamewe_notify_seen_" + component }

// Target is who gets a message on one channel: the shared recipients of
// the settings page (User empty) or one admin's own address.
type Target struct {
	Channel string
	User    string // the admin's hash; empty: shared
	To      string // e-mail addresses, Telegram chat or ntfy topic
	Events  []string
}

// StatusKey is where the collector writes the outcome of the last message
// to t: "<unix time>|ok" or "<unix time>|error|<text>".
func (t Target) StatusKey() string {
	if t.User == "" {
		return "gamewe_notify_last_" + t.Channel
	}
	return UserPrefix + t.User + "_last_" + t.Channel
}

// On reports whether a channel is turned on.
func On(opts map[string]string, channel string) bool {
	return flag(opts, "gamewe_notify_"+channel, false)
}

var sharedTo = map[string]string{Email: "gamewe_notify_email_to", Telegram: "gamewe_notify_telegram_chat", Ntfy: "gamewe_notify_ntfy_topic"}

// Targets returns every target of the channels that are on: per channel
// the shared one (if it has an address) and then the admins' own, sorted.
func Targets(opts map[string]string) []Target {
	var out []Target
	for _, c := range Channels {
		if !On(opts, c) {
			continue
		}
		if to := strings.TrimSpace(opts[sharedTo[c]]); to != "" {
			t := Target{Channel: c, To: to}
			for _, e := range Events {
				if flag(opts, "gamewe_notify_"+c+"_"+e, eventDefault(e)) {
					t.Events = append(t.Events, e)
				}
			}
			out = append(out, t)
		}
		var own []Target
		for k, v := range opts {
			r, ok := strings.CutPrefix(k, UserPrefix)
			h, ch, _ := strings.Cut(r, "_")
			if !ok || ch != c || !userHash(h) {
				continue
			}
			if t, ok := ParseUser(c, h, v); ok {
				own = append(own, t)
			}
		}
		slices.SortFunc(own, func(a, b Target) int { return strings.Compare(a.User, b.User) })
		out = append(out, own...)
	}
	return out
}

// ParseUser reads an admin's value "<events>|<address>"; false without
// an address or event.
func ParseUser(channel, hash, v string) (Target, bool) {
	ev, to, ok := strings.Cut(v, "|")
	to = strings.TrimSpace(to)
	t := Target{Channel: channel, User: hash, To: to}
	for _, e := range strings.Split(ev, ",") {
		if e = strings.TrimSpace(e); slices.Contains(Events, e) && !slices.Contains(t.Events, e) {
			t.Events = append(t.Events, e)
		}
	}
	return t, ok && to != "" && len(t.Events) > 0
}

// Wanted returns the targets that get one of events (each target once).
func Wanted(opts map[string]string, events ...string) []Target {
	var out []Target
	for _, t := range Targets(opts) {
		if slices.ContainsFunc(events, func(e string) bool { return slices.Contains(t.Events, e) }) {
			out = append(out, t)
		}
	}
	return out
}

// Find returns the target of a test message: the shared one of channel
// (user empty) or the admin's own, also without an event chosen.
func Find(opts map[string]string, user, channel string) (Target, error) {
	if !slices.Contains(Channels, channel) {
		return Target{}, fmt.Errorf("unknown channel %q", channel)
	}
	if !On(opts, channel) {
		return Target{}, fmt.Errorf("%s is turned off in the settings", channel)
	}
	if user == "" {
		if to := strings.TrimSpace(opts[sharedTo[channel]]); to != "" {
			return Target{Channel: channel, To: to}, nil
		}
		return Target{}, fmt.Errorf("%s: no shared recipient set", channel)
	}
	if !userHash(user) {
		return Target{}, errors.New("bad admin")
	}
	_, to, _ := strings.Cut(opts[UserPrefix+user+"_"+channel], "|")
	if to = strings.TrimSpace(to); to == "" {
		return Target{}, fmt.Errorf("%s: no own address set", channel)
	}
	return Target{Channel: channel, User: user, To: to}, nil
}

// Message is one notification; the body is plain text.
type Message struct {
	Title, Body string
}

// Sender sends messages. Its zero value uses http.DefaultClient and the
// public Telegram API.
type Sender struct {
	HTTP        *http.Client
	TelegramAPI string // tests; empty: https://api.telegram.org
	// Mail sends an e-mail (tests); nil: SMTP with the settings.
	Mail func(ctx context.Context, opts map[string]string, to []string, m Message) error
}

func (s *Sender) client() *http.Client {
	if s != nil && s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// publicClient reaches only public addresses and follows no redirect:
// for an admin's own ntfy server.
// shortcut: it dials directly (no HTTPS_PROXY); add the proxy when a
// collector behind one needs own servers.
var publicClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10") // carrier-grade NAT

// publicOnly refuses to connect to loopback, private, link-local,
// shared and unspecified addresses (checked after the name lookup).
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || sharedAddressSpace.Contains(ip) {
		return errors.New("the own ntfy server has no public address")
	}
	return nil
}

func flag(opts map[string]string, key string, def bool) bool {
	switch strings.TrimSpace(opts[key]) {
	case "1":
		return true
	case "0":
		return false
	}
	return def
}

// Send sends m to t with the channel's settings from opts. The error
// holds no password or token.
func (s *Sender) Send(ctx context.Context, opts map[string]string, t Target, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var err error
	switch t.Channel {
	case Email:
		var to []string
		if to, err = Recipients(t.To); err != nil {
			err = fmt.Errorf("e-mail: %w", err)
			break
		}
		if s != nil && s.Mail != nil {
			err = s.Mail(ctx, opts, to, m)
		} else {
			err = sendMail(ctx, opts, to, m)
		}
	case Telegram:
		err = s.sendTelegram(ctx, opts, t.To, m)
	case Ntfy:
		err = s.sendNtfy(ctx, opts, t, m)
	default:
		err = fmt.Errorf("unknown channel %q", t.Channel)
	}
	return clean(err, opts)
}

// clean removes the secrets from an error (an URL with the bot token, a
// server that echoes what it got).
func clean(err error, opts map[string]string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, k := range SecretKeys {
		if v := strings.TrimSpace(opts[k]); len(v) >= 4 {
			msg = strings.ReplaceAll(msg, v, "[hidden]")
		}
	}
	return errors.New(msg)
}

// Status is the value of StatusKey for the outcome err at now.
func Status(now time.Time, err error) string {
	if err == nil {
		return strconv.FormatInt(now.Unix(), 10) + "|ok"
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 200 { // the column has 255 characters
		msg = strings.ToValidUTF8(msg[:200], "") + "…"
	}
	return strconv.FormatInt(now.Unix(), 10) + "|error|" + msg
}

// do sends req and reads a JSON answer of at most 64 KiB into v. The error
// never holds the URL (the Telegram token is part of it).
func (s *Sender) do(c *http.Client, req *http.Request, v any) (int, error) {
	res, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return res.StatusCode, err
	}
	_ = json.Unmarshal(body, v) // a non-JSON answer: the status says enough
	return res.StatusCode, nil
}

// ---------------------------------------------------------------- checks
// The web admin checks the same before it stores a value; the collector
// checks again, as hlstats_Options can be written by hand.

var (
	chatID    = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{3,31})$`)
	botToken  = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)
	ntfyTopic = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	hostName  = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$|^\[[0-9A-Fa-f:.]{2,45}\]$`)
)

// Recipients splits gamewe_notify_email_to into addresses (comma,
// semicolon or space separated); an invalid one is an error.
func Recipients(v string) ([]string, error) {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		a, err := address(p)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no recipient")
	}
	if len(out) > MaxRecipients {
		return nil, fmt.Errorf("more than %d recipients", MaxRecipients)
	}
	return out, nil
}

// address is a bare e-mail address (no name, no line break).
func address(v string) (string, error) {
	a, err := mail.ParseAddress(v)
	if err != nil || a.Name != "" || a.Address != v || strings.ContainsAny(v, "\r\n<>") {
		return "", fmt.Errorf("%q is no e-mail address", v)
	}
	return a.Address, nil
}

// ntfyBase is the checked server address (https, no query, no user).
func ntfyBase(v string) (string, error) {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		v = "https://ntfy.sh"
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the ntfy server must be an https address without user, query or fragment")
	}
	return v, nil
}

// ---------------------------------------------------------------- Telegram

func (s *Sender) sendTelegram(ctx context.Context, opts map[string]string, chat string, m Message) error {
	token := strings.TrimSpace(opts["gamewe_notify_telegram_token"])
	chat = strings.TrimSpace(chat)
	if !botToken.MatchString(token) {
		return errors.New("telegram: no valid bot token")
	}
	if !chatID.MatchString(chat) {
		return errors.New("telegram: no valid chat (@channelname or numeric id)")
	}
	base := "https://api.telegram.org"
	if s != nil && s.TelegramAPI != "" {
		base = s.TelegramAPI
	}
	// Plain text (no parse_mode): names of players need no escaping.
	text := m.Title + "\n\n" + m.Body
	if r := []rune(text); len(r) > 4000 { // Telegram takes 4096 characters
		text = string(r[:4000]) + "…"
	}
	form := url.Values{"chat_id": {chat}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("telegram: bad request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var res struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	status, err := s.do(s.client(), req, &res)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	if !res.OK {
		if res.Description == "" {
			res.Description = "HTTP " + strconv.Itoa(status)
		}
		return fmt.Errorf("telegram: %s", res.Description)
	}
	return nil
}

// ---------------------------------------------------------------- ntfy

func (s *Sender) sendNtfy(ctx context.Context, opts map[string]string, t Target, m Message) error {
	base, err := ntfyBase(opts["gamewe_notify_ntfy_url"])
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	topic := strings.TrimSpace(t.To)
	ownServer := false
	if i := strings.LastIndex(topic, "/"); i >= 0 && t.User != "" {
		// An admin's own server (https://host/path/topic): only for their own
		// address, as the server fields of the settings need a higher right.
		if base, err = ntfyBase(topic[:i]); err != nil || base != topic[:i] || len(topic) > 200 {
			return errors.New("ntfy: the own server must be https://host/topic")
		}
		topic, ownServer = topic[i+1:], true
	}
	if !ntfyTopic.MatchString(topic) {
		return errors.New("ntfy: no valid topic (letters, digits, _ and -)")
	}
	// JSON publishing (POST to the server's root): UTF-8 titles need no
	// header encoding.
	body, _ := json.Marshal(map[string]any{"topic": topic, "title": m.Title, "message": m.Body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/", strings.NewReader(string(body)))
	if err != nil {
		return errors.New("ntfy: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.client()
	if ownServer {
		// Never the site's token to another server; only public addresses,
		// no redirects (an admin must not reach the collector's network).
		if s == nil || s.HTTP == nil {
			client = publicClient
		}
	} else if tk := strings.TrimSpace(opts["gamewe_notify_ntfy_token"]); tk != "" {
		req.Header.Set("Authorization", "Bearer "+tk)
	}
	var res struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	status, err := s.do(client, req, &res)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	if status/100 != 2 {
		if res.Error == "" {
			res.Error = "HTTP " + strconv.Itoa(status)
		}
		return fmt.Errorf("ntfy: %s", res.Error)
	}
	return nil
}
