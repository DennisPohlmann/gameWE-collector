// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

// serverInfoCmd runs `gamewe serverinfo`: the A2S_INFO query of
// features.server_info now, also when that is off. Changed values are
// stored in hlstats_Servers as the collector does (a field the server does
// not report keeps its stored value).
func serverInfoCmd(args []string) error {
	var server string
	var cf changeFlags
	cfg, err := loadConfig("serverinfo", args, func(fs *flag.FlagSet) {
		fs.StringVar(&server, "server", "all", "serverId, ip:port or all")
		cf.add(fs, "only show the answers")
	})
	if err != nil {
		return err
	}
	if err := cf.check(); err != nil {
		return err
	}
	servers, err := gameServers(cfg, server)
	if err != nil {
		return err
	}
	if err := needList(true); err != nil {
		return err
	}
	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	store := stats.NewStore(db, settings.NewLive(settings.Defaults()))
	ctx := context.Background()
	text := !structured()
	failed := 0
	doc := serverInfoList{}
	var changed []int // indexes into doc
	for _, s := range servers {
		d := serverInfoDoc{ID: s.ID, Address: s.Address, Name: s.Name}
		got, err := collector.QueryServerInfo(ctx, s.AddrPort, 3*time.Second)
		if err != nil {
			d.Error = err.Error()
			failed++
		} else {
			info := got.Or(s.Info)
			d.Version, d.OS, d.VAC = info.Version, info.OS, info.VAC
			if info != s.Info {
				changed = append(changed, len(doc))
			}
		}
		if text {
			fmt.Printf("server #%d %s %q: ", s.ID, s.Address, s.Name)
			if d.Error != "" {
				fmt.Printf("error: %s\n", d.Error)
			} else {
				state := "unchanged"
				if info := (stats.ServerInfo{Version: d.Version, OS: d.OS, VAC: d.VAC}); info != s.Info {
					state = fmt.Sprintf("changed, stored: version %q, os %q, vac %q", s.Info.Version, s.Info.OS, s.Info.VAC)
				}
				fmt.Printf("version %q, os %q, vac %q (%s)\n", d.Version, d.OS, d.VAC, state)
			}
		}
		doc = append(doc, d)
	}
	switch {
	case len(changed) == 0:
	case cf.dryRun:
		fmt.Fprintf(textOut(), "dry run: nothing changed (%d servers would be updated in hlstats_Servers)\n", len(changed))
	default:
		err := approve(ctx, cf, os.Stdin, textOut(), stdinIsTerminal(), fmt.Sprintf("Store the changed values of %d servers in hlstats_Servers now?", len(changed)))
		if err != nil {
			return err
		}
		for _, i := range changed {
			d := &doc[i]
			if err := store.ServerInfo(ctx, d.ID, stats.ServerInfo{Version: d.Version, OS: d.OS, VAC: d.VAC}); err != nil {
				return fmt.Errorf("database: %w", database.Explain(err))
			}
			d.Stored = true
		}
		fmt.Fprintf(textOut(), "done: %d servers updated\n", len(changed))
	}
	if !text {
		if err := emit(os.Stdout, doc); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d servers did not answer", failed, len(servers))
	}
	return nil
}

type serverInfoDoc struct {
	ID      int64  `json:"id"`
	Address string `json:"address"`
	Name    string `json:"name"`
	Version string `json:"version"`
	OS      string `json:"os"`
	VAC     string `json:"vac"`
	Stored  bool   `json:"stored"` // changed and written to hlstats_Servers
	Error   string `json:"error,omitempty"`
}

type serverInfoList []serverInfoDoc

func (serverInfoList) Header() []string {
	return []string{"id", "address", "version", "os", "vac", "stored", "error"}
}
func (l serverInfoList) Rows() [][]string {
	out := [][]string{}
	for _, s := range l {
		out = append(out, []string{itoa(s.ID), s.Address, s.Version, s.OS, s.VAC, yesNo(s.Stored), s.Error})
	}
	return out
}
