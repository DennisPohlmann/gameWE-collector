// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

// Integration test of a database freshly created with schema/init/init-db.sh
// (MariaDB / MySQL) and schema/postgres/init/init-db.sh (PostgreSQL). It
// connects as the web users, inserts a few rows as gamewe_web_admin and
// deletes them again. Set the database of each type to test as
// host:port/name, and the web passwords given to init-db.sh:
//
//	GAMEWE_TEST_INIT_MYSQL=127.0.0.1:33306/gamewe_init GAMEWE_TEST_INIT_PG=127.0.0.1:35432/gamewe_init \
//	GAMEWE_TEST_WEB_PASSWORD=... GAMEWE_TEST_WEB_ADMIN_PASSWORD=... \
//	go test -tags integration -run Init ./internal/database/
//
// With both set, the two schemas are also compared column by column.
package database

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gamewe-collector/internal/config"
)

var initEnv = map[string]string{config.MySQL: "GAMEWE_TEST_INIT_MYSQL", config.Postgres: "GAMEWE_TEST_INIT_PG"}

// initDB connects to the freshly initialized database of type typ as user.
func initDB(t *testing.T, typ, user, password string) *DB {
	t.Helper()
	v := os.Getenv(initEnv[typ])
	m := regexp.MustCompile(`^(.+):(\d+)/(\w+)$`).FindStringSubmatch(v)
	if m == nil {
		t.Skipf("set %s=host:port/name (a database made by init-db.sh) to run", initEnv[typ])
	}
	cfg := config.Defaults().Database
	cfg.Type, cfg.Host, cfg.Name = typ, m[1], m[3]
	cfg.Port, _ = strconv.Atoi(m[2])
	cfg.User, cfg.Password = user, password
	cfg.TLS.Mode = config.TLSDisable
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func eachType(t *testing.T, f func(t *testing.T, typ string)) {
	for _, typ := range []string{config.MySQL, config.Postgres} {
		t.Run(typ, func(t *testing.T) { f(t, typ) })
	}
}

// The web admin pages insert rows without an id: the identity columns
// (AUTO_INCREMENT, and on PostgreSQL the sequences behind them) must work for
// gamewe_web_admin after the seed.
func TestInitWebAdminInsertsWithoutID(t *testing.T) {
	eachType(t, func(t *testing.T, typ string) {
		db := initDB(t, typ, "gamewe_web_admin", os.Getenv("GAMEWE_TEST_WEB_ADMIN_PASSWORD"))
		ctx := context.Background()
		var seeded int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hlstats_Actions`).Scan(&seeded); err != nil || seeded == 0 {
			t.Fatalf("the seed is missing: %d actions, %v", seeded, err)
		}
		rows := []struct{ table, id, insert, del string }{
			{"hlstats_Games", "id", `INSERT INTO hlstats_Games (code, name) VALUES ('zzt', 'Test')`, `code = 'zzt'`},
			{"hlstats_Servers", "serverId", `INSERT INTO hlstats_Servers (address, port, name, game) VALUES ('192.0.2.1', 27015, 'Test', 'zzt')`, `address = '192.0.2.1'`},
			{"hlstats_Clans", "clanId", `INSERT INTO hlstats_Clans (tag, name, game) VALUES ('[zzt]', 'Test', 'zzt')`, `tag = '[zzt]'`},
			{"hlstats_Weapons", "weaponId", `INSERT INTO hlstats_Weapons (game, code, name) VALUES ('zzt', 'zzt', 'Test')`, `game = 'zzt'`},
			{"hlstats_Actions", "id", `INSERT INTO hlstats_Actions (game, code) VALUES ('zzt', 'zzt')`, `game = 'zzt'`},
			{"hlstats_Teams", "teamId", `INSERT INTO hlstats_Teams (game, code, name) VALUES ('zzt', 'zzt', 'Test')`, `game = 'zzt'`},
			{"hlstats_Roles", "roleId", `INSERT INTO hlstats_Roles (game, code, name) VALUES ('zzt', 'zzt', 'Test')`, `game = 'zzt'`},
			{"hlstats_Awards", "awardId", `INSERT INTO hlstats_Awards (game, code, name, verb) VALUES ('zzt', 'zzt', 'Test', 'tests')`, `game = 'zzt'`},
			{"hlstats_Definition_Ranks_Ribbons", "id", `INSERT INTO hlstats_Definition_Ranks_Ribbons (game_code, code, name) VALUES ('zzt', 'zzt', 'Test')`, `game_code = 'zzt'`},
		}
		for _, r := range rows {
			var before int64
			if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COALESCE(MAX(%s), 0) FROM %s`, r.id, r.table)).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, r.insert); err != nil {
				t.Errorf("%s: insert without id as gamewe_web_admin: %v", r.table, err)
				continue
			}
			var id int64
			if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s`, r.id, r.table, r.del)).Scan(&id); err != nil {
				t.Error(err)
			} else if id <= before {
				t.Errorf("%s: new id %d, but the table already had id %d", r.table, id, before)
			}
			if _, err := db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, r.table, r.del)); err != nil {
				t.Error(err)
			}
		}
		// Options and Users are keyed by name: replacing a value must work.
		for _, q := range []string{
			`INSERT INTO hlstats_Options (keyname, value) VALUES ('zzt_test', '1')`,
			`UPDATE hlstats_Options SET value = '2' WHERE keyname = 'zzt_test'`,
			`DELETE FROM hlstats_Options WHERE keyname = 'zzt_test'`,
			`INSERT INTO hlstats_Users (username, password) VALUES ('zzt_test', 'x')`,
			`DELETE FROM hlstats_Users WHERE username = 'zzt_test'`,
		} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
		// gamewe_web_admin may not write the collector's tables.
		if _, err := db.ExecContext(ctx, `DELETE FROM hlstats_Events_Frags`); err == nil {
			t.Error("gamewe_web_admin may delete from hlstats_Events_Frags")
		}
	})
}

// gamewe_web reads everything except hlstats_Servers.rcon_password and
// log_secret and writes nothing.
func TestInitWebGrants(t *testing.T) {
	eachType(t, func(t *testing.T, typ string) {
		db := initDB(t, typ, "gamewe_web", os.Getenv("GAMEWE_TEST_WEB_PASSWORD"))
		ctx := context.Background()
		for _, tbl := range append(append([]string{}, Tables...), "hlstats_Options", "hlstats_Games") {
			if tbl == "hlstats_Events_Chat" {
				continue // admin only
			}
			if _, err := db.ExecContext(ctx, `SELECT COUNT(*) FROM `+tbl); err != nil {
				t.Errorf("gamewe_web cannot read %s: %v", tbl, err)
			}
		}
		if _, err := db.ExecContext(ctx, `SELECT serverId, address, port, name, game FROM hlstats_Servers`); err != nil {
			t.Errorf("gamewe_web cannot read hlstats_Servers: %v", err)
		}
		if _, err := db.ExecContext(ctx, `SELECT rcon_password FROM hlstats_Servers`); err == nil {
			t.Error("gamewe_web can read hlstats_Servers.rcon_password")
		}
		for _, q := range []string{`SELECT username FROM hlstats_Users`, `SELECT command FROM hlstats_Events_Rcon`,
			`SELECT log_secret FROM hlstats_Servers`, `SELECT log_secret_changed FROM hlstats_Servers`,
			`SELECT ipAddress FROM hlstats_Events_Connects`, `SELECT * FROM hlstats_Events_Connects`, `SELECT message FROM hlstats_Events_Chat`} {
			if _, err := db.ExecContext(ctx, q); err == nil {
				t.Errorf("gamewe_web may: %s", q)
			}
		}
		if _, err := db.ExecContext(ctx, `SELECT timestamp_id, game_id, map, playerId, hostname, hostgroup FROM hlstats_Events_Connects`); err != nil {
			t.Errorf("gamewe_web cannot read the connects without ipAddress: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO hlstats_Options (keyname, value) VALUES ('zzt_web', '1')`); err == nil {
			db.ExecContext(ctx, `DELETE FROM hlstats_Options WHERE keyname = 'zzt_web'`)
			t.Error("gamewe_web can write hlstats_Options")
		}
	})
}

