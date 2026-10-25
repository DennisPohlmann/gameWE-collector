// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/schema"
)

const dbUsage = `usage:
  gamewe db migrate [-config FILE] [-f|--force] [-n|--dry-run]
        bring the database to the schema of this collector: shows the plan,
        recommends a backup and asks "Backup made? Migrate now? [y/N]"
        -f, --force    no question (scripts); the plan is printed anyway
        -n, --dry-run  only print the plan; exit code 0: up to date,
                       2: migration pending, 1: error
        gamewe run does the same at start (database.auto_migrate, default
        true), except for an HLstatsX database without schema versions
  gamewe db status  [-config FILE]
        schema version, pending and applied migrations (with date)
  gamewe db init    [-config FILE]
        create the tables, the reference data and the web users' rights of a
        new, empty database; refuses a database that has tables
  gamewe db grants  [-config FILE] [-print]
        grant the web users their table rights again (runs after every
        migration); -print only prints the SQL, to run it as a database admin
  gamewe db seed    [-config FILE] [-print]
        add the missing reference data (games, teams, actions, weapons,
        awards, ranks, options); existing rows stay as they are
  gamewe db purge   -older-than DAYS [-config FILE] [-dry-run] [-yes]
        delete the events (hlstats_Events_*) older than DAYS days (at least
        7, e.g. 90 or 90d); players, totals, rankings, awards and the history
        per day stay. Asks [y/N] unless -yes; -dry-run only counts
  gamewe db optimize [-config FILE]
        give the space of deleted rows back to the disk (OPTIMIZE TABLE,
        PostgreSQL: VACUUM ANALYZE), e.g. after purge
  gamewe db reset   [-config FILE] [-dry-run] [-yes]
        empty every table and load the reference data again: the database is
        as new (players, statistics, servers, settings, admin logins go; the
        tables stay). One ranking only: gamewe reset GAME -mode all
  gamewe db drop    [-config FILE] [-dry-run] [-yes] [-print]
        drop all tables of the gameWE schema (other tables stay); needs the
        DROP right, which the collector user has not on MariaDB: -print only
        prints the SQL, to run it as a database admin. Afterwards db init
        (or the next start) creates them again
migrate, reset and drop refuse while a collector runs on the database (stop
it first: systemctl stop gamewe-collector); reset and drop ask to type the
database name unless -yes. Make a backup first.
Every db command takes -o text|json|yaml (status: the schema as a document;
the others: ok, error, plan, rows per table, -print SQL and the text lines;
questions then go to stderr).

A new database: create it with schema/init/init-db.sh (database, users,
rights), then start the collector (it creates the tables) or run gamewe db
init. An upgrade: make a backup, replace the binary, start the collector (or
first gamewe db migrate with database.auto_migrate: false).`

// exitError ends the program with code (and msg on stderr if not empty).
// exitConfig is the exit code of gamewe run for a problem that only an
// admin fixes (config file, flags, a database schema that is not migrated
// automatically): EX_CONFIG of sysexits.h. The systemd unit does not
// restart on it (RestartPreventExitStatus).
const exitConfig = 78

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// configPath is the -config of the last loadConfig, for hints.
var configPath = config.DefaultPath

// migrateCommand is the command line that migrates the database of the
// current config.
func migrateCommand() string { return dbCommand("migrate") }

// dbCommand is the command line of gamewe db SUB for the current config.
func dbCommand(sub string) string {
	bin := "gamewe"
	if a := os.Args[0]; strings.ContainsRune(a, '/') && filepath.Base(a) == "gamewe" {
		bin = a // e.g. /opt/gamewe/collector/bin/gamewe from systemd
	}
	cmd := bin + " db " + sub
	if configPath != config.DefaultPath {
		cmd += " -config " + configPath
	}
	return cmd
}

// schemaOutdated is the error of `gamewe run` on an old database that was
// not migrated automatically.
func schemaOutdated(p *database.Plan, auto bool) error {
	why := "database.auto_migrate is false"
	if auto {
		why = "an HLstatsX database without schema versions is not migrated automatically, it is taken over once by hand"
	}
	return fmt.Errorf("the database structure does not match the layout this collector needs (schema version %d, needs %d; %d steps pending; %s). Make a backup, then run: %s",
		p.Current, p.Latest, len(p.Steps), why, migrateCommand())
}

// autoMigrates reports whether gamewe run migrates the database of plan p
// itself: database.auto_migrate is on and it is not an HLstatsX database
// that has never been migrated (that one is taken over by hand, with a
// backup: DATABASE.md).
func autoMigrates(d config.Database, p *database.Plan) bool {
	return d.AutoMigrate && (p.Versions || p.Kind != database.KindHLstatsX)
}

