// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// MinPurgeDays is the smallest -older-than of gamewe db purge: the daily
// awards and the last days' pages read the events.
const MinPurgeDays = 7

// dbIO is where a maintenance command reads its confirmation and writes.
type dbIO struct {
	in          io.Reader
	out         io.Writer
	interactive bool
	res         *result // the document of -o json|yaml; nil for the text
}

func stdIO() dbIO { return dbIO{in: os.Stdin, out: os.Stdout, interactive: stdinIsTerminal()} }

// typed asks to type want to confirm; only exactly want (spaces around
// ignored) says yes.
func typed(ctx context.Context, c dbIO, prompt, want string) (bool, error) {
	if !c.interactive {
		return false, errNoTTYYes
	}
	line, err := ask(ctx, c.in, promptOut(c.out), prompt)
	if ctx.Err() != nil {
		fmt.Fprintln(promptOut(c.out))
		return false, err
	}
	return strings.TrimSpace(line) == want, nil
}

// connect opens the database of d and checks that it answers; the context
// ends after the connect timeout plus extra, or on Ctrl-C / SIGTERM.
func connect(d config.Database, extra time.Duration) (context.Context, func(), *database.DB, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.ConnectTimeout)*time.Second+extra)
	done := func() { cancel(); stop() }
	db, err := openChecked(ctx, d)
	if err != nil {
		done()
		return nil, nil, nil, err
	}
	return ctx, func() { db.Close(); done() }, db, nil
}

// printRows writes the rows per table (and puts them into the document),
// with the table names of the database (prefix).
func (c dbIO) printRows(rows []database.TableRows, pfx database.Prefix) int64 {
	w := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', tabwriter.AlignRight)
	var total int64
	for _, r := range rows {
		fmt.Fprintf(w, "%d\t  %s\t\n", r.Rows, pfx.Table(r.Table))
		total += r.Rows
		if c.res != nil {
			c.res.Tables = append(c.res.Tables, tableDoc{pfx.Table(r.Table), r.Rows})
		}
	}
	w.Flush()
	return total
}

