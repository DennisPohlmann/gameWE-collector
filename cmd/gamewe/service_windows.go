// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"gamewe-collector/internal/config"
)

// serviceName is the Windows service of the collector; its virtual account
// is NT SERVICE\gamewe-collector.
const serviceName = "gamewe-collector"

const serviceAccount = `NT SERVICE\` + serviceName

// logMax is the size of collector.log before it becomes collector.log.1.
const logMax = 50 << 20

func init() {
	usage += `
Windows service (as administrator; the service runs gamewe run as the virtual
account ` + serviceAccount + `, starts with Windows, is restarted after a
failure and logs to ` + filepath.Join(config.DataDir(), "collector.log") + `):
  gamewe service install [-config FILE] [--dry-run|-n] [--force|-f]
                                   install it; only Administrators, SYSTEM and the service
                                   may then read ` + config.Dir() + `,
                                   the service may write its data directory
  gamewe service uninstall|start|stop [--dry-run|-n] [--force|-f]
`
}

// osCommand runs gamewe service.
func osCommand(cmd string, args []string) (bool, error) {
	if cmd != "service" {
		return false, nil
	}
	return true, serviceCmd(args)
}

func serviceCmd(args []string) error {
	if len(args) == 0 {
		return exitError{2, "service install|uninstall|start|stop"}
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("service "+sub, flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "path to the YAML config file (install)")
	var cf changeFlags
	cf.add(fs, "only say what it would do")
	fs.Parse(args)
	if err := cf.check(); err != nil {
		return err
	}
	var what string
	var do func(*mgr.Mgr) error
	switch sub {
	case "install":
		cfg, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(cfg); err != nil {
			return exitError{exitConfig, fmt.Sprintf("%v: create the config first (from config.example.yaml), then install the service", err)}
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		what = fmt.Sprintf("install the service %s: %q run -config %q as %s, started with Windows; Administrators, SYSTEM and the service only may read %s, the service may write %s",
			serviceName, exe, cfg, serviceAccount, config.Dir(), config.DataDir())
		do = func(m *mgr.Mgr) error { return serviceInstall(m, exe, cfg) }
	case "uninstall":
		what = "stop and remove the service " + serviceName + " (config, data and log stay)"
		do = serviceUninstall
	case "start":
		what = "start the service " + serviceName
		do = func(m *mgr.Mgr) error { return withService(m, func(s *mgr.Service) error { return s.Start() }) }
	case "stop":
		what = "stop the service " + serviceName
		do = func(m *mgr.Mgr) error { return withService(m, serviceStop) }
	default:
		return exitError{2, fmt.Sprintf("unknown service command %q: install, uninstall, start or stop", sub)}
	}
	fmt.Println(what)
	if cf.dryRun {
		fmt.Println("dry run: nothing changed")
		return nil
	}
	if err := approve(context.Background(), cf, os.Stdin, os.Stdout, stdinIsTerminal(), "Go on?"); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return errors.New("access denied: run it in a terminal opened as administrator")
	}
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if err := do(m); err != nil {
		return err
	}
	fmt.Println("done")
	return nil
}

func serviceInstall(m *mgr.Mgr, exe, cfg string) error {
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      "gameWE collector",
		Description:      "gameWE collector: game server logs into player statistics (gamewe run)",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true, // after a local database server
		ServiceStartName: serviceAccount,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "run", "-config", cfg)
	if err != nil {
		return err
	}
	defer s.Close()
	// As Restart=on-failure of the systemd unit, also after an exit code
	// (database not reachable yet): after 5s, 30s, then every minute (the
	// last action repeats); the count starts again after a day.
	restart := func(d time.Duration) mgr.RecoveryAction {
		return mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: d}
	}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{restart(5 * time.Second), restart(30 * time.Second), restart(time.Minute)}, 24*60*60); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return err
	}
	if err := os.MkdirAll(config.DataDir(), 0o750); err != nil {
		return err
	}
	// Not inherited from ProgramData (Users may read there): the config
	// holds the database password.
	if err := icacls(config.Dir(), "/inheritance:r", "/grant:r",
		"*S-1-5-32-544:(OI)(CI)F", "*S-1-5-18:(OI)(CI)F", serviceAccount+":(OI)(CI)RX"); err != nil {
		return err
	}
	if err := icacls(config.DataDir(), "/grant", serviceAccount+":(OI)(CI)M"); err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToLower(cfg), strings.ToLower(config.Dir()+`\`)) {
		if err := icacls(cfg, "/grant", serviceAccount+":R"); err != nil {
			return err
		}
	}
	return nil
}

func icacls(args ...string) error {
	out, err := exec.Command("icacls.exe", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func serviceUninstall(m *mgr.Mgr) error {
	return withService(m, func(s *mgr.Service) error {
		if st, err := s.Query(); err == nil && st.State != svc.Stopped {
			if err := serviceStop(s); err != nil {
				return err
			}
		}
		return s.Delete()
	})
}

func withService(m *mgr.Mgr, f func(*mgr.Service) error) error {
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s: %w (gamewe service install installs it)", serviceName, err)
	}
	defer s.Close()
	return f(s)
}

// serviceStop stops the service and waits until it is stopped (the
// collector writes what it holds first).
func serviceStop(s *mgr.Service) error {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	for end := time.Now().Add(45 * time.Second); st.State != svc.Stopped; {
		if time.Now().After(end) {
			return errors.New("the service did not stop within 45s")
		}
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

// serviceMain runs gamewe run under the service manager, when it started
// this process (the command line is the one of serviceInstall).
func serviceMain() (code int, ok bool) {
	if is, err := svc.IsWindowsService(); err != nil || !is {
		return 0, false
	}
	if err := svc.Run(serviceName, collectorService{}); err != nil {
		return 1, true
	}
	return 0, true
}

type collectorService struct{}

func (collectorService) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	var errOut io.Writer = io.Discard
	if err := os.MkdirAll(config.DataDir(), 0o750); err == nil {
		if l, err := openRotatingLog(filepath.Join(config.DataDir(), "collector.log"), logMax); err == nil {
			defer l.Close()
			logOut, errOut = l, l
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runContext = ctx
	done := make(chan error, 1)
	go func() {
		if len(os.Args) < 2 || os.Args[1] != "run" {
			done <- exitError{2, "the service command line must be: gamewe run -config FILE (gamewe service install)"}
			return
		}
		done <- runCmd(os.Args[2:])
	}()
	// Running at once: the start (database, a migration) may take longer
	// than the service manager waits for a starting service.
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err == nil {
				return false, 0
			}
			fmt.Fprintf(errOut, "gamewe run: %v\n", err)
			var ee exitError
			if errors.As(err, &ee) {
				return true, uint32(ee.code)
			}
			return true, 1
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 40000}
				cancel()
			}
		}
	}
}
