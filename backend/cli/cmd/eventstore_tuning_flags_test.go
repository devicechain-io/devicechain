// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "testing"

// The two upgrade flags reach the options as pointers: nil means "keep what the instance has",
// and a zero is a value ("derive it again"), so a given 0 must not read as absent.
func TestUpgradeEventStoreFlagsReachTheOptions(t *testing.T) {
	defer func() {
		upgradeEventStoreMaxWAL, upgradeEventStoreMaxWALGiven = 0, false
		upgradeEventStoreCheckpoint, upgradeEventStoreCheckpointGiven = "", false
	}()
	if up := upgradeOptions("prod", nil, nil); up.EventStoreMaxWALMiB != nil || up.EventStoreCheckpointTimeout != nil {
		t.Fatal("flagless upgrade carries event store tuning")
	}
	upgradeEventStoreMaxWALGiven, upgradeEventStoreMaxWAL = true, 0
	upgradeEventStoreCheckpointGiven, upgradeEventStoreCheckpoint = true, "10min"
	up := upgradeOptions("prod", nil, nil)
	if up.EventStoreMaxWALMiB == nil || *up.EventStoreMaxWALMiB != 0 {
		t.Fatalf("a given 0 was lost: %v", up.EventStoreMaxWALMiB)
	}
	if up.EventStoreCheckpointTimeout == nil || *up.EventStoreCheckpointTimeout != "10min" {
		t.Fatalf("checkpoint timeout = %v", up.EventStoreCheckpointTimeout)
	}
	if upgradeCmd.Flags().Lookup("event-store-max-wal-mib") == nil || upgradeCmd.Flags().Lookup("event-store-checkpoint-timeout") == nil {
		t.Fatal("flags are not registered on upgrade")
	}
}
