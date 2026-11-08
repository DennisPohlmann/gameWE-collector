// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/version"
)

const deletePlayerUsage = `Usage:
  gamewe delete-player GAME PLAYER-ID [--dry-run|-n] [--force|-f] [-wait DURATION] [-config FILE]
  gamewe delete-player -list [-config FILE]

Deletes the player PLAYER-ID (hlstats_Players.playerId, the number in the
web admin's player address) of the ranking GAME with all of their rows, for
erasure requests, as "Delete player data" in the web admin does. A player id
belongs to one ranking: whoever plays in three rankings has three ids and
needs three runs (gamewe jobs shows them). Kills shared with another player
go too, that player's totals stay; whoever plays again is recorded as a new
player. This cannot be undone. Without --force it asks for the player id to
confirm; --dry-run only checks that the player is in the ranking.

With a running collector (heartbeat less than 2 minutes old) the erasure is
queued for it: it pauses the ranking's servers, deletes and goes on.
Otherwise it runs here; then no collector may be running.
-list shows the latest erasures (gamewe jobs: all jobs).
-o json|yaml writes the result as a document; -list also -o csv|tsv|name.`

// deletePlayerCmd runs `gamewe delete-player`.
func deletePlayerCmd(args []string) error {
	return deletePlayerWith(args, os.Stdin, os.Stdout)
}

func deletePlayerWith(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("delete-player", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, deletePlayerUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	var cf changeFlags
	cf.add(fs, "only check that the player is in the ranking")
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for the running collector, or for another job to finish")
	list := fs.Bool("list", false, "show the latest erasures")
	addOutputFlag(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if err := needList(*list); err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	if *list {
		return showJobs(*path, stats.JobFilter{Types: []string{stats.JobDeletePlayer}, Limit: 20}, out)
	}
	if len(pos) != 2 {
		return exitError{2, deletePlayerUsage}
	}
	game, id := pos[0], pos[1]
	playerID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || playerID <= 0 {
		return exitError{2, fmt.Sprintf("player id %q: must be a number above 0", id)}
	}
	return captured(out, func(out io.Writer, _ *result) error {
		return deletePlayerRun(*path, game, playerID, cf, *wait, in, out)
	})
}

func deletePlayerRun(path, game string, playerID int64, cf changeFlags, wait time.Duration, in io.Reader, out io.Writer) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	switch ok, err := store.PlayerInGame(ctx, playerID, game); {
	case err != nil:
		return fmt.Errorf("database: %w", database.Explain(err))
	case !ok:
		return fmt.Errorf("no player %d in ranking %s (a player id belongs to one ranking)", playerID, game)
	}
	id := strconv.FormatInt(playerID, 10)
	fmt.Fprintf(out, "player %s of ranking %s: all of their rows will be deleted\n", id, game)
	if cf.dryRun {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if !cf.force {
		if !interactive(in) {
			return errNoTTYYes
		}
		line, _ := ask(ctx, in, promptOut(out), fmt.Sprintf("This cannot be undone. Type the player id (%s) to delete the player: ", id))
		if strings.TrimSpace(line) != id {
			return errNotConfirmed
		}
	}
	jobs, err := jobsTable(ctx, db, store)
	if err != nil {
		return err
	}
	here := func() (string, error) {
		release, err := database.JobLock(ctx, db, wait, func() { fmt.Fprintln(out, "waiting for another job to finish ...") })
		if err != nil {
			return "", err
		}
		defer release()
		res, err := store.DeletePlayer(ctx, playerID)
		if err != nil {
			return "", fmt.Errorf("stopped (run it again to complete it): %w; done before: %s", err, stats.FormatCounts(res))
		}
		return stats.FormatCounts(res), nil
	}
	// The job keeps the player id in mode, as the web admin queues it.
	origin := stats.JobOrigin{Feature: stats.OriginCLI, Command: "gamewe delete-player " + game + " " + id, Version: version.Version}
	return runJob(ctx, store, jobs, stats.JobDeletePlayer, game, id, origin, wait, out, "deleting here", here)
}
