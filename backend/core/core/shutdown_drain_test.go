// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainSeconds is the pointer form the instance config carries, so a test can say
// "no drain" without it reading as "the key was absent".
func drainSeconds(n int) *int { return &n }

// draining builds a Microservice in the state a real one is in when a SIGTERM
// arrives: started, serving, with a readiness gate — which is what the drain is
// gated on — and the shutdown budget it loaded at startup.
func draining(t *testing.T, drain *int, grace int, callbacks LifecycleCallbacks) *Microservice {
	t.Helper()
	ms := runnable(t, callbacks)
	ms.Readiness = NewReadinessGate()
	ms.InstanceConfiguration.Infrastructure.Shutdown = config.ShutdownConfiguration{
		DrainSeconds:                  drain,
		TerminationGracePeriodSeconds: grace,
	}
	return ms
}

// recordDrainSleep swaps the drain's sleeper for one that records what it was asked
// to wait and returns immediately, and restores it afterwards.
//
// Recording rather than sleeping is the point: a test that actually slept the window
// could only afford to configure a small one, and a small window is the single value
// no operator ever writes. This way the assertion is on the number a production
// shutdown would have waited, not on a number chosen to keep the suite quick.
//
// It replaces a package-level variable, so a test using it must not call t.Parallel().
func recordDrainSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	slept := []time.Duration{}
	prev := drainSleep
	drainSleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { drainSleep = prev })
	return &slept
}

// 🔴 THE GATE. The window an operator configured is the window the shutdown waits.
// Before this, the number came out of the environment at shutdown time and the
// instance document had no say in it at all — so a configured window and an
// unconfigured one produced exactly the same shutdown.
func TestTheConfiguredDrainWindowIsTheOneTheShutdownWaits(t *testing.T) {
	slept := recordDrainSleep(t)

	// 12 against a 40s grace period: a value the platform would never pick for
	// itself, so a shutdown that ignored the configuration could not land on it by
	// accident.
	ms := draining(t, drainSeconds(12), 40, NewNoOpLifecycleCallbacks())

	ms.ShutDownNow()

	require.NoError(t, ms.waitForShutdown())
	require.Len(t, *slept, 1, "a started service must drain exactly once")
	assert.Equal(t, 12*time.Second, (*slept)[0],
		"the shutdown must wait the window the instance configuration asked for")
}

// The window is waited BEFORE the teardown, not after it, and readiness is already
// reporting 503 by the time it starts. That ordering is the whole mechanism: the
// endpoint controllers need to see the pod go unready and pull it from Service
// endpoints while it is still able to answer.
func TestTheDrainHappensAfterReadinessFlipsAndBeforeTeardown(t *testing.T) {
	var order []string

	drainingWhenWindowOpened := false
	prev := drainSleep
	t.Cleanup(func() { drainSleep = prev })

	// The callbacks are wired BEFORE the lifecycle manager is built from them: it
	// takes them by value, so a Preprocess assigned afterwards is assigned to a copy
	// nothing calls, and the ordering below would read as "the teardown never ran".
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		order = append(order, "stop")
		return nil
	}
	callbacks.Terminator.Preprocess = func(context.Context) error {
		order = append(order, "terminate")
		return nil
	}

	ms := draining(t, drainSeconds(7), 30, callbacks)
	drainSleep = func(time.Duration) {
		// Observations are RECORDED here and asserted after ShutDownNow returns.
		// Asserting inside a callback would be safe on this path — it runs on the
		// test's own goroutine — but only by accident of who calls it, which is not
		// a property worth resting a gate on.
		drainingWhenWindowOpened = ms.Readiness.Draining()
		order = append(order, "drain")
	}

	ms.ShutDownNow()

	require.NoError(t, ms.waitForShutdown())
	assert.True(t, drainingWhenWindowOpened,
		"readiness must already report 503 when the window opens, or the window waits for a "+
			"removal nothing has been asked to make")
	assert.Equal(t, []string{"drain", "stop", "terminate"}, order,
		"the window is waited before teardown; waiting after it would drain a pod that had "+
			"already closed everything")
}

