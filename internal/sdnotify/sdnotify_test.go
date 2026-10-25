// SPDX-License-Identifier: GPL-3.0-or-later

package sdnotify

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// listen creates a notify socket like systemd's and points NOTIFY_SOCKET
// at it.
func listen(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	t.Setenv("NOTIFY_SOCKET", name)
	return conn
}

func read(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestNoSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Ready("x"); err != nil {
		t.Fatal(err)
	}
	ExtendStart(context.Background(), time.Second)()
}

func TestBadSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "relative/path")
	if err := Ready("x"); err == nil {
		t.Fatal("want an error for a relative path")
	}
}

func TestReady(t *testing.T) {
	for _, name := range []string{filepath.Join(t.TempDir(), "notify"), "@gamewe-sdnotify-test"} {
		conn := listen(t, name)
		if err := Ready("listening on udp 0.0.0.0:41337\nREADY=0"); err != nil {
			t.Fatal(name, err)
		}
		if got, want := read(t, conn), "READY=1\nSTATUS=listening on udp 0.0.0.0:41337 READY=0"; got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
		if err := Stopping(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, conn); got != "STOPPING=1" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestExtendStart(t *testing.T) {
	conn := listen(t, filepath.Join(t.TempDir(), "notify"))
	stop := ExtendStart(context.Background(), 10*time.Millisecond)
	for range 2 {
		if got := read(t, conn); got != "EXTEND_TIMEOUT_USEC=20000" {
			t.Fatalf("got %q", got)
		}
	}
	stop()
	time.Sleep(30 * time.Millisecond) // a datagram on its way when stop was called
	buf := make([]byte, 64)
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		if _, err := conn.Read(buf); err != nil {
			break
		}
	}
	conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if n, err := conn.Read(buf); err == nil {
		t.Errorf("still extending after stop: %q", buf[:n])
	}
}
