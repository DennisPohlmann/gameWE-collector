// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"strings"
	"testing"
	"time"
)

// The origin of a job: the feature goes into its own column, the details
// as JSON with only the fields that apply; a broken detail loses only the
// details.
func TestJobOrigin(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	o := JobOrigin{Feature: OriginScheduledResets, Startup: true, Schedule: "3 months", Inherited: true, Zone: "Europe/Berlin",
		From: time.Date(2026, 10, 1, 0, 0, 0, 0, loc).Unix(), To: time.Date(2027, 1, 1, 0, 0, 0, 0, loc).Unix(), Version: "1.4.0"}
	d := o.Detail()
	if strings.Contains(d, OriginScheduledResets) || strings.Contains(d, "command") || strings.Contains(d, "token") {
		t.Errorf("detail %s", d)
	}
	if got := ParseJobOrigin(OriginScheduledResets, d); got != o {
		t.Errorf("round trip %+v, want %+v", got, o)
	}
	if s := o.Summary(); s != "scheduled_resets, period 2026-10-01 - 2026-12-31, at the collector's start" {
		t.Errorf("summary %q", s)
	}
	if d := (JobOrigin{Feature: OriginAdmin}).Detail(); d != "" {
		t.Errorf("detail without details %q", d)
	}
	if got := ParseJobOrigin(OriginCLI, "{broken"); got != (JobOrigin{Feature: OriginCLI}) {
		t.Errorf("broken detail %+v", got)
	}
	if got := ParseJobOrigin("", ""); got.Summary() != "-" {
		t.Errorf("old job %q", got.Summary())
	}
	cli := JobOrigin{Feature: OriginCLI, Command: "gamewe reset css -mode all"}
	if s := cli.Summary(); s != "cli, gamewe reset css -mode all" {
		t.Errorf("cli summary %q", s)
	}
	if s := (JobOrigin{Feature: OriginDailyAwards, Day: "2026-10-26"}).Summary(); s != "daily_awards, day 2026-10-26" {
		t.Errorf("awards summary %q", s)
	}
}
