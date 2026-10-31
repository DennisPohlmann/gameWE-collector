package stats

import (
	"net/netip"
	"testing"
)

// publicaddress: an IP address (with or without port) is where A2S_INFO and
// RCON go; anything else leaves them at address:port.
func TestPublicAddr(t *testing.T) {
	game := netip.MustParseAddrPort("203.0.113.136:26015")
	for _, c := range []struct{ in, want string }{
		{"203.0.113.142", "203.0.113.142:26015"},
		{" 203.0.113.142:27015 ", "203.0.113.142:27015"},
		{"203.0.113.136:26015", "203.0.113.136:26015"},
		{"::ffff:203.0.113.142", "203.0.113.142:26015"},
		{"2001:db8::1", "[2001:db8::1]:26015"},
		{"[2001:db8::1]", "[2001:db8::1]:26015"},
		{"[2001:db8::1]:27016", "[2001:db8::1]:27016"},
		// Not used: host names (never resolved), empty, unspecified, port 0,
		// zones and garbage.
		{"", ""},
		{"   ", ""},
		{"dust2.example.org", ""},
		{"dust2.example.org:27015", ""},
		{"0.0.0.0", ""},
		{"0.0.0.0:27015", ""},
		{"::", ""},
		{"203.0.113.142:0", ""},
		{"203.0.113.142:99999", ""},
		{"fe80::1%eth0", ""},
		{"203.0.113.142:27015 x", ""},
		{"http://203.0.113.142:27015", ""},
	} {
		got := PublicAddr(c.in, game)
		if (c.want == "" && got.IsValid()) || (c.want != "" && got.String() != c.want) {
			t.Errorf("PublicAddr(%q) = %v, want %q", c.in, got, c.want)
		}
	}
	if got := PublicAddr("203.0.113.142", netip.AddrPort{}); got.IsValid() {
		t.Errorf("without a usable address: %v", got)
	}
}

func TestQueryAddr(t *testing.T) {
	game := netip.MustParseAddrPort("203.0.113.136:26015")
	if got := (Server{AddrPort: game}).QueryAddr(); got != game {
		t.Errorf("without public address: %v", got)
	}
	pub := netip.MustParseAddrPort("203.0.113.142:26015")
	if got := (Server{AddrPort: game, Public: pub}).QueryAddr(); got != pub {
		t.Errorf("with public address: %v", got)
	}
}
