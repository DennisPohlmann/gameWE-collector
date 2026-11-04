// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// A new table with a player id must be added to the erasure.
func TestDeletePlayerCoversTheSchema(t *testing.T) {
	schema, err := os.ReadFile("../schema/mysql/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string][]string{}
	for _, r := range playerRows {
		known[r.table] = r.cols
	}
	for _, r := range playerRefs {
		known[r.table] = []string{r.col}
	}
	column := regexp.MustCompile("(?m)^  `(playerId|player_id|killerId|victimId|winner_id|d_winner_id)` ")
	seen := 0
	for _, m := range regexp.MustCompile("(?s)CREATE TABLE `(\\w+)` \\((.*?)\\n\\) ENGINE").FindAllStringSubmatch(string(schema), -1) {
		for _, c := range column.FindAllStringSubmatch(m[2], -1) {
			seen++
			if !slices.Contains(known[m[1]], c[1]) {
				t.Errorf("%s.%s is not in playerRows or playerRefs", m[1], c[1])
			}
		}
	}
	if seen < 30 {
		t.Fatalf("found only %d player columns in the schema", seen)
	}
}
