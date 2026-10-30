// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

// Integration test against a real MariaDB / MySQL or PostgreSQL with the
// gameWE schema (internal/schema, created by gamewe db migrate). It
// DELETES ALL ROWS of the database, so it only runs with
// GAMEWE_TEST_DB_WIPE=yes:
//
//	GAMEWE_TEST_DB_TYPE=mysql|postgres GAMEWE_TEST_DB_HOST=127.0.0.1 GAMEWE_TEST_DB_PORT=33306 \
//	GAMEWE_TEST_DB_USER=gamewe GAMEWE_TEST_DB_PASSWORD=... GAMEWE_TEST_DB_NAME=gamewe GAMEWE_TEST_DB_WIPE=yes \
//	go test -race -tags integration ./internal/collector/
package collector

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/schema"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

func testDB(t *testing.T) *database.DB {
	t.Helper()
	if os.Getenv("GAMEWE_TEST_DB_WIPE") != "yes" {
		t.Skip("set GAMEWE_TEST_DB_* and GAMEWE_TEST_DB_WIPE=yes to run (deletes all rows)")
	}
	port, _ := strconv.Atoi(os.Getenv("GAMEWE_TEST_DB_PORT"))
	cfg := config.Defaults().Database
	cfg.Host, cfg.Port = os.Getenv("GAMEWE_TEST_DB_HOST"), port
	cfg.Type = config.MySQL
	if os.Getenv("GAMEWE_TEST_DB_TYPE") == config.Postgres {
		cfg.Type = config.Postgres
	}
	cfg.User, cfg.Password, cfg.Name = os.Getenv("GAMEWE_TEST_DB_USER"), os.Getenv("GAMEWE_TEST_DB_PASSWORD"), os.Getenv("GAMEWE_TEST_DB_NAME")
	cfg.TLS.Mode = config.TLSDisable
	cfg.TablePrefix = config.NormalizeTablePrefix(os.Getenv("GAMEWE_TEST_DB_PREFIX")) // e.g. gw: the tables gw_*
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	release, err := database.LockForTest(ctx, db, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release) // runs before db.Close (cleanups run last first)
	if err := database.CheckTables(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Every table of the schema (also those the collector does not list in
	// database.Tables, such as hlstats_Players_Resets): a row left from an
	// earlier run would be found again once the ids start at 1 again.
	all, err := schema.Tables(db.Type)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range all {
		tbl := tb.Name
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatal(err)
		}
		// InnoDB keeps the AUTO_INCREMENT counter over a DELETE (MyISAM
		// started again at the highest id); the small ids (teams, roles)
		// would run out over many test runs. Back to 1 (the tables are
		// empty; the collector user may ALTER).
		if !db.Postgres() {
			if _, err := db.Exec("ALTER TABLE " + tbl + " AUTO_INCREMENT = 1"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, q := range []string{
		// explicit ids: the tinyint id would run out over many test runs
		`INSERT INTO hlstats_Games (id, code, name) VALUES (1, 'css', 'Counter-Strike: Source'), (2, 'tf', 'Team Fortress 2')`,
		`INSERT INTO hlstats_Actions (game, code, reward_player, reward_team, team, for_PlayerActions, for_TeamActions) VALUES
			('css', 'Planted_The_Bomb', 15, 0, 'TERRORIST', '1', '0'), ('css', 'CTs_Win', 0, 2, 'CT', '0', '1')`,
		`INSERT INTO hlstats_Weapons (game, code, name, modifier) VALUES ('css', 'weapon1', 'W1', 2.00)`,
		`INSERT INTO hlstats_Actions (game, code, reward_player, for_PlayerActions) VALUES ('css', 'headshot', 1, '1')`,
		`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_store_chat', '1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// strictLog returns a logger whose warnings and errors fail the test.
func strictLog(t *testing.T) *slog.Logger {
	var buf bytes.Buffer
	t.Cleanup(func() {
		if buf.Len() > 0 {
			t.Errorf("unexpected log output:\n%s", buf.String())
		}
	})
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// dbHub inserts the servers into hlstats_Servers (address -> game) and
// returns a hub that loaded them, like 'gamewe run' does.
func dbHub(t *testing.T, db *database.DB, rec Replier, servers [][2]string, log ...*slog.Logger) *Hub {
	t.Helper()
	for _, s := range servers {
		insertServer(t, db, s[0], s[1])
	}
	lg := strictLog(t)
	if len(log) > 0 {
		lg = log[0]
	}
	set := settings.NewLive(settings.Defaults())
	hub := NewHub(stats.NewStore(db, set), set, rec, lg)
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return hub
}

func insertServer(t *testing.T, db *database.DB, addr, game string) {
	t.Helper()
	ap := netip.MustParseAddrPort(addr)
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Servers (address, port, name, game, rcon_password) VALUES (?, ?, ?, ?, 'pw')`),
		ap.Addr().String(), ap.Port(), "srv "+addr, game); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *database.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(db.Q(q), args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestIntegrationThreeServers(t *testing.T) {
	db := testDB(t)
	addrs := []string{"198.51.100.1:27015", "198.51.100.1:27016", "198.51.100.2:27015"}
	rec := &recorder{}
	hub := dbHub(t, db, rec, [][2]string{{addrs[0], "css"}, {addrs[1], "css"}, {addrs[2], "tf"}})
	if hub.Count() != 3 {
		t.Fatalf("hub serves %d servers", hub.Count())
	}

	games := [][]string{gameLines(1), gameLines(2), gameLines(3)}
	for s := range games {
		games[s] = append([]string{`Started map "de_dust2" (CRC "1")`}, games[s]...)
		games[s] = append(games[s], fmt.Sprintf(`"B%d<3><STEAM_1:1:%d02><TERRORIST>" triggered "Planted_The_Bomb"`, s+1, s+1))
	}
	now := time.Now().UTC()
	for i := range games[0] {
		for s, addr := range addrs {
			if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(games[s][i]), now); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Before shutdown: the live view has the players still online, with
	// their visit counters, also after a rename.
	rename := `"Roamer<6><STEAM_1:0:777><CT>" changed name to "Рома 😀 Jörg"`
	if err := hub.Dispatch(netip.MustParseAddrPort(addrs[2]), pkt(rename), now); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	const newName = "Рома 😀 Jörg" // Cyrillic, emoji and latin1 need utf8mb4
	// Wait until every server has processed its lines (the workers run in
	// parallel; PostgreSQL needs more round trips than MariaDB).
	for time.Now().Before(deadline) && (count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE name = ?`, newName) == 0 ||
		count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Disconnects`) < 3) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := count(t, db, `SELECT kills FROM hlstats_Livestats WHERE name = ?`, newName); got != 1 {
		t.Errorf("live kills of the renamed roamer: %d, want 1", got)
	}
	var stored string
	if err := db.QueryRow(`SELECT d.lastName FROM hlstats_Players_Data d JOIN hlstats_PlayerUniqueIds u USING (playerId)
		WHERE u.uniqueId = '0:777' AND u.game = 'tf'`).Scan(&stored); err != nil || stored != newName {
		t.Errorf("stored name %q (%v), want %q", stored, err, newName)
	}
	var nameRow string
	if err := db.QueryRow(db.Q(`SELECT name FROM hlstats_PlayerNames WHERE name = ?`), newName).Scan(&nameRow); err != nil || nameRow != newName {
		t.Errorf("hlstats_PlayerNames: %q (%v), want the exact UTF-8 name %q", nameRow, err, newName)
	}
	// hlstats_Livestats is keyed by player_id alone: the css roamer, online
	// on two css servers at once, has one row (5 = 3 B players + 2 roamers).
	if got := count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats`); got != 5 {
		t.Errorf("live rows before stop: %d, want 5", got)
	}
	hub.Close(context.Background())

	css := count(t, db, `SELECT id FROM hlstats_Games WHERE code = 'css'`)
	tf := count(t, db, `SELECT id FROM hlstats_Games WHERE code = 'tf'`)
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"servers", count(t, db, `SELECT COUNT(*) FROM hlstats_Servers`), 3},
		{"server 1 kills", count(t, db, `SELECT kills FROM hlstats_Servers WHERE address = '198.51.100.1' AND port = 27015`), 2},
		{"server 3 game", count(t, db, `SELECT COUNT(*) FROM hlstats_Servers WHERE address = '198.51.100.2' AND game = 'tf'`), 1},
		{"server rounds/ct_wins", count(t, db, `SELECT SUM(rounds) + SUM(ct_wins) FROM hlstats_Servers`), 6},
		{"bombs planted", count(t, db, `SELECT SUM(bombs_planted) FROM hlstats_Servers`), 3},
		{"css frags", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE game_id = ?`, css), 4},
		{"tf frags", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE game_id = ?`, tf), 2},
		{"frags have a map", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE map = 0`), 0},
		// The roamer is one player per game, even though two css servers
		// registered it at the same moment.
		{"roamer unique ids", count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE uniqueId = '0:777'`), 2},
		{"no orphan players", count(t, db, `SELECT COUNT(*) FROM hlstats_Players p LEFT JOIN hlstats_PlayerUniqueIds u USING (playerId) WHERE u.playerId IS NULL`), 0},
		{"players", count(t, db, `SELECT COUNT(*) FROM hlstats_Players`), 3*3 - 1},
		{"roamer css kills", count(t, db, `SELECT p.kills FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u USING (playerId) WHERE u.uniqueId = '0:777' AND u.game = 'css'`), 2},
		{"roamer css wins", count(t, db, `SELECT p.wins FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u USING (playerId) WHERE u.uniqueId = '0:777' AND u.game = 'css'`), 2},
		{"roamer tf kills", count(t, db, `SELECT p.kills FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u USING (playerId) WHERE u.uniqueId = '0:777' AND u.game = 'tf'`), 1},
		{"team bonuses (css only)", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_TeamBonuses WHERE bonus = 2 AND game_id = ?`, css), 4},
		{"player actions", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions WHERE bonus = 15`), 2},
		{"weapon stats", count(t, db, `SELECT SUM(shots) FROM hlstats_Players_Weapons`), 30},
		// The same shots per player, week and map, for the accuracy per map.
		{"map shots", count(t, db, `SELECT SUM(shots) FROM hlstats_Players_Maps`), 30},
		{"map stats have a map", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Maps WHERE map_id = 0`), 0},
		{"history rows", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_History WHERE kills > 0`), 3*2 - 1},
		{"chat lines", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Chat`), 3},
		{"connects", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Connects`), 9},
		{"disconnects", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Disconnects`), 3},
		{"live rows after stop", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats`), 0},
		{"act_players after stop", count(t, db, `SELECT SUM(act_players) FROM hlstats_Servers`), 0},
		{"connection time stored", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Data WHERE total_connects = 0`), 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	// weapon1 has modifier 2.00 on css: a kill is worth 10 (+1 headshot).
	var skill int
	if err := db.QueryRow(`SELECT p.skill FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u USING (playerId)
		WHERE u.uniqueId = '0:101' AND u.game = 'css'`).Scan(&skill); err != nil {
		t.Fatal(err)
	}
	if skill != 1000+10+1+2 { // kill (weapon modifier 2), headshot action, round win bonus
		t.Errorf("server 1 killer skill %d, want 1013", skill)
	}
	for _, a := range addrs {
		if sent := rec.get(a); len(sent) != 1 {
			t.Errorf("%s: replies %q", a, sent)
		}
	}
}

