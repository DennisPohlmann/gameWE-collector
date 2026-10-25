// SPDX-License-Identifier: GPL-3.0-or-later

// Command gamewe is the gameWE collector: it receives Source-engine game
// server logs over UDP, writes player statistics into an HLstatsX database
// (MariaDB / MySQL or PostgreSQL) and answers in-game commands over RCON.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/games"
	"gamewe-collector/internal/geoip"
	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/sdnotify"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/simulate"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/version"

	_ "time/tzdata" // time zones also where the system has none (distroless image)
)

const usage = `gameWE - collector %s

Usage:
  gamewe run      [-config FILE] [-listen IP:PORT] [-log-level LEVEL] [-reload DURATION]
                                   start the collector (used by systemd)
  gamewe check    [-config FILE]   test the database (shows TLS state and pending
                                   migrations), list the game servers and settings found in it
  gamewe db migrate [-config FILE] [-f|--force] [-n|--dry-run]
                                   create the tables of a new database, or bring an older
                                   gameWE / HLstatsX database to this version (shows the plan,
                                   asks first; -n only shows it); "run" does it at start
                                   (database.auto_migrate), or refuses to start until then
  gamewe db status  [-config FILE] schema version, pending and applied migrations
  gamewe db grants  [-config FILE] [-print]
                                   grant the web users their table rights again
  gamewe db seed    [-config FILE] [-print]
                                   add missing reference data (existing rows stay)
  gamewe db init|purge|optimize|reset|drop ...
                                   tables of a new database; delete events older than
                                   -older-than DAYS; give the space back; empty every table;
                                   drop the tables (see "gamewe db help")
  gamewe simulate [-config FILE] [-file LOG] [-game CODE] [-server IP:PORT] [-send HOST:PORT]
                                   replay sample log lines into the database, or send
                                   them as UDP log packets to a running collector
  gamewe awards   [-config FILE] [-date YYYY-MM-DD] [-game CODE] [-tz ZONE]
                                   decide the daily awards of a day again (default: yesterday
                                   in features.daily_awards.timezone); the collector does it
                                   every day at features.daily_awards.time
  gamewe rcon     [-config FILE] -server ID|IP:PORT COMMAND...
                                   send an RCON command to a game server of
                                   hlstats_Servers and print the answer
  gamewe status   [-config FILE] [-server ID|IP:PORT|all]
                                   RCON "status" of the game servers, as the collector reads it
  gamewe logaddress [-config FILE] [-server ID|IP:PORT|all] [-add] [-address IP:PORT]
                                   is the collector registered as log address (logaddress_list)?
                                   -add registers it where it is missing (logaddress_add, log on)
  gamewe game   list|add|set|hide|show|delete ... games (rankings), as in the web admin
  gamewe server list|add|set|remove ...           game servers (hlstats_Servers)
  gamewe option list|get|set ...                  settings (hlstats_Options)
                                   see "gamewe game help"; all take -config and -dry-run
  gamewe reset GAME -mode player-stats|all-stats|all [-dry-run] [-yes]
                                   reset a ranking (player-stats: players stay with the
                                   start skill, server statistics stay; all-stats: server
                                   statistics too; all: players deleted too); queued for
                                   the running collector if there is one
  gamewe reset -list               the latest queued resets
  gamewe geoip-update [-file FILE]  download DB-IP City Lite now (default
                                   /var/lib/gamewe/collector/dbip-city-lite.mmdb); the
                                   collector does it on the first start and monthly
                                   itself (geoip.auto_update)
  gamewe version                   print the version (also -V, -version, --version)

Every command except run takes -o FORMAT (or --output FORMAT, also before the
command: gamewe -o json server list): text (default), json, yaml; the lists
(game|server|option list, status, logaddress, awards, reset -list) also csv,
tsv and name (only the id / code / key, one per line). Passwords and log
secrets are never printed, in no format: only whether they are set.

The config file (default ` + config.DefaultPath + `) holds the UDP
listen address (default 0.0.0.0:41337) and the database connection. Game
servers come from hlstats_Servers and settings from hlstats_Options; both are
re-read every -reload interval (default 60s).
`

