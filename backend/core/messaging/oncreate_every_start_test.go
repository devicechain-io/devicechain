// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
)

// The oncreate callback runs on EVERY start, including a start after a stop.
//
// That is the contract NewNatsManager's doc comment states, and it is the reason a
// service must not construct anything registry-scoped inside its callback: a
// Prometheus collector built there is built again on the second start, and a
// duplicate registration panics. Six services did exactly that.
//
// Before this test the contract was a sentence in a doc comment with nothing holding
// it, which is how it came to be read as "invoked on Start" and not as "invoked again
// on the NEXT start". A guard added here — suppressing the second call, mirroring the
// sampler guard immediately below it in ExecuteStart — would look like a tidy fix for
// the panic and would silently leave the restarted service with readers and writers
// bound to the connection the stop drained. This test is what refuses that.
//
// 🔴 THE CALLBACK CREATES NOTHING. That is deliberate, and it is a limit on what this
// test can prove, so it is worth stating rather than leaving to be rediscovered:
// ExecuteStop drains the connection and a start after it does NOT re-establish one, so
// a callback that asked for a reader or a writer here would spend the retry budget and
// fail long before it reached anything worth asserting on. What this pins is the half
// that runs first on that second start — that the callback is entered at all. That the
// re-entry does not panic on a re-registered collector is pinned per service, beside
// the constructors that used to do it.
func TestOncreateRunsOnEveryStart(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	// newTestManager builds the manager as a struct literal, so it has no stream
	// metrics; ExecuteStart's sampler goroutine samples immediately and would nil-deref
	// without them. Production always comes through NewNatsManager, which builds them.
	nmgr.metrics = newStreamMetrics(nmgr.Microservice)

	calls := 0
	nmgr.oncreate = func(*NatsManager) error {
		calls++
		return nil
	}

	ctx := context.Background()
	if err := nmgr.ExecuteStart(ctx); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after the first start oncreate ran %d times, want 1", calls)
	}
	if err := nmgr.ExecuteStop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := nmgr.ExecuteStart(ctx); err != nil {
		t.Fatalf("start after stop: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after initialize -> start -> stop -> start oncreate ran %d times, want 2. "+
			"A service's callback constructs the objects bound to the connection, so one that "+
			"runs only on the first start leaves a restarted service wired to nothing", calls)
	}
}
