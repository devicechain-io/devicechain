// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	timeNow = func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }
	defer func() { timeNow = time.Now }()

	got, err := parseWhen("since", "6h")
	if err != nil || !got.Equal(time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("6h -> %v, %v", got, err)
	}
	got, err = parseWhen("since", "2026-09-04T00:00:00Z")
	if err != nil || !got.Equal(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("rfc3339 -> %v, %v", got, err)
	}
	for _, bad := range []string{"yesterday", "-1h", "2026-09-04"} {
		if _, err := parseWhen("since", bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// The command is registered under `events failed`, and its input is refused before any
// network call when it is wrong. Each case fails before the session is built.
func TestEventsFailedRefusesBadInput(t *testing.T) {
	reset := func() {
		evTenant, evEmail, evPassword = "acme", "a@b.c", "pw"
		evReason, evDevice, evSince, evUntil = "", "", "", ""
		evLimit, evSummary, evOutput = 100, false, "table"
		for _, n := range []string{"reason", "device", "until", "limit"} {
			if f := eventsFailedCmd.Flags().Lookup(n); f != nil {
				f.Changed = false
			}
		}
	}
	defer reset()
	cases := map[string]struct {
		mutate func()
		want   string
	}{
		"no tenant":  {func() { evTenant = "" }, "--tenant is required"},
		"no email":   {func() { evEmail = "" }, "--email and --password"},
		"bad format": {func() { evOutput = "yaml" }, "--output must be"},
		"bad limit":  {func() { evLimit = 0 }, "--limit must be"},
		"bad reason": {func() { evReason = "nope" }, "--reason"},
		"bad since":  {func() { evSince = "soon" }, "--since"},
		"bad until":  {func() { evUntil = "later" }, "--until"},
		"empty window": {func() {
			evSince, evUntil = "2026-11-02T00:00:00Z", "2026-11-01T00:00:00Z"
		}, "is later than --until"},
		"summary with a list filter": {func() {
			evSummary = true
			eventsFailedCmd.Flags().Lookup("device").Changed = true
		}, "does not apply to --summary"},
	}
	for name, c := range cases {
		reset()
		c.mutate()
		err := runEventsFailed(eventsFailedCmd, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v, want containing %q", name, err, c.want)
		}
	}
}

func TestEventsFailedIsRegistered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"events", "failed"})
	if err != nil || c != eventsFailedCmd {
		t.Fatalf("events failed not found: %v", err)
	}
}
