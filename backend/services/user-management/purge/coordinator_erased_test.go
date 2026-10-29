// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-user-management/iam"
)

// The completion signal: iam_tenant_purges.erased_at. These tests pin the properties that
// make it usable as evidence. It stands only when EVERY store is clean and settled, it is
// set before the token hold elapses (completed_at is the release, up to a half day later),
// it is withdrawn if data reappears, and completion never leaves a record claiming a
// release without an erasure.

const erasedHold = 12 * time.Hour

func erasureRecord(t *testing.T, h *harness, token string, epoch time.Time) *iam.TenantPurge {
	t.Helper()
	rec, err := h.iam.PurgeRecordFor(context.Background(), token, epoch)
	require.NoError(t, err)
	return rec
}

func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			for _, m := range f.GetMetric() {
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("no counter named %q was registered; the ones that were: %v", name, gatheredNames(reg))
	return 0
}

func TestErasureIsClaimedAtSettleNotAtTokenRelease(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb", rows: 9}, &fakeStore{name: "tsdb", rows: 5})
	h.coord.tokenHold = erasedHold
	tenant := h.deleteTenant("acme")
	epoch := *tenant.PurgeEpoch

	h.pass()
	require.Nil(t, erasureRecord(t, h, "acme", epoch).ErasedAt,
		"every store is clean but has not settled: the data is not yet established as gone")

	h.clock = h.clock.Add(settleWindow + time.Second)
	h.pass()
	rec := erasureRecord(t, h, "acme", epoch)
	require.NotNil(t, rec.ErasedAt, "clean and settled: erasure is claimed while the token is still held")
	require.Nil(t, rec.CompletedAt, "the token is still held, so the deletion is not complete")
	require.True(t, h.exists("acme"))
	claimedAt := *rec.ErasedAt
	require.Equal(t, 1.0, counterValue(t, h.reg, "devicechain_usermanagement_tenant_purge_erased_total"))

	// A later pass re-confirms the claim without moving it: the first moment is the evidence.
	h.clock = h.clock.Add(time.Hour)
	h.pass()
	require.True(t, claimedAt.Equal(*erasureRecord(t, h, "acme", epoch).ErasedAt))
	require.Equal(t, 1.0, counterValue(t, h.reg, "devicechain_usermanagement_tenant_purge_erased_total"))

	// Completion keeps the earlier moment, and never precedes it.
	h.clock = h.clock.Add(erasedHold + time.Hour)
	h.pass()
	require.False(t, h.exists("acme"))
	done := erasureRecord(t, h, "acme", epoch)
	require.NotNil(t, done.CompletedAt)
	require.NotNil(t, done.ErasedAt)
	require.True(t, claimedAt.Equal(*done.ErasedAt))
	require.False(t, done.ErasedAt.After(*done.CompletedAt))
}

// The negative control: one store still has data, so the signal is never set, however much
// time passes. Without it the other tests would pass for a signal that is simply always on.
func TestOneStoreStillHoldingDataMeansNoErasureClaim(t *testing.T) {
	held := Pending{StoreName: "tsdb", Holds: "the event store still holds this tenant's telemetry"}
	h := newHarness(t, &fakeStore{name: "rdb", rows: 9}, held)
	h.coord.tokenHold = erasedHold
	tenant := h.deleteTenant("acme")
	epoch := *tenant.PurgeEpoch

	for i := 0; i < 4; i++ {
		h.pass()
		h.clock = h.clock.Add(erasedHold)
	}
	rec := erasureRecord(t, h, "acme", epoch)
	require.Nil(t, rec.ErasedAt, "partial is not complete: rdb is clean but tsdb is not")
	require.Nil(t, rec.CompletedAt)
	require.Zero(t, counterValue(t, h.reg, "devicechain_usermanagement_tenant_purge_erased_total"))
}

func TestAStoreErroringMeansNoErasureClaim(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb", rows: 9}, &fakeStore{name: "tsdb", err: context.DeadlineExceeded})
	h.coord.tokenHold = erasedHold
	tenant := h.deleteTenant("acme")
	h.pass()
	h.clock = h.clock.Add(settleWindow * 3)
	h.pass()
	require.Nil(t, erasureRecord(t, h, "acme", *tenant.PurgeEpoch).ErasedAt)
}

// The claim is withdrawn if data reappears before the token is released.
func TestTheErasureClaimIsWithdrawnWhenDataReappears(t *testing.T) {
	store := &reappearingStore{name: "tsdb", rowsOnCall: map[int]int64{3: 42}}
	h := newHarness(t, store)
	h.coord.tokenHold = erasedHold
	tenant := h.deleteTenant("acme")
	epoch := *tenant.PurgeEpoch

	h.pass()
	h.clock = h.clock.Add(settleWindow + time.Minute)
	h.pass()
	require.NotNil(t, erasureRecord(t, h, "acme", epoch).ErasedAt)

	h.clock = h.clock.Add(time.Minute)
	h.pass() // rows reappear: clean-since restarts, so the settle window is open again
	require.Nil(t, erasureRecord(t, h, "acme", epoch).ErasedAt,
		"a claim must not outlive the evidence for it")

	h.clock = h.clock.Add(settleWindow + time.Minute)
	h.pass()
	require.NotNil(t, erasureRecord(t, h, "acme", epoch).ErasedAt, "re-established once settled again")
	require.Equal(t, 2.0, counterValue(t, h.reg, "devicechain_usermanagement_tenant_purge_erased_total"))
}
