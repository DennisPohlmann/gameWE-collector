// SPDX-License-Identifier: GPL-3.0-or-later

// Package rcontest is a fake Source RCON server for tests.
package rcontest

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
)

// Packet types of Source RCON.
const (
	typeResponse = 0
	typeExec     = 2
	typeAuthResp = 2
	typeAuth     = 3
)

// Server answers commands with Handle (default: empty answer) and records
// them.
type Server struct {
	Addr     string
	Password string
	Handle   func(cmd string) string

	mu   sync.Mutex
	cmds []string
}

// Start listens on 127.0.0.1 until the test ends.
func Start(t *testing.T, password string, handle func(cmd string) string) *Server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), Password: password, Handle: handle}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

// Commands returns the commands received so far.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cmds...)
}

func send(w io.Writer, id, typ int32, body string) {
	buf := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(buf[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	w.Write(buf)
}

func (s *Server) serve(conn net.Conn) {
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
			if body != s.Password {
				send(conn, -1, typeAuthResp, "")
				return
			}
			send(conn, id, typeAuthResp, "")
		case typeExec:
			s.mu.Lock()
			s.cmds = append(s.cmds, body)
			s.mu.Unlock()
			out := ""
			if s.Handle != nil {
				out = s.Handle(body)
			}
			// Source splits long output into packets of at most 4096 bytes.
			for len(out) > 4096 {
				send(conn, id, typeResponse, out[:4096])
				out = out[4096:]
			}
			send(conn, id, typeResponse, out)
		case typeResponse:
			send(conn, id, typeResponse, "")
			send(conn, id, typeResponse, "\x00\x00\x00\x01\x00\x00\x00\x00")
		}
	}
}
