// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
)

// DefaultPath is used when no -config flag is given:
// %ProgramData%\gameWE\collector\config.yaml.
var DefaultPath = filepath.Join(Dir(), "config.yaml")

// Dir is %ProgramData%\gameWE\collector (normally C:\ProgramData\...): the
// config; its subdirectory data (DataDir) holds the GeoIP database and the
// service log.
func Dir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "gameWE", "collector")
}

// DataDir is the directory the service may write: Dir()\data.
func DataDir() string { return filepath.Join(Dir(), "data") }
