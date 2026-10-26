// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// playerRow returns all columns of a player's row in table (hlstats_Players
// or hlstats_PlayersInactive, constants of the tests), nil if there is none.
func playerRow(t *testing.T, db *database.DB, table string, id int64) map[string]sql.NullString {
	t.Helper()
	rows, err := db.Query(db.Q(`SELECT * FROM `+table+` WHERE playerId = ?`), id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		return nil
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	m := map[string]sql.NullString{}
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m
}

func dbPlayerID(t *testing.T, db *database.DB, uniqueID string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(db.Q(`SELECT playerId FROM hlstats_PlayerUniqueIds WHERE uniqueId = ?`), uniqueID).Scan(&id); err != nil {
		t.Fatalf("player %s: %v", uniqueID, err)
	}
	return id
}

func columns(t *testing.T, db *database.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` WHERE 1 = 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestIntegrationInactivePlayers(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	// SELECT * moves the rows both ways: the columns must match in order.
	if a, b := columns(t, db, "hlstats_Players"), columns(t, db, "hlstats_PlayersInactive"); !slices.Equal(a, b) {
		t.Fatalf("columns differ:\n%v\n%v", a, b)
	}
	addr := "198.51.100.70:27015"
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}}, quiet)
	feed := func(h *Hub, lines ...string) {
		t.Helper()
		for _, l := range lines {
			if err := h.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
	}
	feed(hub,
		`"A<2><STEAM_1:0:301><CT>" killed "B<3><STEAM_1:0:302><TERRORIST>" with "weapon1"`,
		`"Bot1<4><BOT><TERRORIST>" killed "A<2><STEAM_1:0:301><CT>" with "weapon1"`,
		`"C<5><STEAM_1:0:303><CT>" killed "B<3><STEAM_1:0:302><TERRORIST>" with "weapon1"`)
	hub.Close(ctx) // ends the visits: no live rows
	srv := int64(count(t, db, `SELECT serverId FROM hlstats_Servers`))
	a, b, bot, c := dbPlayerID(t, db, "0:301"), dbPlayerID(t, db, "0:302"), dbPlayerID(t, db, "BOT:Bot1"), dbPlayerID(t, db, "0:303")

	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Unix()
	for _, id := range []int64{a, bot, c} {
		if _, err := db.Exec(db.Q(`UPDATE hlstats_Players SET last_event = ? WHERE playerId = ?`), old, id); err != nil {
			t.Fatal(err)
		}
	}
	// C is online (a live row), B played recently.
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Livestats (player_id, server_id, unique_id, name) VALUES (?, ?, '0:303', 'C')`), c, srv); err != nil {
		t.Fatal(err)
	}
	// A leftover copy of A (a crash during an earlier move) with other values.
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_PlayersInactive SELECT * FROM hlstats_Players WHERE playerId = ?`), a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(db.Q(`UPDATE hlstats_PlayersInactive SET kills = 999 WHERE playerId = ?`), a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Options (keyname, value) VALUES ('MinActivity', '30')`)); err != nil {
		t.Fatal(err)
	}
	rowA, rowBot := playerRow(t, db, "hlstats_Players", a), playerRow(t, db, "hlstats_Players", bot)
	others := func() []int {
		return []int{count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`), count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds`),
			count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Data`), count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerNames`),
			count(t, db, `SELECT COUNT(*) FROM hlstats_Players_History`), count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats`)}
	}
	before := others()

	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	jobs := &Jobs{Store: store, Log: strictLog(t), Now: func() time.Time { return now },
		Inactive: InactiveConfig{Enabled: true, At: Schedule{Loc: time.UTC, Hour: 1}}}
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	in := func(table string, id int64) bool { return playerRow(t, db, table, id) != nil }
	for _, w := range []struct {
		name         string
		id           int64
		active, gone bool
	}{{"A", a, false, true}, {"bot", bot, false, true}, {"B (recent)", b, true, false}, {"C (online)", c, true, false}} {
		if in(hlstatsPlayers, w.id) != w.active || in(hlstatsInactive, w.id) != w.gone {
			t.Errorf("%s: in hlstats_Players %v, in hlstats_PlayersInactive %v; want %v, %v",
				w.name, in(hlstatsPlayers, w.id), in(hlstatsInactive, w.id), w.active, w.gone)
		}
	}
	if got := playerRow(t, db, hlstatsInactive, a); !equalRows(got, rowA) {
		t.Errorf("moved row of A differs (the leftover copy must be replaced):\n%v\n%v", got, rowA)
	}
	if after := others(); !slices.Equal(after, before) {
		t.Errorf("other tables changed: %v, before %v", after, before)
	}

	// Until the next daily time nothing runs; MinActivity is read at every run.
	if _, err := db.Exec(db.Q(`UPDATE hlstats_Players SET last_event = ? WHERE playerId = ?`), now.Add(-10*24*time.Hour).Unix(), b); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Poll(ctx); err != nil || !in(hlstatsPlayers, b) {
		t.Fatalf("second poll before the next daily time moved B (err %v)", err)
	}
	if _, err := db.Exec(`UPDATE hlstats_Options SET value = '5' WHERE keyname = 'MinActivity'`); err != nil {
		t.Fatal(err)
	}
	if err := jobs.MoveInactive(ctx, now); err != nil || in(hlstatsPlayers, b) || !in(hlstatsInactive, b) {
		t.Fatalf("MinActivity 5 did not move B (err %v)", err)
	}

	// A comes back: the row returns unchanged.
	ev := stats.Ev{Srv: stats.Server{ID: srv, Game: "css", GameID: 1}, Time: now}
	if _, _, err := store.Player(ctx, ev, "0:301", "A"); err != nil {
		t.Fatal(err)
	}
	if got := playerRow(t, db, hlstatsPlayers, a); !equalRows(got, rowA) || in(hlstatsInactive, a) {
		t.Errorf("A back in hlstats_Players differs:\n%v\n%v", got, rowA)
	}

	// The bot comes back over the log: moved back and counted.
	hub2 := NewHub(store, set, &recorder{}, quiet)
	if err := hub2.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	feed(hub2, `"Bot1<4><BOT><TERRORIST>" killed "B<3><STEAM_1:0:302><CT>" with "weapon1"`)
	flush(t, hub2)
	got := playerRow(t, db, hlstatsPlayers, bot)
	if got == nil || in(hlstatsInactive, bot) || in(hlstatsInactive, b) {
		t.Fatalf("bot or B not moved back: %v", got)
	}
	if got["kills"].String != "2" || rowBot["kills"].String != "1" {
		t.Errorf("bot kills %s (before the move %s), want 2", got["kills"].String, rowBot["kills"].String)
	}
	hub2.Close(ctx)

	// A visit that looked the player up before a move (Join after Player):
	// the visit start moves the row back.
	// (C's live row went with the hub's stop; C stays by a recent event.)
	if _, err := db.Exec(db.Q(`UPDATE hlstats_Players SET last_event = ?`), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(db.Q(`UPDATE hlstats_Players SET last_event = ? WHERE playerId = ?`), old, a); err != nil {
		t.Fatal(err)
	}
	if n, err := store.MoveInactive(ctx, now.Add(-30*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("moved %d, err %v; want A", n, err)
	}
	if err := store.Join(ctx, ev, stats.Live{ID: a, UniqueID: "0:301", Name: "A", Team: "CT"}); err != nil {
		t.Fatal(err)
	}
	got = playerRow(t, db, hlstatsPlayers, a)
	if got == nil || in(hlstatsInactive, a) {
		t.Fatal("Join did not move A back")
	}
	want := maps.Clone(rowA)
	want["last_event"] = sql.NullString{String: strconv.FormatInt(now.Unix(), 10), Valid: true}
	if !equalRows(got, want) {
		t.Errorf("A after Join:\n%v\nwant\n%v", got, want)
	}
}

const (
	hlstatsPlayers  = "hlstats_Players"
	hlstatsInactive = "hlstats_PlayersInactive"
)

func equalRows(a, b map[string]sql.NullString) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestIntegrationInactivePlayersBatches(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	old := time.Now().Add(-100 * 24 * time.Hour).Unix()
	const n = 1203 // more than two batches
	for done := 0; done < n; {
		k := min(200, n-done)
		q := `INSERT INTO hlstats_Players (game, game_id, last_event) VALUES (?, 1, ?)` + strings.Repeat(", (?, 1, ?)", k-1)
		args := make([]any, 0, 2*k)
		for range k {
			args = append(args, "css", old)
		}
		if _, err := db.Exec(db.Q(q), args...); err != nil {
			t.Fatal(err)
		}
		done += k
	}
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	moved, err := store.MoveInactive(ctx, time.Now().Add(-28*24*time.Hour))
	if err != nil || moved != n {
		t.Fatalf("moved %d, err %v; want %d", moved, err, n)
	}
	if p, i := count(t, db, `SELECT COUNT(*) FROM hlstats_Players`), count(t, db, `SELECT COUNT(*) FROM hlstats_PlayersInactive`); p != 0 || i != n {
		t.Errorf("hlstats_Players %d, hlstats_PlayersInactive %d; want 0, %d", p, i, n)
	}
	if moved, err := store.MoveInactive(ctx, time.Now()); err != nil || moved != 0 {
		t.Errorf("second run moved %d, err %v", moved, err)
	}
}

func TestIntegrationInactiveDays(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	for _, c := range []struct {
		value string // "" = no row
		days  int
		warn  bool
	}{{"", 28, true}, {"45", 45, false}, {" 7 ", 7, false}, {"abc", 28, true}, {"0", 28, true}, {"3651", 28, true}} {
		if _, err := db.Exec(`DELETE FROM hlstats_Options WHERE keyname = 'MinActivity'`); err != nil {
			t.Fatal(err)
		}
		if c.value != "" {
			if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Options (keyname, value) VALUES ('MinActivity', ?)`), c.value); err != nil {
				t.Fatal(err)
			}
		}
		days, warn, err := store.InactiveDays(ctx)
		if err != nil || days != c.days || (warn != "") != c.warn {
			t.Errorf("MinActivity %q: %d days, warning %q, err %v; want %d, warning %v", c.value, days, warn, err, c.days, c.warn)
		}
	}
}
