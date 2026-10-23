// SPDX-License-Identifier: GPL-3.0-or-later

// Package simulate replays game-server log lines, either straight into the
// database (no network) or as UDP log packets to a running collector.
package simulate

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"gamewe-collector/internal/collector"
	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/settings"
	"gamewe-collector/internal/stats"
)

//go:embed sample.log
var sample []byte

// DefaultServer is the address of the simulated game server.
const DefaultServer = "127.0.0.1:27015"

// Lines returns the lines of file, or the built-in sample if file is "".
func Lines(file string) ([]string, error) {
	data := sample
	if file != "" {
		var err error
		if data, err = os.ReadFile(file); err != nil {
			return nil, err
		}
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

type Options struct {
	Server netip.AddrPort // the simulated game server
	Game   string         // its hlstats_Games code
	Send   string         // collector address; empty: write to the database directly
}

// Run registers the simulated server in hlstats_Servers (if it is not
// there yet) and replays lines: with o.Send as UDP packets from o.Server to
// a running collector, otherwise directly into the database, printing the
// RCON replies instead of sending them.
func Run(ctx context.Context, cfg config.Config, o Options, lines []string, w io.Writer, log *slog.Logger) error {
	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("database: %w", database.Explain(err))
	}
	live := settings.NewLive(settings.Defaults())
	store := stats.NewStore(db, live)
	opts, err := store.Options(ctx)
	if err != nil {
		return err
	}
	set, _ := settings.FromOptions(opts)
	set.Commands = true
	live.Set(set)
	created, err := store.EnsureServer(ctx, o.Server, o.Game, "gameWE simulation")
	if err != nil {
		return fmt.Errorf("register simulated server %s: %w", o.Server, err)
	}
	if created {
		fmt.Fprintf(w, "added %s (game %s) to hlstats_Servers\n", o.Server, o.Game)
	}
	if o.Send != "" {
		if created {
			fmt.Fprintln(w, "waiting 65 s until the collector has re-read hlstats_Servers ...")
			select {
			case <-time.After(65 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return Send(o.Send, o.Server, lines, w)
	}

	servers, err := store.Servers(ctx)
	if err != nil {
		return err
	}
	var srv stats.Server
	for _, s := range servers {
		if s.AddrPort == o.Server {
			srv = s
		}
	}
	if srv.ID == 0 || srv.Err != nil {
		return fmt.Errorf("simulated server %s is not usable in hlstats_Servers: %v", o.Server, srv.Err)
	}
	srv.RconPassword = "simulate" // replies are only printed
	p := collector.NewProcessor(store, live, srv, &collector.PrintReplier{W: w}, log)
	now := time.Now().UTC()
	for i, l := range lines {
		fmt.Fprintf(w, "%s\n", l)
		if err := p.Handle(ctx, l, now.Add(time.Duration(i)*time.Second)); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	p.Shutdown(ctx, now.Add(time.Duration(len(lines))*time.Second))
	fmt.Fprintf(w, "processed %d lines as server #%d %s\n", len(lines), srv.ID, srv.Address)
	return nil
}

// Send transmits lines as UDP log packets from the source address src to
// the collector at addr. The collector only accepts packets whose source
// is a server in hlstats_Servers.
func Send(addr string, src netip.AddrPort, lines []string, w io.Writer) error {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", net.UDPAddrFromAddrPort(src), raddr)
	if err != nil {
		return fmt.Errorf("send from %s: %w", src, err)
	}
	defer conn.Close()
	for _, l := range lines {
		if !strings.HasPrefix(l, "L ") {
			l = time.Now().Format("L 01/02/2006 - 15:04:05: ") + l
		}
		if _, err := conn.Write([]byte("\xff\xff\xff\xffR" + l + "\n\x00")); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintf(w, "sent %d log lines from %s to %s\n", len(lines), src, addr)
	return nil
}
