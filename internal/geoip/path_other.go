// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package geoip

import "github.com/oschwald/maxminddb-golang"

// DefaultPath is the DB-IP City Lite database, shipped in the packages
// and updated monthly there (the service's state directory).
const DefaultPath = "/var/lib/gamewe/collector/dbip-city-lite.mmdb"

func open(path string) (*maxminddb.Reader, error) { return maxminddb.Open(path) }
