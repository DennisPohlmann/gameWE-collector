// SPDX-License-Identifier: GPL-3.0-or-later

// Package version holds the build version, set at link time via -ldflags.
package version

// Version is overwritten by the Makefile:
// -X gamewe-collector/internal/version.Version=<version>
var Version = "dev"
