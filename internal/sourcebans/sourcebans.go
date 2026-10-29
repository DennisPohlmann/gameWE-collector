// SPDX-License-Identifier: GPL-3.0-or-later

// Package sourcebans reads the bans of one player from the public ban list
// of a SourceBans / SourceBans++ installation (its search,
// index.php?p=banlist&advSearch=<Steam ID>&advType=steamid), since
// SourceBans has no public API. The address is the web admin's setting
// hlstats_Options gamewe_sourcebans_url. Only public http(s) addresses are
// contacted (checked at connect time), without redirects, with a time limit
// and a size limit. The page templates understood are the classic one
// (SourceBans 1.4 to SB++ 1.7), the SB++ 1.8 default theme and the SB++
// 2026 one, like the web's src/lib/sourcebans-parse.ts.
package sourcebans

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// State of a ban as the list shows it.
type State string

const (
	Permanent State = "permanent"
	Active    State = "active"
	Expired   State = "expired"
	Unbanned  State = "unbanned"
)

// Running reports whether the ban still keeps the player out.
func (s State) Running() bool { return s == Permanent || s == Active }

// Ban is one ban of the list. Steam is the account part of the Steam ID
// ("1:123" of STEAM_0:1:123) or a SteamID64 when the page shows only that.
type Ban struct {
	Steam  string
	Name   string
	Reason string
	Length string // as shown ("Permanent", "2 wk", "1 hr")
	Date   string // as shown ("26.10.2026, 17:48", or ISO 8601)
	Admin  string // the banning admin as shown ("" when the list hides it)
	State  State
}

// MaxPage is the largest page read.
const MaxPage = 3 << 20

// Timeout is the longest a lookup may take.
const Timeout = 8 * time.Second

// Base normalizes the setting: http(s) only, no user or password, no query,
// ending with "/" ("" when unusable), as the web's sourcebansBase.
func Base(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		if p := strings.ToLower(s[:i]); p != "http" && p != "https" {
			return ""
		}
	} else {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" {
		return ""
	}
	u.RawQuery, u.Fragment = "", ""
	p := strings.TrimSuffix(u.Path, "index.php")
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	u.Path = p
	return u.String()
}

// Host returns the host name of a Base address ("" for none).
func Host(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Client looks bans up on the ban list.
type Client struct {
	// AllowPrivate allows addresses of the LAN and localhost (SourceBans on
	// the same network); off, the setting cannot reach internal services.
	AllowPrivate bool
	// HTTP replaces the default client (tests).
	HTTP *http.Client
}

// ErrPrivate is returned for an address that is not public.
var ErrPrivate = errors.New("not a public address")

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	d := &net.Dialer{Timeout: Timeout, Control: func(_, address string, _ syscall.RawConn) error {
		if c.AllowPrivate {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if Private(ap.Addr()) {
			return fmt.Errorf("%s: %w", ap.Addr(), ErrPrivate)
		}
		return nil
	}}
	tr := &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: Timeout, ResponseHeaderTimeout: Timeout, MaxIdleConns: 2}
	return &http.Client{Transport: tr, Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// Lookup returns the bans the list shows for the Steam ID uniqueID (the
// account part "X:Y", any STEAM_N:X:Y, or a SteamID64) at base (a Base
// address). The search matches the Steam ID exactly; bans of other Steam
// IDs on the page are left out.
func (c *Client) Lookup(ctx context.Context, base, uniqueID string) ([]Ban, error) {
	acc, ok := Account(uniqueID)
	if !ok {
		return nil, fmt.Errorf("%q is not a Steam ID", uniqueID)
	}
	if base == "" {
		return nil, errors.New("no SourceBans address")
	}
	q := url.Values{"p": {"banlist"}, "advSearch": {"STEAM_0:" + acc}, "advType": {"steamid"}}
	html, err := c.fetch(ctx, base, q)
	if err != nil {
		return nil, err
	}
	var out []Ban
	for _, b := range Parse(html) {
		if a, ok := Account(b.Steam); ok && a == acc {
			out = append(out, b)
		}
	}
	if len(out) == 0 && strings.Contains(html, "STEAM_") && len(Parse(html)) == 0 && HasSteamIDs(html) {
		return nil, ErrUnknownTemplate
	}
	return out, nil
}

// fetch reads base+"index.php?"+q: no redirects, at most MaxPage bytes,
// within Timeout.
func (c *Client) fetch(ctx context.Context, base string, q url.Values) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"index.php?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "gameWE collector")
	req.Header.Set("Accept", "text/html")
	res, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return "", fmt.Errorf("redirect from %s; set the final address", base)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", res.StatusCode, base)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxPage+1))
	if err != nil {
		return "", err
	}
	if len(body) > MaxPage {
		return "", fmt.Errorf("page larger than %d bytes", MaxPage)
	}
	return string(body), nil
}

// ErrUnknownTemplate: the page shows Steam IDs, but no ban could be read
// from it (a template this code does not know).
var ErrUnknownTemplate = errors.New("the ban list shows Steam IDs, but no ban could be read (unknown template)")

// Account returns the account part "X:Y" of a Steam ID (STEAM_N:X:Y, X:Y,
// [U:1:N], U:1:N or a SteamID64).
func Account(id string) (string, bool) {
	s := strings.TrimSpace(id)
	if m := reAccount.FindStringSubmatch(s); m != nil {
		return m[1] + ":" + m[2], true
	}
	if m := reU.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil || n == 0 {
			return "", false
		}
		return fmt.Sprintf("%d:%d", n%2, n/2), true
	}
	if reID64.MatchString(s) {
		n, err := strconv.ParseUint(s, 10, 64)
		const base = 76561197960265728
		if err != nil || n <= base || n-base >= 1<<32 {
			return "", false
		}
		n -= base
		return fmt.Sprintf("%d:%d", n%2, n/2), true
	}
	return "", false
}

