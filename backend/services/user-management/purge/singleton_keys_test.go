// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

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
		{coordinatorLockName, 8988596906808030371},
	} {
		if got := rdb.AdvisoryLockKey(tc.name); got != tc.want {
			t.Errorf("%s: key %d, want %d", tc.name, got, tc.want)
		}
	}
}
