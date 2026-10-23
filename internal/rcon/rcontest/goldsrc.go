// SPDX-License-Identifier: GPL-3.0-or-later

package rcontest

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// GoldSrcServer is a fake Half-Life 1 RCON server (UDP challenge RCON).
// Answers longer than 1000 bytes go out in several packets, as HLDS does.
type GoldSrcServer struct {
	Addr     string
	Password string
	Handle   func(cmd string) string

	mu        sync.Mutex
	challenge int
	cmds      []string
}

// StartGoldSrc listens on 127.0.0.1 until the test ends.
func StartGoldSrc(t *testing.T, password string, handle func(cmd string) string) *GoldSrcServer {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &GoldSrcServer{Addr: pc.LocalAddr().String(), Password: password, Handle: handle, challenge: 123456789}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			for _, p := range s.answer(string(buf[:n])) {
				pc.WriteTo(p, from)
			}
		}
	}()
	return s
}

// NewChallenge makes the old challenge invalid (a server restart).
func (s *GoldSrcServer) NewChallenge() {
	s.mu.Lock()
	s.challenge++
	s.mu.Unlock()
}

// Commands returns the commands received so far.
func (s *GoldSrcServer) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cmds...)
}

func pkt(text string) []byte { return append([]byte("\xff\xff\xff\xffl"), text...) }

func (s *GoldSrcServer) answer(in string) [][]byte {
	if !strings.HasPrefix(in, "\xff\xff\xff\xff") {
		return nil
	}
	in = strings.TrimRight(in[4:], "\n\x00")
	s.mu.Lock()
	defer s.mu.Unlock()
	if in == "challenge rcon" {
		return [][]byte{[]byte("\xff\xff\xff\xffchallenge rcon " + strconv.Itoa(s.challenge) + "\n")}
	}
	f := strings.SplitN(in, " ", 3)
	if len(f) < 3 || f[0] != "rcon" {
		return nil
	}
	if f[1] != strconv.Itoa(s.challenge) {
		return [][]byte{pkt("Bad challenge.\n")}
	}
	rest := f[2]
	pw, cmd := "", ""
	if strings.HasPrefix(rest, `"`) {
		if i := strings.Index(rest[1:], `"`); i >= 0 {
			pw, cmd = rest[1:1+i], strings.TrimSpace(rest[2+i:])
		}
	}
	if pw != s.Password {
		return [][]byte{pkt("Bad rcon_password.\n")}
	}
	s.cmds = append(s.cmds, cmd)
	out := ""
	if s.Handle != nil {
		out = s.Handle(cmd)
	}
	var ps [][]byte
	for len(out) > 1000 {
		ps = append(ps, pkt(out[:1000]))
		out = out[1000:]
	}
	return append(ps, pkt(out))
}
