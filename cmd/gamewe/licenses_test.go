// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gamewe-collector/internal/licenseinfo"
)

// Every Go module compiled into gamewe or used by the tests is listed in
// THIRD-PARTY-LICENSES.md with its version and SPDX license, and licenses/
// holds exactly its license texts from the module cache plus modules.json
// (`make licenses` writes them).
func TestThirdPartyLicenses(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("no go tool: %v", err) // e.g. a test binary run alone
	}
	res, err := licenseinfo.Collect("../..")
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Diff("../../licenses"); len(d) > 0 {
		t.Errorf("licenses/ is stale (run make licenses): %v", d)
	}
	doc, err := os.ReadFile("../../THIRD-PARTY-LICENSES.md")
	if err != nil {
		t.Fatal(err)
	}
	binary := 0
	for _, m := range res.Modules {
		if m.License == "UNKNOWN" {
			t.Errorf("%s %s: license text not recognised", m.Path, m.Version)
		}
		if row := "| `" + m.Path + "` | " + m.Version + " | " + m.License + " |"; !strings.Contains(string(doc), row) {
			t.Errorf("THIRD-PARTY-LICENSES.md: no row %q", row)
		}
		if m.Scope == "binary" {
			binary++
		}
	}
	if binary < 5 {
		t.Errorf("only %d modules in the binary", binary)
	}
}

func TestSPDX(t *testing.T) {
	mit := []byte("Permission is hereby granted, free of charge, to any person")
	apache := []byte("Apache License\n   Version 2.0, January 2004")
	bsd3 := []byte("Redistribution and use in source and binary forms ... Neither the name of Google")
	for _, c := range []struct {
		texts map[string][]byte
		want  string
	}{
		{map[string][]byte{"LICENSE": mit}, "MIT"},
		{map[string][]byte{"LICENSE": bsd3}, "BSD-3-Clause"},
		{map[string][]byte{"LICENSE": append(append([]byte{}, mit...), apache...), "NOTICE": []byte("x")}, "MIT AND Apache-2.0"},
		{map[string][]byte{"LICENSE-MIT": mit, "LICENSE-APACHE": apache}, "Apache-2.0 OR MIT"},
		{map[string][]byte{"LICENSE": []byte("all rights reserved")}, "UNKNOWN"},
		{map[string][]byte{"NOTICE": []byte("x")}, "UNKNOWN"},
	} {
		if got := licenseinfo.SPDX(c.texts); got != c.want {
			t.Errorf("SPDX(%v) = %q, want %q", c.texts, got, c.want)
		}
	}
}
