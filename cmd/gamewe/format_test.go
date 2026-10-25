// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gamewe-collector/internal/database"
	"gamewe-collector/internal/output"
	"gamewe-collector/internal/version"
)

// setFormat sets -o for one test.
func setFormat(t *testing.T, f output.Format) {
	t.Helper()
	old := outFormat
	outFormat = f
	t.Cleanup(func() { outFormat = old })
}

func TestGlobalFlags(t *testing.T) {
	for _, c := range []struct {
		args []string
		want output.Format
		rest string
	}{
		{[]string{"server", "list"}, output.Text, "server list"},
		{[]string{"-o", "json", "server", "list"}, output.JSON, "server list"},
		{[]string{"--output", "yaml", "check"}, output.YAML, "check"},
		{[]string{"--output=csv", "-o=name", "game", "list"}, output.Name, "game list"},
		{[]string{"-output", "YML", "db", "status"}, output.YAML, "db status"},
		{[]string{"-V", "-o", "json"}, output.Text, "-V -o json"},
	} {
		setFormat(t, output.Text)
		rest, err := globalFlags(c.args)
		if err != nil || outFormat != c.want || strings.Join(rest, " ") != c.rest {
			t.Errorf("%v: %q %q %v", c.args, outFormat, rest, err)
		}
	}
	for _, args := range [][]string{{"-o"}, {"-o", "xml", "check"}} {
		if _, err := globalFlags(args); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func TestPresetOutput(t *testing.T) {
	setFormat(t, output.Text)
	presetOutput([]string{"-config", "x.yaml", "-yes", "--output", "json"})
	if outFormat != output.JSON {
		t.Errorf("got %q", outFormat)
	}
	presetOutput([]string{"--", "-o", "yaml"})
	if outFormat != output.JSON {
		t.Errorf("after --: got %q", outFormat)
	}
}

// -o and --output are flags of every FlagSet, with the same variable.
func TestOutputFlag(t *testing.T) {
	setFormat(t, output.Text)
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addOutputFlag(fs)
	if err := fs.Parse([]string{"--output", "tsv"}); err != nil || outFormat != output.TSV {
		t.Errorf("%q %v", outFormat, err)
	}
	if err := fs.Parse([]string{"-o", "xml"}); err == nil {
		t.Error("xml accepted")
	}
}

func TestCaptured(t *testing.T) {
	setFormat(t, output.JSON)
	var out bytes.Buffer
	err := captured(&out, func(w io.Writer, r *result) error {
		io.WriteString(w, "server 3 (198.51.100.7:27015): rcon password changed  \n\nnote: x\n")
		r.Tables = append(r.Tables, tableDoc{"hlstats_Players", 2})
		return errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Errorf("error %v", err)
	}
	var got result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	if got.OK || got.Error != "boom" || len(got.Messages) != 2 || got.Messages[0] != "server 3 (198.51.100.7:27015): rcon password changed" || got.Tables[0].Rows != 2 {
		t.Errorf("%+v", got)
	}
	// exit codes without a message (db migrate -n) are no error text
	out.Reset()
	captured(&out, func(w io.Writer, _ *result) error { return exitError{2, ""} })
	if !strings.Contains(out.String(), `"ok": false`) || strings.Contains(out.String(), `"error"`) {
		t.Errorf("%s", out.String())
	}
	// text: no document, the output as it is
	setFormat(t, output.Text)
	out.Reset()
	captured(&out, func(w io.Writer, r *result) error {
		if r != nil {
			t.Error("result for the text output")
		}
		io.WriteString(w, "done\n")
		return nil
	})
	if out.String() != "done\n" {
		t.Errorf("text %q", out.String())
	}
	// csv is refused before anything runs
	setFormat(t, output.CSV)
	ran := false
	err = captured(&out, func(io.Writer, *result) error { ran = true; return nil })
	var ee exitError
	if ran || !errors.As(err, &ee) || ee.code != 2 {
		t.Errorf("csv: ran %v, %v", ran, err)
	}
}

// The documents of the lists hold no secret: only whether the RCON
// password and the log secret are set.
func TestServerListDocument(t *testing.T) {
	l := serverList{{ID: 3, Address: "198.51.100.7:27015", Game: "css", Engine: "source", RconPasswordSet: true, LogSecretSet: false, Name: "=Fun"}}
	for _, f := range []output.Format{output.JSON, output.YAML, output.CSV, output.TSV, output.Name} {
		var b bytes.Buffer
		if err := output.Write(&b, f, l); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		s := b.String()
		switch f {
		case output.JSON:
			if !strings.Contains(s, `"rcon_password_set": true`) || !strings.Contains(s, `"log_secret_set": false`) {
				t.Errorf("json:\n%s", s)
			}
		case output.CSV:
			if s != "id,address,game,engine,rcon_password_set,log_secret_set,name\n3,198.51.100.7:27015,css,source,true,false,'=Fun\n" {
				t.Errorf("csv:\n%s", s)
			}
		case output.Name:
			if s != "3\n" {
				t.Errorf("name: %q", s)
			}
		}
	}
}

func TestPlanDocument(t *testing.T) {
	setFormat(t, output.YAML)
	d := newPlanDocForTest()
	var b bytes.Buffer
	if err := emit(&b, d); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"up_to_date: false", "applied: []", "- version: 2\n    name: gamewe-0.2.1", "steps:"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in\n%s", want, b.String())
		}
	}
}

// gamewe version -o json and gamewe -o yaml version print a document.
func TestVersionDocument(t *testing.T) {
	if os.Getenv("GAMEWE_VERSION_CHILD") == "1" {
		return // TestVersionFlags runs main
	}
	for _, args := range [][]string{{"version", "-o", "json"}, {"-o", "json", "-V"}, {"--output=yaml", "version"}} {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestVersionFlags$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GAMEWE_VERSION_CHILD=1")
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		want := "\"version\": \"" + version.Version + "\""
		if strings.Contains(args[0], "yaml") {
			want = "version: "
		}
		if !strings.Contains(string(out), want) {
			t.Errorf("%v: %q", args, out)
		}
	}
}

func newPlanDocForTest() *planDoc {
	return newPlanDoc(&database.Plan{Kind: database.KindHLstatsX, Latest: 2, Pending: database.Migrations,
		Steps: []database.Step{{Version: 2, Name: "gamewe-0.2.1", What: "alter table", Table: "hlstats_Events_Frags", Rows: -1}}})
}
