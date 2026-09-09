// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// A start after a stop must not re-register this processor's metrics.
//
// The NATS manager invokes its oncreate callback on EVERY start (see
// messaging.NewNatsManager), and this service builds its processor inside that
// callback — it has to, because the processor holds a reader bound to the connection.
// So a collector constructed by the constructor is constructed a second time when the
// service is restarted in place, and promauto's MustRegister panics on the duplicate,
// taking the process down. Building the instruments in the initialize phase and
// handing them in is what makes the second construction free.
//
// This drives the two constructions rather than asserting on the shape of the code,
// so it fails the same way production did: with the panic.
func TestSecondStartDoesNotReRegisterMetrics(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
	// 🔴 LOAD-BEARING, NOT SCAFFOLDING. A Microservice built as a bare struct literal
	// has NO registry, and core builds its metrics unregistered in that case — so
	// duplicate registration is impossible and this test would pass against the very
	// defect it exists to catch. Deleting this line disarms the test silently.
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	// The initialize phase. It runs once (lifecycle.go's initializeFrom is
	// Uninitialized alone), which is what makes it the safe place to register.
	metrics := NewPersistMetrics(ms)

	// Two starts. Reaching past the second is the assertion: a duplicate registration
	// panics, and that takes down the test binary rather than failing this test.
	for start := 1; start <= 2; start++ {
		if p := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), nil, metrics); p == nil {
			t.Fatalf("start %d built no processor", start)
		}
	}

	// The counterweight. Without it, a constructor that had simply stopped building
	// metrics at all would satisfy everything above — the other way to make a
	// collision impossible, and one that leaves the service exporting nothing.
	//
	// The in-flight gauge is the probe because a Gauge exports a sample as soon as it
	// is built; the loop's counter is a CounterVec, which exports nothing until a
	// message is handled and so could not tell "registered" from "not registered".
	const want = "devicechain_eventmanagement_persist_inflight"
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() == want {
			return
		}
	}
	t.Errorf("the registry does not hold %q; the processor's instruments went somewhere else, or nowhere", want)
}
