// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// --compact stays a permanent alias: every spelling of it, alone or with --size compact, resolves to
// the same cluster, and the flag works exactly as it did before --size existed.
func TestResolveInstallSize(t *testing.T) {
	cases := []struct {
		name    string
		compact bool
		size    string
		want    bool
		refused string
	}{
		{"neither", false, "", false, ""},
		{"--compact alone", true, "", true, ""},
		{"--size compact", false, "compact", true, ""},
		{"both agree", true, "compact", true, ""},
		{"--size base", false, "base", false, ""},
		{"--compact contradicts --size base", true, "base", false, "contradict"},
		{"perf is per instance", false, "perf-12k", false, "dcctl bootstrap --size perf-12k"},
		{"--compact with perf", true, "perf-12k", false, "dcctl bootstrap --size"},
		{"unknown", false, "huge", false, "unknown size"},
		{"perf-20k is a target, not a size", false, "perf-20k", false, "unknown size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveInstallSize(tc.compact, tc.size)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("err %v, want a refusal containing %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("compact %v, want %v", got, tc.want)
			}
		})
	}
}

// --size reaches the bootstrap State, and the flags exist on both verbs with the compat alias intact.
func TestSizeFlagIsWired(t *testing.T) {
	old := bootstrapSize
	t.Cleanup(func() { bootstrapSize = old })
	bootstrapSize = "perf-12k"
	opts := bootstrapOptions("i1", nil, nil)
	if opts.Size != "perf-12k" {
		t.Fatalf("bootstrapOptions dropped --size: %q", opts.Size)
	}
	st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, nil, false, bootstrap.EscrowPlan{})
	if st.Size != "perf-12k" {
		t.Errorf("bootstrapStateFor dropped the size: %q", st.Size)
	}
	if bootstrapCmd.Flags().Lookup("size") == nil || installCmd.Flags().Lookup("size") == nil {
		t.Error("--size is missing from install or bootstrap")
	}
	if installCmd.Flags().Lookup("compact") == nil {
		t.Error("--compact was removed: it is a permanent alias")
	}
	if bootstrapCmd.Flags().Lookup("size").Usage == "" || strings.Contains(sizeUsage, "perf-20k") {
		t.Error("--size help is empty or names an unshipped size")
	}
}
