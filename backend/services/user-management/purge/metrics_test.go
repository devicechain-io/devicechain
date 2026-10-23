// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

const metricPrefix = "devicechain_usermanagement_tenant_purge_"

// sample finds one series by metric name and exact label set. found is false when the
// family or the series is missing, so a test can tell "0" from "absent".
func sample(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (m *dto.Metric, found bool) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	next:
		for _, met := range f.GetMetric() {
			if len(met.GetLabel()) != len(labels) {
				continue
			}
			for _, lp := range met.GetLabel() {
				if labels[lp.GetName()] != lp.GetValue() {
					continue next
				}
			}
			return met, true
		}
	}
	return nil, false
}

func blockedGauge(t *testing.T, h *harness, store, reason string) float64 {
	t.Helper()
	m, ok := sample(t, h.reg, metricPrefix+"store_blocked", map[string]string{"store": store, "reason": reason})
	require.True(t, ok, "store_blocked{store=%s,reason=%s} must exist at zero, not be absent", store, reason)
	return m.GetGauge().GetValue()
}

// A store that defers is named, with the reason, and goes back to zero once it recovers.
func TestABlockedStoreIsNamedAndClearsWhenItRecovers(t *testing.T) {
	deferring := &fakeStore{name: StoreBroker, deferred: []string{"retained messages remain"}}
	failing := &fakeStore{name: StoreKeyValue, err: errors.New("kv down")}
	healthy := &fakeStore{name: StoreRelational, rows: 3}
	h := newHarness(t, deferring, failing, healthy)

	// Present at zero before anything is open.
	require.EqualValues(t, 0, blockedGauge(t, h, StoreBroker, ReasonDeferred))

	h.deleteTenant("acme")
	h.deleteTenant("globex")
	h.pass()

	require.EqualValues(t, 2, blockedGauge(t, h, StoreBroker, ReasonDeferred), "two deletions held by broker")
	require.EqualValues(t, 2, blockedGauge(t, h, StoreKeyValue, ReasonError), "two deletions held by a failing kv")
	require.EqualValues(t, 0, blockedGauge(t, h, StoreRelational, ReasonDeferred), "a clean store is not blocking")
	require.EqualValues(t, 0, blockedGauge(t, h, StoreRelational, ReasonError))
	require.EqualValues(t, 0, blockedGauge(t, h, StoreBroker, ReasonError), "reason is exact, not a union")

	deferring.deferred = nil
	failing.err = nil
	h.pass()
	require.EqualValues(t, 0, blockedGauge(t, h, StoreBroker, ReasonDeferred), "recovered store must read 0, not its last value")
	require.EqualValues(t, 0, blockedGauge(t, h, StoreKeyValue, ReasonError))
}

// Store names outside the fixed set fold into "other" so the label is never driven by data.
func TestAnUnknownStoreNameFoldsIntoOther(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "some-new-store", deferred: []string{"x"}})
	h.deleteTenant("acme")
	h.pass()
	require.EqualValues(t, 1, blockedGauge(t, h, "other", ReasonDeferred))
	_, found := sample(t, h.reg, metricPrefix+"store_blocked", map[string]string{"store": "some-new-store", "reason": ReasonDeferred})
	require.False(t, found, "an unlisted store name must not mint a series")
}

// Progress counts this pass's rows once, not the running total again on every pass.
func TestRowsErasedCountThePassNotTheRunningTotal(t *testing.T) {
	h := newHarness(t, &fakeStore{name: StoreRelational, rows: 9})
	h.deleteTenant("acme")
	h.pass()
	h.pass()
	h.pass()
	m, ok := sample(t, h.reg, metricPrefix+"store_rows_erased_total", map[string]string{"store": StoreRelational})
	require.True(t, ok)
	require.EqualValues(t, 9, m.GetCounter().GetValue(), "a second and third pass erase nothing more")
	e, ok := sample(t, h.reg, metricPrefix+"store_erase_duration_seconds", map[string]string{"store": StoreRelational})
	require.True(t, ok)
	require.EqualValues(t, 3, e.GetHistogram().GetSampleCount(), "every erase call is timed")
}

// Completion is counted once, with both durations measured from the cut.
func TestCompletionIsCountedAndTimed(t *testing.T) {
	h := newHarness(t, &fakeStore{name: StoreRelational, rows: 9})
	h.deleteTenant("acme")
	h.pass()
	m, _ := sample(t, h.reg, metricPrefix+"completed_total", nil)
	require.EqualValues(t, 0, m.GetCounter().GetValue(), "not complete inside the settle window")

	h.clock = h.clock.Add(settleWindow + time.Second)
	h.pass()
	m, _ = sample(t, h.reg, metricPrefix+"completed_total", nil)
	require.EqualValues(t, 1, m.GetCounter().GetValue())

	d, ok := sample(t, h.reg, metricPrefix+"duration_seconds", nil)
	require.True(t, ok)
	require.EqualValues(t, 1, d.GetHistogram().GetSampleCount())
	require.InDelta(t, (settleWindow + time.Second).Seconds(), d.GetHistogram().GetSampleSum(), 1)

	c, ok := sample(t, h.reg, metricPrefix+"clean_duration_seconds", nil)
	require.True(t, ok)
	require.EqualValues(t, 1, c.GetHistogram().GetSampleCount())
	require.Less(t, c.GetHistogram().GetSampleSum(), d.GetHistogram().GetSampleSum(),
		"the erasure itself finishes before the waits that follow it")

	h.pass() // nothing is open now: no second observation
	m, _ = sample(t, h.reg, metricPrefix+"completed_total", nil)
	require.EqualValues(t, 1, m.GetCounter().GetValue())
}

