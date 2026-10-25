// SPDX-License-Identifier: GPL-3.0-or-later

package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type server struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	RconPasswordSet bool   `json:"rcon_password_set"`
	LogSecret       bool   `json:"log_secret"`
}

type list []server

func (list) Header() []string { return []string{"id", "name"} }
func (l list) Rows() [][]string {
	var out [][]string
	for _, s := range l {
		out = append(out, []string{string(rune('0' + s.ID)), s.Name})
	}
	return out
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Format{"": Text, "text": Text, "JSON": JSON, "yml": YAML, " yaml ": YAML, "csv": CSV, "tsv": TSV, "name": Name} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Parse("xml"); err == nil || !strings.Contains(err.Error(), "json, yaml") {
		t.Errorf("Parse(xml): %v", err)
	}
}

// Every string under a key that names a secret is masked, in maps and
// lists below it too; flags (bool) and *_set keys stay.
func TestMaskSecrets(t *testing.T) {
	v := map[string]any{
		"rcon_password":     "hunter2",
		"RconPassword":      "hunter2",
		"log_secret":        "123456",
		"db":                map[string]any{"password": "pw", "dsn": "user:pw@tcp(db)/x", "user": "gamewe"},
		"tokens":            []any{"a", "b"},
		"rcon_password_set": true,
		"log_secret_set":    true,
		"empty_password":    "",
		"name":              "hunter2 is the name",
	}
	for _, f := range []Format{JSON, YAML} {
		var b bytes.Buffer
		if err := Write(&b, f, v); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if strings.Count(out, "hunter2") != 1 || strings.Contains(out, "123456") || strings.Contains(out, "pw") {
			t.Errorf("%s: secret not masked:\n%s", f, out)
		}
		var got map[string]any
		var err error
		if f == JSON {
			err = json.Unmarshal(b.Bytes(), &got)
		} else {
			err = yaml.Unmarshal(b.Bytes(), &got)
		}
		if err != nil {
			t.Fatalf("%s: %v\n%s", f, err, out)
		}
		if got["rcon_password"] != Masked || got["RconPassword"] != Masked || got["log_secret"] != Masked ||
			got["db"].(map[string]any)["user"] != "gamewe" || got["db"].(map[string]any)["dsn"] != Masked ||
			got["tokens"].([]any)[1] != Masked || got["rcon_password_set"] != true || got["empty_password"] != "" {
			t.Errorf("%s: %v", f, got)
		}
	}
}

// The JSON document is valid, keeps the field order and the types, also
// for hostile strings (player names).
func TestJSON(t *testing.T) {
	names := []string{"=HYPERLINK(\"x\")", "a\"b\\c", "\x01\t\n", "ünï 😀", "null", "true", "1e3", "- x", ": y", "#z", "", "\xff"}
	var l list
	for i, n := range names {
		l = append(l, server{ID: int64(i), Name: n, LogSecret: i%2 == 0})
	}
	var b bytes.Buffer
	if err := Write(&b, JSON, l); err != nil {
		t.Fatal(err)
	}
	var got []server
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	for i, s := range got {
		want := strings.ToValidUTF8(names[i], "�")
		if s.Name != want || s.ID != int64(i) || s.LogSecret != (i%2 == 0) {
			t.Errorf("%d: %+v, want name %q", i, s, want)
		}
	}
	if !strings.HasPrefix(b.String(), "[\n  {\n    \"id\": 0,\n    \"name\":") {
		t.Errorf("order / indent:\n%s", b.String())
	}
	// YAML reads back the same
	b.Reset()
	if err := Write(&b, YAML, l); err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := yaml.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if len(got) != len(names) || got[5].Name != "true" || got[4].Name != "null" || got[6].Name != "1e3" {
		t.Errorf("yaml: %+v\n%s", got, b.String())
	}
	// empty lists and maps
	b.Reset()
	Write(&b, JSON, map[string]any{"a": []int{}, "b": map[string]int{}, "c": nil})
	if b.String() != "{\n  \"a\": [],\n  \"b\": {},\n  \"c\": null\n}\n" {
		t.Errorf("empty: %q", b.String())
	}
}

func TestTable(t *testing.T) {
	l := list{{ID: 1, Name: "=cmd|' /C calc'!A0"}, {ID: 2, Name: "tab\there,\"q\""}, {ID: 3, Name: "-5"}}
	var b bytes.Buffer
	if err := Write(&b, CSV, l); err != nil {
		t.Fatal(err)
	}
	want := "id,name\n1,'=cmd|' /C calc'!A0\n2,\"tab\there,\"\"q\"\"\"\n3,-5\n"
	if b.String() != want {
		t.Errorf("csv:\n%q\nwant\n%q", b.String(), want)
	}
	b.Reset()
	Write(&b, TSV, l)
	if want := "id\tname\n1\t'=cmd|' /C calc'!A0\n2\ttab here,\"q\"\n3\t-5\n"; b.String() != want {
		t.Errorf("tsv:\n%q\nwant\n%q", b.String(), want)
	}
	b.Reset()
	Write(&b, Name, l)
	if b.String() != "1\n2\n3\n" {
		t.Errorf("name: %q", b.String())
	}
	if err := Write(&b, CSV, map[string]int{}); !errors.Is(err, ErrNotList) {
		t.Errorf("csv of a map: %v", err)
	}
	if err := Write(&b, Text, l); err == nil {
		t.Error("text is written by the commands")
	}
}
