// Package charts holds the Helm chart of the collector (gamewe-collector);
// the test keeps its version the collector's, which every release sets.
package charts

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Chart.yaml has the version of the collector (VERSION), as version and
// appVersion (the image tag by default).
func TestChartVersion(t *testing.T) {
	version := strings.TrimSpace(read(t, "../VERSION"))
	chart := read(t, "gamewe-collector/Chart.yaml")
	if m := regexp.MustCompile(`(?m)^version: (.+)$`).FindStringSubmatch(chart); m == nil || m[1] != version {
		t.Errorf("Chart.yaml version %v, want %s", m, version)
	}
	if m := regexp.MustCompile(`(?m)^appVersion: "(.+)"$`).FindStringSubmatch(chart); m == nil || m[1] != version {
		t.Errorf("Chart.yaml appVersion %v, want %s", m, version)
	}
}

// Every release sets the chart's version and commits it with VERSION.
func TestReleaseSetsChartVersion(t *testing.T) {
	var rc struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(read(t, "../.releaserc.json")), &rc); err != nil {
		t.Fatal(err)
	}
	var prepare string
	var assets []string
	for _, raw := range rc.Plugins {
		var p []json.RawMessage
		if json.Unmarshal(raw, &p) != nil || len(p) < 2 {
			continue
		}
		var name string
		_ = json.Unmarshal(p[0], &name)
		var opts struct {
			PrepareCmd string   `json:"prepareCmd"`
			Assets     []string `json:"assets"`
		}
		_ = json.Unmarshal(p[1], &opts)
		switch name {
		case "@semantic-release/exec":
			prepare = opts.PrepareCmd
		case "@semantic-release/git":
			assets = opts.Assets
		}
	}
	want := `sed -i -E "s/^version: .*/version: ${nextRelease.version}/; s/^appVersion: .*/appVersion: \"${nextRelease.version}\"/" charts/gamewe-collector/Chart.yaml`
	if !strings.Contains(prepare, want) {
		t.Errorf("prepareCmd %q does not set the chart's version", prepare)
	}
	found := false
	for _, a := range assets {
		found = found || a == "charts/gamewe-collector/Chart.yaml"
	}
	if !found {
		t.Errorf("assets %v without the chart", assets)
	}
}

// The chart keeps the password out of the values and runs one collector.
func TestChartValues(t *testing.T) {
	values := read(t, "gamewe-collector/values.yaml")
	for _, s := range []string{"repository: ghcr.io/dennispohlmann/gamewe-collector", `config: ""`, "hostNetwork: true", "runAsNonRoot: true"} {
		if !strings.Contains(values, s) {
			t.Errorf("values.yaml without %q", s)
		}
	}
	dep := read(t, "gamewe-collector/templates/deployment.yaml")
	for _, s := range []string{"replicas: 1", "type: Recreate", "secretName: {{ include \"gamewe-collector.secretName\" . }}", ".Values.image.tag | default .Chart.AppVersion"} {
		if !strings.Contains(dep, s) {
			t.Errorf("deployment.yaml without %q", s)
		}
	}
}
