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

// UseMetricsRegistry refuses a call made after a metric has already been built.
//
// A collector is registered where it was constructed and cannot be moved, so a late
// call does not redirect anything: it splits. Whatever was built first stays on the old
// registry while the gatherer reads the new one, and the endpoint answers 200 with those
// metrics missing — the same silent subtraction the owned registry exists to end. Called
// on a Microservice from NewMicroservice, the stranded collectors are the three
// readiness ones, including the `ready` gauge.
//
// The precondition was a sentence in a doc comment before this, which is the shape that
// keeps being wrong here: an invariant asserted by a comment with nothing enforcing it.
func TestUseMetricsRegistryRefusesALateCall(t *testing.T) {
	ms := &Microservice{FunctionalArea: "late-swap"}
	ms.NewCounter("built_first_total", "A metric constructed before the swap.", nil)

	defer func() {
		if recover() == nil {
			t.Fatal("UseMetricsRegistry accepted a call made after a metric was constructed; " +
				"the earlier collector is stranded off the gatherer and nothing says so")
		}
	}()
	ms.UseMetricsRegistry(prometheus.NewRegistry())
}

// The counterweight: the ordinary order is not merely tolerated but is what every caller
// does, so the guard must not fire on it. Without this, "refuses a late call" could be
// satisfied by a method that refuses every call.
func TestUseMetricsRegistryAcceptsTheOrdinaryOrder(t *testing.T) {
	ms := &Microservice{FunctionalArea: "early-swap"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.NewCounter("built_after_total", "A metric constructed after the swap.", nil)

	families, err := ms.metricsReg.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	if len(families) != 1 || families[0].GetName() != "devicechain_earlyswap_built_after_total" {
		t.Errorf("the attached registry holds %d families, want just the counter built after the swap", len(families))
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
