// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.log")
	os.WriteFile(path, []byte("old\n"), 0o640)
	l, err := openRotatingLog(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"12345\n", "abcdef\n", "x\n"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	// "old\n12345\n" is 10 bytes; "abcdef\n" starts a new file.
	if b, _ := os.ReadFile(path + ".1"); string(b) != "old\n12345\n" {
		t.Errorf(".1 = %q", b)
	}
	if b, _ := os.ReadFile(path); string(b) != "abcdef\nx\n" {
		t.Errorf("log = %q", b)
	}
}