// A deletion the coordinator cannot carry through a pass is counted as a failure.
func TestACoordinatorFaultIsCounted(t *testing.T) {
	h := newHarness(t, &fakeStore{name: StoreRelational, deferred: []string{"still here"}})
	h.deleteTenant("acme")
	// A ledger line for a store nobody asks about makes the pass refuse the tenant.
	h.pass()
	h.coord.stores = []Store{&fakeStore{name: StoreBroker}}
	_ = h.passResult()
	m, ok := sample(t, h.reg, metricPrefix+"failures_total", nil)
	require.True(t, ok)
	require.GreaterOrEqual(t, m.GetCounter().GetValue(), float64(1))
}

// Every replica exports the gauges and the alerts take the max across them, so a replica
// that loses the lock to a peer must read the shared ledger rather than keep what its own
// last pass saw: otherwise a store that recovered on the peer's pass still reads 1 here.
func TestAReplicaThatSkipsThePassReadsTheLedger(t *testing.T) {
	failing := &fakeStore{name: StoreKeyValue, err: errors.New("kv down")}
	deferring := &fakeStore{name: StoreBroker, deferred: []string{"retained messages remain"}}
	h := newHarness(t, failing, deferring)

	ctx := context.Background()
	peerReg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "user-management"}
	ms.UseMetricsRegistry(peerReg)
	peer := NewCoordinator(ms, h.iam, h.coord.db, h.coord.stores, time.Minute,
		settleWindow, tokenHoldWindow, core.NewNoOpLifecycleCallbacks())
	peer.now = func() time.Time { return h.clock }
	// skip drives the peer's real tick on a turn where the other replica holds the lock.
	skip := func() {
		t.Helper()
		peer.tryLock = func(context.Context, func() error) (bool, error) { return false, nil }
		require.ErrorIs(t, peer.RunOnce(ctx), core.ErrPassSkipped)
	}
	peerGauge := func(store, reason string) float64 {
		t.Helper()
		m, ok := sample(t, peerReg, metricPrefix+"store_blocked", map[string]string{"store": store, "reason": reason})
		require.True(t, ok)
		return m.GetGauge().GetValue()
	}

	h.deleteTenant("acme")
	h.clock = h.clock.Add(10 * time.Minute)
	// The peer passes first and sees both stores blocked, and the deletion ten minutes old.
	_ = peer.pass(ctx)
	age, ok := sample(t, peerReg, metricPrefix+"oldest_age_seconds", nil)
	require.True(t, ok)
	require.EqualValues(t, 600, age.GetGauge().GetValue())
	require.EqualValues(t, 1, peerGauge(StoreKeyValue, ReasonError))
	require.EqualValues(t, 1, peerGauge(StoreBroker, ReasonDeferred))

	// Both recover; the other replica holds the next pass and sees it.
	failing.err = nil
	deferring.deferred = nil
	h.pass()
	require.EqualValues(t, 0, blockedGauge(t, h, StoreKeyValue, ReasonError))
	require.EqualValues(t, 1, peerGauge(StoreKeyValue, ReasonError), "the peer has not looked again yet")

	// The peer's tick finds the lock held and reads the ledger instead.
	skip()
	require.EqualValues(t, 0, peerGauge(StoreKeyValue, ReasonError), "a skipped replica must not keep its last pass")
	require.EqualValues(t, 0, peerGauge(StoreBroker, ReasonDeferred))

	// And it reads a blocked store as blocked, not just everything as clean.
	failing.err = errors.New("kv down again")
	h.pass()
	skip()
	require.EqualValues(t, 1, peerGauge(StoreKeyValue, ReasonError))
	require.EqualValues(t, 0, peerGauge(StoreBroker, ReasonDeferred))

	// The oldest-age gauge follows the shared rows too: completed on the other replica, 0 here.
	failing.err = nil
	h.pass()
	h.clock = h.clock.Add(settleWindow + time.Second)
	h.pass()
	h.clock = h.clock.Add(tokenHoldWindow)
	h.pass()
	require.False(t, h.exists("acme"), "the fixture must actually complete the purge")
	skip()
	age, ok = sample(t, peerReg, metricPrefix+"oldest_age_seconds", nil)
	require.True(t, ok)
	require.EqualValues(t, 0, age.GetGauge().GetValue(), "completed on the other replica")
	require.EqualValues(t, 0, peerGauge(StoreKeyValue, ReasonError))
}

// Every deletion takes at least the hold, so the buckets must resolve just above it, not
// lump every completion into the one coarse bucket past the hold.
func TestPurgeDurationBucketsResolveJustPastTheHold(t *testing.T) {
	// Wired from the coordinator's own hold: the exported histogram carries hold+300.
	h := newHarness(t, &fakeStore{name: StoreRelational, rows: 1})
	d, ok := sample(t, h.reg, metricPrefix+"duration_seconds", nil)
	require.True(t, ok)
	var bounds []float64
	for _, b := range d.GetHistogram().GetBucket() {
		bounds = append(bounds, b.GetUpperBound())
	}
	require.Contains(t, bounds, tokenHoldWindow.Seconds()+300)

	require.Equal(t, purgeBuckets, purgeBucketsFor(0), "no hold: the fixed set, unchanged")
	b := purgeBucketsFor(12 * time.Hour)
	require.True(t, sort.Float64sAreSorted(b))
	require.Contains(t, b, 43200.0, "the fixed set survives")
	seen := map[float64]bool{}
	for _, v := range b {
		require.False(t, seen[v], "duplicate bucket %v", v)
		seen[v] = true
	}
	// A deletion two minutes past a 12 h hold lands in a bucket within five minutes of it.
	upper := 0.0
	for _, v := range b {
		if v >= 43200+120 {
			upper = v
			break
		}
	}
	require.LessOrEqual(t, upper, 43200.0+300)
}
