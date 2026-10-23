// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
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
