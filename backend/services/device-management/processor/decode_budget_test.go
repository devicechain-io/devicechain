// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/require"
)

type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock           { return &fakeClock{t: time.Date(2026, 11, 6, 12, 0, 30, 0, time.UTC)} }
func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestTheRecordBudgetAdmitsABurstOf20ThenRefuses(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	admitted := 0
	for i := 0; i < 100; i++ {
		if b.AllowRecord("acme", 7) {
			admitted++
		}
	}
	require.Equal(t, DecodeRecordBurst, admitted, "a broken decoder's flood is cut to the burst")
}

func TestTheRecordBudgetRefillsTenAMinute(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	for i := 0; i < DecodeRecordBurst; i++ {
		require.True(t, b.AllowRecord("acme", 7))
	}
	require.False(t, b.AllowRecord("acme", 7))

	c.add(5 * time.Second) // 0.83 of a token
	require.False(t, b.AllowRecord("acme", 7))
	c.add(time.Second + 100*time.Millisecond) // past 6 s: one token
	require.True(t, b.AllowRecord("acme", 7))
	require.False(t, b.AllowRecord("acme", 7))

	c.add(time.Minute) // ten more
	n := 0
	for b.AllowRecord("acme", 7) {
		n++
	}
	require.Equal(t, DecodeRecordRefillPerMinute, n)

	c.add(time.Hour) // never above the burst
	n = 0
	for b.AllowRecord("acme", 7) {
		n++
	}
	require.Equal(t, DecodeRecordBurst, n)
}

func TestTheRecordBudgetIsPerTenantAndDeviceType(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	for i := 0; i < DecodeRecordBurst; i++ {
		require.True(t, b.AllowRecord("acme", 7))
	}
	require.False(t, b.AllowRecord("acme", 7))
	require.True(t, b.AllowRecord("acme", 8), "another device type has its own bucket")
	require.True(t, b.AllowRecord("globex", 7), "another tenant's same device-type id has its own bucket")
}

func TestTheSampleBudgetIsOnePerProfilePerTenInSeconds(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	require.True(t, b.AllowSample("acme", "meter"))
	require.False(t, b.AllowSample("acme", "meter"))
	c.add(DecodeSampleInterval - time.Second)
	require.False(t, b.AllowSample("acme", "meter"))
	require.True(t, b.AllowSample("acme", "other"), "another profile")
	require.True(t, b.AllowSample("globex", "meter"), "another tenant's profile of the same token")
	c.add(time.Second)
	require.True(t, b.AllowSample("acme", "meter"))
}

func TestATenantPastItsOwnKeyBoundIsDeniedAndNoOtherTenantIs(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	for i := 0; i < maxBudgetKeysPerTenant; i++ {
		require.True(t, b.AllowRecord("acme", uint(i)), fmt.Sprint(i))
		require.True(t, b.AllowSample("acme", fmt.Sprint("p", i)), fmt.Sprint(i))
	}
	require.False(t, b.AllowRecord("acme", uint(maxBudgetKeysPerTenant)), "acme's new key is denied: suppressed, never unbudgeted")
	require.False(t, b.AllowSample("acme", "one-more"))

	require.True(t, b.AllowRecord("globex", 1), "another tenant's first failure is still recorded")
	require.True(t, b.AllowSample("globex", "meter"), "and sampled")
	require.True(t, b.AllowRecord("acme", 0), "acme's existing keys keep working")

	// What has recovered is forgotten, which makes room for acme again.
	c.add(10 * time.Minute)
	require.True(t, b.AllowRecord("acme", uint(maxBudgetKeysPerTenant)))
	require.True(t, b.AllowSample("acme", "one-more"))
}

func TestTheGlobalKeyBoundIsABackstopAboveThePerTenantOne(t *testing.T) {
	require.Greater(t, maxBudgetKeys, 10*maxBudgetKeysPerTenant)
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	for i := 0; i < maxBudgetKeys/maxBudgetKeysPerTenant; i++ {
		for k := 0; k < maxBudgetKeysPerTenant; k++ {
			require.True(t, b.AllowRecord(fmt.Sprint("t", i), uint(k)))
		}
	}
	require.False(t, b.AllowRecord("one-tenant-too-many", 1), "the total is bounded as well")
}

// The figures are the contract the documentation and the alert text state, so they are pinned as
// literals here: a test written in terms of the constants would pass for any value of them.
func TestTheBudgetFiguresAreThePublishedOnes(t *testing.T) {
	require.Equal(t, 20, DecodeRecordBurst)
	require.Equal(t, 10, DecodeRecordRefillPerMinute)
	require.Equal(t, 10*time.Second, DecodeSampleInterval)
	require.Equal(t, 20, model.MaxDecoderSamplesPerProfile)
	require.Equal(t, 7*24*time.Hour, model.DecoderHealthRetention)
}

// Charging reclaims what has recovered before it judges the bound, as AllowRecord does.
func TestChargingReclaimsExpiredKeysBeforeJudgingTheBound(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	for i := 0; i < maxBudgetKeysPerTenant; i++ {
		require.True(t, b.AllowRecord("acme", uint(i)))
	}
	c.add(10 * time.Minute) // every bucket is full again
	for i := 0; i < DecodeRecordBurst; i++ {
		b.Charge("acme", 99999) // a NEW device type
	}
	require.False(t, b.AllowRecord("acme", 99999), "the charges drained the new key's bucket")
}

func TestATenantWithNoKeysLeftIsForgotten(t *testing.T) {
	c := newFakeClock()
	b := newDecodeBudget(c.now)
	for i := 0; i < 2000; i++ { // enough to trigger a sweep
		b.AllowRecord(fmt.Sprint("t", i), 1)
		b.AllowSample(fmt.Sprint("t", i), "p")
	}
	c.add(10 * time.Minute)
	b.AllowRecord("fresh", 1)
	b.AllowSample("fresh", "p")
	b.mu.Lock()
	defer b.mu.Unlock()
	require.LessOrEqual(t, len(b.recordsBy), 2, "the per-tenant counters do not outlive their keys")
	require.LessOrEqual(t, len(b.samplesBy), 2)
}