func main() {
	all, err := globalFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gamewe: %v\n", err)
		os.Exit(2)
	}
	if len(all) == 0 {
		fmt.Fprintf(os.Stderr, usage, version.Version)
		os.Exit(2)
	}
	cmd, args := all[0], all[1:]
	if isVersion(cmd) {
		// before anything else: no config, database or network
		globalFlags(args) // gamewe version -o json; other arguments are ignored
		err = printVersion(os.Stdout)
	}
	switch cmd {
	case "run":
		err = runCmd(args)
	case "check":
		err = checkCmd(args)
	case "simulate":
		err = simulateCmd(args)
	case "awards":
		err = awardsCmd(args)
	case "rcon":
		err = rconCmd(args)
	case "status":
		err = statusCmd(args)
	case "logaddress":
		err = logaddressCmd(args)
	case "geoip-update":
		err = geoipUpdateCmd(args)
	case "game", "server", "option":
		err = adminCmd(cmd, args)
	case "reset":
		err = resetCmd(args)
	case "db":
		err = dbCmd(args)
	case "version", "-V", "-version", "--version":
		// printed above
	case "help", "-h", "-help", "--help":
		fmt.Printf(usage, version.Version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n"+usage, cmd, version.Version)
		os.Exit(2)
	}
	var ee exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintf(os.Stderr, "gamewe %s: %s\n", cmd, ee.msg)
		}
		os.Exit(ee.code)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gamewe %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

// isVersion reports whether the first argument asks for the version.
func isVersion(arg string) bool {
	switch arg {
	case "version", "-V", "-version", "--version":
		return true
	}
	return false
}

// versionLine is the output of "gamewe version".
func versionLine() string { return "gamewe " + version.Version }

// printVersion writes the version line, or {"version": ...} with -o.
func printVersion(out io.Writer) error {
	if !structured() {
		_, err := fmt.Fprintln(out, versionLine())
		return err
	}
	if err := needList(false); err != nil {
		return err
	}
	return emit(out, struct {
		Version string `json:"version"`
	}{version.Version})
}

func loadConfig(name string, args []string, extra func(*flag.FlagSet)) (config.Config, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	addOutputFlag(fs)
	if extra != nil {
		extra(fs)
	}
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return cfg, fmt.Errorf("config %s: %w", *path, err)
	}
	configPath = *path
	return cfg, nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	l.UnmarshalText([]byte(level))
	// systemd's journal adds timestamps itself.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: l,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 && os.Getenv("JOURNAL_STREAM") != "" {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func tlsState(info database.Info) string {
	if !info.TLS {
		return "off (plaintext connection)"
	}
	return strings.TrimSpace(fmt.Sprintf("on, %s %s", info.TLSVersion, info.Cipher))
}