// Many kills between the same two players on two servers at once: every
// counter must be exact although MyISAM has no transactions.
func TestIntegrationConcurrentCounters(t *testing.T) {
	db := testDB(t)
	addrs := []string{"198.51.100.1:27015", "198.51.100.1:27016"}
	hub := dbHub(t, db, &recorder{}, [][2]string{{addrs[0], "css"}, {addrs[1], "css"}})
	const n = 200
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		for s, addr := range addrs {
			line := fmt.Sprintf(`"K<%d><STEAM_1:0:1><CT>" killed "V<%d><STEAM_1:0:2><TERRORIST>" with "ak47"`, 2+s, 3+s)
			if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(line), now); err != nil {
				t.Fatal(err)
			}
		}
	}
	hub.Close(context.Background())
	if got := count(t, db, `SELECT p.kills FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u USING (playerId) WHERE u.uniqueId = '0:1'`); got != 2*n {
		t.Errorf("killer kills %d, want %d", got, 2*n)
	}
	if got := count(t, db, `SELECT kills FROM hlstats_Weapons WHERE code = 'ak47'`); got != 2*n {
		t.Errorf("weapon kills %d, want %d", got, 2*n)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds`); got != 2 {
		t.Errorf("unique ids %d, want 2", got)
	}
}

// A server inserted into hlstats_Servers while the collector runs is used
// after the next reload; unknown senders are rejected; servers with
// unusable rows are ignored.
func TestIntegrationServerAddedAtRuntime(t *testing.T) {
	db := testDB(t)
	rec := &recorder{}
	var logs bytes.Buffer
	hub := dbHub(t, db, rec, [][2]string{{"198.51.100.1:27015", "css"}}, slog.New(slog.NewTextHandler(&logs, nil)))
	defer hub.Close(context.Background())
	// Unusable rows: host name instead of IP, unknown game.
	if _, err := db.Exec(`INSERT INTO hlstats_Servers (address, port, name, game) VALUES
		('cs.example.org', 27015, 'by name', 'css'), ('198.51.100.9', 27015, 'no game', 'xyz')`); err != nil {
		t.Fatal(err)
	}
	added := netip.MustParseAddrPort("198.51.100.5:27016")
	line := `"N<2><STEAM_1:0:55><CT>" say "!rank"`
	if err := hub.Dispatch(added, pkt(line), time.Now()); err != ErrUnknownSource {
		t.Fatalf("before insert: %v", err)
	}
	insertServer(t, db, added.String(), "css")
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hub.Count() != 2 {
		t.Errorf("hub serves %d servers, want 2 (two rows are unusable)", hub.Count())
	}
	for _, want := range []string{`address \"cs.example.org\" is not an IP address`, `game \"xyz\" is not in hlstats_Games`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not mention %s:\n%s", want, logs.String())
		}
	}
	// A second reload does not repeat the warnings.
	before := strings.Count(logs.String(), "is ignored")
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := strings.Count(logs.String(), "is ignored"); after != before {
		t.Errorf("warnings repeated: %d -> %d", before, after)
	}
	if err := hub.Dispatch(added, pkt(line), time.Now()); err != nil {
		t.Fatalf("after reload: %v", err)
	}
	if err := hub.Dispatch(netip.MustParseAddrPort("203.0.113.66:27015"), pkt(line), time.Now()); err != ErrUnknownSource {
		t.Errorf("unknown sender: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(rec.get(added.String())) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := rec.get(added.String()); len(got) != 1 || !strings.HasPrefix(got[0], "gamewe_msay 15 2 0 ") {
		t.Errorf("reply to the added server: %q", got)
	}
}

// The default data of internal/schema/mysql/seed.sql rewards the HLstatsX events:
// player actions, headshot and kill streak actions, player-vs-player actions
// and team bonuses.
// loadSeed replaces the default data with internal/schema/mysql/seed.sql.
func loadSeed(t *testing.T, db *database.DB) {
	t.Helper()
	for _, tbl := range []string{"hlstats_Games", "hlstats_Actions", "hlstats_Weapons", "hlstats_Options", "hlstats_Teams",
		"hlstats_Roles", "hlstats_Awards", "hlstats_Definition_Ranks_Ribbons", "hlstats_Definition_Ribbons_Classes", "hlstats_ClanTags"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatal(err)
		}
	}
	seedFile := "../../internal/schema/mysql/seed.sql"
	if db.Postgres() {
		seedFile = "../../internal/schema/postgres/seed.sql"
	}
	seed, err := os.ReadFile(seedFile)
	if err != nil {
		t.Fatal(err)
	}
	// A statement ends with ";" at the end of a line (a comment may follow).
	for _, stmt := range regexp.MustCompile(`;[ \t]*(?:--[^\n]*)?\n`).Split(string(seed)+"\n", -1) {
		var lines []string
		for _, l := range strings.Split(stmt, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "--") {
				lines = append(lines, l)
			}
		}
		q := strings.TrimSpace(strings.Join(lines, "\n"))
		if strings.Contains(strings.SplitN(q, "(", 2)[0], "hlstats_Roles") {
			// hlstats_Roles.roleId is a tinyint, and MyISAM keeps counting
			// after DELETE: the ~100 roles of the seed, loaded again by
			// every test that calls this, would pass 255 within one test
			// run. These tests use no roles.
			continue
		}
		if q != "" {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("seed: %v\n%.200s", err, q)
			}
		}
	}
}

