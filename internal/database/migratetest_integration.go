// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/schema"
)

// Test databases of the migration tests. They are created and dropped on a
// throwaway server with its admin account:
//
//	GAMEWE_TEST_MIGRATE_TYPE=mysql|postgres GAMEWE_TEST_MIGRATE_HOST=127.0.0.1 GAMEWE_TEST_MIGRATE_PORT=33511 \
//	GAMEWE_TEST_MIGRATE_ADMIN_USER=root GAMEWE_TEST_MIGRATE_ADMIN_PASSWORD=... \
//	go test -tags integration -run Migrate ./internal/database/ ./cmd/gamewe/
//
// The users gamewe, gamewe_web and gamewe_web_admin are created (or their
// passwords set) with the init script schema/init/00_create_database.sql
// (schema/postgres/init/ on PostgreSQL), with the passwords of
// COLLECTOR_PASSWORD, WEB_PASSWORD and WEB_ADMIN_PASSWORD (defaults colpw,
// webpw, admpw): never point this at a server with real users.

// MigrateTestEnv reports the database type of the migration tests, "" if
// they are not configured.
func MigrateTestEnv() string { return os.Getenv("GAMEWE_TEST_MIGRATE_TYPE") }

// TestPassword returns the password of a test user (gamewe, gamewe_web,
// gamewe_web_admin).
func TestPassword(user string) string {
	p := map[string][2]string{
		"gamewe":           {"COLLECTOR_PASSWORD", "colpw"},
		"gamewe_web":       {"WEB_PASSWORD", "webpw"},
		"gamewe_web_admin": {"WEB_ADMIN_PASSWORD", "admpw"},
	}[user]
	if v := os.Getenv(p[0]); v != "" {
		return v
	}
	return p[1]
}

// TestConfig is the connection to database name as user.
func TestConfig(name, user, password string) config.Database {
	cfg := config.Defaults().Database
	cfg.Type = MigrateTestEnv()
	cfg.Host = os.Getenv("GAMEWE_TEST_MIGRATE_HOST")
	cfg.Port, _ = strconv.Atoi(os.Getenv("GAMEWE_TEST_MIGRATE_PORT"))
	cfg.Name, cfg.User, cfg.Password = name, user, password
	cfg.TLS.Mode = config.TLSDisable
	cfg.TablePrefix = config.NormalizeTablePrefix(os.Getenv("GAMEWE_TEST_DB_PREFIX")) // e.g. gw: the tables gw_*
	cfg.MaxOpenConns = 4
	return cfg
}

// AdminDB opens the admin connection to database name ("" for the server).
func AdminDB(name string) (*DB, error) {
	if name == "" {
		name = "mysql"
		if MigrateTestEnv() == config.Postgres {
			name = "postgres"
		}
	}
	return Open(TestConfig(name, os.Getenv("GAMEWE_TEST_MIGRATE_ADMIN_USER"), os.Getenv("GAMEWE_TEST_MIGRATE_ADMIN_PASSWORD")))
}

var reTestName = regexp.MustCompile(`^[a-z0-9_]+$`)

// CreateTestDatabase drops and creates the database name with the init
// script (database, users, rights), as a new installation does. With
// oldRights the collector user gets only the rights of gameWE up to 0.2.1
// (SELECT, INSERT, UPDATE, DELETE; on PostgreSQL no CREATE on the schema).
func CreateTestDatabase(ctx context.Context, name string, oldRights bool) error {
	if !reTestName.MatchString(name) {
		return fmt.Errorf("test database name %q", name)
	}
	admin, err := AdminDB("")
	if err != nil {
		return err
	}
	defer admin.Close()
	pw := map[string]string{"collector": TestPassword("gamewe"), "web": TestPassword("gamewe_web"), "admin": TestPassword("gamewe_web_admin")}
	if admin.Postgres() {
		admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
		if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
			return err
		}
		data, err := os.ReadFile(repoFile("schema/postgres/init/00_create_database.sql"))
		if err != nil {
			return err
		}
		s := string(data)
		s = strings.ReplaceAll(s, `:'db'`, "'"+name+"'")
		for k, v := range map[string]string{"collector_pw": pw["collector"], "web_pw": pw["web"], "admin_pw": pw["admin"]} {
			s = strings.ReplaceAll(s, ":'"+k+"'", "'"+strings.ReplaceAll(v, "'", "''")+"'")
		}
		before, after, ok := strings.Cut(s, "\\connect :\"db\"\n")
		if !ok {
			return fmt.Errorf("00_create_database.sql: no \\connect")
		}
		var lines []string
		for _, l := range strings.Split(before, "\n") {
			if !strings.HasPrefix(l, `\set`) {
				lines = append(lines, l)
			}
		}
		if err := runScript(ctx, admin, schema.Postgres, strings.Join(lines, "\n")); err != nil {
			return fmt.Errorf("00_create_database.sql: %w", err)
		}
		in, err := AdminDB(name)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := runScript(ctx, in, schema.Postgres, after); err != nil {
			return fmt.Errorf("00_create_database.sql (in %s): %w", name, err)
		}
		if oldRights {
			_, err = in.ExecContext(ctx, `REVOKE CREATE ON SCHEMA public FROM gamewe`)
		}
		return err
	}
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
		return err
	}
	data, err := os.ReadFile(repoFile("schema/init/00_create_database.sql"))
	if err != nil {
		return err
	}
	s := strings.ReplaceAll(string(data), "`gamewe`", "`"+name+"`")
	s = strings.ReplaceAll(s, "CHANGE_ME_COLLECTOR", pw["collector"])
	s = strings.ReplaceAll(s, "CHANGE_ME_WEB_ADMIN", pw["admin"])
	s = strings.ReplaceAll(s, "CHANGE_ME_WEB", pw["web"])
	if err := runScript(ctx, admin, schema.MySQL, s); err != nil {
		return fmt.Errorf("00_create_database.sql: %w", err)
	}
	if oldRights {
		_, err = admin.ExecContext(ctx, "REVOKE CREATE, ALTER, INDEX, GRANT OPTION ON `"+name+"`.* FROM 'gamewe'@'%'")
	}
	return err
}

// DropTestDatabase removes a database of CreateTestDatabase.
func DropTestDatabase(ctx context.Context, name string) error {
	admin, err := AdminDB("")
	if err != nil {
		return err
	}
	defer admin.Close()
	if admin.Postgres() {
		admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
		_, err = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name)
		return err
	}
	_, err = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+name+"`")
	return err
}

// RunTestScript runs a SQL script (the files of internal/schema or a test
// fixture) on db.
func RunTestScript(ctx context.Context, db *DB, script string) error {
	return runScript(ctx, db, db.Type, script)
}

// LoadTestFile runs a file of the repository (path from the repository
// root) on db.
func LoadTestFile(ctx context.Context, db *DB, path string) error {
	data, err := os.ReadFile(repoFile(path))
	if err != nil {
		return err
	}
	return RunTestScript(ctx, db, string(data))
}

// repoFile finds path (from the repository root) from a package directory.
func repoFile(path string) string {
	for _, up := range []string{"", "../", "../../", "../../../"} {
		if _, err := os.Stat(up + "go.mod"); err == nil {
			return up + path
		}
	}
	return path
}
