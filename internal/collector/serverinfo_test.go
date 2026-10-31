// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/stats"
)

// a2sSourceReply builds an A2S_INFO 'I' answer.
func a2sSourceReply(appID uint16, env, vac byte, version string) []byte {
	b := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'I', 17}
	for _, s := range []string{"Example Server", "de_dust2", "cstrike", "Counter-Strike: Source"} {
		b = append(append(b, s...), 0)
	}
	b = append(b, byte(appID), byte(appID>>8), 3, 24, 1, 'd', env, 0, vac)
	if appID == 2400 {
		b = append(b, 0, 1, 2)
	}
	b = append(append(b, version...), 0)
	return append(b, 0x80, 0x87, 0x69) // EDF + port, ignored
}

// a2sGoldSrcReply builds the obsolete GoldSrc 'm' answer.
func a2sGoldSrcReply(env, vac byte, mod bool) []byte {
	b := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'm'}
	for _, s := range []string{"203.0.113.5:27015", "Example CS 1.6", "de_dust2", "cstrike", "Counter-Strike"} {
		b = append(append(b, s...), 0)
	}
	b = append(b, 3, 32, 47, 'd', env, 0)
	if mod {
		b = append(b, 1)
		b = append(append(b, "http://example.com"...), 0)
		b = append(append(b, "http://example.com/dl"...), 0)
		b = append(b, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 1)
	} else {
		b = append(b, 0)
	}
	return append(b, vac, 2)
}

func TestParseA2SInfo(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want stats.ServerInfo
	}{
		{"source linux secure", a2sSourceReply(240, 'l', 1, "9540945"), stats.ServerInfo{Version: "9540945", OS: OSLinux, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}},
		{"cs2 windows insecure", a2sSourceReply(730, 'w', 0, "1.40.2.2"), stats.ServerInfo{Version: "1.40.2.2", OS: OSWindows, VAC: VACInsecure, MaxPlayers: 24, Name: "Example Server"}},
		{"mac", a2sSourceReply(440, 'm', 1, "8622567"), stats.ServerInfo{Version: "8622567", OS: OSMac, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}},
		{"the ship", a2sSourceReply(2400, 'w', 1, "1.0.0.4"), stats.ServerInfo{Version: "1.0.0.4", OS: OSWindows, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}},
		{"goldsrc version suffix", a2sSourceReply(10, 'l', 1, "1.1.2.7/Stdio"), stats.ServerInfo{Version: "1.1.2.7", OS: OSLinux, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}},
		{"unknown env and vac", a2sSourceReply(240, 'x', 7, "1"), stats.ServerInfo{Version: "1", MaxPlayers: 24, Name: "Example Server"}},
		{"hostile version", a2sSourceReply(240, 'l', 1, "1.0\x1b[31m\n<b>\xff"+strings.Repeat("9", 100)), stats.ServerInfo{
			Version: ("1.0[31m<b>" + strings.Repeat("9", 100))[:maxVersionLen], OS: OSLinux, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}},
		{"goldsrc m", a2sGoldSrcReply('l', 1, false), stats.ServerInfo{OS: OSLinux, VAC: VACSecure, MaxPlayers: 32, Name: "Example CS 1.6"}},
		{"goldsrc m mod", a2sGoldSrcReply('w', 0, true), stats.ServerInfo{OS: OSWindows, VAC: VACInsecure, MaxPlayers: 32, Name: "Example CS 1.6"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ch, err := ParseA2SInfo(tc.in)
			if err != nil || ch != nil || got != tc.want {
				t.Errorf("got %+v %v %v, want %+v", got, ch, err, tc.want)
			}
		})
	}
	_, ch, err := ParseA2SInfo([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'A', 1, 2, 3, 4})
	if err != nil || !bytes.Equal(ch, []byte{1, 2, 3, 4}) {
		t.Errorf("challenge %v %v", ch, err)
	}
	full := a2sSourceReply(240, 'l', 1, "9540945")
	for _, bad := range [][]byte{
		nil,
		{0xFF, 0xFF, 0xFF},
		{0xFE, 0xFF, 0xFF, 0xFF, 'I'},       // split packet
		{0xFF, 0xFF, 0xFF, 0xFF, 'A', 1, 2}, // short challenge
		{0xFF, 0xFF, 0xFF, 0xFF, 'Z', 0, 0, 0, 0, 0}, // unknown type
		full[:len(full)-4],                           // version without NUL
		full[:20],
		append(append([]byte{}, full...), make([]byte, a2sMaxReply)...),
	} {
		if info, ch, err := ParseA2SInfo(bad); err == nil {
			t.Errorf("% x: no error (%+v %v)", bad, info, ch)
		}
	}
}

