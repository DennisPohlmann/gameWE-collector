// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gamewe-collector/internal/schema"
)

// The maintenance commands gamewe db drop, reset, purge and optimize. Table
// names in their SQL come only from the schema compiled into the collector
// (internal/schema) and MigrationsTable, never from the database or the
// command line: other tables in the database are never touched.

// TableRows is a table and its number of rows.
type TableRows struct {
	Table string
	Rows  int64
}

// OwnTables returns the tables of this collector's schema that exist in the
// database, in schema order, followed by MigrationsTable if it exists, and
// the other hlstats_* tables (of an older HLstatsX; named as in the
// database, with the prefix), which the maintenance commands leave alone.
func OwnTables(ctx context.Context, db *DB) (own, other []string, err error) {
	tables, err := schema.Tables(db.Type)
	if err != nil {
		return nil, nil, err
	}
	cat, err := readCatalog(ctx, db.DB, db.Postgres(), db.Prefix)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]bool{}
	for _, t := range tables {
		known[strings.ToLower(t.Name)] = true
		if cat.tables[strings.ToLower(t.Name)] {
			own = append(own, t.Name)
		}
	}
	if cat.tables[MigrationsTable] {
		own = append(own, MigrationsTable)
	}
	for t := range cat.tables {
		if strings.HasPrefix(t, "hlstats_") && !known[t] && t != MigrationsTable {
			other = append(other, db.Prefix.Table(t))
		}
	}
	slices.Sort(other)
	return own, other, nil
}

// quoteTable quotes a table name of the schema for the dialect.
func quoteTable(dialect, name string) string {
	if dialect == schema.Postgres {
		return name // the PostgreSQL schema uses unquoted (lower case) names
	}
	return "`" + name + "`"
}

// isOwnTable reports whether name is a table of the compiled schema or
// MigrationsTable (the allowlist of the maintenance SQL).
func isOwnTable(dialect, name string) bool {
	if name == MigrationsTable {
		return true
	}
	tables, err := schema.Tables(dialect)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(tables, func(t schema.Table) bool { return t.Name == name })
}

// DropSQL is the statement that drops all tables of the schema and the
// version table (IF EXISTS), for gamewe db drop and its -print. With the
// default prefix also the version table of its old name
// (LegacyMigrationsTable).
func DropSQL(dialect string, pfx Prefix) (string, error) {
	tables, err := schema.Tables(dialect)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(tables)+1)
	for _, t := range tables {
		names = append(names, quoteTable(dialect, t.Name))
	}
	names = append(names, quoteTable(dialect, MigrationsTable))
	if pfx.Default() {
		names = append(names, quoteTable(dialect, LegacyMigrationsTable))
	}
	return "DROP TABLE IF EXISTS " + strings.Join(names, ", ") + ";\n", nil
}

// DropTables drops all tables of the schema and the version table. The
// database is empty afterwards (for gameWE): gamewe db migrate (or the
// next start with database.auto_migrate) creates it again. It needs the DROP
// right, which the collector user has not on MariaDB by default: then it
// returns a *DropRightsError and nothing is dropped.
func DropTables(ctx context.Context, db *DB) error {
	q, err := DropSQL(db.Type, db.Prefix)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, strings.TrimSuffix(q, ";\n")); err != nil {
		if isDenied(err) {
			return &DropRightsError{Err: err}
		}
		return err
	}
	return nil
}

// DropRightsError is returned when the database user may not drop tables.
type DropRightsError struct{ Err error }

func (e *DropRightsError) Error() string {
	return fmt.Sprintf("the database user may not drop tables (%v); nothing was dropped", e.Err)
}

func (e *DropRightsError) Unwrap() error { return e.Err }

// CountRows returns the rows of the tables (exact counts).
func CountRows(ctx context.Context, db *DB, tables []string) ([]TableRows, error) {
	out := make([]TableRows, 0, len(tables))
	for _, t := range tables {
		if !isOwnTable(db.Type, t) {
			return nil, fmt.Errorf("%s is not a table of the gameWE schema", t)
		}
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteTable(db.Type, t)).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		out = append(out, TableRows{t, n})
	}
	return out, nil
}

