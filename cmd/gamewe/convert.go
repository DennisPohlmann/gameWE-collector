// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/convert"
	"gamewe-collector/internal/database"
)

// fromPasswordEnv holds the password of the -from user (never on the
// command line, where every user of the host sees it).
const fromPasswordEnv = "GAMEWE_FROM_PASSWORD"

var reDBName = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

// sourceConfig is the connection to the HLstatsX:CE database of -from:
// NAME (a database on the gameWE database server, with the collector's
// user) or mysql://[user@]host[:port]/NAME (the password in
// GAMEWE_FROM_PASSWORD). TLS and timeouts are those of the config.
func sourceConfig(d config.Database, from string, getenv func(string) string) (config.Database, error) {
	src := d
	from = strings.TrimSpace(from)
	if from == "" {
		return src, errors.New("-from is missing: the HLstatsX:CE database, a name (on the gameWE database server) or mysql://user@host:port/name")
	}
	if !strings.Contains(from, "://") {
		if !reDBName.MatchString(from) {
			return src, fmt.Errorf("-from %q: not a database name", from)
		}
		src.Name = from
		return src, nil
	}
	u, err := url.Parse(from)
	if err != nil || (u.Scheme != "mysql" && u.Scheme != "mariadb") || u.Host == "" {
		return src, fmt.Errorf("-from: want mysql://[user@]host[:port]/name")
	}
	if _, has := u.User.Password(); has {
		return src, fmt.Errorf("-from: no password in the URL (the command line is visible to every user of the host); set %s", fromPasswordEnv)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !reDBName.MatchString(name) {
		return src, fmt.Errorf("-from: the URL needs the database name: mysql://[user@]host[:port]/name")
	}
	src.Name = name
	src.Host = u.Hostname()
	src.Port = 3306
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return src, fmt.Errorf("-from: port %q", p)
		}
		src.Port = n
	}
	if user := u.User.Username(); user != "" {
		src.User = user
		src.Password = getenv(fromPasswordEnv)
		if src.Password == "" {
			return src, fmt.Errorf("-from: set the password of %s in the environment variable %s", user, fromPasswordEnv)
		}
	} else if p := getenv(fromPasswordEnv); p != "" {
		src.Password = p
	}
	return src, nil
}

// convertFlags are the flags of gamewe db convert.
type convertFlags struct {
	config, from, zone, mojibake string
	yes, dryRun                  bool
}

