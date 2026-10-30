// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

// gamewe db convert against throwaway databases (environment: see
// internal/database/migratetest_integration.go, MariaDB only) and the
// schema of HLstatsX:CE 1.7 (sql/install.sql of HLstatsX:CE, not part of
// this repository):
//
//	GAMEWE_TEST_CE_SCHEMA=/path/to/hlstatsx-ce-install.sql GAMEWE_TEST_MIGRATE_TYPE=mysql ... \
//	go test -tags integration -run Convert -v ./cmd/gamewe/
//
// It creates the databases conv_ce, conv_ce_lat (CE with test data),
// conv_gw, conv_gw_lat and conv_gw_run (gameWE) and leaves them for a look
// afterwards (each run creates them again). GAMEWE_TEST_CE_FRAGS sets the
// number of frags (default 3000).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
)

// ceAdmin opens database name as the admin; charset is the connection's
// character set (latin1 writes UTF-8 bytes as latin1: double encoding).
func ceAdmin(t *testing.T, name, charset string) *sql.DB {
	t.Helper()
	c := database.TestConfig(name, os.Getenv("GAMEWE_TEST_MIGRATE_ADMIN_USER"), os.Getenv("GAMEWE_TEST_MIGRATE_ADMIN_PASSWORD"))
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net = c.User, c.Password, "tcp"
	mc.Addr = fmt.Sprintf("%s:%d", c.Host, c.Port)
	mc.DBName = name
	mc.MultiStatements = true
	coll := map[string]string{"utf8mb4": "utf8mb4_unicode_ci", "latin1": "latin1_swedish_ci"}[charset]
	if err := mc.Apply(mysql.Charset(charset, coll)); err != nil {
		t.Fatal(err)
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q[:min(len(q), 200)], err)
	}
}

