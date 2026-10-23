// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gamewe-collector/internal/version"
)

func TestIsVersion(t *testing.T) {
	for _, a := range []string{"version", "-V", "-version", "--version"} {
		if !isVersion(a) {
			t.Errorf("%q is not the version", a)
		}
	}
	for _, a := range []string{"run", "-v", "--V", "versions", ""} {
		if isVersion(a) {
			t.Errorf("%q is the version", a)
		}
	}
	if got := versionLine(); got != "gamewe "+version.Version {
		t.Errorf("version line %q", got)
	}
}

// The binary prints the version and exits 0 for every alias, also with
// arguments after it, without reading a config (the default path does not
// exist here).
func TestVersionFlags(t *testing.T) {
	if os.Getenv("GAMEWE_VERSION_CHILD") == "1" {
		for i, a := range os.Args {
			if a == "--" {
				os.Args = append([]string{"gamewe"}, os.Args[i+1:]...)
				break
			}
		}
		main()
		return
	}
	for _, args := range [][]string{{"-V"}, {"--version"}, {"-version"}, {"version"}, {"-V", "run", "-config", "/nonexistent.yaml"}} {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestVersionFlags$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GAMEWE_VERSION_CHILD=1")
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		if got := strings.TrimSpace(string(out)); !strings.HasPrefix(got, "gamewe "+version.Version) {
			t.Errorf("%v: output %q", args, got)
		}
	}
}
