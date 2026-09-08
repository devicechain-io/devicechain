// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
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

const (
	// microserviceMethods is how many methods are declared on *Microservice. Check it
	// with, and update it from, the sum of:
	//
	//	grep -c '^func (ms \*Microservice)' core/*.go
	microserviceMethods = 40

	// helpersDrivenThroughCallers are the internal ones the table does not call directly
	// because every one of them is on a path a row above already drives: outcomeCh,
	// cancelRoot, readinessGate, exportReady, shutDown, waitForShutdown, reportOutcome
	// and metricsGatherer. Calling them directly would test them in isolation from the
	// wiring, which is where two of this change's three defects actually lived.
	helpersDrivenThroughCallers = 8

	// structLiteralMethodCount is therefore how many rows the table must carry.
	structLiteralMethodCount = microserviceMethods - helpersDrivenThroughCallers
)

// structLiteralCase is one method exercised on &Microservice{}.
type structLiteralCase struct {
	// name is the method as a reader of the Microservice doc comment would look it up.
	name string
	// call invokes it. It must not depend on anything the zero value does not have.
	call func(ms *Microservice)
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
		{name: "Banner", call: func(ms *Microservice) { ms.Banner() }},
		{name: "Mux", call: func(ms *Microservice) { assert.NotNil(t, ms.Mux()) }},
		{name: "RegisterProbes", call: func(ms *Microservice) { ms.RegisterProbes(nil) }},
		{name: "NewHttpServer", call: func(ms *Microservice) { assert.NotNil(t, ms.NewHttpServer(0)) }},
		{name: "MetricsSubsystem", call: func(ms *Microservice) { _ = ms.MetricsSubsystem() }},
		{name: "MetricsHandler", call: func(ms *Microservice) { assert.NotNil(t, ms.MetricsHandler()) }},
		{name: "UseMetricsRegistry", call: func(ms *Microservice) { ms.UseMetricsRegistry(nil) }},
		{name: "LoadInstanceConfiguration", call: func(ms *Microservice) { _ = ms.LoadInstanceConfiguration() }},
		{name: "LoadMicroserviceConfiguration", call: func(ms *Microservice) { _ = ms.LoadMicroserviceConfiguration() }},
		{name: "ExecuteInitialize", call: func(ms *Microservice) { _ = ms.ExecuteInitialize(ctx) }},
		{name: "ExecuteStart", call: func(ms *Microservice) { assert.NoError(t, ms.ExecuteStart(ctx)) }},
		{name: "ExecuteStop", call: func(ms *Microservice) { assert.NoError(t, ms.ExecuteStop(ctx)) }},
		{name: "ExecuteTerminate", call: func(ms *Microservice) { assert.NoError(t, ms.ExecuteTerminate(ctx)) }},

		// MetricsRegisterer must return an UNTYPED nil. Returning the *prometheus.Registry
		// field through a prometheus.Registerer result would produce a non-nil interface
		// wrapping a nil pointer, and the metric constructors below would panic on it.
		{name: "MetricsRegisterer", call: func(ms *Microservice) {
			assert.Nil(t, ms.MetricsRegisterer(), "must be an untyped nil, not an interface holding a nil registry")
		}},

		// The metric constructors: safe, and specifically safe by being built
		// unregistered. A counter that still counts is what keeps the code under test
		// behaving the same way it does in a service.
		{name: "NewCounter", call: func(ms *Microservice) {
			c := ms.NewCounter("literal_counter", "h", nil)
			require.NotNil(t, c)
			c.Inc()
		}},
		{name: "NewCounterVec", call: func(ms *Microservice) {
			assert.NotNil(t, ms.NewCounterVec("literal_counter_vec", "h", []string{"l"}))
		}},
		{name: "NewGauge", call: func(ms *Microservice) {
			g := ms.NewGauge("literal_gauge", "h", nil)
			require.NotNil(t, g)
			g.Set(1)
		}},
		{name: "NewGaugeVec", call: func(ms *Microservice) {
			assert.NotNil(t, ms.NewGaugeVec("literal_gauge_vec", "h", []string{"l"}))
		}},
		{name: "NewProcessorMetrics", call: func(ms *Microservice) {
			pm := ms.NewProcessorMetrics("literal")
			require.NotNil(t, pm)
			pm.Start()(ResultOK)
		}},

		// --- Safe, and the outcome channel is why. Each of these sends on it or receives
		// from it, and on a nil channel every one of them parks rather than failing. ---
		{name: "InitializeAndStart", call: func(ms *Microservice) {
			// Refused by the instance-id guard before the zero lifecycle is reached, so
			// this is an error and not a panic.
			assert.ErrorContains(t, ms.InitializeAndStart(), "invalid instance id")
		}},
		{name: "finished", call: func(ms *Microservice) {
			ms.finished(errors.New("boom"))
			assert.ErrorContains(t, ms.waitForShutdown(), "boom")
		}},
		{name: "ShutDownNow", call: func(ms *Microservice) {
			ms.ShutDownNow()
			assert.NoError(t, ms.waitForShutdown(), "a stop before startup finished reports an orderly one")
		}},
		{name: "FailNow", call: func(ms *Microservice) {
			ms.FailNow(errors.New("unfit"))
			assert.ErrorContains(t, ms.waitForShutdown(), "unfit")
		}},
		{name: "Run", call: func(ms *Microservice) {
			// Run's own exit is stubbed by the caller below, so this returns.
			assert.ErrorContains(t, ms.Run(), "invalid instance id")
		}},

		// --- Refuse. A plausible answer here is indistinguishable from a real one. ---
		{name: "Initialize", call: func(ms *Microservice) { _ = ms.Initialize(ctx) }, panics: true},
		{name: "Start", call: func(ms *Microservice) { _ = ms.Start(ctx) }, panics: true},
		{name: "Stop", call: func(ms *Microservice) { _ = ms.Stop(ctx) }, panics: true},
		{name: "Terminate", call: func(ms *Microservice) { _ = ms.Terminate(ctx) }, panics: true},

		{name: "MarkReady", call: func(ms *Microservice) { _ = ms.MarkReady(nil) },
			panics: true, wantPanic: "no ReadinessGate"},
		{name: "MarkReadyWithoutAuthSurface", call: func(ms *Microservice) { ms.MarkReadyWithoutAuthSurface() },
			panics: true, wantPanic: "no ReadinessGate"},

		// 🔴 StartAuthGate is the one whose refusal had to MOVE to be worth anything. It
		// used to return cleanly and then panic from the goroutine it had spawned, which
		// no caller and no test can catch — the process simply died. Asserting the panic
		// on THIS stack is asserting that it is raised before the goroutine exists.
		{name: "StartAuthGate", call: func(ms *Microservice) {
			ms.StartAuthGate(ctx, func(context.Context) (*auth.Validator, error) { return nil, nil })
		}, panics: true, wantPanic: "no ReadinessGate"},
		{name: "StartInstanceAuthGate", call: func(ms *Microservice) { ms.StartInstanceAuthGate(ctx) },
			panics: true, wantPanic: "no ReadinessGate"},
	}

	// 🔴 A TABLE TEST IS THE SHAPE THAT PASSES BY DOING NOTHING, and this one is more
	// exposed to that than most: a table over methods reports the same clean PASS whether
	// it drove thirty-two of them or none. An empty slice, a filter that matches nothing,
	// a loop over the wrong variable — each of those is a green run asserting that a type
	// nobody exercised behaves correctly.
	//
	// So two things are counted rather than assumed. The first is the table's SIZE against
	// the number of methods declared on *Microservice, which is what makes a method added
	// to the type and not to this table a failing test rather than a silent gap:
	//
	//	grep -c '^func (ms \*Microservice)' core/*.go
	//
	// It is a hand-maintained number and that is deliberate — moving it is the step that
	// makes someone decide what the new method does on a struct literal and write it into
	// the doc comment. Nothing can derive it, since the doc comment is prose.
	require.Len(t, cases, structLiteralMethodCount,
		"every method on *Microservice needs a row here and a line in the Microservice doc "+
			"comment; count them with: grep -c '^func (ms \\*Microservice)' core/*.go")

	// The second is that the body actually ran for each row. len(cases) alone does not say
	// the loop executed — it says the slice was built.
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
				tc.call(&Microservice{})
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
// reads and the process would hang exactly as it did with a nil one — a shape no
// per-method assertion above can see, because each of those calls only one side.
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