func runCmd(args []string) error {
	var listen, level string
	var reload time.Duration
	cfg, err := loadConfig("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&listen, "listen", "", "UDP address for game-server logs, overrides listen: in the config (IP:port)")
		fs.StringVar(&level, "log-level", "info", "debug, info, warn or error")
		fs.DurationVar(&reload, "reload", time.Minute, "how often hlstats_Servers and hlstats_Options are re-read")
	})
	if err != nil {
		return exitError{exitConfig, err.Error()}
	}
	if reload < 5*time.Second {
		return exitError{exitConfig, "-reload must be at least 5s"}
	}
	if structured() {
		return exitError{2, "-o: run writes a log (stderr / journal), it has no output format"}
	}
	if warnRoot(os.Stderr) {
		defer func() { warnRoot(os.Stderr) }() // again at the end, after all other output
	}
	log := newLogger(level)
	if geteuid() == 0 {
		for _, l := range rootLines() {
			log.Warn(l)
		}
	}
	log.Info("gameWE collector starting", "version", version.Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.ConnectTimeout+5)*time.Second)
	info, err := database.Inspect(cctx, db)
	cancel()
	if err != nil {
		d := cfg.Database
		return fmt.Errorf("database %s:%d/%s not reachable: %w", d.Host, d.Port, d.Name, database.Explain(err))
	}
	log.Info("database connected", "type", cfg.Database.Type, "server", info.Version, "tls", tlsState(info))
	// The schema must be the one of this collector: database.auto_migrate
	// brings it up to date here (as gamewe db migrate -f), before anything
	// else touches the database; otherwise only gamewe db migrate changes it.
	cctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	plan, err := database.PlanMigrations(cctx, db)
	cancel()
	if err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	for _, w := range plan.Warnings {
		log.Warn(w)
	}
	if !plan.UpToDate() && cfg.Database.AutoMigrate {
		// A migration can take longer than systemd's start timeout.
		sdnotify.Status("migrating the database schema")
		stop := sdnotify.ExtendStart(ctx, 30*time.Second)
		plan, err = autoMigrate(ctx, log, db, cfg.Database, plan)
		stop()
		if err != nil {
			return err
		}
	}
	if plan.UpToDate() {
		cctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		err = database.CheckTables(cctx, db)
		cancel()
		if err != nil {
			return fmt.Errorf("database: %w", database.Explain(err))
		}
	} else {
		for i, r := range plan.Missing {
			if i == 20 {
				log.Error(fmt.Sprintf("... and %d more", len(plan.Missing)-i))
				break
			}
			log.Error(r.String())
		}
		return exitError{exitConfig, schemaOutdated(plan, cfg.Database.AutoMigrate).Error()}
	}
	log.Info("database schema up to date", "version", plan.Current)

	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	replier := collector.NewRconReplier(ctx, set, log)
	hub := collector.NewHub(store, set, replier, log)
	hub.LineLog = os.Stderr // gamewe_log_lines: every received log line in the journal
	auto := &geoip.Auto{DB: geoip.New(log), Explicit: func() string {
		if cfg.GeoIP.Path != "" {
			return cfg.GeoIP.Path // config.yaml wins
		}
		if p := set.Get().GeoIPDB; p != "" && p != geoip.LegacyPath {
			return p // hlstats_Options gamewe_geoip_db (legacy setting)
		}
		return ""
	}}
	hub.Geo = auto
	if listen == "" {
		listen = cfg.Listen.UDP()
	}
	rs := cfg.Features.RconStatus
	every, _ := rs.Every() // checked by config.Validate
	la := cfg.Features.LogAddress
	laEvery, _ := la.Every() // checked by config.Validate
	hub.Monitor = collector.MonitorConfig{Status: rs.Enabled, StatusInterval: every, LogAddress: la.Enabled, LogInterval: laEvery,
		PublicAddress: cfg.Listen.PublicAddress, ListenPort: listenPort(listen, cfg.Listen.Port)}
	ls := cfg.Features.LogSecret
	hub.Secret = collector.SecretConfig{Mode: secretMode(ls.Enabled)}
	if len(ls.Obsolete) > 0 {
		log.Warn("features.log_secret: the automatic log secret (auto, interval) was removed and is ignored; set the secret per server with gamewe server set -logsecret or in the web admin, and the same sv_logsecret in server.cfg",
			"ignored", strings.Join(ls.Obsolete, ", "))
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = hub.Reload(rctx)
	cancel()
	if err != nil {
		return err
	}
	gpath := auto.Path()
	if gpath == geoip.DefaultPath {
		if ok, err := geoip.CopyOld(gpath, geoip.OldPath); err != nil {
			log.Warn("GeoIP database of the old location not taken over", "err", err)
		} else if ok {
			log.Info("GeoIP database moved to the new location; the old directory can be deleted", "path", gpath, "old", filepath.Dir(geoip.OldPath))
		}
	}
	_, gerr := os.Stat(gpath)
	switch {
	case cfg.GeoIP.AutoUpdate && (cfg.GeoIP.Path != "" || auto.Explicit() == ""):
		// A missing file is downloaded now (in the background), then monthly.
		if gerr != nil {
			log.Info("GeoIP database missing, downloading DB-IP City Lite", "path", gpath)
		} else {
			auto.DB.Check(gpath)
		}
		u := &geoip.Updater{Dest: gpath, Log: log, OnUpdate: auto.DB.Reload}
		go u.Run(ctx, 6*time.Hour)
	case gerr != nil && !cfg.GeoIP.AutoUpdate:
		log.Warn("GeoIP database missing and geoip.auto_update is off: player and server locations are not filled (gamewe geoip-update downloads it)", "path", gpath)
	default:
		auto.DB.Check(gpath)
	}
	log.Info("bots recorded", "ranked", set.Get().RankBots)
	if hub.Count() == 0 {
		log.Warn("no usable game servers in hlstats_Servers yet; add them in the web admin (checked every " + reload.String() + ")")
	}
	go hub.RunReloader(ctx, reload)
	go (&collector.Jobs{Store: store, Hub: hub, Log: log}).Run(ctx)
	da := cfg.Features.DailyAwards
	if da.Enabled {
		loc, _ := da.Location() // checked by config.Validate
		h, m, _ := da.At()
		go collector.RunDailyAwards(ctx, store, collector.Schedule{Loc: loc, Hour: h, Minute: m}, log)
	}
	log.Info("features", "rcon_status", rs.Enabled, "rcon_status_every", every.String(), "log_address", la.Enabled, "log_address_every", laEvery.String(),
		"log_secret", ls.Enabled, "daily_awards", da.Enabled, "daily_awards_at", da.Time+" "+da.Timezone)

	// Config, database and servers are fine; READY=1 once the UDP port is
	// open, so that systemctl start / restart report every failure before.
	err = collector.Serve(ctx, listen, hub, log, func(a net.Addr) {
		if err := sdnotify.Ready("listening for game server logs on udp " + a.String()); err != nil {
			log.Warn("systemd notification failed", "err", err)
		}
	})
	sdnotify.Stopping()
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hub.Close(sctx)
	log.Info("gameWE collector stopped")
	return err
}

