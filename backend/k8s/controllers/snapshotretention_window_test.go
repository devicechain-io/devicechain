// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestParseRetentionWindow(t *testing.T) {
	day := 24 * time.Hour
	for in, want := range map[string]time.Duration{
		"":    0,
		"7d":  7 * day,
		"30d": 30 * day,
		"2w":  14 * day,
		// MONTHS, as in the ObjectStore, at 31 days: never shorter than barman's
		// reading of the same string.
		"1m": 31 * day,
		"3m": 93 * day,
		"1h": time.Hour,
	} {
		got, err := ParseRetentionWindow(in)
		if err != nil || got != want {
			t.Errorf("ParseRetentionWindow(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// Each of these is refused, never read as some default: a guessed window
	// deletes what someone meant to keep.
	for _, in := range []string{"0d", "7", "7x", "-1d", "d", "7 d", "07d", "1y", "30min", "seven", "7D"} {
		if got, err := ParseRetentionWindow(in); err == nil {
			t.Errorf("ParseRetentionWindow(%q) = %v, want an error", in, got)
		}
	}
}

func TestSelectForPruning(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	week := 7 * day
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	done := func(name string, stopped time.Time) backupView {
		return backupView{Name: name, Phase: backupPhaseCompleted, Created: stopped.Add(-time.Minute), StoppedAt: stopped}
	}
	in := func(name, phase string, created time.Time) backupView {
		return backupView{Name: name, Phase: phase, Created: created}
	}

	for _, tc := range []struct {
		name    string
		window  time.Duration
		backups []backupView
		deleted []string
	}{{
		name:   "keeps the window and the newest base before it",
		window: week,
		backups: []backupView{
			done("d1", ago(day)), done("d6", ago(6*day)), done("d8", ago(8*day)),
			done("d9", ago(9*day)), done("d30", ago(30*day)),
		},
		// d8 is the base the window's first moment replays from.
		deleted: []string{"d30", "d9"},
	}, {
		name:   "one finished exactly at the start of the window is inside it",
		window: week,
		backups: []backupView{
			done("edge", ago(week)), done("d8", ago(8*day)), done("d9", ago(9*day)),
		},
		deleted: []string{"d9"},
	}, {
		name:    "nothing inside the window: the newest before it stays, alone",
		window:  week,
		backups: []backupView{done("d20", ago(20*day)), done("d10", ago(10*day))},
		deleted: []string{"d20"},
	}, {
		name:   "failed and invalid ones go once created before the window",
		window: week,
		backups: []backupView{
			in("failed-old", backupPhaseFailed, ago(8*day)),
			in("failed-new", backupPhaseFailed, ago(2*day)),
			in("invalid-old", backupPhaseInvalid, ago(8*day)),
		},
		deleted: []string{"failed-old", "invalid-old"},
	}, {
		name:   "a backup still in flight is never touched, however old",
		window: week,
		backups: []backupView{
			in("running", "running", ago(30*day)),
			in("pending", "pending", ago(30*day)),
			in("finalizing", "finalizing", ago(30*day)),
			in("started", "started", ago(30*day)),
			in("archiving", "walArchivingFailing", ago(30*day)),
		},
	}, {
		name:    "a completed one with no stop time is not treated as old",
		window:  week,
		backups: []backupView{{Name: "no-stop", Phase: backupPhaseCompleted, Created: ago(30 * day)}},
	}, {
		name:    "no window keeps everything",
		window:  0,
		backups: []backupView{done("d30", ago(30*day)), done("d60", ago(60*day)), in("f", backupPhaseFailed, ago(60*day))},
	}, {
		name:   "a month window keeps 30.5 days",
		window: 31 * day,
		backups: []backupView{
			done("d30.5", ago(30*day+12*time.Hour)), done("d32", ago(32*day)), done("d33", ago(33*day)),
		},
		deleted: []string{"d33"},
	}, {
		name: "empty",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, b := range selectForPruning(tc.backups, tc.window, now) {
				got = append(got, b.Name)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.deleted...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("deleted %v, want %v", got, want)
			}
		})
	}
}
