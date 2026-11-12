// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console (not NUL or a pipe, which a
// scheduled task or CI gives as stdin).
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// lineTerminal changes nothing: the console hands a line over on Enter.
func lineTerminal(*os.File, io.Writer) func() { return func() {} }
