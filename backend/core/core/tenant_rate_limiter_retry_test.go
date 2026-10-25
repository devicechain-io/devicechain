// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// RetryAfter is one token at the tenant's own rate, never below a second, and it does not
// spend anything.
func TestTenantRateLimiterRetryAfterFollowsTheTenantsRate(t *testing.T) {
	l := NewTenantRateLimiter(func(tenant string) TenantCeiling {
		switch tenant {
		case "slow":
			return TenantCeiling{RatePerSecond: 0.1, Burst: 1}
		case "none":
			return TenantCeiling{RatePerSecond: 0, Burst: 1}
		}
		return TenantCeiling{RatePerSecond: 20, Burst: 1}
	})
	assert.Equal(t, 10*time.Second, l.RetryAfter("slow"))
	assert.Equal(t, time.Second, l.RetryAfter("fast"), "a fast tenant still waits the one-second floor")
	assert.Equal(t, time.Second, l.RetryAfter("none"))
	assert.True(t, l.Allow("slow"), "RetryAfter must not have spent the tenant's token")
}
