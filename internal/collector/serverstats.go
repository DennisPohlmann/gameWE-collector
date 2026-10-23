// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/stats"
)

// ServerStats is the answer of the RCON command "stats".
type ServerStats struct {
	CPU    int // percent, rounded
	FPS    int // server frames per second, rounded
	Uptime int // minutes since the server process started
}

// RconStats asks the game server for "stats" over RCON.
func RconStats(srv stats.Server, timeout time.Duration) (ServerStats, error) {
	c := connKey{srv.AddrPort.String(), srv.RconPassword, timeout, srv.Engine}.dial()
	defer c.Close()
	out, err := c.Exec("stats")
	if err != nil {
		return ServerStats{}, err
	}
	return ParseStats(out)
}

// ParseStats reads the output of "stats". Source servers answer
//
//	CPU    In (KB/s)  Out (KB/s)  Uptime  Map changes  FPS      Players  Connects
//	0.00   0.00       0.00        6       1            64.00    0        0
//
// (older ones "CPU In Out Uptime Users FPS Players"): CPU, uptime (minutes)
// and FPS are the 1st, 4th and 6th value of the line after the header.
func ParseStats(out string) (ServerStats, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "CPU") || i+1 >= len(lines) {
			continue
		}
		f := strings.Fields(lines[i+1])
		if len(f) < 6 {
			break
		}
		var v [3]float64
		for j, k := range []int{0, 3, 5} {
			x, err := strconv.ParseFloat(f[k], 64)
			if err != nil {
				return ServerStats{}, errors.New("unexpected stats output")
			}
			v[j] = x
		}
		return ServerStats{CPU: int(math.Round(v[0])), Uptime: int(math.Round(v[1])), FPS: int(math.Round(v[2]))}, nil
	}
	return ServerStats{}, errors.New("unexpected stats output")
}
