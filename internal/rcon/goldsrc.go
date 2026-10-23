// SPDX-License-Identifier: GPL-3.0-or-later

package rcon

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Conn is an RCON connection: Source (TCP, *Client) or GoldSrc (UDP,
// *GoldSrc).
type Conn interface {
	Exec(cmd string) (string, error)
	LocalAddr() net.Addr
	Close() error
}

// New returns the RCON client for a server's engine: goldsrc selects the
// Half-Life 1 UDP protocol, everything else Source TCP RCON.
func New(goldsrc bool, address, password string, timeout time.Duration) Conn {
	if goldsrc {
		return &GoldSrc{Address: address, Password: password, Timeout: timeout}
	}
	return &Client{Address: address, Password: password, Timeout: timeout}
}

var (
	oob = []byte{0xff, 0xff, 0xff, 0xff}
	// ErrBadChallenge: the server rejected the challenge (it changes when
	// the server restarts); the next Exec asks for a new one.
	ErrBadChallenge = errors.New("rcon: bad challenge")
)

// goldsrcLinger is how long Exec keeps reading after the first answer
// packet: GoldSrc sends long output in several packets without an end mark.
const goldsrcLinger = 150 * time.Millisecond

// GoldSrc is the Half-Life 1 (GoldSrc) RCON client: connectionless UDP
// packets to the game port, "challenge rcon" first, then
// rcon <challenge> "<password>" <command>. It is safe for concurrent use;
// commands are sent one at a time.
type GoldSrc struct {
	Address  string
	Password string
	Timeout  time.Duration

	mu        sync.Mutex
	conn      net.Conn
	challenge string
}

// Exec runs a console command and returns its output.
func (g *GoldSrc) Exec(cmd string) (string, error) {
	if strings.ContainsAny(cmd, "\x00\n\r") || strings.ContainsAny(g.Password, "\"\x00\n\r") {
		return "", errors.New("rcon: command or password contains a line break, NUL or (password) quote")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.exec(cmd)
	if errors.Is(err, ErrBadChallenge) {
		g.challenge = ""
		out, err = g.exec(cmd)
	}
	if err != nil && !errors.Is(err, ErrAuth) {
		g.closeLocked()
	}
	return out, err
}

func (g *GoldSrc) exec(cmd string) (string, error) {
	if g.conn == nil {
		c, err := net.DialTimeout("udp", g.Address, g.Timeout)
		if err != nil {
			return "", fmt.Errorf("rcon %s: %w", g.Address, err)
		}
		g.conn, g.challenge = c, ""
	}
	if g.challenge == "" {
		ans, err := g.roundTrip("challenge rcon", false)
		if err != nil {
			return "", fmt.Errorf("rcon %s challenge: %w", g.Address, err)
		}
		f := strings.Fields(ans)
		if len(f) < 3 || f[0] != "challenge" || f[1] != "rcon" {
			return "", fmt.Errorf("rcon %s: unexpected challenge answer %q", g.Address, trim(ans))
		}
		g.challenge = f[2]
	}
	out, err := g.roundTrip(fmt.Sprintf(`rcon %s "%s" %s`, g.challenge, g.Password, cmd), true)
	if err != nil {
		return "", fmt.Errorf("rcon %s: %w", g.Address, err)
	}
	switch t := strings.TrimSpace(out); {
	case strings.HasPrefix(t, "Bad rcon_password"):
		return "", ErrAuth
	case strings.HasPrefix(t, "Bad challenge"):
		return "", ErrBadChallenge
	case strings.HasPrefix(t, "You have been banned"):
		return "", fmt.Errorf("rcon %s: the server banned the collector's address (too many bad passwords?)", g.Address)
	}
	return out, nil
}

// roundTrip sends one connectionless packet and returns the text of the
// answer: the first packet, and with more the packets that follow within
// goldsrcLinger.
func (g *GoldSrc) roundTrip(cmd string, more bool) (string, error) {
	g.conn.SetWriteDeadline(time.Now().Add(g.Timeout))
	if _, err := g.conn.Write(append(append([]byte{}, oob...), cmd+"\n"...)); err != nil {
		return "", err
	}
	var out strings.Builder
	buf := make([]byte, 65536)
	deadline := time.Now().Add(g.Timeout)
	for got := false; ; {
		g.conn.SetReadDeadline(deadline)
		n, err := g.conn.Read(buf)
		if err != nil {
			var ne net.Error
			if got && errors.As(err, &ne) && ne.Timeout() {
				return out.String(), nil
			}
			return "", err
		}
		p, ok := payload(buf[:n])
		if !ok {
			continue // not an answer packet
		}
		out.WriteString(p)
		got = true
		if !more || out.Len() > 1<<20 {
			return out.String(), nil
		}
		deadline = time.Now().Add(goldsrcLinger)
	}
}

// payload returns the text of a GoldSrc answer packet: \xff\xff\xff\xff and
// a type byte ('l' print, 'n' or 'A'/'9' in some builds) before the text.
// Split packets (\xfe\xff\xff\xff) are not used for RCON output.
func payload(p []byte) (string, bool) {
	if len(p) < 5 || !bytes.Equal(p[:4], oob) {
		return "", false
	}
	p = p[4:]
	if bytes.HasPrefix(p, []byte("challenge")) {
		return string(bytes.TrimRight(p, "\x00\n")), true
	}
	return string(bytes.TrimRight(p[1:], "\x00")), true
}

func trim(s string) string {
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.TrimSpace(s)
}

// LocalAddr is the collector's address of the UDP socket (nil before the
// first command).
func (g *GoldSrc) LocalAddr() net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn == nil {
		return nil
	}
	return g.conn.LocalAddr()
}

func (g *GoldSrc) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closeLocked()
}

func (g *GoldSrc) closeLocked() error {
	if g.conn == nil {
		return nil
	}
	err := g.conn.Close()
	g.conn, g.challenge = nil, ""
	return err
}