// createCE creates the CE database name with the schema of
// GAMEWE_TEST_CE_SCHEMA (its reference data included).
func createCE(t *testing.T, name string) *sql.DB {
	t.Helper()
	schemaFile := os.Getenv("GAMEWE_TEST_CE_SCHEMA")
	data, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	root := ceAdmin(t, "mysql", "utf8mb4")
	mustExec(t, root, "DROP DATABASE IF EXISTS `"+name+"`")
	mustExec(t, root, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	db := ceAdmin(t, name, "utf8mb4")
	rows, err := db.Query(string(data)) // results of OPTIMIZE and the like
	if err != nil {
		t.Fatalf("CE schema: %v", err)
	}
	for {
		for rows.Next() {
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("CE schema: %v", err)
	}
	rows.Close()
	return db
}

// The names of the test players: umlauts, other scripts, emoji, quotes.
var cePlayers = []string{"Jörg Müller", "Ζεύς", "李小龍", "😀 Smiley", `O'Brien "Q"`, "Ñandú", "Åsa Øster", "plain_ascii",
	"Łukasz Żółć", "Привет", "ßtraße", "Zoë <b>", "tab\tname", "René", "Ärger", "Mädchen"}

// fillCE writes the test data. Times are local (Europe/Berlin) around both
// changes of 2024; server 99 does not exist in hlstats_Servers.
func fillCE(t *testing.T, db *sql.DB, frags int) {
	t.Helper()
	r := rand.New(rand.NewSource(42))
	mustExec(t, db, `INSERT INTO hlstats_Games (code, name, hidden, realgame) VALUES ('css2', 'Counter-Strike: Source Zwei', '0', 'css')`)
	mustExec(t, db, `INSERT INTO hlstats_Servers (serverId, address, port, name, sortorder, game, publicaddress, statusurl, rcon_password, kills, players, act_map, max_players)
		VALUES (1, '192.0.2.10', 27015, 'Öffentlicher Server äöü', 2, 'css', 'cs.example.org:27015', NULL, 'change-me', 1000, 40, 'de_dust2', 24),
		       (2, '192.0.2.11', 27015, 'TF2 «Spaß»', 1, 'tf', '', 'http://example.org/s', '', 500, 20, 'ctf_2fort', 32),
		       (3, '192.0.2.12', 27016, 'Clone server', 3, 'css2', '', NULL, 'change-me-too', -5, 3, '', 16)`)
	mustExec(t, db, `INSERT INTO hlstats_Users (username, password, acclevel, playerId) VALUES ('Jörg', MD5('change-me'), 80, 1)`)
	mustExec(t, db, `INSERT INTO hlstats_HostGroups (id, pattern, name) VALUES (1, '%.example.net', 'Example ISP')`)
	mustExec(t, db, `INSERT INTO hlstats_Clans (clanId, tag, name, homepage, game) VALUES (1, '[ÄÖÜ]', 'Die Ümläute', '', 'css'), (2, '<TF>', 'Team Fortress «Clan»', 'http://example.org', 'tf')`)
	mustExec(t, db, `INSERT INTO hlstats_Teams (game, code, name, hidden) VALUES ('css2', 'CT', 'Counter-Terrorists', '0'), ('css2', 'TERRORIST', 'Terrorists', '0')`)
	mustExec(t, db, `INSERT INTO hlstats_Weapons (game, code, name, modifier, kills, headshots) VALUES ('css', 'laser_ce', 'CE-only laser', 1.5, 7, 2)`)
	mustExec(t, db, `UPDATE hlstats_Weapons SET kills = 111, headshots = 22 WHERE game = 'css' AND code = 'ak47'`)
	mustExec(t, db, `UPDATE hlstats_Roles SET kills = 33, deaths = 44 WHERE game = 'tf' AND code = 'medic'`)
	mustExec(t, db, `UPDATE hlstats_Actions SET count = 5 WHERE game = 'css' AND code = 'Planted_The_Bomb'`)
	mustExec(t, db, `INSERT INTO hlstats_Maps_Counts (game, map, kills, headshots) VALUES ('css', 'de_dust2', 900, 300), ('tf', 'ctf_2fort', 400, 0), ('css', 'de_ümlaut', 5, 1)`)

	games := []string{"css", "tf", "css2"}
	servers := map[string]int{"css": 1, "tf": 2, "css2": 3}
	maps := map[string][]string{"css": {"de_dust2", "cs_office", "de_ümlaut"}, "tf": {"ctf_2fort", "cp_badlands"}, "css2": {"de_dust2", "de_neu"}}
	weapons := map[string][]string{"css": {"ak47", "awp", "deagle", "laser_ce", "unknown_gun"}, "tf": {"scattergun", "rocketlauncher"}, "css2": {"ak47", "m4a1"}}
	teams := map[string][]string{"css": {"CT", "TERRORIST", "Spectator"}, "tf": {"Red", "Blue"}, "css2": {"CT", "TERRORIST"}}
	roles := []string{"medic", "scout", "civilian"}
	n := 40
	game := func(p int) string { return games[p%3] }
	var b strings.Builder
	for p := 1; p <= n; p++ {
		g := game(p)
		name := cePlayers[p%len(cePlayers)]
		if p > len(cePlayers) {
			name = fmt.Sprintf("%s %d", name, p)
		}
		lat, lng := "NULL", "NULL"
		if p%4 != 0 {
			lat, lng = fmt.Sprintf("%.4f", 48.1+float64(p%5)*0.11111), fmt.Sprintf("%.4f", 11.5+float64(p%5)*0.22222) // shared places
		}
		hide := []int{0, 0, 1, 3}[p%4]
		kills, deaths := r.Intn(5000), r.Intn(5000)
		b.Reset()
		fmt.Fprintf(&b, `INSERT INTO hlstats_Players (playerId, last_event, connection_time, lastName, lastAddress, city, state, country, flag, lat, lng, clan,
			kills, deaths, suicides, skill, shots, hits, teamkills, fullName, email, homepage, icq, game, hideranking, headshots, last_skill_change, kill_streak, death_streak, createdate)
			VALUES (%d, %d, %d, ?, '192.0.2.%d', ?, 'Bayern', 'Deutschland', 'DE', %s, %s, %d, %d, %d, %d, %d, %d, %d, %d, ?, NULL, NULL, %s, '%s', %d, %d, %d, %d, %d, %d)`,
			p, 1711900000+p*60, r.Intn(100000), p, lat, lng, p%3, kills, deaths, r.Intn(50), 800+r.Intn(1500), r.Intn(90000), r.Intn(30000), r.Intn(20),
			map[bool]string{true: "12345", false: "NULL"}[p%7 == 0], g, hide, r.Intn(1000), r.Intn(200)-100, r.Intn(30), r.Intn(10), map[bool]int{true: 0, false: 1700000000 + p}[p%5 == 0])
		city := []string{"München", "Köln", "Zürich", "Wien", "Göteborg"}[p%5]
		full := map[bool]any{true: "Vollständiger Näme", false: nil}[p%6 == 0]
		mustExec(t, db, b.String(), name, city, full)
		mustExec(t, db, `INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game) VALUES (?, ?, ?)`, p, fmt.Sprintf("STEAM_ID_PLACEHOLDER_%d", p), g)
		mustExec(t, db, `INSERT INTO hlstats_PlayerNames (playerId, name, lastuse, connection_time, numuses, kills, deaths, suicides, headshots, shots, hits)
			VALUES (?, ?, '2024-03-31 01:00:00', 100, 3, 10, 5, 1, 2, 300, 100)`, p, name)
		if p%3 == 0 {
			mustExec(t, db, `INSERT INTO hlstats_PlayerNames (playerId, name, lastuse, numuses, kills) VALUES (?, ?, '2024-10-27 02:30:00', 1, 1)`, p, name+" (alt)")
		}
		for d := 0; d < 3; d++ {
			mustExec(t, db, `INSERT INTO hlstats_Players_History (playerId, eventTime, connection_time, kills, deaths, suicides, skill, shots, hits, game, headshots, teamkills, kill_streak, death_streak, skill_change)
				VALUES (?, DATE_ADD('2024-03-30', INTERVAL ? DAY), 3600, ?, ?, 1, 1000, 100, 50, ?, 3, 1, 4, 2, ?)`, p, d, r.Intn(70000), r.Intn(100), g, r.Intn(40)-20)
		}
	}
	mustExec(t, db, `INSERT INTO hlstats_Players_History (playerId, eventTime, game) VALUES (1, NULL, 'css')`) // no date: left out

	// events: random local times on 2024-03-29 .. 04-01 and 10-25 .. 10-28,
	// plus the edge times of both changes
	times := []string{"2024-03-31 01:59:59", "2024-03-31 02:00:00", "2024-03-31 02:30:00", "2024-03-31 03:00:00",
		"2024-10-27 01:59:59", "2024-10-27 02:30:00", "2024-10-27 03:00:00"}
	when := func(i int) string {
		if i < len(times) {
			return times[i]
		}
		base := time.Date(2024, 3, 29, 0, 0, 0, 0, time.UTC)
		if i%2 == 1 {
			base = time.Date(2024, 10, 25, 0, 0, 0, 0, time.UTC)
		}
		return base.Add(time.Duration(r.Intn(4*86400)) * time.Second).Format(time.DateTime)
	}
	type ins struct {
		head string
		rows []string
	}
	batch := func(head string, rows []string) {
		for i := 0; i < len(rows); i += 500 {
			mustExec(t, db, head+strings.Join(rows[i:min(i+500, len(rows))], ","))
		}
	}
	q := func(s string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
	}
	pick := func(l []string) string { return l[r.Intn(len(l))] }
	player := func(g string) int { // a player of game g
		for {
			if p := 1 + r.Intn(n); game(p) == g {
				return p
			}
		}
	}
	var fr, tk, su, co, en, di, ct, cr, cn, ch, la, ad []string
	for i := 0; i < frags; i++ {
		g := games[i%3]
		srv := servers[g]
		if i%250 == 249 {
			srv = 99 // a server CE no longer has
		}
		tm := q(when(i))
		if i == frags-2 {
			tm = "NULL"
		}
		fr = append(fr, fmt.Sprintf("(%s, %d, %s, %d, %d, %s, %d, %s, %d, %d, %d)", tm, srv, q(pick(maps[g])), player(g), player(g), q(pick(weapons[g])), r.Intn(2),
			map[bool]string{true: "NULL", false: strconv.Itoa(r.Intn(4000) - 2000)}[i%10 == 0], r.Intn(4000)-2000, r.Intn(500), r.Intn(4000)-2000))
		if i%20 == 0 {
			tk = append(tk, fmt.Sprintf("(%s, %d, %s, %d, %d, %s)", tm, srv, q(pick(maps[g])), player(g), player(g), q(pick(weapons[g]))))
			su = append(su, fmt.Sprintf("(%s, %d, %s, %d, %s)", tm, srv, q(pick(maps[g])), player(g), q(pick([]string{"world", "worldspawn", pick(weapons[g])}))))
		}
		if i%10 == 0 {
			ip := fmt.Sprintf("192.0.2.%d", r.Intn(250))
			if i%70 == 0 {
				ip = "2001:db8::1"
			}
			co = append(co, fmt.Sprintf("(%s, %d, %s, %d, %s, %s, %s)", tm, srv, q(pick(maps[g])), player(g), q(ip), q("host-ü.example.net"), q("Example ISP")))
			en = append(en, fmt.Sprintf("(%s, %d, %s, %d)", tm, srv, q(pick(maps[g])), player(g)))
			di = append(di, fmt.Sprintf("(%s, %d, %s, %d)", tm, srv, q(pick(maps[g])), player(g)))
			ct = append(ct, fmt.Sprintf("(%s, %d, %s, %d, %s)", tm, srv, q(pick(maps[g])), player(g), q(pick(teams[g]))))
			la = append(la, fmt.Sprintf("(%s, %d, %s, %d, %d)", tm, srv, q(pick(maps[g])), player(g), r.Intn(300)))
			ch = append(ch, fmt.Sprintf("(%s, %d, %s, %d, %d, %s)", tm, srv, q(pick(maps[g])), player(g), r.Intn(3), q(pick([]string{"gg 😀", "Grüße aus Köln", "привет", `say "hi"; quit`, "nice shot!"}))))
		}
		if i%30 == 1 && g == "tf" {
			cr = append(cr, fmt.Sprintf("(%s, %d, %s, %d, %s)", tm, srv, q(pick(maps[g])), player(g), q(pick(roles))))
		}
		if i%50 == 0 {
			cn = append(cn, fmt.Sprintf("(%s, %d, %s, %d, %s, %s)", tm, srv, q(pick(maps[g])), player(g), q("old Näme"), q(pick(cePlayers))))
			ad = append(ad, fmt.Sprintf("(%s, %d, %s, %s, %s, %s)", tm, srv, q(pick(maps[g])), q("Mani Admin Plugin"), q("kick Ünknown"), q(pick(cePlayers))))
		}
	}
	batch(`INSERT INTO hlstats_Events_Frags (eventTime, serverId, map, killerId, victimId, weapon, headshot, pos_x, pos_y, pos_z, pos_victim_x) VALUES `, fr)
	batch(`INSERT INTO hlstats_Events_Teamkills (eventTime, serverId, map, killerId, victimId, weapon) VALUES `, tk)
	batch(`INSERT INTO hlstats_Events_Suicides (eventTime, serverId, map, playerId, weapon) VALUES `, su)
	batch(`INSERT INTO hlstats_Events_Connects (eventTime, serverId, map, playerId, ipAddress, hostname, hostgroup) VALUES `, co)
	batch(`INSERT INTO hlstats_Events_Entries (eventTime, serverId, map, playerId) VALUES `, en)
	batch(`INSERT INTO hlstats_Events_Disconnects (eventTime, serverId, map, playerId) VALUES `, di)
	batch(`INSERT INTO hlstats_Events_ChangeTeam (eventTime, serverId, map, playerId, team) VALUES `, ct)
	batch(`INSERT INTO hlstats_Events_ChangeRole (eventTime, serverId, map, playerId, role) VALUES `, cr)
	batch(`INSERT INTO hlstats_Events_ChangeName (eventTime, serverId, map, playerId, oldName, newName) VALUES `, cn)
	batch(`INSERT INTO hlstats_Events_Chat (eventTime, serverId, map, playerId, message_mode, message) VALUES `, ch)
	batch(`INSERT INTO hlstats_Events_Latency (eventTime, serverId, map, playerId, ping) VALUES `, la)
	batch(`INSERT INTO hlstats_Events_Admin (eventTime, serverId, map, type, message, playerName) VALUES `, ad)
	mustExec(t, db, `INSERT INTO hlstats_Events_Rcon (eventTime, serverId, map, type, remoteIp, password, command) VALUES ('2024-03-30 12:00:00', 1, 'de_dust2', 'GOOD', '192.0.2.200', 'change-me', 'status')`)

	// actions (ids of CE's own reference data), one CE action id that has
	// no action any more (99999: left out)
	var pa, ppa, tb []string
	for i := 0; i < frags/10; i++ {
		tm := q(when(i))
		pa = append(pa, fmt.Sprintf("(%s, 1, 'de_dust2', %d, (SELECT id FROM hlstats_Actions WHERE game = 'css' AND code = %s), %d)",
			tm, player("css"), q(pick([]string{"Planted_The_Bomb", "Defused_The_Bomb", "headshot"})), r.Intn(100000)-50000))
		ppa = append(ppa, fmt.Sprintf("(%s, 2, 'ctf_2fort', %d, %d, (SELECT id FROM hlstats_Actions WHERE game = 'tf' AND code = 'domination'), %d)", tm, player("tf"), player("tf"), r.Intn(10)))
		tb = append(tb, fmt.Sprintf("(%s, 1, 'de_dust2', %d, (SELECT MIN(id) FROM hlstats_Actions WHERE game = 'css' AND code = 'CTs_Win'), %d)", tm, player("css"), r.Intn(400)-200))
	}
	pa = append(pa, "('2024-03-30 12:00:00', 1, 'de_dust2', 1, 99999, 5)")
	batch(`INSERT INTO hlstats_Events_PlayerActions (eventTime, serverId, map, playerId, actionId, bonus) VALUES `, pa)
	batch(`INSERT INTO hlstats_Events_PlayerPlayerActions (eventTime, serverId, map, playerId, victimId, actionId, bonus) VALUES `, ppa)
	batch(`INSERT INTO hlstats_Events_TeamBonuses (eventTime, serverId, map, playerId, actionId, bonus) VALUES `, tb)

	// awards: winners of three days, the latest winner
	mustExec(t, db, `INSERT INTO hlstats_Players_Awards (awardTime, awardId, playerId, count, game)
		SELECT DATE_ADD('2024-03-30', INTERVAL d.n DAY), a.awardId, 1 + d.n, 3 + d.n, a.game
		FROM hlstats_Awards a JOIN (SELECT 0 AS n UNION SELECT 1 UNION SELECT 2) d WHERE a.game = 'css' AND a.code IN ('ak47', 'awp')`)
	mustExec(t, db, `UPDATE hlstats_Awards SET d_winner_id = 4, d_winner_count = 9 WHERE game = 'css' AND code = 'ak47'`)

	// server load and trend
	mustExec(t, db, `INSERT INTO hlstats_server_load (server_id, timestamp, act_players, min_players, max_players, map, uptime, fps) VALUES
		(1, 1711846800, 10, 2, 24, 'de_dust2', '3600', '66.7'), (1, 1711847100, 12, 2, 24, 'de_ümlaut', 'n/a', '-'),
		(2, 1711846800, 5, 0, 32, NULL, '120', '100'), (99, 1711846800, 1, 0, 8, 'de_dust2', '1', '1')`)
	mustExec(t, db, `INSERT INTO hlstats_Trend (timestamp, game, players, kills, headshots, servers, act_slots, max_slots) VALUES
		(1711846800, 'css', 40, 1000, 300, 1, 10, 24), (1711846800, 'tf', 20, 500, 0, 1, 5, 32)`)
}

// latin1Only replaces the text of col that latin1 cannot store with repl
// (an SQL expression); key identifies a row (an SQL expression).
func latin1Only(t *testing.T, db *sql.DB, table, key, col, repl string) {
	t.Helper()
	rows, err := db.Query("SELECT " + key + ", " + col + " FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		for _, r := range v {
			if r > 0xff {
				bad = append(bad, k)
				break
			}
		}
	}
	rows.Close()
	for _, k := range bad {
		mustExec(t, db, "UPDATE "+table+" SET "+col+" = "+repl+" WHERE "+key+" = ?", k)
	}
}

// checksums is CHECKSUM TABLE of every table of the CE database.
func checksums(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string]string{}
	for _, tb := range tables {
		var name string
		var sum sql.NullString
		if err := db.QueryRow("CHECKSUM TABLE `"+tb+"` EXTENDED").Scan(&name, &sum); err != nil {
			t.Fatal(err)
		}
		out[tb] = sum.String
	}
	return out
}

