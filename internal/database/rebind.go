// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"strconv"
	"strings"
)

// Rebind replaces ? placeholders with PostgreSQL's $1, $2, ... Question
// marks inside single-quoted strings, double-quoted identifiers, backtick
// identifiers and -- or /* */ comments are left alone. A doubled quote
// character inside a literal is handled as an escaped quote.
func Rebind(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(q))
			b.WriteString(q[i:end])
			i = end - 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			end := strings.IndexByte(q[i:], '\n')
			if end < 0 {
				end = len(q) - i
			}
			b.WriteString(q[i : i+end])
			i += end - 1
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				end = len(q) - i
			} else {
				end += 4
			}
			b.WriteString(q[i : i+end])
			i += end - 1
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
