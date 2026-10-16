// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

// These drive the periodic liveness probe (presenceProbeLoop) through the same fakes the
// connect probe's tests use. reconcile_test.go covers reconcileProbe's own rules (only
// rebirthed nodes are judged, an aborted probe declares nobody); what is pinned here is
// WHEN a probe runs on a steady session and WHICH devices it asks about.

// countingReconciler is fakeReconciler for a test with a probe loop running on another
// goroutine: the device-state read is counted under a lock, so the test can wait on the
// count without a data race.
type countingReconciler struct {
	mu      sync.Mutex
	devices []AssertedDevice
	max     uint64
	reads   int
}

func (f *countingReconciler) AssertedActive(_ context.Context, _, _ string) ([]AssertedDevice, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return append([]AssertedDevice(nil), f.devices...), f.max, nil
}

func (f *countingReconciler) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// eventually polls cond for up to wait, returning whether it became true.
func eventually(wait time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

const loopSession = uint64(5_000_000_000_000_000_000)

// loopClient is a client over one group with a counting reconciler, a tiny probe window
// and interval, a real clock, and a recording rebirth publisher — everything the periodic
// probe needs to run and be seen running.
func loopClient(fake *fakeIngester, rec reconcileSource, metrics Metrics) (*Client, *atomic.Int32) {
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"g1"}},
		Broker{}, fake, admitAllSamples{}, time.Now, metrics)
	c.SetReconciler(rec)
	c.probeWindow = 5 * time.Millisecond
	c.probeInterval = 20 * time.Millisecond
	c.subscribe = (&refuseFilters{}).subscribe
	rebirths := new(atomic.Int32)
	c.rebirthPub = func(_, _ string) bool { rebirths.Add(1); return true }
	return c, rebirths
}

func probeMetrics() (Metrics, *prometheus.CounterVec, *prometheus.CounterVec) {
	probes := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "probes"}, []string{"trigger"})
	disconnects := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "disconnects"}, []string{"trigger"})
	return Metrics{PresenceProbes: probes, PresenceProbeDisconnects: disconnects}, probes, disconnects
}

