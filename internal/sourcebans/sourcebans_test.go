// SPDX-License-Identifier: GPL-3.0-or-later

package sourcebans

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// SB++ 1.8 default theme as a live installation serves its search result
// (2026-10), with a fictional player; the "Advanced Search" box is a
// tr.collapse without details.
const detailPage = `
<table class="table table_box"><tr class="collapse">
  <td class="text:center"> <span class="text:bold">Advanced Search</span> (Click) </td></tr>
<tr class="table_hide"><td colspan="1"><div class="collapse_content">
  <label for="steam_"> Steam ID </label><input type="text" id="steamid" value="" /></div></td></tr></table>
<table class="table table_box">
<tr class="collapse" >
  <td class="text:center"><img src="images/games/web.png" alt="MOD" /></td>
  <td>26.10.2026, 17:48 </td><td class=""> Test &amp; Co </td>
  <td class="listtable_1_permanent">Permanent </td></tr>
<tr class="table_hide"><td colspan="8"><div class="collapse_content"><div class="padding">
  <ul class="ban_action responsive_show:desktop"><li><a href='index.php?p=login'>Admin ? Sign In</a></li></ul>
  <ul class="ban_list_detal">
    <ul class="ban_action responsive_hide:desktop lists-mobile"> </ul>
    <li> <span><i class="fas fa-user"></i> Player</span> <span>Test &amp; Co</span> </li>
    <li> <span><i class="fab fa-steam-symbol"></i> Steam ID</span> <span>STEAM_0:1:12345</span> </li>
    <li> <span><i class="fab fa-steam-symbol"></i> Steam Community</span> <span> <a href="http://steamcommunity.com/profiles/76561197960290419">76561197960290419</a> </span> </li>
    <li> <span><i class="fas fa-play"></i> Invoked on</span> <span>26.10.2026, 17:48 </span> </li>
    <li> <span><i class="fas fa-hourglass-half"></i> Ban length</span> <span>Permanent </span> </li>
    <li> <span><i class="fas fa-question"></i> Reason</span> <span>Inappropriate Language</span> </li>
    <li> <span><i class="fas fa-ban"></i> Blocked (1)</span> <i><span title="Server: 192.0.2.10:27015">Someone</span></i> </li>
  </ul>
  <ul class="ban_list_comments margin-left"><div class="padding"> None </div></ul>
</div></div></td></tr>
<tr class="collapse" onclick="xajax_ServerHostPlayers(2, 'id', 'host_5778');" >
  <td>03.05.2026, 18:21 </td><td class=""> Voted Out </td><td class="listtable_1_unbanned">1 hr (Expired)</td></tr>
<tr class="table_hide"><td colspan="8"><div class="collapse_content">
  <ul class="ban_list_detal"><ul class="ban_action lists-mobile"> </ul>
    <li> <span><i class="fas fa-user"></i> Player</span> <span>Voted Out</span> </li>
    <li> <span><i class="fab fa-steam-symbol"></i> Steam ID</span> <span>STEAM_0:0:67890</span> </li>
    <li> <span><i class="fas fa-play"></i> Invoked on</span> <span>03.05.2026, 18:21 </span> </li>
    <li> <span><i class="fas fa-hourglass-half"></i> Ban length</span> <span>1 hr (Expired)</span> </li>
    <li> <span><i class="fas fa-question"></i> Reason</span> <span>Voteban</span> </li>
  </ul></div></td></tr></table>`

