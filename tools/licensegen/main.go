// SPDX-License-Identifier: GPL-3.0-or-later

// Command licensegen copies the license texts of every Go module gameWE
// uses (compiled into gamewe or only in the tests) from the module cache
// to licenses/<module path with _>/ and writes licenses/modules.json
// (path, version, scope, SPDX license, files), see internal/licenseinfo.
// Run it from the repository root after a dependency change (`make
// licenses`), then update THIRD-PARTY-LICENSES.md; with -check it only
// reports what is stale and exits 1.
package main

import (
	"flag"
	"fmt"
	"os"

	"gamewe-collector/internal/licenseinfo"
)

func main() {
	check := flag.Bool("check", false, "only report stale files, exit 1 when there are any")
	flag.Parse()
	res, err := licenseinfo.Collect(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "licensegen:", err)
		os.Exit(1)
	}
	if *check {
		if d := res.Diff("licenses"); len(d) > 0 {
			fmt.Fprintln(os.Stderr, "licensegen: stale:", d)
			os.Exit(1)
		}
		return
	}
	if err := res.Write("licenses"); err != nil {
		fmt.Fprintln(os.Stderr, "licensegen:", err)
		os.Exit(1)
	}
	for _, m := range res.Modules {
		if m.License == "UNKNOWN" {
			fmt.Fprintf(os.Stderr, "licensegen: %s %s: license not recognised, check %v by hand\n", m.Path, m.Version, m.Files)
		}
	}
	fmt.Printf("licenses/modules.json: %d modules\n", len(res.Modules))
}
