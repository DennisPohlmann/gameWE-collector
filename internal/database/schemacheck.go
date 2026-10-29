// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"gamewe-collector/internal/schema"
)

// Requirement is a table, column or index of the schema (internal/schema)
// that the database lacks, a column still of a narrower type than the
// schema's, or (MariaDB) a table still on another storage engine than the
// schema's. `gamewe db migrate` adds, widens or converts it.
type Requirement struct {
	Table  string
	Column string // "" for an index or a whole table
	Index  string // MySQL key name; PostgreSQL: <table>_<name> in lower case
	Engine string // MySQL: the engine the schema wants (InnoDB) while the table is on another
	Type   string // the type the schema wants for Column (Migration.Widen) while it has another

	pfx Prefix // String names the tables as the database does
}

func (r Requirement) String() string {
	t := r.pfx.Table(r.Table)
	switch {
	case r.Type != "":
		return fmt.Sprintf("schema outdated: column %s.%s is not %s", t, r.Column, r.Type)
	case r.Column != "":
		return fmt.Sprintf("schema incomplete: missing column %s.%s", t, r.Column)
	case r.Index != "":
		return fmt.Sprintf("schema incomplete: missing index %s on %s", r.pfx.Table(r.Index), t)
	case r.Engine != "":
		return fmt.Sprintf("schema outdated: table %s is not %s", t, r.Engine)
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