func one[T any](t *testing.T, db *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func convertTarget(t *testing.T, ctx context.Context, name, ce string) (config.Database, *sql.DB) {
	t.Helper()
	if err := database.CreateTestDatabase(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	root := ceAdmin(t, "mysql", "utf8mb4")
	mustExec(t, root, "GRANT SELECT ON `"+ce+"`.* TO 'gamewe'@'%'")
	d := database.TestConfig(name, "gamewe", database.TestPassword("gamewe"))
	d.AutoMigrate = true
	return d, ceAdmin(t, name, "utf8mb4")
}

func TestConvertHLstatsXCE(t *testing.T) {
	if database.MigrateTestEnv() != config.MySQL || os.Getenv("GAMEWE_TEST_CE_SCHEMA") == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* (mysql) and GAMEWE_TEST_CE_SCHEMA (HLstatsX:CE sql/install.sql) to run")
	}
	ctx := context.Background()
	frags := 3000
	if n, err := strconv.Atoi(os.Getenv("GAMEWE_TEST_CE_FRAGS")); err == nil && n > 0 {
		frags = n
	}
	ce := createCE(t, "conv_ce")
	fillCE(t, ce, frags)
	before := checksums(t, ce)
	bin := buildGamewe(t)
	d, gdb := convertTarget(t, ctx, "conv_gw", "conv_ce")
	cfg := writeConfig(t, d)

	// the plan only: nothing changes
	out, code := gw(t, bin, "db", "convert", "-config", cfg, "-from", "conv_ce", "-source-timezone", "Europe/Berlin", "-n")
	expect(t, out, code, 0, "source:      conv_ce", "HLstatsX:CE 1.7.0", "new: the gameWE schema", "Europe/Berlin",
		"hlstats_Events_Frags -> hlstats_Events_Frags", "hlstats_Events_Rcon", "dry run: nothing changed")
	if n := one[int](t, gdb, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()`); n != 0 {
		t.Fatalf("dry run created %d tables", n)
	}
	// no terminal, no -yes: refused
	out, code = gw(t, bin, "db", "convert", "-config", cfg, "-from", "conv_ce")
	expect(t, out, code, 1, "use --force")

	start := time.Now()
	out, code = gw(t, bin, "db", "convert", "-config", cfg, "-from", "conv_ce", "-source-timezone", "Europe/Berlin", "-yes")
	expect(t, out, code, 0, "creating the gameWE schema", "converting ...", "rows in CE and in gameWE:")
	t.Logf("convert of %d frags took %s", frags, time.Since(start))
	if strings.Contains(out, "(differs)") {
		// only the events of server 99 and of the unknown action may be missing
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "(differs)") && !strings.Contains(l, "hlstats_Events_") && !strings.Contains(l, "hlstats_server_load") {
				t.Errorf("rows differ: %s", l)
			}
		}
	}

	// the CE database is unchanged
	if after := checksums(t, ce); fmt.Sprint(after) != fmt.Sprint(before) {
		for k, v := range before {
			if after[k] != v {
				t.Errorf("CE table %s changed", k)
			}
		}
	}

	// rows: everything but the events of server 99 / action 99999
	for _, c := range []struct{ ce, gw string }{
		{`SELECT COUNT(*) FROM hlstats_Players`, `SELECT COUNT(*) FROM hlstats_Players`},
		{`SELECT COUNT(*) FROM hlstats_Players`, `SELECT COUNT(*) FROM hlstats_Players_Data`},
		{`SELECT COUNT(*) FROM hlstats_PlayerNames`, `SELECT COUNT(*) FROM hlstats_PlayerNames`},
		{`SELECT COUNT(*) FROM hlstats_PlayerUniqueIds`, `SELECT COUNT(*) FROM hlstats_PlayerUniqueIds`},
		{`SELECT COUNT(*) FROM hlstats_Players_History WHERE eventTime IS NOT NULL`, `SELECT COUNT(*) FROM hlstats_Players_History`},
		{`SELECT COUNT(*) FROM hlstats_Events_Frags WHERE serverId <> 99`, `SELECT COUNT(*) FROM hlstats_Events_Frags`},
		{`SELECT COUNT(*) FROM hlstats_Events_Connects WHERE serverId <> 99`, `SELECT COUNT(*) FROM hlstats_Events_Connects`},
		{`SELECT COUNT(*) FROM hlstats_Events_PlayerActions WHERE actionId <> 99999`, `SELECT COUNT(*) FROM hlstats_Events_PlayerActions`},
		{`SELECT COUNT(*) FROM hlstats_Players_Awards`, `SELECT COUNT(*) FROM hlstats_Awards_History`},
		{`SELECT COUNT(*) FROM hlstats_server_load WHERE server_id <> 99`, `SELECT COUNT(*) FROM hlstats_server_load`},
		{`SELECT SUM(kills) FROM hlstats_Players`, `SELECT SUM(kills) FROM hlstats_Players`},
		{`SELECT COUNT(DISTINCT CONCAT(lat, ',', lng)) FROM hlstats_Players WHERE lat IS NOT NULL`, `SELECT COUNT(*) FROM hlstats_Players_Locations`},
	} {
		if a, b := one[int64](t, ce, c.ce), one[int64](t, gdb, c.gw); a != b {
			t.Errorf("%s = %d, gameWE %s = %d", c.ce, a, c.gw, b)
		}
	}
	// Not copied, and gameWE has no such tables.
	if n := one[int64](t, gdb, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME IN ('hlstats_Events_Rcon', 'hlstats_Events_Admin', 'hlstats_HostGroups', 'hlstats_Trend')`); n != 0 {
		t.Errorf("%d tables gameWE has no longer are in the converted database", n)
	}

	// samples
	for _, name := range cePlayers {
		if n := one[int](t, gdb, `SELECT COUNT(*) FROM hlstats_PlayerNames WHERE BINARY name = BINARY ?`, name); n == 0 {
			t.Errorf("name %q missing", name)
		}
	}
	if s := one[string](t, gdb, `SELECT name FROM hlstats_Servers WHERE serverId = 1`); s != "Öffentlicher Server äöü" {
		t.Errorf("server name %q", s)
	}
	if s := one[string](t, gdb, `SELECT CONCAT(display_id, ' ', rcon_password, ' ', kills) FROM hlstats_Servers WHERE serverId = 3`); s != "3 change-me-too 0" {
		t.Errorf("server 3: %q", s)
	}
	// local times (Europe/Berlin) -> UTC, the edge times as MariaDB converts them
	for local, want := range map[string]int64{"2024-03-31 01:59:59": 1711846799, "2024-03-31 02:30:00": 1711846800, "2024-03-31 03:00:00": 1711846800,
		"2024-10-27 01:59:59": 1729987199, "2024-10-27 02:30:00": 1729989000, "2024-10-27 03:00:00": 1729994400} {
		if n := one[int](t, gdb, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE timestamp_id = ?`, want); n == 0 {
			t.Errorf("no frag at %d (%s Europe/Berlin)", want, local)
		}
	}
	if n := one[int](t, gdb, `SELECT COUNT(*) FROM hlstats_Events_Frags WHERE timestamp_id = 0`); n != 1 {
		t.Errorf("%d frags without time, want 1 (eventTime NULL)", n)
	}
	// every frag: game, server, map, weapon as in CE (by the codes)
	if n := one[int](t, gdb, `SELECT COUNT(*) FROM hlstats_Events_Frags f LEFT JOIN hlstats_Maps m ON m.id = f.map LEFT JOIN hlstats_Weapons w ON w.weaponId = f.weapon
		WHERE m.id IS NULL OR w.weaponId IS NULL OR f.game_id = 0`); n != 0 {
		t.Errorf("%d frags without map, weapon or game", n)
	}
	cmp := func(what, ceQ, gwQ string) {
		t.Helper()
		a, b := one[string](t, ce, ceQ), one[string](t, gdb, gwQ)
		if a != b {
			t.Errorf("%s: CE %s, gameWE %s", what, a, b)
		}
	}
	cmp("frags per weapon", `SELECT GROUP_CONCAT(n ORDER BY k SEPARATOR ',') FROM (SELECT CONCAT(s.game, '/', e.weapon, '/', e.map) AS k, COUNT(*) AS n FROM hlstats_Events_Frags e JOIN hlstats_Servers s USING (serverId) GROUP BY 1) x`,
		`SELECT GROUP_CONCAT(n ORDER BY k SEPARATOR ',') FROM (SELECT CONCAT(g.code, '/', w.code, '/', m.map) AS k, COUNT(*) AS n FROM hlstats_Events_Frags e JOIN hlstats_Games g ON g.id = e.game_id
			JOIN hlstats_Weapons w ON w.weaponId = e.weapon JOIN hlstats_Maps m ON m.id = e.map GROUP BY 1) x`)
	cmp("frag positions", `SELECT CONCAT(SUM(killerId), '/', SUM(victimId), '/', SUM(headshot), '/', COUNT(pos_x), '/', SUM(pos_x), '/', SUM(pos_victim_x)) FROM hlstats_Events_Frags WHERE serverId <> 99`,
		`SELECT CONCAT(SUM(killerId), '/', SUM(victimId), '/', SUM(headshot), '/', COUNT(pos_x), '/', SUM(pos_x), '/', SUM(pos_victim_x)) FROM hlstats_Events_Frags`)
	cmp("team changes", `SELECT GROUP_CONCAT(n ORDER BY k) FROM (SELECT CONCAT(s.game, '/', e.team) AS k, COUNT(*) AS n FROM hlstats_Events_ChangeTeam e JOIN hlstats_Servers s USING (serverId) GROUP BY 1) x`,
		`SELECT GROUP_CONCAT(n ORDER BY k) FROM (SELECT CONCAT(g.code, '/', t.code) AS k, COUNT(*) AS n FROM hlstats_Events_ChangeTeam e JOIN hlstats_Games g ON g.id = e.game_id JOIN hlstats_Teams t ON t.teamId = e.team GROUP BY 1) x`)
	cmp("role changes", `SELECT GROUP_CONCAT(n ORDER BY k) FROM (SELECT e.role AS k, COUNT(*) AS n FROM hlstats_Events_ChangeRole e JOIN hlstats_Servers s USING (serverId) GROUP BY 1) x`,
		`SELECT GROUP_CONCAT(n ORDER BY k) FROM (SELECT r.code AS k, COUNT(*) AS n FROM hlstats_Events_ChangeRole e JOIN hlstats_Roles r ON r.roleId = e.role GROUP BY 1) x`)
	cmp("player actions", `SELECT GROUP_CONCAT(n ORDER BY k) FROM (SELECT a.code AS k, COUNT(*) AS n, SUM(e.bonus) FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId GROUP BY 1) x`,
		`SELECT GROUP_CONCAT(n ORDER BY k) FROM (SELECT a.code AS k, COUNT(*) AS n FROM hlstats_Events_PlayerActions e JOIN hlstats_Actions a ON a.id = e.actionId GROUP BY 1) x`)
	cmp("bonus clamped", `SELECT SUM(LEAST(GREATEST(bonus, -32768), 32767)) FROM hlstats_Events_PlayerActions WHERE actionId <> 99999`, `SELECT SUM(bonus) FROM hlstats_Events_PlayerActions`)
	cmp("ipv4", `SELECT SUM(IFNULL(INET_ATON(IF(ipAddress REGEXP '^[0-9]+([.][0-9]+){3}$', ipAddress, NULL)), 0)) FROM hlstats_Events_Connects WHERE serverId <> 99`, `SELECT SUM(ipAddress) FROM hlstats_Events_Connects`)
	cmp("chat", `SELECT GROUP_CONCAT(DISTINCT message ORDER BY message SEPARATOR '|') FROM hlstats_Events_Chat`, `SELECT GROUP_CONCAT(DISTINCT message ORDER BY message SEPARATOR '|') FROM hlstats_Events_Chat`)
	cmp("players", `SELECT GROUP_CONCAT(CONCAT_WS('/', playerId, kills, deaths, skill, teamkills, IF(hideranking = 3, 0, hideranking), headshots) ORDER BY playerId) FROM hlstats_Players`,
		`SELECT GROUP_CONCAT(CONCAT_WS('/', playerId, kills, deaths, skill, team_kills, hideranking, headshots) ORDER BY playerId) FROM hlstats_Players`)
	cmp("player data", `SELECT GROUP_CONCAT(CONCAT_WS('/', playerId, lastName, IF(createdate > 0, createdate, last_event), IF(lat IS NULL, '', city)) ORDER BY playerId SEPARATOR '|') FROM hlstats_Players`,
		`SELECT GROUP_CONCAT(CONCAT_WS('/', d.playerId, d.lastName, d.first_connect, IFNULL(l.city, '')) ORDER BY d.playerId SEPARATOR '|') FROM hlstats_Players_Data d LEFT JOIN hlstats_Players_Locations l ON l.id = d.location_id`)
	cmp("game_id of players", `SELECT GROUP_CONCAT(game ORDER BY playerId) FROM hlstats_Players`,
		`SELECT GROUP_CONCAT(g.code ORDER BY p.playerId) FROM hlstats_Players p JOIN hlstats_Games g ON g.id = p.game_id`)
	cmp("weapon counters", `SELECT CONCAT(kills, '/', headshots) FROM hlstats_Weapons WHERE game = 'css' AND code = 'ak47'`, `SELECT CONCAT(kills, '/', headshots) FROM hlstats_Weapons WHERE game = 'css' AND code = 'ak47'`)
	cmp("CE-only weapon", `SELECT CONCAT(name, '/', modifier) FROM hlstats_Weapons WHERE code = 'laser_ce'`, `SELECT CONCAT(name, '/', modifier) FROM hlstats_Weapons WHERE code = 'laser_ce'`)
	cmp("role counters", `SELECT CONCAT(kills, '/', deaths) FROM hlstats_Roles WHERE game = 'tf' AND code = 'medic'`, `SELECT CONCAT(kills, '/', deaths) FROM hlstats_Roles WHERE game = 'tf' AND code = 'medic'`)
	cmp("award winners", `SELECT GROUP_CONCAT(CONCAT_WS('/', a.code, w.awardTime, w.playerId, w.count) ORDER BY a.code, w.awardTime) FROM hlstats_Players_Awards w JOIN hlstats_Awards a USING (awardId)`,
		`SELECT GROUP_CONCAT(CONCAT_WS('/', a.code, w.date, w.winner_id, w.winner_count) ORDER BY a.code, w.date) FROM hlstats_Awards_History w JOIN hlstats_Awards a ON a.awardId = w.award_id`)
	cmp("latest winner", `SELECT CONCAT(d_winner_id, '/', d_winner_count) FROM hlstats_Awards WHERE game = 'css' AND code = 'ak47'`, `SELECT CONCAT(d_winner_id, '/', d_winner_count) FROM hlstats_Awards WHERE game = 'css' AND code = 'ak47'`)
	cmp("css2 game", `SELECT CONCAT(name, '/', realgame) FROM hlstats_Games WHERE code = 'css2'`, `SELECT CONCAT(name, '/', realgame) FROM hlstats_Games WHERE code = 'css2'`)
	cmp("server load", `SELECT GROUP_CONCAT(CONCAT_WS('/', l.server_id, IFNULL(l.map, '-'), LEAST(IF(uptime REGEXP '^[0-9]+$', CAST(uptime AS UNSIGNED), 0), 65535), LEAST(IF(fps REGEXP '^[0-9]+([.][0-9]+)?$', ROUND(fps), 0), 65535)) ORDER BY l.server_id, l.timestamp) FROM hlstats_server_load l WHERE server_id <> 99`,
		`SELECT GROUP_CONCAT(CONCAT_WS('/', l.server_id, IFNULL(m.map, '-'), uptime, fps) ORDER BY l.server_id, l.timestamp) FROM hlstats_server_load l LEFT JOIN hlstats_Maps m ON m.id = l.map`)

	// the gameWE database is accepted: up to date, check, collector starts
	out, code = gw(t, bin, "db", "migrate", "-config", cfg, "-n")
	expect(t, out, code, 0, "The database is up to date.")
	out, code = gw(t, bin, "check", "-config", cfg)
	expect(t, out, code, 0, "schema:    ok")
	runStarts(t, bin, cfg)
	// a second convert is refused: not new
	mustExec(t, gdb, `UPDATE hlstats_Options SET value = '0' WHERE keyname = 'gamewe_heartbeat'`)
	out, code = gw(t, bin, "db", "convert", "-config", cfg, "-from", "conv_ce", "-yes")
	expect(t, out, code, 1, "is not new", "hlstats_Players has 40 rows")
	// db migrate on CE points to db convert
	ceCfg := writeConfig(t, database.TestConfig("conv_ce", "gamewe", database.TestPassword("gamewe")))
	out, code = gw(t, bin, "db", "migrate", "-config", ceCfg, "-n")
	expect(t, out, code, 1, "gamewe db convert -from")
	// the source as the target: refused
	out, code = gw(t, bin, "db", "convert", "-config", ceCfg, "-from", "conv_ce", "-n")
	expect(t, out, code, 1, "the source is the gameWE database itself")
	if after := checksums(t, ce); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Error("CE database changed")
	}
}

// A collector running on the gameWE database: convert refuses, nothing is
// written.
func TestConvertRefusesRunningCollector(t *testing.T) {
	if database.MigrateTestEnv() != config.MySQL || os.Getenv("GAMEWE_TEST_CE_SCHEMA") == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* (mysql) and GAMEWE_TEST_CE_SCHEMA to run")
	}
	ctx := context.Background()
	ce := createCE(t, "conv_ce_run")
	fillCE(t, ce, 100)
	bin := buildGamewe(t)
	d, gwdb := convertTarget(t, ctx, "conv_gw_run", "conv_ce_run")
	cfg := writeConfig(t, d)
	stop := startRun(t, bin, cfg) // creates the schema, holds the collector lock
	out, code := gw(t, bin, "db", "convert", "-config", cfg, "-from", "conv_ce_run", "-yes")
	expect(t, out, code, 1, "a collector is running on this host:", "then convert", rerunLine(bin, "db", "convert", "-config", cfg, "-from", "conv_ce_run", "-yes"))
	stop()
	if n := one[int](t, gwdb, `SELECT COUNT(*) FROM hlstats_Players`); n != 0 {
		t.Errorf("%d players written", n)
	}
}

// An old CE installation: latin1 tables with UTF-8 stored as latin1
// (double encoded) and genuine latin1 text; names equal in
// utf8mb4_unicode_ci are merged. The time zone is the server's (UTC).
func TestConvertLatin1(t *testing.T) {
	if database.MigrateTestEnv() != config.MySQL || os.Getenv("GAMEWE_TEST_CE_SCHEMA") == "" {
		t.Skip("set GAMEWE_TEST_MIGRATE_* (mysql) and GAMEWE_TEST_CE_SCHEMA to run")
	}
	ctx := context.Background()
	ce := createCE(t, "conv_ce_lat")
	fillCE(t, ce, 200)
	// latin1 has no Greek, Cyrillic, CJK or emoji: such text did not exist in these installations
	latin1Only(t, ce, "hlstats_PlayerNames", "CONCAT(playerId, '/', name)", "name", "CONCAT('Spieler ', playerId, IF(name LIKE '%(alt)', ' (alt)', ''))")
	latin1Only(t, ce, "hlstats_Players", "playerId", "lastName", "CONCAT('Spieler ', playerId)")
	latin1Only(t, ce, "hlstats_Events_Chat", "id", "message", "'gg'")
	for _, tb := range []string{"hlstats_PlayerNames", "hlstats_Players", "hlstats_Events_Chat", "hlstats_Clans"} {
		mustExec(t, ce, "ALTER TABLE "+tb+" CONVERT TO CHARACTER SET latin1 COLLATE latin1_swedish_ci")
	}
	mustExec(t, ce, `DELETE FROM hlstats_PlayerNames WHERE playerId IN (1, 2)`)
	mustExec(t, ce, `UPDATE hlstats_Players SET lastName = 'René' WHERE playerId = 2`)                           // genuine latin1 (utf8mb4 connection)
	mustExec(t, ce, `INSERT INTO hlstats_PlayerNames (playerId, name, numuses, kills) VALUES (2, 'René', 1, 1)`) // genuine latin1
	lat := ceAdmin(t, "conv_ce_lat", "latin1")                                                                   // UTF-8 bytes stored as latin1, as old CE daemons did
	mustExec(t, lat, `INSERT INTO hlstats_PlayerNames (playerId, name, numuses, kills) VALUES (1, 'Jörg Müller', 2, 10), (1, 'Jorg Muller', 3, 5)`)
	mustExec(t, lat, `UPDATE hlstats_Players SET lastName = 'Jörg Müller' WHERE playerId = 1`)
	mustExec(t, lat, `INSERT INTO hlstats_Events_Chat (eventTime, serverId, map, playerId, message) VALUES ('2024-03-31 02:30:00', 1, 'de_dust2', 1, 'Grüße €')`)
	if s := one[string](t, ce, `SELECT lastName FROM hlstats_Players WHERE playerId = 1`); s != "JÃ¶rg MÃ¼ller" {
		t.Fatalf("not double encoded: %q", s)
	}
	before := checksums(t, ce)
	bin := buildGamewe(t)
	d, gwdb := convertTarget(t, ctx, "conv_gw_lat", "conv_ce_lat")
	cfg := writeConfig(t, d)
	out, code := gw(t, bin, "db", "convert", "-config", cfg, "-from", "conv_ce_lat", "-yes")
	expect(t, out, code, 0, "latin1_swedish_ci (4 tables)", "player names look double-encoded (MÃ¼ller) and are repaired",
		"SYSTEM = UTC", "player names merged")
	for q, want := range map[string]string{
		`SELECT lastName FROM hlstats_Players_Data WHERE playerId = 1`:                                    "Jörg Müller",
		`SELECT lastName FROM hlstats_Players_Data WHERE playerId = 2`:                                    "René",
		`SELECT GROUP_CONCAT(name, '=', numuses, '/', kills) FROM hlstats_PlayerNames WHERE playerId = 1`: "Jörg Müller=5/15",
		`SELECT GROUP_CONCAT(name) FROM hlstats_PlayerNames WHERE playerId = 2`:                           "René",
		`SELECT message FROM hlstats_Events_Chat WHERE message LIKE '%€'`:                                 "Grüße €",
		`SELECT name FROM hlstats_Clans WHERE clanId = 1`:                                                 "Die Ümläute",
		`SELECT CAST(timestamp_id AS CHAR) FROM hlstats_Events_Chat WHERE message LIKE '%€'`:              "1711852200", // 02:30 UTC
	} {
		if got := one[string](t, gwdb, q); got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
	if after := checksums(t, ce); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Error("CE database changed")
	}
	// -mojibake keep takes the text as it is
	d2, gw2 := convertTarget(t, ctx, "conv_gw_lat2", "conv_ce_lat")
	out, code = gw(t, bin, "db", "convert", "-config", writeConfig(t, d2), "-from", "conv_ce_lat", "-mojibake", "keep", "-yes")
	expect(t, out, code, 0)
	if got := one[string](t, gw2, `SELECT lastName FROM hlstats_Players_Data WHERE playerId = 1`); got != "JÃ¶rg MÃ¼ller" {
		t.Errorf("keep: %q", got)
	}
}