// secretMode converts features.log_secret.enabled.
func secretMode(enabled bool) collector.SecretMode {
	if enabled {
		return collector.SecretOn
	}
	return collector.SecretOff
}

// checkReport is the document of gamewe check -o json|yaml. It holds no
// secret: the database password is not in it, the RCON password and the
// log secret of a server only as set / not set.
type checkReport struct {
	OK            bool           `json:"ok"`
	Error         string         `json:"error,omitempty"`
	Config        string         `json:"config"`
	Listen        string         `json:"listen"`
	Features      checkFeatures  `json:"features"`
	Database      checkDatabase  `json:"database"`
	Schema        *checkSchema   `json:"schema,omitempty"`
	Tables        int            `json:"tables,omitempty"`
	Settings      *checkSettings `json:"settings,omitempty"`
	Servers       []checkServer  `json:"servers"`
	ServersUsable int            `json:"servers_usable"`
	Warnings      []string       `json:"warnings,omitempty"`
	Missing       []string       `json:"missing,omitempty"`
	Notes         []string       `json:"notes,omitempty"`
}

type checkFeatures struct {
	RconStatus          bool   `json:"rcon_status"`
	RconStatusEvery     string `json:"rcon_status_every"`
	LogAddress          bool   `json:"log_address"`
	LogAddressEvery     string `json:"log_address_every"`
	LogSecret           bool   `json:"log_secret"`
	DailyAwards         bool   `json:"daily_awards"`
	DailyAwardsTime     string `json:"daily_awards_time"`
	DailyAwardsTimezone string `json:"daily_awards_timezone"`
}

