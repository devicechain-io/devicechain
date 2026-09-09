// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withShutdownConfig sets the drain window and grace period a Microservice derives its
// teardown budget from, and returns the budget that produces.
//
// 🔴 IT CONFIGURES THE SERVICE RATHER THAN OVERRIDING THE BUDGET, and that is the point
// rather than tidiness: the budget is DERIVED, so a test that set it directly would
// drive the enforcement while stepping over the arithmetic that decides the number —
// and the arithmetic is the half that has to track two operator-settable values.
// Everything below therefore asserts against the derived figure, never a literal.
//
// drainSeconds is taken by value and stored as an address because a nil DrainSeconds
// means "absent, use the default" while a zero one means "do not drain at all"; the two
// are different configurations and a plain int cannot express the second.
func withShutdownConfig(t *testing.T, ms *Microservice, graceSeconds, drainSeconds int) time.Duration {
	t.Helper()
	ms.InstanceConfiguration.Infrastructure.Shutdown = config.ShutdownConfiguration{
		DrainSeconds:                  &drainSeconds,
		TerminationGracePeriodSeconds: graceSeconds,
	}
	return ms.teardownBudget()
}

// The budget is the REMAINDER of the shutdown budget, not a number of its own, so it
// has to move with both of the operator's numbers. A constant would be wrong in both
// directions: too long for a raised drain, overrunning the grace period it was supposed
// to fit inside, and no longer for a raised grace period, ignoring what the operator
// said the pod may take.
func TestTheTeardownBudgetTracksTheConfiguredShutdownWindow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		grace, drain int
		want         time.Duration
	}{
		{"the chart defaults", 30, 5, 23 * time.Second},
		{"a raised grace period lengthens it", 120, 5, 113 * time.Second},
		{"a raised drain shortens it", 30, 15, 13 * time.Second},
		{"no drain at all gives teardown the rest", 30, 0, 28 * time.Second},
		{"a grace period too small to divide still leaves something", 1, 0, minShutdownTeardownBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &Microservice{}
			got := withShutdownConfig(t, ms, tc.grace, tc.drain)
			assert.Equal(t, tc.want, got,
				"grace=%d drain=%d: the teardown budget must be what is LEFT of the grace period "+
					"after the drain, less the margin the process needs to report its outcome",
				tc.grace, tc.drain)
		})
	}
}

// An unconfigured Microservice — a struct literal, which around thirty fixtures in this
// tree are — must get the defaults rather than a zero budget. A zero here would abandon
// teardown before it started, in every one of those fixtures and in any service whose
// instance document omits the block.
func TestAnUnconfiguredMicroserviceGetsTheDefaultBudget(t *testing.T) {
	ms := &Microservice{}
	want := time.Duration(config.DefaultTerminationGracePeriodSeconds)*time.Second -
		time.Duration(config.DefaultShutdownDrainSeconds)*time.Second - shutdownTeardownMargin
	assert.Equal(t, want, ms.teardownBudget(),
		"a zero ShutdownConfiguration must read as the defaults, not as a zero budget")
}

// shutDownWithin drives a full shutdown and returns how it ended plus how long the
// call took, refusing to wait longer than limit for either.
//
// 🔴 IT IS ShutDownNow ITSELF THAT HAS TO BE BOUNDED, not just the outcome channel,
// and that is not a precaution — it is what the first negative control on this file
// found. Without the budget, shutDown does not return AT ALL: it is sitting in the
// teardown it can no longer abandon. A test that called it inline and then waited on
// the outcome with a timeout never reached the timeout, so the run hung instead of
// failing, and a hang is neither a pass nor a fail.
//
// Both waits report from THIS goroutine. The spawned one only closes a channel; an
// assertion inside it would be a silent pass, because a failure in a goroutine the
// test is no longer watching cannot fail the test.
func shutDownWithin(t *testing.T, ms *Microservice, limit time.Duration) (error, time.Duration) {
	t.Helper()
	returned := make(chan struct{})
	start := time.Now()
	go func() {
		ms.ShutDownNow()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(limit):
		t.Fatalf("ShutDownNow did not return within %s: teardown is unbounded, so the process "+
			"sits here until the kubelet SIGKILLs it", limit)
	}
	elapsed := time.Since(start)
	select {
	case err := <-ms.outcomeCh():
		return err, elapsed
	case <-time.After(limit):
		t.Fatalf("ShutDownNow returned but reported no outcome within %s, so nothing decides "+
			"the process's exit status", limit)
		return nil, elapsed
	}
}

