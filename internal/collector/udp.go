// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// Serve listens for UDP log packets on addr and hands them to the hub
// until ctx is done.
func Serve(ctx context.Context, addr string, hub *Hub, log *slog.Logger) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	log.Info("listening for game server logs", "udp", conn.LocalAddr().String())
	return ServeConn(ctx, conn.(*net.UDPConn), hub, log)
}

// ServeConn reads packets from conn until ctx is done. Rejected packets
// are logged the first time per source and reason, then every 1000th time.
func ServeConn(ctx context.Context, conn *net.UDPConn, hub *Hub, log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	type key struct {
		src    string
		reason error
	}
	counts := map[key]uint64{}
	buf := make([]byte, 65535)
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Warn("udp read", "err", err)
			continue
		}
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port()) // log IPv4 as a.b.c.d
		if err := hub.Dispatch(src, buf[:n], time.Now().UTC()); err != nil {
			k := key{src.String(), err}
			if len(counts) > 10000 {
				counts = map[key]uint64{} // bound memory under a flood of sources
			}
			counts[k]++
			if c := counts[k]; c == 1 || c%1000 == 0 {
				log.Warn("rejected log packet", "source", src.String(), "reason", err, "count", c)
			}
		}
	}
}
