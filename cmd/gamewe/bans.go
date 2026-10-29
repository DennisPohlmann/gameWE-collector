// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/sourcebans"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/steam"
)

const bansUsage = `Usage:
  gamewe bans [--dry-run|-n] [--force|-f] [-config FILE]

Runs one complete pass of the bans-sync job (features.bans_sync), also when
it is off: deletes the SourceBans bans whose end has passed, reads the whole
SourceBans list (with features.sourcebans on and an address set in the web
admin) and asks the Steam Web API for every account that is due (never
asked, or the answer is older than features.bans_sync.steam_recheck), one
request of 100 accounts per steam_pause. The results are stored in
hlstats_Bans and hlstats_Bans_Players for the website. It shows how many
accounts are due and asks [y/N]; --force (-f) does not ask (scripts, cron),
--dry-run (-n) only counts them. A running collector does the same on its
own; running both at once only asks Steam twice.`

// bansCmd runs `gamewe bans`.
func bansCmd(args []string) error {
	return bansWith(args, os.Stdin, os.Stdout)
}

func bansWith(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("bans", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, bansUsage) }
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	var cf changeFlags
	cf.add(fs, "only count the accounts")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return exitError{2, fmt.Sprintf("unexpected argument %q\n\n%s", pos[0], bansUsage)}
	}
	if err := cf.check(); err != nil {
		return err
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
	ctx := context.Background()
	set := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, set)
	opts, err := store.Options(ctx)
	if err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	s, _ := settings.FromOptions(opts)
	set.Set(s)
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	w := newBanSync(cfg, store, set, log)
	w.Config.Enabled = true
	if err := w.Count(ctx); err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	key := set.Get().SteamAPIKey != ""
	fmt.Fprintf(out, "%d accounts due at Steam (Steam Web API key set: %v), SourceBans list: %v\n", w.Due(), key, w.Config.SourceBans && sourcebans.Base(set.Get().SourceBansURL) != "")
	if cf.dryRun {
		fmt.Fprintln(out, "dry run: nothing changed")
		return nil
	}
	if err := approve(ctx, cf, in, out, interactive(in), "Read the bans now?"); err != nil {
		return err
	}
	if err := w.Once(ctx); err != nil {
		return err
	}
	checked, banned, sb, err := store.BanCounts(ctx)
	if err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	fmt.Fprintf(out, "done: %d accounts checked at Steam, %d of them banned, %d running SourceBans bans; %d still due\n", checked, banned, sb, w.Due())
	return nil
}

// newBanSync builds the bans-sync job from the config (validated before).
func newBanSync(cfg config.Config, store *stats.Store, set *settings.Live, log *slog.Logger) *collector.BanSync {
	bs, sb := cfg.Features.BansSync, cfg.Features.SourceBans
	recheck, _ := bs.Recheck()
	pause, _ := bs.Pause()
	sbEvery, _ := sb.Every()
	loc, _ := sb.Location(cfg.Timezone)
	client := &sourcebans.Client{AllowPrivate: sb.AllowPrivate}
	return &collector.BanSync{Store: store, Set: set, Steam: (&steam.Client{}).PlayerBans, List: client.List, Log: log, Loc: loc,
		Config: collector.BanSyncConfig{Enabled: bs.Enabled, Recheck: recheck, Pause: pause, SourceBans: sb.Enabled, SourceBansEvery: sbEvery}}
}
