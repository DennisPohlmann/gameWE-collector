// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gamewe-collector/internal/notify"
	"gamewe-collector/internal/stats"
	"gamewe-collector/internal/update"
)

// WebRepo is the web frontend's GitHub repository (update notifications).
const WebRepo = "DennisPohlmann/gameWE-web"

// WebVersionKey is where the web writes its version at start.
const WebVersionKey = "gamewe_web_version"

// updateNotifyEvery is how often the releases are looked up for the
// update notifications (only while a target wants them).
const updateNotifyEvery = 6 * time.Hour

// notifyEvent sends the message of msg to every target that wants one of
// events and writes each outcome to its status key. It runs after the job
// is finished; a failure is only logged.
func (j *Jobs) notifyEvent(ctx context.Context, msg func(opts map[string]string) notify.Message, events ...string) {
	ctx = context.WithoutCancel(ctx) // a stop during a reset still tells about it
	opts, err := j.Store.Options(ctx)
	if err != nil {
		j.Log.Warn("notify: reading the settings failed", "err", err)
		return
	}
	targets := notify.Wanted(opts, events...)
	if len(targets) == 0 {
		return
	}
	for _, t := range targets {
		j.sendTo(ctx, opts, t, msg(notify.For(opts, t)))
	}
}

// sendTo sends m to t and writes the outcome to its status key.
func (j *Jobs) sendTo(ctx context.Context, opts map[string]string, t notify.Target, m notify.Message) error {
	err := j.Notify.Send(ctx, opts, t, m)
	if err != nil {
		j.Log.Warn("notify: sending failed", "channel", t.Channel, "own", t.User != "", "err", err)
	}
	if serr := j.Store.SetOption(ctx, t.StatusKey(), notify.Status(j.now(), err)); serr != nil {
		j.Log.Warn("notify: storing the status failed", "err", serr)
	}
	return err
}

// notifyTest sends the test message of a notify-test job: to the shared
// target of channel mode, or to the own address of the admin game (hash).
func (j *Jobs) notifyTest(ctx context.Context, job stats.Job) (string, error) {
	opts, err := j.Store.Options(ctx)
	if err != nil {
		return "", err
	}
	t, err := notify.Find(opts, strings.TrimSpace(job.Game), job.Mode)
	if err != nil {
		return "", err
	}
	if err := j.sendTo(ctx, opts, t, notify.TestMessage(notify.For(opts, t), t.Channel)); err != nil {
		return "", err
	}
	return "sent by " + t.Channel, nil
}

// resetMessage tells about a reset job of an admin, the API or the CLI.
func (j *Jobs) resetMessage(ctx context.Context, job stats.Job) func(map[string]string) notify.Message {
	name := j.Store.GameName(ctx, job.Game)
	return func(opts map[string]string) notify.Message {
		return notify.ResetMessage(opts, job.Game, name, job.RequestedBy)
	}
}

// resetVars are the placeholders of the reset webhooks.
func (j *Jobs) resetVars(ctx context.Context, game, by string) func(map[string]string) map[string]string {
	name := j.Store.GameName(ctx, game)
	return func(opts map[string]string) map[string]string {
		return notify.WebhookVars(opts, notify.EventReset, game, name, notify.ResetMessage(opts, game, name, by), j.now(), "by", by)
	}
}

// hallVars are the placeholders of the hall of fame webhooks.
func (j *Jobs) hallVars(ctx context.Context, game string, res stats.ScheduledResult) func(map[string]string) map[string]string {
	msg := j.hallMessage(ctx, game, res)
	name := j.Store.GameName(ctx, game)
	return func(opts map[string]string) map[string]string {
		top := make([]notify.Place, len(res.Top))
		for i, e := range res.Top {
			top[i] = notify.Place{Place: e.Place, Name: e.Name, Points: e.Skill}
		}
		return notify.WebhookVars(opts, notify.EventHall, game, name, msg(opts), j.now(), notify.HallVars(res.From, res.To, top)...)
	}
}

// updateVars are the placeholders of the update webhooks.
func (j *Jobs) updateVars(component, version, current, url string) func(map[string]string) map[string]string {
	return func(opts map[string]string) map[string]string {
		m := notify.UpdateMessage(opts, component, version, current, url)
		return notify.WebhookVars(opts, notify.EventUpdate, "", "", m, j.now(), "component", component, "version", version, "current", current, "release_url", url)
	}
}

// fireWebhooks calls the switched-on webhooks of event with the
// placeholders of vars (the texts in the site's language) and stores each
// outcome. A failure is only logged.
func (j *Jobs) fireWebhooks(ctx context.Context, event string, vars func(opts map[string]string) map[string]string) {
	ctx = context.WithoutCancel(ctx)
	hooks, err := j.Store.Webhooks(ctx, event)
	if err != nil {
		j.Log.Debug("notify: reading the webhooks failed", "err", err) // an older schema has none
		return
	}
	if len(hooks) == 0 {
		return
	}
	opts, err := j.Store.Options(ctx)
	if err != nil {
		j.Log.Warn("notify: reading the settings failed", "err", err)
		return
	}
	v := vars(notify.For(opts, notify.Target{}))
	for _, w := range hooks {
		j.callWebhook(ctx, w, v)
	}
}

