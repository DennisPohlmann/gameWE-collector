// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// changeFlags are the flags of every command that changes something:
// --dry-run (-n) shows what it would do and changes nothing, --force (-f)
// runs it without asking. -yes and -y are the older names of --force and
// keep working (scripts, cron). Without either the command shows what it
// will do and asks.
type changeFlags struct {
	dryRun, force bool
}

// add registers the flags on fs; dryHelp says what --dry-run shows.
func (c *changeFlags) add(fs *flag.FlagSet, dryHelp string) {
	fs.BoolVar(&c.dryRun, "dry-run", false, dryHelp+"; changes nothing")
	fs.BoolVar(&c.dryRun, "n", false, "short for --dry-run")
	fs.BoolVar(&c.force, "force", false, "do not ask before changing anything (scripts, cron)")
	fs.BoolVar(&c.force, "f", false, "short for --force")
	fs.BoolVar(&c.force, "yes", false, "older name of --force")
	fs.BoolVar(&c.force, "y", false, "older name of --force")
}

// check refuses --force together with --dry-run.
func (c changeFlags) check() error {
	if c.force && c.dryRun {
		return exitError{2, "--force and --dry-run cannot be used together"}
	}
	return nil
}

// interactive reports whether in is a terminal to ask on (a reader that is
// no file, as in the tests, counts as one).
func interactive(in io.Reader) bool {
	if f, ok := in.(*os.File); ok {
		fi, err := f.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
	return true
}

// errNoTTYYes is returned when a command that changes something cannot ask.
var errNoTTYYes = errors.New("no interactive confirmation possible (stdin is not a terminal), nothing changed: use --force (-f) to run it without asking, or --dry-run (-n) to see what it would do")

// errNotConfirmed: the question was answered with no.
var errNotConfirmed = exitError{1, "not confirmed, nothing changed"}

// approve asks question (e.g. "Move 12 players now?") with [y/N] unless
// --force: y, yes, j or ja go on, anything else (also Enter, end of input,
// Ctrl-C) is errNotConfirmed; without a terminal it is errNoTTYYes.
func approve(ctx context.Context, c changeFlags, in io.Reader, out io.Writer, interactive bool, question string) error {
	if c.force {
		return nil
	}
	if !interactive {
		return errNoTTYYes
	}
	line, err := ask(ctx, in, promptOut(out), question+" [y/N] ")
	if ctx.Err() != nil || (err != nil && line == "") {
		fmt.Fprintln(promptOut(out))
		return errNotConfirmed
	}
	if !saysYes(line) {
		return errNotConfirmed
	}
	return nil
}