// ResetData empties every table of the schema (the structure and the
// schema versions stay), resets the AUTO_INCREMENT / identity counters and
// loads the reference data again: the database is as new. MariaDB: DELETE
// and ALTER TABLE (rights the collector user has); PostgreSQL: one TRUNCATE
// ... RESTART IDENTITY in a transaction (the owner of the tables may).
func ResetData(ctx context.Context, db *DB) error {
	tables, err := schema.Tables(db.Type)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if db.Postgres() {
		names := make([]string, 0, len(tables))
		for _, t := range tables {
			names = append(names, quoteTable(db.Type, t.Name))
		}
		_, err = conn.ExecContext(ctx, "TRUNCATE TABLE "+strings.Join(names, ", ")+" RESTART IDENTITY")
	} else {
		for _, t := range tables {
			q := quoteTable(db.Type, t.Name)
			if _, err = conn.ExecContext(ctx, "DELETE FROM "+q); err != nil {
				err = fmt.Errorf("%s: %w", t.Name, err)
				break
			}
			if strings.Contains(t.Create, "AUTO_INCREMENT") {
				if _, err = conn.ExecContext(ctx, "ALTER TABLE "+q+" AUTO_INCREMENT = 1"); err != nil {
					err = fmt.Errorf("%s: %w", t.Name, err)
					break
				}
			}
		}
	}
	if err != nil {
		return rightsHint(ctx, conn, db.Postgres(), db.Prefix, err)
	}
	return runScript(ctx, conn, db.Type, schema.Seed(db.Type))
}

// PurgeTables are the event tables (hlstats_Events_*, with timestamp_id)
// gamewe db purge deletes old rows from.
func PurgeTables(dialect string) ([]string, error) {
	tables, err := schema.Tables(dialect)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tables {
		if !strings.HasPrefix(strings.ToLower(t.Name), "hlstats_events_") {
			continue
		}
		if slices.ContainsFunc(t.Columns, func(c schema.Column) bool { return strings.EqualFold(c.Name, "timestamp_id") }) {
			out = append(out, t.Name)
		}
	}
	return out, nil
}

// purgeChunk is how many rows one DELETE removes on MariaDB, so that one
// statement locks few rows (and a MyISAM table, before schema version 19,
// only shortly) at a time.
const purgeChunk = 20_000

// Purge deletes the events before before from PurgeTables (dry: only
// counts them) and returns the rows per table. Players, their totals,
// rankings, awards and the history per day stay.
func Purge(ctx context.Context, db *DB, before time.Time, dry bool) ([]TableRows, error) {
	tables, err := PurgeTables(db.Type)
	if err != nil {
		return nil, err
	}
	cat, err := readCatalog(ctx, db.DB, db.Postgres(), db.Prefix)
	if err != nil {
		return nil, err
	}
	var out []TableRows
	for _, t := range tables {
		if !cat.tables[strings.ToLower(t)] {
			continue
		}
		q := quoteTable(db.Type, t)
		if dry {
			var n int64
			if err := db.QueryRowContext(ctx, db.Q("SELECT COUNT(*) FROM "+q+" WHERE timestamp_id < ?"), before.Unix()).Scan(&n); err != nil {
				return out, fmt.Errorf("%s: %w", t, err)
			}
			out = append(out, TableRows{t, n})
			continue
		}
		var total int64
		for {
			stmt := "DELETE FROM " + q + " WHERE timestamp_id < ?"
			if !db.Postgres() {
				stmt += fmt.Sprintf(" LIMIT %d", purgeChunk)
			}
			res, err := db.ExecContext(ctx, db.Q(stmt), before.Unix())
			if err != nil {
				return out, fmt.Errorf("%s: %w", t, err)
			}
			n, _ := res.RowsAffected()
			total += n
			if db.Postgres() || n < purgeChunk {
				break
			}
		}
		out = append(out, TableRows{t, total})
	}
	return out, nil
}

// Optimize gives the space of deleted rows back and renews the statistics
// of the tables (MariaDB: OPTIMIZE TABLE, which rebuilds an InnoDB table
// and locks a MyISAM one while it runs; PostgreSQL: VACUUM ANALYZE). progress is called before each
// table.
func Optimize(ctx context.Context, db *DB, tables []string, progress func(table string)) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, t := range tables {
		if !isOwnTable(db.Type, t) {
			return fmt.Errorf("%s is not a table of the gameWE schema", t)
		}
		if progress != nil {
			progress(t)
		}
		if db.Postgres() {
			if _, err := conn.ExecContext(ctx, "VACUUM (ANALYZE) "+quoteTable(db.Type, t)); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			continue
		}
		// OPTIMIZE TABLE answers with rows (Table, Op, Msg_type, Msg_text);
		// an error is a row with Msg_type error.
		rows, err := conn.QueryContext(ctx, "OPTIMIZE TABLE "+quoteTable(db.Type, t))
		if err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		var msgs []string
		for rows.Next() {
			var tbl, op, typ, text sql.NullString
			if err := rows.Scan(&tbl, &op, &typ, &text); err != nil {
				rows.Close()
				return fmt.Errorf("%s: %w", t, err)
			}
			if strings.EqualFold(typ.String, "error") {
				msgs = append(msgs, text.String)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		if len(msgs) > 0 {
			return fmt.Errorf("%s: %w", t, errors.New(strings.Join(msgs, "; ")))
		}
	}
	return nil
}
