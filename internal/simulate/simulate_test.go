// SPDX-License-Identifier: GPL-3.0-or-later

package simulate

import (
	"testing"

	"gamewe-collector/internal/logparse"
)

// Every line of the built-in sample must be understood by the parser.
func TestSampleParses(t *testing.T) {
	lines, err := Lines("")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) < 30 {
		t.Fatalf("sample has only %d lines", len(lines))
	}
	for _, l := range lines {
		if _, ok := logparse.ParseLine(l); !ok {
			t.Errorf("not parsed: %s", l)
		}
	}
}