// autoMigrate is the migration at the start of gamewe run
// (database.auto_migrate): what gamewe db migrate -f does, with the steps
// in the log. It returns the plan afterwards; the plan p is returned
// unchanged if the database is not migrated automatically.
func autoMigrate(ctx context.Context, log *slog.Logger, db *database.DB, d config.Database, p *database.Plan) (*database.Plan, error) {
	if !autoMigrates(d, p) {
		log.Warn("database not migrated automatically: an HLstatsX database without schema versions is taken over once by hand (backup first)", "command", migrateCommand())
		return p, nil
	}
	log.Info("database schema outdated, migrating automatically (database.auto_migrate)",
		"database", describeKind(p), "version", p.Current, "needs", p.Latest, "steps", len(p.Steps))
	out := &lineLog{log: log}
	res, err := database.Migrate(ctx, db, database.MigrateOptions{Out: out, Waiting: func() {
		log.Info("another migration is running on this database, waiting for it")
	}})
	out.flush()
	if err != nil {
		return nil, fmt.Errorf("automatic migration (database.auto_migrate) failed; every step is idempotent, the next start (or gamewe db migrate) goes on from here: %w", err)
	}
	for _, w := range res.Warnings {
		log.Warn("rights of the web users: " + w)
	}
	if len(res.Warnings) > 0 {
		log.Warn("the schema is up to date; give the web users their rights as a database admin: " + grantsHint(d))
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	after, err := database.PlanMigrations(cctx, db)
	if err != nil {
		return nil, fmt.Errorf("database: %w", database.Explain(err))
	}
	if after.UpToDate() {
		log.Info("database migrated", "version", after.Current)
	}
	return after, nil
}

// lineLog writes every complete line written to it as one info record.
type lineLog struct {
	log *slog.Logger
	buf []byte
}

func (l *lineLog) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		if line := strings.TrimSpace(string(l.buf[:i])); line != "" {
			l.log.Info("migrate: " + line)
		}
		l.buf = l.buf[i+1:]
	}
}

// flush logs a last line without newline.
func (l *lineLog) flush() {
	if line := strings.TrimSpace(string(l.buf)); line != "" {
		l.log.Info("migrate: " + line)
	}
	l.buf = nil
}

func dbCmd(args []string) error {
	args, err := globalFlags(args) // gamewe db -o json status
	if err != nil {
		return exitError{2, err.Error()}
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Println(dbUsage)
		return nil
	}
	// -o among the subcommand's flags decides before the command runs
	// whether its text is captured into a document (FlagSet.Parse sets it
	// again, the same).
	presetOutput(args[1:])
	maint := func(cmd func([]string, dbIO) error) error {
		return captured(os.Stdout, func(w io.Writer, r *result) error {
			c := stdIO()
			c.out, c.res = w, r
			return cmd(args[1:], c)
		})
	}
	switch args[0] {
	case "migrate":
		return dbMigrateCmd(args[1:], os.Stdin, os.Stdout, stdinIsTerminal())
	case "status":
		return dbStatusCmd(args[1:], os.Stdout)
	case "grants":
		return captured(os.Stdout, func(w io.Writer, r *result) error { return dbGrantsCmd(args[1:], w, r) })
	case "seed":
		return captured(os.Stdout, func(w io.Writer, r *result) error { return dbSeedCmd(args[1:], w, r) })
	case "init":
		return maint(dbInitCmd)
	case "drop":
		return maint(dbDropCmd)
	case "reset":
		return maint(dbResetCmd)
	case "purge":
		return maint(dbPurgeCmd)
	case "optimize":
		return maint(dbOptimizeCmd)
	}
	return exitError{2, fmt.Sprintf("unknown command %q\n\n%s", "db "+args[0], dbUsage)}
}

// presetOutput sets the output format from -o / --output among args (up
// to "--"); errors are reported when the flags are parsed.
func presetOutput(args []string) {
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		name, val, hasVal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || (name != "o" && name != "output") {
			continue
		}
		if !hasVal {
			if i+1 == len(args) {
				return
			}
			i++
			val = args[i]
		}
		formatFlag{}.Set(val)
	}
}

// stdinIsTerminal reports whether stdin is a terminal (someone can answer).
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// migrateFlags are the flags of gamewe db migrate.
type migrateFlags struct {
	config        string
	force, dryRun bool
}