func TestIntegrationSeedEvents(t *testing.T) {
	db := testDB(t)
	loadSeed(t, db)
	addr := "198.51.100.1:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}})
	lines := []string{
		`"K<2><STEAM_1:0:71><Unassigned>" joined team "CT"`,
		`"V<3><STEAM_1:0:72><Unassigned>" joined team "TERRORIST"`,
		`"W<4><STEAM_1:0:73><Unassigned>" joined team "TERRORIST"`,
		`"K<2><STEAM_1:0:71><CT>" killed "V<3><STEAM_1:0:72><TERRORIST>" with "ak47" (headshot)`,
		`"K<2><STEAM_1:0:71><CT>" killed "W<4><STEAM_1:0:73><TERRORIST>" with "ak47"`,
		`"W<4><STEAM_1:0:73><TERRORIST>" triggered "Planted_The_Bomb"`,
		`"K<2><STEAM_1:0:71><CT>" triggered "gg_levelup"`,
		`"K<2><STEAM_1:0:71><CT>" triggered "domination" against "V<3><STEAM_1:0:72><TERRORIST>"`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
	}
	for _, l := range lines {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	rows, err := db.Query(`SELECT a.code, e.bonus FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId ORDER BY a.code`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var code string
		var bonus int
		rows.Scan(&code, &bonus)
		got = append(got, fmt.Sprintf("%s=%d", code, bonus))
	}
	rows.Close()
	if strings.Join(got, " ") != "gg_levelup=1 headshot=1 kill_streak_2=1 Planted_The_Bomb=10" { // utf8mb4_unicode_ci sorts case-insensitively
		t.Errorf("player actions: %v", got)
	}
	checks := []struct {
		name      string
		got, want int
	}{
		{"domination", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerPlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.code = 'domination' AND e.bonus = 5`), 1},
		{"team bonus", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_TeamBonuses WHERE bonus = 2`), 1},
		{"actions counted", count(t, db, `SELECT SUM(count) FROM hlstats_Actions`), 6},
		{"weekly action stats", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Actions`), 5},
		// HLstatsX: the victim of a player-vs-player action loses the reward.
		{"domination victim", count(t, db, `SELECT p.last_skill_change FROM hlstats_Players p
			JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = '0:72'`), -5},
		// One round, won by K; V and W lost it.
		{"history rounds", count(t, db, `SELECT SUM(rounds) FROM hlstats_Players_History`), 3},
		{"history wins", count(t, db, `SELECT SUM(h.wins) FROM hlstats_Players_History h
			JOIN hlstats_PlayerUniqueIds u ON u.playerId = h.playerId WHERE u.uniqueId = '0:71'`), 1},
		{"history losses", count(t, db, `SELECT SUM(losses) FROM hlstats_Players_History`), 2},
		{"history kill streak", count(t, db, `SELECT MAX(kill_streak) FROM hlstats_Players_History`), 2},
		{"history death streak", count(t, db, `SELECT MAX(death_streak) FROM hlstats_Players_History`), 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// The week number computed in Go must equal MariaDB's YEARWEEK() (mode 0),
// which old HLstatsX code used, for every day of many years.
func TestIntegrationYearWeek(t *testing.T) {
	db := testDB(t)
	if db.Postgres() {
		t.Skip("YEARWEEK is a MariaDB function")
	}
	for d := time.Date(1999, 12, 1, 0, 0, 0, 0, time.UTC); d.Year() < 2041; d = d.AddDate(0, 0, 1) {
		var want int
		if err := db.QueryRow(`SELECT YEARWEEK(?)`, d.Format("2006-01-02")).Scan(&want); err != nil {
			t.Fatal(err)
		}
		if got := stats.YearWeek(d); got != want {
			t.Fatalf("YearWeek(%s) = %d, MariaDB says %d", d.Format("2006-01-02"), got, want)
		}
	}
}

// The HLstatsX behaviour: kill streak rewards when the
// streak ends, team-switch suicides, idle players, lines after the
// disconnect, clans, bans, Steam3 ids and the live view flags.
func TestIntegrationHLstatsXBehaviour(t *testing.T) {
	db := testDB(t)
	for _, q := range []string{
		`INSERT INTO hlstats_Actions (game, code, reward_player, for_PlayerActions) VALUES
			('css', 'kill_streak_2', 1, '1'), ('css', 'kill_streak_3', 2, '1'), ('css', 'Got_The_Bomb', 0, '1')`,
		`DELETE FROM hlstats_ClanTags`,
		`INSERT INTO hlstats_ClanTags (pattern, position, pattern_length, sort_id) VALUES ('[AXXXXX]', 'EITHER', 8, 1)`,
		`DELETE FROM hlstats_Clans`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	addr := "198.51.100.1:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}})
	now := time.Now().UTC()
	send := func(lines ...string) {
		t.Helper()
		for _, l := range lines {
			if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), now); err != nil {
				t.Fatal(err)
			}
		}
	}
	send(
		`"[ZOOM]Kay<2><[U:1:142]><Unassigned>" joined team "CT"`, // Steam3 id of STEAM_0:0:71
		`"Vic<3><STEAM_1:0:72><Unassigned>" joined team "TERRORIST"`,
		`"Wes<4><STEAM_1:0:73><Unassigned>" joined team "TERRORIST"`,
		`World triggered "Round_Start"`,
		`"Wes<4><STEAM_1:0:73><TERRORIST>" triggered "Got_The_Bomb"`,
		`"[ZOOM]Kay<2><[U:1:142]><CT>" killed "Vic<3><STEAM_1:0:72><TERRORIST>" with "ak47"`,
		`"[ZOOM]Kay<2><[U:1:142]><CT>" killed "Wes<4><STEAM_1:0:73><TERRORIST>" with "ak47"`,
		`"[ZOOM]Kay<2><[U:1:142]><CT>" killed "Vic<3><STEAM_1:0:72><TERRORIST>" with "ak47"`,
		// Kay's streak of 3 ends with this death: one kill_streak_3, no kill_streak_2.
		`"Vic<3><STEAM_1:0:72><TERRORIST>" killed "[ZOOM]Kay<2><[U:1:142]><CT>" with "glock"`,
		// Changing teams while alive: the game logs a suicide, which is ignored.
		`"Wes<4><STEAM_1:0:73><TERRORIST>" joined team "CT"`,
		`"Wes<4><STEAM_1:0:73><CT>" committed suicide with "world"`,
		`"Vic<3><STEAM_1:0:72><TERRORIST>" disconnected (reason "VAC banned from secure server")`,
		// After the disconnect: stats only, no new visit and no live row.
		`"Vic<3><STEAM_1:0:72><TERRORIST>" triggered "weaponstats" (weapon "glock") (shots "9") (hits "3") (kills "1") (headshots "0") (tks "0") (damage "120") (deaths "0")`,
		`"Vic<3><STEAM_1:0:72><TERRORIST>" triggered "Dropped_The_Bomb"`,
		`Banid: "Wes<4><STEAM_1:0:73><>" was kicked and banned "permanently" by "Console"`,
	)
	// Wait until the lines are processed, then let Wes time out.
	deadline := time.Now().Add(10 * time.Second)
	for count(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE hideranking = 2`) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE is_dead = 1 AND has_bomb = 0`); n != 2 {
		t.Errorf("dead in the live view: %d, want 2 (Kay, Wes)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats`); n != 2 {
		t.Errorf("live rows: %d, want 2 (Kay, Wes)", n)
	}
	hub.Tick(now.Add(time.Duration(settings.Defaults().IdleTimeout+1) * time.Second))
	hub.Close(context.Background())

	player := func(uid string) string {
		return `(SELECT playerId FROM hlstats_PlayerUniqueIds WHERE uniqueId = '` + uid + `')`
	}
	checks := []struct {
		name      string
		got, want int
	}{
		{"Steam3 id stored as 0:71", count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE uniqueId = '0:71'`), 1},
		{"kill_streak_3", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId
			WHERE a.code = 'kill_streak_3' AND e.playerId = `+player("0:71")), 1},
		{"no kill_streak_2", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId
			WHERE a.code = 'kill_streak_2'`), 0},
		{"team-switch suicide ignored", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Suicides`), 0},
		{"VAC ban hidden", count(t, db, `SELECT hideranking FROM hlstats_Players WHERE playerId = `+player("0:72")), 2},
		{"permanent ban hidden", count(t, db, `SELECT hideranking FROM hlstats_Players WHERE playerId = `+player("0:73")), 2},
		{"weaponstats after leaving", count(t, db, `SELECT shots FROM hlstats_Players WHERE playerId = `+player("0:72")), 9},
		{"one visit of Vic", count(t, db, `SELECT numuses FROM hlstats_PlayerNames WHERE playerId = `+player("0:72")), 1},
		{"idle and banned players left", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats`), 0},
		{"clan created", count(t, db, `SELECT COUNT(*) FROM hlstats_Clans WHERE tag = '[ZOOM]' AND name = 'ZOOM' AND game = 'css'`), 1},
		{"clan of Kay", count(t, db, `SELECT COUNT(*) FROM hlstats_Players p JOIN hlstats_Clans c ON c.clanId = p.clan WHERE p.playerId = `+player("0:71")), 1},
		{"no clan for Vic", count(t, db, `SELECT clan FROM hlstats_Players WHERE playerId = `+player("0:72")), 0},
		{"Wes connection time stored", count(t, db, `SELECT CASE WHEN connection_time >= 300 THEN 1 ELSE 0 END FROM hlstats_Players WHERE playerId = `+player("0:73")), 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	// hlstats_Servers.players is refreshed by the next reload.
	set := settings.NewLive(settings.Defaults())
	if err := NewHub(stats.NewStore(db, set), set, &recorder{}, strictLog(t)).Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT players FROM hlstats_Servers`); n != 1 {
		t.Errorf("hlstats_Servers.players = %d, want 1 (Kay; Vic and Wes are banned)", n)
	}
}

