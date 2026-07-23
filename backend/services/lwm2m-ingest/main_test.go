// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-lwm2m-ingest/registry"
	"github.com/devicechain-io/dc-microservice/core"
)

// TestLoadConfigurationParsesRenderedChartDocument pins the chart↔service seam. The
// Helm chart renders this exact JSON into the mounted config document; the service
// parses it with DisallowUnknownFields, so a single key the struct does not name (a
// chart/struct drift) is a startup crash-loop, invisible to a test that hand-builds the
// struct. This feeds the literal rendered bytes through the real loader.
func TestLoadConfigurationParsesRenderedChartDocument(t *testing.T) {
	rendered := `{"listen":{"host":"0.0.0.0","port":5684},"security":{"connectionIdLength":8,"handshakeTimeoutSeconds":10,"idleTimeoutSeconds":0,"maxSessions":50000,"identities":[{"identity":"dev-1","pskEnv":"DC_LWM2M_PSK_DEV1","tenant":"acme","externalId":"plant-a/sensor-1","deviceTypeToken":"sensor","autoRegister":true}]}}`

	var cfg config.Lwm2mConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(rendered), &cfg))
	assert.Equal(t, "0.0.0.0", cfg.Listen.Host)
	assert.Equal(t, 5684, cfg.Listen.Port)
	require.NotNil(t, cfg.Security.ConnectionIdLength)
	assert.Equal(t, 8, *cfg.Security.ConnectionIdLength)
	assert.Equal(t, 50000, cfg.Security.MaxSessions)
	require.Len(t, cfg.Security.Identities, 1)
	assert.Equal(t, "dev-1", cfg.Security.Identities[0].Identity)
	assert.Equal(t, "DC_LWM2M_PSK_DEV1", cfg.Security.Identities[0].PskEnv)
	assert.Equal(t, "acme", cfg.Security.Identities[0].Tenant)
	assert.Equal(t, "plant-a/sensor-1", cfg.Security.Identities[0].ExternalId)
	assert.True(t, cfg.Security.Identities[0].AutoRegister)
}

// The default (empty) configuration must load and validate — a bare render with no
// identities is a valid inert deployment.
func TestEmptyConfigurationIsValid(t *testing.T) {
	var cfg config.Lwm2mConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), &cfg))
	assert.Equal(t, config.DefaultListenPort, cfg.Listen.Port)
}

// fakeServe is a serveServer whose Serve() blocks until Stop() (or an explicit die) unblocks it,
// recording how many times Stop was called. A test drives the serve-intent supervision without a
// real DTLS transport.
type fakeServe struct {
	release  chan struct{}
	stops    atomic.Int32
	stopOnce sync.Once
}

func newFakeServe() *fakeServe { return &fakeServe{release: make(chan struct{})} }

func (f *fakeServe) Serve() error { <-f.release; return nil }

func (f *fakeServe) Stop() {
	f.stops.Add(1)
	f.stopOnce.Do(func() { close(f.release) })
}

// die makes a blocked Serve() return WITHOUT a Stop — the unexpected-transport-death path.
func (f *fakeServe) die() { f.stopOnce.Do(func() { close(f.release) }) }

// TestSuperviseServeGracefulStopDoesNotFatal is the L3a serve-intent guard (MUST-FIX #1): an
// intended stop (a leadership eviction) does NOT trigger the fatal callback, even though Serve
// returns. This is the shape that replaces the process-global "stopping" flag with per-serve-term
// intent, so a lease blip stops the socket without killing the pod. stopServing blocks until the
// serve goroutine has unwound, so the zero death count is deterministic (the assert happens-after
// the goroutine's intent check). It pins "an intended stop never fatals"; the store-BEFORE-Stop
// ordering that guarantees this in production (where a real Serve does not return instantly) is a
// correctness argument in the code comment, not something this fast fake can race-pin on its own.
func TestSuperviseServeGracefulStopDoesNotFatal(t *testing.T) {
	f := newFakeServe()
	var deaths atomic.Int32
	stopServing := superviseServe(f, func(error) { deaths.Add(1) })

	stopServing() // records intent, Stops, waits for the serve goroutine to observe it

	assert.Equal(t, int32(0), deaths.Load(), "a graceful (intended) Stop must NOT trigger the fatal path")
	assert.Equal(t, int32(1), f.stops.Load(), "stopServing must Stop the transport exactly once")

	stopServing() // idempotent: a second call does nothing
	assert.Equal(t, int32(1), f.stops.Load(), "stopServing is idempotent")
}

// TestSuperviseServeUnexpectedDeathFatals is the counterweight: when Serve returns while the term
// still intends to serve (a socket death on the single serving replica), the fatal callback DOES
// fire — the total-ingest-outage guard that a Ready pod would otherwise hide — and it is handed the
// Serve error so the production fatal log names the cause.
func TestSuperviseServeUnexpectedDeathFatals(t *testing.T) {
	f := newFakeServe()
	died := make(chan error, 1)
	superviseServe(f, func(err error) { died <- err })

	f.die() // Serve returns with no intent recorded → the unexpected-death path

	select {
	case <-died:
	case <-time.After(2 * time.Second):
		t.Fatal("an unexpected Serve return must trigger the fatal path")
	}
}

// --- L3b failover reconstruction orchestration -----------------------------

type mainReconciler struct {
	mu       sync.Mutex
	devices  map[string][]adapter.AssertedDevice
	maxByTen map[string]uint64
	failN    int // fail the first N reads (across all tenants), then succeed
	calls    int
}

