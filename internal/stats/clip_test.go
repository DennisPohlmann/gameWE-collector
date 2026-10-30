// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"Müller", 3, "Mül"},
		{"日本語テキスト", 3, "日本語"},
		// A chat line in Windows-1251: no UTF-8, refused
		// by MariaDB with error 1366 before.
		{"\xc3\xee \xe2 \xef\xf0\xe5\xee\xea\xf2 ok", 4096, "� � � ok"},
		{"ok\xffok", 3, "ok�"},
	} {
		got := clip(c.in, c.n)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("clip(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