// Lines of the gameWE / gameME SourceMod plugin (CS:S): headshot lines,
// locations, player models, queries of other plugins.
func TestIntegrationPluginLines(t *testing.T) {
	db := testDB(t)
	for _, q := range []string{
		`DELETE FROM hlstats_Roles`,
		`INSERT INTO hlstats_Roles (game, code, name) VALUES ('css', 'gign', 'GIGN'), ('css', 'leet', 'Elite Crew')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	addr := "198.51.100.1:27015"
	rec := &recorder{}
	hub := dbHub(t, db, rec, [][2]string{{addr, "css"}})
	for _, l := range []string{
		`"Alice<2><STEAM_1:0:81><Unassigned>" joined team "CT"`,
		`"Bob<3><STEAM_1:0:82><Unassigned>" joined team "TERRORIST"`,
		`"Alice<2><STEAM_1:0:81><CT>" changed role to "gign"`,
		`"Bob<3><STEAM_1:0:82><TERRORIST>" changed role to "leet"`,
		`"Alice<2><STEAM_1:0:81><CT>" triggered "headshot"`,
		`"Alice<2><STEAM_1:0:81><CT>" located on "kill" (position "-1200 345 64") against "Bob<3><STEAM_1:0:82><TERRORIST>" (victim_position "-1100 300 0")`,
		`"Alice<2><STEAM_1:0:81><CT>" killed "Bob<3><STEAM_1:0:82><TERRORIST>" with "deagle" (headshot)`,
		`"Alice<2><STEAM_1:0:81><CT>" requested "playerinfo" (value "7")`,
		`"Server" requested "top10" (value "8")`,
		`"Bob<3><STEAM_1:0:82><TERRORIST>" requested "next" (value "9")`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	checks := []struct {
		name      string
		got, want int
	}{
		{"one headshot action", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.code = 'headshot'`), 1},
		{"role changes", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_ChangeRole`), 2},
		{"role kills", count(t, db, `SELECT kills + headshots FROM hlstats_Roles WHERE code = 'gign'`), 2},
		{"role deaths", count(t, db, `SELECT deaths FROM hlstats_Roles WHERE code = 'leet'`), 1},
		{"kill location", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Locations WHERE type = 1 AND loc_1 = -1200 AND loc_2 = 345 AND map = 0`), 1},
		{"death location", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Locations WHERE type = 2 AND loc_1 = -1100 AND loc_2 = 300`), 1},
		{"location day", count(t, db, `SELECT MAX(day) FROM hlstats_Events_Locations`), int(time.Now().Unix() / 86400)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	sent := rec.get(addr)
	if len(sent) != 3 {
		t.Fatalf("answers: %q", sent)
	}
	if a := strings.Fields(sent[0]); len(a) < 4 || a[0] != "gamewe_raw_message" || a[1] != "101" || a[2] != "7" || a[3] != "2" {
		t.Errorf("playerinfo: %s", sent[0])
	}
	if !strings.HasPrefix(sent[1], `gamewe_raw_message 102 8 -1 `) || !strings.Contains(sent[1], `"Alice"`) {
		t.Errorf("top10: %s", sent[1])
	}
	if !strings.HasPrefix(sent[2], `gamewe_raw_message 103 9 3 1 `) {
		t.Errorf("next: %s", sent[2])
	}
}

func TestIntegrationDailyAwards(t *testing.T) {
	db := testDB(t)
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	in, out := day.Add(12*time.Hour).Unix(), day.Add(-time.Hour).Unix() // inside / before the day
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(db.Q(q), args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO hlstats_Awards (awardType, game, code, name, verb) VALUES
		('W', 'css', 'ak47', 'AK47', 'kills'), ('W', 'css', 'latency', 'Best Latency', 'ms'),
		('O', 'css', 'Planted_The_Bomb', 'Demolitionist', 'plantings'), ('O', 'css', 'most_kills', 'Most Kills', 'kills'),
		('O', 'css', 'longest_playtime', 'Longest Play Time', 'hours'), ('O', 'css', 'most_damage', 'Most Damage', 'damage'),
		('O', 'css', 'Rescued_A_Hostage', 'Rescuer', 'hostages'),
		('W', 'css', 'teamkills', 'Team Killer', 'team kills')`)
	exec(`INSERT INTO hlstats_Players (playerId, game, game_id, skill, hideranking) VALUES
		(11, 'css', 1, 1000, 0), (12, 'css', 1, 1000, 0), (13, 'css', 1, 1000, 2), (14, 'css', 1, 1000, 0)`)
	exec(`INSERT INTO hlstats_Players_Data (playerId, lastName) VALUES (11, 'A'), (12, 'B'), (13, 'C'), (14, 'D')`)
	ak := count(t, db, `SELECT weaponId FROM hlstats_Weapons WHERE game = 'css' AND code = 'weapon1'`)
	exec(`UPDATE hlstats_Weapons SET code = 'ak47' WHERE weaponId = ?`, ak)
	bomb := count(t, db, `SELECT id FROM hlstats_Actions WHERE game = 'css' AND code = 'Planted_The_Bomb'`)
	frag := `INSERT INTO hlstats_Events_Frags (timestamp_id, game_id, map, killerId, victimId, weapon, headshot) VALUES (?, 1, 0, ?, 14, ?, 0)`
	for _, k := range []struct{ ts, killer int64 }{{in, 11}, {in, 11}, {in, 12}, {in, 12}, {in, 13}, {in, 13}, {in, 13}, {out, 12}, {out, 12}} {
		exec(frag, k.ts, k.killer, ak) // 11 and 12 tie at 2 (12 has 2 more the day before), 13 is banned
	}
	exec(`INSERT INTO hlstats_Events_PlayerActions (timestamp_id, game_id, map, playerId, actionId, bonus) VALUES (?, 1, 0, 14, ?, 10)`, in, bomb)
	exec(`INSERT INTO hlstats_Players_History (playerId, eventTime, game, kills, connection_time, damage, team_kills) VALUES
		(11, '2026-10-21', 'css', 2, 3600, 100, 0), (12, '2026-10-21', 'css', 2, 7200, 0, 3), (13, '2026-10-21', 'css', 9, 9000, 900, 5),
		(14, '2026-10-20', 'css', 50, 99999, 999, 7)`)
	// Ping samples: 11 averages 40; 12 averages 35.5 (its samples of 0 do not
	// count); 14 has only 29 samples of the day; 13 is banned.
	lat := `INSERT INTO hlstats_Events_Latency (timestamp_id, game_id, server_id, map, playerId, ping) VALUES (?, 1, 1, 0, ?, ?)`
	for i := int64(0); i < 30; i++ {
		exec(lat, in+i*60, 11, 40)
		exec(lat, in+i*60, 12, 35+i%2)
		exec(lat, in+i*60, 13, 5)
	}
	for i := int64(0); i < 20; i++ {
		exec(lat, in+i*60, 12, 0)
	}
	for i := int64(0); i < 29; i++ {
		exec(lat, in+i*60, 14, 10)
	}
	for i := int64(0); i < 5; i++ {
		exec(lat, out-i*60, 14, 10) // the day before
	}
	exec(lat, day.Add(-100*24*time.Hour).Unix(), 14, 10) // older than LatencyKeep

	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // a rerun replaces the day
		if _, err := store.DailyAwards(ctx, day, "css"); err != nil {
			t.Fatal(err)
		}
	}
	winner := func(code string) string {
		var w, c sql.NullInt64
		err := db.QueryRow(db.Q(`SELECT h.winner_id, h.winner_count FROM hlstats_Awards_History h
			JOIN hlstats_Awards a ON a.awardId = h.award_id WHERE a.code = ? AND h.date = '2026-10-21'`), code).Scan(&w, &c)
		if errors.Is(err, sql.ErrNoRows) {
			return "-"
		}
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d:%d", w.Int64, c.Int64)
	}
	for code, want := range map[string]string{
		"ak47": "11:2", "latency": "12:36", "Planted_The_Bomb": "14:1", "most_kills": "11:2",
		"longest_playtime": "12:7200", "most_damage": "11:100", "Rescued_A_Hostage": "-",
		// HLstatsX:CE's W award of the day's history, not a weapon
		"teamkills": "12:3",
	} {
		if got := winner(code); got != want {
			t.Errorf("%s: winner %s, want %s", code, got, want)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Awards_History`); n != 7 {
		t.Errorf("%d history rows, want 7", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Latency WHERE timestamp_id < ?`, out-3600); n != 1 {
		t.Errorf("old ping samples: %d before the run for all games, want 1 (a run for one game keeps them)", n)
	}
	if n := count(t, db, `SELECT d_winner_id FROM hlstats_Awards WHERE code = 'longest_playtime'`); n != 12 {
		t.Errorf("d_winner_id %d, want 12", n)
	}
	// The next day: no frags, so no ak47 winner; d_winner_* stays the latest winner.
	if _, err := store.DailyAwards(ctx, day.Add(24*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT d_winner_id FROM hlstats_Awards WHERE code = 'ak47'`); n != 11 {
		t.Errorf("d_winner_id after an empty day %d, want 11", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Latency WHERE timestamp_id < ?`, out-3600); n != 0 {
		t.Errorf("%d ping samples older than LatencyKeep left", n)
	}
	if done, err := store.AwardsDone(ctx, day); err != nil || !done {
		t.Errorf("AwardsDone = %v, %v", done, err)
	}
}

// Two servers of one game with the same bot names (the engine's default
// names): a bot is one player per name and game, and hlstats_Livestats has
// one row per player, so the live row moves to the server that saw the bot
// last. act_bots counts the bots of each server on its own.
func TestIntegrationBotsPerServer(t *testing.T) {
	db := testDB(t)
	addrs := []string{"198.51.100.1:27015", "198.51.100.1:27016"}
	hub := dbHub(t, db, &recorder{}, [][2]string{{addrs[0], "css"}, {addrs[1], "css"}})
	for i, a := range addrs {
		lines := []string{`"Bot Joe<3><BOT><CT>" entered the game`, `"Bot Ann<4><BOT><TERRORIST>" entered the game`}
		if i == 0 {
			lines = append(lines, `"Alice<2><STEAM_1:0:91><CT>" entered the game`)
		}
		for _, l := range lines {
			if err := hub.Dispatch(netip.MustParseAddrPort(a), pkt(l), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		// The second server's bots come after the first server's.
		deadline := time.Now().Add(10 * time.Second)
		for count(t, db, `SELECT COALESCE(SUM(act_players), 0) FROM hlstats_Servers`) < 3+2*i && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	first := `(SELECT serverId FROM hlstats_Servers WHERE port = 27015)`
	second := `(SELECT serverId FROM hlstats_Servers WHERE port = 27016)`
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"act_players first", count(t, db, `SELECT act_players FROM hlstats_Servers WHERE serverId = `+first), 3},
		{"act_bots first", count(t, db, `SELECT act_bots FROM hlstats_Servers WHERE serverId = `+first), 2},
		{"act_players second", count(t, db, `SELECT act_players FROM hlstats_Servers WHERE serverId = `+second), 2},
		{"act_bots second", count(t, db, `SELECT act_bots FROM hlstats_Servers WHERE serverId = `+second), 2},
		// The shared bots' live rows are on the second server only.
		{"live bots first", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE unique_id LIKE 'BOT:%' AND server_id = `+first), 0},
		{"live bots second", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE unique_id LIKE 'BOT:%' AND server_id = `+second), 2},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// Bots are recorded but hidden, GeoIP locations, hlstats_server_load.
func TestIntegrationBotsGeoLoad(t *testing.T) {
	db := testDB(t)
	addr := "203.0.113.136:27015"
	insertServer(t, db, addr, "css")
	// Bots of css neither ranked nor worth points (read by Reload).
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Options (keyname, value) VALUES ('gamewe_rank_bots_css', '0'), ('gamewe_bot_points_css', '0'), ('gamewe_bot_kills_css', '0')`)); err != nil {
		t.Fatal(err)
	}
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	hub := NewHub(store, set, &recorder{}, strictLog(t))
	hub.Geo = fakeGeo{}
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{
		`"Bot Bob<9><BOT><TERRORIST>" joined team "TERRORIST"`,
		`"K<2><STEAM_1:0:91><>" connected, address "203.0.113.2:27005"`,
		`"K<2><STEAM_1:0:91><CT>" joined team "CT"`,
		`"K<2><STEAM_1:0:91><CT>" killed "Bot Bob<9><BOT><TERRORIST>" with "weapon1"`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	checks := []struct {
		name      string
		got, want int
	}{
		{"bot hidden", count(t, db, `SELECT p.hideranking FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = 'BOT:Bot Bob'`), 1},
		{"bot live", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE unique_id = 'BOT:Bot Bob'`), 1},
		// The web's live view: rows with player_hidden = 0 joined to the server.
		{"bot in live view", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats l JOIN hlstats_Servers s ON s.serverId = l.server_id
			WHERE l.unique_id = 'BOT:Bot Bob' AND l.player_hidden = 0`), 1},
		{"act_players with bot", count(t, db, `SELECT act_players FROM hlstats_Servers`), 2},
		{"human skill unchanged", count(t, db, `SELECT p.skill FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = '0:91'`), 1000},
		// gamewe_bot_kills_css = 0: the frag is stored, the kill not counted.
		{"human kill of the bot not counted", count(t, db, `SELECT p.kills FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = '0:91'`), 0},
		// gamewe_bot_kills_css = 0: not for the career rank either.
		{"no career kill", count(t, db, `SELECT p.career_kills FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = '0:91'`), 0},
		{"no kill in the name's counts", count(t, db, `SELECT COALESCE(SUM(n.kills), 0) FROM hlstats_PlayerNames n JOIN hlstats_PlayerUniqueIds u ON u.playerId = n.playerId WHERE u.uniqueId = '0:91'`), 0},
		{"frag stored", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags`), 1},
		{"location row", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Locations WHERE flag = 'de' AND country = 'Germany'`), 1},
		{"player location", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Data d JOIN hlstats_Players_Locations l ON l.id = d.location_id
			JOIN hlstats_PlayerUniqueIds u ON u.playerId = d.playerId WHERE u.uniqueId = '0:91'`), 1},
		{"live location", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats s JOIN hlstats_Players_Locations l ON l.id = s.cli_location_id`), 1},
		{"server location", count(t, db, `SELECT COUNT(*) FROM hlstats_Servers WHERE country_code = 'de' AND country_name = 'Germany'`), 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	hub.Close(context.Background())
	if err := store.RecordServerLoad(context.Background(), stats.Load{ServerID: 1, Time: 1792000000, Players: 3, MinPlayers: 1,
		MaxPlayers: 300, MapID: 7, CPU: 12, FPS: 66, Uptime: 70000}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_server_load WHERE timestamp = 1792000000 AND act_players = 3 AND min_players = 1
		AND max_players = 255 AND map = 7 AND cpu = 12 AND fps = 66 AND uptime = 65535`); n != 1 {
		t.Errorf("server load row not stored as expected (%d)", n)
	}
}

