// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWarnRoot(t *testing.T) {
	defer func(f func() int) { geteuid = f }(geteuid)
	var buf bytes.Buffer
	geteuid = func() int { return 1000 }
	if warnRoot(&buf) || buf.Len() != 0 {
		t.Errorf("warning as uid 1000: %q", buf.String())
	}
	geteuid = func() int { return 0 }
	if !warnRoot(&buf) || !strings.Contains(buf.String(), "running as ROOT") || strings.Count(buf.String(), "\n") < 5 {
		t.Errorf("no banner as root: %q", buf.String())
	}
	if l := rootLines(); len(l) != 3 || !strings.HasPrefix(l[0], "WARNING:") {
		t.Errorf("journal lines %q", l)
	}
}
