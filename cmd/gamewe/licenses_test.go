// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every module compiled into gamewe is listed in THIRD-PARTY-LICENSES.md
// with its version, and its license text is in licenses/.
func TestThirdPartyLicenses(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err) // no go tool (e.g. a test binary run alone)
	}
	doc, err := os.ReadFile("../../THIRD-PARTY-LICENSES.md")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		path, ver, _ := strings.Cut(l, " ")
		if !strings.Contains(string(doc), "| `"+path+"` | "+ver+" |") {
			t.Errorf("THIRD-PARTY-LICENSES.md: no row for %s %s", path, ver)
		}
		dir := filepath.Join("../../licenses", strings.ReplaceAll(path, "/", "_"))
		if files, err := os.ReadDir(dir); err != nil || len(files) == 0 {
			t.Errorf("no license text for %s in %s", path, dir)
		}
	}
	if len(seen) < 5 {
		t.Errorf("only %d modules found", len(seen))
	}
}
