// SPDX-License-Identifier: GPL-3.0-or-later

package localrun

import "testing"

func TestRuntime(t *testing.T) {
	for _, c := range []struct {
		env   map[string]string
		files []string
		want  string
	}{
		{nil, nil, ""},
		{nil, []string{"/.dockerenv"}, RuntimeDocker},
		{nil, []string{"/run/.containerenv"}, RuntimeContainer},
		{map[string]string{"container": "podman"}, nil, RuntimeContainer},
		// Kubernetes wins over the /.dockerenv of a Docker runtime below it.
		{map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1"}, []string{"/.dockerenv"}, RuntimeContainer},
	} {
		got := runtimeOf(func(k string) string { return c.env[k] }, func(p string) bool {
			for _, f := range c.files {
				if f == p {
					return true
				}
			}
			return false
		})
		if got != c.want {
			t.Errorf("%v %v: %q, want %q", c.env, c.files, got, c.want)
		}
	}
}
