// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/config"
	"gamewe-collector/internal/database"
	"gamewe-collector/internal/geoip"
)

const configUsage = `gamewe config test [-config FILE] [-o text|json|yaml]
  check the config file the way gamewe run loads it, without starting
  anything: no port is opened, no database connection, no migration (that
  is gamewe check). Also: gamewe --config-test, gamewe -t, gamewe run -t.
  Exit code 0: no errors (warnings allowed); 1: errors; 2: wrong usage.
`

// configCmd is gamewe config SUBCOMMAND.
func configCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, configUsage)
		return exitError{2, ""}
	}
	switch args[0] {
	case "test":
		return configTestCmd(args[1:])
	case "help", "-h", "-help", "--help":
		fmt.Print(configUsage)
		return nil
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", args[0], configUsage)
	return exitError{2, ""}
}

// testFlag finds -t / --config-test (also =true) among the flags of gamewe
// run and returns the others.
func testFlag(args []string) ([]string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			continue // a flag value
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name != "t" && name != "config-test" {
			continue
		}
		if on, err := strconv.ParseBool(val); hasVal && err == nil && !on {
			return append(args[:i:i], args[i+1:]...), false
		}
		return append(args[:i:i], args[i+1:]...), true
	}
	return args, false
}

// removedFlags refuses the flags of gamewe run that are gone, with what to
// use instead (the flag package would only say "not defined").
func removedFlags(args []string) error {
	for _, a := range args {
		if a == "--" {
			break
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "listen" {
			return exitError{2, "-listen was removed: set listen.udp (enabled, address, port) in the config file"}
		}
	}
	return nil
}

// configTestCmd is gamewe config test (--config-test, -t): it takes the
// flags of gamewe run that change the configuration and checks them too.
func configTestCmd(args []string) error {
	if err := removedFlags(args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("config test", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "path to the YAML config file")
	level := fs.String("log-level", "info", "as for gamewe run (only checked)")
	reload := fs.Duration("reload", time.Minute, "as for gamewe run (only checked)")
	addOutputFlag(fs)
	fs.Parse(args)
	if fs.NArg() > 0 {
		return exitError{2, fmt.Sprintf("unexpected argument %q\n\n%s", fs.Arg(0), configUsage)}
	}
	if err := needList(false); err != nil {
		return err
	}
	rep := configTest(*path, config.LintOptions{GeoIPDefault: geoip.DefaultPath}, *level, *reload)
	doc := newConfigTestDoc(*path, rep)
	if structured() {
		if err := emit(os.Stdout, doc); err != nil {
			return err
		}
	} else {
		writeConfigTest(os.Stdout, doc)
	}
	if !doc.OK {
		return exitError{1, ""} // the summary line says it
	}
	return nil
}

// configTest lints the file and the flags of gamewe run, and loads the
// certificates of database.tls as the database connection would (offline).
func configTest(path string, opt config.LintOptions, level string, reload time.Duration) config.Report {
	r := config.LintFile(path, opt)
	if reload < 5*time.Second {
		r.Findings = append(r.Findings, config.Finding{Severity: config.SevError, Key: "-reload", Msg: "must be at least 5s"})
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		r.Findings = append(r.Findings, config.Finding{Severity: config.SevError, Key: "-log-level", Msg: fmt.Sprintf("%q: want debug, info, warn or error", level)})
	}
	if !r.Decoded || r.Config.Database.TLS.Mode == config.TLSDisable {
		return r
	}
	for _, f := range r.Findings {
		if f.Severity == config.SevError && strings.HasPrefix(f.Key, "database.tls") {
			return r // a file is missing or the mode is wrong: said already
		}
	}
	d := r.Config.Database
	if _, err := database.BuildTLS(d.TLS, d.Host); err != nil {
		key, msg := "database.tls", err.Error()
		for _, p := range []struct{ prefix, key string }{
			{"database.tls client certificate: ", "database.tls.cert_file"},
			{"database.tls.ca_file: ", "database.tls.ca_file"},
			{"database.tls.ca_file ", "database.tls.ca_file"},
		} {
			if strings.HasPrefix(msg, p.prefix) {
				key, msg = p.key, strings.TrimPrefix(msg, p.prefix)
				break
			}
		}
		r.Findings = append(r.Findings, config.Finding{Severity: config.SevError, Key: key, Msg: msg})
	}
	return r
}

// configTestDoc is the document of gamewe config test -o json|yaml. The
// messages hold no secret values.
type configTestDoc struct {
	Config   string          `json:"config"`
	OK       bool            `json:"ok"`
	SyntaxOK bool            `json:"syntax_ok"`
	Errors   int             `json:"errors"`
	Warnings int             `json:"warnings"`
	Findings []configFinding `json:"findings"`
}

type configFinding struct {
	Severity string `json:"severity"`
	Key      string `json:"key,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

func newConfigTestDoc(path string, r config.Report) configTestDoc {
	doc := configTestDoc{Config: path, OK: r.Errors() == 0, SyntaxOK: r.SyntaxOK, Errors: r.Errors(), Warnings: r.Warnings(),
		Findings: []configFinding{}}
	for _, f := range r.Findings {
		doc.Findings = append(doc.Findings, configFinding{string(f.Severity), f.Key, f.Line, f.Msg})
	}
	return doc
}

// writeConfigTest writes the text output, as nginx -t does: one line per
// finding, then the summary.
func writeConfigTest(w io.Writer, doc configTestDoc) {
	for _, f := range doc.Findings {
		msg := f.Message
		if f.Key != "" {
			msg = f.Key + ": " + msg
		}
		if f.Line > 0 {
			msg += fmt.Sprintf(" in %s:%d", doc.Config, f.Line)
		}
		fmt.Fprintf(w, "gamewe: [%s] %s\n", f.Severity, msg)
	}
	if doc.SyntaxOK {
		fmt.Fprintf(w, "gamewe: the configuration file %s syntax is ok\n", doc.Config)
	}
	switch {
	case !doc.OK:
		fmt.Fprintf(w, "gamewe: configuration file %s test failed (%s, %s)\n", doc.Config, plural(doc.Errors, "error"), plural(doc.Warnings, "warning"))
	case doc.Warnings > 0:
		fmt.Fprintf(w, "gamewe: configuration file %s test is successful (%s)\n", doc.Config, plural(doc.Warnings, "warning"))
	default:
		fmt.Fprintf(w, "gamewe: configuration file %s test is successful\n", doc.Config)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