func parseMigrateFlags(args []string, errOut io.Writer) (migrateFlags, error) {
	var f migrateFlags
	fs := flag.NewFlagSet("db migrate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&f.config, "config", config.DefaultPath, "path to the YAML config file")
	fs.BoolVar(&f.force, "force", false, "migrate without asking (the plan is printed anyway)")
	fs.BoolVar(&f.force, "f", false, "short for -force")
	fs.BoolVar(&f.dryRun, "dry-run", false, "only print the plan; exit code 2 if a migration is pending")
	fs.BoolVar(&f.dryRun, "n", false, "short for -dry-run")
	addOutputFlag(fs)
	if err := fs.Parse(args); err != nil {
		return f, exitError{2, ""}
	}
	if fs.NArg() > 0 {
		return f, exitError{2, fmt.Sprintf("unexpected argument %q\n\n%s", fs.Arg(0), dbUsage)}
	}
	if f.force && f.dryRun {
		return f, exitError{2, "-force and -dry-run cannot be used together"}
	}
	return f, nil
}

// errNoTTY is returned when the question cannot be asked.
var errNoTTY = errors.New("no interactive confirmation possible (stdin is not a terminal): use -force (-f) to migrate without asking, or -dry-run (-n) to see the plan")

// confirm asks whether to migrate now; only y, yes, j or ja (any case)
// say yes, everything else (also an empty line or end of input) is no.
// Ctrl-C (ctx ending) is no as well.
func confirm(ctx context.Context, in io.Reader, out io.Writer, interactive bool) (bool, error) {
	if !interactive {
		return false, errNoTTY
	}
	line, err := ask(ctx, in, out, "Backup made? Migrate now? [y/N] ")
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false, nil
	}
	return saysYes(line), nil
}

// saysYes reports whether an answer says yes: y, yes, j or ja (any case).
func saysYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "j", "ja":
		return true
	}
	return false
}

// ask writes prompt to out and reads the answer line from in; on a
// terminal Enter always ends the line (lineTerminal), Ctrl-C ends the
// question (ctx).
func ask(ctx context.Context, in io.Reader, out io.Writer, prompt string) (string, error) {
	if f, ok := in.(*os.File); ok {
		defer lineTerminal(f, out)()
	}
	fmt.Fprint(out, prompt)
	return readAnswer(ctx, in)
}

// readAnswer reads one answer line from in. The line ends at LF or CR (a
// terminal in raw mode, a script that sends CR). It
// returns ctx.Err() when ctx ends first (Ctrl-C while the question waits),
// io.EOF at the end of input; the read itself goes on in the background
// until in ends or the program exits.
func readAnswer(ctx context.Context, in io.Reader) (string, error) {
	type answer struct {
		line string
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		var b [1]byte
		var line []byte
		for len(line) < 1024 {
			n, err := in.Read(b[:])
			if n == 1 {
				if b[0] == '\n' || b[0] == '\r' {
					ch <- answer{string(line), nil}
					return
				}
				line = append(line, b[0])
			}
			if err != nil {
				ch <- answer{string(line), err}
				return
			}
		}
		ch <- answer{string(line), nil}
	}()
	select {
	case a := <-ch:
		return a.line, a.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// printPlan writes the plan of a migration.
func printPlan(out io.Writer, p *database.Plan) {
	fmt.Fprintf(out, "database:  %s, schema version %d, this collector needs %d\n", describeKind(p), p.Current, p.Latest)
	for _, w := range p.Warnings {
		fmt.Fprintf(out, "warning:   %s\n", w)
	}
	if p.UpToDate() {
		return
	}
	for _, m := range p.Pending {
		fmt.Fprintf(out, "pending:   v%d %s: %s\n", m.Version, m.Name, m.Desc)
	}
	fmt.Fprintf(out, "plan (%d steps):\n", len(p.Steps))
	for i, s := range p.Steps {
		label := s.Name
		if s.Version > 0 {
			label = fmt.Sprintf("v%d %s", s.Version, s.Name)
		}
		fmt.Fprintf(out, "  %2d. [%s] %s", i+1, label, s.What)
		if s.Rows > 0 {
			fmt.Fprintf(out, " (about %d rows)", s.Rows)
		}
		fmt.Fprintln(out)
		if s.Slow {
			fmt.Fprintf(out, "      %s\n", s.SlowNote())
		}
	}
}

func describeKind(p *database.Plan) string {
	switch {
	case p.Kind == database.KindEmpty:
		return "new (no gameWE tables yet)"
	case !p.Versions:
		return "existing " + p.Kind + " database without schema versions"
	}
	return p.Kind
}

// backupAdvice is the backup recommendation with an example command.
func backupAdvice(d config.Database, stamp string) string { return backupFor(d, "migrate", stamp) }

// backupFor is the backup recommendation before gamewe db what.
func backupFor(d config.Database, what, stamp string) string {
	ing := map[string]string{"migrate": "migrating", "drop": "dropping", "reset": "resetting"}[what]
	if d.Type == config.Postgres {
		return fmt.Sprintf("Make a backup before %s, e.g.:\n  pg_dump -U postgres -h %s -Fc %s > %s-before-%s-%s.dump", ing, d.Host, d.Name, d.Name, what, stamp)
	}
	return fmt.Sprintf("Make a backup before %s, e.g.:\n  mariadb-dump -u root -p -h %s --single-transaction --routines %s > %s-before-%s-%s.sql", ing, d.Host, d.Name, d.Name, what, stamp)
}

func openChecked(ctx context.Context, d config.Database) (*database.DB, error) {
	db, err := database.Open(d)
	if err != nil {
		return nil, err
	}
	if _, err := database.Inspect(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s:%d/%s not reachable: %w", d.Host, d.Port, d.Name, database.Explain(err))
	}
	return db, nil
}

func dbMigrateCmd(args []string, in io.Reader, out io.Writer, interactive bool) error {
	f, err := parseMigrateFlags(args, os.Stderr)
	if err != nil {
		return err
	}
	return captured(out, func(out io.Writer, r *result) error { return dbMigrate(f, in, out, interactive, r) })
}

func dbMigrate(f migrateFlags, in io.Reader, out io.Writer, interactive bool, r *result) error {
	cfg, err := config.Load(f.config)
	if err != nil {
		return fmt.Errorf("config %s: %w", f.config, err)
	}
	configPath = f.config
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.ConnectTimeout+5)*time.Second)
	db, err := openChecked(cctx, cfg.Database)
	cancel()
	if err != nil {
		return err
	}
	defer db.Close()
	return migrate(ctx, db, cfg.Database, f, in, out, interactive, r)
}

