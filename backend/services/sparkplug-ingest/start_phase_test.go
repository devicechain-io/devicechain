// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// newTestMicroservice installs a Microservice with its own metrics registry, restoring
// the package globals afterwards. Each call gets a fresh one, so the instruments one test
// builds cannot collide with another's. It returns the registry,
// which is what the metric assertions below read.
//
// 🔴 THE REGISTRY IS NOT OPTIONAL SCAFFOLDING. A bare &core.Microservice{} has a nil
// registerer, and promauto reads nil as "construct the collector but register it
// nowhere" — every metric then builds happily, twice, and a test written on one could
// not observe a duplicate registration at all.
func newTestMicroservice(t *testing.T) *prometheus.Registry {
	t.Helper()

	prevMs := Microservice
	prevNats, prevGauge := NatsManager, leaderGauge
	t.Cleanup(func() {
		Microservice = prevMs
		NatsManager, leaderGauge = prevNats, prevGauge
	})

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "sparkplug-ingest",
		Readiness:      core.NewReadinessGate(),
	}
	registry := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(registry)
	// No validator, matching production: this service authenticates at the broker and
	// verifies no JWT, so the gate is opened without an auth surface rather than left
	// closed (which would make every /readyz below a 503 for the wrong reason).
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	leaderGauge = nil
	return registry
}

// registeredMetrics is the sorted set of metric family names currently registered. It
// reads the registry rather than /metrics because the assertion has to be made BEFORE
// the first server start, when there is no server to read it from.
//
// 🔴 IT DOES NOT SEE A VEC WITH NO CHILDREN, and reading the registry instead of the
// text output does not change that: Gather() builds families from collected series, so
// a CounterVec/GaugeVec nobody has observed a label value on contributes nothing —
// messages_total and the eight jetstream_* GaugeVecs are absent from every list this
// returns. The consequence for the caller is worth stating plainly: a plain Counter or
// Gauge moved onto the start path is caught by the set comparison a pass before it
// would panic, but a childless Vec is invisible here and is caught by the PANIC ARM
// alone, on the second entry. Coverage holds either way; the early, legible failure
// does not.
func registeredMetrics(t *testing.T, registry *prometheus.Registry) []string {
	t.Helper()

	families, err := registry.Gather()
	require.NoError(t, err)
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, family.GetName())
	}
	sort.Strings(names)
	return names
}

// A second entry into the start phase must not panic, and that phase must build nothing
// that can only be built once.
//
// 🔴 THIS IS THE TRAP THE START PHASE MAKES EASY TO WALK INTO. LifecycleComponent does
// not promise ExecuteStart runs once — a start that fails restores the component to
// Initialized, so a retried start enters it again — and the leader gauge goes through
// promauto, which panics on a duplicate registration. Build it from the start path and a
// retried start is a crash — not at the next deploy, which is why that door has never
// been walked through in production.
//
// The probe routes were the second door until core/service took the HTTP surface over;
// its own TestARetriedStartAfterAFailedBindServes pins that half now, once for every
// service that has no GraphQL plane.
//
// So the leadership start is driven twice here, with the initialize phase run once
// beforehand, exactly as a retry after a failed start reaches it. Both assertions matter
// and they fail differently:
//
//   - a construction moved onto the start path PANICS on the second pass, which fails
//     the test binary rather than this test. This arm catches EVERY instrument;
//   - and for an instrument that yields a series on construction — a plain Counter or
//     Gauge, as leaderGauge is — the metric-set comparison catches it one pass earlier,
//     as a plain failure, because the start phase changed the registry between the
//     snapshot and the check. A Vec with no children is invisible to that comparison;
//     see registeredMetrics.
func TestStartPhaseRestartDoesNotPanic(t *testing.T) {
	registry := newTestMicroservice(t)

	// The INITIALIZE phase, through the SERVICE's own function rather than a copy of it:
	// a test that built its own gauge would be asserting against its own wiring, and would
	// keep passing if the real one moved.
	buildMetrics()
	// The lease hangs off the NatsManager, which production also builds in initialize.
	// This one is never Initialized, so its JetStream context is nil and startLeadership
	// fails at the lease — after the point where a start-phase instrument would be
	// constructed, which is all this test needs to drive.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })

	afterInitialize := registeredMetrics(t, registry)
	// The counterweight: "the start phase registers nothing new" is also satisfied by a
	// buildMetrics that builds nothing at all.
	require.Contains(t, afterInitialize, "devicechain_sparkplugingest_is_leader",
		"the leader gauge is not built in the initialize phase")
	// The rebirth queue's counters are named here for the same reason, and they are the
	// half of buildMetrics most likely to be added later by someone reaching for the
	// nearest constructor. The equality check below does not need them: it compares the
	// snapshot against itself and so catches a plain Counter moved onto the start path
	// whether or not this test ever names it. What these two lines catch is the other
	// failure — a counter that is not built ANYWHERE, which leaves the snapshot and the
	// post-start set identical and every assertion below satisfied.
	require.Contains(t, afterInitialize, "devicechain_sparkplugingest_rebirth_enqueued_total",
		"the rebirth enqueued counter is not built in the initialize phase")
	require.Contains(t, afterInitialize, "devicechain_sparkplugingest_rebirth_dropped_total",
		"the rebirth dropped counter is not built in the initialize phase")

	// start runs the part of afterMicroserviceStarted that is this service's own.
	start := func() {
		require.ErrorContains(t, startLeadership(), "JetStream context not initialized",
			"the lease got further than this test can drive it; the assertions below no longer cover the start path")
	}

	start()
	require.Equal(t, afterInitialize, registeredMetrics(t, registry),
		"the start phase registered a metric; it must construct none, or a restart panics")

	// Reaching the far side of this at all is half the assertion: a construction moved
	// into the start path panics, and a panic fails the binary rather than this test.
	start()
	require.Equal(t, afterInitialize, registeredMetrics(t, registry),
		"the restarted start phase registered a metric")
}
