// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecoveredBrokerFailureDoesNotParkTheDemoteLoop drives the broker-recovery exit
// through the production launch (launchDemoteLoop), the production recheck and
// restartForRecoveredBroker, and the production stopBrokerPresence.
//
// What it pins: the recovered path reaches the exit carrying "reachable again", and the
// demote loop ends. restartForRecoveredBroker runs on the demote-loop goroutine, which is
// the goroutine that closes rt.stopped, and the teardown the exit triggers runs
// stopBrokerPresence, which waits on rt.stopped.
//
// endProcess is replaced by a stand-in that models Microservice.FailNow's contract: it
// returns at once and runs the teardown (here, the stopBrokerPresence that
// beforeMicroserviceStopped would run) on a goroutine of its own. That the REAL FailNow
// behaves that way is core's property, pinned there
// (TestFailNowFromAGoroutineTheTeardownWaitsOnExitsWithItsOwnError); this service no
// longer carries its own `go`.
func TestRecoveredBrokerFailureDoesNotParkTheDemoteLoop(t *testing.T) {
	type outcome struct {
		err     error
		elapsed time.Duration
	}
	got := make(chan outcome, 1)

	prevEnd, prevPresence := endProcess, brokerPresence
	t.Cleanup(func() { endProcess, brokerPresence = prevEnd, prevPresence })
	endProcess = func(err error) {
		go func() {
			started := time.Now()
			stopBrokerPresence()
			got <- outcome{err: err, elapsed: time.Since(started)}
		}()
	}

	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rt := &presenceRuntime{cancel: cancel, stopped: make(chan struct{})}
	brokerPresence = rt
	launchDemoteLoop(runCtx, rt, &recheckBroker{
		reachable: func(context.Context) bool { return true },
		recovered: restartForRecoveredBroker,
	}, time.Hour, 0)

	select {
	case o := <-got:
		require.Error(t, o.err)
		assert.Contains(t, o.err.Error(), "reachable again",
			"the reason the pod is going away was lost on the way to the exit")
		assert.Less(t, o.elapsed, 2*time.Second,
			"stopBrokerPresence took %v: it waited out its cap on rt.stopped, so the demote loop "+
				"never returned after reporting the exit", o.elapsed)
	case <-time.After(15 * time.Second):
		t.Fatal("a recovered broker never reached the process exit")
	}

	select {
	case <-rt.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the demote loop did not end after its runtime was stopped")
	}
}

// TestFailProcessReachesMicroserviceFailNow is the other half: failProcess hands the
// error to Microservice.FailNow, not to an immediate os.Exit.
//
// The observable is that the error reaches FailNow at all, and that is all it pins. This
// Microservice is a struct literal that never started, so FailNow takes its "nothing to
// tear down" branch and returns; it proves nothing about the teardown itself, which is
// core's to test. What it does rule out is the log.Fatal this replaced — that would end
// the test binary here rather than log FailNow's line.
func TestFailProcessReachesMicroserviceFailNow(t *testing.T) {
	prev := Microservice
	t.Cleanup(func() { Microservice = prev })
	Microservice = &core.Microservice{
		InstanceId:     "event-sources-failnow",
		FunctionalArea: "event-sources-failnow",
		Readiness:      core.NewReadinessGate(),
	}

	logged := logSink.Capture(t)
	failProcess(errors.New("the broker refused this source's subscription"))

	require.Eventually(t, func() bool {
		return strings.Contains(logged.String(), "declared this process unfit to continue")
	}, 5*time.Second, 5*time.Millisecond,
		"failProcess did not reach Microservice.FailNow; logs were:\n"+logged.String())
	assert.Contains(t, logged.String(), "the broker refused this source's subscription",
		"the reason the pod is going away was dropped on the way to FailNow")
}

// A process with no microservice handle cannot be ended, and must say so rather than
// return as though it had been.
func TestFailProcessWithoutAMicroserviceSaysSo(t *testing.T) {
	prev := Microservice
	t.Cleanup(func() { Microservice = prev })
	Microservice = nil

	logged := logSink.Capture(t)
	failProcess(errors.New("boom"))
	require.Eventually(t, func() bool {
		return strings.Contains(logged.String(), "cannot end the process")
	}, 5*time.Second, 5*time.Millisecond, "logs were:\n"+logged.String())
}