type column struct {
	nullable, identity bool
	def                string
}

// columns returns table.column -> column of the hlstats_ tables (names in
// lower case: PostgreSQL folds unquoted names).
func columns(t *testing.T, db *DB) map[string]column {
	q := `SELECT LOWER(TABLE_NAME), LOWER(COLUMN_NAME), IS_NULLABLE = 'YES', COALESCE(COLUMN_DEFAULT, ''), EXTRA LIKE '%auto_increment%'
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE 'hlstats\_%'`
	if db.Postgres() {
		// pg_catalog: information_schema hides the defaults from non-owners.
		q = `SELECT c.relname, a.attname, NOT a.attnotnull, COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity <> ''
			FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname LIKE 'hlstats\_%' AND a.attnum > 0 AND NOT a.attisdropped`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]column{}
	for rows.Next() {
		var tbl, col string
		var c column
		if err := rows.Scan(&tbl, &col, &c.nullable, &c.def, &c.identity); err != nil {
			t.Fatal(err)
		}
		c.def = normDefault(c.def)
		out[tbl+"."+col] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var pgCast = regexp.MustCompile(`::[a-z ]+(\(\d+(,\d+)?\))?$`)

// normDefault makes the default expressions of both databases comparable:
// '5'::character varying and 5, 0.00 and 0, current_timestamp() and
// CURRENT_TIMESTAMP are the same. The MariaDB zero date is 1970-01-01 on
// PostgreSQL (see docs/DB-SEMANTICS.md).
func normDefault(s string) string {
	s = pgCast.ReplaceAllString(s, "")
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = s[1 : len(s)-1] // PostgreSQL: (-1)
		s = pgCast.ReplaceAllString(s, "")
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		s = strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	switch strings.ToLower(strings.TrimSuffix(s, "()")) {
	case "null":
		return ""
	case "current_timestamp":
		return "CURRENT_TIMESTAMP"
	case "0000-00-00 00:00:00":
		return "1970-01-01 00:00:00"
	case "0000-00-00":
		return "1970-01-01"
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return s
}

// primaryKeys returns table -> primary key columns (lower case).
func primaryKeys(t *testing.T, db *DB) map[string]string {
	q := `SELECT LOWER(TABLE_NAME), LOWER(GROUP_CONCAT(COLUMN_NAME ORDER BY ORDINAL_POSITION))
		FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'PRIMARY'
		AND TABLE_NAME LIKE 'hlstats\_%' GROUP BY TABLE_NAME`
	if db.Postgres() {
		q = `SELECT c.relname, string_agg(a.attname, ',' ORDER BY array_position(i.indkey::int2[], a.attnum))
			FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
			WHERE i.indisprimary AND n.nspname = current_schema() AND c.relname LIKE 'hlstats\_%' GROUP BY c.relname`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var tbl, cols string
		if err := rows.Scan(&tbl, &cols); err != nil {
			t.Fatal(err)
		}
		out[tbl] = cols
	}
	return out
}

// Both schemas must have the same columns with the same NOT NULLs, defaults,
// identity columns and primary keys (hlstats_Options: keyname,
// hlstats_Users: username).
func TestInitSchemaParity(t *testing.T) {
	my := initDB(t, config.MySQL, "gamewe_web_admin", os.Getenv("GAMEWE_TEST_WEB_ADMIN_PASSWORD"))
	pg := initDB(t, config.Postgres, "gamewe_web_admin", os.Getenv("GAMEWE_TEST_WEB_ADMIN_PASSWORD"))
	a, b := columns(t, my), columns(t, pg)
	if len(a) < 300 {
		t.Fatalf("only %d MariaDB columns", len(a))
	}
	for k, c := range a {
		d, ok := b[k]
		switch {
		case !ok:
			t.Errorf("%s: missing on PostgreSQL", k)
		case c != d:
			t.Errorf("%s: MariaDB %+v, PostgreSQL %+v", k, c, d)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("%s: only on PostgreSQL", k)
		}
	}
	pa, pb := primaryKeys(t, my), primaryKeys(t, pg)
	for _, want := range [][2]string{{"hlstats_options", "keyname"}, {"hlstats_users", "username"}} {
		if pa[want[0]] != want[1] || pb[want[0]] != want[1] {
			t.Errorf("%s: primary key MariaDB (%s), PostgreSQL (%s), want (%s)", want[0], pa[want[0]], pb[want[0]], want[1])
		}
	}
	if len(pa) != len(pb) {
		t.Errorf("%d MariaDB primary keys, %d PostgreSQL primary keys", len(pa), len(pb))
	}
	for k, v := range pa {
		if pb[k] != v {
			t.Errorf("%s: primary key MariaDB (%s), PostgreSQL (%s)", k, v, pb[k])
		}
	}
}
