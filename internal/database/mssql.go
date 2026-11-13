// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"

	"gamewe-collector/internal/schema"
)

// ToMSSQL turns a query written for MariaDB and PostgreSQL (the subset both
// understand, with ? placeholders) into T-SQL for Microsoft SQL Server:
//
//   - ? becomes @p1, @p2, ... (go-mssqldb numbers positional arguments so)
//   - LIMIT n [OFFSET m] and LIMIT m, n become OFFSET m ROWS FETCH NEXT n
//     ROWS ONLY, with ORDER BY (SELECT NULL) when the query has no ORDER BY
//   - `name` becomes [name]; TRUE and FALSE become 1 and 0
//   - IFNULL( becomes COALESCE(, LENGTH( and CHAR_LENGTH( become LEN(
//   - ROUND(x) gets its second argument (ROUND(x, 0))
//   - AVG(x) averages as float: SQL Server's AVG of an integer column is an
//     integer, MariaDB's and PostgreSQL's a decimal
//
// Strings, quoted identifiers and comments are left alone.
func ToMSSQL(q string) string {
	q = fixCalls(q)
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	// ordered[d]: the query at paren depth d has an ORDER BY so far.
	ordered := []bool{false}
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			end := skipQuoted(q, i)
			b.WriteString(q[i:end])
			i = end - 1
		case c == '`':
			end := skipQuoted(q, i)
			b.WriteByte('[')
			b.WriteString(strings.Trim(q[i:end], "`"))
			b.WriteByte(']')
			i = end - 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-', c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := skipComment(q, i)
			b.WriteString(q[i:end])
			i = end - 1
		case c == '?':
			n++
			b.WriteString("@p" + strconv.Itoa(n))
		case c == '(':
			ordered = append(ordered, false)
			b.WriteByte(c)
		case c == ')':
			if len(ordered) > 1 {
				ordered = ordered[:len(ordered)-1]
			}
			b.WriteByte(c)
		case isWordStart(c) && (i == 0 || !isWord(q[i-1])):
			j := i
			for j < len(q) && isWord(q[j]) {
				j++
			}
			word := strings.ToUpper(q[i:j])
			switch word {
			case "ORDER":
				ordered[len(ordered)-1] = true
				b.WriteString(q[i:j])
			case "LIMIT":
				lim, off, end, ok := parseLimit(q, j, &n)
				if !ok {
					b.WriteString(q[i:j])
					break
				}
				out := strings.TrimRight(b.String(), " \t\n")
				b.Reset()
				b.WriteString(out)
				if !ordered[len(ordered)-1] {
					b.WriteString(" ORDER BY (SELECT NULL)")
				}
				b.WriteString(" OFFSET " + off + " ROWS FETCH NEXT " + lim + " ROWS ONLY")
				j = end
			case "TRUE":
				b.WriteByte('1')
			case "FALSE":
				b.WriteByte('0')
			default:
				b.WriteString(q[i:j])
			}
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// parseLimit reads what follows LIMIT at q[i:]: "n", "n OFFSET m" or
// "m, n", each a number or a ?, and returns them as T-SQL values (?
// numbered on from *n) and the end of the clause.
func parseLimit(q string, i int, n *int) (lim, off string, end int, ok bool) {
	tok := func(i int) (string, int) {
		for i < len(q) && (q[i] == ' ' || q[i] == '\t' || q[i] == '\n' || q[i] == '\r') {
			i++
		}
		j := i
		if j < len(q) && (q[j] == '?' || q[j] == ',') {
			return q[j : j+1], j + 1
		}
		for j < len(q) && isWord(q[j]) {
			j++
		}
		return q[i:j], j
	}
	val := func(t string) (string, bool) {
		if t == "?" {
			*n++
			return "@p" + strconv.Itoa(*n), true
		}
		if _, err := strconv.Atoi(t); err == nil {
			return t, true
		}
		return "", false
	}
	t, j := tok(i)
	first, ok := val(t)
	if !ok {
		return "", "", 0, false
	}
	off = "0"
	lim = first
	end = j
	switch t2, j2 := tok(j); strings.ToUpper(t2) {
	case ",":
		t3, j3 := tok(j2)
		if lim, ok = val(t3); !ok {
			return "", "", 0, false
		}
		off, end = first, j3
	case "OFFSET":
		t3, j3 := tok(j2)
		if off, ok = val(t3); !ok {
			return "", "", 0, false
		}
		end = j3
	}
	return lim, off, end, true
}

// fixCalls rewrites the function calls that differ: IFNULL, LENGTH,
// CHAR_LENGTH, ROUND with one argument, AVG.
func fixCalls(q string) string {
	type edit struct {
		at, cut int
		s       string
	}
	var edits []edit
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(q, i) - 1
			continue
		case c == '-' && i+1 < len(q) && q[i+1] == '-', c == '/' && i+1 < len(q) && q[i+1] == '*':
			i = skipComment(q, i) - 1
			continue
		case !isWordStart(c) || i > 0 && isWord(q[i-1]):
			continue
		}
		j := i
		for j < len(q) && isWord(q[j]) {
			j++
		}
		k := j
		for k < len(q) && q[k] == ' ' {
			k++
		}
		if k >= len(q) || q[k] != '(' {
			i = j - 1
			continue
		}
		close := matchParen(q, k)
		switch strings.ToUpper(q[i:j]) {
		case "IFNULL":
			edits = append(edits, edit{i, j - i, "COALESCE"})
		case "LENGTH", "CHAR_LENGTH":
			edits = append(edits, edit{i, j - i, "LEN"})
		case "ROUND":
			if close > 0 && !topLevelComma(q[k+1:close]) {
				edits = append(edits, edit{close, 0, ", 0"})
			}
		case "AVG":
			if close > 0 {
				edits = append(edits, edit{k + 1, 0, "CAST("}, edit{close, 0, " AS float)"})
			}
		}
		i = j - 1
	}
	if len(edits) == 0 {
		return q
	}
	// Applied from the end so the positions stay valid; at the same position
	// the edit found later (IFNULL inside AVG) goes first.
	sort.SliceStable(edits, func(a, b int) bool { return edits[a].at < edits[b].at })
	for e := len(edits) - 1; e >= 0; e-- {
		x := edits[e]
		q = q[:x.at] + x.s + q[x.at+x.cut:]
	}
	return q
}