// A server that wants the challenge first (S2C_CHALLENGE), as newer
// Source servers do.
func TestQueryServerInfoChallenge(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	reqs := make(chan []byte, 4)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := append([]byte{}, buf[:n]...)
			reqs <- req
			if bytes.HasSuffix(req, []byte{9, 8, 7, 6}) {
				pc.WriteTo(a2sSourceReply(240, 'w', 1, "7604765"), from)
			} else {
				pc.WriteTo([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'A', 9, 8, 7, 6}, from)
			}
		}
	}()
	addr := netip.MustParseAddrPort(pc.LocalAddr().String())
	info, err := QueryServerInfo(context.Background(), addr, time.Second)
	if err != nil || info != (stats.ServerInfo{Version: "7604765", OS: OSWindows, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}) {
		t.Fatalf("got %+v %v", info, err)
	}
	if first := <-reqs; !bytes.Equal(first, a2sRequest) {
		t.Errorf("first request % x", first)
	}
}

func TestQueryServerInfoTimeout(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close() // never answers
	start := time.Now()
	_, err = QueryServerInfo(context.Background(), netip.MustParseAddrPort(pc.LocalAddr().String()), 100*time.Millisecond)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err %v after %v", err, time.Since(start))
	}
}

// Real-looking status answers of the engines.
func TestParseStatusInfo(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     stats.ServerInfo
	}{
		{"css", cssStatus, stats.ServerInfo{Version: "7604765", VAC: VACSecure}},
		{"hlds", hldsStatus, stats.ServerInfo{Version: "1.1.2.7", VAC: VACSecure}},
		{"csgo", `hostname: Example CS:GO
version : 1.38.8.1/13881 1575/8012 insecure
udp/ip  : 0.0.0.0:27015  (public ip: 203.0.113.7)
os      :  Linux
type    :  community dedicated
map     : de_mirage
players : 0 humans, 0 bots (12/0 max) (not hibernating)
`, stats.ServerInfo{Version: "1.38.8.1", OS: OSLinux, VAC: VACInsecure}},
		{"cs2", `Server:  Running [0.0.0.0:27015]
hostname  : Example CS2
spawn     : 1
version   : 1.40.2.2/14022 10151 secure  public
udp/ip    : 0.0.0.0:27015 (public 203.0.113.8:27015)
os/type   : Windows dedicated
map       : de_inferno
players   : 2 humans, 0 bots (16 max) (not hibernating) (unreserved)
`, stats.ServerInfo{Version: "1.40.2.2", OS: OSWindows, VAC: VACSecure}},
		{"tf2", `hostname: Example TF2
version : 8622567/24 8622567 secure
udp/ip  : 203.0.113.9:27015  (public ip: 203.0.113.9)
map     : ctf_2fort at: 0 x, 0 y, 0 z
`, stats.ServerInfo{Version: "8622567", VAC: VACSecure}},
		{"hostile", "hostname: x\nversion : \x01\x02<script>" + strings.Repeat("a", 200) + " secureX\nos : BeOS\nmap : y\n",
			stats.ServerInfo{Version: ("<script>" + strings.Repeat("a", 200))[:maxVersionLen]}},
		{"none", "hostname: x\nmap : y\n", stats.ServerInfo{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := ParseStatus(tc.in)
			if err != nil || st.Info != tc.want {
				t.Errorf("got %+v %v, want %+v", st.Info, err, tc.want)
			}
		})
	}
}

// The processor writes only changes; A2S wins over status, a value nobody
// reports any more stays.
func TestProcessorServerInfo(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx := context.Background()
	p.srv.storedInfo = stats.ServerInfo{Version: "1", OS: OSLinux, VAC: VACSecure}
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", OS: OSLinux, VAC: VACSecure}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(st.infos) != 0 {
		t.Fatalf("unchanged info written: %v", st.infos)
	}
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "2", OS: OSLinux, VAC: VACInsecure}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	status, _ := ParseStatus(cssStatus) // version 7604765 secure: A2S wins
	if err := p.Status(ctx, status, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.ServerInfo(ctx, stats.ServerInfo{}, time.Now().UTC()); err != nil { // no answer content: status fills in
		t.Fatal(err)
	}
	want := []stats.ServerInfo{{Version: "2", OS: OSLinux, VAC: VACInsecure}, {Version: "7604765", OS: OSLinux, VAC: VACSecure}}
	if len(st.infos) != len(want) || st.infos[0] != want[0] || st.infos[1] != want[1] {
		t.Errorf("writes %+v, want %+v", st.infos, want)
	}
}

