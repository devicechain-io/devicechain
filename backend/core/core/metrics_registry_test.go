// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// NewMicroservice must hand the microservice a registry of its own, and this pins
// both halves of what that buys.
//
// The first half is that constructing two of them in one process is no longer fatal.
// NewMicroservice registers three collectors under a subsystem derived from
// $DC_MS_FUNCTIONAL_AREA, and on a shared registry the second construction hits
// MustRegister with an AlreadyRegisteredError — a panic, from a constructor whose name
// promises nothing of the sort. That made "one Microservice per process, forever" an
// invariant nothing stated, and it had already been worked around independently in
// three places outside this module.
//
// The second half is that the registry is not merely present but is what the metric
// constructors actually write to. A microservice holding an empty registry it never
// registers into passes any "is there a registry?" check and still exports nothing.
func TestEachMicroserviceGetsItsOwnMetricsRegistry(t *testing.T) {
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "registry-probe")

	// Reaching the next line at all is part of the assertion: a duplicate registration
	// panics, and a panic here takes down the test binary rather than this test.
	first := NewMicroservice(NewNoOpLifecycleCallbacks())
	second := NewMicroservice(NewNoOpLifecycleCallbacks())

	if first.MetricsRegisterer() == nil || second.MetricsRegisterer() == nil {
		t.Fatal("NewMicroservice left the metrics registerer nil; every metric it constructs would register nowhere")
	}
	if first.MetricsRegisterer() == second.MetricsRegisterer() {
		t.Fatal("both microservices share one registry; a second construction is only safe because they do not")
	}

	// devicechain_registryprobe_ready is one of the three NewMicroservice registers,
	// and it is a plain Gauge — it exports a sample as soon as it is built, so its
	// absence means the constructor did not register into this registry.
	const want = "devicechain_registryprobe_ready"
	for label, ms := range map[string]*Microservice{"first": first, "second": second} {
		families, err := ms.metricsReg.Gather()
		if err != nil {
			t.Fatalf("gathering the %s microservice's registry: %v", label, err)
		}
		found := 0
		for _, f := range families {
			if f.GetName() == want {
				found += len(f.GetMetric())
			}
		}
		if found != 1 {
			t.Errorf("the %s microservice's registry holds %d samples of %q, want exactly 1", label, found, want)
		}
	}
}

// A Microservice built as a struct literal has no registry, and its metrics are then
// constructed unregistered rather than falling back to the process default registry.
//
// That is deliberate, and it is what makes the literal safe to build repeatedly: the
// default registry is shared by the whole test binary, so a fallback would restore the
// duplicate-registration panic for exactly the callers who build literals — which is
// every test in this repo that needs a Microservice.
//
// The counterweight is the second half: an unregistered collector still WORKS, so no
// code under test behaves differently. Only a gatherer can tell the difference.
func TestALiteralMicroserviceRegistersNowhereButStillCounts(t *testing.T) {
	ms := &Microservice{FunctionalArea: "literal-probe"}
	if ms.MetricsRegisterer() != nil {
		t.Fatal("a literal Microservice reported a registerer; MetricsRegisterer must return an untyped nil so promauto skips registration")
	}

	counter := ms.NewCounter("literal_probe_total", "Probe metric.", nil)
	counter.Inc()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gathering the default registry: %v", err)
	}
	for _, f := range families {
		if strings.Contains(f.GetName(), "literalprobe") {
			t.Errorf("%q reached the process default registry; two literals sharing an area would then collide", f.GetName())
		}
	}

	// Registered nowhere is not the same as inert. Put it on a throwaway registry and
	// read the value back, so "nothing was exported" cannot be mistaken for "nothing
	// was counted".
	scratch := prometheus.NewRegistry()
	scratch.MustRegister(counter)
	got, err := scratch.Gather()
	if err != nil {
		t.Fatalf("gathering the scratch registry: %v", err)
	}
	if len(got) != 1 || len(got[0].GetMetric()) != 1 || got[0].GetMetric()[0].GetCounter().GetValue() != 1 {
		t.Errorf("unregistered counter did not record its Inc; gathered %+v", got)
	}
}
