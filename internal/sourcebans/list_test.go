// SPDX-License-Identifier: GPL-3.0-or-later

package sourcebans

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// classicRow is one ban of the classic template with Steam ID n.
func classicRow(n int, state string) string {
	return fmt.Sprintf(`<tr class="opener tbl_out"><td class="listtable_1_%s">Permanent</td></tr>
		<tr><td><table><tr><td> Player </td><td>P%d</td></tr><tr><td> Steam ID </td><td>STEAM_0:0:%d</td></tr>
		<tr><td> Invoked on </td><td>01-02-20 10:00</td></tr><tr><td> Banlength </td><td>Permanent</td></tr>
		<tr><td> Banned by Admin </td><td>Admin %d</td></tr>
		<tr><td> Reason </td><td>R%d</td></tr></table></td></tr>`, state, n, n, n, n)
}

// The list is read page by page until a page brings nothing new (past the
// last page SourceBans repeats it); every state is returned.
func TestList(t *testing.T) {
	var pages []string
	fail := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("p") != "banlist" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		pages = append(pages, q.Get("page"))
		n, _ := strconv.Atoi(q.Get("page"))
		if n == fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if n > 3 {
			n = 3 // the last page again
		}
		var b strings.Builder
		b.WriteString("<table>")
		for i := 0; i < 2; i++ {
			st := "permanent"
			if i == 1 {
				st = "unbanned"
			}
			b.WriteString(classicRow(n*10+i, st))
		}
		b.WriteString("</table>")
		w.Write([]byte(b.String()))
	}))
	defer srv.Close()
	c := &Client{AllowPrivate: true}
	ctx := context.Background()
	bans, err := c.List(ctx, Base(srv.URL), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(bans) != 6 || strings.Join(pages, ",") != "1,2,3,4" {
		t.Fatalf("%d bans from pages %v", len(bans), pages)
	}
	if b := bans[0]; b.Steam != "0:10" || b.State != Permanent || b.Admin != "Admin 10" || b.Reason != "R10" {
		t.Errorf("first ban %+v", b)
	}
	if bans[1].State != Unbanned {
		t.Errorf("state %s", bans[1].State)
	}
	// A failing page: nothing, so no ban is taken for lifted.
	pages, fail = nil, 2
	if bans, err := c.List(ctx, Base(srv.URL), 0); err == nil || bans != nil {
		t.Errorf("failed page: %d bans, %v", len(bans), err)
	}
	if _, err := (&Client{}).List(ctx, Base(srv.URL), 0); !errors.Is(err, ErrPrivate) {
		t.Errorf("private address: %v", err)
	}
}

func TestListEmptyAndUnknown(t *testing.T) {
	body := `<p>No bans</p>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	c := &Client{AllowPrivate: true}
	if bans, err := c.List(context.Background(), Base(srv.URL), 0); err != nil || len(bans) != 0 {
		t.Errorf("empty list: %+v %v", bans, err)
	}
	body = `<div class="card">STEAM_0:0:2 banned</div>`
	if _, err := c.List(context.Background(), Base(srv.URL), 0); !errors.Is(err, ErrUnknownTemplate) {
		t.Errorf("unknown template: %v", err)
	}
}

func TestParseDetailAdmin(t *testing.T) {
	page := strings.Replace(detailPage, `<li> <span><i class="fas fa-question"></i> Reason</span>`,
		`<li> <span><i class="fas fa-user-shield"></i> Admin</span> <span>Ann &amp; Bob</span> </li><li> <span><i class="fas fa-question"></i> Reason</span>`, 1)
	if b := Parse(page); len(b) != 2 || b[0].Admin != "Ann & Bob" || b[1].Admin != "" {
		t.Errorf("admin: %+v", b)
	}
}