// stateGeo places 203.0.113.0/24 in Saxony, 198.51.100.0/24 in Munich and
// 192.0.2.0/24 in Nuremberg.
type stateGeo struct{}

func (stateGeo) Lookup(_ string, ip netip.Addr) (stats.Location, bool) {
	switch b := ip.As4(); {
	case ip.Is4() && b[0] == 203 && b[1] == 0 && b[2] == 113:
		return stats.Location{Flag: "de", Country: "Germany", City: "Falkenstein", State: "Saxony", Lat: 50.4779, Lng: 12.3713}, true
	case ip.Is4() && b[0] == 198 && b[1] == 51 && b[2] == 100:
		return stats.Location{Flag: "de", Country: "Germany", City: "Munich", State: "Bavaria", Lat: 48.1374, Lng: 11.5755}, true
	case ip.Is4() && b[0] == 192 && b[1] == 0 && b[2] == 2:
		return stats.Location{Flag: "de", Country: "Germany", City: "Nuremberg", State: "Bavaria", Lat: 49.4521, Lng: 11.0767}, true
	}
	return stats.Location{}, false
}

// A location row that exists without a state, or with a legacy region code
// from HLstatsX, gets the state of the next lookup; a stored name stays.
func TestIntegrationLocationState(t *testing.T) {
	db := testDB(t)
	addr := "203.0.113.136:27015"
	insertServer(t, db, addr, "css")
	for _, q := range []string{
		`INSERT INTO hlstats_Players_Locations (city, state, country, flag, lat, lng) VALUES ('Falkenstein', '', 'Germany', 'de', '50.4779', '12.3713')`,
		`INSERT INTO hlstats_Players_Locations (city, state, country, flag, lat, lng) VALUES ('Munich', 'Upper Bavaria', 'Germany', 'de', '48.1374', '11.5755')`,
		`INSERT INTO hlstats_Players_Locations (city, state, country, flag, lat, lng) VALUES ('Nuremberg', '02', 'Germany', 'de', '49.4521', '11.0767')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	hub := NewHub(store, set, &recorder{}, strictLog(t))
	hub.Geo = stateGeo{}
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{
		`"K<2><STEAM_1:0:91><>" connected, address "203.0.113.2:27005"`,
		`"M<3><STEAM_1:0:92><>" connected, address "198.51.100.7:27005"`,
		`"N<4><STEAM_1:0:93><>" connected, address "192.0.2.9:27005"`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	located := `SELECT COUNT(*) FROM hlstats_Players_Data d JOIN hlstats_Players_Locations l ON l.id = d.location_id`
	deadline := time.Now().Add(10 * time.Second)
	for count(t, db, located) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	hub.Close(context.Background())
	checks := []struct {
		name      string
		got, want int
	}{
		{"no new rows", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Locations`), 3},
		{"empty state filled", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Locations WHERE lat = '50.4779' AND state = 'Saxony'`), 1},
		{"legacy code replaced", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Locations WHERE lat = '49.4521' AND state = 'Bavaria'`), 1},
		{"stored state kept", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_Locations WHERE lat = '48.1374' AND state = 'Upper Bavaria'`), 1},
		{"players located", count(t, db, located), 3},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// RCON status alone fills the server and the live view.
func TestIntegrationStatus(t *testing.T) {
	db := testDB(t)
	insertServer(t, db, "203.0.113.136:27015", "css")
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	ctx := context.Background()
	servers, err := store.Servers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers %v %v", servers, err)
	}
	p := NewProcessor(store, set, servers[0], &recorder{}, strictLog(t))
	out := "hostname: Status Test\nmap     : de_dust2 at: 0 x, 0 y, 0 z\nplayers : 1 humans, 1 bots (32 max)\n" +
		"# userid name uniqueid connected ping loss state adr\n" +
		"#  2 \"Alice\" STEAM_0:1:4321 1:00:00 80 0 active 192.0.2.10:27005\n#  3 \"Bot Bob\" BOT active\n"
	st, err := ParseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := p.Status(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateServerPlayers(ctx); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want int
	}{
		{"act_players", count(t, db, `SELECT act_players FROM hlstats_Servers`), 2},
		{"max_players", count(t, db, `SELECT max_players FROM hlstats_Servers`), 32},
		{"act_map", count(t, db, `SELECT COUNT(*) FROM hlstats_Servers WHERE act_map = 'de_dust2' AND map_started = ?`, now.Unix()), 1},
		{"live rows", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats l JOIN hlstats_Servers s ON s.serverId = l.server_id WHERE l.player_hidden = 0`), 2},
		{"bot live", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE unique_id = 'BOT:Bot Bob' AND name = 'Bot Bob'`), 1},
		{"human ping", count(t, db, `SELECT ping FROM hlstats_Livestats WHERE unique_id = '1:4321'`), 80},
		{"human connected", count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE unique_id = '1:4321' AND connected = ?`, now.Add(-time.Hour).Unix()), 1},
		{"ping sample", count(t, db, `SELECT ping FROM hlstats_Events_Latency l JOIN hlstats_PlayerUniqueIds u ON u.playerId = l.playerId WHERE u.uniqueId = '1:4321'`), 80},
		{"average ping", count(t, db, `SELECT d.average_ping FROM hlstats_Players_Data d JOIN hlstats_PlayerUniqueIds u ON u.playerId = d.playerId WHERE u.uniqueId = '1:4321'`), 80},
		{"no bot samples", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Latency`), 1},
		{"players created", count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE uniqueId IN ('1:4321', 'BOT:Bot Bob')`), 2},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	// Status has no team: the next log line sets the bot's.
	if err := p.Handle(ctx, `"Bot Bob<3><BOT><CT>" killed "Alice<2><STEAM_0:1:4321><TERRORIST>" with "m4a1"`, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE unique_id = 'BOT:Bot Bob' AND team = 'CT'`); got != 1 {
		t.Error("bot team not set from the kill line")
	}
	// Alice leaves: gone from the live view after two polls without her.
	st.Players = st.Players[1:]
	for i := 1; i <= 2; i++ {
		if err := p.Status(ctx, st, now.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats`); got != 1 {
		t.Errorf("live rows after leaving: %d, want 1", got)
	}
	if got := count(t, db, `SELECT act_players FROM hlstats_Servers`); got != 1 {
		t.Errorf("act_players after leaving: %d, want 1", got)
	}
}

// Server info from A2S_INFO and status lands in hlstats_Servers and is read
// back with the server list.
func TestIntegrationServerInfo(t *testing.T) {
	db := testDB(t)
	insertServer(t, db, "203.0.113.137:27015", "css")
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	ctx := context.Background()
	servers, err := store.Servers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Info != (stats.ServerInfo{}) {
		t.Fatalf("servers %+v %v", servers, err)
	}
	p := NewProcessor(store, set, servers[0], &recorder{}, strictLog(t))
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "9540945", OS: OSLinux}); err != nil {
		t.Fatal(err)
	}
	st, err := ParseStatus("hostname: x\nversion : 9540945/24 9540945 insecure\nmap     : de_dust2\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Status(ctx, st, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	servers, err = store.Servers(ctx)
	if want := (stats.ServerInfo{Version: "9540945", OS: OSLinux, VAC: VACInsecure}); err != nil || servers[0].Info != want {
		t.Errorf("info %+v %v, want %+v", servers[0].Info, err, want)
	}
}