// The slots of A2S_INFO go to max_players (not with the info columns), only
// when they change, and not once an RCON status reports them.
func TestProcessorA2SMaxPlayers(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx := context.Background()
	id := p.srv.info.ID
	for range 2 {
		if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 24, Name: "Example Server"}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if want := fmt.Sprintf("%d:24:0", id); len(st.status) != 1 || st.status[0] != want {
		t.Fatalf("status writes %v, want [%s]", st.status, want)
	}
	if len(st.infos) != 1 || st.infos[0].MaxPlayers != 0 || p.maxPlayers() != 24 {
		t.Fatalf("infos %+v, maxPlayers %d", st.infos, p.maxPlayers())
	}
	status, _ := ParseStatus(cssStatus)
	if err := p.Status(ctx, status, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	n := len(st.status)
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 10}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(st.status) != n || p.maxPlayers() != status.MaxPlayers {
		t.Errorf("A2S_INFO slots written over the status: %v, maxPlayers %d", st.status[n:], p.maxPlayers())
	}
}

// Name and slots of a status overwrite what is stored, also a value set by
// hand in the web admin (re-read every minute, Update); unchanged values
// are not written again.
func TestProcessorStatusOverwritesNameAndSlots(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx := context.Background()
	id := p.srv.info.ID
	status, _ := ParseStatus(cssStatus)
	apply := func() {
		t.Helper()
		if err := p.Status(ctx, status, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	host := stats.ServerName(status.Hostname)
	apply()
	if len(st.names) != 1 || st.names[0] != host || p.srv.info.MaxPlayers != status.MaxPlayers {
		t.Fatalf("names %v, slots %d", st.names, p.srv.info.MaxPlayers)
	}
	n := len(st.status)
	apply()
	if len(st.names) != 1 || len(st.status) != n {
		t.Fatalf("unchanged values written again: names %v, status %v", st.names, st.status[n:])
	}
	// Changed by hand in the web admin.
	srv := p.srv.info
	srv.Name, srv.MaxPlayers = "My server", 64
	p.Update(srv)
	apply()
	if want := fmt.Sprintf("%d:%d:0", id, status.MaxPlayers); len(st.names) != 2 || st.names[1] != host || st.status[len(st.status)-1] != want {
		t.Errorf("hand-set values kept: names %v, status %v", st.names, st.status)
	}
}

// Without RCON, A2S_INFO sets the name too; with a status, its hostname wins.
func TestProcessorA2SName(t *testing.T) {
	p, st := newTestProcessor(t)
	ctx := context.Background()
	srv := p.srv.info
	srv.Name = "Typed by hand"
	p.Update(srv)
	for range 2 {
		if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 24, Name: " Example\x07 Server "}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.names) != 1 || st.names[0] != "Example Server" || st.infos[0].Name != "" {
		t.Fatalf("names %v, infos %+v", st.names, st.infos)
	}
	status, _ := ParseStatus(cssStatus)
	if err := p.Status(ctx, status, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.ServerInfo(ctx, stats.ServerInfo{Version: "1", MaxPlayers: 24, Name: "Example Server"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if host := stats.ServerName(status.Hostname); len(st.names) != 2 || st.names[1] != host || p.srv.info.Name != host {
		t.Errorf("A2S_INFO name written over the status: %v", st.names)
	}
}

// The monitor queries A2S_INFO without an RCON password.
func TestMonitorServerInfo(t *testing.T) {
	firstServerInfo = 10 * time.Millisecond
	t.Cleanup(func() { firstServerInfo = 3 * time.Second })
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			_, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(a2sSourceReply(240, 'l', 1, "9540945"), from)
		}
	}()
	srv := stats.Server{ID: 3, AddrPort: netip.MustParseAddrPort(pc.LocalAddr().String())}
	got := make(chan stats.ServerInfo, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitor(ctx, func() stats.Server { return srv }, MonitorConfig{ServerInfo: true, InfoInterval: 20 * time.Millisecond},
			func(Status, time.Time) { t.Error("status without status job") }, func(i stats.ServerInfo) { got <- i },
			func() time.Duration { return time.Second }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case info := <-got:
			if info != (stats.ServerInfo{Version: "9540945", OS: OSLinux, VAC: VACSecure, MaxPlayers: 24, Name: "Example Server"}) {
				t.Fatalf("info %+v", info)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no server info delivered")
		}
	}
	cancel()
	<-done
}
