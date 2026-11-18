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
		{sweepLockName, -3785425726666926856},
		{reconcileLockName, -4004865937300204268},
		{strandedLockName, -5918301416751400144},
	} {
		if got := rdb.AdvisoryLockKey(tc.name); got != tc.want {
			t.Errorf("%s: key %d, want %d", tc.name, got, tc.want)
		}
	}
}
