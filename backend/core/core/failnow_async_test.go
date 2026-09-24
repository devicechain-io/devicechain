// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 WHAT THIS FILE PINS: FailNow's CONTRACT, which used to be every caller's. It ran the
// drain and the whole teardown on the caller's goroutine, so every production caller
// wrapped it in its own `go` — five copies of one rule, each with its own comment saying
// why. FailNow now returns at once and runs the teardown itself, and it CLAIMS the process
// before it returns. These tests are where that property lives; the per-service tests
// that used to pin each `go` now defer to them.

// oneSecondBudget sets the teardown budget to its 1s floor, so a teardown that waits on
// its own caller is cut short quickly and visibly rather than after the default grace.
func oneSecondBudget(t *testing.T, ms *Microservice) {
	t.Helper()
	ms.InstanceConfiguration.Infrastructure.Shutdown.TerminationGracePeriodSeconds = 1
	require.Equal(t, time.Second, ms.teardownBudget(), "the precondition: a 1s teardown budget")
}

// THE PIN. A goroutine the teardown waits on — a read loop that a Stopper joins — calls
// FailNow. Inline, the teardown waits for the caller, the caller waits inside FailNow for
// the teardown, and the budget expires: the process still exits non-zero, but the outcome
// it carries is "teardown did not finish", and the reason the pod died is lost.
func TestFailNowFromAGoroutineTheTeardownWaitsOnExitsWithItsOwnError(t *testing.T) {
	codes := captureExit(t)
	callerReturned := make(chan struct{})
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		<-callerReturned // the Stopper joins the goroutine that called FailNow
		return nil
	}
	ms := runnable(t, callbacks)
	oneSecondBudget(t, ms)

	go func() {
		ms.FailNow(errors.New("the leadership supervisor stopped"))
		close(callerReturned)
	}()
	err := ms.reportOutcome(ms.waitForShutdown())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the leadership supervisor stopped",
		"the outcome is not the caller's error, so the exit does not say what failed")
	assert.NotContains(t, err.Error(), "teardown did not finish",
		"the teardown waited on the goroutine that called FailNow: FailNow ran it inline")
	assert.Equal(t, []int{1}, *codes)
}

// FailNow returns while the teardown it started is still running. The Stopper must have
// been ENTERED, or a FailNow that never tore anything down would pass as "returned fast".
func TestFailNowReturnsBeforeTheTeardownRuns(t *testing.T) {
	captureExit(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	ms := runnable(t, callbacks)

	returned := make(chan struct{})
	go func() {
		ms.FailNow(errors.New("unfit"))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("FailNow is still inside the teardown: it did not return until the Stopper did")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("FailNow returned but never started the teardown")
	}
	close(release)
	err := ms.waitForShutdown()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unfit")
}

// 🔴 THE CLAIM HAPPENS BEFORE FailNow RETURNS. A signal handler's shutDown(nil) arriving
// right after must find the process already claimed; if the claim were made on the
// teardown's goroutine, the signal would usually win the swap and the failure would exit
// 0 — reported as an orderly stop.
func TestAShutdownRightAfterFailNowCannotMakeItOrderly(t *testing.T) {
	codes := captureExit(t)
	ms := runnable(t, NewNoOpLifecycleCallbacks())

	ms.FailNow(errors.New("irrecoverable"))
	ms.ShutDownNow() // as a SIGTERM landing a moment later would

	err := ms.reportOutcome(ms.waitForShutdown())
	require.Error(t, err, "a shutdown after FailNow painted an orderly exit over the failure")
	assert.Contains(t, err.Error(), "irrecoverable")
	assert.Equal(t, []int{1}, *codes)
}

// captureCoreLogs points the global logger at a capturing sink through logWriter, the seam
// NewMicroservice writes through, and restores it afterwards.
func captureCoreLogs(t *testing.T) *dctest.LogSink {
	t.Helper()
	savedWriter := logWriter
	sink := dctest.NewLogSink(io.Discard)
	logWriter = sink
	t.Cleanup(func() {
		logWriter = savedWriter
		NewMicroservice(NewNoOpLifecycleCallbacks())
	})
	NewMicroservice(NewNoOpLifecycleCallbacks())
	return sink.Capture(t)
}

// A nil receiver cannot end anything, and must say so rather than panic: the caller is a
// component that has something to report and nowhere else to report it.
func TestFailNowOnANilMicroserviceSaysSoAndReturns(t *testing.T) {
	logs := captureCoreLogs(t)
	var ms *Microservice

	require.NotPanics(t, func() { ms.FailNow(errors.New("boom")) })
	assert.Contains(t, logs.String(), "cannot end the process")
	assert.Contains(t, logs.String(), "boom")
}

// A FailNow that loses the claim must not announce a non-zero exit it will not produce.
// The shutdown that owns the process decides the outcome — here an orderly one.
func TestAFailNowThatLosesTheClaimDoesNotAnnounceANonZeroExit(t *testing.T) {
	codes := captureExit(t)
	logs := captureCoreLogs(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	ms := runnable(t, callbacks)

	go ms.ShutDownNow()
	<-entered
	ms.FailNow(errors.New("too late"))
	close(release)

	require.NoError(t, ms.reportOutcome(ms.waitForShutdown()))
	assert.Empty(t, *codes)
	out := logs.String()
	assert.False(t, strings.Contains(out, "shutting down with a non-zero status"),
		"the losing FailNow announced a non-zero exit the process did not make:\n%s", out)
	assert.Contains(t, out, "a shutdown already owns it",
		"the losing FailNow was dropped without a word")
}
