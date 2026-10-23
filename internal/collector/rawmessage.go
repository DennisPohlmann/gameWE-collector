// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gamewe-collector/internal/stats"
)

// Types of <prefix>_raw_message, as in the plugin's gamewe.inc / gameme.inc.
// The 1xx and 1001 answers go to the callback of a query (the plugin's
// natives log `"P" requested "<type>" (value "<query_id>")`); 1, 4, 5 and 6
// go to the plugin's forwards for chat commands (gamewe_raw_messages).
const (
	rawRank              = 1
	rawSession           = 4
	rawTop10             = 5
	rawNext              = 6
	rawCallbackPlayer    = 101
	rawCallbackTop10     = 102
	rawCallbackNext      = 103
	rawCallbackSpectator = 1001
)

// nextCount is how many players a "next" answer lists.
const nextCount = 5

// maxCommand keeps raw messages below the engine's 512-byte command limit.
const maxCommand = 500

// Query answers a query of another plugin; st is nil for queries without a
// player ("Server" requested ...). Unknown types are ignored.
func (c Commands) Query(ctx context.Context, srv *serverState, st *playerState, typ string, queryID int, now time.Time) error {
	switch typ {
	case "playerinfo":
		if st != nil {
			return c.rawPlayer(ctx, srv, st, rawCallbackPlayer, queryID, now)
		}
	case "top10":
		// The plugin only accepts the callback answer with userid -1.
		return c.rawTop10(ctx, srv, rawCallbackTop10, queryID, -1)
	case "next":
		if st != nil {
			return c.rawNext(ctx, srv, st, rawCallbackNext, queryID)
		}
	case "spectatorinfo":
		if st != nil {
			return c.rawSpectator(ctx, srv, st, queryID)
		}
	default:
		c.log.Debug("unknown plugin query", "type", typ)
	}
	return nil
}

func (c Commands) raw(srv *serverState, typ, queryID, userID int, values []string) {
	cmd := fmt.Sprintf("%s_raw_message %d %d %d", c.set.Get().CommandPrefix, typ, queryID, userID)
	if len(values) > 0 {
		cmd += " " + strings.Join(values, " ")
	}
	c.send(srv, cmd)
}

func f2(x float64) string { return strconv.FormatFloat(x, 'f', 2, 64) }

func ratio(a, b int) float64 {
	if b == 0 {
		return float64(a)
	}
	return float64(a) / float64(b)
}

func hpk(headshots, kills int) float64 {
	if kills == 0 {
		return 0
	}
	return float64(headshots) / float64(kills)
}

func accuracy(hits, shots int) float64 {
	if shots == 0 {
		return 0
	}
	return 100 * float64(hits) / float64(shots)
}

