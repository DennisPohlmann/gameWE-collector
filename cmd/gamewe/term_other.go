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
