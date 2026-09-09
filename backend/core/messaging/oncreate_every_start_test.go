// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
)

// The oncreate callback runs on EVERY ExecuteStart. It is not a once-per-process hook,
// and nothing here may assume it is.
//
// That is the contract NewNatsManager's doc comment states, and it is the reason a
// service must not construct anything registry-scoped inside its callback: a Prometheus
// collector built there is built again on a second entry, and a duplicate registration
// panics. Six services did exactly that.
//
// Before this test the contract was a sentence in a doc comment with nothing holding
// it, which is how it came to be read as "invoked on Start" and not as "invoked again
// on the NEXT one". A guard added here — suppressing the second call, mirroring the
// sampler guard immediately below it in ExecuteStart — would look like a tidy fix for
// the panic and would silently leave the service with readers and writers bound to the
// connection the previous stop drained. This test is what refuses that.
//
// 🔴 IT DRIVES ExecuteStart/ExecuteStop DIRECTLY, NOT THE MANAGER. That is not a
// shortcut around the state machine, it is the only way to reach the property: the
// lifecycle refuses a start from Stopped, so the second entry that matters is the one a
// RETRIED start produces — a start whose failure restored the component to Initialized.
// Driving the Execute methods is what that retry looks like from the component's side.
//
// 🔴 THE CALLBACK CREATES NOTHING, which is a limit on what this test can prove and is
// worth stating rather than leaving to be rediscovered: ExecuteStop drains the
// connection and nothing re-establishes one, so a callback that asked for a reader or a
// writer here would spend the retry budget and fail long before reaching anything worth
// asserting on. What this pins is that the callback is entered at all. That the
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
		t.Fatalf("second start: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after a second ExecuteStart oncreate ran %d times, want 2. "+
			"A service's callback constructs the objects bound to the connection, so one that "+
			"runs only on the first entry leaves the service wired to nothing", calls)
	}
}
