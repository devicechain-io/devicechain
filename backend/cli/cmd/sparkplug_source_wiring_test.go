// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

var someSources = []bootstrap.SparkplugSource{{Tenant: "plant", Broker: bootstrap.SparkplugBroker{URL: "tcp://b:1883"}}}

// --sparkplug-source exists on both verbs that can carry it, and the parsed sources reach what the
// pipeline starts from. A deleted copy would leave the flag accepted and inert: the source
// parsed, validated, and never rendered.
func TestSparkplugSourceReachesTheBootstrapAndUpgradeOptions(t *testing.T) {
	if bootstrapCmd.Flags().Lookup("sparkplug-source") == nil {
		t.Fatal("bootstrap has no --sparkplug-source")
	}
	if upgradeCmd.Flags().Lookup("sparkplug-source") == nil {
		t.Fatal("upgrade has no --sparkplug-source")
	}

	bootstrapSparkplugSources = someSources
	t.Cleanup(func() { bootstrapSparkplugSources = nil })
	opts := bootstrapOptions("prod", nil, nil)
	if len(opts.SparkplugSources) != 1 {
		t.Fatalf("bootstrap options lost the sources: %v", opts.SparkplugSources)
	}
	st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, nil, false, bootstrap.EscrowPlan{})
	if len(st.SparkplugSources) != 1 || st.SparkplugSources[0].Tenant != "plant" {
		t.Fatalf("the bootstrap state lost the sources: %v", st.SparkplugSources)
	}

	upgradeSparkplugSources = someSources
	t.Cleanup(func() { upgradeSparkplugSources = nil })
	if up := upgradeOptions("prod", nil, nil); len(up.SparkplugSources) != 1 {
		t.Fatalf("upgrade options lost the sources: %v", up.SparkplugSources)
	}
	upgradeSparkplugSources = nil
	if up := upgradeOptions("prod", nil, nil); up.SparkplugSources != nil || up.WithdrawSparkplugSources {
		t.Fatalf("an upgrade given no file carries sources: %v", up.SparkplugSources)
	}

	// The withdrawal reaches both options, and decides the sources on the state even with none.
	if bootstrapCmd.Flags().Lookup("withdraw-sparkplug-sources") == nil || upgradeCmd.Flags().Lookup("withdraw-sparkplug-sources") == nil {
		t.Fatal("--withdraw-sparkplug-sources is missing from a verb")
	}
	bootstrapWithdrawSparkplug, upgradeWithdrawSparkplug = true, true
	t.Cleanup(func() { bootstrapWithdrawSparkplug, upgradeWithdrawSparkplug = false, false })
	if !upgradeOptions("prod", nil, nil).WithdrawSparkplugSources {
		t.Fatal("upgrade options lost the withdrawal")
	}
	bootstrapSparkplugSources = nil
	bopts := bootstrapOptions("prod", nil, nil)
	if !bopts.WithdrawSparkplugSources {
		t.Fatal("bootstrap options lost the withdrawal")
	}
	st = bootstrapStateFor(bopts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, nil, false, bootstrap.EscrowPlan{})
	if !st.SparkplugSourcesGiven || len(st.SparkplugSources) != 0 {
		t.Fatalf("a withdrawing bootstrap must decide the sources (given=%v)", st.SparkplugSourcesGiven)
	}
}
