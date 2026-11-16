// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/notify"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/update"
)

// A scheduled reset tells the hall of fame once to every target that wants
// the hall of fame or resets; a manual reset only those that want resets; a
// test job sends to the admin's own address; a failure is stored as the
// target's status; a new release is told once.
func TestIntegrationNotify(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	ctx := context.Background()
	const admin = "0123456789abcdef0123456789abcdef"
	for k, v := range map[string]string{
		"gamewe_notify_email": "1", "gamewe_notify_email_to": "team@example.org", "gamewe_notify_email_reset": "0",
		"gamewe_notify_email_from": "stats@example.org", "gamewe_notify_smtp_host": "smtp.example.org", "gamewe_notify_lang": "de",
		notify.UserPrefix + admin + "_email": "reset,update|me@example.org",
		stats.ScheduleKey("css"):             "month",
	} {
		if err := store.SetOption(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	type sent struct {
		to    string
		title string
	}
	var mails []sent
	fail := ""
	sender := &notify.Sender{Mail: func(_ context.Context, _ map[string]string, to []string, m notify.Message) error {
		if to[0] == fail {
			return errors.New("550 mailbox unavailable")
		}
		mails = append(mails, sent{strings.Join(to, ","), m.Title})
		return nil
	}}
	loc := time.UTC
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, loc)
	latest := update.Release{Version: "9.9.9", URL: "https://github.com/DennisPohlmann/gameWE-collector/releases/tag/v9.9.9"}
	jobs := &Jobs{Store: store, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Zone: loc, Now: func() time.Time { return now },
		Notify: sender, Version: "1.0.0", UpdateCheck: true,
		Latest: func(context.Context, string) (update.Release, error) { return latest, nil }}
	if err := jobs.QueueScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	// The first poll: the collector's release is newer than the running
	// one; the web wrote no version, so its release is only remembered.
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 1 || mails[0].to != "me@example.org" || mails[0].title != "gameWE Collector 9.9.9 ist erschienen" {
		t.Fatalf("update mails %+v", mails)
	}
	mails = nil

	now = time.Date(2026, 11, 1, 0, 0, 30, 0, loc)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 2 || mails[0].to != "team@example.org" || mails[1].to != "me@example.org" || !strings.HasPrefix(mails[0].title, "Ruhmeshalle Counter-Strike: Source: 2026-10-01 – 2026-10-31") {
		t.Fatalf("hall mails %+v", mails)
	}
	mails = nil

	// A manual reset: only the admin wants resets.
	if _, err := store.AddJob(ctx, stats.JobReset, "tf", stats.ResetPlayerStats, "dennis", stats.JobOrigin{Feature: stats.OriginAdmin}, now); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 1 || mails[0].to != "me@example.org" || mails[0].title != "Ranking Team Fortress 2 zurückgesetzt" {
		t.Fatalf("reset mails %+v", mails)
	}

	// Test messages: the admin's own (fails), the shared one, a missing one.
	fail = "me@example.org"
	for _, j := range [][2]string{{admin, notify.Email}, {"", notify.Email}, {"", notify.Telegram}} {
		if _, err := store.AddJob(ctx, stats.JobNotifyTest, j[0], j[1], "dennis", stats.JobOrigin{Feature: stats.OriginAdmin}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	recent, err := store.RecentJobs(ctx, 3)
	if err != nil || len(recent) != 3 {
		t.Fatalf("jobs %+v %v", recent, err)
	}
	// newest first
	if recent[0].Status != stats.JobFailed || !strings.Contains(recent[0].Result, "telegram is turned off") ||
		recent[1].Status != stats.JobDone || recent[2].Status != stats.JobFailed || !strings.Contains(recent[2].Result, "550") {
		t.Errorf("test jobs %+v", recent)
	}
	opts, _ := store.Options(ctx)
	if s := opts[notify.UserPrefix+admin+"_last_email"]; !strings.Contains(s, "|error|e-mail") && !strings.Contains(s, "|error|550") {
		t.Errorf("own status %q", s)
	}
	if s := opts["gamewe_notify_last_email"]; !strings.HasSuffix(s, "|ok") {
		t.Errorf("shared status %q", s)
	}

	// The same release is not told again, a new one of the web is.
	mails = nil
	jobs.lastUpdates = time.Time{}
	if err := store.SetOption(ctx, WebVersionKey, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	fail = ""
	latest.Version = "9.9.9"
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 0 {
		t.Errorf("told again: %+v", mails)
	}
	latest.Version = "10.0.0"
	jobs.lastUpdates = time.Time{}
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 2 || !strings.Contains(mails[0].title, "Collector 10.0.0") || !strings.Contains(mails[1].title, "Web 10.0.0") {
		t.Errorf("new releases: %+v", mails)
	}
	hub.Close(ctx)
}