// 🔴 THE ELAPSED BOUND, and the one test that spends a real window. Everything
// above measures what was handed to drainSleep, which stays green if that variable
// is ever pointed at something that does not actually wait. This one uses the
// production sleeper and measures the clock.
//
// One second is the smallest window the configuration can express, so this is as
// cheap as a real measurement gets.
func TestTheDrainReallyWaitsAndIsNotJustRecorded(t *testing.T) {
	ms := draining(t, drainSeconds(1), 30, NewNoOpLifecycleCallbacks())

	started := time.Now()
	ms.ShutDownNow()
	elapsed := time.Since(started)

	require.NoError(t, ms.waitForShutdown())
	assert.GreaterOrEqual(t, elapsed, time.Second,
		"a one-second window that returns sooner than a second is not being waited")
	assert.Less(t, elapsed, 30*time.Second,
		"a one-second window that takes this long is waiting on something other than the window")
}

// 🔴 THE COUNTERWEIGHT, and the property the drain exists for: through the whole
// window the pod reports itself NOT READY while still answering ordinary requests.
// A drain that stopped serving when it flipped readiness would sever exactly the
// in-flight work it was added to protect, and every assertion above would still
// pass — they only measure how long it waited.
func TestThePodStopsBeingReadyButKeepsServingForTheWholeWindow(t *testing.T) {
	prev := drainSleep
	t.Cleanup(func() { drainSleep = prev })

	// The server is closed by the STOP callback, the way a real service's HTTP
	// server is shut down by its own. Without that, "still serving" would be true of
	// this fixture no matter where the window sat in the sequence, and the second
	// assertion below could not fail for any arrangement of the code.
	var server *httptest.Server
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Stopper.Preprocess = func(context.Context) error {
		server.Close()
		return nil
	}

	ms := draining(t, drainSeconds(9), 30, callbacks)
	ms.Readiness.MarkReadyWithoutAuthSurface()
	ms.RegisterProbes(ms.Readiness)
	ms.Mux().HandleFunc("/work", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	server = httptest.NewServer(ms.Mux())

	var readyzDuringWindow, workDuringWindow int
	drainSleep = func(time.Duration) {
		readyzDuringWindow = statusOf(server.URL + "/readyz")
		workDuringWindow = statusOf(server.URL + "/work")
	}

	// Ready and serving before anything is asked to stop, so the two readings below
	// are a CHANGE rather than a state that was always true.
	require.Equal(t, http.StatusOK, statusOf(server.URL+"/readyz"))
	require.Equal(t, http.StatusTeapot, statusOf(server.URL+"/work"))

	ms.ShutDownNow()

	require.NoError(t, ms.waitForShutdown())
	assert.Equal(t, http.StatusServiceUnavailable, readyzDuringWindow,
		"the pod must report itself unready for the whole window, or the endpoint controllers "+
			"have nothing to react to")
	assert.Equal(t, http.StatusTeapot, workDuringWindow,
		"and it must still answer requests, or the window is a pause with no purpose "+
			"(-1 here means the server was already closed when the window opened)")
}

// An operator who wrote 0 asked for no drain and gets none. It is the value a local
// single-instance run wants, since there is no Service to be pulled out of, and it
// is the reason the key is a pointer: an absent key must not reach here.
func TestAnExplicitZeroWindowSkipsTheDrainEntirely(t *testing.T) {
	slept := recordDrainSleep(t)

	ms := draining(t, drainSeconds(0), 30, NewNoOpLifecycleCallbacks())

	ms.ShutDownNow()

	require.NoError(t, ms.waitForShutdown())
	assert.Empty(t, *slept, "a zero window must not wait at all")
	assert.True(t, ms.Readiness.Draining(), "it must still report itself unready before tearing down")
}

// A Microservice that never loaded an instance document — a struct literal, which is
// a supported way to build one — still drains for the platform default rather than
// reading an absent configuration as "sever everything".
func TestAConfigurationThatWasNeverLoadedStillDrains(t *testing.T) {
	slept := recordDrainSleep(t)

	ms := runnable(t, NewNoOpLifecycleCallbacks())
	ms.Readiness = NewReadinessGate()

	ms.ShutDownNow()

	require.NoError(t, ms.waitForShutdown())
	require.Len(t, *slept, 1)
	assert.Equal(t, config.DefaultShutdownDrainSeconds*time.Second, (*slept)[0])
}

// A service that never finished starting was never in a Service's endpoints, so
// there is nothing to drain and no reason to spend the window before exiting. This
// is existing behaviour; it is pinned here because the drain now reads a
// configuration, and a configured window that started applying to a pod that never
// served would add its length to every interrupted startup.
func TestAServiceThatNeverStartedDoesNotSpendTheWindow(t *testing.T) {
	slept := recordDrainSleep(t)

	ms := starting(t, NewNoOpLifecycleCallbacks())
	ms.Readiness = NewReadinessGate()
	ms.InstanceConfiguration.Infrastructure.Shutdown = config.ShutdownConfiguration{
		DrainSeconds:                  drainSeconds(20),
		TerminationGracePeriodSeconds: 60,
	}

	ms.ShutDownNow()

	require.NoError(t, ms.waitForShutdown())
	assert.Empty(t, *slept, "a pod that was never in a Service's endpoints has nothing to drain")
}

// 🔴 THE OTHER HALF OF MOVING THE KEY. A deployment still setting the removed
// environment variable is refused at startup rather than quietly running on the
// default — which would leave the operator's number having stopped taking effect
// with nothing anywhere to say so.
// It is deliberately built on a lifecycle that WOULD START — inertComponent's
// Initialize and Start both succeed — so that removing the refusal makes this test
// fail on its own assertion rather than crash on a zero lifecycle. A panic is not a
// kill; it says nothing about whether the refusal happened.
//
// InitializeAndStart is the boundary asserted rather than Run, because what Run adds
// on top of a returned error — reporting it and exiting non-zero — is already pinned
// by TestRunExitsNonZeroWhenStartupIsRefused. Driving Run here would hang instead:
// with a startup that succeeds, nothing ever sends an outcome.
func TestStillSettingTheRemovedDrainVariableRefusesStartup(t *testing.T) {
	t.Setenv(ENV_REMOVED_SHUTDOWN_DRAIN_SECONDS, "45")

	ms := &Microservice{InstanceId: "dctest"}
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
	ms.lifecycle = NewLifecycleManager("test", inertComponent{}, NewNoOpLifecycleCallbacks())

	err := ms.InitializeAndStart()

	require.Error(t, err, "a service still carrying the removed variable must refuse to start "+
		"rather than run on the default with nothing to say the value stopped taking effect")
	assert.Contains(t, err.Error(), "DC_SHUTDOWN_DRAIN_SECONDS")
	assert.Contains(t, err.Error(), "45", "the refusal must quote the value that is being ignored")
	assert.Contains(t, err.Error(), "shutdownDrainSeconds",
		"and must name where the value belongs now")
	assert.Equal(t, Uninitialized, ms.lifecycle.State,
		"the refusal must land before the lifecycle is entered")
}

// ...and the counterweight, because refusing a set variable is only useful while a
// service that does not set it starts normally. Without this, the refusal could be
// unconditional and nothing here would notice.
func TestAnUnsetDrainVariableDoesNotBlockStartup(t *testing.T) {
	ms := &Microservice{InstanceId: "dctest"}
	ms.rootCtx, ms.cancel = context.WithCancel(context.Background())
	ms.lifecycle = NewLifecycleManager("test", inertComponent{}, NewNoOpLifecycleCallbacks())

	require.NoError(t, ms.InitializeAndStart())
}

// statusOf issues a GET and returns its status code, failing the test rather than
// returning a plausible zero if the request could not be made at all.
func statusOf(url string) int {
	resp, err := http.Get(url) //nolint:gosec // a httptest URL built in this test
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
