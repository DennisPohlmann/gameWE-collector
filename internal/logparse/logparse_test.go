// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"reflect"
	"strings"
	"testing"
)

const ts = "L 10/22/2026 - 12:00:00: "

func TestParsePacket(t *testing.T) {
	ff := "\xff\xff\xff\xff"
	cases := []struct {
		pkt, secret, want string
		err               error
	}{
		{ff + "RL 10/22/2026 - 12:00:00: World triggered \"Round_Start\"\n\x00", "", ts + `World triggered "Round_Start"`, nil},
		{ff + "S12345L 10/22/2026 - 12:00:00: x\n\x00", "12345", ts + "x", nil},
		{ff + "S999L 10/22/2026 - 12:00:00: x", "12345", "", ErrBadSecret},
		{ff + "RL 10/22/2026 - 12:00:00: x", "12345", "", ErrBadSecret},
		{"L 10/22/2026 - 12:00:00: x", "", ts + "x", nil},
		{ff + "S12345L 10/22/2026 - 12:00:00: x", "", ts + "x", nil},        // no check: the secret is cut off
		{ff + "S1234L 10/22/2026 - 12:00:00: x", "12345", "", ErrBadSecret}, // prefix of the secret
		{ff + "S123456L 10/22/2026 - 12:00:00: x", "12345", "", ErrBadSecret},
		{ff + "SL 10/22/2026 - 12:00:00: x", "12345", "", ErrBadSecret},
		{ff + "log L 10/22/2026 - 12:00:00: x", "12345", "", ErrBadSecret}, // GoldSrc without secret
		{ff + "S12345 no log line", "12345", "", ErrNotLog},
		{ff + "TSource Engine Query", "", "", ErrNotLog},
	}
	for _, c := range cases {
		got, err := ParsePacket([]byte(c.pkt), c.secret)
		if err != c.err || got != c.want {
			t.Errorf("ParsePacket(%q) = %q, %v; want %q, %v", c.pkt, got, err, c.want, c.err)
		}
	}
	line, sec, has, err := SplitPacket([]byte(ff + "S42L 10/22/2026 - 12:00:00: x\n\x00"))
	if err != nil || line != ts+"x" || sec != "42" || !has {
		t.Errorf("SplitPacket = %q %q %v %v", line, sec, has, err)
	}
	if _, sec, has, err = SplitPacket([]byte(ff + "RL 10/22/2026 - 12:00:00: x")); err != nil || has || sec != "" {
		t.Errorf("SplitPacket R = %q %v %v", sec, has, err)
	}
}

