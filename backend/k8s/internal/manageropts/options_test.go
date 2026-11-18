// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package manageropts

import (
	"flag"
	"testing"
)

func TestManagerFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "default"},
		{name: "enabled", args: []string{"--leader-elect"}, want: true},
		{name: "explicit true", args: []string{"--leader-elect=true"}, want: true},
		{name: "disabled", args: []string{"--leader-elect=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			opts := BindManagerFlags(flags)
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if opts.LeaderElection != tc.want {
				t.Errorf("LeaderElection = %v, want %v", opts.LeaderElection, tc.want)
			}
			if opts.LeaderElectionID != "27be0819.devicechain.io" {
				t.Errorf("unexpected election identity: %q", opts.LeaderElectionID)
			}
			if !opts.LeaderElectionReleaseOnCancel {
				t.Error("the lease must be released on a clean shutdown")
			}
			if opts.LeaderElectionNamespace != "" {
				t.Errorf("unexpected election namespace override: %q", opts.LeaderElectionNamespace)
			}
			if opts.Metrics.BindAddress != ":8080" || opts.HealthProbeBindAddress != ":8081" {
				t.Errorf("unexpected default endpoint addresses: metrics=%q, probe=%q", opts.Metrics.BindAddress, opts.HealthProbeBindAddress)
			}
		})
	}
}

func TestManagerEndpointFlags(t *testing.T) {
	flags := flag.NewFlagSet("endpoints", flag.ContinueOnError)
	opts := BindManagerFlags(flags)
	if err := flags.Parse([]string{"--metrics-bind-address=0", "--health-probe-bind-address=:9091"}); err != nil {
		t.Fatal(err)
	}
	if opts.Metrics.BindAddress != "0" || opts.HealthProbeBindAddress != ":9091" {
		t.Errorf("endpoint flags did not reach manager options: metrics=%q, probe=%q", opts.Metrics.BindAddress, opts.HealthProbeBindAddress)
	}
}
