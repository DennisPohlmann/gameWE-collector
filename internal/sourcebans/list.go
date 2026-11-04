// SPDX-License-Identifier: GPL-3.0-or-later

package sourcebans

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// MaxPages is the most pages of the ban list List reads (SourceBans shows
// 30 bans per page by default, so about 30,000 bans).
const MaxPages = 1000

// ErrTooManyPages: the list has more than MaxPages pages; what was read is
// not the whole list.
var ErrTooManyPages = errors.New("the ban list has more pages than are read")

// ErrTooManyBans: the list shows more than MaxBans bans; nothing is changed.
var ErrTooManyBans = errors.New("the ban list has more bans than are read")

// MaxBans bounds the memory of one read of a list.
const MaxBans = 200_000

// List reads the whole public ban list at base (a Base address) page by
// page (index.php?p=banlist&page=N), with pause between two pages, and
// returns every ban it shows, in every state. Past the last page SourceBans
// repeats it or shows nothing, so the reading stops at the first page
// without a new ban, on a page that is recognised as the ban list. An error means the list was not read completely (a
// page failed, more than MaxPages pages, or the first page shows Steam IDs
// in a template this code does not know); then nothing is returned.
func (c *Client) List(ctx context.Context, base string, pause time.Duration) ([]Ban, error) {
	if base == "" {
		return nil, errors.New("no SourceBans address")
	}
	var out []Ban
	seen := map[string]bool{}
	for page := 1; ; page++ {
		if page > MaxPages {
			return nil, ErrTooManyPages
		}
		if page > 1 && pause > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pause):
			}
		}
		html, err := c.fetch(ctx, base, map[string][]string{"p": {"banlist"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		found := Parse(html)
		if page == 1 && len(found) == 0 && HasSteamIDs(html) {
			return nil, ErrUnknownTemplate
		}
		// An error or maintenance page with status 200 is not an empty list:
		// taken as one, it would end every running ban.
		if len(found) == 0 && !isListPage(html) {
			return nil, ErrNotBanList
		}
		added := 0
		for _, b := range found {
			k := b.Steam + "|" + b.Date + "|" + b.Reason + "|" + b.Length
			if seen[k] {
				continue
			}
			seen[k] = true
			if len(out) >= MaxBans {
				return nil, ErrTooManyBans
			}
			out = append(out, b)
			added++
		}
		if added == 0 {
			return out, nil
		}
	}
}
