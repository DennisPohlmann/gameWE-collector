// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// geteuid is os.Geteuid (a variable for tests).
var geteuid = os.Geteuid

// rootBanner is printed at start when the collector runs as root.
const rootBanner = `################################################################################
#                                                                              #
#   WARNING: gameWE collector is running as ROOT.                              #
#   This is not recommended: run it as the gamewe user (systemd unit, or      #
#   sudo -u gamewe gamewe ...). It keeps running normally.                     #
#                                                                              #
################################################################################
`

// warnRoot writes the banner to w if the process runs as root and reports
// whether it did.
func warnRoot(w io.Writer) bool {
	if geteuid() != 0 {
		return false
	}
	fmt.Fprint(w, rootBanner)
	return true
}

// rootLines are the banner lines without the frame, for the journal.
func rootLines() []string {
	var out []string
	for _, l := range strings.Split(rootBanner, "\n") {
		if t := strings.Trim(l, "# "); t != "" {
			out = append(out, t)
		}
	}
	return out
}
