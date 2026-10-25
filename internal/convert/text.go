// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Mojibake modes (-mojibake): what happens to text that is UTF-8 stored a
// second time as latin1 (MÃ¼ller instead of Müller).
const (
	MojibakeAuto   = "auto"   // repair it in the text of latin1 tables
	MojibakeRepair = "repair" // repair it in the text of every table (UTF-8 written into utf8 tables over a latin1 connection)
	MojibakeKeep   = "keep"   // take the text as the server returns it
)

// ParseMojibake checks a -mojibake value.
func ParseMojibake(s string) (string, error) {
	switch s {
	case MojibakeAuto, MojibakeRepair, MojibakeKeep:
		return s, nil
	}
	return "", fmt.Errorf("-mojibake %q: want auto, repair or keep", s)
}

// cp1252 holds the characters of the bytes 0x80-0x9f in MariaDB's latin1
// (Windows-1252; the five bytes Windows-1252 leaves free map to U+0081,
// U+008D, U+008F, U+0090, U+009D). The bytes 0xa0-0xff are U+00A0-U+00FF.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8d, 'Ž', 0x8f,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9d, 'ž', 'Ÿ',
}

// latin1Byte is the byte of r in MariaDB's latin1, false if it has none.
func latin1Byte(r rune) (byte, bool) {
	switch {
	case r < 0x80 || (r >= 0xa0 && r <= 0xff):
		return byte(r), true
	case r <= 0xffff:
		for i, c := range cp1252 {
			if c == r {
				return byte(0x80 + i), true
			}
		}
	}
	return 0, false
}

// Repair undoes one round of double encoding: s, written back as latin1
// bytes, is valid UTF-8 with at least one multibyte character (MÃ¼ller ->
// Müller). Text that is not double encoded (Müller, plain ASCII, a
// character latin1 does not have) is returned as it is.
func Repair(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		c, ok := latin1Byte(r)
		if !ok {
			return s
		}
		b = append(b, c)
	}
	if !utf8.Valid(b) || utf8.RuneCount(b) == len(b) {
		return s
	}
	return string(b)
}

// LooksDoubleEncoded reports whether Repair would change s.
func LooksDoubleEncoded(s string) bool { return Repair(s) != s }

// isLatin1 reports whether a table collation (latin1_swedish_ci, ...) is
// one of latin1.
func isLatin1(collation string) bool { return strings.HasPrefix(strings.ToLower(collation), "latin1") }
