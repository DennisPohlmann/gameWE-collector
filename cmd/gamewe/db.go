// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
  gamewe db status  [-config FILE]
        schema version, pending and applied migrations (with date)
  gamewe db grants  [-config FILE] [-print]
        grant the web users their table rights again (runs after every
        migration); -print only prints the SQL, to run it as a database admin
  gamewe db seed    [-config FILE] [-print]
        add the missing reference data (games, teams, actions, weapons,
        awards, ranks, options); existing rows stay as they are

A new database: create it with schema/init/init-db.sh (database, users,
rights), then gamewe db migrate. An upgrade: replace the binary, make a
backup, gamewe db migrate, start the collector. The collector refuses to
start while the database is not up to date.`

// exitError ends the program with code (and msg on stderr if not empty).
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

// schemaOutdated is the error of `gamewe run` on an old database.
func schemaOutdated(p *database.Plan) error {
	return fmt.Errorf("the database structure does not match the layout this collector needs (schema version %d, needs %d; %d steps pending). Make a backup, then run: %s",
		p.Current, p.Latest, len(p.Steps), migrateCommand())
}

func dbCmd(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Println(dbUsage)
		return nil
	}
	switch args[0] {
	case "migrate":
		return dbMigrateCmd(args[1:], os.Stdin, os.Stdout, stdinIsTerminal())
	case "status":
		return dbStatusCmd(args[1:], os.Stdout)
	case "grants":
		return dbGrantsCmd(args[1:], os.Stdout)
	case "seed":
		return dbSeedCmd(args[1:], os.Stdout)
	}
	return exitError{2, fmt.Sprintf("unknown command %q\n\n%s", "db "+args[0], dbUsage)}
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
func confirm(in io.Reader, out io.Writer, interactive bool) (bool, error) {
	if !interactive {
		return false, errNoTTY
	}
	fmt.Fprint(out, "Backup made? Migrate now? [y/N] ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "j", "ja":
		return true, nil
	}
	return false, nil
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
func backupAdvice(d config.Database, stamp string) string {
	if d.Type == config.Postgres {
		return fmt.Sprintf("Make a backup before migrating, e.g.:\n  pg_dump -U postgres -h %s -Fc %s > %s-before-migrate-%s.dump", d.Host, d.Name, d.Name, stamp)
	}
	return fmt.Sprintf("Make a backup before migrating, e.g.:\n  mariadb-dump -u root -p -h %s --single-transaction --routines %s > %s-before-migrate-%s.sql", d.Host, d.Name, d.Name, stamp)
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
	return migrate(ctx, db, cfg.Database, f, in, out, interactive)
}

// migrate is gamewe db migrate after the flags and the connection.
func migrate(ctx context.Context, db *database.DB, d config.Database, f migrateFlags, in io.Reader, out io.Writer, interactive bool) error {
	p, err := database.PlanMigrations(ctx, db)
	if err != nil {
		return err
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
	fmt.Fprintln(out)
	fmt.Fprintln(out, backupAdvice(d, time.Now().Format("20060102-1504")))
	fmt.Fprintln(out, "Stop the collector while migrating: MyISAM tables are locked while they are changed.")
	if !f.force {
		ok, err := confirm(in, out, interactive)
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

func dbGrantsCmd(args []string, out io.Writer) error {
	var print bool
	cfg, err := loadConfig("db grants", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&print, "print", false, "only print the SQL (to run it as a database admin)")
	})
	if err != nil {
		return err
	}
	if print {
		fmt.Fprint(out, schema.Grants(cfg.Database.Type))
		return nil
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

func dbSeedCmd(args []string, out io.Writer) error {
	var print bool
	cfg, err := loadConfig("db seed", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&print, "print", false, "only print the SQL")
	})
	if err != nil {
		return err
	}
	if print {
		fmt.Fprint(out, schema.Seed(cfg.Database.Type))
		return nil
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
