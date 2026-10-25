// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"gamewe-collector/internal/stats"
)

// DefaultServerInfoInterval is how often the server info (A2S_INFO) is
// queried by default (features.server_info.interval).
const DefaultServerInfoInterval = 5 * time.Minute

// When the monitor first queries the server info after start (variable for
// tests).
var firstServerInfo = 3 * time.Second

// a2sTimeout is the longest wait for one A2S_INFO answer.
const a2sTimeout = 3 * time.Second

// a2sMaxReply is the largest A2S_INFO answer read. A single packet of the
// Source query protocol is at most 1400 bytes; bigger (or split) answers are
// not parsed.
const a2sMaxReply = 1400

// Server operating systems and VAC states as stored in hlstats_Servers
// (server_os, server_vac); "" is unknown.
const (
	OSLinux     = "linux"
	OSWindows   = "windows"
	OSMac       = "mac"
	VACSecure   = "secure"
	VACInsecure = "insecure"
)

// maxVersionLen is the length of hlstats_Servers.server_version.
const maxVersionLen = 64

var a2sRequest = append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'T'}, "Source Engine Query\x00"...)

// QueryServerInfo asks the game server at addr for A2S_INFO (Valve's
// server query protocol, the server browser's query on the game port; no
// RCON password needed) and returns version, OS and VAC state. An
// S2C_CHALLENGE answer is answered once with the challenge.
func QueryServerInfo(ctx context.Context, addr netip.AddrPort, timeout time.Duration) (stats.ServerInfo, error) {
	if timeout <= 0 || timeout > a2sTimeout {
		timeout = a2sTimeout
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr.String())
	if err != nil {
		return stats.ServerInfo{}, err
	}
	defer c.Close()
	req := a2sRequest
	buf := make([]byte, a2sMaxReply+1)
	for try := 0; try < 2; try++ {
		deadline := time.Now().Add(timeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		c.SetDeadline(deadline)
		if _, err := c.Write(req); err != nil {
			return stats.ServerInfo{}, err
		}
		// A connected UDP socket only receives from addr.
		n, err := c.Read(buf)
		if err != nil {
			return stats.ServerInfo{}, fmt.Errorf("a2s_info: %w", err)
		}
		if n > a2sMaxReply {
			return stats.ServerInfo{}, errors.New("a2s_info: answer too big")
		}
		info, challenge, err := ParseA2SInfo(buf[:n])
		if err != nil {
			return stats.ServerInfo{}, err
		}
		if challenge == nil {
			return info, nil
		}
		req = append(append([]byte{}, a2sRequest...), challenge...)
	}
	return stats.ServerInfo{}, errors.New("a2s_info: no answer after the challenge")
}

// ParseA2SInfo reads an A2S_INFO answer: the Source reply ('I'), the
// obsolete GoldSrc reply ('m', no version string) or an S2C_CHALLENGE
// ('A'), for which it returns the 4 challenge bytes to send back.
func ParseA2SInfo(b []byte) (info stats.ServerInfo, challenge []byte, err error) {
	if len(b) > a2sMaxReply {
		return info, nil, errors.New("a2s_info: answer too big")
	}
	r := a2sReader{b: b}
	if h := r.bytes(4); !bytes.Equal(h, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		return info, nil, errors.New("a2s_info: not a single-packet answer")
	}
	switch r.byte() {
	case 'A':
		ch := r.bytes(4)
		if r.err != nil {
			return info, nil, r.err
		}
		return info, append([]byte{}, ch...), nil
	case 'I':
		r.byte() // protocol
		r.str()  // name
		r.str()  // map
		r.str()  // folder
		r.str()  // game
		id := r.bytes(2)
		r.bytes(4) // players, max players, bots, server type
		env := r.byte()
		r.byte() // visibility
		vac := r.byte()
		if r.err == nil && binary.LittleEndian.Uint16(id) == 2400 {
			r.bytes(3) // The Ship: mode, witnesses, duration
		}
		version := r.str()
		if r.err != nil {
			return info, nil, r.err
		}
		info = stats.ServerInfo{Version: CleanVersion(version), OS: a2sOS(env), VAC: a2sVAC(vac)}
		return info, nil, nil
	case 'm':
		r.str()    // address
		r.str()    // name
		r.str()    // map
		r.str()    // folder
		r.str()    // game
		r.bytes(4) // players, max players, protocol, server type
		env := r.byte()
		r.byte()           // visibility
		if r.byte() == 1 { // mod
			r.str()    // link
			r.str()    // download link
			r.byte()   // null
			r.bytes(8) // version, size
			r.bytes(2) // type, dll
		}
		vac := r.byte()
		if r.err != nil {
			return info, nil, r.err
		}
		return stats.ServerInfo{OS: a2sOS(env), VAC: a2sVAC(vac)}, nil, nil
	}
	if r.err != nil {
		return info, nil, r.err
	}
	return info, nil, errors.New("a2s_info: unknown answer type")
}

func a2sOS(env byte) string {
	switch env {
	case 'l', 'L':
		return OSLinux
	case 'w', 'W':
		return OSWindows
	case 'm', 'o':
		return OSMac
	}
	return ""
}

func a2sVAC(v byte) string {
	switch v {
	case 0:
		return VACInsecure
	case 1:
		return VACSecure
	}
	return ""
}

// a2sReader reads the fields of a query answer; after the first error
// every read returns zero values.
type a2sReader struct {
	b   []byte
	err error
}

var errA2SShort = errors.New("a2s_info: answer too short")

func (r *a2sReader) bytes(n int) []byte {
	if r.err != nil || len(r.b) < n {
		r.err = errA2SShort
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *a2sReader) byte() byte { return r.bytes(1)[0] }

// str reads a NUL-terminated string.
func (r *a2sReader) str() string {
	if r.err != nil {
		return ""
	}
	i := bytes.IndexByte(r.b, 0)
	if i < 0 {
		r.err = errA2SShort
		return ""
	}
	v := string(r.b[:i])
	r.b = r.b[i+1:]
	return v
}

// CleanVersion makes a version string safe to store: printable ASCII only,
// without a "/Stdio"-style suffix, at most maxVersionLen bytes.
func CleanVersion(v string) string {
	var sb strings.Builder
	for i := 0; i < len(v); i++ {
		if c := v[i]; c >= 0x20 && c < 0x7F {
			sb.WriteByte(c)
		}
	}
	v = strings.TrimSpace(sb.String())
	if i := strings.IndexByte(v, '/'); i > 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if len(v) > maxVersionLen {
		v = v[:maxVersionLen]
	}
	return v
}

var (
	// Source: "version : 9540945/24 9540945 secure", CS2 "version  : 1.40.2.2/14022 10151 secure  public",
	// GoldSrc: "version :  48/1.1.2.7/Stdio 8684 secure  (10)".
	reStatusVersion = regexp.MustCompile(`(?m)^version\s*:\s*(\S+)(.*)$`)
	// CS:GO "os      :  Linux", CS2 "os/type  : Linux dedicated".
	reStatusOS = regexp.MustCompile(`(?mi)^os(?:/type)?\s*:\s*(\S+)`)
	reSecure   = regexp.MustCompile(`(?i)\b(in)?secure\b`)
)

// parseStatusInfo reads version, OS and VAC state from a "status" answer
// (whatever of them it lists).
func parseStatusInfo(out string) stats.ServerInfo {
	var info stats.ServerInfo
	if m := reStatusVersion.FindStringSubmatch(out); m != nil {
		v := m[1]
		// GoldSrc lists protocol/version/build: the second part.
		if parts := strings.Split(v, "/"); len(parts) >= 3 {
			v = parts[1]
		}
		info.Version = CleanVersion(v)
		if s := reSecure.FindStringSubmatch(m[2]); s != nil {
			info.VAC = VACSecure
			if s[1] != "" {
				info.VAC = VACInsecure
			}
		}
	}
	if m := reStatusOS.FindStringSubmatch(out); m != nil {
		switch strings.ToLower(m[1]) {
		case "linux":
			info.OS = OSLinux
		case "windows", "win32", "win64":
			info.OS = OSWindows
		case "mac", "macos", "osx":
			info.OS = OSMac
		}
	}
	return info
}
