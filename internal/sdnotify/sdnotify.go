// SPDX-License-Identifier: GPL-3.0-or-later

// Package sdnotify tells systemd about the state of the service
// (sd_notify(3), Type=notify units): READY=1 once the collector works, so
// that "systemctl start" and "restart" wait for it and report a start that
// fails. Pure Go, no libsystemd: one datagram to the unix socket in
// $NOTIFY_SOCKET. Without that variable (no systemd, a container, a shell)
// every call does nothing.
package sdnotify

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Send sends state (lines like "READY=1" or "STATUS=...") to the socket in
// $NOTIFY_SOCKET. It returns nil without doing anything when the variable
// is not set.
func Send(state string) error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "@") {
		return fmt.Errorf("NOTIFY_SOCKET %q: not a unix socket path", path)
	}
	// "@name" is a socket in the abstract namespace; Go writes those with
	// a leading "@" as well.
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

// Ready says that the service is up: systemctl start / restart return now.
func Ready(status string) error { return Send("READY=1\nSTATUS=" + oneLine(status)) }

// Stopping says that the service is shutting down.
func Stopping() error { return Send("STOPPING=1") }

// Status sets the text "systemctl status" shows.
func Status(status string) error { return Send("STATUS=" + oneLine(status)) }

// ExtendStart asks systemd for more start time (TimeoutStartSec) every
// interval, each time for twice the interval, until ctx is done or the
// returned stop is called. For long steps before Ready, such as a database
// migration.
func ExtendStart(ctx context.Context, interval time.Duration) (stop func()) {
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	msg := fmt.Sprintf("EXTEND_TIMEOUT_USEC=%d", (2 * interval).Microseconds())
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		Send(msg)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				Send(msg)
			}
		}
	}()
	return cancel
}

// oneLine keeps a status to one line: a newline would start a new
// assignment in the datagram.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}
