// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gamewe-collector/internal/admin"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
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
                    [-logsecret N|""]  (must match sv_logsecret in the game server's server.cfg; "": none, packets not checked)
  gamewe server remove ID|IP:PORT
  gamewe option list
  gamewe option get KEY
  gamewe option set KEY VALUE
every command takes -config FILE, -dry-run (show what would change) and
-o text|json|yaml; the lists also -o csv|tsv|name`

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
	realgame := fs.String("realgame", "", "base game (css, cstrike, czero, tf, ...); default: the template's")
	hidden := fs.Bool("hidden", false, "add the game hidden; game set: -hidden=true|false")
	order := fs.Int("order", 0, "game set: position in the web's game list (hlstats_Games.display_id)")
	var game, name, rcon, rconFile, public, logSecret optional
	fs.Var(&game, "game", "hlstats_Games code")
	fs.Var(&name, "name", "server or game name")
	fs.Var(&rcon, "rcon-password", "RCON password (visible in the process list: prefer -rcon-password-file)")
	fs.Var(&rconFile, "rcon-password-file", "file with the RCON password ('-' = stdin)")
	fs.Var(&public, "public-address", "address shown in the web")
	fs.Var(&logSecret, "logsecret", "log secret (sv_logsecret in server.cfg): a number 1..2147483647, or \"\" (none: the server's log packets are not checked)")
	addOutputFlag(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New(adminUsage)
	}
	sub, rest := pos[0], pos[1:]
	list := sub == "list"
	if err := needList(list); err != nil {
		return err
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
	if structured() && (list || kind+" "+sub == "option get") {
		a.Out = os.Stderr // notes; the document goes to stdout
		return adminDoc(ctx, a, kind, rest)
	}
	return captured(os.Stdout, func(w io.Writer, _ *result) error {
		a.Out = w
		return adminRun(ctx, a, kind, sub, rest, adminArgs{fields: admin.ServerFields{Game: game.ptr(), Name: name.ptr(), RconPassword: rcon.ptr(), PublicAddress: public.ptr(), LogSecret: logSecret.ptr()},
			game: admin.GameFields{Name: name.ptr()}, given: given, template: *template, realgame: *realgame, hidden: *hidden, order: *order, logSecret: logSecret, dry: *dry})
	})
}

// adminArgs are the flags of adminCmd that adminRun needs.
type adminArgs struct {
	fields             admin.ServerFields
	game               admin.GameFields
	given              map[string]bool
	template, realgame string
	hidden             bool
	order              int
	logSecret          optional
	dry                bool
}

// adminDoc writes the document of a list or of option get.
func adminDoc(ctx context.Context, a *admin.Admin, kind string, rest []string) error {
	var v any
	var err error
	switch kind {
	case "game":
		v, err = a.Games(ctx)
		v = gameList(v.([]admin.Game))
	case "server":
		v, err = a.Servers(ctx)
		v = serverList(v.([]admin.ServerRow))
	case "option":
		if len(rest) == 1 {
			v, err = a.Option(ctx, rest[0])
		} else if len(rest) == 0 {
			v, err = a.Options(ctx)
			v = optionList(v.([]admin.Option))
		} else {
			return errors.New(adminUsage)
		}
	}
	if err != nil {
		return err
	}
	return emit(os.Stdout, v)
}

// adminRun runs `gamewe game|server|option SUB` with the text output.
func adminRun(ctx context.Context, a *admin.Admin, kind, sub string, rest []string, f adminArgs) error {
	want := func(n int) error {
		if len(rest) != n {
			return errors.New(adminUsage)
		}
		return nil
	}
	fields, given, logSecret, dry := f.fields, f.given, f.logSecret, &f.dry
	switch kind + " " + sub {
	case "game list":
		return a.ListGames(ctx)
	case "game add":
		if err := want(2); err != nil {
			return err
		}
		return a.AddGame(ctx, rest[0], rest[1], f.template, f.realgame, f.hidden)
	case "game set":
		if err := want(1); err != nil {
			return err
		}
		gf := f.game
		if given["realgame"] {
			gf.Realgame = &f.realgame
		}
		if given["order"] {
			gf.Order = &f.order
		}
		if given["hidden"] {
			gf.Hidden = &f.hidden
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
		if err := a.AddServer(ctx, rest[0], fields); err != nil || !logSecret.set || *dry {
			return err
		}
		secretHint(a.Out, logSecret.v)
		return nil
	case "server set":
		if err := want(1); err != nil {
			return err
		}
		if err := a.SetServer(ctx, rest[0], fields); err != nil || !logSecret.set || *dry {
			return err
		}
		secretHint(a.Out, logSecret.v)
		return nil
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

// secretHint reminds that the collector never sets sv_logsecret itself.
// The value is not printed.
func secretHint(w io.Writer, secret string) {
	if secret == "" {
		fmt.Fprintln(w, "note: no log secret: the collector accepts all log packets of this server (remove sv_logsecret from server.cfg too)")
		return
	}
	fmt.Fprintln(w, "note: set the same value as sv_logsecret in the game server's server.cfg; until then its log packets are rejected (GoldSrc servers are never checked)")
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
