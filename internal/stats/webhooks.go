// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"gamewe-collector/internal/notify"
)

const webhookCols = `id, name, event, enabled, method, url, headers, payload, expect_status, allow_private`

// Webhooks are the switched-on webhooks of event (hlstats_gamewe_webhooks:
// event holds one or more, "hof,reset"), by id.
func (s *Store) Webhooks(ctx context.Context, event string) ([]notify.Webhook, error) {
	rows, err := s.query(ctx, `SELECT `+webhookCols+` FROM hlstats_gamewe_webhooks WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notify.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		if slices.Contains(w.Events, event) {
			out = append(out, w)
		}
	}
	return out, rows.Err()
}

// Webhook is one webhook by id, also a switched-off one (its test).
func (s *Store) Webhook(ctx context.Context, id string) (notify.Webhook, error) {
	w, err := scanWebhook(s.queryRow(ctx, `SELECT `+webhookCols+` FROM hlstats_gamewe_webhooks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, errors.New("no such webhook")
	}
	return w, err
}

// SetWebhookStatus writes the outcome of the last call of a webhook.
func (s *Store) SetWebhookStatus(ctx context.Context, id, status string) error {
	_, err := s.exec(ctx, `UPDATE hlstats_gamewe_webhooks SET last_status = ? WHERE id = ?`, status, id)
	return err
}

func scanWebhook(r interface{ Scan(...any) error }) (notify.Webhook, error) {
	var w notify.Webhook
	var enabled, private int
	var events string
	err := r.Scan(&w.ID, &w.Name, &events, &enabled, &w.Method, &w.URL, &w.Headers, &w.Payload, &w.Expect, &private)
	w.Events, w.Enabled, w.AllowPrivate = notify.SplitEvents(events), enabled == 1, private == 1
	w.Method = strings.ToUpper(strings.TrimSpace(w.Method))
	return w, err
}
