// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// lineTerminal makes Enter end the line on the terminal f while a question
// waits. With icrnl off (stty -icrnl, left so by another program) or igncr
// on, Enter sends CR: the terminal echoes ^M and, in canonical mode, never
// hands the line over, so the question would wait for ever. It switches
// icrnl on (telling note) and returns the function that restores the mode.
func lineTerminal(f *os.File, note io.Writer) func() {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || t.Lflag&unix.ICANON == 0 || (t.Iflag&unix.ICRNL != 0 && t.Iflag&unix.IGNCR == 0) {
		return func() {} // no terminal, raw mode (readAnswer takes CR), or Enter works
	}
	old := *t
	t.Iflag = t.Iflag&^unix.IGNCR | unix.ICRNL
	if unix.IoctlSetTermios(fd, unix.TCSETS, t) != nil {
		return func() {}
	}
	fmt.Fprintln(note, "note: this terminal sends CR for Enter (stty -icrnl), switched on for the question; `stty sane` repairs it for good")
	return func() { unix.IoctlSetTermios(fd, unix.TCSETS, &old) }
}