// migrate is gamewe db migrate after the flags and the connection; r is
// the document of -o json|yaml (nil for the text).
func migrate(ctx context.Context, db *database.DB, d config.Database, f migrateFlags, in io.Reader, out io.Writer, interactive bool, r *result) error {
	p, err := database.PlanMigrations(ctx, db)
	if err != nil {
		return err
	}
	if r != nil {
		r.Plan = newPlanDoc(p)
	}
	printPlan(out, p)
	if p.UpToDate() {
		fmt.Fprintln(out, "The database is up to date.")
		return nil
	}
	if why, err := database.PostgresRights(ctx, db); err != nil {
		return err
	} else if why != "" {
		fmt.Fprintf(out, "warning:   %s\n", why)
	}
	if f.dryRun {
		fmt.Fprintf(out, "Dry run: nothing changed. %d steps pending.\n", len(p.Steps))
		return exitError{2, ""}
	}
	if p.Kind != database.KindEmpty { // a new database has nothing to back up
		// A running collector writes into the tables a migration changes
		// (MyISAM tables are locked while they change): stop it first.
		if err := refuseRunning(ctx, db, promptOut(out), "migrate"); err != nil {
			return err
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, backupAdvice(d, time.Now().Format("20060102-1504")))
	}
	if !f.force {
		ok, err := confirm(ctx, in, promptOut(out), interactive)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled: nothing changed.")
			return exitError{1, ""}
		}
	}
	fmt.Fprintln(out, "migrating ...")
	res, err := database.Migrate(ctx, db, database.MigrateOptions{Out: out, Waiting: func() {
		fmt.Fprintln(out, "another gamewe db migrate is running on this database, waiting for it ...")
	}})
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "warning:   rights of the web users: %s\n", w)
	}
	if len(res.Warnings) > 0 {
		fmt.Fprintf(out, "           the schema is up to date; give the web users their rights as a database admin:\n           %s\n", grantsHint(d))
	}
	if res.Plan.UpToDate() {
		fmt.Fprintln(out, "The database is up to date (another gamewe db migrate did it meanwhile).")
		return nil
	}
	fmt.Fprintf(out, "done: the database has schema version %d.\n", database.Latest())
	return nil
}

func grantsHint(d config.Database) string {
	if d.Type == config.Postgres {
		return fmt.Sprintf("%s -print | psql -U postgres -d %s", dbCommand("grants"), d.Name)
	}
	return fmt.Sprintf("%s -print | mariadb -u root -p %s", dbCommand("grants"), d.Name)
}

