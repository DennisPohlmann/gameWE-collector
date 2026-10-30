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
// until ctx is done. ready (if not nil) is called with the bound address
// once the socket is open, before the first packet is read.
func Serve(ctx context.Context, addr string, hub *Hub, log *slog.Logger, ready func(net.Addr)) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	log.Info("listening for game server logs", "udp", conn.LocalAddr().String())
	if ready != nil {
		ready(conn.LocalAddr())
	}
	return ServeConn(ctx, conn.(*net.UDPConn), hub, log)
}

// ServeConn reads packets from conn until ctx is done. Rejected packets
// are logged the first time per source and reason, then every
// RejectRepeat while they keep coming.
func ServeConn(ctx context.Context, conn *net.UDPConn, hub *Hub, log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	rl := newRejectLog(log, "rejected log packet")
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
			rl.note(src.String(), err, func() []any { return hub.udpHint(src, err) })
		}
	}
}

// udpHint says how to fix the rejection of a log packet from src.
func (h *Hub) udpHint(src netip.AddrPort, err error) []any {
	switch {
	case errors.Is(err, ErrUnknownPort):
		return []any{"configured_ports", h.PortsAt(src.Addr()),
			"hint", "add this server with its own port in the web admin, or fix the port of the existing one"}
	case errors.Is(err, ErrUnknownSource):
		return []any{"hint", "add the server " + src.String() + " in the web admin (Servers), if it is yours"}
	}
	return nil
}
