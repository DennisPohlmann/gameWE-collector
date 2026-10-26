// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package main

import (
	"io"
	"os"
)

// lineTerminal changes nothing outside Linux (the collector is built for
// Linux only; this keeps go vet and the tests working elsewhere).
func lineTerminal(*os.File, io.Writer) func() { return func() {} }

// isTerminal: a character device counts as a terminal outside Linux.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
