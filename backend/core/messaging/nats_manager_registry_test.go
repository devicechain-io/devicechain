// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Two NatsManagers on the SAME functional area must be able to exist in one process.
//
// They could not before: the manager's stream-metrics collectors were registered on
// the process-global default registry under a key composed from the functional area,
// so the second construction reached MustRegister with a collector already there and
// panicked. The hazard belonged to an exported constructor while the only remedy for
// it — a unique-area helper — lived in a _test.go file in this package, so no service
// module could reach it and two of them re-derived it from scratch.
//
// The fixed area here is the assertion. A unique one would pass either way.
func TestTwoManagersOnOneAreaCoexist(t *testing.T) {
	const area = "shared-area"

	build := func() (*NatsManager, *prometheus.Registry) {
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
		reg := prometheus.NewRegistry()
		ms.UseMetricsRegistry(reg)
		// Constructing a manager touches no broker; it builds the manager, its
		// lifecycle wrapper and its collectors.
		return NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(),
			func(*NatsManager) error { return nil }), reg
	}

	// Reaching past the second call is the first half of the assertion: a duplicate
	// registration panics, and that would take down the test binary.
	_, firstReg := build()
	_, secondReg := build()

	// The second half: both managers really did register, on their own registries.
	// Without it this test would also pass if the constructor had simply stopped
	// registering anything at all, which is the other way to make a collision
	// impossible and would leave every service exporting no stream metrics.
	//
	// jetstream_broker_clustered is the probe because it is a plain Gauge, and a
	// Gauge exports a sample as soon as it is built. The replication triple beside it
	// is a GaugeVec, which exports nothing until a stream is sampled — an unconnected
	// manager never samples one, so a vec probe could not tell "registered" from
	// "not registered" here.
	const want = "devicechain_sharedarea_jetstream_broker_clustered"
	for label, reg := range map[string]*prometheus.Registry{"first": firstReg, "second": secondReg} {
		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("gathering the %s manager's registry: %v", label, err)
		}
		found := false
		for _, f := range families {
			if f.GetName() == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the %s manager's registry does not hold %q; its collectors went somewhere else, or nowhere", label, want)
		}
	}
}
