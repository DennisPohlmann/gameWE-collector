// SPDX-License-Identifier: GPL-3.0-or-later

package rcon

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func packet(id, typ int32, body string) []byte {
	buf := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(buf[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	return buf
}

// A server that keeps answering is cut off at the size limit, and one that
// trickles at Timeout, whatever the packets in between.
func TestAnswerLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pause time.Duration
		want  string
	}{{"too large", 0, "larger than"}, {"past the deadline", 20 * time.Millisecond, "timeout"}} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				read := func() int32 {
					var h [12]byte
					if _, err := c.Read(h[:]); err != nil {
						return 0
					}
					size := binary.LittleEndian.Uint32(h[0:])
					rest := make([]byte, size-8)
					c.Read(rest)
					return int32(binary.LittleEndian.Uint32(h[4:]))
				}
				c.Write(packet(read(), typeAuthResp, ""))
				id := read()
				chunk := strings.Repeat("x", 4000)
				for {
					if _, err := c.Write(packet(id, typeResponse, chunk)); err != nil {
						return
					}
					time.Sleep(tc.pause)
				}
			}()
			cl := &Client{Address: ln.Addr().String(), Password: "dummy", Timeout: 300 * time.Millisecond}
			defer cl.Close()
			start := time.Now()
			_, err = cl.Exec("status")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}
