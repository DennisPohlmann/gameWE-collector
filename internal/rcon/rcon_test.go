// SPDX-License-Identifier: GPL-3.0-or-later

package rcon

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeServer implements enough of Source RCON to test the client: it checks
// the password, records commands and replies "ok:<cmd>", and mirrors empty
// RESPONSE_VALUE packets followed by the trailing packet Source sends.
func fakeServer(t *testing.T, password string, got chan<- string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(conn, password, got)
		}
	}()
	return ln.Addr().String()
}

func send(w io.Writer, id, typ int32, body string) {
	buf := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(buf[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	w.Write(buf)
}

func serve(conn net.Conn, password string, got chan<- string) {
	defer conn.Close()
	for {
		var hdr [12]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(hdr[0:])
		id := int32(binary.LittleEndian.Uint32(hdr[4:]))
		typ := int32(binary.LittleEndian.Uint32(hdr[8:]))
		payload := make([]byte, size-8)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		body := string(payload[:len(payload)-2])
		switch typ {
		case typeAuth:
			send(conn, id, typeResponse, "")
			if body != password {
				send(conn, -1, typeAuthResp, "")
				return
			}
			send(conn, id, typeAuthResp, "")
		case typeExec:
			got <- body
			send(conn, id, typeResponse, "ok:")
			send(conn, id, typeResponse, body)
		case typeResponse:
			send(conn, id, typeResponse, "")
			send(conn, id, typeResponse, "\x00\x00\x00\x01\x00\x00\x00\x00")
		}
	}
}

func TestExec(t *testing.T) {
	got := make(chan string, 4)
	c := &Client{Address: fakeServer(t, "pw", got), Password: "pw", Timeout: 2 * time.Second}
	defer c.Close()
	for _, cmd := range []string{`gamewe_psay 2 0 "hi"`, "status"} {
		out, err := c.Exec(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if out != "ok:"+cmd {
			t.Errorf("output %q", out)
		}
		if s := <-got; s != cmd {
			t.Errorf("server got %q", s)
		}
	}
}

func TestBadPassword(t *testing.T) {
	c := &Client{Address: fakeServer(t, "pw", make(chan string, 1)), Password: "nope", Timeout: 2 * time.Second}
	if _, err := c.Exec("status"); err != ErrAuth {
		t.Fatalf("got %v, want ErrAuth", err)
	}
}
