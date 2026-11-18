// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/pflag"
)

func TestEpisodeFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		manifest string
		args     []string
		bad      bool
	}{
		{"towerpulse", nil, false}, {"sitepulse", nil, false},
		{"towerpulse", []string{"--episode", "night-shift"}, false},
		{"towerpulse", []string{"--episode", "classic"}, false},
		{"towerpulse", []string{"--episode", "typo"}, true},
		{"towerpulse", []string{"--episode="}, true},
		{"sitepulse", []string{"--episode", "night-shift"}, true},
		{"sitepulse", []string{"--episode", "classic"}, true},
		{"sitepulse", []string{"--episode="}, true},
	} {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		registerEpisodeFlag(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		_, err := resolveEpisode(fs, tc.manifest)
		if (err != nil) != tc.bad {
			t.Errorf("%s %v: %v", tc.manifest, tc.args, err)
		}
	}
	if simCreateCmd.Flags().Lookup("episode") == nil {
		t.Fatal("create lacks episode flag")
	}
}
func TestSimWindowsOutput(t *testing.T) {
	epoch := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	rec := &sim.Record{Name: "night", Episode: "night-shift", StartedAt: &epoch, Windows: []sim.EpisodeWindow{{Role: "INCIDENT", Start: epoch.Add(45 * time.Minute), End: epoch.Add(95 * time.Minute)}, {Role: "NORMAL", Start: epoch.Add(105 * time.Minute), End: epoch.Add(160 * time.Minute)}}}
	var out bytes.Buffer
	if err := printSimWindows(&out, rec); err != nil {
		t.Fatal(err)
	}
	want := "INCIDENT\t2026-11-02T00:45:00Z\t2026-11-02T01:35:00Z\nNORMAL\t2026-11-02T01:45:00Z\t2026-11-02T02:40:00Z\n"
	if out.String() != want {
		t.Fatalf("%q != %q", out.String(), want)
	}
	for _, old := range []*sim.Record{{Name: "old"}, {Name: "new", Episode: "night-shift"}} {
		if err := printSimWindows(&out, old); err == nil {
			t.Errorf("missing windows accepted: %+v", old)
		}
	}
}
