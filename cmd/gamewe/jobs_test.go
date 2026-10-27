// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"slices"
	"strings"
	"testing"
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
