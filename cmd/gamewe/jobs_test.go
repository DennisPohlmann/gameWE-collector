// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/stats"
)

func TestJobValues(t *testing.T) {
	got, err := jobValues("-type", " inactive,reset,,inactive ", jobTypes)
	if err != nil || !slices.Equal(got, []string{"inactive", "reset"}) {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := jobValues("-status", "", jobStatuses); err != nil || got != nil {
		t.Errorf("empty: got %q, %v", got, err)
	}
	if _, err := jobValues("-status", "done,bogus", jobStatuses); err == nil || !strings.Contains(err.Error(), `-status "bogus"`) {
		t.Errorf("bogus: %v", err)
	}
}

func TestQueueText(t *testing.T) {
	open := []stats.Job{
		{ID: 3, Type: stats.JobReset, Game: "css", Status: stats.JobRunning},
		{ID: 4, Type: stats.JobInactive, Status: stats.JobPending},
		{ID: 5, Type: stats.JobReset, Game: "tf", Status: stats.JobPending},
		{ID: 6, Type: stats.JobScheduledReset, Game: "csgo", Status: stats.JobPending},
		{ID: 9, Type: stats.JobInactive, Status: stats.JobPending},
	}
	for _, c := range []struct {
		id   int64
		want string
	}{
		{3, "next in line"},
		{4, "1 job ahead: 3 reset css (running)"},
		{6, "3 jobs ahead: 3 reset css (running), 4 inactive (pending), 5 reset tf (pending)"},
		{9, "4 jobs ahead: 3 reset css (running), 4 inactive (pending), 5 reset tf (pending), ..."},
	} {
		if got := queueText(c.id, open); got != c.want {
			t.Errorf("job %d: %q, want %q", c.id, got, c.want)
		}
	}
}

func TestJobWatch(t *testing.T) {
	var b strings.Builder
	w := &jobWatch{out: &b, id: 7}
	t0 := time.Date(2026, 10, 27, 8, 0, 0, 0, time.Local)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	ahead := []stats.Job{{ID: 6, Type: stats.JobReset, Game: "css", Status: stats.JobRunning}, {ID: 7, Status: stats.JobPending}}
	pending := stats.Job{ID: 7, Status: stats.JobPending}
	running := stats.Job{ID: 7, Status: stats.JobRunning, StartedAt: at(30).Unix()}

	w.update(pending, ahead, at(2))
	w.update(pending, ahead, at(4))  // nothing new
	w.update(pending, ahead, at(18)) // a note after jobNoteEvery
	w.update(pending, ahead[1:], at(20))
	w.update(running, nil, at(32))
	w.update(running, nil, at(34)) // nothing new
	running.Result = "500 of 1200 players moved so far"
	w.update(running, nil, at(36))
	w.update(running, nil, at(40))
	w.update(running, nil, at(52)) // a note again
	want := `job 7 waits for the collector: 1 job ahead: 6 reset css (running)
job 7 still waiting (16s) ...
job 7 waits for the collector: next in line
job 7 started in the collector at 08:00:30, running ...
job 7 running (6s): 500 of 1200 players moved so far
job 7 still running (22s) ...
`
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
