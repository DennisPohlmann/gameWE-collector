// SPDX-License-Identifier: GPL-3.0-or-later

package localrun

import "os"

// Runtimes of Runtime.
const (
	RuntimeDocker    = "docker"
	RuntimeContainer = "container"
)

// Runtime says where this process runs: RuntimeContainer in Kubernetes or
// a Podman container (and whatever sets the variable container),
// RuntimeDocker in a Docker container (/.dockerenv), "" directly on the
// host. The collector writes it to hlstats_Options gamewe_collector_runtime
// for the web admin, which tells its own the same way.
func Runtime() string { return runtimeOf(os.Getenv, exists) }

func runtimeOf(getenv func(string) string, exists func(string) bool) string {
	switch {
	case getenv("KUBERNETES_SERVICE_HOST") != "" || getenv("container") != "" || exists("/run/.containerenv"):
		return RuntimeContainer
	case exists("/.dockerenv"):
		return RuntimeDocker
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