func (m *mainReconciler) AssertedActive(_ context.Context, tenant, _ string) ([]adapter.AssertedDevice, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.failN > 0 {
		m.failN--
		return nil, 0, errors.New("device-state down")
	}
	return m.devices[tenant], m.maxByTen[tenant], nil
}

type mainResolver struct {
	mu      sync.Mutex
	token   string
	outcome adapter.ResolveOutcome
	calls   int
}

func (m *mainResolver) Resolve(_ context.Context, _, _ string, _ adapter.IngestPolicy) (string, adapter.ResolveOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.token, m.outcome, nil
}

type mainEmitter struct {
	mu     sync.Mutex
	events []adapter.PresenceEvent
}

func (m *mainEmitter) EmitPresence(_ context.Context, _, _, _ string, ev adapter.PresenceEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return nil
}

func (m *mainEmitter) disconnects() []adapter.PresenceEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []adapter.PresenceEvent
	for _, e := range m.events {
		if !e.Connected {
			out = append(out, e)
		}
	}
	return out
}

func newReconTestRegistry(res *mainResolver, em *mainEmitter, epoch *adapter.EpochSource, gauge prometheus.Gauge) *registry.Registry {
	return registry.New(res, em, epoch, registry.Metrics{ActiveRegistrations: gauge}, registry.Options{Source: registry.SourceLwM2M})
}

// buildReverseBindings resolves a duplicate (tenant, externalId) across two identities
// deterministically — the LOWEST identity wins — so the reconstruction installs exactly one shadow
// and is stable run-to-run (B8).
func TestBuildReverseBindingsDedup(t *testing.T) {
	bindings := map[string]config.PskBinding{
		"id-b": {Tenant: "acme", ExternalId: "plant-a/sensor-1"},
		"id-a": {Tenant: "acme", ExternalId: "plant-a/sensor-1"}, // collides with id-b
		"id-c": {Tenant: "acme", ExternalId: "plant-a/sensor-2"},
	}
	rev := buildReverseBindings(bindings)
	require.Contains(t, rev, "acme")
	assert.Equal(t, "id-a", rev["acme"]["plant-a/sensor-1"].identity, "the lower identity wins the collision")
	assert.Equal(t, "id-c", rev["acme"]["plant-a/sensor-2"].identity)
}

// reconstructPresence installs a shadow for an asserted device that still has a binding, and
// DISCONNECTS one whose credential was decommissioned (B6). The floor is raised above the stored
// sessions, so the orphan DISCONNECT is minted at an epoch that exceeds them (the end-to-end F6
// pin at the orchestration seam).
func TestReconstructPresenceInstallsShadowsAndDisconnectsOrphans(t *testing.T) {
	const storedMax = uint64(1_900_000_000_000_000_000)
	rec := &mainReconciler{
		devices: map[string][]adapter.AssertedDevice{"acme": {
			{ExternalId: "plant-a/sensor-1", SessionId: storedMax},
			{ExternalId: "plant-a/orphan-9", SessionId: storedMax - 10},
		}},
		maxByTen: map[string]uint64{"acme": storedMax},
	}
	res := &mainResolver{token: "tok-1", outcome: adapter.ResolveFound}
	em := &mainEmitter{}
	epoch := adapter.NewEpochSource(nil)
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "active"})
	reg := newReconTestRegistry(res, em, epoch, gauge)

	bindings := map[string]config.PskBinding{
		"id-1": {Tenant: "acme", ExternalId: "plant-a/sensor-1", DeviceTypeToken: "sensor"},
	}
	reconstructPresence(context.Background(), epoch, rec, reg, bindings)

	assert.Equal(t, float64(1), testutil.ToFloat64(gauge), "the matched device gets one shadow registration")
	// The orphan DISCONNECT runs on a background goroutine.
	require.Eventually(t, func() bool { return len(em.disconnects()) == 1 }, 2*time.Second, 5*time.Millisecond,
		"the unmatched (decommissioned-credential) device is DISCONNECTED (B6)")
	dis := em.disconnects()
	assert.Equal(t, "plant-a/orphan-9", dis[0].ExternalId)
	assert.Equal(t, "reconstruct-orphan", dis[0].Reason)
	assert.Greater(t, dis[0].SessionId, storedMax, "the orphan DISCONNECT epoch is floored above the stored sessions (F6)")
}

// A tenant whose FIRST device-state read fails is retried IN-TERM (F4): once the read succeeds the
// shadow is installed, without waiting for the next leadership acquisition.
func TestReconstructPresenceRetriesFailedRead(t *testing.T) {
	prev := reconstructReadRetryInterval
	reconstructReadRetryInterval = 10 * time.Millisecond
	defer func() { reconstructReadRetryInterval = prev }()

	rec := &mainReconciler{
		devices:  map[string][]adapter.AssertedDevice{"acme": {{ExternalId: "plant-a/sensor-1", SessionId: 5}}},
		maxByTen: map[string]uint64{"acme": 5},
		failN:    1, // the first read fails; the in-term retry succeeds
	}
	res := &mainResolver{token: "tok-1", outcome: adapter.ResolveFound}
	em := &mainEmitter{}
	epoch := adapter.NewEpochSource(nil)
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "active"})
	reg := newReconTestRegistry(res, em, epoch, gauge)

	bindings := map[string]config.PskBinding{"id-1": {Tenant: "acme", ExternalId: "plant-a/sensor-1"}}
	reconstructPresence(context.Background(), epoch, rec, reg, bindings)

	// The first pass read failed → no shadow yet; the retry goroutine installs it.
	assert.Equal(t, float64(0), testutil.ToFloat64(gauge), "the failed first read installs no shadow")
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) == 1 }, 2*time.Second, 5*time.Millisecond,
		"the in-term retry installs the shadow once device-state recovers (F4)")
}
