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

	pfx Prefix // String names the tables as the database does
}

func (r Requirement) String() string {
	t := r.pfx.Table(r.Table)
	switch {
	case r.Column != "":
		return fmt.Sprintf("schema incomplete: missing column %s.%s", t, r.Column)
	case r.Index != "":
		return fmt.Sprintf("schema incomplete: missing index %s on %s", r.pfx.Table(r.Index), t)
	}
	return fmt.Sprintf("schema incomplete: missing table %s", t)
}

// withPrefix sets the prefix String names the tables with (only another
// than the default, so that the values stay comparable).
func withPrefix(reqs []Requirement, p Prefix) []Requirement {
	if !p.Default() {
		for i := range reqs {
			reqs[i].pfx = p
		}
	}
	return reqs
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
	cat, err := readCatalog(ctx, db.DB, db.Postgres(), db.Prefix)
	if err != nil {
		return nil, err
	}
	return withPrefix(missing(tables, cat, db.Postgres()), db.Prefix), nil
}