// A ranking cloned from css (another hlstats_Games code with copies of the
// css rows, as the web admin creates css1, css2, ...) is a game of its own:
// actions, weapons, teams, players and awards use its rows only.
func TestIntegrationClonedGame(t *testing.T) {
	db := testDB(t)
	loadSeed(t, db)
	id := count(t, db, `SELECT MAX(id) FROM hlstats_Games`) + 1 // explicit: the tinyint auto-increment runs out over many test runs
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO hlstats_Games (id, code, name, realgame, hidden) VALUES (%d, 'css3', 'Counter-Strike: Source - GunGame', 'css', '0')`, id),
		`INSERT INTO hlstats_Teams (game, code, name, hidden) SELECT 'css3', code, name, hidden FROM hlstats_Teams WHERE game = 'css'`,
		`INSERT INTO hlstats_Actions (game, code, reward_player, reward_team, team, description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions)
			SELECT 'css3', code, reward_player, reward_team, team, description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions FROM hlstats_Actions WHERE game = 'css'`,
		`INSERT INTO hlstats_Weapons (game, code, name, modifier) SELECT 'css3', code, name, modifier FROM hlstats_Weapons WHERE game = 'css'`,
		`INSERT INTO hlstats_Awards (awardType, game, code, name, verb) SELECT awardType, 'css3', code, name, verb FROM hlstats_Awards WHERE game = 'css'`,
		`UPDATE hlstats_Actions SET reward_player = 77 WHERE game = 'css3' AND code = 'Planted_The_Bomb'`,
	} {
		if _, err := db.Exec(db.Q(q)); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	addr := "198.51.100.3:27315"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css3"}})
	now := time.Now().UTC()
	for _, l := range []string{
		`"K<2><STEAM_1:0:81><Unassigned>" joined team "CT"`,
		`"V<3><STEAM_1:0:82><Unassigned>" joined team "TERRORIST"`,
		`"K<2><STEAM_1:0:81><CT>" killed "V<3><STEAM_1:0:82><TERRORIST>" with "ak47" (headshot)`,
		`"V<3><STEAM_1:0:82><TERRORIST>" triggered "Planted_The_Bomb"`,
		`"K<2><STEAM_1:0:81><CT>" triggered "gg_levelup"`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), now); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	checks := []struct {
		name      string
		got, want int
	}{
		{"players in css3", count(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css3'`), 2},
		{"players in css", count(t, db, `SELECT COUNT(*) FROM hlstats_Players WHERE game = 'css'`), 0},
		{"unique ids in css3", count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE game = 'css3'`), 2},
		{"css3 weapon kill", count(t, db, `SELECT kills FROM hlstats_Weapons WHERE game = 'css3' AND code = 'ak47'`), 1},
		{"css weapon untouched", count(t, db, `SELECT kills FROM hlstats_Weapons WHERE game = 'css' AND code = 'ak47'`), 0},
		{"no new weapon rows", count(t, db, `SELECT COUNT(*) FROM hlstats_Weapons WHERE game = 'css3'`), count(t, db, `SELECT COUNT(*) FROM hlstats_Weapons WHERE game = 'css'`)},
		{"css3 bomb reward", count(t, db, `SELECT e.bonus FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.game = 'css3' AND a.code = 'Planted_The_Bomb'`), 77},
		{"css3 gungame action", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.game = 'css3' AND a.code = 'gg_levelup'`), 1},
		{"css3 headshot action", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.game = 'css3' AND a.code = 'headshot'`), 1},
		{"no css actions", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.game = 'css'`), 0},
		{"css3 team bonus", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_TeamBonuses e JOIN hlstats_Actions a ON a.id = e.actionId WHERE a.game = 'css3'`), 1},
		{"no new team rows", count(t, db, `SELECT COUNT(*) FROM hlstats_Teams WHERE game = 'css3'`), count(t, db, `SELECT COUNT(*) FROM hlstats_Teams WHERE game = 'css'`)},
		{"history in css3", count(t, db, `SELECT COUNT(*) FROM hlstats_Players_History WHERE game = 'css3'`), 2},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	res, err := store.DailyAwards(context.Background(), now, "")
	if err != nil {
		t.Fatal(err)
	}
	won := map[string]string{}
	for _, r := range res {
		if r.WinnerID > 0 {
			won[r.Game+" "+r.Code] = fmt.Sprint(r.Count)
		}
	}
	if won["css3 ak47"] != "1" || won["css3 most_kills"] != "1" || len(won) == 0 {
		t.Errorf("css3 awards: %v", won)
	}
	for k := range won {
		if !strings.HasPrefix(k, "css3 ") {
			t.Errorf("award outside css3: %s", k)
		}
	}
}

// Kill-line positions on both databases, and the heatmap query of the web.
func TestIntegrationKillPositions(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.40:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}})
	for _, l := range []string{
		`"A<2><STEAM_1:0:61><CT>" [100 -200 30] killed "B<3><STEAM_1:1:62><TERRORIST>" [70 -130 3] with "weapon1"`,
		`"A<2><STEAM_1:0:61><CT>" [110 -190 30] killed "B<3><STEAM_1:1:62><TERRORIST>" [-70 130 3] with "weapon1"`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	rows, err := db.Query(db.Q(`SELECT FLOOR(loc_1 / 64.0) AS gx, FLOOR(loc_2 / 64.0) AS gy, type, COUNT(*) AS n
		FROM hlstats_Events_Locations WHERE map = (SELECT MIN(map) FROM hlstats_Events_Locations) GROUP BY 1, 2, 3 ORDER BY 3, 1, 2`))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var gx, gy float64
		var typ, n int
		if err := rows.Scan(&gx, &gy, &typ, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%g,%g=%d", typ, gx, gy, n))
	}
	if strings.Join(got, " ") != "1:1,-4=1 1:1,-3=1 2:-2,2=1 2:1,-3=1" {
		t.Errorf("heatmap bins %v", got)
	}
	// hlstats_Events_Frags.pos_*: from the kill line, and on CS:S from the
	// plugin's located line: as the plugin hooks player_death before the
	// engine logs the kill (with its headshot line first, %L with Steam3
	// ids), and after the kill line.
	css := "198.51.100.41:27015"
	hub2 := dbHub(t, db, &recorder{}, [][2]string{{css, "css"}})
	for _, l := range []string{
		`"C<4><STEAM_1:0:63><CT>" killed "D<5><STEAM_1:1:64><TERRORIST>" with "weapon1"`,
		`"C<4><STEAM_1:0:63><CT>" located on "kill" (position "11 -12 13") against "D<5><STEAM_1:1:64><TERRORIST>" (victim_position "14 15 -16")`,
		`"D<5><STEAM_1:1:64><TERRORIST>" killed "C<4><STEAM_1:0:63><CT>" with "weapon1"`,
		`"E<6><[U:1:130]><CT>" triggered "headshot"`,
		`"E<6><[U:1:130]><CT>" located on "kill" (position "-500 600 70") against "Bot Fay<7><BOT><TERRORIST>" (victim_position "-480 640 64")`,
		`"E<6><STEAM_1:0:65><CT>" killed "Bot Fay<7><BOT><TERRORIST>" with "weapon1" (headshot)`,
	} {
		if err := hub2.Dispatch(netip.MustParseAddrPort(css), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub2.Close(context.Background())
	checks := []struct {
		name      string
		got, want int
	}{
		{"kill-line positions", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE pos_x = 100 AND pos_y = -200 AND pos_z = 30 AND pos_victim_x = 70 AND pos_victim_y = -130 AND pos_victim_z = 3`), 1},
		{"plugin positions", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE pos_x = 11 AND pos_y = -12 AND pos_z = 13 AND pos_victim_x = 14 AND pos_victim_y = 15 AND pos_victim_z = -16`), 1},
		{"plugin positions before the kill line", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE pos_x = -500 AND pos_y = 600 AND pos_z = 70 AND pos_victim_x = -480 AND pos_victim_y = 640 AND pos_victim_z = 64 AND headshot = 1`), 1},
		{"no position", count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE pos_x IS NULL AND pos_victim_x IS NULL`), 1},
		{"frag heatmap bins", count(t, db, `SELECT COUNT(*) FROM (SELECT FLOOR(pos_victim_x / 64.0) AS gx, FLOOR(pos_victim_y / 64.0) AS gy, COUNT(*) AS n
			FROM hlstats_Events_Frags WHERE pos_victim_x IS NOT NULL GROUP BY 1, 2) b`), 4},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// !hideranking on a real database: hidden players leave the ranking, keep
// the setting when they come back, and a ban (2) wins over it.
func TestIntegrationHideRanking(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.42:27015"
	rec := &recorder{}
	hub := dbHub(t, db, rec, [][2]string{{addr, "css"}})
	send := func(l string) {
		t.Helper()
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	send(`"A<2><STEAM_1:0:71><CT>" killed "B<3><STEAM_1:1:72><TERRORIST>" with "weapon1"`)
	send(`"A<2><STEAM_1:0:71><CT>" say "!hideranking 1"`)
	send(`"A<2><STEAM_1:0:71><CT>" disconnected (reason "Disconnect")`)
	send(`"A<4><STEAM_1:0:71><>" connected, address "203.0.113.5:27005"`)
	hub.Close(context.Background())
	hide := func(uid string) int {
		return count(t, db, `SELECT p.hideranking FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = ?`, uid)
	}
	if h := hide("0:71"); h != 3 {
		t.Errorf("hideranking after !hideranking 1 and a reconnect: %d, want 3", h)
	}
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	if _, total, err := store.Rank(context.Background(), "css", 1000); err != nil || total != 1 {
		t.Errorf("ranked players %d (%v), want 1: the hidden player is not counted", total, err)
	}
	addr2 := "198.51.100.43:27015" // another server of the same game
	hub2 := dbHub(t, db, rec, [][2]string{{addr2, "css"}})
	for _, l := range []string{
		`"A<2><STEAM_1:0:71><CT>" disconnected (reason "VAC banned from secure server")`,
		`"A<5><STEAM_1:0:71><CT>" say "!hideranking 0"`,
	} {
		if err := hub2.Dispatch(netip.MustParseAddrPort(addr2), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub2.Close(context.Background())
	if h := hide("0:71"); h != 2 {
		t.Errorf("hideranking after a ban and !hideranking 0: %d, want 2", h)
	}
}

// Rounds, wins, losses and survived rounds on both databases.
func TestIntegrationSurvived(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.44:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}})
	for _, l := range []string{
		`World triggered "Round_Start"`,
		`"A<2><STEAM_1:0:81><CT>" killed "B<3><STEAM_1:1:82><TERRORIST>" with "weapon1"`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
		`World triggered "Round_Start"`,
		`"B<3><STEAM_1:1:82><TERRORIST>" killed "A<2><STEAM_1:0:81><CT>" with "weapon1"`,
		`Team "TERRORIST" triggered "Terrorists_Win" (CT "1") (T "1")`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	intType := "SIGNED" // CAST target for the float column survived
	if db.Postgres() {
		intType = "INTEGER"
	}
	q := func(uid, col string) int {
		return count(t, db, `SELECT CAST(p.`+col+` AS `+intType+`) FROM hlstats_Players p JOIN hlstats_PlayerUniqueIds u ON u.playerId = p.playerId WHERE u.uniqueId = ?`, uid)
	}
	for _, c := range []struct {
		uid, col string
		want     int
	}{
		{"0:81", "rounds", 2}, {"0:81", "wins", 1}, {"0:81", "losses", 1}, {"0:81", "survived_count", 1}, {"0:81", "survived", 50},
		{"1:82", "rounds", 2}, {"1:82", "survived_count", 1}, {"1:82", "survived", 50},
	} {
		if got := q(c.uid, c.col); got != c.want {
			t.Errorf("%s %s = %d, want %d", c.uid, c.col, got, c.want)
		}
	}
	if n := count(t, db, `SELECT SUM(survived_count) FROM hlstats_Players_History`); n != 2 {
		t.Errorf("history survived_count %d, want 2", n)
	}
}

// The history day is the day of the store's HistoryZone (the daily awards'
// zone), not the UTC day: a kill and a round at 23:30 UTC count for the
// next day at UTC+5, also the survived share written at the round end.
func TestIntegrationHistoryZone(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.45:27015"
	insertServer(t, db, addr, "css")
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	store.HistoryZone = time.FixedZone("UTC+5", 5*3600)
	hub := NewHub(store, set, &recorder{}, strictLog(t))
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC)
	for _, l := range []string{
		`World triggered "Round_Start"`,
		`"A<2><STEAM_1:0:91><CT>" killed "B<3><STEAM_1:1:92><TERRORIST>" with "weapon1"`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), at); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	// (The shutdown adds the play time at the real now, on that day's row.)
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Players_History WHERE eventTime = ?`, "2026-10-25"); n != 0 {
		t.Errorf("%d history rows on the UTC day 2026-10-25", n)
	}
	if n := count(t, db, `SELECT COALESCE(SUM(kills), 0) FROM hlstats_Players_History WHERE eventTime = ?`, "2026-10-26"); n != 1 {
		t.Errorf("history kills on 2026-10-26: %d, want 1", n)
	}
	if n := count(t, db, `SELECT COALESCE(SUM(survived_count), 0) FROM hlstats_Players_History WHERE eventTime = ?`, "2026-10-26"); n != 1 {
		t.Errorf("history survived_count on 2026-10-26: %d, want 1", n)
	}
}

