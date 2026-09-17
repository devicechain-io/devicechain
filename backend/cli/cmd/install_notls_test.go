// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
)

// runInstallWith parses argv through the REAL install flag set and runs the command's
// RunE up to its first refusal, restoring every flag it touched.
func runInstallWith(t *testing.T, argv ...string) error {
	t.Helper()
	names := []string{"no-tls", "compact", "max-connections", "skip-preflight", "dry-run", "no-monitoring"}
	saved := map[string]string{}
	for _, n := range names {
		f := installCmd.Flags().Lookup(n)
		if f == nil {
			t.Fatalf("dcctl install has no --%s flag", n)
		}
		saved[n] = f.Value.String()
	}
	t.Cleanup(func() {
		for n, v := range saved {
			if err := installCmd.Flags().Set(n, v); err != nil {
				t.Fatalf("restoring --%s: %v", n, err)
			}
			installCmd.Flags().Lookup(n).Changed = false
		}
	})
	if err := installCmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parsing %v: %v", argv, err)
	}
	return installCmd.RunE(installCmd, []string{"local"})
}

// 🔴 --no-tls ALONE DOES NOTHING TO A CLUSTER, so it is refused rather than accepted as
// though cert-manager had been left out.
func TestInstallRefusesNoTLSWithoutCompact(t *testing.T) {
	err := runInstallWith(t, "--no-tls")
	if err == nil || !strings.Contains(err.Error(), "--no-tls on dcctl install only takes effect with --compact") {
		t.Errorf("dcctl install --no-tls without --compact was not refused: %v", err)
	}
}

// The counterweight. Each run is stopped by the --max-connections refusal that follows,
// so reaching it proves the --no-tls check let the run through without touching a cluster.
func TestInstallAcceptsNoTLSWhereItMeansSomething(t *testing.T) {
	for _, argv := range [][]string{
		{"--compact", "--no-tls", "--max-connections=5"},
		{"--no-tls=false", "--max-connections=5"},
		{"--max-connections=5"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			err := runInstallWith(t, argv...)
			if err == nil || !strings.Contains(err.Error(), "--max-connections must be at least 100") {
				t.Errorf("dcctl install %v did not reach the checks after --no-tls: %v", argv, err)
			}
		})
	}
}
