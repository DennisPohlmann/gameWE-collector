// SPDX-License-Identifier: GPL-3.0-or-later

// Package rcon is a minimal Source RCON (TCP) client.
// Protocol: https://developer.valvesoftware.com/wiki/Source_RCON_Protocol
package rcon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	typeResponse = 0
	typeExec     = 2
	typeAuthResp = 2
	typeAuth     = 3

	maxPacket = 4096 + 10
)

var ErrAuth = errors.New("rcon authentication failed (wrong rcon_password?)")

// Client keeps one authenticated connection and reconnects on demand.
// It is safe for concurrent use; commands are sent one at a time.
type Client struct {
	Address  string
	Password string
	Timeout  time.Duration

	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
	id   int32
}

// Exec runs a console command and returns its output. On any error the
// connection is dropped, and the next call reconnects.
func (c *Client) Exec(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		if err := c.connect(); err != nil {
			return "", err
		}
	}
	out, err := c.exec(cmd)
	if err != nil {
		c.closeLocked()
	}
	return out, err
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

// LocalAddr is the collector's address of the open connection (nil if
// none): the address the game server sees.
func (c *Client) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	return c.conn.LocalAddr()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn, c.r = nil, nil
	return err
}

func (c *Client) connect() error {
	conn, err := net.DialTimeout("tcp", c.Address, c.Timeout)
	if err != nil {
		return fmt.Errorf("rcon %s: %w", c.Address, err)
	}
	c.conn, c.r = conn, bufio.NewReader(conn)
	id := c.nextID()
	if err := c.write(id, typeAuth, c.Password); err != nil {
		c.closeLocked()
		return err
	}
	// The server answers with an empty RESPONSE_VALUE followed by
	// AUTH_RESPONSE; its id is -1 if the password was wrong.
	for {
		rid, typ, _, err := c.read()
		if err != nil {
			c.closeLocked()
			return fmt.Errorf("rcon %s auth: %w", c.Address, err)
		}
		if typ != typeAuthResp {
			continue
		}
		if rid == -1 || rid != id {
			c.closeLocked()
			return ErrAuth
		}
		return nil
	}
}

func (c *Client) exec(cmd string) (string, error) {
	id := c.nextID()
	if err := c.write(id, typeExec, cmd); err != nil {
		return "", err
	}
	// An empty RESPONSE_VALUE packet sent right after the command is
	// mirrored back once all output has been sent; that marks the end of
	// multi-packet responses. Packets with other ids (e.g. the trailing
	// packet Source sends after the mirror) are skipped.
	end := c.nextID()
	if err := c.write(end, typeResponse, ""); err != nil {
		return "", err
	}
	var out []byte
	for {
		rid, typ, body, err := c.read()
		if err != nil {
			return "", err
		}
		switch {
		case rid == end:
			return string(out), nil
		case rid == id && typ == typeResponse:
			out = append(out, body...)
		}
	}
}

func (c *Client) nextID() int32 {
	c.id++
	if c.id <= 0 {
		c.id = 1
	}
	return c.id
}

func (c *Client) write(id, typ int32, body string) error {
	if len(body) > maxPacket-10 {
		return fmt.Errorf("rcon command too long (%d bytes)", len(body))
	}
	buf := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(buf[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	c.conn.SetWriteDeadline(time.Now().Add(c.Timeout))
	_, err := c.conn.Write(buf)
	return err
}

func (c *Client) read() (id, typ int32, body []byte, err error) {
	c.conn.SetReadDeadline(time.Now().Add(c.Timeout))
	var hdr [12]byte
	if _, err = io.ReadFull(c.r, hdr[:]); err != nil {
		return
	}
	size := int32(binary.LittleEndian.Uint32(hdr[0:]))
	id = int32(binary.LittleEndian.Uint32(hdr[4:]))
	typ = int32(binary.LittleEndian.Uint32(hdr[8:]))
	if size < 10 || size > maxPacket {
		err = fmt.Errorf("rcon: invalid packet size %d", size)
		return
	}
	payload := make([]byte, size-8)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	body = payload[:len(payload)-2]
	return
}