func TestParseDetail(t *testing.T) {
	bans := Parse(detailPage)
	want := []Ban{
		{Steam: "1:12345", Name: "Test & Co", Reason: "Inappropriate Language", Length: "Permanent", Date: "26.10.2026, 17:48", State: Permanent},
		{Steam: "0:67890", Name: "Voted Out", Reason: "Voteban", Length: "1 hr", Date: "03.05.2026, 18:21", State: Expired},
	}
	if len(bans) != len(want) {
		t.Fatalf("%d bans: %+v", len(bans), bans)
	}
	for i := range want {
		if bans[i] != want[i] {
			t.Errorf("ban %d: %+v, want %+v", i, bans[i], want[i])
		}
	}
	running := strings.Replace(detailPage, `listtable_1_permanent">Permanent`, `listtable_1_banned">2 wk`, 1)
	if s := Parse(running)[0].State; s != Active || !s.Running() {
		t.Errorf("running ban: %s", s)
	}
	lifted := strings.Replace(detailPage, `1 hr (Expired)</td>`, `1 hr (Unbanned)</td>`, 1)
	lifted = strings.Replace(lifted, `<span>1 hr (Expired)</span>`, `<span>1 hr (Unbanned)</span>`, 1)
	if s := Parse(lifted)[1].State; s != Unbanned || s.Running() {
		t.Errorf("lifted ban: %s", s)
	}
	profileOnly := strings.Replace(detailPage, "<span>STEAM_0:1:12345</span>", "<span></span>", 1)
	if s := Parse(profileOnly)[0].Steam; s != "76561197960290419" {
		t.Errorf("profile only: %q", s)
	}
}

func TestParseClassicAndNew(t *testing.T) {
	classic := `<table><tr class="opener tbl_out"><td>x</td><td class="listtable_1_permanent">Permanent</td></tr>
		<tr><td><table><tr><td> Player </td><td>Old  Name</td></tr><tr><td> Steam ID </td><td>STEAM_0:0:42</td></tr>
		<tr><td> Invoked on </td><td>01-02-20 10:00</td></tr><tr><td> Banlength </td><td>Permanent</td></tr>
		<tr><td> Reason </td><td>Aimbot</td></tr></table></td></tr></table>`
	if b := Parse(classic); len(b) != 1 || b[0] != (Ban{Steam: "0:42", Name: "Old Name", Reason: "Aimbot", Length: "Permanent", Date: "01-02-20 10:00", State: Permanent}) {
		t.Errorf("classic: %+v", b)
	}
	sb26 := `<tbody><tr class="ban-row" data-state="expired"><td><a class="ban-player-cell__name" href="#">New</a></td>
		<td>STEAM_0:1:7</td><td class="text-muted truncate" title="Wallhack">Wallhack</td><td class="col-length">1 d</td>
		<td><time datetime="2026-10-01T12:00:00Z">1 Oct</time></td></tr></tbody>`
	if b := Parse(sb26); len(b) != 1 || b[0] != (Ban{Steam: "1:7", Name: "New", Reason: "Wallhack", Length: "1 d", Date: "2026-10-01T12:00:00Z", State: Expired}) {
		t.Errorf("SB++ 2026: %+v", b)
	}
}

func TestParseHostile(t *testing.T) {
	evil := strings.Replace(detailPage, "<span>Inappropriate Language</span>", "<span><script>alert(1)</script>"+strings.Repeat("x", 1000)+"</span>", 1)
	r := Parse(evil)[0].Reason
	if strings.ContainsAny(r, "<>") || len([]rune(r)) > 300 {
		t.Errorf("reason not plain or too long: %d %q", len(r), r[:40])
	}
	if b := Parse(strings.Repeat(`<tr class="collapse"`, 10000)); len(b) != 0 {
		t.Errorf("empty rows: %d bans", len(b))
	}
}

func TestMatches(t *testing.T) {
	host := Host(Base("https://bans.example.org/index.php"))
	if host != "bans.example.org" {
		t.Fatalf("host %q", host)
	}
	for _, r := range []string{
		"You have been banned from this server. Access bans.example.org for more info.",
		"Sie wurden von diesem Server gebannt, weitere Informationen auf bans.example.org.",
		"Вы были забанены на этом сервере, для получения информации посетите: BANS.EXAMPLE.ORG.",
		"你已被此服务器封禁, 详情请参阅: https://bans.example.org/.",
	} {
		if !Matches(r, host, nil) {
			t.Errorf("not matched: %q", r)
		}
	}
	if Matches("Disconnect by user.", host, nil) || Matches("Kicked by Console", host, []string{" "}) {
		t.Error("ordinary disconnect matched")
	}
	if !Matches("Du bist gebannt (siehe Forum)", "", []string{"gebannt"}) {
		t.Error("phrase not matched")
	}
}

