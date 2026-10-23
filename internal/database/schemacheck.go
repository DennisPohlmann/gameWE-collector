// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strings"
)

// Requirement is a column, index or table of the current schema
// (schema/init) that databases created with older versions lack. The
// collector runs without most of them (it checks and falls back), but loses
// what they are for.
type Requirement struct {
	Table  string
	Column string // "" for an index or a whole table
	Index  string // MariaDB index name; PostgreSQL: <table>_<name>
	For    string // what is missing without it
}

func (r Requirement) String() string {
	switch {
	case r.Column != "":
		return fmt.Sprintf("schema incomplete: missing %s.%s (%s)", r.Table, r.Column, r.For)
	case r.Index != "":
		return fmt.Sprintf("schema incomplete: missing index %s on %s (%s)", r.Index, r.Table, r.For)
	}
	return fmt.Sprintf("schema incomplete: missing table %s (%s)", r.Table, r.For)
}

// Requirements are the parts of the schema that older versions did not
// create, oldest first.
var Requirements = []Requirement{
	{Table: "hlstats_Servers", Column: "rcon_password", For: "RCON: in-game replies, status, logaddress"},
	{Table: "hlstats_Events_Latency", For: "ping samples and the latency award"},
	{Table: "hlstats_Games", Column: "realgame", For: "the base game of each game code (engine, cloned games); guessed from the code without it"},
	{Table: "hlstats_Events_Frags", Column: "pos_victim_x", For: "kill positions for heatmaps"},
	{Table: "hlstats_Events_Frags", Index: "map", For: "heatmaps per map"},
	{Table: "hlstats_Awards_History", Index: "date", For: "awards page speed"},
	{Table: "hlstats_Awards_History", Index: "award_id", For: "daily awards speed"},
	{Table: "hlstats_gamewe_jobs", For: "ranking resets by the running collector (web admin, gamewe reset)"},
	{Table: "hlstats_Events_Frags", Column: "server_id", For: "the server of each event (latest events per server); events are stored without it"},
}

// SchemaHint is what to do about a database with an incomplete schema.
const SchemaHint = "this database was created with an older version of gameWE: recreate it with schema/init (INSTALLATION.md)"

// MissingSchema returns the requirements the database lacks (read only:
// information_schema and pg_indexes).
func MissingSchema(ctx context.Context, db *DB) ([]Requirement, error) {
	var missing []Requirement
	for _, r := range Requirements {
		var q string
		var args []any
		switch {
		case r.Column != "" && db.Postgres():
			q, args = `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
				[]any{strings.ToLower(r.Table), strings.ToLower(r.Column)}
		case r.Column != "":
			q, args = `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND LOWER(TABLE_NAME) = ? AND LOWER(COLUMN_NAME) = ?`,
				[]any{strings.ToLower(r.Table), strings.ToLower(r.Column)}
		case r.Index != "" && db.Postgres():
			q, args = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`,
				[]any{strings.ToLower(r.Table + "_" + r.Index)}
		case r.Index != "":
			q, args = `SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND LOWER(TABLE_NAME) = ? AND INDEX_NAME = ?`,
				[]any{strings.ToLower(r.Table), r.Index}
		case db.Postgres():
			q, args = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`,
				[]any{strings.ToLower(r.Table)}
		default:
			q, args = `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND LOWER(TABLE_NAME) = ?`,
				[]any{strings.ToLower(r.Table)}
		}
		var n int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return nil, fmt.Errorf("schema check %s: %w", r.Table, err)
		}
		if n == 0 {
			missing = append(missing, r)
		}
	}
	return missing, nil
}