type checkDatabase struct {
	Type    string `json:"type"`
	User    string `json:"user"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Name    string `json:"name"`
	TLSMode string `json:"tls_mode"`
	Server  string `json:"server,omitempty"` // version of the database server
	TLS     string `json:"tls,omitempty"`
}

type checkSchema struct {
	Version     int  `json:"version"`
	Needs       int  `json:"needs"`
	UpToDate    bool `json:"up_to_date"`
	Pending     int  `json:"pending_steps"`
	AutoMigrate bool `json:"migrated_at_start"` // gamewe run migrates it (database.auto_migrate)
}

type checkSettings struct {
	Keys          int  `json:"keys"` // gamewe_* keys stored in hlstats_Options
	KillPoints    int  `json:"kill_points"`
	HeadshotBonus int  `json:"headshot_bonus"`
	StartSkill    int  `json:"start_skill"`
	Commands      bool `json:"commands"`
}

type checkServer struct {
	ID              int64  `json:"id"`
	Address         string `json:"address"`
	Name            string `json:"name"`
	Game            string `json:"game"`
	Usable          bool   `json:"usable"`
	State           string `json:"state"`
	RconPasswordSet bool   `json:"rcon_password_set"`
	LogSecretSet    bool   `json:"log_secret_set"`
	Note            string `json:"note,omitempty"`
}

func checkCmd(args []string) error {
	cfg, err := loadConfig("check", args, nil)
	if err != nil {
		return err
	}
	warnRoot(textOut())
	if err := needList(false); err != nil {
		return err
	}
	rep := &checkReport{Servers: []checkServer{}}
	if !structured() {
		return check(cfg, os.Stdout, rep)
	}
	err = check(cfg, io.Discard, rep)
	rep.OK = err == nil
	if err != nil {
		rep.Error = err.Error()
	}
	if werr := emit(os.Stdout, rep); werr != nil && err == nil {
		return werr
	}
	return err
}

// check is gamewe check: the text goes to out, the same facts into rep.
func check(cfg config.Config, out io.Writer, rep *checkReport) error {
	warn := func(w string) {
		fmt.Fprintf(out, "warning:   %s\n", w)
		rep.Warnings = append(rep.Warnings, w)
	}
	fmt.Fprintln(out, "config:    ok")
	rep.Config = "ok"
	d := cfg.Database
	fmt.Fprintf(out, "listen:    udp %s\n", cfg.Listen.UDP())
	rep.Listen = cfg.Listen.UDP()
	da := cfg.Features.DailyAwards
	every, _ := cfg.Features.RconStatus.Every()
	laEvery, _ := cfg.Features.LogAddress.Every()
	fmt.Fprintf(out, "features:  rcon_status %v (every %v), log_address %v (every %v), log_secret %v, daily_awards %v (%s %s)\n",
		cfg.Features.RconStatus.Enabled, every, cfg.Features.LogAddress.Enabled, laEvery, cfg.Features.LogSecret.Enabled, da.Enabled, da.Time, da.Timezone)
	rep.Features = checkFeatures{cfg.Features.RconStatus.Enabled, every.String(), cfg.Features.LogAddress.Enabled, laEvery.String(),
		cfg.Features.LogSecret.Enabled, da.Enabled, da.Time, da.Timezone}
	if o := cfg.Features.LogSecret.Obsolete; len(o) > 0 {
		warn(fmt.Sprintf("features.log_secret: %s ignored, the automatic log secret was removed (set it per server with gamewe server set -logsecret)", strings.Join(o, ", ")))
	}
	fmt.Fprintf(out, "database:  %s %s@%s:%d/%s, tls mode %s\n", d.Type, d.User, d.Host, d.Port, d.Name, d.TLS.Mode)
	rep.Database = checkDatabase{Type: d.Type, User: d.User, Host: d.Host, Port: d.Port, Name: d.Name, TLSMode: d.TLS.Mode}

	db, err := database.Open(d)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(d.ConnectTimeout+5)*time.Second)
	defer cancel()
	info, err := database.Inspect(ctx, db)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", database.Explain(err))
	}
	fmt.Fprintf(out, "connected: %s\n", info.Version)
	fmt.Fprintf(out, "tls:       %s\n", tlsState(info))
	rep.Database.Server, rep.Database.TLS = info.Version, tlsState(info)
	if d.TLS.Mode != config.TLSDisable && !info.TLS {
		// Should be impossible with the drivers' settings; never accept it.
		return errors.New("tls is configured but the server reports a plaintext connection")
	}
	// The schema first: it names everything missing, CheckTables stops at
	// the first missing table.
	plan, err := database.PlanMigrations(ctx, db)
	if err != nil {
		return err
	}
	rep.Schema = &checkSchema{Version: plan.Current, Needs: plan.Latest, UpToDate: plan.UpToDate(), Pending: len(plan.Steps)}
	if plan.UpToDate() {
		fmt.Fprintf(out, "schema:    ok (version %d)\n", plan.Current)
	} else {
		if autoMigrates(d, plan) {
			rep.Schema.AutoMigrate = true
			fmt.Fprintf(out, "schema:    version %d, this collector needs %d: %d migration steps pending, gamewe run migrates at start (database.auto_migrate).\n", plan.Current, plan.Latest, len(plan.Steps))
			fmt.Fprintf(out, "           Make a backup before the start, or migrate now: %s   (plan: %s -n)\n", migrateCommand(), migrateCommand())
		} else {
			fmt.Fprintf(out, "schema:    version %d, this collector needs %d: %d migration steps pending, the collector does not start.\n", plan.Current, plan.Latest, len(plan.Steps))
			fmt.Fprintf(out, "           Make a backup, then run: %s   (plan: %s -n)\n", migrateCommand(), migrateCommand())
		}
	}
	for _, w := range plan.Warnings {
		warn(w)
	}
	for _, r := range plan.Missing {
		m := strings.TrimPrefix(r.String(), "schema incomplete: missing ")
		fmt.Fprintf(out, "missing:   %s\n", m)
		rep.Missing = append(rep.Missing, m)
	}
	for _, f := range obsoleteSQL() {
		n := f + " is from an older package and not used any more (the schema is built into gamewe); it can be deleted"
		fmt.Fprintf(out, "note:      %s\n", n)
		rep.Notes = append(rep.Notes, n)
	}
	if err := database.CheckTables(ctx, db); err != nil {
		return err
	}
	fmt.Fprintf(out, "tables:    ok (%d HLstatsX tables)\n", len(database.Tables))
	rep.Tables = len(database.Tables)

	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	opts, err := store.Options(ctx)
	if err != nil {
		return err
	}
	set, warnings := settings.FromOptions(opts)
	fmt.Fprintf(out, "settings:  %d gamewe_* keys in hlstats_Options (others use defaults): kill_points %d, headshot_bonus %d, start_skill %d, commands %v\n",
		len(opts), set.KillPoints, set.HeadshotBonus, set.StartSkill, set.Commands)
	rep.Settings = &checkSettings{len(opts), set.KillPoints, set.HeadshotBonus, set.StartSkill, set.Commands}
	for _, w := range warnings {
		warn(w)
	}
	servers, err := store.Servers(ctx)
	if err != nil {
		return err
	}
	usable := 0
	for _, s := range servers {
		state := "ok"
		switch {
		case s.Err != nil:
			state = "IGNORED: " + s.Err.Error()
		case s.RconPassword == "":
			state = "ok, no rcon_password: no replies to in-game commands"
		}
		if s.Err == nil {
			usable++
		}
		fmt.Fprintf(out, "server:    #%d %s %q game %s: %s\n", s.ID, s.Address, s.Name, s.Game, state)
		cs := checkServer{ID: s.ID, Address: s.Address, Name: s.Name, Game: s.Game, Usable: s.Err == nil, State: state,
			RconPasswordSet: s.RconPassword != "", LogSecretSet: s.LogSecret != ""}
		if msg, info := collector.SecretNote(secretMode(cfg.Features.LogSecret.Enabled), s); msg != "" && s.Err == nil {
			fmt.Fprintf(out, "%-11s%s\n", map[bool]string{true: "note:", false: "warning:"}[info], msg)
			cs.Note = msg
		}
		rep.Servers = append(rep.Servers, cs)
	}
	fmt.Fprintf(out, "servers:   %d in hlstats_Servers, %d usable\n", len(servers), usable)
	rep.ServersUsable = usable
	if usable == 0 {
		return errors.New("no usable game servers in hlstats_Servers: add them in the web admin")
	}
	return nil
}

func simulateCmd(args []string) error {
	var file, send, game, server string
	cfg, err := loadConfig("simulate", args, func(fs *flag.FlagSet) {
		fs.StringVar(&file, "file", "", "log file to replay (default: built-in sample)")
		fs.StringVar(&send, "send", "", "send as UDP log packets to the collector at this address instead")
		fs.StringVar(&game, "game", "css", "hlstats_Games code of the simulated server")
		fs.StringVar(&server, "server", simulate.DefaultServer, "IP:port of the simulated server (and the source address of -send)")
	})
	if err != nil {
		return err
	}
	ap, err := netip.ParseAddrPort(server)
	if err != nil {
		return fmt.Errorf("-server: %w", err)
	}
	lines, err := simulate.Lines(file)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return captured(os.Stdout, func(w io.Writer, _ *result) error {
		return simulate.Run(ctx, cfg, simulate.Options{Server: ap, Game: game, Send: send}, lines, w, newLogger("info"))
	})
}

func awardsCmd(args []string) error {
	var date, game, tz string
	cfg, err := loadConfig("awards", args, func(fs *flag.FlagSet) {
		fs.StringVar(&date, "date", "", "day YYYY-MM-DD (default: yesterday)")
		fs.StringVar(&game, "game", "", "game code (default: all games)")
		fs.StringVar(&tz, "tz", "", "time zone of the day (default: features.daily_awards.timezone)")
	})
	if err != nil {
		return err
	}
	if err := needList(true); err != nil {
		return err
	}
	if tz == "" {
		tz = cfg.Features.DailyAwards.Timezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("-tz %q: %v", tz, err)
	}
	y, m, d := time.Now().In(loc).Date()
	day := time.Date(y, m, d-1, 0, 0, 0, 0, loc)
	if date != "" {
		if day, err = time.ParseInLocation("2006-01-02", date, loc); err != nil {
			return fmt.Errorf("-date %q: want YYYY-MM-DD", date)
		}
	}
	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	res, err := store.DailyAwards(ctx, day, game)
	if err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	doc := awardsDoc{Date: stats.AwardDate(day), Timezone: loc.String(), Awards: []awardDoc{}}
	if !structured() {
		fmt.Printf("daily awards of %s (%s):\n", doc.Date, loc)
	}
	for _, r := range res {
		winner := "-"
		a := awardDoc{Game: r.Game, Type: r.Type, Code: r.Code}
		if r.WinnerID > 0 {
			pl, err := store.PlayerStats(ctx, r.WinnerID)
			name := "?"
			if err == nil {
				name = pl.Name
			}
			winner = fmt.Sprintf("%s (#%d) %d", name, r.WinnerID, r.Count)
			a.WinnerID, a.Winner, a.Count = r.WinnerID, name, r.Count
		}
		doc.Awards = append(doc.Awards, a)
		if !structured() {
			fmt.Printf("  %-5s %s %-20s %s\n", r.Game, r.Type, r.Code, winner)
		}
	}
	if structured() {
		return emit(os.Stdout, doc)
	}
	return nil
}

type awardsDoc struct {
	Date     string     `json:"date"`
	Timezone string     `json:"timezone"`
	Awards   []awardDoc `json:"awards"`
}

type awardDoc struct {
	Game     string `json:"game"`
	Type     string `json:"type"` // W (weapon) or O (action)
	Code     string `json:"code"`
	WinnerID int64  `json:"winner_id,omitempty"`
	Winner   string `json:"winner,omitempty"`
	Count    int    `json:"count,omitempty"`
}

func (awardsDoc) Header() []string {
	return []string{"game", "type", "code", "winner_id", "winner", "count"}
}
func (d awardsDoc) Rows() [][]string {
	out := [][]string{}
	for _, a := range d.Awards {
		out = append(out, []string{a.Game, a.Type, a.Code, itoa(a.WinnerID), a.Winner, itoa(a.Count)})
	}
	return out
}

// listenPort is the port of the -listen address, or def.
func listenPort(listen string, def int) int {
	if ap, err := netip.ParseAddrPort(listen); err == nil {
		return int(ap.Port())
	}
	return def
}

// gameServers opens the database and returns the game servers that match
// sel: a serverId, an ip:port, or "all" / "" for all usable servers.
func gameServers(cfg config.Config, sel string) ([]stats.Server, error) {
	db, err := database.Open(cfg.Database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	all, err := store.Servers(context.Background())
	if err != nil {
		return nil, fmt.Errorf("database: %w", database.Explain(err))
	}
	var out []stats.Server
	for _, s := range all {
		if s.Err != nil {
			continue
		}
		if sel == "" || sel == "all" || strconv.FormatInt(s.ID, 10) == sel || s.AddrPort.String() == sel || s.Address == sel {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no usable game server %q in hlstats_Servers", sel)
	}
	return out, nil
}

func rconClient(s stats.Server) (rcon.Conn, error) {
	if s.RconPassword == "" {
		return nil, fmt.Errorf("server %d %s has no rcon_password in hlstats_Servers", s.ID, s.Address)
	}
	return rcon.New(s.Engine == games.GoldSrc, s.AddrPort.String(), s.RconPassword, 5*time.Second), nil
}

func rconCmd(args []string) error {
	var server string
	fs := flag.NewFlagSet("rcon", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	fs.StringVar(&server, "server", "", "serverId or ip:port of hlstats_Servers")
	addOutputFlag(fs)
	fs.Parse(args)
	if server == "" || server == "all" || fs.NArg() == 0 {
		return errors.New("usage: gamewe rcon -server ID|IP:PORT COMMAND...")
	}
	if err := needList(false); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fmt.Errorf("config %s: %w", *path, err)
	}
	servers, err := gameServers(cfg, server)
	if err != nil {
		return err
	}
	c, err := rconClient(servers[0])
	if err != nil {
		return err
	}
	defer c.Close()
	out, err := c.Exec(strings.Join(fs.Args(), " "))
	if err != nil {
		return err
	}
	if structured() {
		// The command is not repeated: it can carry a password
		// (rcon_password, sv_password, ...).
		return emit(os.Stdout, struct {
			ServerID int64  `json:"server_id"`
			Address  string `json:"address"`
			Output   string `json:"output"`
		}{servers[0].ID, servers[0].Address, out})
	}
	fmt.Print(out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

func statusCmd(args []string) error {
	var server string
	cfg, err := loadConfig("status", args, func(fs *flag.FlagSet) {
		fs.StringVar(&server, "server", "all", "serverId, ip:port or all")
	})
	if err != nil {
		return err
	}
	servers, err := gameServers(cfg, server)
	if err != nil {
		return err
	}
	if err := needList(true); err != nil {
		return err
	}
	failed := 0
	doc := statusList{}
	text := !structured()
	for _, s := range servers {
		if text {
			fmt.Printf("server #%d %s %q:\n", s.ID, s.Address, s.Name)
		}
		d := statusDoc{ID: s.ID, Address: s.Address, Name: s.Name, Players: []statusPlayer{}}
		c, err := rconClient(s)
		if err == nil {
			var out string
			if out, err = c.Exec("status"); err == nil {
				var st collector.Status
				if st, err = collector.ParseStatus(out); err == nil {
					d.Hostname, d.Map, d.MaxPlayers = st.Hostname, st.Map, st.MaxPlayers
					if text {
						fmt.Printf("  hostname %q, map %s, %d players (%d max)\n", st.Hostname, st.Map, len(st.Players), st.MaxPlayers)
					}
					for _, p := range st.Players {
						// as the text: no IP address
						d.Players = append(d.Players, statusPlayer{p.UserID, p.Name, p.UniqueID, p.Ping})
						if text {
							fmt.Printf("    #%-3d %-24s %-14s ping %d\n", p.UserID, p.Name, p.UniqueID, p.Ping)
						}
					}
				}
			}
			c.Close()
		}
		if err != nil {
			if text {
				fmt.Printf("  error: %v\n", err)
			}
			d.Error = err.Error()
			failed++
		}
		doc = append(doc, d)
	}
	if !text {
		if err := emit(os.Stdout, doc); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d servers failed", failed, len(servers))
	}
	return nil
}

func logaddressCmd(args []string) error {
	var server, address string
	var add bool
	cfg, err := loadConfig("logaddress", args, func(fs *flag.FlagSet) {
		fs.StringVar(&server, "server", "all", "serverId, ip:port or all")
		fs.BoolVar(&add, "add", false, "register the collector where it is missing (logaddress_add, log on)")
		fs.StringVar(&address, "address", "", "ip:port to register (default: listen.public_address, else the address of the RCON connection and listen.port)")
	})
	if err != nil {
		return err
	}
	if address == "" {
		address = cfg.Listen.PublicAddress
	}
	servers, err := gameServers(cfg, server)
	if err != nil {
		return err
	}
	if err := needList(true); err != nil {
		return err
	}
	text := !structured()
	failed := 0
	doc := logaddressList{}
	for _, s := range servers {
		if text {
			fmt.Printf("server #%d %s %q:\n", s.ID, s.Address, s.Name)
		}
		d := logaddressDoc{ID: s.ID, Address: s.Address, Name: s.Name, LogAddresses: []string{}}
		err := func() error {
			c, err := rconClient(s)
			if err != nil {
				return err
			}
			defer c.Close()
			if _, err := c.Exec("echo"); err != nil {
				return err
			}
			addr, err := collector.LogAddress(c, address, cfg.Listen.Port)
			if err != nil {
				return err
			}
			list, added, err := collector.EnsureLogAddress(c, s.Engine, addr, add)
			if err != nil {
				return err
			}
			d.Collector, d.Added = addr.String(), added
			for _, a := range list {
				d.LogAddresses = append(d.LogAddresses, a.String())
			}
			d.Registered = added || slices.Contains(list, addr)
			if !text {
				return nil
			}
			fmt.Printf("  log addresses: %v\n  collector %s: ", list, addr)
			switch {
			case added:
				fmt.Println("registered now (logaddress_add, log on)")
			case slices.Contains(list, addr):
				fmt.Println("registered")
			default:
				fmt.Println("NOT registered (use -add)")
			}
			return nil
		}()
		if err != nil {
			if text {
				fmt.Printf("  error: %v\n", err)
			}
			d.Error = err.Error()
			failed++
		}
		doc = append(doc, d)
	}
	if !text {
		if err := emit(os.Stdout, doc); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d servers failed", failed, len(servers))
	}
	return nil
}

type statusDoc struct {
	ID         int64          `json:"id"`
	Address    string         `json:"address"`
	Name       string         `json:"name"`
	Hostname   string         `json:"hostname,omitempty"`
	Map        string         `json:"map,omitempty"`
	MaxPlayers int            `json:"max_players,omitempty"`
	Players    []statusPlayer `json:"players"`
	Error      string         `json:"error,omitempty"`
}

type statusPlayer struct {
	UserID   int    `json:"userid"`
	Name     string `json:"name"`
	UniqueID string `json:"unique_id"`
	Ping     int    `json:"ping"`
}

// statusList is written as csv with one line per player.
type statusList []statusDoc

func (statusList) Header() []string {
	return []string{"server_id", "server", "map", "userid", "name", "unique_id", "ping"}
}
func (l statusList) Rows() [][]string {
	out := [][]string{}
	for _, s := range l {
		for _, p := range s.Players {
			out = append(out, []string{itoa(s.ID), s.Address, s.Map, itoa(p.UserID), p.Name, p.UniqueID, itoa(p.Ping)})
		}
	}
	return out
}

type logaddressDoc struct {
	ID           int64    `json:"id"`
	Address      string   `json:"address"`
	Name         string   `json:"name"`
	Collector    string   `json:"collector,omitempty"` // the address the collector is registered with
	LogAddresses []string `json:"log_addresses"`
	Registered   bool     `json:"registered"`
	Added        bool     `json:"added"` // registered now (-add)
	Error        string   `json:"error,omitempty"`
}

type logaddressList []logaddressDoc

func (logaddressList) Header() []string {
	return []string{"id", "address", "collector", "registered", "added", "error"}
}
func (l logaddressList) Rows() [][]string {
	out := [][]string{}
	for _, s := range l {
		out = append(out, []string{itoa(s.ID), s.Address, s.Collector, yesNo(s.Registered), yesNo(s.Added), s.Error})
	}
	return out
}
