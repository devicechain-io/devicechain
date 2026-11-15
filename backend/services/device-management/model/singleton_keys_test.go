// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Lock bytes are a rolling-upgrade contract between old and new replicas.
func TestSingletonLockKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int64
	}{
		{agentEffectsDrainLockName, 2217645974197667389},
		{ownershipDrainLockName, 5481199707940735846},
		{sessionKickDrainLockName, 286895151743108058},
	} {
		if got := rdb.AdvisoryLockKey(tc.name); got != tc.want {
			t.Errorf("%s: key %d, want %d", tc.name, got, tc.want)
		}
	}
}
