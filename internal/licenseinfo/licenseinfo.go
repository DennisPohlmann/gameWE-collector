// SPDX-License-Identifier: GPL-3.0-or-later

// Package licenseinfo lists the Go modules of gameWE with their license
// texts: the modules compiled into the gamewe binary and those only the
// tests use. tools/licensegen writes the result to licenses/ (texts and
// modules.json, `make licenses`); the test in cmd/gamewe checks that both
// and THIRD-PARTY-LICENSES.md are current.
package licenseinfo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Module is one row of licenses/modules.json.
type Module struct {
	Path    string   `json:"path"`
	Version string   `json:"version"`
	Scope   string   `json:"scope"` // "binary" or "test"
	License string   `json:"license"`
	Files   []string `json:"files"`
}

// Result is what licenses/ should hold: modules.json and, per module
// directory, the license files (name -> content).
type Result struct {
	Modules []Module
	Texts   map[string]map[string][]byte
}

// Dir is the directory under licenses/ of a module path.
func Dir(path string) string { return strings.ReplaceAll(path, "/", "_") }

var licenseFile = regexp.MustCompile(`^(?i)(LICEN[CS]E|COPYING|NOTICE)([-._][A-Za-z0-9.-]*)?$`)

// Collect asks the go tool (run in root, the repository) for the modules
// and reads their license files from the module cache.
func Collect(root string) (*Result, error) {
	binary, err := modules(root, "./cmd/gamewe")
	if err != nil {
		return nil, err
	}
	all, err := modules(root, "-test", "./...")
	if err != nil {
		return nil, err
	}
	res := &Result{Texts: map[string]map[string][]byte{}}
	for key, dir := range all {
		path, version, _ := strings.Cut(key, "@")
		scope := "test"
		if _, ok := binary[key]; ok {
			scope = "binary"
		}
		texts, err := readTexts(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if len(texts) == 0 {
			return nil, fmt.Errorf("%s: no license file in %s", key, dir)
		}
		m := Module{Path: path, Version: version, Scope: scope, License: SPDX(texts)}
		for name := range texts {
			m.Files = append(m.Files, name)
		}
		sort.Strings(m.Files)
		res.Modules = append(res.Modules, m)
		res.Texts[Dir(path)] = texts
	}
	sort.Slice(res.Modules, func(i, j int) bool { return res.Modules[i].Path < res.Modules[j].Path })
	return res, nil
}

// modules: "path@version" -> module directory of every non-main module in
// the dependencies of the given packages.
func modules(root string, args ...string) (map[string]string, error) {
	cmd := exec.Command("go", append([]string{"list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}"}, args...)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if key, dir, ok := strings.Cut(l, " "); ok {
			m[key] = dir
		}
	}
	return m, nil
}

func readTexts(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	texts := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !licenseFile.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		texts[e.Name()] = b
	}
	return texts, nil
}

// SPDX names the license of a module from its license files: one file per
// license (LICENSE-APACHE, LICENSE-MIT) is a choice ("OR"), several licenses
// in one file apply together ("AND"). NOTICE files carry no license.
// "UNKNOWN" when a text is not recognised.
func SPDX(texts map[string][]byte) string {
	var names []string
	for name := range texts {
		if !strings.HasPrefix(strings.ToUpper(name), "NOTICE") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var per []string
	for _, n := range names {
		ids := detect(string(texts[n]))
		if len(ids) == 0 {
			return "UNKNOWN"
		}
		per = append(per, strings.Join(ids, " AND "))
	}
	if len(per) == 0 {
		return "UNKNOWN"
	}
	return strings.Join(per, " OR ")
}

func detect(t string) []string {
	t = strings.Join(strings.Fields(t), " ")
	var ids []string
	if strings.Contains(t, "Permission is hereby granted, free of charge") {
		ids = append(ids, "MIT")
	}
	if strings.Contains(t, "Apache License") && strings.Contains(t, "Version 2.0") {
		ids = append(ids, "Apache-2.0")
	}
	if strings.Contains(t, "Mozilla Public License Version 2.0") || strings.Contains(t, "Mozilla Public License, version 2.0") {
		ids = append(ids, "MPL-2.0")
	}
	if strings.Contains(t, "Permission to use, copy, modify, and/or distribute this software for any purpose") ||
		strings.Contains(t, "Permission to use, copy, modify, and distribute this software for any purpose with or without fee") {
		ids = append(ids, "ISC")
	}
	if strings.Contains(t, "Redistribution and use in source and binary forms") {
		if strings.Contains(t, "Neither the name") || strings.Contains(t, "may be used to endorse or promote") {
			ids = append(ids, "BSD-3-Clause")
		} else {
			ids = append(ids, "BSD-2-Clause")
		}
	}
	return ids
}

// JSON is the content of licenses/modules.json.
func (r *Result) JSON() []byte {
	b, _ := json.MarshalIndent(r.Modules, "", "  ")
	return append(b, '\n')
}

// Write stores the license texts and modules.json under dir (licenses/);
// module directories that are no longer used are removed. The Go
// distribution's own text (licenses/go) is kept.
func (r *Result) Write(dir string) error {
	keep := map[string]bool{"go": true}
	for d, texts := range r.Texts {
		keep[d] = true
		p := filepath.Join(dir, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
		old, _ := os.ReadDir(p)
		for _, e := range old {
			if _, ok := texts[e.Name()]; !ok {
				if err := os.Remove(filepath.Join(p, e.Name())); err != nil {
					return err
				}
			}
		}
		for name, b := range texts {
			if err := os.WriteFile(filepath.Join(p, name), b, 0o644); err != nil {
				return err
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return os.WriteFile(filepath.Join(dir, "modules.json"), r.JSON(), 0o644)
}

// Diff lists what differs between r and the files under dir (empty: current).
func (r *Result) Diff(dir string) []string {
	var out []string
	if b, err := os.ReadFile(filepath.Join(dir, "modules.json")); err != nil || !bytes.Equal(b, r.JSON()) {
		out = append(out, "modules.json")
	}
	for d, texts := range r.Texts {
		for name, want := range texts {
			if got, err := os.ReadFile(filepath.Join(dir, d, name)); err != nil || !bytes.Equal(got, want) {
				out = append(out, filepath.Join(d, name))
			}
		}
		if old, err := os.ReadDir(filepath.Join(dir, d)); err == nil {
			for _, e := range old {
				if _, ok := texts[e.Name()]; !ok {
					out = append(out, filepath.Join(d, e.Name())+" (not used)")
				}
			}
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if _, ok := r.Texts[e.Name()]; e.IsDir() && !ok && e.Name() != "go" {
			out = append(out, e.Name()+"/ (not used)")
		}
	}
	sort.Strings(out)
	return out
}
