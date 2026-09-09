// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// structLiteralBudget bounds how long one method may take on a Microservice that has
// nothing set. Every one of them either returns or panics immediately; the budget is not
// a performance assertion, it is how a BLOCK is turned into a named failure.
//
// 🔴 THAT IS THE WHOLE REASON THIS TEST DRIVES EACH CALL ON ITS OWN GOROUTINE. The defect
// this file pins is a nil channel, and a nil channel does not return a wrong answer or
// panic — it parks. Called inline, a regression here would not fail this test; it would
// hang the package until the go test timeout fired, ten minutes later, and report a
// goroutine dump naming neither the method nor the expectation it broke.
const structLiteralBudget = 5 * time.Second

// unexportedRowsCovered are the rows for methods reflect cannot enumerate. Reflection
// sees only the exported set, so the unexported ones have to be named by hand — and
// naming them is the point: the list says which internals are driven DIRECTLY rather than
// through a caller, and it is short because driving them directly is usually the wrong
// test. Two of this change's three defects lived in the WIRING between an internal helper
// and its caller, which a direct call cannot see.
//
// The rest — outcomeCh, cancelRoot, readinessGate, exportReady, shutDown, reportOutcome,
// waitForShutdown and metricsGatherer — are each on a path some row above already drives.
var unexportedRowsCovered = []string{"finished"}

// structLiteralCase is one method exercised on &Microservice{}.
type structLiteralCase struct {
	// name is the method as a reader of the Microservice doc comment would look it up.
	name string
	// call invokes it. It must not depend on anything the zero value does not have.
	call func(t *testing.T, ms *Microservice)
	// panics is what this method is DOCUMENTED to do on a struct literal, and the
	// assertion runs in both directions: a method listed as safe must not panic, and a
	// method listed as refusing must not quietly succeed. Only the second half catches a
	// refusal being softened into an invented answer, which is the thing the type's
	// doc comment promises will not happen.
	panics bool
	// wantPanic, when set, must appear in the panic value. It is set for the refusals
	// core raises itself, so a runtime nil dereference cannot be mistaken for one.
	wantPanic string
}

