// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// A start after a stop must not re-register these components' metrics.
//
// The NATS manager invokes its oncreate callback on EVERY start (see
// messaging.NewNatsManager), and this service builds both of these inside that
// callback — it has to, because each holds a reader bound to the connection. So a
// collector constructed by either constructor is constructed a second time when the
// service is restarted in place, and promauto's MustRegister panics on the duplicate,
// taking the process down. Building the instruments in the initialize phase and
// handing them in is what makes the second construction free.
//
// This drives the two constructions rather than asserting on the shape of the code,
// so it fails the same way production did: with the panic.
func TestSecondStartDoesNotReRegisterMetrics(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	// 🔴 LOAD-BEARING, NOT SCAFFOLDING. A Microservice built as a bare struct literal
	// has NO registry, and core builds its metrics unregistered in that case — so
	// duplicate registration is impossible and this test would pass against the very
	// defect it exists to catch. Deleting this line disarms the test silently.
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	// The initialize phase. It runs once (lifecycle.go's initializeFrom is
	// Uninitialized alone), which is what makes it the safe place to register.
	resolve := NewResolveMetrics(ms)
	raiseAlarm := NewRaiseAlarmMetrics(ms)

	// Two starts. Reaching past the second is the assertion: a duplicate registration
	// panics, and that takes down the test binary rather than failing this test.
	for start := 1; start <= 2; start++ {
		if p := NewInboundEventsProcessor(ms, nil, nil, nil, core.NewNoOpLifecycleCallbacks(),
			nil, "", 0, resolve); p == nil {
			t.Fatalf("start %d built no inbound events processor", start)
		}
		if c := NewRaiseAlarmConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), nil, nil,
			raiseAlarm); c == nil {
			t.Fatalf("start %d built no raise-alarm consumer", start)
		}
	}

	// The counterweight. Without it, a constructor that had simply stopped building
	// metrics at all would satisfy everything above — the other way to make a
	// collision impossible, and one that leaves the service exporting nothing.
	//
	// One probe per builder, so neither can go missing behind the other. Each is a
	// Gauge or a plain Counter, which export a sample as soon as they are built; the
	// loops' CounterVecs export nothing until a message is handled and so could not
	// tell "registered" from "not registered".
	want := []string{
		"devicechain_devicemanagement_resolve_inflight",
		"devicechain_devicemanagement_resolve_event_time_bounded_total",
		// Underscore, not hyphen. This line read `raise-alarm_inflight` for as long as
		// the consumer passed that loop name, which is how an illegal metric name
		// stayed pinned by a passing test: the registry holds whatever name it is
		// given, and only the exposition — or now the constructor — objects.
		"devicechain_devicemanagement_raise_alarm_inflight",
		"devicechain_devicemanagement_raise_alarm_dead_lettered_total",
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	have := map[string]bool{}
	for _, f := range families {
		have[f.GetName()] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("the registry does not hold %q; those instruments went somewhere else, or nowhere", w)
		}
	}
}