// TestSteadySessionReprobesASilentAssertedDevice is the missed-death hole on a Host that
// never reconnects. The projection keeps reporting one device asserted-active, and the
// device never produces traffic — a death certificate the broker lost, or a DISCONNECT the
// connect probe emitted and the platform did not store. Before the periodic probe, the
// connect probe was the only one that ever ran: one device-state read, one DISCONNECT,
// and then nothing for as long as the session lasted (on main this test fails with
// reads=1, presence=1). Now a later tick reads device-state again and declares the
// still-silent device again, labelled as the periodic probe's verdict.
func TestSteadySessionReprobesASilentAssertedDevice(t *testing.T) {
	fake := &fakeIngester{}
	rec := &countingReconciler{devices: []AssertedDevice{{ExternalId: "g1/node", SessionId: loopSession}}, max: loopSession}
	metrics, probes, disconnects := probeMetrics()
	c, rebirths := loopClient(fake, rec, metrics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := new(atomic.Bool)
	c.onConnected(ctx, newFakeClient(), 1000, make(chan struct{}, 1), up)
	require.True(t, up.Load(), "the session must be fully up for any probe to run")

	// The connect probe: one read, one rebirth, one DISCONNECT.
	require.True(t, eventually(2*time.Second, func() bool { return fake.presenceCount() >= 1 }),
		"the connect probe did not declare the silent device DISCONNECTED")

	// A steadily connected session: the device stays asserted-active and silent.
	// A second probe reads device-state again and declares it again.
	assert.True(t, eventually(2*time.Second, func() bool { return rec.readCount() >= 2 }),
		"device-state was read once, by the connect probe, and never again: a silent death on a steady session is never looked for (reads=%d)", rec.readCount())
	assert.True(t, eventually(2*time.Second, func() bool { return fake.presenceCount() >= 2 }),
		"the silent device was declared DISCONNECTED once, by the connect probe, and never again (presence=%d)", fake.presenceCount())
	cancel()
	c.loopWG.Wait()
	c.probeWG.Wait()

	assert.GreaterOrEqual(t, rebirths.Load(), int32(2), "each probe asks the silent node to rebirth")
	assert.Equal(t, float64(1), testutil.ToFloat64(probes.WithLabelValues(ProbeTriggerConnect)), "exactly one connect probe per session")
	assert.GreaterOrEqual(t, testutil.ToFloat64(probes.WithLabelValues(ProbeTriggerPeriodic)), float64(1), "the periodic probe reached a verdict")
	assert.GreaterOrEqual(t, testutil.ToFloat64(disconnects.WithLabelValues(ProbeTriggerPeriodic)), float64(1), "the periodic verdict was counted under its own trigger")
	for _, ev := range fake.presence {
		assert.False(t, ev.Connected)
		assert.Equal(t, "reconcile-timeout", ev.Reason)
		assert.Greater(t, ev.SessionId, loopSession, "every verdict carries an epoch above the stored session so the projection accepts it")
	}
}

// TestPeriodicProbeSkipsADeviceHeardInsideTheInterval is the storm bound and the mutation
// control for the silence filter and its cutoff: a device that produced traffic inside the
// last interval is alive by its own evidence, so its node is NOT asked to rebirth and it is
// not judged. The stamps come from an injected clock rather than the wall clock: the device
// is stamped seen HALF an interval before the instant every tick reads as "now", which the
// correct cutoff (now minus the interval) accepts and a cutoff of "now" rejects. With the
// wall clock the two stamps could land on the same tick of a coarse clock, and a cutoff of
// "now" would then pass this test by accident — a kill that depends on timing is not a kill.
func TestPeriodicProbeSkipsADeviceHeardInsideTheInterval(t *testing.T) {
	fake := &fakeIngester{}
	rec := &countingReconciler{devices: []AssertedDevice{{ExternalId: "g1/node", SessionId: loopSession}}, max: loopSession}
	metrics, probes, _ := probeMetrics()
	c, rebirths := loopClient(fake, rec, metrics)
	c.resetSeen()

	tickNow := fixedNow()
	c.now = func() time.Time { return tickNow.Add(-c.probeInterval / 2) }
	c.markSeen("g1/node") // heard from half an interval before every tick's "now"
	c.now = func() time.Time { return tickNow }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.presenceProbeLoop(ctx)

	// Several ticks' worth: the loop reads the projection each time and finds nothing to ask.
	require.True(t, eventually(2*time.Second, func() bool { return rec.readCount() >= 3 }), "the loop is ticking")
	assert.True(t, eventually(time.Second, func() bool { return testutil.ToFloat64(probes.WithLabelValues(ProbeTriggerPeriodic)) >= 3 }),
		"a tick with nothing to ask still counts as a probe that reached its verdict")
	cancel()
	c.loopWG.Wait()
	c.probeWG.Wait()
	assert.Equal(t, int32(0), rebirths.Load(), "a device heard from inside the interval was asked to rebirth")
	assert.Equal(t, 0, fake.presenceCount(), "a device heard from inside the interval was declared DISCONNECTED")
}

// TestPeriodicProbeJudgesAgainstItsOwnStart is the mutation control for `since`: a
// device heard from EARLIER in the session but silent since (older than the interval) is
// exactly the one the probe is for. Judged against the session start, as the connect probe
// does, it would count as alive forever — the markSeen below would satisfy the zero
// `since` — and never be declared.
func TestPeriodicProbeJudgesAgainstItsOwnStart(t *testing.T) {
	fake := &fakeIngester{}
	rec := &countingReconciler{devices: []AssertedDevice{{ExternalId: "g1/node", SessionId: loopSession}}, max: loopSession}
	c, rebirths := loopClient(fake, rec, Metrics{})
	c.resetSeen()
	c.markSeen("g1/node") // heard from once, at session start
	time.Sleep(2 * c.probeInterval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.presenceProbeLoop(ctx)
	assert.True(t, eventually(2*time.Second, func() bool { return fake.presenceCount() >= 1 }),
		"a device heard from before the interval and silent since was never declared DISCONNECTED")
	cancel()
	c.loopWG.Wait()
	c.probeWG.Wait()
	assert.GreaterOrEqual(t, rebirths.Load(), int32(1), "its node was asked to rebirth first")
}

// TestPeriodicProbeAnswerInsideTheWindowIsAlive is the other half of `since`: a device
// that answers the probe's rebirth INSIDE the window is alive and is not declared, even
// though it was silent for a whole interval before. The rebirth publisher plays the node
// answering.
func TestPeriodicProbeAnswerInsideTheWindowIsAlive(t *testing.T) {
	fake := &fakeIngester{}
	rec := &countingReconciler{devices: []AssertedDevice{{ExternalId: "g1/node", SessionId: loopSession}}, max: loopSession}
	c, rebirths := loopClient(fake, rec, Metrics{})
	c.resetSeen()
	c.rebirthPub = func(_, _ string) bool {
		rebirths.Add(1)
		c.markSeen("g1/node") // the node answers at once
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.presenceProbeLoop(ctx)
	require.True(t, eventually(2*time.Second, func() bool { return rebirths.Load() >= 3 }), "the silent device is asked every tick")
	cancel()
	c.loopWG.Wait()
	c.probeWG.Wait()
	assert.Equal(t, 0, fake.presenceCount(), "a device that answered its rebirth was declared DISCONNECTED")
}

// TestPeriodicProbeSkipsATickWhileTheConnectProbeIsInItsWindow pins the in-flight guard: a
// probe that is still waiting for answers owns the verdict, and a tick inside its window
// must not rebirth the same nodes again. The only probe a tick can overlap is the CONNECT
// probe, which onConnected runs on a goroutine of its own — the loop runs its own probes
// inline, so it can never overlap itself, and a test that only started the loop would pass
// with the guard deleted (the first mutation pass proved exactly that). So this stages a
// connect probe with a window far longer than the interval and lets several ticks land
// inside it.
func TestPeriodicProbeSkipsATickWhileTheConnectProbeIsInItsWindow(t *testing.T) {
	fake := &fakeIngester{}
	devices := []AssertedDevice{{ExternalId: "g1/node", SessionId: loopSession}}
	rec := &countingReconciler{devices: devices, max: loopSession}
	c, rebirths := loopClient(fake, rec, Metrics{})
	c.resetSeen()
	c.sessions.SetEpochFloor(loopSession + 1)
	c.probeWindow = 10 * c.probeInterval

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The connect probe, as onConnected launches it.
	c.probeWG.Add(1)
	go func() {
		defer c.probeWG.Done()
		c.reconcileProbe(ctx, devices, time.Time{}, ProbeTriggerConnect)
	}()
	require.True(t, eventually(2*time.Second, func() bool { return rebirths.Load() >= 1 }), "the connect probe rebirths")

	go c.presenceProbeLoop(ctx)
	// Several ticks land inside the connect probe's window. Each must stand down BEFORE
	// reading the projection: the staged connect probe above never read it, so any read at
	// all is a tick that did not.
	time.Sleep(5 * c.probeInterval)
	assert.Equal(t, 0, rec.readCount(), "a tick inside the connect probe's window read the projection instead of standing down")
	assert.Equal(t, int32(1), rebirths.Load(), "ticks inside the connect probe's window rebirthed the node again")
	cancel()
	c.loopWG.Wait()
	c.probeWG.Wait()
}

// TestPeriodicProbeEndsWithTheSessionAndDeclaresNothing: cancelling the session context
// stops the loop, and a probe caught mid-window by the cancellation declares nobody.
func TestPeriodicProbeEndsWithTheSessionAndDeclaresNothing(t *testing.T) {
	fake := &fakeIngester{}
	rec := &countingReconciler{devices: []AssertedDevice{{ExternalId: "g1/node", SessionId: loopSession}}, max: loopSession}
	c, rebirths := loopClient(fake, rec, Metrics{})
	c.resetSeen()
	c.probeWindow = 10 * time.Second // long: the cancellation, not the timer, ends it

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.presenceProbeLoop(ctx); close(done) }()
	require.True(t, eventually(2*time.Second, func() bool { return rebirths.Load() >= 1 }), "the first tick probes")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not end with its session")
	}
	assert.Equal(t, 0, fake.presenceCount(), "a probe whose session ended mid-window declared a device DISCONNECTED")
}

// TestNoReconcilerLaunchesNoLoop: a decode-only client (no projection to read) runs no
// periodic probe. Without a reconciler the loop would read nothing and ask nothing, but a
// goroutine that ticks forever for no reason is still a goroutine.
func TestNoReconcilerLaunchesNoLoop(t *testing.T) {
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"g1"}},
		Broker{}, &fakeIngester{}, admitAllSamples{}, time.Now, Metrics{})
	c.subscribe = (&refuseFilters{}).subscribe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.onConnected(ctx, newFakeClient(), 1000, make(chan struct{}, 1), new(atomic.Bool))
	// probeWG.Wait returns at once only if nothing was launched; with a loop launched and
	// the context live it would block until the deadline below.
	waited := make(chan struct{})
	go func() { c.loopWG.Wait(); c.probeWG.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("a probe goroutine was launched on a client with no reconciler")
	}
}

// TestSetPresenceProbeIntervalRefusesNonPositive: zero here can only be a wiring slip (the
// configuration layer never yields it), and a zero interval is "never", the hole the probe
// exists to close, so it panics rather than being read as off.
func TestSetPresenceProbeIntervalRefusesNonPositive(t *testing.T) {
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, nil, nil, time.Now, Metrics{})
	assert.Equal(t, config.DefaultPresenceProbeSeconds*time.Second, c.PresenceProbeInterval(), "a client is built at the platform default")
	assert.Panics(t, func() { c.SetPresenceProbeInterval(0) })
	assert.Panics(t, func() { c.SetPresenceProbeInterval(-time.Second) })
	c.SetPresenceProbeInterval(90 * time.Second)
	assert.Equal(t, 90*time.Second, c.PresenceProbeInterval())
}
