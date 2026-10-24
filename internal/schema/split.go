// SPDX-License-Identifier: GPL-3.0-or-later

package schema

import (
	"strings"
)

// Statement is one SQL statement of a script.
type Statement struct {
	SQL string
	// GExec: psql's \gexec ended the statement; every value of its result is
	// a statement to run.
	GExec bool
}

// Split cuts a script into statements, the way the mysql and psql clients
// do for the files of this package: statements end with ; outside strings,
// quoted identifiers and comments; MySQL's DELIMITER changes the end marker
// (for BEGIN NOT ATOMIC ... END blocks), PostgreSQL's $$ quotes and \gexec
// are understood. Comments are removed, empty statements dropped.
func Split(dialect, script string) []Statement {
	var out []Statement
	var b strings.Builder
	delim := ";"
	pg := dialect == Postgres
	flush := func(gexec bool) {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, Statement{SQL: s, GExec: gexec})
		}
		b.Reset()
	}
	atLineStart := true
	for i := 0; i < len(script); i++ {
		c := script[i]
		if atLineStart && !pg {
			// DELIMITER is a client command, valid only at the start of a line.
			line := script[i:]
			if j := strings.IndexByte(line, '\n'); j >= 0 {
				line = line[:j]
			}
			if f := strings.Fields(line); len(f) == 2 && strings.EqualFold(f[0], "DELIMITER") {
				flush(false)
				delim = f[1]
				i += len(line)
				continue
			}
		}
		atLineStart = c == '\n'
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(script) {
				if script[j] == '\\' && !pg && c != '`' {
					j += 2
					continue
				}
				if script[j] == c {
					if j+1 < len(script) && script[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(script))
			b.WriteString(script[i:end])
			i = end - 1
		case c == '-' && strings.HasPrefix(script[i:], "--"):
			j := strings.IndexByte(script[i:], '\n')
			if j < 0 {
				i = len(script)
			} else {
				i += j - 1 // keep the newline
			}
		case c == '/' && strings.HasPrefix(script[i:], "/*"):
			j := strings.Index(script[i+2:], "*/")
			if j < 0 {
				i = len(script)
			} else {
				i += j + 3
			}
			b.WriteByte(' ')
		case pg && c == '$':
			// A dollar quote: $$ or $tag$ ... the same tag.
			j := strings.IndexByte(script[i+1:], '$')
			tag := ""
			if j >= 0 {
				tag = script[i : i+j+2]
			}
			if j < 0 || strings.ContainsAny(tag[1:len(tag)-1], " \t\n;()'") {
				b.WriteByte(c)
				continue
			}
			k := strings.Index(script[i+len(tag):], tag)
			end := len(script)
			if k >= 0 {
				end = i + len(tag) + k + len(tag)
			}
			b.WriteString(script[i:end])
			i = end - 1
		case pg && c == '\\' && strings.HasPrefix(script[i:], `\gexec`):
			flush(true)
			i += len(`\gexec`) - 1
		case strings.HasPrefix(script[i:], delim):
			flush(false)
			i += len(delim) - 1
		default:
			b.WriteByte(c)
		}
	}
	flush(false)
	return out
}
