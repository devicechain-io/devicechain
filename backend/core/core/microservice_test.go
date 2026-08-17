// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureExit swaps the process-exit hook for the duration of one test and returns
// the codes it was asked to exit with. The stub RETURNS, which the real os.Exit never
// does, so a test also proves the statement after it is harmless — see reportOutcome.
//
// It replaces a package-level variable, so a test using it must not call t.Parallel().
// Nothing enforces that; it is stated because a parallel test would swap the hook out
// from under another one and read someone else's exit codes.
func captureExit(t *testing.T) *[]int {
	t.Helper()
	codes := []int{}
	prev := exitProcess
	exitProcess = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exitProcess = prev })
	return &codes
}

// shutdownDrainDelay resolves the drain window from the environment, falling back
// to the default and tolerating invalid input.
func TestShutdownDrainDelay(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset uses default", false, "", defaultShutdownDrain},
		{"explicit seconds", true, "10", 10 * time.Second},
		{"zero disables drain", true, "0", 0},
		{"empty uses default", true, "", defaultShutdownDrain},
		{"invalid uses default", true, "soon", defaultShutdownDrain},
		{"negative uses default", true, "-3", defaultShutdownDrain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(ENV_SHUTDOWN_DRAIN_SECONDS, tc.val)
			}
			assert.Equal(t, tc.want, shutdownDrainDelay())
		})
	}
}

// A service that refuses to start must say so in its exit status. This is the whole
// point of the outcome channel: before it, Run returned nil here and the container
// reported Completed, which is what an orderly shutdown reports.
func TestRunExitsNonZeroWhenStartupIsRefused(t *testing.T) {
	codes := captureExit(t)

	// An invalid instance id is the earliest fail-closed guard in InitializeAndStart,
	// so this exercises the real Run path without needing a config volume or a broker.
	ms := &Microservice{InstanceId: "not a valid token", outcome: make(chan error, 1)}
	err := ms.Run()

	require.Error(t, err, "Run must surface the startup failure, not swallow it")
	assert.Contains(t, err.Error(), "invalid instance id")
	assert.Equal(t, []int{1}, *codes)
}

// The counterweight: reporting a failure is only useful while an orderly shutdown
// still exits 0. A rolling update terminates every pod on purpose, so a false
// positive here would paint every deployment red.
func TestAnOrderlyShutdownExitsZero(t *testing.T) {
	codes := captureExit(t)

	ms := &Microservice{outcome: make(chan error, 1)}
	ms.finished(nil)
	err := ms.reportOutcome(ms.waitForShutdown())

	assert.NoError(t, err)
	assert.Empty(t, *codes, "an orderly shutdown must not set an exit status")
}

// A teardown that failed is a failure too, end to end — and this covers the WIRING,
// not just the helper. ShutDownNow is the only thing that reports one, and its three
// sends sit within a dozen lines of each other: two failures and one success. A test
// that only exercised finished() would pass with all three reporting the same thing.
func TestAFailedTeardownIsReported(t *testing.T) {
	codes := captureExit(t)

	ms := &Microservice{outcome: make(chan error, 1)}
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
	ms.lifecycle = NewLifecycleManager("test", ms, NewNoOpLifecycleCallbacks())

	// Stop refuses a component that was never initialized, which is the real error
	// this path sees when a SIGTERM lands while the service is still starting up.
	ms.ShutDownNow()

	err := ms.reportOutcome(ms.waitForShutdown())
	require.Error(t, err, "a shutdown that could not stop the service must not report success")
	assert.Contains(t, err.Error(), "uninitialized")
	assert.Equal(t, []int{1}, *codes)
}

// The signal handler is armed before InitializeAndStart is launched, so a SIGTERM
// can race a startup failure and both can report. The first one is the answer: a
// startup that was refused is why the process is ending, whatever tidying followed.
func TestTheFirstOutcomeIsTheReportedOne(t *testing.T) {
	refused := errors.New("startup refused")

	ms := &Microservice{outcome: make(chan error, 1)}
	ms.finished(refused)
	ms.finished(nil)

	assert.Same(t, refused, ms.waitForShutdown())
}

// ...and the later outcome must be DROPPED, not parked on a channel nobody will
// drain. Run receives exactly once, so a second sender that blocks would strand its
// goroutine forever — with the signal handler's goroutine, that is the one holding
// the process's only response to SIGTERM.
func TestALateOutcomeDoesNotBlockItsSender(t *testing.T) {
	ms := &Microservice{outcome: make(chan error, 1)}

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		ms.finished(errors.New("first"))
		ms.finished(errors.New("second"))
		ms.finished(nil)
	}()

	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("a later outcome blocked its sender; nothing will ever drain that send")
	}
}
