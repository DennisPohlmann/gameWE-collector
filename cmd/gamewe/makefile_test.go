// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The Makefile runs the binary without a terminal (make package, make
// geoip in CI): every command of it that changes something needs --force,
// or it stops without changing anything (v0.15.0 failed so).
func TestMakefileForce(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	reads := map[string]bool{"version": true, "check": true, "status": true, "config": true, "help": true}
	run := regexp.MustCompile(`^\t@?\$\(BIN\)\s+(\S+)(.*)`) // a recipe line that runs the binary
	found := 0
	for _, line := range strings.Split(string(data), "\n") {
		m := run.FindStringSubmatch(line)
		if m == nil || reads[m[1]] {
			continue
		}
		found++
		if !regexp.MustCompile(`(^|\s)(--force|-f|-yes)(\s|$)`).MatchString(m[2]) {
			t.Errorf("Makefile runs %q without --force: %s", m[1], strings.TrimSpace(line))
		}
	}
	if found == 0 {
		t.Error("no $(BIN) command found in the Makefile")
	}
}