// matchParen returns the index of the paren closing the one at q[open], or
// -1.
func matchParen(q string, open int) int {
	depth := 0
	for i := open; i < len(q); i++ {
		switch c := q[i]; {
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(q, i) - 1
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func topLevelComma(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(s, i) - 1
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			return true
		}
	}
	return false
}

// skipQuoted returns the end (exclusive) of the quoted text starting at
// q[i]; a doubled quote is an escaped one.
func skipQuoted(q string, i int) int {
	c := q[i]
	j := i + 1
	for j < len(q) {
		if q[j] == c {
			if j+1 < len(q) && q[j+1] == c {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return len(q)
}

// skipComment returns the end (exclusive) of the -- or /* */ comment at q[i].
func skipComment(q string, i int) int {
	if q[i] == '-' {
		if end := strings.IndexByte(q[i:], '\n'); end >= 0 {
			return i + end
		}
		return len(q)
	}
	if end := strings.Index(q[i+2:], "*/"); end >= 0 {
		return i + 2 + end + 2
	}
	return len(q)
}

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isWord(c byte) bool { return isWordStart(c) || c >= '0' && c <= '9' || c == '@' || c == '$' }

// msExists reports SQL Server's "already exists" errors of CREATE TABLE,
// CREATE INDEX and ALTER TABLE ADD: SQL Server has no IF NOT EXISTS, and a
// test by name in a string literal would miss the table prefix rewrite.
func msExists(err error) bool {
	var se mssql.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Number {
	case 1913, 2714, 2705: // index, object, column exists
		return true
	}
	return false
}

// Application locks (sp_getapplock) of the session, named like GET_LOCK:
// the lock's prefix and the database name.
const (
	msLockName    = `DECLARE @res nvarchar(255) = LEFT(@p1 + DB_NAME(), 255); `
	msReleaseLock = msLockName + `IF APPLOCK_MODE('public', @res, 'Session') <> 'NoLock' EXEC sp_releaseapplock @Resource = @res, @LockOwner = 'Session'`
	msHoldsLock   = msLockName + `SELECT CASE WHEN APPLOCK_MODE('public', @res, 'Session') = 'Exclusive' THEN 1 ELSE 0 END`
	msLockUsed    = msLockName + `SELECT CASE WHEN APPLOCK_TEST('public', @res, 'Exclusive', 'Session') = 1 THEN 0 ELSE 1 END`
)

// msTryLock takes the application lock prefix+database without waiting.
func msTryLock(ctx context.Context, conn *sql.Conn, prefix string) (bool, error) {
	var r int
	err := conn.QueryRowContext(ctx, msLockName+`DECLARE @r int; EXEC @r = sp_getapplock @Resource = @res, @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = 0; SELECT @r`, prefix).Scan(&r)
	return r >= 0, err
}

var reInsert = regexp.MustCompile(`(?is)^\s*INSERT INTO (\w+)\s*\(([^)]*)\)\s*(VALUES|SELECT)\b(.*)$`)

// msKeys returns the primary key and unique indexes of a schema table that
// only use columns of cols.
func msKeys(table string, cols []string) [][]string {
	tables, err := schema.Tables(schema.MSSQL)
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[strings.ToLower(c)] = true
	}
	var keys [][]string
	for _, t := range tables {
		if !strings.EqualFold(t.Name, table) {
			continue
		}
		for _, ix := range t.Indexes {
			if !ix.Unique || slices.ContainsFunc(ix.Columns, func(c string) bool { return !have[strings.ToLower(c)] }) {
				continue
			}
			keys = append(keys, ix.Columns)
		}
	}
	return keys
}

func splitCols(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(c))
	}
	return out
}

// msSource is the rows of an INSERT ... VALUES / SELECT as a derived table v.
func msSource(kind, rest, cols string) string {
	return "(" + kind + rest + ") AS v (" + cols + ")"
}

// MSSQLIgnore turns "INSERT INTO t (cols) VALUES (...)" or "... SELECT ..."
// into an insert that skips the rows whose primary or unique key exists, as
// MariaDB's INSERT IGNORE (SQL Server has none). The key test holds the key
// range until the statement ends, so two sessions cannot both insert. A
// statement of another form is returned as it is.
func MSSQLIgnore(insert string) string {
	m := reInsert.FindStringSubmatch(insert)
	if m == nil {
		return insert
	}
	cols := splitCols(m[2])
	var or []string
	for _, k := range msKeys(m[1], cols) {
		var and []string
		for _, c := range k {
			and = append(and, "gw_t."+c+" = v."+c)
		}
		or = append(or, "("+strings.Join(and, " AND ")+")")
	}
	if len(or) == 0 {
		return insert // no key among the columns: nothing can collide
	}
	return "INSERT INTO " + m[1] + " (" + m[2] + ") SELECT * FROM " + msSource(m[3], m[4], m[2]) +
		" WHERE NOT EXISTS (SELECT 1 FROM " + m[1] + " gw_t WITH (UPDLOCK, HOLDLOCK) WHERE " + strings.Join(or, " OR ") + ")"
}

// MSSQLUpsert turns an "INSERT INTO t (cols) VALUES ..." into a MERGE on
// key that runs the assignments sets ("col = expr") when the row exists. In
// them t.col is the stored value, v.col the inserted one (MSSQLNew).
func MSSQLUpsert(insert, key string, sets []string) string {
	m := reInsert.FindStringSubmatch(insert)
	if m == nil {
		return insert
	}
	t := m[1]
	var on, vals []string
	for _, k := range splitCols(key) {
		on = append(on, t+"."+k+" = v."+k)
	}
	for _, c := range splitCols(m[2]) {
		vals = append(vals, "v."+c)
	}
	return "MERGE INTO " + t + " WITH (HOLDLOCK) USING " + msSource(m[3], m[4], m[2]) + " ON " + strings.Join(on, " AND ") +
		" WHEN MATCHED THEN UPDATE SET " + strings.Join(sets, ", ") +
		" WHEN NOT MATCHED THEN INSERT (" + m[2] + ") VALUES (" + strings.Join(vals, ", ") + ");"
}

// MSSQLDeadlock reports SQL Server's deadlock error (1205): the statement
// was rolled back and may run again. Two MERGE ... WITH (HOLDLOCK) on the
// same key range can deadlock where MariaDB and PostgreSQL wait.
func MSSQLDeadlock(err error) bool {
	var se mssql.Error
	return errors.As(err, &se) && se.Number == 1205
}

// MSSQLIdentityInsert lets q (an insert that gives the identity column of
// table) run: SET IDENTITY_INSERT around it. The driver resets the session
// option when the connection goes back to the pool.
func MSSQLIdentityInsert(table, q string) string {
	return "SET IDENTITY_INSERT " + table + " ON; " + q + "; SET IDENTITY_INSERT " + table + " OFF"
}

// MSSQLColumns returns the columns of a schema table, comma-separated, for
// statements that copy whole rows (SQL Server needs them named when the
// table has an identity column).
func MSSQLColumns(table string) string {
	tables, _ := schema.Tables(schema.MSSQL)
	for _, t := range tables {
		if strings.EqualFold(t.Name, table) {
			names := make([]string, len(t.Columns))
			for i, c := range t.Columns {
				names[i] = c.Name
			}
			return strings.Join(names, ", ")
		}
	}
	return "*"
}
