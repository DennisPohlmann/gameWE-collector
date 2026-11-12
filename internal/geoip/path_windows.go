// SPDX-License-Identifier: GPL-3.0-or-later

package geoip

import (
	"os"
	"path/filepath"

	"github.com/oschwald/maxminddb-golang"

	"gamewe-collector/internal/config"
)

// DefaultPath is the DB-IP City Lite database in the data directory, shipped
// in the zip and updated monthly there.
var DefaultPath = filepath.Join(config.DataDir(), "dbip-city-lite.mmdb")

// open reads the file into memory: a mapped file cannot be replaced on
// Windows, so the monthly update could not rename the new one over it.
func open(path string) (*maxminddb.Reader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return maxminddb.FromBytes(b)
}