var (
	reAccount = regexp.MustCompile(`^(?:STEAM_[0-5]:)?([01]):(\d{1,10})$`)
	reU       = regexp.MustCompile(`^\[?U:1:(\d{1,10})\]?$`)
	reID64    = regexp.MustCompile(`^7656119\d{10}$`)
)

// Private reports addresses that are not public: loopback, private, link
// local, CGNAT, multicast, documentation and the like.
func Private(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() ||
		a.IsUnspecified() || a.IsInterfaceLocalMulticast() || !a.IsValid() {
		return true
	}
	for _, p := range privateNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

var privateNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Matches reports whether a disconnect reason is a SourceBans kick: it
// names the ban list's host (SourceBans++ puts its website into the
// message in every language: "Access {1} for more info", "weitere
// Informationen auf {1}", ...) or contains one of phrases (case
// insensitive). The lookup confirms it; a false match only costs one.
func Matches(reason, host string, phrases []string) bool {
	r := strings.ToLower(reason)
	if host != "" && strings.Contains(r, host) {
		return true
	}
	for _, p := range phrases {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" && strings.Contains(r, p) {
			return true
		}
	}
	return false
}

// LengthMinutes returns the length of a ban in minutes: 0 and true for
// permanent, false when the text is not understood. SourceBans shows lengths
// as "Permanent", "1 hr", "2 wk", "30 d", "1 mo", "1 wk, 2 d", "45 min".
func LengthMinutes(s string) (int, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimSpace(reLengthTail.ReplaceAllString(t, ""))
	if t == "" {
		return 0, false
	}
	if strings.HasPrefix(t, "perm") {
		return 0, true
	}
	total := 0
	for _, part := range strings.Split(t, ",") {
		m := reLengthPart.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			return 0, false
		}
		n, _ := strconv.Atoi(m[1])
		unit, ok := lengthUnits[m[2]]
		if !ok || n > 1<<20 {
			return 0, false
		}
		total += n * unit
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

var (
	reLengthTail = regexp.MustCompile(`\((?:unbanned|expired|deleted)\)$`)
	reLengthPart = regexp.MustCompile(`^(\d{1,7})\s*([a-z]+)$`)
	lengthUnits  = map[string]int{
		"min": 1, "mins": 1, "minute": 1, "minutes": 1, "m": 1,
		"hr": 60, "hrs": 60, "hour": 60, "hours": 60, "h": 60,
		"d": 1440, "day": 1440, "days": 1440,
		"wk": 10080, "wks": 10080, "week": 10080, "weeks": 10080, "w": 10080,
		"mo": 43200, "mos": 43200, "month": 43200, "months": 43200,
		"yr": 525600, "yrs": 525600, "year": 525600, "years": 525600, "y": 525600,
	}
)

// ParseDate reads the date of a ban in loc (the time zone the SourceBans
// installation shows, normally the collector's): ISO 8601, SB++ 1.8
// "02.01.2006, 15:04" and the other common SourceBans formats.
func ParseDate(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	for _, f := range []string{"02.01.2006, 15:04", "02.01.2006 15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "01-02-06 15:04", "02-01-2006 15:04"} {
		if t, err := time.ParseInLocation(f, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ---------------------------------------------------------------- parsing

// Parse returns the bans of a ban list page.
func Parse(html string) []Ban {
	switch {
	case strings.Contains(html, `class="ban-row`):
		return parseNew(html)
	case strings.Contains(html, `class="ban_list_detal"`):
		return parseDetail(html)
	default:
		return parseClassic(html)
	}
}

// HasSteamIDs reports whether the page shows Steam IDs at all.
func HasSteamIDs(html string) bool { return reSteam2.MatchString(html) }

var (
	reSteam2     = regexp.MustCompile(`STEAM_[0-5]:([01]):(\d{1,10})`)
	reProfile    = regexp.MustCompile(`steamcommunity\.com/profiles/(7656119\d{10})`)
	reState      = regexp.MustCompile(`data-state="([a-z]+)"`)
	reNewReason  = regexp.MustCompile(`<td class="text-muted truncate"[^>]*?(?:title="([^"]*)")?[^>]*>([\s\S]*?)</td>`)
	reNewName    = regexp.MustCompile(`ban-player-cell__name"[^>]*>([\s\S]*?)</a>`)
	reNewLength  = regexp.MustCompile(`<td class="col-length[^"]*"[^>]*>([\s\S]*?)</td>`)
	reNewDate    = regexp.MustCompile(`<time datetime="([^"]+)"`)
	reClass      = regexp.MustCompile(`class="listtable_1_(permanent|banned|unbanned)"[^>]*>([\s\S]*?)</td>`)
	reDetailEnd  = regexp.MustCompile(`class="ban_list_comments|</td>`)
	reTags       = regexp.MustCompile(`<[^>]*>`)
	reSpace      = regexp.MustCompile(`\s+`)
	reEntity     = regexp.MustCompile(`&#(\d{1,7});`)
	reLengthNote = regexp.MustCompile(`(?i)\s*\((?:Unbanned|Expired|Deleted)\)\s*$`)
	detailFields = map[string]*regexp.Regexp{}
	classicField = map[string]*regexp.Regexp{}
)

func init() {
	for _, l := range []string{"Player", "Steam ID", "Steam Community", "Invoked on", "Ban length", "Reason", "Admin"} {
		detailFields[l] = regexp.MustCompile(`<span>(?:\s*<i[^>]*></i>)?\s*` + regexp.QuoteMeta(l) + `\s*</span>\s*<span[^>]*>([\s\S]*?)</span>`)
	}
	for _, l := range []string{"Player", "Invoked on", "Banlength", "Reason", "Banned by Admin"} {
		classicField[l] = regexp.MustCompile(`>\s*` + regexp.QuoteMeta(l) + `\s*</td>\s*<td[^>]*>([\s\S]*?)</td>`)
	}
}

// SB++ 2026: one <tr class="ban-row ..." data-state="..."> per ban.
func parseNew(html string) []Ban {
	var out []Ban
	parts := strings.Split(html, `<tr class="ban-row`)
	for _, chunk := range parts[1:] {
		row, _, _ := strings.Cut(chunk, "</tbody>")
		steam := steamOf(row)
		if steam == "" {
			continue
		}
		b := Ban{Steam: steam, Name: text(sub(reNewName, row)), Length: text(sub(reNewLength, row)), Date: sub(reNewDate, row), State: Active}
		if m := reNewReason.FindStringSubmatch(row); m != nil {
			if m[1] != "" {
				b.Reason = text(m[1])
			} else {
				r, _, _ := strings.Cut(m[2], "<div")
				b.Reason = text(r)
			}
		}
		switch s := State(sub(reState, row)); s {
		case Permanent, Active, Expired, Unbanned:
			b.State = s
		}
		out = append(out, b)
	}
	return out
}

// SB++ 1.8 default theme: <tr class="collapse"> (game, country, date, name,
// length with the listtable_1_* state class), then <tr class="table_hide">
// whose <ul class="ban_list_detal"> holds <li><span><i></i> Label</span>
// <span>value</span></li> pairs. The list holds an empty <ul> of its own,
// so it ends at the comments list or the end of the cell.
func parseDetail(html string) []Ban {
	var out []Ban
	parts := strings.Split(html, `<tr class="collapse"`)
	for _, chunk := range parts[1:] {
		_, detail, ok := strings.Cut(chunk, `class="ban_list_detal"`)
		if !ok {
			continue
		}
		if loc := reDetailEnd.FindStringIndex(detail); loc != nil {
			detail = detail[:loc[0]]
		}
		field := func(l string) string { return text(sub(detailFields[l], detail)) }
		steam := steamOf(field("Steam ID"))
		if steam == "" {
			steam = steamOf(field("Steam Community"))
		}
		if steam == "" {
			continue
		}
		cls := reClass.FindStringSubmatch(chunk)
		length := field("Ban length")
		if length == "" && cls != nil {
			length = text(cls[2])
		}
		out = append(out, Ban{Steam: steam, Name: field("Player"), Reason: field("Reason"),
			Length: reLengthNote.ReplaceAllString(length, ""), Date: field("Invoked on"), Admin: field("Admin"), State: classState(cls, length)})
	}
	return out
}

// Classic template: <tr class="opener ..."> with the summary, followed by a
// sliding panel with "Steam ID", "Invoked on", "Banlength", "Reason" rows.
func parseClassic(html string) []Ban {
	var out []Ban
	parts := strings.Split(html, `<tr class="opener`)
	for _, chunk := range parts[1:] {
		steam := steamOf(chunk)
		if steam == "" {
			continue
		}
		field := func(l string) string { return text(sub(classicField[l], chunk)) }
		cls := reClass.FindStringSubmatch(chunk)
		length := field("Banlength")
		if length == "" && cls != nil {
			length = text(cls[2])
		}
		out = append(out, Ban{Steam: steam, Name: field("Player"), Reason: field("Reason"),
			Length: reLengthNote.ReplaceAllString(length, ""), Date: field("Invoked on"), Admin: field("Banned by Admin"), State: classState(cls, length)})
	}
	return out
}

// classState: the listtable_1_* class of the length cell, permanent, banned
// (running) or unbanned (lifted, or expired when the length says so).
func classState(cls []string, length string) State {
	switch {
	case cls != nil && cls[1] == "permanent":
		return Permanent
	case cls != nil && cls[1] == "unbanned":
		if strings.Contains(strings.ToLower(length), "expired") {
			return Expired
		}
		return Unbanned
	}
	return Active
}

func steamOf(s string) string {
	if m := reSteam2.FindStringSubmatch(s); m != nil {
		return m[1] + ":" + m[2]
	}
	if m := reProfile.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if t := strings.TrimSpace(s); reID64.MatchString(t) {
		return t
	}
	return ""
}

func sub(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// text turns HTML into plain text of at most 300 characters.
func text(s string) string {
	s = reTags.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#039;", "'", "&#39;", "'", "&apos;", "'").Replace(s)
	s = reEntity.ReplaceAllStringFunc(s, func(e string) string {
		n, err := strconv.Atoi(e[2 : len(e)-1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return " "
		}
		return string(rune(n))
	})
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300])
	}
	return s
}
