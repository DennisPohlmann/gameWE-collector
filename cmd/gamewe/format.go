// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/admin"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/output"
)

// outFormat is the output format of -o / --output (before the command or
// among its flags).
var outFormat = output.Text

// formatFlag sets outFormat.
type formatFlag struct{}

func (formatFlag) String() string { return string(outFormat) }
func (formatFlag) Set(s string) error {
	f, err := output.Parse(s)
	if err != nil {
		return err
	}
	outFormat = f
	return nil
}

// addOutputFlag registers -o and -output (also --o / --output) on fs.
func addOutputFlag(fs *flag.FlagSet) {
	fs.Var(formatFlag{}, "o", "output format: "+output.List())
	fs.Var(formatFlag{}, "output", "output format (the same as -o)")
}

// globalFlags reads -o FORMAT, -o=FORMAT, --output FORMAT, ... before the
// command and returns the arguments from the command on.
func globalFlags(args []string) ([]string, error) {
	for len(args) > 0 {
		name, val, hasVal := strings.Cut(strings.TrimLeft(args[0], "-"), "=")
		if !strings.HasPrefix(args[0], "-") || (name != "o" && name != "output") {
			return args, nil
		}
		args = args[1:]
		if !hasVal {
			if len(args) == 0 {
				return nil, fmt.Errorf("flag needs an argument: -%s (%s)", name, output.List())
			}
			val, args = args[0], args[1:]
		}
		if err := (formatFlag{}).Set(val); err != nil {
			return nil, err
		}
	}
	return args, nil
}

// structured reports whether -o asks for a document instead of the text.
func structured() bool { return outFormat.Structured() }

// needList refuses csv, tsv and name for a command whose result is not a
// list, before the command does anything.
func needList(list bool) error {
	if outFormat.Tabular() && !list {
		return exitError{2, fmt.Sprintf("-o %s: %v; this command supports text, json and yaml", outFormat, output.ErrNotList)}
	}
	return nil
}

// emit writes the result v to out in the -o format.
func emit(out io.Writer, v any) error { return output.Write(out, outFormat, v) }

// textOut is where text meant for people (notes, warnings, prompts) goes:
// stdout for the text output, stderr when stdout carries a document.
func textOut() io.Writer {
	if structured() {
		return os.Stderr
	}
	return os.Stdout
}

// result is the document of a command that changes something (and of the
// maintenance commands): whether it worked, and the lines it printed.
type result struct {
	OK     bool       `json:"ok"`
	Error  string     `json:"error,omitempty"`
	Plan   *planDoc   `json:"plan,omitempty"`
	Tables []tableDoc `json:"tables,omitempty"`
	SQL    string     `json:"sql,omitempty"`
	// Messages are the lines of the text output.
	Messages []string `json:"messages"`
}

type tableDoc struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// captured runs fn. With a document format its text goes into the
// messages of a result, which is written to out also when fn fails (the
// error goes to stderr and the exit code as with the text output).
func captured(out io.Writer, fn func(w io.Writer, r *result) error) error {
	if !structured() {
		return fn(out, nil)
	}
	if err := needList(false); err != nil {
		return err
	}
	var buf bytes.Buffer
	r := &result{}
	err := fn(&buf, r)
	r.OK = err == nil
	var ee exitError
	if err != nil && !(errors.As(err, &ee) && ee.msg == "") {
		r.Error = err.Error()
	}
	r.Messages = lines(buf.String())
	if werr := emit(out, r); werr != nil && err == nil {
		return werr
	}
	return err
}

