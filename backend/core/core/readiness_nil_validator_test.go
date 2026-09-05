// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE GATE'S OWN COMMENT SAID A NIL VALIDATOR MEANT "STILL CLOSED", AND MarkReady STORED
// ONE WITHOUT OBJECTION. Two production services had been opening the gate with nil since
// they were written, so /readyz answered 200 and Validator() was nil for the life of the
// process — a state the contract said could not exist. The resolution is that both are
// legitimate and neither may be silent: a service that verifies no tokens says so, and a
// service that meant to hand over a validator and had none is refused.

// TestMarkReadyRefusesANilValidator is the fail-closed half. A gate opened with no
// validator on a service that HAS a token-verifying surface reports ready and then refuses
// every authenticated request for the life of the process; a gate that stays closed keeps
// the pod out of Service endpoints, which is the loud failure.
func TestMarkReadyRefusesANilValidator(t *testing.T) {
	g := NewReadinessGate()

	g.MarkReady(nil)

	assert.False(t, g.Ready(), "a nil validator must not open the gate")
	assert.Nil(t, g.Validator())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, g.WaitReady(ctx), context.DeadlineExceeded,
		"consumers parked on the gate must stay parked")
}

// TestARefusedMarkReadyDoesNotBurnTheOnce. The refusal must leave the gate usable: a
// service whose validator arrives late — the ordinary StartAuthGate case — would otherwise
// be permanently not-ready because one earlier caller passed nil.
func TestARefusedMarkReadyDoesNotBurnTheOnce(t *testing.T) {
	g := NewReadinessGate()
	v := &auth.Validator{}

	g.MarkReady(nil)
	g.MarkReady(v)

	require.True(t, g.Ready(), "a refused nil must not consume the one-shot open")
	assert.Same(t, v, g.Validator())
}

// TestMarkReadyWithoutAuthSurfaceOpens is the other half: a service whose HTTP surface is
// health and metrics only verifies no token, so nil is the permanent and correct answer —
// and it has to be reachable, or the refusal above would simply break those services.
func TestMarkReadyWithoutAuthSurfaceOpens(t *testing.T) {
	g := NewReadinessGate()

	g.MarkReadyWithoutAuthSurface()

	assert.True(t, g.Ready(), "a service with nothing to authenticate must still become ready")
	assert.Nil(t, g.Validator(), "and it must still hand consumers nothing to authenticate with")
	assert.NoError(t, g.WaitReady(context.Background()))
}

// TestTheTwoDoorsAreStillOneGate. Both open the same latch exactly once, so a stray second
// call of either kind cannot flap readiness or swap a live validator out from under the
// request path.
func TestTheTwoDoorsAreStillOneGate(t *testing.T) {
	v := &auth.Validator{}

	withValidatorFirst := NewReadinessGate()
	withValidatorFirst.MarkReady(v)
	withValidatorFirst.MarkReadyWithoutAuthSurface()
	assert.Same(t, v, withValidatorFirst.Validator(),
		"opening again without a validator must not blank a live one")

	withoutFirst := NewReadinessGate()
	withoutFirst.MarkReadyWithoutAuthSurface()
	withoutFirst.MarkReady(v)
	assert.Nil(t, withoutFirst.Validator(),
		"the first open wins, exactly as it does for two validators")
	assert.True(t, withoutFirst.Ready())
}

// TestTheReadyGaugeFollowsTheGateNotTheCall. The exported signal and the probe that
// governs traffic must not disagree: a gauge set because MarkReady was CALLED would read 1
// for a service the gate had just kept closed.
func TestTheReadyGaugeFollowsTheGateNotTheCall(t *testing.T) {
	// An UNREGISTERED gauge, assigned to the unexported field directly: NewGauge goes
	// through promauto and the default registry, so building one the normal way here would
	// collide with every other test in the package. The field is what MarkReady writes, so
	// this reads exactly what a scrape would.
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "ready"})
	ms := &Microservice{Readiness: NewReadinessGate(), readyGauge: gauge}

	ms.MarkReady(nil)
	require.False(t, ms.Readiness.Ready(), "the fixture depends on the gate refusing")
	assert.Equal(t, float64(0), testutil.ToFloat64(gauge),
		"the exported ready signal must not claim 1 for a service the gate kept closed")

	ms.MarkReady(&auth.Validator{})
	assert.True(t, ms.Readiness.Ready())
	assert.Equal(t, float64(1), testutil.ToFloat64(gauge))

	// And the other door exports it too, or a service that verifies no tokens would be
	// ready to Kubernetes and permanently 0 on the dashboard.
	other := &Microservice{Readiness: NewReadinessGate(),
		readyGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "ready"})}
	other.MarkReadyWithoutAuthSurface()
	assert.Equal(t, float64(1), testutil.ToFloat64(other.readyGauge))

	drained := make(chan struct{})
	go func() { defer close(drained); _ = ms.Readiness.WaitReady(context.Background()) }()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("the gate reported ready but never released a waiter")
	}
}
