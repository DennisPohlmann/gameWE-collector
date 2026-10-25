// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gamewe-collector/internal/database"
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

// A config problem ends gamewe run with exitConfig, which the systemd unit
// does not restart (RestartPreventExitStatus); it happens before anything
// is sent to systemd.
func TestRunConfigErrorExitCode(t *testing.T) {
	dir := t.TempDir()
	bad := dir + "/config.yaml"
	if err := os.WriteFile(bad, []byte("listen: {port: notanumber}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-config", dir + "/missing.yaml"},
		{"-config", bad},
	} {
		var ee exitError
		if err := runCmd(args); !errors.As(err, &ee) || ee.code != exitConfig {
			t.Errorf("run %v: %v, want exit code %d", args, err, exitConfig)
		}
	}
	unit, err := os.ReadFile("../../packaging/gamewe-collector.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nType=notify\n", fmt.Sprintf("\nRestartPreventExitStatus=2 %d\n", exitConfig)} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit without %q", strings.TrimSpace(want))
		}
	}
}

// The tls line of gamewe check carries the connection state and the
// configured mode once, not split over the database and tls lines.
func TestCheckTLS(t *testing.T) {
	for _, c := range []struct {
		info database.Info
		mode string
		want string
	}{
		{database.Info{}, "disable", "off (plaintext connection, mode disable)"},
		{database.Info{TLS: true, TLSVersion: "TLSv1.3", Cipher: "TLS_AES_256_GCM_SHA384"}, "verify-full",
			"on, TLSv1.3 TLS_AES_256_GCM_SHA384 (mode verify-full)"},
		{database.Info{TLS: true, TLSVersion: "TLSv1.2"}, "require", "on, TLSv1.2 (mode require)"},
	} {
		if got := checkTLS(c.info, c.mode); got != c.want {
			t.Errorf("checkTLS(%+v, %q) = %q, want %q", c.info, c.mode, got, c.want)
		}
	}
}