// gamewe db init: the tables of a new, empty database.
func dbInitCmd(args []string, c dbIO) error {
	var cf changeFlags
	cfg, err := loadConfig("db init", args, func(fs *flag.FlagSet) { cf.add(fs, "only print the plan") })
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	ctx, done, db, err := connect(cfg.Database, 10*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return dbInit(ctx, db, cfg.Database, cf, c)
}

func dbInit(ctx context.Context, db *database.DB, d config.Database, cf changeFlags, c dbIO) error {
	own, other, err := database.OwnTables(ctx, db)
	if err != nil {
		return err
	}
	if n := len(own) + len(other); n > 0 {
		return fmt.Errorf("the database %s is not empty (%d gameWE / HLstatsX tables): db init only creates a new database. "+
			"To bring it to this version: %s; to empty it: %s", d.Name, n, migrateCommand(), dbCommand("reset"))
	}
	err = migrate(ctx, db, d, migrateFlags{force: cf.force, dryRun: cf.dryRun, question: "Create the tables now?"}, c.in, c.out, c.interactive, c.res)
	var ee exitError
	if cf.dryRun && errors.As(err, &ee) && ee.code == 2 {
		return nil // pending is what a dry run of a new database shows
	}
	return err
}

// gamewe db drop: all tables of the schema away.
func dbDropCmd(args []string, c dbIO) error {
	var print bool
	var cf changeFlags
	cfg, err := loadConfig("db drop", args, func(fs *flag.FlagSet) {
		cf.add(fs, "only list the tables and their rows")
		fs.BoolVar(&print, "print", false, "only print the SQL (to run it as a database admin)")
	})
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	yes, dry := cf.force, cf.dryRun
	if print {
		q, err := database.DropSQL(cfg.Database.Type, tablePrefix(cfg.Database))
		if err != nil {
			return err
		}
		return printSQL(c.out, c.res, tablePrefix(cfg.Database).Script(q))
	}
	ctx, done, db, err := connect(cfg.Database, 5*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return dbDrop(ctx, db, cfg.Database, yes, dry, c)
}

func dbDrop(ctx context.Context, db *database.DB, d config.Database, yes, dry bool, c dbIO) error {
	own, other, err := database.OwnTables(ctx, db)
	if err != nil {
		return err
	}
	if len(own) == 0 {
		fmt.Fprintf(c.out, "the database %s has no gameWE tables: nothing to drop\n", d.Name)
		return nil
	}
	rows, err := database.CountRows(ctx, db, own)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "drop %d tables of the database %s (rows per table):\n", len(own), d.Name)
	total := c.printRows(rows, db.Prefix)
	if len(other) > 0 {
		fmt.Fprintf(c.out, "not dropped (not part of the gameWE schema): %s\n", strings.Join(other, ", "))
	}
	if dry {
		fmt.Fprintln(c.out, "dry run: nothing changed")
		return nil
	}
	if err := refuseRunning(ctx, db, promptOut(c.out), "drop the tables"); err != nil {
		return err
	}
	fmt.Fprintln(c.out, backupFor(d, "drop", time.Now().Format("20060102-1504")))
	if !yes {
		ok, err := typed(ctx, c, fmt.Sprintf("This deletes all %d rows and cannot be undone. Type the database name (%s) to drop the tables: ", total, d.Name), d.Name)
		if err != nil {
			return err
		}
		if !ok {
			return exitError{1, "not confirmed, nothing changed"}
		}
	}
	if err := database.DropTables(ctx, db); err != nil {
		var re *database.DropRightsError
		if errors.As(err, &re) {
			return fmt.Errorf("%w.\nThe collector user has no DROP right (DATABASE.md). Run it as a database admin:\n  %s", err, dropHint(d))
		}
		return err
	}
	fmt.Fprintf(c.out, "done: %d tables dropped. %s creates them again (gamewe run does it at start with database.auto_migrate).\n", len(own), dbCommand("init"))
	return nil
}

func dropHint(d config.Database) string {
	if d.Type == config.Postgres {
		return fmt.Sprintf("%s -print | psql -U postgres -d %s", dbCommand("drop"), d.Name)
	}
	return fmt.Sprintf("%s -print | mariadb -u root -p %s", dbCommand("drop"), d.Name)
}

// gamewe db reset: every table empty, the reference data again.
func dbResetCmd(args []string, c dbIO) error {
	var cf changeFlags
	cfg, err := loadConfig("db reset", args, func(fs *flag.FlagSet) { cf.add(fs, "only list the tables and their rows") })
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	yes, dry := cf.force, cf.dryRun
	ctx, done, db, err := connect(cfg.Database, 30*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return dbReset(ctx, db, cfg.Database, yes, dry, c)
}

func dbReset(ctx context.Context, db *database.DB, d config.Database, yes, dry bool, c dbIO) error {
	p, err := database.PlanMigrations(ctx, db)
	if err != nil {
		return err
	}
	if p.Kind == database.KindEmpty {
		return fmt.Errorf("the database %s has no gameWE tables yet: create them with %s", d.Name, dbCommand("init"))
	}
	if !p.UpToDate() {
		return fmt.Errorf("the database is not up to date (schema version %d, needs %d): run %s first", p.Current, p.Latest, migrateCommand())
	}
	own, _, err := database.OwnTables(ctx, db)
	if err != nil {
		return err
	}
	var data []string
	for _, t := range own {
		if t != database.MigrationsTable {
			data = append(data, t)
		}
	}
	rows, err := database.CountRows(ctx, db, data)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "reset the database %s: empty all %d tables (rows per table), then load the reference data again:\n", d.Name, len(data))
	total := c.printRows(rows, db.Prefix)
	fmt.Fprintln(c.out, "Everything goes: players, statistics, events, awards, clans, game servers, settings (hlstats_Options), admin logins (hlstats_Users)\n"+
		"and own games or changed definitions. The tables and the schema versions stay. Only one ranking: gamewe reset GAME -mode all.")
	if dry {
		fmt.Fprintln(c.out, "dry run: nothing changed")
		return nil
	}
	if err := refuseRunning(ctx, db, promptOut(c.out), "reset the database"); err != nil {
		return err
	}
	fmt.Fprintln(c.out, backupFor(d, "reset", time.Now().Format("20060102-1504")))
	if !yes {
		ok, err := typed(ctx, c, fmt.Sprintf("This deletes all %d rows and cannot be undone. Type the database name (%s) to reset it: ", total, d.Name), d.Name)
		if err != nil {
			return err
		}
		if !ok {
			return exitError{1, "not confirmed, nothing changed"}
		}
	}
	fmt.Fprintln(c.out, "resetting ...")
	if err := database.ResetData(ctx, db); err != nil {
		return fmt.Errorf("reset stopped (run it again to complete it): %w", err)
	}
	fmt.Fprintln(c.out, "done: the database is as new (reference data loaded). Add the game servers and an admin login again.")
	return nil
}

// parseDays reads -older-than: days, as 90 or 90d.
func parseDays(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(s), "d"))
	if err != nil || n < MinPurgeDays {
		return 0, fmt.Errorf("-older-than %q: want a number of days, at least %d, e.g. 90 or 90d", s, MinPurgeDays)
	}
	return n, nil
}

// gamewe db purge: the events older than some days.
func dbPurgeCmd(args []string, c dbIO) error {
	var older string
	var cf changeFlags
	cfg, err := loadConfig("db purge", args, func(fs *flag.FlagSet) {
		fs.StringVar(&older, "older-than", "", fmt.Sprintf("delete the events older than this many days (at least %d), e.g. 90 or 90d", MinPurgeDays))
		cf.add(fs, "only count the rows")
	})
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	yes, dry := cf.force, cf.dryRun
	days, err := parseDays(older)
	if err != nil {
		return exitError{2, err.Error() + "\n\n" + dbUsage}
	}
	ctx, done, db, err := connect(cfg.Database, 6*time.Hour)
	if err != nil {
		return err
	}
	defer done()
	return dbPurge(ctx, db, cfg.Database, days, yes, dry, c)
}

