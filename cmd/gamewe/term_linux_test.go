// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPty opens a pseudo terminal; the test types on master, the question
// reads slave.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo terminal: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("ptsname: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open pts: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

func iflag(t *testing.T, f *os.File) uint32 {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Iflag
}

// On a terminal with icrnl off Enter sends CR, which never ends a line in
// canonical mode: the question switches icrnl on while it waits (so "y"
// and Enter is an answer) and restores the mode afterwards.
func TestAskTerminalWithoutICRNL(t *testing.T) {
	master, slave := openPty(t)
	tio, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Iflag &^= unix.ICRNL // stty -icrnl
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got := make(chan string, 1)
	go func() {
		line, _ := ask(context.Background(), slave, &out, "Migrate now? [y/N] ")
		got <- line
	}()
	time.Sleep(100 * time.Millisecond) // the question waits
	master.Write([]byte("y\r"))        // y and Enter
	select {
	case line := <-got:
		if line != "y" {
			t.Errorf("answer %q, want y", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("y and Enter (CR) do not end the answer")
	}
	if !strings.Contains(out.String(), "stty sane") || !strings.HasSuffix(out.String(), "Migrate now? [y/N] ") {
		t.Errorf("output %q", out.String())
	}
	if iflag(t, slave)&unix.ICRNL != 0 {
		t.Error("the terminal mode was not restored")
	}

	// A terminal where Enter works: no note, nothing changed.
	tio.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	go func() {
		line, _ := ask(context.Background(), slave, &out, "q? ")
		got <- line
	}()
	time.Sleep(100 * time.Millisecond)
	master.Write([]byte("n\r"))
	select {
	case line := <-got:
		if line != "n" || out.String() != "q? " {
			t.Errorf("answer %q, output %q", line, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
}