// lines splits text into its non-empty lines, without trailing spaces.
func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimRight(l, " \t\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---- database plans (db status, db migrate) ----

type planDoc struct {
	Database string       `json:"database"` // as in the text: "new (...)", "gamewe", ...
	Kind     string       `json:"kind"`
	Version  int          `json:"version"`
	Needs    int          `json:"needs"`
	UpToDate bool         `json:"up_to_date"`
	Applied  []appliedDoc `json:"applied"`
	Pending  []pendingDoc `json:"pending"`
	Steps    []stepDoc    `json:"steps"`
	Warnings []string     `json:"warnings,omitempty"`
	Missing  []string     `json:"missing,omitempty"`
	// OldTables are the tables gameWE no longer uses (gamewe db cleanup).
	OldTables []string `json:"old_tables,omitempty"`
}

type appliedDoc struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at"` // RFC 3339, UTC
	Note      string `json:"note,omitempty"`
}

type pendingDoc struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type stepDoc struct {
	Version int    `json:"version,omitempty"`
	Name    string `json:"name"`
	What    string `json:"what"`
	Table   string `json:"table,omitempty"`
	Rows    int64  `json:"rows,omitempty"`
	Slow    bool   `json:"slow,omitempty"`
}

func newPlanDoc(p *database.Plan) *planDoc {
	d := &planDoc{Database: describeKind(p), Kind: p.Kind, Version: p.Current, Needs: p.Latest, UpToDate: p.UpToDate(),
		Applied: []appliedDoc{}, Pending: []pendingDoc{}, Steps: []stepDoc{}, Warnings: p.Warnings}
	for _, a := range p.Applied {
		d.Applied = append(d.Applied, appliedDoc{a.Version, a.Name, a.AppliedAt.UTC().Format(time.RFC3339), a.Note})
	}
	for _, m := range p.Pending {
		d.Pending = append(d.Pending, pendingDoc{m.Version, m.Name, m.Desc})
	}
	for _, s := range p.Steps {
		d.Steps = append(d.Steps, stepDoc{s.Version, s.Name, s.What, s.Table, max(s.Rows, 0), s.Slow})
	}
	for _, r := range p.Missing {
		d.Missing = append(d.Missing, strings.TrimPrefix(r.String(), "schema incomplete: missing "))
	}
	for _, o := range p.Old {
		d.OldTables = append(d.OldTables, o.Table)
	}
	return d
}

// unixTime is a Unix time as RFC 3339 in local time, "" for 0.
func unixTime(t int64) string {
	if t == 0 {
		return ""
	}
	return time.Unix(t, 0).Format(time.RFC3339)
}

func itoa[T ~int | ~int64](n T) string { return strconv.FormatInt(int64(n), 10) }

func yesNo(b bool) string { return strconv.FormatBool(b) }

// ---- lists (csv, tsv, name) ----

type gameList []admin.Game

func (gameList) Header() []string {
	return []string{"code", "realgame", "realgame_guessed", "engine", "hidden", "rank_bots", "bot_points", "bot_kills", "bot_stats", "block_vac", "servers", "players", "name"}
}
func (l gameList) Rows() [][]string {
	out := [][]string{}
	for _, g := range l {
		out = append(out, []string{g.Code, g.Realgame, yesNo(g.Guessed), g.Engine, yesNo(g.Hidden), yesNo(g.RankBots), yesNo(g.BotPoints), yesNo(g.BotKills), yesNo(g.BotStats), yesNo(g.BlockVAC), itoa(g.Servers), itoa(g.Players), g.Name})
	}
	return out
}

type serverList []admin.ServerRow

func (serverList) Header() []string {
	return []string{"id", "address", "game", "engine", "rcon_password_set", "log_secret_set", "name"}
}
func (l serverList) Rows() [][]string {
	out := [][]string{}
	for _, s := range l {
		out = append(out, []string{itoa(s.ID), s.Address, s.Game, s.Engine, yesNo(s.RconPasswordSet), yesNo(s.LogSecretSet), s.Name})
	}
	return out
}

type optionList []admin.Option

func (optionList) Header() []string { return []string{"key", "value", "default", "description"} }
func (l optionList) Rows() [][]string {
	out := [][]string{}
	for _, o := range l {
		out = append(out, []string{o.Key, o.Value, yesNo(o.Default), o.Description})
	}
	return out
}