// quoted makes s one quoted console argument of at most n characters.
func quoted(s string, n int) string {
	s = Sanitize(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return `"` + s + `"`
}

// favWeapon is the weapon with the most kills of the visit, "-" if none.
func favWeapon(st *playerState) string {
	best, n := "-", 0
	for w, k := range st.weaponKills {
		if k > n || (k == n && w < best) {
			best, n = w, k
		}
	}
	return best
}

// playerValues are the 45 values of a player answer after "<type>
// <query_id> <userid>", in the order the plugin reads them: the totals, the
// visit (session) and the "global" values (gameWE has no network-wide
// ranking, so they are the game's), then the country code.
func playerValues(pl stats.Player, rank, total int, st *playerState, now time.Time) []string {
	i := strconv.Itoa
	v := []string{
		i(rank), i(total), i(pl.Skill), i(pl.Kills), i(pl.Deaths), f2(ratio(pl.Kills, pl.Deaths)),
		i(pl.Suicides), i(pl.Headshots), f2(hpk(pl.Headshots, pl.Kills)), f2(accuracy(pl.Hits, pl.Shots)),
		i(pl.ConnectionTime), i(pl.KillAssists), i(pl.KillsAssisted), i(pl.PointsHealed), i(pl.FlagsCaptured),
		i(pl.CustomWins), i(pl.KillStreak), i(pl.DeathStreak),
	}
	posChange := 0
	if st.startRank > 0 {
		posChange = st.startRank - rank
	}
	v = append(v,
		i(posChange), i(st.skillDelta), i(st.kills), i(st.deaths), f2(ratio(st.kills, st.deaths)),
		i(st.suicides), i(st.headshots), f2(hpk(st.headshots, st.kills)), f2(accuracy(st.hits, st.shots)),
		i(int(now.Sub(st.since).Seconds())), "0", "0", "0", "0", "0", i(st.bestKill), i(st.bestDeath),
		quoted(favWeapon(st), 32),
	)
	v = append(v,
		i(rank), i(total), i(pl.Skill), i(pl.Kills), i(pl.Deaths), f2(ratio(pl.Kills, pl.Deaths)),
		i(pl.Headshots), f2(hpk(pl.Headshots, pl.Kills)),
		quoted(pl.Country, 2),
	)
	return v
}

// rawPlayer sends the player answer (types 101 and 1, 4).
func (c Commands) rawPlayer(ctx context.Context, srv *serverState, st *playerState, typ, queryID int, now time.Time) error {
	pl, err := c.store.PlayerStats(ctx, st.live.ID)
	if errors.Is(err, stats.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	rank, total, err := c.store.Rank(ctx, srv.info.Game, pl.Skill)
	if err != nil {
		return err
	}
	c.raw(srv, typ, queryID, st.userID, playerValues(pl, rank, total, st, now))
	return nil
}

// fitEntries formats list entries with names of at most nameLen characters and
// shortens the names until the command fits.
func fitEntries(head string, n int, entry func(i, nameLen int) string) []string {
	for nameLen := 32; ; nameLen -= 4 {
		var out []string
		size := len(head)
		for i := 0; i < n; i++ {
			e := entry(i, nameLen)
			out = append(out, e)
			size += 1 + len(e)
		}
		if size <= maxCommand || nameLen <= 4 {
			return out
		}
	}
}

// rawTop10 sends the top 10 (types 102 and 5): skill, name, kpd, hpk per
// player, best first; no values if nobody is ranked.
func (c Commands) rawTop10(ctx context.Context, srv *serverState, typ, queryID, userID int) error {
	top, err := c.store.Top(ctx, srv.info.Game, 10)
	if err != nil {
		return err
	}
	vals := fitEntries("x_raw_message 102 65535 -1", len(top), func(i, nameLen int) string {
		t := top[i]
		return fmt.Sprintf("%d %s %s %s", t.Skill, quoted(t.Name, nameLen), f2(ratio(t.Kills, t.Deaths)), f2(hpk(t.Headshots, t.Kills)))
	})
	c.raw(srv, typ, queryID, userID, vals)
	return nil
}

// rawNext sends the players ranked directly above the player (types 103
// and 6): rank, skill, name, kpd, hpk per player, best first; no values if
// the player is number one.
func (c Commands) rawNext(ctx context.Context, srv *serverState, st *playerState, typ, queryID int) error {
	pl, err := c.store.PlayerStats(ctx, st.live.ID)
	if errors.Is(err, stats.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	above, err := c.store.Above(ctx, srv.info.Game, pl.Skill, nextCount)
	if err != nil {
		return err
	}
	slices.Reverse(above)
	ranks := make([]int, len(above))
	for i, a := range above {
		if ranks[i], _, err = c.store.Rank(ctx, srv.info.Game, a.Skill); err != nil {
			return err
		}
	}
	vals := fitEntries("x_raw_message 103 65535 65535", len(above), func(i, nameLen int) string {
		a := above[i]
		return fmt.Sprintf("%d %d %s %s %s", ranks[i], a.Skill, quoted(a.Name, nameLen), f2(ratio(a.Kills, a.Deaths)), f2(hpk(a.Headshots, a.Kills)))
	})
	c.raw(srv, typ, queryID, st.userID, vals)
	return nil
}

// rawSpectator sends the text shown to players who spectate st (type 1001)
// to every player on the server (the plugin keeps it per spectator).
func (c Commands) rawSpectator(ctx context.Context, srv *serverState, st *playerState, queryID int) error {
	pl, err := c.store.PlayerStats(ctx, st.live.ID)
	if errors.Is(err, stats.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	rank, total, err := c.store.Rank(ctx, srv.info.Game, pl.Skill)
	if err != nil {
		return err
	}
	var callers []int
	for _, o := range srv.players {
		callers = append(callers, o.userID)
	}
	sort.Ints(callers)
	ids := make([]string, len(callers))
	for i, u := range callers {
		ids[i] = strconv.Itoa(u)
	}
	text := menuText([]string{
		st.live.Name,
		fmt.Sprintf("Rank %d of %d, %d points", rank, total, pl.Skill),
		fmt.Sprintf("Kills %d:%d (%s), headshots %d", pl.Kills, pl.Deaths, kd(pl.Kills, pl.Deaths), pl.Headshots),
	})
	c.send(srv, fmt.Sprintf(`%s_raw_message %d %d %s %d "%s"`, c.set.Get().CommandPrefix, rawCallbackSpectator, queryID,
		strings.Join(ids, ","), st.userID, text))
	return nil
}