// TestStructLiteralMicroserviceMethods is the gate behind the per-method list in the
// Microservice doc comment. That list is a promise about a construction mode ~30 fixtures
// in this tree use, and before this test nothing measured it: the promise was written as
// a comment on three fields and read as one about the type.
//
// A method added to Microservice and not added here is not covered — that is a real gap
// and no test can close it, so the compensating discipline is that this table and the doc
// comment are edited together.
func TestStructLiteralMicroserviceMethods(t *testing.T) {
	ctx := context.Background()

	cases := []structLiteralCase{
		// --- Safe: no constructor-only field is on the path at all. ---
		{name: "Banner", call: func(t *testing.T, ms *Microservice) { ms.Banner() }},
		{name: "Mux", call: func(t *testing.T, ms *Microservice) { assert.NotNil(t, ms.Mux()) }},
		{name: "RegisterProbes", call: func(t *testing.T, ms *Microservice) { ms.RegisterProbes(nil) }},
		{name: "NewHttpServer", call: func(t *testing.T, ms *Microservice) { assert.NotNil(t, ms.NewHttpServer(0)) }},
		{name: "MetricsSubsystem", call: func(t *testing.T, ms *Microservice) { _ = ms.MetricsSubsystem() }},
		{name: "MetricsHandler", call: func(t *testing.T, ms *Microservice) { assert.NotNil(t, ms.MetricsHandler()) }},
		{name: "UseMetricsRegistry", call: func(t *testing.T, ms *Microservice) { ms.UseMetricsRegistry(nil) }},
		{name: "LoadInstanceConfiguration", call: func(t *testing.T, ms *Microservice) { _ = ms.LoadInstanceConfiguration() }},
		// Safe, and the only one of the pair that is also USEFUL here: it reads the path
		// it is handed rather than the chart's mount point, so a struct literal can load
		// a real document with it. The assertion is on the error rather than discarded,
		// because "did not panic" would be satisfied by a method that ignored its
		// argument — and the argument is the whole difference between the two rows.
		{name: "LoadInstanceConfigurationFrom", call: func(t *testing.T, ms *Microservice) {
			assert.Error(t, ms.LoadInstanceConfigurationFrom("/nonexistent/dc-instance-config"),
				"it must read the path it was given")
		}},
		{name: "LoadMicroserviceConfiguration", call: func(t *testing.T, ms *Microservice) { _ = ms.LoadMicroserviceConfiguration() }},
		{name: "ExecuteInitialize", call: func(t *testing.T, ms *Microservice) { _ = ms.ExecuteInitialize(ctx) }},
		{name: "ExecuteStart", call: func(t *testing.T, ms *Microservice) { assert.NoError(t, ms.ExecuteStart(ctx)) }},
		{name: "ExecuteStop", call: func(t *testing.T, ms *Microservice) { assert.NoError(t, ms.ExecuteStop(ctx)) }},
		{name: "ExecuteTerminate", call: func(t *testing.T, ms *Microservice) { assert.NoError(t, ms.ExecuteTerminate(ctx)) }},

		// MetricsRegisterer must return an UNTYPED nil. Returning the *prometheus.Registry
		// field through a prometheus.Registerer result would produce a non-nil interface
		// wrapping a nil pointer, and the metric constructors below would panic on it.
		{name: "MetricsRegisterer", call: func(t *testing.T, ms *Microservice) {
			assert.Nil(t, ms.MetricsRegisterer(), "must be an untyped nil, not an interface holding a nil registry")
		}},

		// The metric constructors: safe, and specifically safe by being built
		// unregistered. A counter that still counts is what keeps the code under test
		// behaving the same way it does in a service.
		{name: "NewCounter", call: func(t *testing.T, ms *Microservice) {
			c := ms.NewCounter("literal_counter", "h")
			if assert.NotNil(t, c) {
				c.Inc()
			}
		}},
		// The Vec rows drive a labelled child rather than stopping at NotNil, because
		// applying the label names is the only thing that distinguishes these two
		// constructors from the two unlabelled ones — and WithLabelValues panics on an
		// arity its collector was not built with, so the call is the assertion.
		{name: "NewCounterVec", call: func(t *testing.T, ms *Microservice) {
			cv := ms.NewCounterVec("literal_counter_vec", "h", []string{"l"})
			if assert.NotNil(t, cv) {
				cv.WithLabelValues("v").Inc()
			}
		}},
		{name: "NewGauge", call: func(t *testing.T, ms *Microservice) {
			g := ms.NewGauge("literal_gauge", "h")
			if assert.NotNil(t, g) {
				g.Set(1)
			}
		}},
		{name: "NewGaugeVec", call: func(t *testing.T, ms *Microservice) {
			gv := ms.NewGaugeVec("literal_gauge_vec", "h", []string{"l"})
			if assert.NotNil(t, gv) {
				gv.WithLabelValues("v").Set(1)
			}
		}},
		{name: "NewProcessorMetrics", call: func(t *testing.T, ms *Microservice) {
			pm := ms.NewProcessorMetrics("literal")
			if assert.NotNil(t, pm) {
				pm.Start()(ResultOK)
			}
		}},

		// --- Safe, and the outcome channel is why. Each of these sends on it or receives
		// from it, and on a nil channel every one of them parks rather than failing. ---
		{name: "InitializeAndStart", call: func(t *testing.T, ms *Microservice) {
			// Refused by the instance-id guard before the zero lifecycle is reached, so
			// this is an error and not a panic.
			assert.ErrorContains(t, ms.InitializeAndStart(), "invalid instance id")
		}},
		{name: "finished", call: func(t *testing.T, ms *Microservice) {
			ms.finished(errors.New("boom"))
			assert.ErrorContains(t, ms.waitForShutdown(), "boom")
		}},
		{name: "ShutDownNow", call: func(t *testing.T, ms *Microservice) {
			ms.ShutDownNow()
			assert.NoError(t, ms.waitForShutdown(), "a stop before startup finished reports an orderly one")
		}},
		{name: "FailNow", call: func(t *testing.T, ms *Microservice) {
			ms.FailNow(errors.New("unfit"))
			assert.ErrorContains(t, ms.waitForShutdown(), "unfit")
		}},
		{name: "Run", call: func(t *testing.T, ms *Microservice) {
			// Run's own exit is stubbed by the caller below, so this returns.
			assert.ErrorContains(t, ms.Run(), "invalid instance id")
		}},

		// --- Refuse. A plausible answer here is indistinguishable from a real one. ---
		{name: "Initialize", call: func(t *testing.T, ms *Microservice) { _ = ms.Initialize(ctx) }, panics: true},
		{name: "Start", call: func(t *testing.T, ms *Microservice) { _ = ms.Start(ctx) }, panics: true},
		{name: "Stop", call: func(t *testing.T, ms *Microservice) { _ = ms.Stop(ctx) }, panics: true},
		{name: "Terminate", call: func(t *testing.T, ms *Microservice) { _ = ms.Terminate(ctx) }, panics: true},

		{name: "MarkReady", call: func(t *testing.T, ms *Microservice) { _ = ms.MarkReady(nil) },
			panics: true, wantPanic: "no ReadinessGate"},
		{name: "MarkReadyWithoutAuthSurface", call: func(t *testing.T, ms *Microservice) { ms.MarkReadyWithoutAuthSurface() },
			panics: true, wantPanic: "no ReadinessGate"},

		// 🔴 StartAuthGate is the one whose refusal had to MOVE to be worth anything. It
		// used to return cleanly and then panic from the goroutine it had spawned, which
		// no caller and no test can catch — the process simply died. Asserting the panic
		// on THIS stack is asserting that it is raised before the goroutine exists.
		{name: "StartAuthGate", call: func(t *testing.T, ms *Microservice) {
			ms.StartAuthGate(ctx, func(context.Context) (*auth.Validator, error) { return nil, nil })
		}, panics: true, wantPanic: "no ReadinessGate"},
		{name: "StartInstanceAuthGate", call: func(t *testing.T, ms *Microservice) { ms.StartInstanceAuthGate(ctx) },
			panics: true, wantPanic: "no ReadinessGate"},
	}

	// 🔴 A TABLE TEST IS THE SHAPE THAT PASSES BY DOING NOTHING, and this one is more
	// exposed to that than most: a table over methods reports the same clean PASS whether
	// it drove thirty-two of them or none. An empty slice, a filter that matches nothing,
	// a loop over the wrong variable — each of those is a green run asserting that a type
	// nobody exercised behaves correctly.
	//
	// So three things are checked rather than assumed, and the first is asked of the TYPE
	// rather than of a number. A count would pass on a tree where a new exported method was
	// added and an existing row duplicated, since those net to zero; and when it did fail
	// it could only say the total was wrong, not what was missing. reflect enumerates the
	// exported method set, so the guard names the method that has no row.
	rows := map[string]int{}
	for _, tc := range cases {
		rows[tc.name]++
	}
	for name, n := range rows {
		assert.Equal(t, 1, n, "the table has %d rows named %q; a duplicate hides a missing method "+
			"from any check that only counts", n, name)
	}

	msType := reflect.TypeOf(&Microservice{})
	for i := 0; i < msType.NumMethod(); i++ {
		name := msType.Method(i).Name
		assert.Contains(t, rows, name, "Microservice.%s has no row here. Decide what it does on a "+
			"struct literal, add the row, and say so in the Microservice doc comment — the two are "+
			"edited together because nothing can derive prose", name)
	}
	for _, name := range unexportedRowsCovered {
		assert.Contains(t, rows, name, "%s is listed as covered directly and has no row", name)
	}

	// Second: no row names something that is not a method at all. The two Contains loops
	// above are one-directional, so a row for a method since renamed or removed would
	// otherwise sit here forever, testing nothing.
	assert.Len(t, cases, msType.NumMethod()+len(unexportedRowsCovered),
		"the table has rows matching neither an exported method nor unexportedRowsCovered")

	// Third: that the body actually ran for each row. Everything above inspects the slice,
	// and a slice that was built is not a slice that was used.
	ran := 0

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran++
			// Run's failure path calls exitProcess, which is os.Exit in a binary. Stub it
			// for every case, not just Run's, so a regression that reaches it fails here
			// instead of ending the test binary.
			captureExit(t)

			var recovered any
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { recovered = recover() }()
				tc.call(t, &Microservice{})
			}()

			select {
			case <-done:
			case <-time.After(structLiteralBudget):
				t.Fatalf("%s blocked on a Microservice with nothing set; it must return or panic, "+
					"and a nil channel makes it park instead of doing either", tc.name)
			}

			if !tc.panics {
				assert.Nil(t, recovered, "%s is documented as safe on a struct literal", tc.name)
				return
			}
			require.NotNil(t, recovered, "%s is documented as refusing on a struct literal, and it "+
				"returned instead — a refusal softened into an answer the caller cannot tell from a real one", tc.name)
			if tc.wantPanic != "" {
				assert.Contains(t, fmt.Sprint(recovered), tc.wantPanic,
					"%s must refuse with core's own message naming the fix, not a bare nil dereference", tc.name)
			}
		})
	}

	assert.Equal(t, len(cases), ran, "the table was built but the loop did not drive every row")
}

// TestOutcomeChannelIsCreatedOnceAndShared is the counterweight to the lazy creation
// above: "there is always a channel" is only useful while it is always the SAME one.
// Created per call instead, finished would send into a channel waitForShutdown never
// reads, and the process would park exactly as it did on a nil one.
//
// ⚠️ The table above DOES catch that — its finished, ShutDownNow and FailNow rows each
// send and then receive, so a per-call channel parks the receive and trips the budget.
// What this test buys is therefore not coverage but ATTRIBUTION: those rows would report
// "blocked on a Microservice with nothing set", which is the symptom EVERY outcome-channel
// regression shares. This one names the cause, and does it in microseconds rather than
// after three five-second timeouts.
func TestOutcomeChannelIsCreatedOnceAndShared(t *testing.T) {
	ms := &Microservice{}
	first, second := ms.outcomeCh(), ms.outcomeCh()
	require.NotNil(t, first)
	assert.True(t, first == second, "outcomeCh handed out two different channels; finished would "+
		"then send into one waitForShutdown never reads")

	// And it publishes to the field rather than keeping a private one, so the field and
	// the accessor cannot disagree about which channel this Microservice is using.
	assert.True(t, ms.outcome == first, "outcomeCh must publish the channel to the field")
}
