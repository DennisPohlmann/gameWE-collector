// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

func TestChangeFlags(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		dry, force bool
		bad        bool
	}{
		{nil, false, false, false},
		{[]string{"--force"}, false, true, false},
		{[]string{"-force"}, false, true, false},
		{[]string{"-f"}, false, true, false},
		{[]string{"-yes"}, false, true, false}, // older name, kept for scripts
		{[]string{"--yes"}, false, true, false},
		{[]string{"-y"}, false, true, false},
		{[]string{"--dry-run"}, true, false, false},
		{[]string{"-dry-run"}, true, false, false},
		{[]string{"-n"}, true, false, false},
		{[]string{"-n", "-f"}, true, true, true},
	} {
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var c changeFlags
		c.add(fs, "show")
		if err := fs.Parse(tc.args); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if c.dryRun != tc.dry || c.force != tc.force || (c.check() != nil) != tc.bad {
			t.Errorf("%v: %+v check %v", tc.args, c, c.check())
		}
	}
}

func TestApprove(t *testing.T) {
	ctx := context.Background()
	var out strings.Builder
	if err := approve(ctx, changeFlags{force: true}, strings.NewReader(""), &out, false, "Go?"); err != nil || out.Len() != 0 {
		t.Errorf("--force: %v, asked %q", err, out.String())
	}
	if err := approve(ctx, changeFlags{}, strings.NewReader("y\n"), &out, false, "Go?"); !errors.Is(err, errNoTTYYes) || out.Len() != 0 {
		t.Errorf("no terminal: %v, asked %q", err, out.String())
	}
	if !strings.Contains(errNoTTYYes.Error(), "--force") || !strings.Contains(errNoTTYYes.Error(), "--dry-run") {
		t.Errorf("hint: %v", errNoTTYYes)
	}
	for in, ok := range map[string]bool{"y\n": true, "yes\n": true, "J\n": true, "ja\n": true, "n\n": false, "\n": false, "": false, "maybe\n": false} {
		out.Reset()
		err := approve(ctx, changeFlags{}, strings.NewReader(in), &out, true, "Move 3 players now?")
		if (err == nil) != ok || (!ok && err != error(errNotConfirmed)) {
			t.Errorf("answer %q: %v", in, err)
		}
		if !strings.Contains(out.String(), "Move 3 players now? [y/N] ") {
			t.Errorf("question %q", out.String())
		}
	}
	if interactive(strings.NewReader("")) != true {
		t.Error("a reader is a terminal in tests")
	}
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if interactive(r) {
		t.Error("a pipe is no terminal")
	}
}