func TestParseLine(t *testing.T) {
	alice := Player{"Alice", 2, "STEAM_1:0:123", "CT"}
	bob := Player{"Bob", 3, "STEAM_1:1:456", "TERRORIST"}
	cases := []struct {
		line string
		want Event
	}{
		{ts + `"Alice<2><STEAM_1:0:123><CT>" killed "Bob<3><STEAM_1:1:456><TERRORIST>" with "ak47" (headshot)`,
			Kill{Killer: alice, Victim: bob, Weapon: "ak47", Headshot: true}},
		{`"Alice<2><STEAM_1:0:123><CT>" [100 -20 64] killed "Bob<3><STEAM_1:1:456><TERRORIST>" [0 0 0] with "m4a1"`,
			Kill{Killer: alice, Victim: bob, Weapon: "m4a1", KillerPos: &[3]int{100, -20, 64}, VictimPos: &[3]int{0, 0, 0}}},
		{`"Bob<3><STEAM_1:1:456><TERRORIST>" committed suicide with "world"`, Suicide{Player: bob, Weapon: "world"}},
		{`"Alice<2><STEAM_1:0:123><>" connected, address "192.0.2.1:27005"`,
			Connected{Player: Player{"Alice", 2, "STEAM_1:0:123", ""}, Address: "192.0.2.1:27005"}},
		{`"Alice<2><STEAM_1:0:123><CT>" entered the game`, Entered{Player: alice}},
		{`"Alice<2><STEAM_1:0:123><CT>" disconnected (reason "Disconnect by user.")`,
			Disconnected{Player: alice, Reason: "Disconnect by user."}},
		{`"Alice<2><STEAM_1:0:123><Unassigned>" joined team "CT"`,
			JoinedTeam{Player: Player{"Alice", 2, "STEAM_1:0:123", "Unassigned"}, Team: "CT"}},
		{`"Alice<2><STEAM_1:0:123>" switched from team <Unassigned> to <CT>`, JoinedTeam{Player: alice, Team: "CT"}},
		{`"Alice<2><STEAM_1:0:123><CT>" changed name to "Alicia"`, NameChange{Player: alice, NewName: "Alicia"}},
		{`"Bob<3><STEAM_1:1:456><TERRORIST>" say "!rank"`, Say{Player: bob, Text: "!rank"}},
		{`"Bob<3><STEAM_1:1:456><TERRORIST>" say_team "go b"`, Say{Player: bob, Text: "go b", Team: true}},
		{`"Alice<2><STEAM_1:0:123><CT>" triggered "weaponstats" (weapon "ak47") (shots "30") (hits "10") (kills "2") (headshots "1") (tks "0") (damage "450") (deaths "0")`,
			PlayerTriggered{Player: alice, Trigger: "weaponstats", Props: map[string]string{
				"weapon": "ak47", "shots": "30", "hits": "10", "kills": "2", "headshots": "1", "tks": "0", "damage": "450", "deaths": "0"}}},
		{`"Alice<2><STEAM_1:0:123><CT>" triggered "weaponstats2" (weapon "ak47") (head "1") (chest "4") (stomach "2") (leftarm "1") (rightarm "0") (leftleg "1") (rightleg "1")`,
			PlayerTriggered{Player: alice, Trigger: "weaponstats2", Props: map[string]string{
				"weapon": "ak47", "head": "1", "chest": "4", "stomach": "2", "leftarm": "1", "rightarm": "0", "leftleg": "1", "rightleg": "1"}}},
		{`Team "CT" triggered "CTs_Win" (CT "3") (T "1")`, TeamTriggered{Team: "CT", Trigger: "CTs_Win"}},
		{`"Alice<2><STEAM_1:0:123><CT>" triggered "domination" against "Bob<3><STEAM_1:1:456><TERRORIST>"`,
			PlayerTriggered{Player: alice, Trigger: "domination", Props: map[string]string{}, Victim: &bob}},
		// the SourceMod plugin's kill assist (CS:GO, Insurgency)
		{`"Alice<2><STEAM_1:0:123><CT>" triggered "kill_assist" against "Bob<3><STEAM_1:1:456><TERRORIST>"`,
			PlayerTriggered{Player: alice, Trigger: "kill_assist", Props: map[string]string{}, Victim: &bob}},
		{`Team "CT" scored "3" with "5" players`, TeamScored{Team: "CT", Score: 3, Players: 5}},
		{`World triggered "Round_Start"`, WorldTriggered{Trigger: "Round_Start"}},
		{`World triggered "Round_Win" (winner "Red")`, WorldTriggered{Trigger: "Round_Win", Winner: "Red"}},
		{`Loading map "de_dust2"`, MapStarted{Map: "de_dust2"}},
		{`Started map "cs_office" (CRC "-1234567")`, MapStarted{Map: "cs_office"}},
		{`Banid: "Bob<3><STEAM_1:1:456><>" was kicked and banned "permanently" by "Console"`,
			Banned{Player: Player{"Bob", 3, "STEAM_1:1:456", ""}, Permanent: true}},
		{`Banid: "Bob<3><STEAM_1:1:456><>" was banned "for 5.00 minutes" by "Console"`,
			Banned{Player: Player{"Bob", 3, "STEAM_1:1:456", ""}}},
		{`"Alice<2><STEAM_1:0:123><CT>" requested "playerinfo" (value "17")`, Requested{Player: &alice, Type: "playerinfo", QueryID: 17}},
		{`"Server" requested "top10" (value "3")`, Requested{Type: "top10", QueryID: 3}},
		{`"Alice<2><STEAM_1:0:123><CT>" located on "kill" (position "-1200 345 64") against "Bob<3><STEAM_1:1:456><TERRORIST>" (victim_position "-1100 300 0")`,
			Located{Player: alice, Event: "kill", Pos: [3]int{-1200, 345, 64}, Victim: &bob, VictimAt: [3]int{-1100, 300, 0}}},
		{`"Bob<3><STEAM_1:1:456><TERRORIST>" located on "Planted_The_Bomb" (position "10 20 30")`,
			Located{Player: bob, Event: "Planted_The_Bomb", Pos: [3]int{10, 20, 30}}},
		{`"Alice<2><STEAM_1:0:123><CT>" changed role to "gign"`, RoleChange{Player: alice, Role: "gign"}},
		{`"Alice<2><STEAM_1:0:123><CT>" setup "cl_connectmethod" (value "serverbrowser_internet")`, Setup{Player: alice, Name: "cl_connectmethod", Value: "serverbrowser_internet"}},
		{`"Alice<2><STEAM_1:0:123><CT>" triggered "headshot" (position "1 2 3")`,
			PlayerTriggered{Player: alice, Trigger: "headshot", Props: map[string]string{"position": "1 2 3"}}},
		{`"Laam4<7><[U:1:213129]><Unassigned>" triggered "clantag" (value "Kuningas")`,
			PlayerTriggered{Player: Player{"Laam4", 7, "[U:1:213129]", "Unassigned"}, Trigger: "clantag", Props: map[string]string{"value": "Kuningas"}}},
	}
	for _, c := range cases {
		got, ok := ParseLine(c.line)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseLine(%q)\n got  %#v\n want %#v", c.line, got, c.want)
		}
	}
}

func TestParseLineIgnoresUnknown(t *testing.T) {
	for _, l := range []string{"", "Log file started", `server_cvar: "mp_friendlyfire" "0"`, `Rcon: "rcon 1 "x" status" from "1.2.3.4:5"`} {
		if ev, ok := ParseLine(l); ok {
			t.Errorf("ParseLine(%q) = %#v, want ignored", l, ev)
		}
	}
}

// Chat text must never be parsed as another event.
func TestChatInjection(t *testing.T) {
	line := `"Eve<7><STEAM_1:0:7><CT>" say "x<3><STEAM_1:0:1><CT>" killed "V<4><STEAM_1:0:2><T>" with "ak47""`
	ev, ok := ParseLine(line)
	if _, isSay := ev.(Say); !ok || !isSay {
		t.Fatalf("got %#v, want Say", ev)
	}
}

func TestPlayerKey(t *testing.T) {
	if k := (Player{Name: "Joe", SteamID: "BOT"}).Key(); k != "BOT:Joe" {
		t.Errorf("bot key %q", k)
	}
	if (Player{SteamID: "STEAM_ID_PENDING", UserID: 2}).Trackable() {
		t.Error("pending id must not be tracked")
	}
}

func TestCheckSecret(t *testing.T) {
	for _, ok := range []string{"", "1", "42", "2147483647"} {
		if err := CheckSecret(ok); err != nil {
			t.Errorf("CheckSecret(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"0", "007", "2147483648", "-5", "+5", "12a", "1 2", "1;quit", "\"1\"", "１２", strings.Repeat("9", 65)} {
		if err := CheckSecret(bad); err == nil {
			t.Errorf("CheckSecret(%q) accepted", bad)
		}
	}
}
