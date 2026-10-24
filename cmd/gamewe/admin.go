// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"gamewe-collector/internal/admin"
	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/games"
	"gamewe-collector/internal/rcon"
)

const adminUsage = `usage:
  gamewe game list
  gamewe game add CODE NAME [-template CODE] [-realgame GAME] [-hidden]
  gamewe game set CODE [-name NAME] [-realgame GAME] [-order N] [-hidden=true|false]
  gamewe game hide|show CODE
  gamewe game delete CODE                 (only a game without servers and players, not a default game)
  gamewe server list
  gamewe server add IP:PORT -game CODE [-name NAME] [-rcon-password-file FILE | -rcon-password PW] [-public-address ADDR] [-logsecret N]
  gamewe server set ID|IP:PORT [-game CODE] [-name NAME] [-rcon-password-file FILE | -rcon-password PW] [-public-address ADDR]
                    [-logsecret N|auto|""]  (sv_logsecret; auto: a random one, also set over RCON; "": none)
  gamewe server remove ID|IP:PORT
  gamewe option list
  gamewe option get KEY
  gamewe option set KEY VALUE
every command takes -config FILE and -dry-run (show what would change)`

// optional is a string flag that knows whether it was given.
type optional struct {
	v   string
	set bool
}

func (o *optional) String() string     { return o.v }
func (o *optional) Set(s string) error { o.v, o.set = s, true; return nil }
func (o *optional) ptr() *string {
	if !o.set {
		return nil
	}
	return &o.v
}

// parseInterspersed parses flags given before, between or after the
// positional arguments and returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// adminCmd runs `gamewe game|server|option ...`.
func adminCmd(kind string, args []string) error {
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, adminUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	dry := fs.Bool("dry-run", false, "only show what would change")
	template := fs.String("template", "", "game whose definitions a new game copies")
	realgame := fs.String("realgame", "", "base game (css, cstrike, tf, ...); default: the template's")
	hidden := fs.Bool("hidden", false, "add the game hidden; game set: -hidden=true|false")
	order := fs.Int("order", 0, "game set: position in the web's game list (hlstats_Games.display_id)")
	var game, name, rcon, rconFile, public, logSecret optional
	fs.Var(&game, "game", "hlstats_Games code")
	fs.Var(&name, "name", "server or game name")
	fs.Var(&rcon, "rcon-password", "RCON password (visible in the process list: prefer -rcon-password-file)")
	fs.Var(&rconFile, "rcon-password-file", "file with the RCON password ('-' = stdin)")
	fs.Var(&public, "public-address", "address shown in the web")
	fs.Var(&logSecret, "logsecret", "log secret (sv_logsecret): a number 1..2147483647, auto (random, set over RCON) or \"\" (none)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New(adminUsage)
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if rcon.set && rconFile.set {
		return errors.New("give -rcon-password or -rcon-password-file, not both")
	}
	if rconFile.set {
		var data []byte
		if rconFile.v == "-" {
			data, err = readAllLimited(os.Stdin)
		} else {
			data, err = os.ReadFile(rconFile.v)
		}
		if err != nil {
			return fmt.Errorf("rcon password file: %w", err)
		}
		rcon = optional{strings.TrimRight(string(data), "\r\n"), true}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fmt.Errorf("config %s: %w", *path, err)
	}
	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a := &admin.Admin{DB: db, Out: os.Stdout, DryRun: *dry}
	sub, rest := pos[0], pos[1:]
	want := func(n int) error {
		if len(rest) != n {
			return errors.New(adminUsage)
		}
		return nil
	}
	autoSecret := logSecret.set && logSecret.v == "auto"
	if autoSecret {
		if kind+" "+sub != "server set" {
			return errors.New("-logsecret auto: only with server set (the server needs its rcon_password)")
		}
		if logSecret.v, err = collector.RandomSecret(); err != nil {
			return err
		}
	}
	fields := admin.ServerFields{Game: game.ptr(), Name: name.ptr(), RconPassword: rcon.ptr(), PublicAddress: public.ptr(), LogSecret: logSecret.ptr()}
	switch kind + " " + sub {
	case "game list":
		return a.ListGames(ctx)
	case "game add":
		if err := want(2); err != nil {
			return err
		}
		return a.AddGame(ctx, rest[0], rest[1], *template, *realgame, *hidden)
	case "game set":
		if err := want(1); err != nil {
			return err
		}
		gf := admin.GameFields{Name: name.ptr()}
		if given["realgame"] {
			gf.Realgame = realgame
		}
		if given["order"] {
			gf.Order = order
		}
		if given["hidden"] {
			gf.Hidden = hidden
		}
		return a.SetGame(ctx, rest[0], gf)
	case "game hide", "game show":
		if err := want(1); err != nil {
			return err
		}
		return a.SetGameHidden(ctx, rest[0], sub == "hide")
	case "game delete":
		if err := want(1); err != nil {
			return err
		}
		return a.DeleteGame(ctx, rest[0])
	case "server list":
		return a.ListServers(ctx)
	case "server add":
		if err := want(1); err != nil {
			return err
		}
		return a.AddServer(ctx, rest[0], fields)
	case "server set":
		if err := want(1); err != nil {
			return err
		}
		if err := a.SetServer(ctx, rest[0], fields); err != nil || !logSecret.set || *dry {
			return err
		}
		return applyLogSecret(cfg, rest[0], logSecret.v, autoSecret)
	case "server remove":
		if err := want(1); err != nil {
			return err
		}
		return a.RemoveServer(ctx, rest[0])
	case "option list":
		return a.ListOptions(ctx)
	case "option get":
		if err := want(1); err != nil {
			return err
		}
		return a.GetOption(ctx, rest[0])
	case "option set":
		if err := want(2); err != nil {
			return err
		}
		return a.SetOption(ctx, rest[0], rest[1])
	}
	return errors.New(adminUsage)
}

// applyLogSecret sets a new log secret on the game server over RCON, after
// it was stored (so a running collector accepts the old one meanwhile).
// A value given by hand is only set when the server is reachable; the
// collector (features.log_secret auto) or server.cfg do it otherwise.
func applyLogSecret(cfg config.Config, ref, secret string, required bool) error {
	servers, err := gameServers(cfg, ref)
	if err == nil && servers[0].Engine == games.GoldSrc {
		err = errors.New("GoldSrc servers have no sv_logsecret: their log packets are not checked")
	}
	var c rcon.Conn
	if err == nil {
		c, err = rconClient(servers[0])
	}
	if err == nil {
		defer c.Close()
		err = collector.ApplySecret(c, secret)
	}
	switch {
	case err == nil:
		fmt.Println("sv_logsecret set on the game server over RCON")
		return nil
	case required:
		return fmt.Errorf("log secret stored, but not set on the game server: %w (features.log_secret auto sets it, otherwise set sv_logsecret in server.cfg)", err)
	}
	fmt.Printf("note: sv_logsecret not set over RCON (%v): features.log_secret auto sets it, otherwise set it in server.cfg\n", err)
	return nil
}

// readAllLimited reads at most 1 KiB (an RCON password).
func readAllLimited(f *os.File) ([]byte, error) {
	buf := make([]byte, 1024)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}