// hlstats_Teams and hlstats_Roles: time, kills, deaths, rounds, wins,
// losses and survived (the web's teams page) on both databases.
func TestIntegrationTeamRoleStats(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.45:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}})
	t0 := time.Now().UTC()
	for _, l := range []struct {
		at   int
		line string
	}{
		{0, `"A<2><STEAM_1:0:91><Unassigned>" joined team "CT"`},
		{0, `"B<3><STEAM_1:1:92><Unassigned>" joined team "TERRORIST"`},
		{1, `"A<2><STEAM_1:0:91><CT>" changed role to "gsg9"`},
		{1, `"B<3><STEAM_1:1:92><TERRORIST>" changed role to "leet"`},
		{2, `World triggered "Round_Start"`},
		{10, `"A<2><STEAM_1:0:91><CT>" killed "B<3><STEAM_1:1:92><TERRORIST>" with "weapon1" (headshot)`},
		{20, `Team "CT" triggered "CTs_Win" (CT "1") (T "0")`},
		{61, `"A<2><STEAM_1:0:91><CT>" disconnected (reason "Disconnect")`},
		{121, `"B<3><STEAM_1:1:92><TERRORIST>" disconnected (reason "Disconnect")`},
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l.line), t0.Add(time.Duration(l.at)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	q := func(table, code, col string) int {
		return count(t, db, `SELECT `+col+` FROM `+table+` WHERE game = 'css' AND code = ?`, code)
	}
	for _, c := range []struct {
		table, code, col string
		want             int
	}{
		{"hlstats_Teams", "CT", "time_played", 61}, {"hlstats_Teams", "TERRORIST", "time_played", 121},
		{"hlstats_Teams", "CT", "kills", 1}, {"hlstats_Teams", "CT", "headshots", 1}, {"hlstats_Teams", "TERRORIST", "deaths", 1},
		{"hlstats_Teams", "CT", "rounds", 1}, {"hlstats_Teams", "CT", "wins", 1}, {"hlstats_Teams", "CT", "survived", 1},
		{"hlstats_Teams", "TERRORIST", "rounds", 1}, {"hlstats_Teams", "TERRORIST", "losses", 1}, {"hlstats_Teams", "TERRORIST", "survived", 0},
		{"hlstats_Roles", "gsg9", "time_played", 60}, {"hlstats_Roles", "leet", "time_played", 120},
		{"hlstats_Roles", "gsg9", "wins", 1}, {"hlstats_Roles", "gsg9", "rounds", 1}, {"hlstats_Roles", "gsg9", "kills", 1},
		{"hlstats_Roles", "leet", "losses", 1}, {"hlstats_Roles", "leet", "deaths", 1},
	} {
		if got := q(c.table, c.code, c.col); got != c.want {
			t.Errorf("%s %s %s = %d, want %d", c.table, c.code, c.col, got, c.want)
		}
	}
}

