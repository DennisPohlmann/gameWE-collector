// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gamewe-collector/internal/schema"
)

// gamewe db cleanup: the tables gameWE no longer uses. Like the other
// maintenance commands it names only tables of a list compiled into the
// collector (OldTableList), never one read from the database.

// OldTable is a table gameWE no longer uses, with why and (OldTables) its
// rows.
type OldTable struct {
	Table string
	Why   string
	Rows  int64
}

// retiredTables were part of the gameWE schema up to collector 1.4: tables
// of the HLstatsX schema that gameWE never wrote and nothing read.
var retiredTables = []OldTable{
	{Table: "hlstats_Events_Admin", Why: "admin events: gameWE never writes them, nothing reads them"},
	{Table: "hlstats_Events_Rcon", Why: "RCON commands (HLstatsX kept the RCON passwords there in clear text): gameWE never writes them"},
	{Table: "hlstats_HostGroups", Why: "settings of the old HLstatsX pages"},
	{Table: "hlstats_Players_Roles", Why: "roles per player: gameWE never writes them, nothing reads them"},
	{Table: "hlstats_Players_Roles_History_Months", Why: "monthly archive of the roles per player: never written"},
	{Table: "hlstats_Players_Teams", Why: "teams per player: gameWE never writes them, nothing reads them"},
	{Table: "hlstats_Players_Teams_History_Months", Why: "monthly archive of the teams per player: never written"},
	{Table: "hlstats_Style", Why: "styles of the old HLstatsX pages"},
	{Table: "hlstats_Trend", Why: "totals over time: gameWE never writes them, nothing reads them"},
}

// ceTables are tables of HLstatsX:CE that gameWE has no table for (left
// over when a database was used by CE before).
var ceTables = []string{
	"hlstats_Countries", "hlstats_Events_Statsme", "hlstats_Events_Statsme2", "hlstats_Events_StatsmeLatency",
	"hlstats_Events_StatsmeTime", "hlstats_Games_Defaults", "hlstats_Games_Supported", "hlstats_Heatmap_Config",
	"hlstats_Maps_Counts", "hlstats_Mods_Defaults", "hlstats_Mods_Supported", "hlstats_Options_Choices",
	"hlstats_Players_Awards", "hlstats_Players_Ribbons", "hlstats_Ranks", "hlstats_Ribbons", "hlstats_Ribbons_Triggers",
	"hlstats_Servers_Config", "hlstats_Servers_Config_Default", "hlstats_Servers_VoiceComm",
}

// OldTableList is every table gamewe db cleanup may drop, in the order it
// lists them: the retired ones, the leftovers of HLstatsX:CE and, with the
// default prefix, the old name of the version table.
func OldTableList(pfx Prefix) []OldTable {
	out := slices.Clone(retiredTables)
	for _, t := range ceTables {
		out = append(out, OldTable{Table: t, Why: "left over from HLstatsX:CE, gameWE has no such table"})
	}
	if pfx.Default() {
		out = append(out, OldTable{Table: LegacyMigrationsTable, Why: "the old name of the version table, its versions are in " + MigrationsTable})
	}
	return out
}

// oldIn returns the tables of OldTableList the database has.
func oldIn(cat catalog, pfx Prefix) []OldTable {
	var out []OldTable
	for _, t := range OldTableList(pfx) {
		if cat.tables[strings.ToLower(t.Table)] {
			out = append(out, t)
		}
	}
	return out
}

// OldTables returns the old tables the database has with their rows
// (exact counts), and the other hlstats_* tables that are neither part of
// the schema nor known as old (named as in the database, with the prefix):
// those are only listed, never dropped.
func OldTables(ctx context.Context, db *DB) (old []OldTable, unknown []string, err error) {
	cat, err := readCatalog(ctx, db.DB, db.Postgres(), db.Prefix)
	if err != nil {
		return nil, nil, err
	}
	old = oldIn(cat, db.Prefix)
	for i, t := range old {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteTable(db.Type, t.Table)).Scan(&old[i].Rows); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", db.Prefix.Table(t.Table), err)
		}
	}
	tables, err := schema.Tables(db.Type)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]bool{MigrationsTable: true}
	for _, t := range tables {
		known[strings.ToLower(t.Name)] = true
	}
	for _, t := range OldTableList(db.Prefix) {
		known[strings.ToLower(t.Table)] = true
	}
	for t := range cat.tables {
		if strings.HasPrefix(t, "hlstats_") && !known[t] {
			unknown = append(unknown, db.Prefix.Table(t))
		}
	}
	slices.Sort(unknown)
	return old, unknown, nil
}

// CleanupSQL is the statement that drops the tables (all of OldTableList
// when tables is nil), IF EXISTS, for gamewe db cleanup and its -print.
// Names not in OldTableList are refused.
func CleanupSQL(dialect string, pfx Prefix, tables []string) (string, error) {
	list := OldTableList(pfx)
	if tables == nil {
		for _, t := range list {
			tables = append(tables, t.Table)
		}
	}
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		if !slices.ContainsFunc(list, func(o OldTable) bool { return o.Table == t }) {
			return "", fmt.Errorf("%s is not an old table gameWE may drop", t)
		}
		names = append(names, quoteTable(dialect, t))
	}
	if len(names) == 0 {
		return "", nil
	}
	return "DROP TABLE IF EXISTS " + strings.Join(names, ", ") + ";\n", nil
}

// DropOldTables drops the tables (of OldTableList). It needs the DROP
// right, which the collector user has not on MariaDB by default: then it
// returns a *DropRightsError and nothing is dropped.
func DropOldTables(ctx context.Context, db *DB, tables []string) error {
	q, err := CleanupSQL(db.Type, db.Prefix, tables)
	if err != nil || q == "" {
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