// callWebhook calls w and writes the outcome to its last_status.
func (j *Jobs) callWebhook(ctx context.Context, w notify.Webhook, vars map[string]string) (int, error) {
	status, err := j.Notify.SendWebhook(ctx, w, vars)
	if err != nil {
		j.Log.Warn("notify: webhook failed", "webhook", w.ID, "event", vars["event"], "err", err)
	}
	if serr := j.Store.SetWebhookStatus(ctx, w.ID, notify.WebhookStatus(j.now(), status, err)); serr != nil {
		j.Log.Warn("notify: storing the webhook status failed", "err", serr)
	}
	return status, err
}

// webhookTest calls the webhook of a webhook-test job (game: its id) with
// sample values ({test} is 1), once for each of its events, and stops at
// the first failure.
func (j *Jobs) webhookTest(ctx context.Context, job stats.Job) (string, error) {
	w, err := j.Store.Webhook(ctx, strings.TrimSpace(job.Game))
	if err != nil {
		return "", err
	}
	opts, err := j.Store.Options(ctx)
	if err != nil {
		return "", err
	}
	opts = notify.For(opts, notify.Target{})
	now := j.now()
	var out []string
	for _, e := range w.Events {
		var v map[string]string
		switch e {
		case notify.EventHall:
			from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
			to := from.AddDate(0, 1, 0)
			top := []notify.Place{{Place: 1, Name: "Player 1", Points: 1500}, {Place: 2, Name: "Player 2", Points: 1400}, {Place: 3, Name: "Player 3", Points: 1300}}
			v = notify.WebhookVars(opts, e, "test", "Test", notify.HallMessage(opts, "test", "Test", from, to, top), now, notify.HallVars(from, to, top)...)
		case notify.EventUpdate:
			v = j.updateVars("collector", j.Version, j.Version, "")(opts)
		default:
			v = notify.WebhookVars(opts, e, "test", "Test", notify.ResetMessage(opts, "test", "Test", job.RequestedBy), now, "by", job.RequestedBy)
		}
		v["test"] = "1"
		status, err := j.callWebhook(ctx, w, v)
		if err != nil {
			if len(w.Events) > 1 {
				err = fmt.Errorf("%s: %w", e, err)
			}
			return "", err
		}
		out = append(out, "HTTP "+strconv.Itoa(status))
		if len(w.Events) > 1 {
			out[len(out)-1] = e + ": " + out[len(out)-1]
		}
	}
	return strings.Join(out, ", "), nil
}

// hallMessage tells about a scheduled reset that stored a hall of fame.
func (j *Jobs) hallMessage(ctx context.Context, game string, res stats.ScheduledResult) func(map[string]string) notify.Message {
	name := j.Store.GameName(ctx, game)
	top := make([]notify.Place, len(res.Top))
	for i, e := range res.Top {
		top[i] = notify.Place{Place: e.Place, Name: e.Name, Points: e.Skill}
	}
	return func(opts map[string]string) notify.Message {
		return notify.HallMessage(opts, game, name, res.From, res.To, top)
	}
}

// notifyUpdates looks up the latest releases of the collector and the web
// and tells each new one once (SeenKey), when a target wants updates and
// gamewe_update_check is not 0. Without a known running version (an older
// web writes none) the first release seen is only remembered.
func (j *Jobs) notifyUpdates(ctx context.Context) error {
	opts, err := j.Store.Options(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts["gamewe_update_check"]) == "0" {
		return nil
	}
	if len(notify.Wanted(opts, notify.EventUpdate)) == 0 {
		if hooks, err := j.Store.Webhooks(ctx, notify.EventUpdate); err != nil || len(hooks) == 0 {
			return nil
		}
	}
	var errs []error
	for _, c := range []struct{ name, repo, current string }{
		{"collector", update.Repo, j.Version},
		{"web", WebRepo, strings.TrimSpace(opts[WebVersionKey])},
	} {
		rel, err := j.latest(ctx, c.repo)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.name, err))
			continue
		}
		seen := strings.TrimSpace(opts[notify.SeenKey(c.name)])
		if rel.Version == seen {
			continue
		}
		known := c.current != "" && c.current != "dev"
		tell := (known && update.Newer(rel.Version, c.current)) || (!known && seen != "")
		if tell {
			for _, t := range notify.Wanted(opts, notify.EventUpdate) {
				j.sendTo(ctx, opts, t, notify.UpdateMessage(notify.For(opts, t), c.name, rel.Version, c.current, rel.URL))
			}
			j.fireWebhooks(ctx, notify.EventUpdate, j.updateVars(c.name, rel.Version, c.current, rel.URL))
		}
		if err := j.Store.SetOption(ctx, notify.SeenKey(c.name), rel.Version); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (j *Jobs) latest(ctx context.Context, repo string) (update.Release, error) {
	if j.Latest != nil {
		return j.Latest(ctx, repo)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return update.Latest(ctx, update.Client(), repo)
}
