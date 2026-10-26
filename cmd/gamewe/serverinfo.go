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
	var dry bool
	cfg, err := loadConfig("serverinfo", args, func(fs *flag.FlagSet) {
		fs.StringVar(&server, "server", "all", "serverId, ip:port or all")
		fs.BoolVar(&dry, "dry-run", false, "only show the answers, store nothing")
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
	for _, s := range servers {
		d := serverInfoDoc{ID: s.ID, Address: s.Address, Name: s.Name}
		got, err := collector.QueryServerInfo(ctx, s.AddrPort, 3*time.Second)
		if err == nil {
			info := got.Or(s.Info)
			d.Version, d.OS, d.VAC = info.Version, info.OS, info.VAC
			if info != s.Info && !dry {
				if err = store.ServerInfo(ctx, s.ID, info); err == nil {
					d.Stored = true
				}
			}
		}
		if err != nil {
			d.Error = err.Error()
			failed++
		}
		if text {
			fmt.Printf("server #%d %s %q: ", s.ID, s.Address, s.Name)
			switch {
			case d.Error != "":
				fmt.Printf("error: %s\n", d.Error)
			default:
				state := "unchanged"
				if d.Stored {
					state = "stored"
				} else if dry && (stats.ServerInfo{Version: d.Version, OS: d.OS, VAC: d.VAC}) != s.Info {
					state = "changed (dry run: not stored)"
				}
				fmt.Printf("version %q, os %q, vac %q (%s)\n", d.Version, d.OS, d.VAC, state)
			}
		}
		doc = append(doc, d)
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
