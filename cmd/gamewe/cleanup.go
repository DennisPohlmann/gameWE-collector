// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
)

// errCleanupNoTTY: gamewe db cleanup has no --force, someone has to type
// the confirmation.
var errCleanupNoTTY = errors.New("no interactive confirmation possible (stdin is not a terminal), nothing dropped: " +
	"run it in a terminal, or print the SQL with -print and run it yourself")

// gamewe db cleanup: drop the tables gameWE no longer uses, after the
// list and a typed confirmation (no --force).
func dbCleanupCmd(args []string, c dbIO) error {
	var print, dry bool
	cfg, err := loadConfig("db cleanup", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&dry, "dry-run", false, "only list the old tables and their rows; changes nothing")
		fs.BoolVar(&dry, "n", false, "short for --dry-run")
		fs.BoolVar(&print, "print", false, "only print the SQL (to run it as a database admin)")
	})
	if err != nil {
		return err
	}
	if print {
		q, err := database.CleanupSQL(cfg.Database.Type, tablePrefix(cfg.Database), nil)
		if err != nil {
			return err
		}
		return printSQL(c.out, c.res, tablePrefix(cfg.Database).Script(cleanupComment(tablePrefix(cfg.Database))+q))
	}
	ctx, done, db, err := connect(cfg.Database, 5*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return dbCleanup(ctx, db, cfg.Database, dry, c)
}

// cleanupComment lists the tables of the -print SQL with why.
func cleanupComment(pfx database.Prefix) string {
	var b strings.Builder
	b.WriteString("-- gamewe db cleanup: the tables gameWE no longer uses (only those that exist are dropped)\n")
	for _, t := range database.OldTableList(pfx) {
		fmt.Fprintf(&b, "--   %s: %s\n", pfx.Table(t.Table), t.Why)
	}
	return b.String()
}

func dbCleanup(ctx context.Context, db *database.DB, d config.Database, dry bool, c dbIO) error {
	p, err := database.PlanMigrations(ctx, db)
	if err != nil {
		return err
	}
	if !p.UpToDate() {
		return fmt.Errorf("the schema of the database %s is not up to date (%d steps pending): run %s first, then the cleanup", d.Name, len(p.Steps), migrateCommand())
	}
	old, unknown, err := database.OldTables(ctx, db)
	if err != nil {
		return err
	}
	if len(old) == 0 {
		fmt.Fprintf(c.out, "The database %s has no old tables: nothing to clean up.\n", d.Name)
		printUnknown(c, unknown)
		return nil
	}
	var total int64
	fmt.Fprintf(c.out, "gameWE no longer uses these %d tables of the database %s:\n\n", len(old), d.Name)
	w := tabwriter.NewWriter(c.out, 0, 2, 3, ' ', 0)
	fmt.Fprintln(w, "  TABLE\tROWS\tWHY")
	for _, t := range old {
		name := db.Prefix.Table(t.Table)
		fmt.Fprintf(w, "  %s\t%d\t%s\n", name, t.Rows, t.Why)
		total += t.Rows
		if c.res != nil {
			c.res.Tables = append(c.res.Tables, tableDoc{name, t.Rows})
		}
	}
	w.Flush()
	fmt.Fprintf(c.out, "\n  %d tables, %d rows\n", len(old), total)
	printUnknown(c, unknown)
	if dry {
		fmt.Fprintln(c.out, "dry run: nothing changed")
		return nil
	}
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, backupFor(d, "cleanup", time.Now().Format("20060102-1504")))
	if !c.interactive {
		return errCleanupNoTTY
	}
	ok, err := typed(ctx, c, fmt.Sprintf("This drops the %d tables above with all their %d rows and cannot be undone.\nType the database name (%s) to drop them: ", len(old), total, d.Name), d.Name)
	if err != nil {
		return err
	}
	if !ok {
		return exitError{1, "not confirmed, nothing dropped"}
	}
	names := make([]string, len(old))
	for i, t := range old {
		names[i] = t.Table
	}
	if err := database.DropOldTables(ctx, db, names); err != nil {
		var re *database.DropRightsError
		if errors.As(err, &re) {
			return fmt.Errorf("%w.\nThe collector user has no DROP right (DATABASE.md). Run it as a database admin:\n  %s", err, cleanupHint(d))
		}
		return err
	}
	fmt.Fprintf(c.out, "done: %d old tables dropped.\n", len(old))
	return nil
}

func printUnknown(c dbIO, unknown []string) {
	if len(unknown) > 0 {
		fmt.Fprintf(c.out, "\nLeft alone (neither gameWE's nor known as old, drop them by hand if you are sure): %s\n", strings.Join(unknown, ", "))
	}
}

func cleanupHint(d config.Database) string {
	if d.Type == config.Postgres {
		return fmt.Sprintf("%s -print | psql -U postgres -d %s", dbCommand("cleanup"), d.Name)
	}
	return fmt.Sprintf("%s -print | mariadb -u root -p %s", dbCommand("cleanup"), d.Name)
}

// oldTablesHint is the line of gamewe db status and migrate about the old
// tables ("" when there are none).
func oldTablesHint(p *database.Plan) string {
	if len(p.Old) == 0 {
		return ""
	}
	names := make([]string, len(p.Old))
	for i, t := range p.Old {
		names[i] = t.Table
	}
	return fmt.Sprintf("%d tables gameWE no longer uses (%s): %s lists them and drops them after a confirmation",
		len(p.Old), strings.Join(names, ", "), dbCommand("cleanup"))
}
