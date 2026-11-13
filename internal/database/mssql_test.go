// SPDX-License-Identifier: GPL-3.0-or-later

package database

import "testing"

func TestToMSSQL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"SELECT a FROM t WHERE b = ? AND c = '?'", "SELECT a FROM t WHERE b = @p1 AND c = '?'"},
		{"SELECT a FROM t ORDER BY a LIMIT 10", "SELECT a FROM t ORDER BY a OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT a FROM t WHERE x = ? LIMIT 1", "SELECT a FROM t WHERE x = @p1 ORDER BY (SELECT NULL) OFFSET 0 ROWS FETCH NEXT 1 ROWS ONLY"},
		{"SELECT a FROM t ORDER BY a LIMIT ? OFFSET ?", "SELECT a FROM t ORDER BY a OFFSET @p2 ROWS FETCH NEXT @p1 ROWS ONLY"},
		{"SELECT a FROM t ORDER BY a LIMIT 5, 10", "SELECT a FROM t ORDER BY a OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT (SELECT b FROM u WHERE u.a = t.a LIMIT 1) FROM t ORDER BY a",
			"SELECT (SELECT b FROM u WHERE u.a = t.a ORDER BY (SELECT NULL) OFFSET 0 ROWS FETCH NEXT 1 ROWS ONLY) FROM t ORDER BY a"},
		{"SELECT `key` FROM t WHERE ok = TRUE", "SELECT [key] FROM t WHERE ok = 1"},
		{"SELECT IFNULL(a, 0), LENGTH(b), ROUND(c), ROUND(d, 2) FROM t", "SELECT COALESCE(a, 0), LEN(b), ROUND(c, 0), ROUND(d, 2) FROM t"},
		{"SELECT AVG(IFNULL(x, 0)), ROUND(AVG(y)) FROM t", "SELECT AVG(CAST(COALESCE(x, 0) AS float)), ROUND(AVG(CAST(y AS float)), 0) FROM t"},
		{"SELECT 'LIMIT 1' -- LIMIT 2\nFROM t", "SELECT 'LIMIT 1' -- LIMIT 2\nFROM t"},
	} {
		if got := ToMSSQL(c.in); got != c.want {
			t.Errorf("ToMSSQL(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// The keys come from the compiled schema: every unique key whose columns
// the insert gives, joined with OR; none of them: the insert as it is.
func TestMSSQLIgnoreUpsert(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"INSERT INTO hlstats_PlayerNames (playerId, name, lastuse, numuses) VALUES (?, ?, ?, 0)",
			"INSERT INTO hlstats_PlayerNames (playerId, name, lastuse, numuses) SELECT * FROM (VALUES (?, ?, ?, 0)) AS v (playerId, name, lastuse, numuses)" +
				" WHERE NOT EXISTS (SELECT 1 FROM hlstats_PlayerNames gw_t WHERE (gw_t.playerId = v.playerId AND gw_t.name = v.name))"},
		{"INSERT INTO hlstats_Players_Locations (id, lat, lng) VALUES (?, ?, ?)",
			"INSERT INTO hlstats_Players_Locations (id, lat, lng) SELECT * FROM (VALUES (?, ?, ?)) AS v (id, lat, lng)" +
				" WHERE NOT EXISTS (SELECT 1 FROM hlstats_Players_Locations gw_t WHERE (gw_t.id = v.id) OR (gw_t.lat = v.lat AND gw_t.lng = v.lng))"},
		{"INSERT INTO hlstats_Players_Locations (city) VALUES (?)", "INSERT INTO hlstats_Players_Locations (city) VALUES (?)"},
	} {
		if got := MSSQLIgnore(c.in); got != c.want {
			t.Errorf("MSSQLIgnore(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	got := MSSQLUpsert("INSERT INTO hlstats_Options (keyname, value) VALUES (?, ?)", "keyname", []string{"value = v.value"})
	want := "MERGE INTO hlstats_Options USING (VALUES (?, ?)) AS v (keyname, value) ON hlstats_Options.keyname = v.keyname" +
		" WHEN MATCHED THEN UPDATE SET value = v.value WHEN NOT MATCHED THEN INSERT (keyname, value) VALUES (v.keyname, v.value);"
	if got != want {
		t.Errorf("MSSQLUpsert\n got %q\nwant %q", got, want)
	}
}
