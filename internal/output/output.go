// SPDX-License-Identifier: GPL-3.0-or-later

// Package output writes the results of the gamewe commands in the format of
// -o / --output: text (the commands' own output), json, yaml, and for lists
// csv, tsv and name. Secrets never reach a document: the commands put only
// "set / not set" into their results, and Write masks every string under a
// key that names a password, secret, token or DSN as a second line of
// defence.
package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Format is an output format of -o.
type Format string

const (
	Text Format = "text" // the commands' own output (default)
	JSON Format = "json" // one indented JSON document
	YAML Format = "yaml" // one YAML document
	CSV  Format = "csv"  // lists: a header line and one line per item
	TSV  Format = "tsv"  // lists: as csv, tab separated
	Name Format = "name" // lists: the first column (id, code, key) only, one per line
)

// Formats are all formats, in the order of the help.
var Formats = []Format{Text, JSON, YAML, CSV, TSV, Name}

// Masked replaces a secret value.
const Masked = "***"

// ErrNotList is returned for csv, tsv and name with a result that is not
// a list.
var ErrNotList = errors.New("only list commands can be written as csv, tsv or name")

// Parse reads the value of -o (any case; yml is yaml, "" is text).
func Parse(s string) (Format, error) {
	f := Format(strings.ToLower(strings.TrimSpace(s)))
	switch f {
	case "":
		return Text, nil
	case "yml":
		return YAML, nil
	}
	for _, k := range Formats {
		if f == k {
			return f, nil
		}
	}
	return Text, fmt.Errorf("unknown output format %q (%s)", s, List())
}

// List is the formats for usage texts: "text, json, ...".
func List() string {
	s := make([]string, len(Formats))
	for i, f := range Formats {
		s[i] = string(f)
	}
	return strings.Join(s, ", ")
}

// Structured reports whether f is not the text output.
func (f Format) Structured() bool { return f != Text && f != "" }

// Tabular reports whether f needs a Table.
func (f Format) Tabular() bool { return f == CSV || f == TSV || f == Name }

// Table is a result that is a list: csv, tsv and name write it.
type Table interface {
	Header() []string // the column names, lower case
	Rows() [][]string // one per item; the first column identifies it (name)
}

// Write writes v as f. Text is not written here (the commands print it
// themselves): Write returns an error for it.
func Write(w io.Writer, f Format, v any) error {
	switch f {
	case JSON, YAML:
		n, err := toNode(v)
		if err != nil {
			return err
		}
		mask(n, false)
		if f == JSON {
			var b bytes.Buffer
			writeJSON(&b, n, "")
			b.WriteByte('\n')
			_, err = w.Write(b.Bytes())
			return err
		}
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(n); err != nil {
			return err
		}
		return enc.Close()
	case CSV, TSV, Name:
		t, ok := v.(Table)
		if !ok {
			return ErrNotList
		}
		return writeTable(w, f, t)
	}
	return fmt.Errorf("output format %q is not a document format", f)
}

// toNode converts v to a YAML node tree through its JSON form, so the json
// tags name the keys in every format and the order of the fields stays.
func toNode(v any) (*yaml.Node, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("output: %w", err)
	}
	n := &doc
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	plain(n)
	return n, nil
}

// plain drops the JSON flow and quoting styles: the YAML encoder chooses
// block style and quotes only where a string needs it.
func plain(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plain(c)
	}
}

// secretKey reports whether a key names a secret value: rcon_password,
// RconPassword, log_secret, password, dsn, token, ... but not the
// rcon_password_set flags.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	if strings.HasSuffix(k, "_set") || strings.HasSuffix(k, "set") && strings.Contains(k, "password") {
		return false
	}
	for _, s := range []string{"password", "passwd", "secret", "token", "dsn", "apikey", "api_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// mask replaces every non-empty string under a secret key (also inside
// lists and maps below it) with Masked.
func mask(n *yaml.Node, secret bool) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			mask(n.Content[i+1], secret || secretKey(n.Content[i].Value))
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			mask(c, secret)
		}
	case yaml.ScalarNode:
		if secret && n.Tag == "!!str" && n.Value != "" {
			n.Value = Masked
		}
	}
}

// writeJSON writes the node tree as indented JSON.
func writeJSON(b *bytes.Buffer, n *yaml.Node, indent string) {
	in := indent + "  "
	switch n.Kind {
	case yaml.MappingNode:
		if len(n.Content) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i := 0; i+1 < len(n.Content); i += 2 {
			b.WriteString(in)
			jsonString(b, n.Content[i].Value)
			b.WriteString(": ")
			writeJSON(b, n.Content[i+1], in)
			if i+2 < len(n.Content) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case yaml.SequenceNode:
		if len(n.Content) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, c := range n.Content {
			b.WriteString(in)
			writeJSON(b, c, in)
			if i+1 < len(n.Content) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			b.WriteString("null")
		case "!!bool", "!!int", "!!float":
			b.WriteString(n.Value) // as json.Marshal wrote it
		default:
			jsonString(b, n.Value)
		}
	default:
		b.WriteString("null")
	}
}

// jsonString writes s quoted, without escaping <, > and & (no HTML here).
func jsonString(b *bytes.Buffer, s string) {
	e := json.NewEncoder(b)
	e.SetEscapeHTML(false)
	e.Encode(s)             // a string always encodes
	b.Truncate(b.Len() - 1) // the newline of Encode
}

func writeTable(w io.Writer, f Format, t Table) error {
	rows := t.Rows()
	if f == Name {
		var b strings.Builder
		for _, r := range rows {
			if len(r) > 0 {
				b.WriteString(oneLine(r[0]))
				b.WriteByte('\n')
			}
		}
		_, err := io.WriteString(w, b.String())
		return err
	}
	if f == TSV {
		var b strings.Builder
		for _, r := range append([][]string{t.Header()}, rows...) {
			for i, c := range r {
				if i > 0 {
					b.WriteByte('\t')
				}
				b.WriteString(cell(oneLine(c)))
			}
			b.WriteByte('\n')
		}
		_, err := io.WriteString(w, b.String())
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(t.Header()); err != nil {
		return err
	}
	for _, r := range rows {
		c := make([]string, len(r))
		for i := range r {
			c[i] = cell(r[i])
		}
		if err := cw.Write(c); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// oneLine replaces tabs and line breaks (tsv, name) with spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// cell keeps a spreadsheet from running a value as a formula: player
// names and server names are chosen by players and admins (=HYPERLINK(...)).
// A value starting with = + - @ (or a tab / carriage return) that is not a
// number gets a leading apostrophe.
func cell(s string) string {
	if s == "" || !strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	return "'" + s
}
