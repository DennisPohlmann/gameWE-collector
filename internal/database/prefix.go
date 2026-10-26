// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql/driver"
	"hash/fnv"
	"strings"

	"gamewe-collector/internal/config"
)

// Table prefix (database.table_prefix). The code and the compiled schema
// always name the tables hlstats_<Name> and MigrationsTable; with another
// prefix the connection rewrites these names in every statement it sends
// (Prefix.SQL), so the database has <prefix>_<Name> and
// <prefix>_gamewe_schema_migrations. Names that come back from the catalog
// are turned into the code's names with Prefix.Logical. With the default
// prefix nothing is rewritten.

// codePrefix is the prefix the code and the schema files are written with.
const codePrefix = config.DefaultTablePrefix + "_"

// Prefix is a valid table prefix without the trailing "_"
// (config.NormalizeTablePrefix). The zero value is the default.
type Prefix string

// Default reports whether p is the default prefix (nothing is rewritten).
func (p Prefix) Default() bool { return p == "" || p == config.DefaultTablePrefix }

// String is the prefix as configured, without the trailing "_".
func (p Prefix) String() string {
	if p == "" {
		return config.DefaultTablePrefix
	}
	return string(p)
}

// Table returns the name a table of the code (hlstats_Players,
// MigrationsTable) has in the database.
func (p Prefix) Table(name string) string {
	if p.Default() {
		return name
	}
	if hasCodePrefix(name) {
		return string(p) + "_" + name[len(codePrefix):]
	}
	if strings.EqualFold(name, MigrationsTable) {
		return string(p) + "_" + name
	}
	return name
}

// Logical returns the code's name of a table (or PostgreSQL index) name of
// the database, and whether it belongs to this prefix. A name of another
// prefix that would read as one of the code's names (hlstats_Players when
// the prefix is gw) is not this collector's: ok is false. Other names come
// back unchanged with ok true.
func (p Prefix) Logical(name string) (string, bool) {
	if p.Default() {
		return name, true
	}
	if len(name) > len(p) && name[len(p)] == '_' && strings.EqualFold(name[:len(p)], string(p)) {
		rest := name[len(p)+1:]
		if strings.EqualFold(rest, MigrationsTable) {
			return MigrationsTable, true
		}
		return codePrefix + rest, true
	}
	if hasCodePrefix(name) || strings.EqualFold(name, MigrationsTable) {
		return name, false
	}
	return name, true
}

func hasCodePrefix(name string) bool {
	return len(name) > len(codePrefix) && strings.EqualFold(name[:len(codePrefix)], codePrefix)
}

// SQL rewrites the table names of a statement: every identifier that
// starts with hlstats_ and the identifier MigrationsTable, also inside
// `...` or "..." quotes. String literals and comments stay as they are.
func (p Prefix) SQL(q string) string { return p.rewrite(q, false) }

// Script rewrites a script of the schema (schema.sql, seed.sql,
// grants.sql) like SQL, and also inside string literals: grants.sql builds
// statements as strings. Only for the compiled scripts, never for data.
func (p Prefix) Script(q string) string { return p.rewrite(q, true) }

func (p Prefix) rewrite(q string, literals bool) string {
	if p.Default() || !needsRewrite(q) {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'' && !literals:
			end := quotedEnd(q, i)
			b.WriteString(q[i:end])
			i = end
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			end := strings.IndexByte(q[i:], '\n')
			if end < 0 {
				end = len(q) - i
			}
			b.WriteString(q[i : i+end])
			i += end
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				end = len(q) - i
			} else {
				end += 4
			}
			b.WriteString(q[i : i+end])
			i += end
		case isIdentByte(c) && (i == 0 || !isIdentByte(q[i-1])):
			j := i
			for j < len(q) && isIdentByte(q[j]) {
				j++
			}
			b.WriteString(p.Table(q[i:j]))
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// needsRewrite is a quick check before the scan: most statements of the
// collector name a table, but the lock and SET statements do not.
func needsRewrite(q string) bool {
	l := strings.ToLower(q)
	return strings.Contains(l, codePrefix) || strings.Contains(l, MigrationsTable)
}

// quotedEnd returns the index after the literal that starts at q[i]; a
// doubled quote does not end it (as in Rebind).
func quotedEnd(q string, i int) int {
	c := q[i]
	for j := i + 1; j < len(q); j++ {
		if q[j] == c {
			if j+1 < len(q) && q[j+1] == c {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// like is the LIKE pattern of the tables of the prefix: hlstats\_%.
func (p Prefix) like() string {
	return strings.ReplaceAll(p.String(), "_", `\_`) + `\_%`
}

// lockName is the name of a GET_LOCK lock: per prefix, so that two
// installations with other prefixes in one database do not block each
// other.
func (p Prefix) lockName(base string) string {
	if p.Default() {
		return base
	}
	return base + string(p) + "."
}

// lockKey is the key of a PostgreSQL advisory lock, per prefix like
// lockName.
func (p Prefix) lockKey(base int64) int64 {
	if p.Default() {
		return base
	}
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(string(p))))
	return base ^ int64(h.Sum32())<<8
}

// prefixConnector rewrites the table names of every statement sent over
// its connections (Prefix.SQL).
type prefixConnector struct {
	driver.Connector
	p Prefix
}

func (c prefixConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &prefixConn{Conn: conn, p: c.p}, nil
}

// prefixConn passes everything on to the driver's connection; the methods
// that take SQL rewrite it first. Both drivers (go-sql-driver/mysql, pgx
// stdlib) implement all the optional interfaces below.
type prefixConn struct {
	driver.Conn
	p Prefix
}

func (c *prefixConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(c.p.SQL(query))
}

func (c *prefixConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, c.p.SQL(query))
	}
	return c.Conn.Prepare(c.p.SQL(query))
}

func (c *prefixConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.Conn.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, c.p.SQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *prefixConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, c.p.SQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *prefixConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback of database/sql itself
}

func (c *prefixConn) Ping(ctx context.Context) error {
	if pg, ok := c.Conn.(driver.Pinger); ok {
		return pg.Ping(ctx)
	}
	return nil
}

func (c *prefixConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *prefixConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *prefixConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.Conn.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}