// act_players and the live rows follow the players really on the server on
// both databases: bots re-added after a 24/7 map restart without a
// disconnect line, doubled lines, a reconnect with a new userid, more bots
// than slots, and a collector restart in the middle of a map.
func TestIntegrationPlayerCount(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.46:27315"
	insertServer(t, db, addr, "css")
	if _, err := db.Exec(db.Q(`UPDATE hlstats_Servers SET max_players = 6 WHERE address = ?`), "198.51.100.46"); err != nil {
		t.Fatal(err)
	}
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	servers, err := store.Servers(context.Background())
	if err != nil || len(servers) != 1 || servers[0].MaxPlayers != 6 {
		t.Fatalf("servers %+v %v", servers, err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	newProc := func() *Processor { return NewProcessor(store, set, servers[0], &recorder{}, quiet) }
	ctx, t0 := context.Background(), time.Now().UTC()
	feed := func(p *Processor, at time.Time, lines ...string) {
		t.Helper()
		for _, l := range lines {
			if err := p.Handle(ctx, l, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(when string, want int) {
		t.Helper()
		id := servers[0].ID
		if n := count(t, db, `SELECT act_players FROM hlstats_Servers WHERE serverId = ?`, id); n != want {
			t.Errorf("%s: act_players %d, want %d", when, n, want)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Livestats WHERE server_id = ?`, id); n != want {
			t.Errorf("%s: %d live rows, want %d", when, n, want)
		}
	}
	p := newProc()
	feed(p, t0, `Started map "gg_simpsons" (CRC "1")`,
		`"A<2><STEAM_1:0:71><CT>" entered the game`, `"B<3><STEAM_1:0:72><TERRORIST>" entered the game`)
	for i := 0; i < 4; i++ {
		feed(p, t0.Add(time.Second), fmt.Sprintf(`"Old %d<%d><BOT><CT>" entered the game`, i, 10+i))
	}
	check("map start", 6)
	// GunGame winner: the same map again, new bots, the old ones without a
	// disconnect line.
	t1 := t0.Add(15 * time.Minute)
	feed(p, t1, `Loading map "gg_simpsons"`, `Started map "gg_simpsons" (CRC "1")`,
		`"A<2><STEAM_1:0:71><CT>" entered the game`, `"B<3><STEAM_1:0:72><TERRORIST>" entered the game`)
	for i := 0; i < 4; i++ {
		feed(p, t1.Add(time.Second), fmt.Sprintf(`"New %d<%d><BOT><CT>" entered the game`, i, 20+i))
	}
	check("map restart", 6)
	// bot_quota adds more bots than there are slots (old visits lost).
	for i := 4; i < 9; i++ {
		feed(p, t1.Add(time.Duration(i)*time.Second), fmt.Sprintf(`"New %d<%d><BOT><CT>" entered the game`, i, 20+i))
	}
	check("bots beyond the slots", 6)
	// Doubled lines and a reconnect with a new userid.
	for i := 0; i < 4; i++ {
		l := fmt.Sprintf(`L 10/23/2026 - 12:10:%02d: "A<2><STEAM_1:0:71><CT>" say "x%d"`, i, i)
		feed(p, t1.Add(time.Minute), l, l)
	}
	feed(p, t1.Add(2*time.Minute), `"A<2><STEAM_1:0:71><CT>" disconnected (reason "Disconnect")`,
		`"A<40><STEAM_1:0:71><>" connected, address "192.0.2.71:27005"`, `"A<40><STEAM_1:0:71><>" entered the game`)
	check("reconnect", 6)
	// The collector restarts in the middle of the map: the stale live rows
	// go, the players come back with their next lines.
	q := newProc()
	feed(q, t1.Add(3*time.Minute), `"A<40><STEAM_1:0:71><CT>" say "back"`, `"B<3><STEAM_1:0:72><TERRORIST>" say "back"`)
	check("restart", 2)
}

// A fresh schema (internal/schema) is complete: the schema check (read only, also
// for the collector's user) finds nothing missing on both databases.
func TestIntegrationSchemaCheck(t *testing.T) {
	db := testDB(t)
	missing, err := database.MissingSchema(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range missing {
		t.Errorf("fresh schema: %s", r)
	}
}

// Every event row carries the server it happened on (hlstats_Servers.serverId
// in server_id), on both databases: the web shows "latest events" per server.
func TestIntegrationEventServerID(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(db.Q(`INSERT INTO hlstats_Actions (game, code, reward_player, for_PlayerPlayerActions) VALUES ('css', 'domination', 2, '1')`)); err != nil {
		t.Fatal(err)
	}
	a, b := "198.51.100.47:27015", "198.51.100.48:27016"
	hub := dbHub(t, db, &recorder{}, [][2]string{{a, "css"}, {b, "css"}})
	at := time.Now().UTC()
	feed := func(addr string, lines ...string) {
		t.Helper()
		for _, l := range lines {
			at = at.Add(3 * time.Second) // a suicide right after a team change is not counted
			if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), at); err != nil {
				t.Fatal(err)
			}
		}
	}
	feed(a,
		`"K<2><STEAM_1:0:111><Unassigned>" entered the game`,
		`"K<2><STEAM_1:0:111><Unassigned>" joined team "CT"`,
		`"V<3><STEAM_1:0:112><Unassigned>" joined team "TERRORIST"`,
		`"T<4><STEAM_1:0:113><Unassigned>" joined team "CT"`,
		`"K<2><STEAM_1:0:111><CT>" killed "V<3><STEAM_1:0:112><TERRORIST>" with "weapon1"`,
		`"K<2><STEAM_1:0:111><CT>" killed "T<4><STEAM_1:0:113><CT>" with "weapon1"`,
		`"V<3><STEAM_1:0:112><TERRORIST>" committed suicide with "world"`,
		`"V<3><STEAM_1:0:112><TERRORIST>" triggered "Planted_The_Bomb"`,
		`"K<2><STEAM_1:0:111><CT>" triggered "domination" against "V<3><STEAM_1:0:112><TERRORIST>"`,
		`Team "CT" triggered "CTs_Win" (CT "1") (T "0")`,
		`"K<2><STEAM_1:0:111><CT>" disconnected (reason "Disconnect")`)
	feed(b, `"Z<5><STEAM_1:0:114><Unassigned>" entered the game`)
	hub.Close(context.Background())
	ids := map[string]int{}
	for _, addr := range []string{a, b} {
		ap := netip.MustParseAddrPort(addr)
		ids[addr] = count(t, db, `SELECT serverId FROM hlstats_Servers WHERE address = ? AND port = ?`, ap.Addr().String(), int(ap.Port()))
	}
	for _, tbl := range []string{"Frags", "Teamkills", "Suicides", "PlayerActions", "PlayerPlayerActions", "TeamBonuses", "Entries", "Disconnects", "ChangeTeam"} {
		on := count(t, db, `SELECT COUNT(*) FROM hlstats_Events_`+tbl+` WHERE server_id = ?`, ids[a])
		other := count(t, db, `SELECT COUNT(*) FROM hlstats_Events_`+tbl+` WHERE server_id <> ?`, ids[a])
		if tbl == "Entries" {
			other -= count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Entries WHERE server_id = ?`, ids[b]) // Z on b
		}
		if on == 0 || other != 0 {
			t.Errorf("hlstats_Events_%s: %d rows with server %d, %d with another server id", tbl, on, ids[a], other)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Entries WHERE server_id = ?`, ids[b]); n != 1 {
		t.Errorf("entries of server b: %d, want 1", n)
	}
}

// Counter-Strike 2 over HTTP: lines without "L " and with milliseconds,
// [U:1:n] SteamIDs, hit groups from the attacked lines (neck included),
// kills per weapon from the kill lines and the _off weapon names of CS2
// counted as the weapon.
func TestIntegrationCS2(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO hlstats_Games (id, code, name, realgame) VALUES (3, 'cs2', 'Counter-Strike 2', 'cs2')`); err != nil {
		t.Fatal(err)
	}
	addr := "198.51.100.9:27015"
	insertServer(t, db, addr, "cs2")
	set := settings.NewLive(settings.Defaults())
	hub := NewHub(stats.NewStore(db, set), set, &recorder{}, strictLog(t))
	hub.Source2Hits = true
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := `"Pat<2><[U:1:42]><%s>"`
	b := `"Bob<3><[U:1:43]><%s>"`
	f := func(p, team string) string { return fmt.Sprintf(p, team) }
	ts := "10/25/2026 - 12:00:00.250 - "
	lines := []string{
		`World triggered "Round_Start"`,
		f(a, "") + ` connected, address "192.0.2.41:27005"`,
		f(b, "") + ` connected, address "192.0.2.42:27005"`,
		f(a, "Unassigned") + ` joined team "CT"`,
		f(b, "Unassigned") + ` joined team "TERRORIST"`,
		f(a, "CT") + ` [10 20 30] attacked ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (damage "27") (damage_armor "3") (health "73") (armor "97") (hitgroup "neck")`,
		f(a, "CT") + ` [10 20 30] attacked ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (damage "73") (damage_armor "0") (health "0") (armor "97") (hitgroup "head")`,
		f(a, "CT") + ` [10 20 30] killed ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (headshot)`,
		`World triggered "Round_End"`,
	}
	var body []string
	for _, l := range lines {
		body = append(body, ts+l)
	}
	if n, err := hub.DispatchLines(netip.MustParseAddrPort(addr), body, time.Now().UTC()); err != nil || n != len(body) {
		t.Fatalf("dispatch: %d %v", n, err)
	}
	hub.Close(context.Background())
	if got := count(t, db, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds WHERE game = 'cs2'`); got != 2 {
		t.Errorf("cs2 players: %d, want 2", got)
	}
	m4 := `SELECT COALESCE(SUM(%s), 0) FROM hlstats_Players_Weapons w JOIN hlstats_Weapons x ON x.weaponId = w.weapon_id WHERE x.code = 'm4a1_silencer'`
	// Kills, headshots and the victim's death per weapon come from the kill
	// line; CS2 logs no shots, so shots and hits stay 0.
	for col, want := range map[string]int{"neck": 1, "head": 1, "damage": 100, "kills": 1, "headshots": 1, "deaths": 1, "shots": 0, "hits": 0} {
		if got := count(t, db, fmt.Sprintf(m4, "w."+col)); got != want {
			t.Errorf("m4a1_silencer %s: %d, want %d", col, got, want)
		}
	}
	if got := count(t, db, `SELECT COUNT(*) FROM hlstats_Events_Frags f JOIN hlstats_Weapons x ON x.weaponId = f.weapon WHERE x.code = 'm4a1_silencer' AND f.headshot = 1`); got != 1 {
		t.Errorf("headshot frags with m4a1_silencer: %d, want 1", got)
	}
}

// CS2 with the gameWE plugin: its weaponstats bring shots, hits, damage,
// kills, headshots and deaths per weapon, the attacked lines only the hit
// groups (nothing counted twice).
func TestIntegrationCS2Plugin(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO hlstats_Games (id, code, name, realgame) VALUES (3, 'cs2', 'Counter-Strike 2', 'cs2')`); err != nil {
		t.Fatal(err)
	}
	addr := "198.51.100.9:27015"
	insertServer(t, db, addr, "cs2")
	set := settings.NewLive(settings.Defaults())
	hub := NewHub(stats.NewStore(db, set), set, &recorder{}, strictLog(t))
	hub.Source2Hits = true
	if err := hub.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := `"Pat<2><[U:1:42]><%s>"`
	b := `"Bob<3><[U:1:43]><%s>"`
	f := func(p, team string) string { return fmt.Sprintf(p, team) }
	ts := "10/26/2026 - 12:00:00.250 - "
	lines := []string{
		`gameWE Plugin 0.2.0 (CounterStrikeSharp)`,
		`World triggered "Round_Start"`,
		f(a, "") + ` connected, address "192.0.2.41:27005"`,
		f(b, "") + ` connected, address "192.0.2.42:27005"`,
		f(a, "Unassigned") + ` joined team "CT"`,
		f(b, "Unassigned") + ` joined team "TERRORIST"`,
		f(a, "CT") + ` [10 20 30] attacked ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (damage "27") (damage_armor "3") (health "73") (armor "97") (hitgroup "neck")`,
		f(a, "CT") + ` [10 20 30] attacked ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (damage "73") (damage_armor "0") (health "0") (armor "97") (hitgroup "head")`,
		f(a, "CT") + ` [10 20 30] killed ` + f(b, "TERRORIST") + ` [40 50 60] with "m4a1_silencer_off" (headshot)`,
		f(b, "TERRORIST") + ` triggered "weaponstats" (weapon "m4a1_silencer") (shots "0") (hits "0") (kills "0") (headshots "0") (tks "0") (damage "0") (deaths "1")`,
		f(a, "CT") + ` triggered "weaponstats" (weapon "m4a1_silencer") (shots "5") (hits "2") (kills "1") (headshots "1") (tks "0") (damage "100") (deaths "0")`,
		`World triggered "Round_End"`,
	}
	var body []string
	for _, l := range lines {
		body = append(body, ts+l)
	}
	if n, err := hub.DispatchLines(netip.MustParseAddrPort(addr), body, time.Now().UTC()); err != nil || n != len(body) {
		t.Fatalf("dispatch: %d %v", n, err)
	}
	hub.Close(context.Background())
	m4 := `SELECT COALESCE(SUM(%s), 0) FROM hlstats_Players_Weapons w JOIN hlstats_Weapons x ON x.weaponId = w.weapon_id WHERE x.code = 'm4a1_silencer'`
	for col, want := range map[string]int{"neck": 1, "head": 1, "damage": 100, "kills": 1, "headshots": 1, "deaths": 1, "shots": 5, "hits": 2} {
		if got := count(t, db, fmt.Sprintf(m4, "w."+col)); got != want {
			t.Errorf("m4a1_silencer %s: %d, want %d", col, got, want)
		}
	}
}

// A chat line and a name in an old code page (Windows-1251, no UTF-8) are
// kept with U+FFFD for the bad bytes; the database refused the whole event
// before (MariaDB error 1366).
func TestIntegrationInvalidUTF8(t *testing.T) {
	db := testDB(t)
	addr := "198.51.100.9:27015"
	hub := dbHub(t, db, &recorder{}, [][2]string{{addr, "css"}})
	now := time.Now().UTC()
	for _, l := range []string{
		`Started map "de_dust2" (CRC "1")`,
		"\"\xc8\xe2\xe0\xed<2><STEAM_1:0:91><>\" entered the game",
		"\"\xc8\xe2\xe0\xed<2><STEAM_1:0:91><CT>\" say \"\xc3\xee \xe2 \xef\xf0\xe5\xee\xea\xf2\"",
	} {
		if err := hub.Dispatch(netip.MustParseAddrPort(addr), pkt(l), now); err != nil {
			t.Fatal(err)
		}
	}
	hub.Close(context.Background())
	var msg string
	if err := db.QueryRow(`SELECT message FROM hlstats_Events_Chat`).Scan(&msg); err != nil || msg != "� � �" {
		t.Errorf("chat message %q (%v), want %q", msg, err, "� � �")
	}
	var name string
	if err := db.QueryRow(`SELECT d.lastName FROM hlstats_Players_Data d JOIN hlstats_PlayerUniqueIds u USING (playerId)
		WHERE u.uniqueId = '0:91'`).Scan(&name); err != nil || name != "�" {
		t.Errorf("stored name %q (%v), want %q", name, err, "�")
	}
}
