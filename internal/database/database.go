// SPDX-License-Identifier: GPL-3.0-or-later

// Package database opens the MariaDB / MySQL or PostgreSQL connection
// with strict TLS handling. Once TLS is configured, a connection never
// falls back to plaintext.
package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"gamewe-collector/internal/config"
)

// Tables lists the HLstatsX tables gameWE reads or writes.
var Tables = []string{
	"hlstats_Games", "hlstats_Servers", "hlstats_Maps", "hlstats_Weapons", "hlstats_Teams", "hlstats_Actions",
	"hlstats_Players", "hlstats_PlayerUniqueIds", "hlstats_Players_Data", "hlstats_PlayerNames",
	"hlstats_Players_History", "hlstats_Players_Weapons", "hlstats_Players_Actions", "hlstats_Livestats",
	"hlstats_Events_Connects", "hlstats_Events_Entries", "hlstats_Events_Disconnects", "hlstats_Events_ChangeName",
	"hlstats_Events_ChangeTeam", "hlstats_Events_Frags", "hlstats_Events_Teamkills", "hlstats_Events_Suicides",
	"hlstats_Events_PlayerActions", "hlstats_Events_TeamBonuses", "hlstats_Events_Chat",
	"hlstats_Events_PlayerPlayerActions", "hlstats_Events_ChangeRole", "hlstats_Events_Locations", "hlstats_Events_Latency",
	"hlstats_Roles", "hlstats_Clans", "hlstats_ClanTags", "hlstats_Awards", "hlstats_Awards_History",
	"hlstats_Players_Locations", "hlstats_server_load",
}

// DB is a connection pool together with its SQL dialect.
type DB struct {
	*sql.DB
	Type string // config.MySQL or config.Postgres
}

// Postgres reports whether the database is PostgreSQL.
func (d *DB) Postgres() bool { return d.Type == config.Postgres }

// Q converts a query written with ? placeholders for the driver.
func (d *DB) Q(query string) string {
	if d.Postgres() {
		return Rebind(query)
	}
	return query
}

// Open creates a connection pool. It does not connect yet.
func Open(cfg config.Database) (*DB, error) {
	tlsConf, err := BuildTLS(cfg.TLS, cfg.Host)
	if err != nil {
		return nil, err
	}
	var db *sql.DB
	if cfg.Type == config.Postgres {
		db, err = openPostgres(cfg, tlsConf)
	} else {
		db, err = openMySQL(cfg, tlsConf)
	}
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxOpenConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &DB{DB: db, Type: cfg.Type}, nil
}

func openMySQL(cfg config.Database, tlsConf *tls.Config) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	mc.DBName = cfg.Name
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Timeout = time.Duration(cfg.ConnectTimeout) * time.Second
	// The schema (schema/hlstatsx.sql) is utf8mb4.
	if err := mc.Apply(mysql.Charset("utf8mb4", "utf8mb4_unicode_ci")); err != nil {
		return nil, err
	}
	if tlsConf != nil {
		// A registered custom config is always required; the driver only
		// falls back to plaintext for tls=preferred, which is never used.
		if err := mysql.RegisterTLSConfig("gamewe", tlsConf); err != nil {
			return nil, err
		}
		mc.TLSConfig = "gamewe"
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, fmt.Errorf("mysql config: %w", err)
	}
	return sql.OpenDB(conn), nil
}

func openPostgres(cfg config.Database, tlsConf *tls.Config) (*sql.DB, error) {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/" + cfg.Name,
	}
	q := url.Values{}
	// sslmode=disable only stops pgx from building its own TLS settings and
	// plaintext fallbacks; TLS is set explicitly below.
	q.Set("sslmode", "disable")
	q.Set("connect_timeout", strconv.Itoa(cfg.ConnectTimeout))
	q.Set("application_name", "gamewe")
	u.RawQuery = q.Encode()
	cc, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("postgres config: %w", err)
	}
	cc.RuntimeParams["client_encoding"] = "UTF8"
	cc.RuntimeParams["timezone"] = "UTC"
	cc.TLSConfig = tlsConf
	cc.Fallbacks = nil
	return stdlib.OpenDB(*cc), nil
}

// Info describes the live connection, as reported by the server.
type Info struct {
	Version    string
	TLS        bool
	TLSVersion string
	Cipher     string
}

// Inspect queries the server version and the TLS state of one connection.
func Inspect(ctx context.Context, db *DB) (Info, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return Info{}, Explain(err)
	}
	defer conn.Close()
	var info Info
	if db.Postgres() {
		err = conn.QueryRowContext(ctx, `SELECT current_setting('server_version')`).Scan(&info.Version)
		if err == nil {
			info.Version = "PostgreSQL " + info.Version
			err = conn.QueryRowContext(ctx,
				`SELECT ssl, COALESCE(version, ''), COALESCE(cipher, '') FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).
				Scan(&info.TLS, &info.TLSVersion, &info.Cipher)
		}
		return info, err
	}
	var name string
	err = conn.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&info.Version)
	if err == nil {
		err = conn.QueryRowContext(ctx, `SHOW SESSION STATUS LIKE 'Ssl_version'`).Scan(&name, &info.TLSVersion)
	}
	if err == nil {
		err = conn.QueryRowContext(ctx, `SHOW SESSION STATUS LIKE 'Ssl_cipher'`).Scan(&name, &info.Cipher)
	}
	info.TLS = info.TLSVersion != ""
	return info, err
}

// CheckTables verifies that every table exists and is readable.
func CheckTables(ctx context.Context, db *DB) error {
	for _, t := range Tables {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t+" WHERE 1 = 0").Scan(&n); err != nil {
			if t == "hlstats_Events_Latency" {
				return fmt.Errorf("table %s: %w (%s)", t, err, SchemaHint)
			}
			return fmt.Errorf("table %s: %w (is this the gameWE database, see schema/hlstatsx.sql or schema/postgres/hlstatsx.sql?)", t, err)
		}
	}
	return nil
}

// Explain adds a hint to common TLS certificate errors.
func Explain(err error) error {
	if err == nil {
		return nil
	}
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &unknown):
		return fmt.Errorf("%w (hint: the server certificate is not signed by database.tls.ca_file or a system CA)", err)
	case errors.As(err, &host):
		return fmt.Errorf("%w (hint: the certificate does not match the host; set database.tls.server_name or use verify-ca)", err)
	case errors.As(err, &invalid):
		return fmt.Errorf("%w (hint: the server certificate is expired or not valid for TLS server use)", err)
	}
	return err
}
