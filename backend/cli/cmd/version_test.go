// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestDescribeCommit(t *testing.T) {
	tests := []struct {
		name      string
		commit    string
		treeState string
		want      string
	}{
		{
			name:      "clean tree abbreviates the hash",
			commit:    "611647ce3c1e849129a435f02d8acae92e9c9c71",
			treeState: "clean",
			want:      "611647ce3c1e",
		},
		{
			name:      "dirty tree is called out",
			commit:    "611647ce3c1e849129a435f02d8acae92e9c9c71",
			treeState: "dirty",
			want:      "611647ce3c1e (dirty: built with uncommitted changes)",
		},
		{
			// A `go build` that bypasses the makefile leaves the stamps empty.
			// Saying so is the honest answer; inventing one is not.
			name:      "no stamp says so rather than guessing",
			commit:    "",
			treeState: "",
			want:      "unknown (not built via the makefile)",
		},
		{
			name:      "unknown tree state does not annotate",
			commit:    "abc123",
			treeState: "",
			want:      "abc123",
		},
		{
			name:      "short hash is not truncated",
			commit:    "abc123",
			treeState: "clean",
			want:      "abc123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeCommit(tc.commit, tc.treeState); got != tc.want {
				t.Errorf("describeCommit(%q, %q) = %q, want %q",
					tc.commit, tc.treeState, got, tc.want)
			}
		})
	}
}

func TestDescribeBuildDate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		stamp string
		want  string
	}{
		{
			name:  "fresh build reports minutes",
			stamp: now.Add(-5 * time.Minute).Format(time.RFC3339),
			want:  "2026-07-16T11:55:00Z (5 minutes ago)",
		},
		{
			name:  "hours",
			stamp: now.Add(-3 * time.Hour).Format(time.RFC3339),
			want:  "2026-07-16T09:00:00Z (3 hours ago)",
		},
		{
			// The regression this command exists for: the dcctl found on PATH was
			// a 19-day-old build reporting a version identical to a fresh one.
			name:  "a weeks-old build is flagged stale",
			stamp: now.Add(-19 * 24 * time.Hour).Format(time.RFC3339),
			want:  "2026-06-27T12:00:00Z (19 days ago) — stale; consider rebuilding",
		},
		{
			name:  "just under the stale threshold is not flagged",
			stamp: now.Add(-6 * 24 * time.Hour).Format(time.RFC3339),
			want:  "2026-07-10T12:00:00Z (6 days ago)",
		},
		{
			name:  "exactly at the threshold is flagged",
			stamp: now.Add(-staleAfter).Format(time.RFC3339),
			want:  "2026-07-09T12:00:00Z (7 days ago) — stale; consider rebuilding",
		},
		{
			name:  "no stamp says so rather than guessing",
			stamp: "",
			want:  "unknown (not built via the makefile)",
		},
		{
			// A malformed stamp is a build-tooling bug; surface it.
			name:  "unparsable stamp is surfaced, not swallowed",
			stamp: "last tuesday",
			want:  "last tuesday (unparsable timestamp)",
		},
		{
			// Clock skew must not produce "-3 hours ago" or a huge bogus age.
			name:  "future build date omits a meaningless age",
			stamp: now.Add(1 * time.Hour).Format(time.RFC3339),
			want:  "2026-07-16T13:00:00Z",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeBuildDate(tc.stamp, now); got != tc.want {
				t.Errorf("describeBuildDate(%q) = %q, want %q", tc.stamp, got, tc.want)
			}
		})
	}
}

func TestHumanizeAge(t *testing.T) {
	tests := []struct {
		age  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{1 * time.Minute, "1 minute ago"},
		{2 * time.Minute, "2 minutes ago"},
		{59 * time.Minute, "59 minutes ago"},
		{1 * time.Hour, "1 hour ago"},
		{23 * time.Hour, "23 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{19 * 24 * time.Hour, "19 days ago"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := humanizeAge(tc.age); got != tc.want {
				t.Errorf("humanizeAge(%v) = %q, want %q", tc.age, got, tc.want)
			}
		})
	}
}

// The whole point of the version suffix is that two builds of the same VERSION
// are distinguishable. Assert the fields carry enough to tell them apart even
// when the version string cannot.
func TestVersionFieldsSurfaceBuildIdentity(t *testing.T) {
	fields := versionFields(time.Now())

	labels := make(map[string]string, len(fields))
	for _, f := range fields {
		labels[f.label] = f.value
	}

	for _, want := range []string{"commit", "built", "images", "go", "platform"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("version output is missing the %q field", want)
		}
	}

	// `images` must stay a bare image tag: it is fed to a registry lookup, so a
	// build stamp leaking into it would send bootstrap after an image that does
	// not exist.
	if strings.Contains(labels["images"], "-dev.") {
		t.Errorf("images tag %q carries a dev build stamp; it must remain a "+
			"resolvable published tag", labels["images"])
	}
}