func parseConvertFlags(args []string) (convertFlags, error) {
	var f convertFlags
	fs := flag.NewFlagSet("db convert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&f.config, "config", config.DefaultPath, "path to the YAML config file")
	fs.StringVar(&f.from, "from", "", "the HLstatsX:CE database: NAME on the gameWE database server, or mysql://[user@]host[:port]/NAME (password in "+fromPasswordEnv+")")
	fs.StringVar(&f.zone, "source-timezone", convert.ZoneServer, "time zone in which CE wrote eventTime: server (the CE server's), an IANA name such as Europe/Berlin, UTC or +01:00")
	fs.StringVar(&f.mojibake, "mojibake", convert.MojibakeAuto, "double-encoded UTF-8 (MÃ¼ller): auto (repair in latin1 tables), repair (in all tables) or keep")
	fs.BoolVar(&f.yes, "yes", false, "convert without asking (the plan is printed anyway)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "only print the plan")
	fs.BoolVar(&f.dryRun, "n", false, "short for -dry-run")
	addOutputFlag(fs)
	if err := fs.Parse(args); err != nil {
		return f, exitError{2, ""}
	}
	if fs.NArg() > 0 {
		return f, exitError{2, fmt.Sprintf("unexpected argument %q\n\n%s", fs.Arg(0), dbUsage)}
	}
	if f.yes && f.dryRun {
		return f, exitError{2, "-yes and -dry-run cannot be used together"}
	}
	return f, nil
}

// gamewe db convert: an HLstatsX:CE database into a new gameWE database.
func dbConvertCmd(args []string, c dbIO) error {
	f, err := parseConvertFlags(args)
	if err != nil {
		return err
	}
	zone, err := convert.ParseZone(f.zone)
	if err != nil {
		return exitError{2, err.Error()}
	}
	moji, err := convert.ParseMojibake(f.mojibake)
	if err != nil {
		return exitError{2, err.Error()}
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		return fmt.Errorf("config %s: %w", f.config, err)
	}
	configPath = f.config
	if cfg.Database.Type != config.MySQL {
		return convert.ErrNotMySQL
	}
	srcCfg, err := sourceConfig(cfg.Database, f.from, os.Getenv)
	if err != nil {
		return exitError{2, err.Error()}
	}
	ctx, done, dst, err := connect(cfg.Database, 48*time.Hour)
	if err != nil {
		return err
	}
	defer done()
	srcCfg.MaxOpenConns = 2
	src, err := openChecked(ctx, srcCfg)
	if err != nil {
		return fmt.Errorf("HLstatsX:CE database (-from): %w", err)
	}
	defer src.Close()
	return dbConvert(ctx, src, dst, srcCfg, cfg.Database, f, convert.Options{Zone: zone, Mojibake: moji}, c)
}

func dbConvert(ctx context.Context, src, dst *database.DB, s, d config.Database, f convertFlags, opt convert.Options, c dbIO) error {
	p, err := convert.Inspect(ctx, src, dst, opt)
	if err != nil {
		var nn *convert.NotNewError
		if errors.As(err, &nn) {
			return fmt.Errorf("%w. Create a new database (schema/init/init-db.sh) or empty this one (%s), then convert again", err, dbCommand("reset"))
		}
		return err
	}
	printConvertPlan(c, p, s, d)
	if f.dryRun {
		fmt.Fprintln(c.out, "dry run: nothing changed")
		return nil
	}
	if err := refuseRunning(ctx, dst, promptOut(c.out), "convert"); err != nil {
		return err
	}
	if !f.yes {
		if !c.interactive {
			return errNoTTYYes
		}
		line, _ := ask(ctx, c.in, promptOut(c.out), "The HLstatsX:CE database is only read. Convert now? [y/N] ")
		if !saysYes(line) {
			fmt.Fprintln(promptOut(c.out))
			return exitError{1, "not confirmed, nothing changed"}
		}
	}
	if p.TargetNew {
		fmt.Fprintln(c.out, "creating the gameWE schema ...")
		res, err := database.Migrate(ctx, dst, database.MigrateOptions{Out: c.out})
		if err != nil {
			return err
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(c.out, "warning:   rights of the web users: %s (afterwards: %s)\n", w, grantsHint(d))
		}
	}
	fmt.Fprintln(c.out, "converting ...")
	opt.Out = c.out
	res, err := convert.Run(ctx, src, dst, p, opt)
	if err != nil {
		return fmt.Errorf("%w\nThe gameWE database is filled partly; the CE database is unchanged. Fix the cause, empty the gameWE database (%s -yes) and convert again", err, dbCommand("reset"))
	}
	printConvertResult(c, res)
	return nil
}

func printConvertPlan(c dbIO, p *convert.Plan, s, d config.Database) {
	ver := ""
	if p.SourceVersion != "" {
		ver = ", HLstatsX:CE " + p.SourceVersion
	}
	fmt.Fprintf(c.out, "source:      %s on %s (%s%s), only read\n", p.SourceName, net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), p.SourceServer, ver)
	target := "a gameWE database without data"
	if p.TargetNew {
		target = fmt.Sprintf("new: the gameWE schema (version %d) is created first", database.Latest())
	}
	fmt.Fprintf(c.out, "target:      %s on %s (%s)\n", d.Name, net.JoinHostPort(d.Host, strconv.Itoa(d.Port)), target)
	if p.Zone.Server() {
		fmt.Fprintf(c.out, "time zone:   eventTime is local time of the CE server (%s); other zone: -source-timezone\n", p.ServerZone)
	} else {
		fmt.Fprintf(c.out, "time zone:   eventTime is local time in %s (-source-timezone; the CE server has %s)\n", p.Zone.Name, p.ServerZone)
	}
	if p.FirstEvent != "" {
		fmt.Fprintf(c.out, "             e.g. the first frag: %s -> %s UTC\n", p.FirstEvent, p.FirstEventUTC)
	}
	var colls []string
	for k, n := range p.Collations {
		colls = append(colls, fmt.Sprintf("%s (%d tables)", k, n))
	}
	sort.Strings(colls)
	fmt.Fprintf(c.out, "characters:  %s -> utf8mb4_unicode_ci; double-encoded UTF-8: -mojibake %s\n", strings.Join(colls, ", "), p.Mojibake)
	if p.DoubleEncoded > 0 && p.Repaired {
		fmt.Fprintf(c.out, "             %d player names look double-encoded (MÃ¼ller) and are repaired\n", p.DoubleEncoded)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(c.out, "warning:     %s\n", w)
	}
	fmt.Fprintf(c.out, "plan (%d steps, rows in CE):\n", len(p.Steps))
	w := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
	for i, st := range p.Steps {
		fmt.Fprintf(w, "  %2d.\t%s\t%d\t%s -> %s\t%s\n", i+1, st.Name, st.Rows, st.Source, st.Target, st.Note)
		if c.res != nil {
			c.res.Tables = append(c.res.Tables, tableDoc{st.Source, st.Rows})
		}
	}
	w.Flush()
	if len(p.Skipped) > 0 {
		fmt.Fprintln(c.out, "not converted:")
		w := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
		for _, sk := range p.Skipped {
			fmt.Fprintf(w, "      %s\t%d\t%s\n", sk.Table, sk.Rows, sk.Why)
		}
		w.Flush()
	}
}

func printConvertResult(c dbIO, r *convert.Result) {
	fmt.Fprintf(c.out, "done in %s.", r.Duration.Round(time.Second))
	if r.Repaired > 0 {
		fmt.Fprintf(c.out, " %d double-encoded texts repaired.", r.Repaired)
	}
	if r.Merged > 0 {
		fmt.Fprintf(c.out, " %d player names merged (equal in utf8mb4_unicode_ci).", r.Merged)
	}
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, "rows in CE and in gameWE:")
	fmt.Fprintf(c.out, "%10s %10s\n", "CE", "gameWE")
	for _, s := range r.Summary {
		mark := ""
		if s.CE != s.GameWE {
			mark = " (differs)"
		}
		fmt.Fprintf(c.out, "%10d %10d  %s%s\n", s.CE, s.GameWE, s.What, mark)
	}
	fmt.Fprintln(c.out, "Fewer rows in gameWE: events of servers no longer in CE's hlstats_Servers, events of unknown actions,\n"+
		"history rows without a date, player names equal in utf8mb4_unicode_ci (merged).")
	fmt.Fprintf(c.out, "Next: %s, set the gameWE settings (web admin), switch the game servers (docs/MIGRATION-HLSTATSX.md).\n", strings.Replace(dbCommand("status"), " db status", " check", 1))
}