func TestBase(t *testing.T) {
	for in, want := range map[string]string{
		"https://bans.example.org/":           "https://bans.example.org/",
		"bans.example.org":                    "https://bans.example.org/",
		"http://example.org/sb/index.php?x=1": "http://example.org/sb/",
		"ftp://example.org/":                  "",
		"https://user:pw@example.org/":        "",
		"":                                    "",
	} {
		if got := Base(in); got != want {
			t.Errorf("Base(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccount(t *testing.T) {
	for in, want := range map[string]string{
		"STEAM_0:1:13": "1:13", "STEAM_1:0:5": "0:5", "1:13": "1:13", "[U:1:27]": "1:13", "U:1:27": "1:13",
		"76561197960265755": "1:13",
	} {
		if got, ok := Account(in); !ok || got != want {
			t.Errorf("Account(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"BOT", "STEAM_ID_PENDING", "0:x", "76561197960265728", "[U:1:0]", "2:5"} {
		if got, ok := Account(in); ok {
			t.Errorf("Account(%q) = %q, want none", in, got)
		}
	}
}

func TestLengthMinutes(t *testing.T) {
	for in, want := range map[string]int{
		"Permanent": 0, "1 hr": 60, "2 wk": 20160, "30 d": 43200, "1 wk, 2 d": 12960, "45 min": 45, "1 mo": 43200,
		"1 hr (Expired)": 60, "2 Days": 2880,
	} {
		if got, ok := LengthMinutes(in); !ok || got != want {
			t.Errorf("LengthMinutes(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "soon", "0 min", "1 fortnight", "99999999 yr"} {
		if got, ok := LengthMinutes(in); ok {
			t.Errorf("LengthMinutes(%q) = %d, want not understood", in, got)
		}
	}
}

func TestParseDate(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	if d, ok := ParseDate("26.10.2026, 17:48", berlin); !ok || d.UTC().Format(time.RFC3339) != "2026-10-26T16:48:00Z" {
		t.Errorf("SB++ 1.8 date: %v %v", d, ok)
	}
	if d, ok := ParseDate("2026-10-01T12:00:00Z", berlin); !ok || d.Unix() != 1790856000 {
		t.Errorf("ISO date: %v %v", d, ok)
	}
	if _, ok := ParseDate("yesterday", berlin); ok {
		t.Error("nonsense parsed")
	}
}

func TestPrivate(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.16.5.4", "100.64.0.1", "169.254.1.1", "::1", "fd00::1", "fe80::1", "::ffff:10.0.0.1", "0.0.0.0", "192.0.2.10", "2001:db8::1"} {
		if !Private(netip.MustParseAddr(s)) {
			t.Errorf("%s counted as public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "2a00:1450::1"} {
		if Private(netip.MustParseAddr(s)) {
			t.Errorf("%s counted as private", s)
		}
	}
}

func TestLookup(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		switch r.URL.Query().Get("advSearch") {
		case "STEAM_0:1:12345":
			w.Write([]byte(detailPage))
		case "STEAM_0:0:1":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "STEAM_0:0:2":
			w.Write([]byte(`<div class="card">STEAM_0:0:2 banned</div>`))
		default:
			w.Write([]byte(`<p>No bans</p>`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c := &Client{AllowPrivate: true}
	base := Base(srv.URL)
	bans, err := c.Lookup(ctx, base, "STEAM_1:1:12345")
	if err != nil || len(bans) != 1 || bans[0].Reason != "Inappropriate Language" {
		t.Fatalf("lookup: %+v %v", bans, err)
	}
	if !strings.Contains(asked, "advSearch=STEAM_0%3A1%3A12345") || !strings.Contains(asked, "advType=steamid") {
		t.Errorf("query %q", asked)
	}
	if bans, err := c.Lookup(ctx, base, "0:9"); err != nil || len(bans) != 0 {
		t.Errorf("no ban: %+v %v", bans, err)
	}
	if _, err := c.Lookup(ctx, base, "0:1"); err == nil {
		t.Error("redirect followed")
	}
	if _, err := c.Lookup(ctx, base, "0:2"); !errors.Is(err, ErrUnknownTemplate) {
		t.Errorf("unknown template: %v", err)
	}
	if _, err := c.Lookup(ctx, base, "BOT"); err == nil {
		t.Error("bot looked up")
	}
	if _, err := (&Client{}).Lookup(ctx, base, "1:12345"); !errors.Is(err, ErrPrivate) {
		t.Errorf("private address: %v", err)
	}
}
