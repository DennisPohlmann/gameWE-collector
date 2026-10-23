// SPDX-License-Identifier: GPL-3.0-or-later

package rcon_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/rcon"
	"gamewe-collector/internal/rcon/rcontest"
)

func TestGoldSrc(t *testing.T) {
	long := strings.Repeat("x", 2500)
	gs := rcontest.StartGoldSrc(t, "pw with space", func(cmd string) string {
		if cmd == "long" {
			return long
		}
		return "echo: " + cmd + "\n"
	})
	c := rcon.New(true, gs.Addr, "pw with space", time.Second)
	defer c.Close()
	out, err := c.Exec(`gamewe_amx_psay #2 "hello world"`)
	if err != nil || out != "echo: gamewe_amx_psay #2 \"hello world\"\n" {
		t.Fatalf("exec: %q %v", out, err)
	}
	if out, err := c.Exec("long"); err != nil || out != long {
		t.Errorf("multi-packet answer: %d bytes, %v", len(out), err)
	}
	gs.NewChallenge() // the server restarted: the old challenge is refused once
	if out, err := c.Exec("status"); err != nil || out != "echo: status\n" {
		t.Errorf("after a new challenge: %q %v", out, err)
	}
	if c.LocalAddr() == nil {
		t.Error("no local address")
	}
	bad := rcon.New(true, gs.Addr, "wrong", time.Second)
	defer bad.Close()
	if _, err := bad.Exec("status"); !errors.Is(err, rcon.ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := c.Exec("say a\nquit"); err == nil {
		t.Error("command with a line break sent")
	}
	if got := strings.Join(gs.Commands(), "|"); strings.Contains(got, "quit") {
		t.Errorf("commands %q", got)
	}
	dead := rcon.New(true, "127.0.0.1:1", "pw", 200*time.Millisecond)
	defer dead.Close()
	if _, err := dead.Exec("status"); err == nil {
		t.Error("no error without a server")
	}
}
