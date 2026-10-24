// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The read-only web user must be able to read every table of the schema
// except hlstats_Users, hlstats_Events_Rcon and hlstats_Events_Chat, and never
// hlstats_Servers.rcon_password, log_secret, log_secret_changed or
// hlstats_Events_Connects.ipAddress.
func TestWebUserGrants(t *testing.T) {
	schema, err := os.ReadFile("../../internal/schema/mysql/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	tables := regexp.MustCompile("CREATE TABLE `([^`]+)`").FindAllStringSubmatch(string(schema), -1)
	if len(tables) < 50 {
		t.Fatalf("found only %d tables in the schema", len(tables))
	}
	hidden := map[string]bool{"hlstats_Users": true, "hlstats_Events_Rcon": true, "hlstats_Events_Chat": true}
	hiddenCol := map[string][]string{"hlstats_Servers": {"rcon_password", "log_secret", "log_secret_changed"}, "hlstats_Events_Connects": {"ipAddress"}}
	data, err := os.ReadFile("../../internal/schema/mysql/grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	grants := map[string]string{} // table -> column list ("" = whole table)
	for _, m := range regexp.MustCompile("GRANT SELECT (?:\\(([^)]*)\\) )?ON `([^`]+)` TO 'gamewe_web'@").FindAllStringSubmatch(string(data), -1) {
		grants[m[2]] = m[1]
	}
	// gamewe_web only reads.
	for _, m := range regexp.MustCompile("GRANT ([A-Z, ]+) (?:\\([^)]*\\) )?ON `([^`]+)` TO 'gamewe_web'@").FindAllStringSubmatch(string(data), -1) {
		if m[1] != "SELECT" {
			t.Errorf("gamewe_web gets %s on %s", m[1], m[2])
		}
	}
	// The web admin queues ranking resets (hlstats_gamewe_jobs); gamewe_web
	// never writes there.
	if !strings.Contains(string(data), "GRANT INSERT ON `hlstats_gamewe_jobs` TO 'gamewe_web_admin'@'%';") {
		t.Error("grants.sql: gamewe_web_admin cannot queue jobs")
	}
	// A user gamewe_admin (the web README's manual setup) gets the same,
	// if it exists (MariaDB and the generated PostgreSQL file).
	pg, err := os.ReadFile("../../internal/schema/postgres/grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string][]byte{"grants.sql": data, "postgres grants.sql": pg} {
		plain := strings.ReplaceAll(string(f), "`", "")
		if !strings.Contains(plain, "gamewe_admin") || !strings.Contains(plain, "SELECT, INSERT ON hlstats_gamewe_jobs TO") {
			t.Errorf("%s: no conditional grant for gamewe_admin on hlstats_gamewe_jobs", name)
		}
		if regexp.MustCompile(`GRANT [A-Z, ]*INSERT[A-Z, ]* ON hlstats_gamewe_jobs TO '?gamewe_web'?[@;]`).MatchString(plain) {
			t.Errorf("%s grants gamewe_web INSERT", name)
		}
	}
	for _, tb := range tables {
		cols, ok := grants[tb[1]]
		hide, restricted := hiddenCol[tb[1]]
		switch {
		case hidden[tb[1]]:
			if ok {
				t.Errorf("gamewe_web can read %s", tb[1])
			}
		case !ok:
			t.Errorf("no SELECT for gamewe_web on %s", tb[1])
		case restricted && cols == "":
			t.Errorf("gamewe_web can read all of %s", tb[1])
		case restricted:
			granted := map[string]bool{}
			for _, c := range strings.Split(cols, ",") {
				granted[strings.Trim(strings.TrimSpace(c), "`")] = true
			}
			for _, col := range hide {
				if granted[col] {
					t.Errorf("gamewe_web can read %s.%s", tb[1], col)
				}
			}
		case !restricted && cols != "":
			t.Errorf("gamewe_web has only column rights on %s", tb[1])
		}
	}
	if regexp.MustCompile("ON `?[a-z_]*`?\\.\\* TO 'gamewe_web'").Match(data) {
		t.Error("grants.sql grants gamewe_web a whole database")
	}
	if !strings.Contains(string(data), "REVOKE SELECT ON `hlstats_Events_Chat` FROM 'gamewe_web'") {
		t.Error("grants.sql does not revoke an old SELECT on hlstats_Events_Chat")
	}
}
