// SPDX-License-Identifier: GPL-3.0-or-later

package collector

import (
	"context"
	"errors"
	"fmt"
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
	if strings.TrimSpace(opts["gamewe_update_check"]) == "0" || len(notify.Wanted(opts, notify.EventUpdate)) == 0 {
		return nil
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
