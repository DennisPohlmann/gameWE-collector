// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"gamewe-collector/internal/schema"
)

// Requirement is a table, column or index of the schema (internal/schema)
// that the database lacks. `gamewe db migrate` adds it.
type Requirement struct {
	Table  string
	Column string // "" for an index or a whole table
	Index  string // MySQL key name; PostgreSQL: <table>_<name> in lower case
}

func (r Requirement) String() string {
	switch {
	case r.Column != "":
		return fmt.Sprintf("schema incomplete: missing column %s.%s", r.Table, r.Column)
	case r.Index != "":
		return fmt.Sprintf("schema incomplete: missing index %s on %s", r.Index, r.Table)
	}
	return fmt.Sprintf("schema incomplete: missing table %s", r.Table)
}

// SchemaHint is what to do about a database with an incomplete or old
// schema.
const SchemaHint = "make a backup, then run: gamewe db migrate"

// MissingSchema returns the tables, columns and indexes of the schema the
// database lacks (read only: information_schema and pg_indexes).
func MissingSchema(ctx context.Context, db *DB) ([]Requirement, error) {
	tables, err := schema.Tables(db.Type)
	if err != nil {
		return nil, err
	}
	cat, err := readCatalog(ctx, db.DB, db.Postgres())
	if err != nil {
		return nil, err
	}
	return missing(tables, cat, db.Postgres()), nil
}
