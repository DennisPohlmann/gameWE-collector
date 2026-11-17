// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package collector

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gamewe-collector/internal/notify"
	"gamewe-collector/internal/stats"
)

// A scheduled reset calls the hall of fame and the reset webhooks, a
// manual reset only the reset one, a switched-off webhook none; each
// outcome lands in last_status; a test job calls a webhook with sample
// values and reports the status.
func TestIntegrationWebhooks(t *testing.T) {
	db := testDB(t)
	hub, store, _ := resetFixture(t, db)
	flush(t, hub)
	ctx := context.Background()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		if r.URL.Path == "/reset" {
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	for _, q := range []string{
		`INSERT INTO hlstats_gamewe_webhooks (id, name, event, enabled, method, url, headers, payload, expect_status, allow_private) VALUES
			('h1', 'Hall', 'hof', 1, 'POST', '` + srv.URL + `/hof', 'Authorization: Bearer tok_1234
Content-Type: application/json', '{"r":"{ranking}","p1":"{place1_name}","to":"{to}","test":{test}}', 0, 1),
			('r1', 'Reset', 'reset', 1, 'PUT', '` + srv.URL + `/reset', '', '{ranking} by {by}', 202, 1),
			('off', 'Off', 'reset', 0, 'POST', '` + srv.URL + `/off', '', 'x', 0, 1),
			('m1', 'Many', 'hof,reset', 1, 'POST', '` + srv.URL + `/many', '', '{event} {event_label} {by}{place1_name}', 0, 1)`,
		`INSERT INTO hlstats_Options (keyname, value) VALUES ('` + stats.ScheduleKey("css") + `', 'month')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	loc := time.UTC
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, loc)
	jobs := &Jobs{Store: store, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Zone: loc, Now: func() time.Time { return now }, Notify: &notify.Sender{}}
	if err := jobs.QueueScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 1, 0, 0, 30, 0, loc)
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[0] != `/hof Bearer tok_1234 {"r":"Counter-Strike: Source","p1":"A","to":"2026-10-31","test":0}` || calls[1] != "/many  hof Hall of fame stored A" ||
		calls[2] != "/many  reset Ranking reset schedule" || calls[3] != "/reset  Counter-Strike: Source by schedule" {
		t.Fatalf("scheduled: %q", calls)
	}
	calls = nil
	if _, err := store.AddJob(ctx, stats.JobReset, "tf", stats.ResetPlayerStats, "dennis", stats.JobOrigin{Feature: stats.OriginAdmin}, now); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "/many  reset Ranking reset dennis" || calls[1] != "/reset  Team Fortress 2 by dennis" {
		t.Fatalf("reset: %q", calls)
	}
	var last string
	if err := db.QueryRow(`SELECT last_status FROM hlstats_gamewe_webhooks WHERE id = 'h1'`).Scan(&last); err != nil || !strings.HasSuffix(last, "|ok|200") {
		t.Errorf("status %q %v", last, err)
	}

	// The test: sample values; the reset one answers 200 instead of 202.
	calls = nil
	for _, id := range []string{"h1", "r1", "gone", "m1"} {
		if _, err := store.AddJob(ctx, stats.JobWebhookTest, id, "webhook", "dennis", stats.JobOrigin{Feature: stats.OriginAdmin}, now); err != nil {
			t.Fatal(err)
		}
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.URL.Path+" "+string(b))
	})
	if err := jobs.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	recent, err := store.RecentJobs(ctx, 4)
	if err != nil || len(recent) != 4 {
		t.Fatalf("jobs %+v %v", recent, err)
	}
	if recent[0].Status != stats.JobDone || recent[0].Result != "hof: HTTP 200, reset: HTTP 200" ||
		recent[1].Status != stats.JobFailed || !strings.Contains(recent[1].Result, "no such webhook") ||
		recent[2].Status != stats.JobFailed || !strings.Contains(recent[2].Result, "HTTP 200, expected 202") ||
		recent[3].Status != stats.JobDone || recent[3].Result != "HTTP 200" {
		t.Errorf("test jobs %+v", recent)
	}
	// The test of a webhook of two events calls it once for each.
	if len(calls) != 4 || !strings.Contains(calls[0], `"p1":"Player 1"`) || !strings.Contains(calls[0], `"test":1`) ||
		calls[2] != "/many hof Hall of fame stored Player 1" || calls[3] != "/many reset Ranking reset dennis" {
		t.Errorf("test calls %q", calls)
	}
	if err := db.QueryRow(`SELECT last_status FROM hlstats_gamewe_webhooks WHERE id = 'r1'`).Scan(&last); err != nil || !strings.Contains(last, "|error|webhook: HTTP 200, expected 202") {
		t.Errorf("status %q %v", last, err)
	}
	hub.Close(ctx)
}