func dbStatusCmd(args []string, out io.Writer) error {
	cfg, err := loadConfig("db status", args, nil)
	if err != nil {
		return err
	}
	if err := needList(false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Database.ConnectTimeout+30)*time.Second)
	defer cancel()
	db, err := openChecked(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	p, err := database.PlanMigrations(ctx, db)
	if err != nil {
		return err
	}
	if structured() {
		return emit(out, newPlanDoc(p))
	}
	printStatus(out, p)
	return nil
}

// printStatus writes gamewe db status.
func printStatus(out io.Writer, p *database.Plan) {
	state := "up to date"
	if !p.UpToDate() {
		state = fmt.Sprintf("NOT up to date: %d steps pending, run %s (backup first)", len(p.Steps), migrateCommand())
	}
	fmt.Fprintf(out, "database:  %s\n", describeKind(p))
	fmt.Fprintf(out, "schema:    version %d, this collector needs %d: %s\n", p.Current, p.Latest, state)
	for _, w := range p.Warnings {
		fmt.Fprintf(out, "warning:   %s\n", w)
	}
	if len(p.Applied) == 0 {
		fmt.Fprintln(out, "applied:   none")
	}
	for i, a := range p.Applied {
		head := "applied:  "
		if i > 0 {
			head = "          "
		}
		fmt.Fprintf(out, "%s v%d %-14s %s UTC  %s\n", head, a.Version, a.Name, a.AppliedAt.Format("2006-01-02 15:04:05"), a.Note)
	}
	if len(p.Pending) == 0 {
		fmt.Fprintln(out, "pending:   none")
	}
	for i, m := range p.Pending {
		head := "pending:  "
		if i > 0 {
			head = "          "
		}
		fmt.Fprintf(out, "%s v%d %s: %s\n", head, m.Version, m.Name, m.Desc)
	}
	for _, r := range p.Missing {
		fmt.Fprintf(out, "missing:   %s\n", strings.TrimPrefix(r.String(), "schema incomplete: missing "))
	}
}

func dbGrantsCmd(args []string, out io.Writer, r *result) error {
	var print bool
	cfg, err := loadConfig("db grants", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&print, "print", false, "only print the SQL (to run it as a database admin)")
	})
	if err != nil {
		return err
	}
	if print {
		return printSQL(out, r, schema.Grants(cfg.Database.Type))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Database.ConnectTimeout+60)*time.Second)
	defer cancel()
	db, err := openChecked(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	warn := database.RunGrants(ctx, db, cfg.Database.Type)
	for _, w := range warn {
		fmt.Fprintf(out, "failed:    %s\n", w)
	}
	if len(warn) > 0 {
		return fmt.Errorf("%d grants failed; run as a database admin: %s", len(warn), grantsHint(cfg.Database))
	}
	fmt.Fprintln(out, "rights of the web users granted")
	return nil
}

func dbSeedCmd(args []string, out io.Writer, r *result) error {
	var print bool
	cfg, err := loadConfig("db seed", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&print, "print", false, "only print the SQL")
	})
	if err != nil {
		return err
	}
	if print {
		return printSQL(out, r, schema.Seed(cfg.Database.Type))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Database.ConnectTimeout+120)*time.Second)
	defer cancel()
	db, err := openChecked(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.RunSeed(ctx, db); err != nil {
		return err
	}
	fmt.Fprintln(out, "missing reference data added")
	return nil
}

// printSQL writes the SQL of -print, or puts it into the document.
func printSQL(out io.Writer, r *result, sql string) error {
	if r != nil {
		r.SQL = sql
		return nil
	}
	_, err := fmt.Fprint(out, sql)
	return err
}

// promptOut is where a question goes: out for the text, stderr when
// stdout carries a document.
func promptOut(out io.Writer) io.Writer {
	if structured() {
		return os.Stderr
	}
	return out
}

// obsoleteSQL lists SQL files of older packages next to the binary
// (/opt/gamewe/collector/doc/schema/...). The collector does not read them:
// the schema is compiled in.
func obsoleteSQL() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Join(filepath.Dir(exe), "..", "doc", "schema")
	var out []string
	for _, f := range []string{"hlstatsx.sql", "init/01_schema.sql", "init/02_seed.sql", "init/03_grants.sql",
		"postgres/hlstatsx.sql", "postgres/init/01_schema.sql", "postgres/init/02_seed.sql", "postgres/init/03_grants.sql"} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); err == nil {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}
