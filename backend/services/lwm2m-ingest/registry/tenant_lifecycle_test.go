// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionEnds records the OnSessionEnd hook, which is how the observe manager and the
// downlink conn table learn a session is over.
type sessionEnds struct {
	mu    sync.Mutex
	ended []string
}

func (s *sessionEnds) hook(identity string, _ uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = append(s.ended, identity)
}

func (s *sessionEnds) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ended...)
}

// An Update is a device keeping its registration alive, and the registration carries the
// device token resolved when it registered. For a tenant that has been deleted it must not
// be extended: the registration is ended (its observations and its downlink conn with it)
// and the device is told the registration is unknown, so it re-registers and meets the
// lifecycle gate there. Kept alive instead, it would outlive the purge and reach whatever
// tenant holds the token next.
func TestAnUpdateForADeletedTenantEndsTheRegistration(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	deleted := false
	ends := &sessionEnds{}
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.TenantDeleted = func(tenant string) bool { return deleted && tenant == "acme" }
		o.OnSessionEnd = ends.hook
	})

	result, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, RegisterOK, result)
	require.Equal(t, UpdateOK, r.Update("dev-1", regId, 300), "a live tenant's Update is unaffected")

	deleted = true
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", regId, 300))
	_, ok := r.DeviceToken("dev-1")
	assert.False(t, ok, "the registration must be gone, not merely refused once")
	assert.Equal(t, []string{"dev-1"}, ends.all(), "its observations and downlink conn must end with it")
	assert.Empty(t, em.disconnects(), "nothing is written into a tenant whose data is being erased")
}

// A device that stays asleep for the whole purge sends no Update for the check above to
// act on, and its lifetime can be a day. The periodic sweep ends every registration of a
// deleted tenant regardless, leaving other tenants' registrations alone.
func TestTheSweepEndsEveryRegistrationOfADeletedTenant(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	ends := &sessionEnds{}
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.TenantDeleted = func(tenant string) bool { return tenant == "acme" }
		o.OnSessionEnd = ends.hook
	})
	// Registered while the gate still read the tenant as live (the resolver fake admits it).
	other := testBinding
	other.Tenant = "acme-corp"
	other.ExternalId = "plant-b/sensor-9"
	result, _, _, _ := r.Register(context.Background(), "dev-1", testBinding, 86400)
	require.Equal(t, RegisterOK, result)
	result, _, _, _ = r.Register(context.Background(), "dev-2", other, 86400)
	require.Equal(t, RegisterOK, result)

	r.SweepDeletedTenants()

	_, ok := r.DeviceToken("dev-1")
	assert.False(t, ok, "a deleted tenant's registration must be ended by the sweep")
	_, ok = r.DeviceToken("dev-2")
	assert.True(t, ok, "another tenant's registration must survive the sweep")
	assert.Equal(t, []string{"dev-1"}, ends.all())
	assert.Empty(t, em.disconnects(), "nothing is written into a tenant whose data is being erased")
}

// The sweep runs on its own until its context ends.
func TestRunTenantSweepSweepsUntilCancelled(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	var mu sync.Mutex
	deleted := false
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.TenantDeleted = func(string) bool { mu.Lock(); defer mu.Unlock(); return deleted }
	})
	result, _, _, _ := r.Register(context.Background(), "dev-1", testBinding, 86400)
	require.Equal(t, RegisterOK, result)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.RunTenantSweep(ctx, 5*time.Millisecond); close(done) }()

	mu.Lock()
	deleted = true
	mu.Unlock()
	require.Eventually(t, func() bool { _, ok := r.DeviceToken("dev-1"); return !ok }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunTenantSweep did not return after its context was cancelled")
	}
}

// A registration whose lifetime lapses after its tenant was deleted still ends its session (its
// observations and downlink conn go), but writes no DISCONNECTED: the tenant's data is being
// erased. A live tenant's expiry in the same registry still writes one.
func TestAnExpiryForADeletedTenantEndsTheSessionWithoutWritingPresence(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	ends := &sessionEnds{}
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.TenantDeleted = func(tenant string) bool { return tenant == testBinding.Tenant }
		o.OnSessionEnd = ends.hook
	})
	other := testBinding
	other.Tenant = "acme-corp"
	other.ExternalId = "plant-b/sensor-9"
	result, deletedReg, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, RegisterOK, result)
	result, liveReg, _, _ := r.Register(context.Background(), "dev-2", other, 300)
	require.Equal(t, RegisterOK, result)

	r.onExpiry(deletedReg, 0)
	assert.Equal(t, []string{"dev-1"}, ends.all(), "the lapsed session must still end")
	assert.Empty(t, em.disconnects(), "nothing is written into a tenant whose data is being erased")

	r.onExpiry(liveReg, 0)
	assert.Len(t, em.disconnects(), 1, "a live tenant's expiry still writes its DISCONNECTED")
}

// TestTheTenantSweepFilesItsPasses pins the loop's call site: the pass counter moves only
// when the loop actually runs the sweep body, so a loop that records but no longer sweeps
// (or sweeps but no longer records) fails here.
func TestTheTenantSweepFilesItsPasses(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "lwm2m-ingest"}
	ms.UseMetricsRegistry(reg)

	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, func(o *Options) { o.TenantDeleted = func(string) bool { return true } })
	r.metrics = Metrics{TenantSweep: ms.NewPeriodicTaskMetrics("tenant_sweep")}
	result, _, _, _ := r.Register(context.Background(), "dev-1", testBinding, 86400)
	require.Equal(t, RegisterOK, result)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.RunTenantSweep(ctx, 5*time.Millisecond); close(done) }()

	passes := func(outcome string) float64 {
		families, err := reg.Gather()
		require.NoError(t, err)
		for _, f := range families {
			if f.GetName() != "devicechain_lwm2mingest_tenant_sweep_passes_total" {
				continue
			}
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "outcome" && l.GetValue() == outcome {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}
	require.Eventually(t, func() bool { return passes(core.PassComplete) >= 1 }, 2*time.Second, 5*time.Millisecond)
	_, ok := r.DeviceToken("dev-1")
	assert.False(t, ok, "the pass that was filed must be one that swept")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunTenantSweep did not return after its context was cancelled")
	}
	assert.Zero(t, passes(core.PassFailed), "a stop is never a failed pass")
}
