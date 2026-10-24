//go:build integration

// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
)

// testDB opens the database of the collector's integration tests
// (GAMEWE_TEST_DB_*); the test works on its own game codes only.
func testDB(t *testing.T) *database.DB {
	t.Helper()
	if os.Getenv("GAMEWE_TEST_DB_WIPE") != "yes" {
		t.Skip("set GAMEWE_TEST_DB_* and GAMEWE_TEST_DB_WIPE=yes to run")
	}
	port, _ := strconv.Atoi(os.Getenv("GAMEWE_TEST_DB_PORT"))
	cfg := config.Defaults().Database
	cfg.Host, cfg.Port, cfg.Type = os.Getenv("GAMEWE_TEST_DB_HOST"), port, config.MySQL
	if os.Getenv("GAMEWE_TEST_DB_TYPE") == config.Postgres {
		cfg.Type = config.Postgres
	}
	cfg.User, cfg.Password, cfg.Name = os.Getenv("GAMEWE_TEST_DB_USER"), os.Getenv("GAMEWE_TEST_DB_PASSWORD"), os.Getenv("GAMEWE_TEST_DB_NAME")
	cfg.TLS.Mode = config.TLSDisable
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// The collector's tests (another package, run at the same time) wipe
	// all tables.
	release, err := database.LockForTest(context.Background(), db, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return db
}

func count(t *testing.T, db *database.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(db.Q(q), args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestAdmin(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	clean := func() {
		for _, q := range []string{
			`DELETE FROM hlstats_Servers WHERE address = '198.51.100.99'`,
			`DELETE FROM hlstats_Teams WHERE game IN ('zz_tpl', 'zz_new')`,
			`DELETE FROM hlstats_Actions WHERE game IN ('zz_tpl', 'zz_new')`,
			`DELETE FROM hlstats_Weapons WHERE game IN ('zz_tpl', 'zz_new')`,
			`DELETE FROM hlstats_Games WHERE code IN ('zz_tpl', 'zz_new')`,
			`DELETE FROM hlstats_Options WHERE keyname = 'gamewe_command_display'`,
		} {
			if _, err := db.Exec(db.Q(q)); err != nil {
				t.Fatal(err)
			}
		}
	}
	clean()
	t.Cleanup(clean)
	for _, q := range []string{
		`INSERT INTO hlstats_Games (code, name, realgame, hidden) VALUES ('zz_tpl', 'Template', 'cstrike', '0')`,
		`INSERT INTO hlstats_Teams (game, code, name, hidden) VALUES ('zz_tpl', 'CT', 'Counter-Terrorists', '0'), ('zz_tpl', 'TERRORIST', 'Terrorists', '0')`,
		`INSERT INTO hlstats_Actions (game, code, reward_player, reward_team, team, description, for_PlayerActions, for_PlayerPlayerActions, for_TeamActions, for_WorldActions)
			VALUES ('zz_tpl', 'Planted_The_Bomb', 7, 0, 'TERRORIST', 'Bomb', '1', '0', '0', '0')`,
		`INSERT INTO hlstats_Weapons (game, code, name, modifier) VALUES ('zz_tpl', 'ak47', 'AK', 1.20)`,
	} {
		if _, err := db.Exec(db.Q(q)); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	a := &Admin{DB: db, Out: &out}

	dry := &Admin{DB: db, Out: &out, DryRun: true}
	if err := dry.AddGame(ctx, "zz_new", "New", "zz_tpl", "", false); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'zz_new'`); n != 0 {
		t.Fatal("dry run added the game")
	}
	for i := 0; i < 2; i++ { // the second run completes nothing more
		if err := a.AddGame(ctx, "zz_new", "New", "zz_tpl", "", true); err != nil {
			t.Fatal(err)
		}
	}
	checks := []struct {
		name      string
		got, want int
	}{
		{"game", count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'zz_new' AND realgame = 'cstrike' AND hidden = '1'`), 1},
		{"teams", count(t, db, `SELECT COUNT(*) FROM hlstats_Teams WHERE game = 'zz_new'`), 2},
		{"action reward", count(t, db, `SELECT reward_player FROM hlstats_Actions WHERE game = 'zz_new' AND code = 'Planted_The_Bomb'`), 7},
		{"weapons", count(t, db, `SELECT COUNT(*) FROM hlstats_Weapons WHERE game = 'zz_new'`), 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
	if err := a.AddGame(ctx, "zz_new", "New", "", "", false); !errors.Is(err, ErrExists) {
		t.Errorf("add without template of an existing game: %v", err)
	}
	if err := a.AddGame(ctx, "Bad Code", "x", "", "", false); err == nil {
		t.Error("bad code accepted")
	}
	if err := a.AddGame(ctx, "zz_x", "x", "", "quake", false); err == nil {
		t.Error("unknown realgame accepted")
	}

	pw, name, game := "s3cret", "Test", "zz_new"
	if err := a.AddServer(ctx, "198.51.100.99:27015", ServerFields{Game: &game, Name: &name, RconPassword: &pw}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddServer(ctx, "198.51.100.99:27015", ServerFields{Game: &game}); !errors.Is(err, ErrExists) {
		t.Errorf("second add: %v", err)
	}
	other := "zz_tpl"
	if err := a.SetServer(ctx, "198.51.100.99:27015", ServerFields{Game: &other}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Servers WHERE address = '198.51.100.99' AND game = 'zz_tpl' AND rcon_password = 's3cret' AND name = 'Test'`); n != 1 {
		t.Error("server set changed the wrong fields")
	}
	if err := a.DeleteGame(ctx, "zz_tpl"); err == nil {
		t.Error("game with a server deleted")
	}
	if err := a.ListServers(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Error("RCON password printed")
	}
	if err := a.RemoveServer(ctx, "198.51.100.99:27015"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteGame(ctx, "zz_new"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Teams WHERE game = 'zz_new'`); n != 0 {
		t.Error("definitions of the deleted game left")
	}

	cssRows := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'css'`)
	for _, x := range []*Admin{a, dry} {
		for _, code := range []string{"css", "CSS"} {
			if err := x.DeleteGame(ctx, code); err == nil || !strings.Contains(err.Error(), "default ranking") {
				t.Errorf("delete %s (dry run %v): %v", code, x.DryRun, err)
			}
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'css'`); n != cssRows {
		t.Errorf("css rows: %d, want %d", n, cssRows)
	}

	gname, grealgame, gorder, ghidden := "Renamed", "CSS", 7, true
	if err := dry.SetGame(ctx, "zz_tpl", GameFields{Name: &gname, Realgame: &grealgame, Order: &gorder, Hidden: &ghidden}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'zz_tpl' AND name = 'Template' AND realgame = 'cstrike' AND display_id = 0 AND hidden = '0'`); n != 1 {
		t.Error("dry run changed the game")
	}
	if err := a.SetGame(ctx, "zz_tpl", GameFields{Name: &gname, Realgame: &grealgame, Order: &gorder, Hidden: &ghidden}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'zz_tpl' AND name = 'Renamed' AND realgame = 'css' AND display_id = 7 AND hidden = '1'`); n != 1 {
		t.Error("game set did not store the fields")
	}
	gorder = 3
	if err := a.SetGame(ctx, "zz_tpl", GameFields{Order: &gorder}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'zz_tpl' AND name = 'Renamed' AND realgame = 'css' AND display_id = 3 AND hidden = '1'`); n != 1 {
		t.Error("game set -order changed other fields")
	}
	bad, empty := "quake", ""
	if err := a.SetGame(ctx, "zz_tpl", GameFields{Realgame: &bad}); err == nil {
		t.Error("unknown realgame accepted")
	}
	if err := a.SetGame(ctx, "zz_tpl", GameFields{Name: &empty}); err == nil {
		t.Error("empty name accepted")
	}
	if err := a.SetGame(ctx, "zz_tpl", GameFields{}); err == nil || err.Error() != "nothing to change" {
		t.Errorf("game set without fields: %v", err)
	}
	if err := a.SetGame(ctx, "zz_none", GameFields{Name: &gname}); !errors.Is(err, ErrNotFound) {
		t.Errorf("game set of an unknown game: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Games WHERE code = 'zz_tpl' AND realgame = 'css'`); n != 1 {
		t.Error("refused game set changed the realgame")
	}

	if err := a.SetOption(ctx, "gamewe_command_display", "both"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetOption(ctx, "gamewe_command_display", "bogus"); err == nil {
		t.Error("invalid value accepted")
	}
	if err := a.SetOption(ctx, "gamewe_typo", "1"); err == nil {
		t.Error("unknown collector key accepted")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM hlstats_Options WHERE keyname = 'gamewe_command_display' AND value = 'both'`); n != 1 {
		t.Error("option not stored")
	}
}