func dbPurge(ctx context.Context, db *database.DB, d config.Database, days int, yes, dry bool, c dbIO) error {
	if p, err := database.PlanMigrations(ctx, db); err != nil {
		return err
	} else if !p.UpToDate() {
		return fmt.Errorf("the database is not up to date (schema version %d, needs %d): run %s first", p.Current, p.Latest, migrateCommand())
	}
	before := time.Now().AddDate(0, 0, -days)
	rows, err := database.Purge(ctx, db, before, true)
	if err != nil {
		return err
	}
	// The finished jobs older than stats.JobKeep, as the collector deletes
	// them every hour.
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	jobsBefore := time.Now().Add(-stats.JobKeep)
	jobs, err := jobsTable(ctx, db, store)
	if err != nil {
		return err
	}
	if jobs {
		n, err := store.CountOldJobs(ctx, jobsBefore)
		if err != nil {
			return err
		}
		rows = append(rows, database.TableRows{Table: "hlstats_gamewe_jobs", Rows: n})
	}
	fmt.Fprintf(c.out, "events before %s (older than %d days), rows per table:\n", before.UTC().Format("2006-01-02 15:04 UTC"), days)
	total := c.printRows(rows, db.Prefix)
	if jobs {
		fmt.Fprintf(c.out, db.Prefix.Table("hlstats_gamewe_jobs")+": the finished jobs older than %d days (as the collector does every hour).\n", int(stats.JobKeep.Hours()/24))
	}
	fmt.Fprintln(c.out, "Players, their totals, rankings, awards and the history per day stay; the event lists of the web pages lose these rows.")
	if dry || total == 0 {
		if total == 0 {
			fmt.Fprintln(c.out, "nothing to delete")
		} else {
			fmt.Fprintln(c.out, "dry run: nothing changed")
		}
		return nil
	}
	if !yes {
		if !c.interactive {
			return errNoTTYYes
		}
		line, _ := ask(ctx, c.in, promptOut(c.out), fmt.Sprintf("Delete %d rows? This cannot be undone. [y/N] ", total))
		if !saysYes(line) {
			fmt.Fprintln(promptOut(c.out))
			return exitError{1, "not confirmed, nothing changed"}
		}
	}
	fmt.Fprintln(c.out, "deleting ...")
	res, err := database.Purge(ctx, db, before, false)
	var n int64
	for _, r := range res {
		n += r.Rows
	}
	if err == nil && jobs {
		var old int64
		if old, err = store.CountOldJobs(ctx, jobsBefore); err == nil {
			if err = store.DeleteOldJobs(ctx, jobsBefore); err == nil {
				n += old
			}
		}
	}
	if err != nil {
		return fmt.Errorf("purge stopped after %d rows (run it again to complete it): %w", n, err)
	}
	fmt.Fprintf(c.out, "done: %d rows deleted. To give the space back to the disk: %s\n", n, dbCommand("optimize"))
	return nil
}

// gamewe db optimize: the space of deleted rows back.
func dbOptimizeCmd(args []string, c dbIO) error {
	var cf changeFlags
	cfg, err := loadConfig("db optimize", args, func(fs *flag.FlagSet) { cf.add(fs, "only list the tables") })
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	ctx, done, db, err := connect(cfg.Database, 6*time.Hour)
	if err != nil {
		return err
	}
	defer done()
	own, _, err := database.OwnTables(ctx, db)
	if err != nil {
		return err
	}
	if len(own) == 0 {
		return fmt.Errorf("the database %s has no gameWE tables", cfg.Database.Name)
	}
	verb := "OPTIMIZE TABLE"
	if db.Postgres() {
		verb = "VACUUM ANALYZE"
	}
	fmt.Fprintf(c.out, "%s of %d tables of the database %s\n", verb, len(own), cfg.Database.Name)
	if !db.Postgres() {
		fmt.Fprintln(c.out, "OPTIMIZE TABLE rebuilds each table (and locks it on MyISAM): the collector waits meanwhile (best at a quiet time).")
	}
	if cf.dryRun {
		for _, t := range own {
			fmt.Fprintf(c.out, "  %s\n", db.Prefix.Table(t))
		}
		fmt.Fprintln(c.out, "dry run: nothing changed")
		return nil
	}
	if err := approve(ctx, cf, c.in, c.out, c.interactive, "Optimize them now?"); err != nil {
		return err
	}
	start := time.Now()
	err = database.Optimize(ctx, db, own, func(t string) { fmt.Fprintf(c.out, "optimize %s\n", db.Prefix.Table(t)) })
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "done: %d tables optimized in %s\n", len(own), time.Since(start).Round(100*time.Millisecond))
	return nil
}
