// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// On a database without server_id (an older schema) the event
// inserts leave the column out; with it they keep it.
func TestEventServerIDFallback(t *testing.T) {
	st := step{`INSERT INTO hlstats_Events_Entries (timestamp_id, game_id, server_id, map, playerId) VALUES (?, ?, ?, ?, ?)`,
		[]any{int64(10), 1, int64(7), 3, int64(42)}}
	for _, has := range []bool{true, false} {
		s := &Store{cache: map[string]cached{"hlstats_Events_Frags.server_id": {has, time.Now()}}}
		got := s.ev(context.Background(), st)
		want := st
		if !has {
			want = step{`INSERT INTO hlstats_Events_Entries (timestamp_id, game_id, map, playerId) VALUES (?, ?, ?, ?)`,
				[]any{int64(10), 1, 3, int64(42)}}
		}
		if got.q != want.q || !reflect.DeepEqual(got.args, want.args) {
			t.Errorf("server_id %v: %q %v", has, got.q, got.args)
		}
	}
}