// A COMPONENT THAT WILL NOT STOP MUST NOT HOLD THE PROCESS PAST THE BUDGET.
//
// Teardown reaches into whatever the service depends on, and shutdown is exactly when
// one of those is likely to be the thing that has failed. Every call used to run on
// context.Background(), so the only bound on the whole sequence was the kubelet's
// grace period — and hitting that means SIGKILL, which reports nothing about which
// component was still working.
//
// What is asserted is the ELAPSED TIME, not merely that the call returned. A version
// that eventually returns after the blocked component gives up would satisfy "it
// returned" while providing none of the bounding this exists for.
func TestTeardownDoesNotOutlastItsBudget(t *testing.T) {
	captureExit(t)

	// A stop that never comes back: a reader blocked on a broker that has stopped
	// answering, a pool draining against a database that is gone.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(ctx context.Context) error {
		<-release
		return nil
	}
	ms := runnable(t, callbacks)
	// The smallest budget the derivation will produce, so this does not spend the
	// chart's default 23s proving a bound that fires at any size.
	budget := withShutdownConfig(t, ms, 1, 0)

	outcome, elapsed := shutDownWithin(t, ms, 30*time.Second)

	assert.Less(t, elapsed, budget+5*time.Second,
		"shutDown took %s against a %s budget; the deadline is not being enforced", elapsed, budget)
	require.Error(t, outcome, "a teardown that was abandoned mid-flight is not an orderly shutdown")
	assert.Contains(t, outcome.Error(), "teardown did not finish",
		"the outcome must say the budget ran out, not merely that something failed")
}

// 🔴 THE COUNTERWEIGHT. A budget that fires early is worse than no budget: it would
// abandon teardown on every ordinary shutdown, leaving readers bound and connections
// undrained, and the test above cannot tell that apart from correct behaviour — it
// only ever asserts that shutDown STOPS waiting.
//
// So this drives the same path with a teardown that is slow but finite, and asserts
// both halves of what must still happen: the budget waits for it, and BOTH lifecycle
// steps run to completion.
func TestAnUnexpiredBudgetStillRunsTeardownInFull(t *testing.T) {
	codes := captureExit(t)

	stopped, terminated := false, false
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(ctx context.Context) error {
		time.Sleep(300 * time.Millisecond)
		stopped = true
		return nil
	}
	callbacks.Terminator.Preprocess = func(ctx context.Context) error {
		terminated = true
		return nil
	}
	ms := runnable(t, callbacks)
	withShutdownConfig(t, ms, 30, 0)

	outcome, _ := shutDownWithin(t, ms, 60*time.Second)

	assert.NoError(t, outcome, "a teardown that finished inside its budget is an orderly shutdown")
	assert.True(t, stopped, "Stop did not run: the budget truncated work it was supposed to allow")
	assert.True(t, terminated, "Terminate did not run, so the teardown was cut short after Stop")
	assert.Equal(t, Terminated, ms.lifecycle.State,
		"the component did not reach Terminated, so teardown did not complete")
	assert.Empty(t, *codes, "an orderly shutdown must not set an exit status")
}

// The budget is a DEADLINE the components can see, not only a stopwatch the caller
// keeps. Passing it down is what lets a component unwind early and cleanly instead of
// being abandoned; without it every component would run on a context that is never
// done, and the select in teardown would be the only thing that ever noticed.
func TestTeardownHandsComponentsADeadline(t *testing.T) {
	captureExit(t)

	var stopDeadline, terminateDeadline time.Time
	var stopHasDeadline, terminateHasDeadline bool
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(ctx context.Context) error {
		stopDeadline, stopHasDeadline = ctx.Deadline()
		return nil
	}
	callbacks.Terminator.Preprocess = func(ctx context.Context) error {
		terminateDeadline, terminateHasDeadline = ctx.Deadline()
		return nil
	}
	ms := runnable(t, callbacks)
	budget := withShutdownConfig(t, ms, 30, 5)

	shutDownWithin(t, ms, 60*time.Second)

	require.True(t, stopHasDeadline,
		"Stop ran on a context with no deadline, so nothing it calls can bound itself against the grace period")
	require.True(t, terminateHasDeadline, "Terminate ran on a context with no deadline")
	assert.WithinDuration(t, time.Now().Add(budget), stopDeadline, 5*time.Second,
		"Stop's deadline is not the DERIVED teardown budget of %s", budget)
	assert.WithinDuration(t, stopDeadline, terminateDeadline, time.Second,
		"Stop and Terminate must share ONE budget; a fresh deadline per step means the total is unbounded")
}
