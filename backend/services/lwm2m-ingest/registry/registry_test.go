// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
)

// --- fakes -----------------------------------------------------------------

type resolveCall struct {
	tenant     string
	externalId string
	policy     adapter.IngestPolicy
}

// fakeResolver records how it was called and returns a scripted outcome. delay lets a
// concurrency test hold every caller past the pre-lock resolve so they all contend on the
// install (the compare-on-epoch path).
type fakeResolver struct {
	mu      sync.Mutex
	token   string
	outcome adapter.ResolveOutcome
	err     error
	delay   time.Duration
	calls   []resolveCall
}

func (f *fakeResolver) Resolve(_ context.Context, tenant, externalId string, policy adapter.IngestPolicy) (string, adapter.ResolveOutcome, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.calls = append(f.calls, resolveCall{tenant, externalId, policy})
	f.mu.Unlock()
	return f.token, f.outcome, f.err
}

func (f *fakeResolver) lastCall() resolveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

type emitted struct {
	tenant string
	source string
	token  string
	ev     adapter.PresenceEvent
}

// fakeEmitter captures presence writes. failFirst fails the first N emits (to drive the
// emit-failure paths).
type fakeEmitter struct {
	mu        sync.Mutex
	events    []emitted
	failFirst int
}

func (f *fakeEmitter) EmitPresence(_ context.Context, tenant, source, token string, ev adapter.PresenceEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFirst > 0 {
		f.failFirst--
		return errors.New("emit failed")
	}
	f.events = append(f.events, emitted{tenant, source, token, ev})
	return nil
}

func (f *fakeEmitter) all() []emitted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]emitted(nil), f.events...)
}

func (f *fakeEmitter) connects() []emitted {
	var out []emitted
	for _, e := range f.all() {
		if e.ev.Connected {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeEmitter) disconnects() []emitted {
	var out []emitted
	for _, e := range f.all() {
		if !e.ev.Connected {
			out = append(out, e)
		}
	}
	return out
}

// testClock is a settable clock shared by the registry and its epoch source, so an epoch
// and its OccurredAt derive from the same controllable instant.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// --- helpers ---------------------------------------------------------------

var testBinding = config.PskBinding{Tenant: "acme", ExternalId: "plant-a/sensor-1", DeviceTypeToken: "sensor", AutoRegister: true}

// newTestRegistry builds a registry over the fakes, a shared test clock, and a fixed
// regId sequence, with tiny windows so tests run fast. opt lets a test override Options.
func newTestRegistry(res *fakeResolver, em *fakeEmitter, clk *testClock, opt func(*Options)) *Registry {
	var n int
	var mu sync.Mutex
	o := Options{
		Now: clk.now,
		NewRegID: func() string {
			mu.Lock()
			defer mu.Unlock()
			n++
			return "reg-" + string(rune('0'+n))
		},
		MinLifetime:    time.Second,
		DefaultLife:    time.Hour,
		Grace:          time.Second,
		ReplaceBackoff: 5 * time.Second,
	}
	if opt != nil {
		opt(&o)
	}
	epoch := adapter.NewEpochSource(clk.now)
	return New(res, em, epoch, Metrics{}, o)
}

func okResolver() *fakeResolver {
	return &fakeResolver{token: "tok-1", outcome: adapter.ResolveFound}
}

// --- tests -----------------------------------------------------------------

// TestStopIsTerminal pins the L3a eviction-race fix (code-review #3): once Stop is called (a
// leadership eviction), the registry refuses every further Register/Update/Deregister — so a
// request that raced ahead of the term cancellation cannot install an entry (and arm a lifetime
// timer that would fire in a dead term on a standby) — and the active-registrations gauge is zeroed
// so an evicted standby advertises no phantom registrations.
func TestStopIsTerminal(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "active_registrations"})
	epoch := adapter.NewEpochSource(clk.now)
	r := New(res, em, epoch, Metrics{ActiveRegistrations: gauge}, Options{
		Now: clk.now, MinLifetime: time.Second, DefaultLife: time.Hour, Grace: time.Second, ReplaceBackoff: 5 * time.Second,
	})

	// A live registration first: CONNECTED emitted, gauge at 1.
	result, _, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, RegisterOK, result)
	require.Len(t, em.connects(), 1)
	require.Equal(t, float64(1), testutil.ToFloat64(gauge))

	// Stop (eviction) is terminal.
	r.Stop()
	assert.Equal(t, float64(0), testutil.ToFloat64(gauge), "Stop zeroes the active-registrations gauge (no phantom registrations on a standby)")

	// A Register after Stop is refused (5.03) and emits no new presence.
	result2, _, _, _ := r.Register(context.Background(), "dev-2",
		config.PskBinding{Tenant: "acme", ExternalId: "plant-a/sensor-2", AutoRegister: true}, 300)
	assert.Equal(t, RegisterUnavailable, result2, "a Register after Stop is refused")
	assert.Len(t, em.connects(), 1, "a refused Register emits no presence")

	// Update and Deregister after Stop are refused too, and emit nothing.
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", "anything", 300), "an Update after Stop is 4.04")
	assert.False(t, r.Deregister("dev-1", "anything"), "a Deregister after Stop is a no-op")
	assert.Empty(t, em.disconnects(), "no DISCONNECT emitted after Stop")
}

// Register resolves with the CREDENTIAL's binding (tenant + external id), never any value
// a client could assert — and emits CONNECTED under that tenant at a fresh epoch. This is
// the D1 tenancy invariant at the Registry seam (the handler ignoring `ep` is pinned by
// the integration test).
func TestRegisterEmitsConnectedWithBindingTenancy(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, RegisterOK, result)
	require.NotEmpty(t, regId)

	call := res.lastCall()
	assert.Equal(t, "acme", call.tenant, "resolve must use the binding tenant")
	assert.Equal(t, "plant-a/sensor-1", call.externalId, "resolve must use the binding external id")
	assert.Equal(t, SourceLwM2M, call.policy.Source)

	conns := em.connects()
	require.Len(t, conns, 1)
	assert.Equal(t, "acme", conns[0].tenant)
	assert.Equal(t, "tok-1", conns[0].token)
	assert.Equal(t, "plant-a/sensor-1", conns[0].ev.ExternalId)
	assert.True(t, conns[0].ev.Connected)
	assert.Equal(t, uint64(time.Unix(1_700_000_000, 0).UnixNano()), conns[0].ev.SessionId,
		"the session epoch is the mint clock's UnixNano")
}

// Deregister emits DISCONNECTED at the SAME session epoch the Register emitted CONNECTED
// at — so the projection's same-session ordering supersedes the CONNECTED rather than
// leaving the device stuck (the epoch discipline, guard 4).
func TestDeregisterEmitsDisconnectedAtSameEpoch(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	connectEpoch := em.connects()[0].ev.SessionId

	clk.advance(time.Minute)
	require.True(t, r.Deregister("dev-1", regId))

	dis := em.disconnects()
	require.Len(t, dis, 1)
	assert.Equal(t, connectEpoch, dis[0].ev.SessionId, "DISCONNECT must reuse the CONNECT session epoch")
	assert.False(t, dis[0].ev.Connected)
	// The registration is gone.
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", regId, 300))
}

// A lapsed lifetime (no Update) emits DISCONNECTED at the stored epoch and removes the
// registration — the lifetime-timer presence model (guard 5).
func TestExpiryEmitsDisconnectedAtStoredEpoch(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	connectEpoch := em.connects()[0].ev.SessionId

	// Drive the expiry logic directly (deterministic): the arming timer's generation is 0.
	r.onExpiry(regId, 0)

	dis := em.disconnects()
	require.Len(t, dis, 1)
	assert.Equal(t, connectEpoch, dis[0].ev.SessionId)
	assert.Equal(t, "lifetime-expiry", dis[0].ev.Reason)
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", regId, 300), "the entry must be gone after expiry")
}

// An Update before expiry re-arms the timer and bumps the generation, so a stale fire of
// the OLD timer is a no-op (no spurious DISCONNECT for a live device). The generation
// guard is the correctness mechanism.
func TestUpdateSuppressesStaleExpiryFire(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, UpdateOK, r.Update("dev-1", regId, 300)) // bumps generation 0 -> 1

	// The OLD timer (generation 0) fires late: it must NOT disconnect a device an Update
	// just kept alive.
	r.onExpiry(regId, 0)
	assert.Empty(t, em.disconnects(), "a stale-generation expiry fire must emit nothing")

	// The CURRENT timer (generation 1) firing does disconnect.
	r.onExpiry(regId, 1)
	assert.Len(t, em.disconnects(), 1)
}

// A registration is updatable / deregisterable ONLY by the credential that created it: a
// different authenticated identity gets a uniform not-found and cannot touch the
// registration or emit presence for it (per-op authz, guard 3).
func TestForeignIdentityCannotUpdateOrDeregister(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)

	assert.Equal(t, UpdateUnknown, r.Update("dev-2", regId, 300), "a foreign identity must not update")
	assert.False(t, r.Deregister("dev-2", regId), "a foreign identity must not deregister")
	assert.Empty(t, em.disconnects(), "a foreign op must emit no presence")

	// dev-1's registration is intact and still updatable by its owner.
	assert.Equal(t, UpdateOK, r.Update("dev-1", regId, 300))
}

// A re-Register after the replace backoff mints a HIGHER epoch, emits a fresh CONNECTED,
// and replaces the old registration WITHOUT a DISCONNECT (the device re-registered, it did
// not disconnect — the higher epoch supersedes the old session).
func TestReRegisterMintsHigherEpochNoDisconnect(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, regId1, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	epoch1 := em.connects()[0].ev.SessionId

	clk.advance(10 * time.Second) // past the replace backoff
	_, regId2, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)

	require.NotEqual(t, regId1, regId2)
	conns := em.connects()
	require.Len(t, conns, 2, "each register emits a CONNECTED")
	assert.Greater(t, conns[1].ev.SessionId, epoch1, "the re-register must mint a higher epoch")
	assert.Empty(t, em.disconnects(), "a re-register must NOT emit a DISCONNECT for the old session")
	// The old location is gone; only the new one is live.
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", regId1, 300))
	assert.Equal(t, UpdateOK, r.Update("dev-1", regId2, 300))
}

// A rapid re-Register (within the replace backoff) is collapsed: it returns the existing
// location and mints NO new session, so a Register loop cannot produce unbounded distinct
// presence writes (R5 storm control).
func TestRapidReRegisterCollapses(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, regId1, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	// No clock advance: still inside the replace backoff.
	result, regId2, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)

	assert.Equal(t, RegisterOK, result)
	assert.Equal(t, regId1, regId2, "a rapid re-register returns the existing location")
	assert.Len(t, em.connects(), 1, "a collapsed re-register must not emit a second CONNECTED")
}

// clampLifetime raises a too-short lifetime to the minimum and defaults an absent one —
// the `lt` rate knob (R5).
func TestClampLifetime(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.MinLifetime = 60 * time.Second
		o.DefaultLife = 24 * time.Hour
	})
	assert.Equal(t, 24*time.Hour, r.clampLifetime(0), "absent lt takes the default")
	assert.Equal(t, 60*time.Second, r.clampLifetime(5), "a too-short lt is raised to the minimum")
	assert.Equal(t, 300*time.Second, r.clampLifetime(300), "a sufficient lt is kept")
}

// A DISCONNECT is stamped strictly after its session's CONNECTED even when the wall clock
// stepped back within the session — else the projection rejects the older-stamped
// same-session DISCONNECT and the device is stuck CONNECTED (R6).
func TestDisconnectStampAfterClockStepBack(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	connectedAt := time.Unix(1_700_000_500, 0)
	clk := &testClock{t: connectedAt}
	r := newTestRegistry(res, em, clk, nil)

	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)

	// The clock steps BACK (an NTP correction mid-session).
	clk.set(time.Unix(1_700_000_400, 0))
	require.True(t, r.Deregister("dev-1", regId))

	dis := em.disconnects()
	require.Len(t, dis, 1)
	assert.Equal(t, connectedAt.Add(time.Nanosecond).UTC(), dis[0].ev.OccurredAt.UTC(),
		"a step-back DISCONNECT must be stamped 1ns after its CONNECTED, not at the earlier clock")
}

// An unknown device on a credential that does not auto-register is a definitive refusal:
// 4.03-mapped result, no presence emitted, no registration installed (R3).
func TestRegisterUnknownDeviceRefusedNoEmit(t *testing.T) {
	res := &fakeResolver{outcome: adapter.ResolveDropped}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterUnknownDevice, result)
	assert.Empty(t, regId)
	assert.Empty(t, em.all(), "a refused registration must emit nothing")
}

// If the CONNECTED emit fails, Register refuses (retryable) and installs NO registration —
// so the device's own LwM2M retry re-drives it, rather than a 2.01 that recorded a
// presence never emitted (R3/R4).
func TestRegisterEmitFailureRefusesNoEntry(t *testing.T) {
	res := okResolver()
	em := &fakeEmitter{failFirst: 100} // fail every attempt
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterUnavailable, result)
	assert.Empty(t, regId)
	assert.Empty(t, em.connects())
	// Nothing installed: a follow-up Update finds no registration.
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", "reg-1", 300))
}

// When the DISCONNECT emit budget is exhausted (durable store down), Deregister still
// succeeds for the device (its DELETE is honored), the registration is removed, and the
// lost transition is COUNTED (bounded loss) rather than hanging or silently vanishing —
// the L3 reconstruction is the backstop. This exercises the shared emitDisconnect
// exhaustion path (deregister and expiry both use it).
func TestDeregisterEmitFailureStillSucceedsAndCounts(t *testing.T) {
	res := okResolver()
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	dropped := prometheus.NewCounter(prometheus.CounterOpts{Name: "dropped_total"})
	epoch := adapter.NewEpochSource(clk.now)
	r := New(res, em, epoch, Metrics{Dropped: dropped}, Options{Now: clk.now})

	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	// Now every emit fails.
	em.mu.Lock()
	em.failFirst = 100
	em.mu.Unlock()

	assert.True(t, r.Deregister("dev-1", regId), "the device's DELETE is honored even if the DISCONNECT emit fails")
	assert.Empty(t, em.disconnects(), "every disconnect emit failed")
	assert.Equal(t, float64(1), testutil.ToFloat64(dropped), "an exhausted DISCONNECT emit is counted as a drop")
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", regId, 300), "the registration is removed regardless of the emit outcome")
}

// A retryable resolve failure (device-management unreachable) is RegisterUnavailable — the
// device re-Registers; it is not a definitive refusal.
func TestRegisterResolveErrorIsUnavailable(t *testing.T) {
	res := &fakeResolver{err: errors.New("device-management down")}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, _, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterUnavailable, result)
	assert.Empty(t, em.all())
}

// gatedEmitter blocks each emit until the test releases that specific epoch, so a test can
// force a precise install ordering (which is otherwise scheduler-dependent). It records
// the arriving epoch on `arrived` before blocking.
type gatedEmitter struct {
	mu      sync.Mutex
	arrived chan uint64
	gates   map[uint64]chan struct{}
	events  []emitted
}

func newGatedEmitter() *gatedEmitter {
	return &gatedEmitter{arrived: make(chan uint64, 8), gates: map[uint64]chan struct{}{}}
}

func (g *gatedEmitter) gate(epoch uint64) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.gates[epoch]
	if !ok {
		ch = make(chan struct{})
		g.gates[epoch] = ch
	}
	return ch
}

func (g *gatedEmitter) EmitPresence(_ context.Context, tenant, source, token string, ev adapter.PresenceEvent) error {
	g.arrived <- ev.SessionId
	<-g.gate(ev.SessionId)
	g.mu.Lock()
	g.events = append(g.events, emitted{tenant, source, token, ev})
	g.mu.Unlock()
	return nil
}

func (g *gatedEmitter) release(epoch uint64) { close(g.gate(epoch)) }

// Two concurrent Registers for the same identity: the HIGHER epoch installs first, then
// the LOWER-epoch install arrives late. The late lower install MUST be discarded so the
// entry keeps the higher epoch — otherwise every later Deregister/expiry at the stored
// (lower) epoch is rejected by the projection and the device is stuck CONNECTED forever
// (R1, the compare-on-epoch invariant). The gated emitter forces the exact bad
// interleaving deterministically (a scheduler-dependent test would pass even with the
// guard removed — as an earlier version of this test did).
func TestCompareOnEpochDiscardsLowerLateInstall(t *testing.T) {
	res := okResolver()
	em := newGatedEmitter()
	// Real-time epoch source (distinct ordered mints); no replace backoff so neither
	// goroutine is collapsed before it reaches the install.
	r := New(res, em, adapter.NewEpochSource(nil), Metrics{}, Options{ReplaceBackoff: time.Nanosecond})

	type regRet struct {
		regId     string
		epoch     uint64
		establish bool
	}
	var retMu sync.Mutex
	var rets []regRet
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, rid, ep, est := r.Register(context.Background(), "dev-1", testBinding, 300)
			retMu.Lock()
			rets = append(rets, regRet{rid, ep, est})
			retMu.Unlock()
		}()
	}

	// Both goroutines have minted a distinct epoch and are blocked in the emitter.
	a, b := <-em.arrived, <-em.arrived
	hi, lo := a, b
	if lo > hi {
		hi, lo = lo, hi
	}

	// Release the HIGHER epoch first; it installs entry@hi.
	em.release(hi)
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		e := r.byIdentity["dev-1"]
		return e != nil && e.epoch == hi
	}, time.Second, time.Millisecond, "the higher-epoch register must install first")

	// Now release the LOWER epoch; its late install must be DISCARDED by compare-on-epoch.
	em.release(lo)
	wg.Wait()

	r.mu.Lock()
	stored := r.byIdentity["dev-1"]
	r.mu.Unlock()
	require.NotNil(t, stored)
	assert.Equal(t, hi, stored.epoch,
		"the entry must keep the HIGHER epoch; a late lower-epoch install is discarded (R1, no stuck-CONNECTED)")

	// Both callers report the WINNING session's location and epoch; exactly one establishes
	// (the winner). The compare-on-epoch loser is handed the winner's epoch and establish=false
	// so it does not waste Observe I/O on a superseded conn (L2b).
	retMu.Lock()
	defer retMu.Unlock()
	require.Len(t, rets, 2)
	var winners, losers int
	for _, rr := range rets {
		assert.Equal(t, hi, rr.epoch, "both callers report the winning session epoch")
		assert.Equal(t, stored.regId, rr.regId, "both callers report the winning location")
		if rr.establish {
			winners++
		} else {
			losers++
		}
	}
	assert.Equal(t, 1, winners, "exactly one caller (the winner) establishes")
	assert.Equal(t, 1, losers, "the compare-on-epoch loser does not establish")
}

// Register's telemetry-lifecycle returns (epoch, establish) drive the handler's Observe wiring
// (L2b): a fresh install and a storm-collapse both establish (the storm-collapse may be on a new
// conn), while a definitive refusal returns a zero epoch and does not establish.
func TestRegisterReturnsEpochAndEstablish(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, regId, epoch, establish := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, RegisterOK, result)
	require.NotEmpty(t, regId)
	require.NotZero(t, epoch)
	assert.True(t, establish, "a fresh install must establish observations")

	// Storm-collapse (within the replace backoff): same location AND epoch, establish still true.
	result2, regId2, epoch2, establish2 := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterOK, result2)
	assert.Equal(t, regId, regId2, "a storm-collapsed re-register returns the existing location")
	assert.Equal(t, epoch, epoch2, "a storm-collapse keeps the session epoch")
	assert.True(t, establish2, "a storm-collapse must re-establish (the reboot may be on a new conn)")

	// An unknown device (auto-register off) is a definitive refusal: zero epoch, no establish.
	res3 := &fakeResolver{outcome: adapter.ResolveDropped}
	r3 := newTestRegistry(res3, &fakeEmitter{}, clk, nil)
	result3, _, epoch3, establish3 := r3.Register(context.Background(), "dev-2", testBinding, 300)
	assert.Equal(t, RegisterUnknownDevice, result3)
	assert.Zero(t, epoch3)
	assert.False(t, establish3)
}

// OnSessionEnd fires exactly when a session ENDS — a Deregister or a lifetime-expiry — and NOT
// on an install-replace (a reboot re-register), where the successor's Establish supersedes the
// old observations. This is the seam the observe manager hooks to cancel a session's
// observations (L2b). Removing the hook from either end path, or adding it to the replace path,
// reddens an assertion here.
func TestOnSessionEndHook(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	var mu sync.Mutex
	type sessionEnd struct {
		identity string
		epoch    uint64
	}
	var ended []sessionEnd
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.OnSessionEnd = func(identity string, epoch uint64) {
			mu.Lock()
			ended = append(ended, sessionEnd{identity, epoch})
			mu.Unlock()
		}
	})

	_, _, _, _ = r.Register(context.Background(), "dev-1", testBinding, 300)

	// A re-register (replace) supersedes the old session WITHOUT ending it — no hook.
	clk.advance(10 * time.Second)
	_, regId2, epoch2, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	mu.Lock()
	assert.Empty(t, ended, "an install-replace must NOT fire OnSessionEnd")
	mu.Unlock()

	// Deregister ends the session → the hook fires once with the CURRENT session epoch.
	require.True(t, r.Deregister("dev-1", regId2))
	mu.Lock()
	require.Len(t, ended, 1)
	assert.Equal(t, "dev-1", ended[0].identity)
	assert.Equal(t, epoch2, ended[0].epoch)
	ended = ended[:0]
	mu.Unlock()

	// A lifetime expiry also ends the session → the hook fires with the lapsed session's epoch.
	_, regId3, epoch3, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	r.onExpiry(regId3, 0)
	mu.Lock()
	require.Len(t, ended, 1)
	assert.Equal(t, epoch3, ended[0].epoch)
	mu.Unlock()
}

// --- L3b failover reconstruction -------------------------------------------

// entrySnapshot reads the fields of an installed entry under the lock (tests inspect shadow
// installs without racing the timer goroutines).
func (r *Registry) entrySnapshot(regId string) (e entry, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.byRegId[regId]
	if cur == nil {
		return entry{}, false
	}
	return *cur, true
}

func (r *Registry) shadowRegIDFor(identity string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byIdentity[identity]
	if e == nil {
		return "", false
	}
	return e.regId, true
}

// A reconstruction shadow installs into BOTH indexes at a fresh PRE-MINTED epoch, armed for the
// MAX lifetime (F2), with an empty token (resolved lazily at expiry). When its timer lapses with
// no re-Register, it DISCONNECTS the device at that pre-minted epoch. The epoch is pinned to an
// ABSOLUTE value (the L1a lesson: a relative assert.Greater would hide a severed epoch clock).
func TestReconstructShadowExpiryDisconnectsAtFreshEpoch(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, func(o *Options) { o.MaxLife = 2 * time.Hour })

	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	regId, ok := r.shadowRegIDFor("dev-1")
	require.True(t, ok)

	snap, ok := r.entrySnapshot(regId)
	require.True(t, ok)
	assert.True(t, snap.shadow, "a reconstructed entry is a shadow")
	assert.Empty(t, snap.token, "a shadow carries no token (resolved lazily at expiry)")
	assert.Equal(t, 2*time.Hour, snap.lifetime, "a shadow is armed for the MAX lifetime (F2), not a per-device lt")

	// The pre-minted epoch is the clock's UnixNano (no floor set): an ABSOLUTE pin.
	const wantEpoch = uint64(1_700_000_000_000_000_000)
	assert.Equal(t, wantEpoch, snap.epoch, "the shadow epoch is pre-minted from the epoch source")

	// The timer lapses with no re-Register (drive it directly, generation 0).
	r.onExpiry(regId, 0)

	dis := em.disconnects()
	require.Len(t, dis, 1)
	assert.Equal(t, wantEpoch, dis[0].ev.SessionId, "the shadow DISCONNECT carries the pre-minted fresh epoch (F6)")
	assert.Equal(t, "reconstruct-expiry", dis[0].ev.Reason)
	assert.Equal(t, "tok-1", dis[0].token, "the token is lazily resolved at expiry")
	assert.Equal(t, adapter.ResolveFound, res.outcome)
	assert.False(t, res.lastCall().policy.AutoRegister, "the lazy resolve is LOOKUP-ONLY (F5-1): a deleted device is not re-created to mark it offline")
	// The shadow is gone after it disconnected.
	_, ok = r.entrySnapshot(regId)
	assert.False(t, ok)
}

// A shadow NEVER replaces a live registration (B1): a device that is already CONNECTED is always
// fresher than the device-state snapshot the reconstruction reads. Load-bearing because the
// in-term retry (F4) can run reconstruction WHILE the term serves, so a device may have
// re-Registered between the failed first read and the retry.
func TestReconstructShadowSkipsLiveEntry(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	_, liveRegId, liveEpoch, _ := r.Register(context.Background(), "dev-1", testBinding, 300)

	assert.False(t, r.ReconstructShadow("dev-1", testBinding), "a shadow must not replace a live entry")
	snap, ok := r.entrySnapshot(liveRegId)
	require.True(t, ok, "the live entry is untouched")
	assert.False(t, snap.shadow)
	assert.Equal(t, liveEpoch, snap.epoch, "the live entry keeps its own epoch, not a shadow's")
}

// A re-Register supersedes a shadow: the device's real registration mints a HIGHER epoch,
// installLocked drops the shadow (cancelling its timer), and no DISCONNECT is ever emitted for the
// shadow. The device is CONNECTED at the higher epoch — presence AND (on the live conn)
// observations re-establish.
func TestReconstructShadowSupersededByReRegister(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	shadowRegId, ok := r.shadowRegIDFor("dev-1")
	require.True(t, ok)
	shadowSnap, _ := r.entrySnapshot(shadowRegId)

	// The device comes back and re-Registers.
	result, liveRegId, liveEpoch, establish := r.Register(context.Background(), "dev-1", testBinding, 300)
	require.Equal(t, RegisterOK, result)
	assert.True(t, establish, "a real re-Register establishes observations")
	assert.NotEqual(t, shadowRegId, liveRegId, "the live registration has its own location")
	assert.Greater(t, liveEpoch, shadowSnap.epoch, "the live re-Register mints a higher epoch than the shadow")
	require.Len(t, em.connects(), 1, "the re-Register emits a fresh CONNECTED")

	// The shadow was dropped; firing its (already-cancelled) timer does nothing.
	_, ok = r.entrySnapshot(shadowRegId)
	assert.False(t, ok, "the shadow is dropped by the re-Register")
	r.onExpiry(shadowRegId, 0)
	assert.Empty(t, em.disconnects(), "a superseded shadow never emits a DISCONNECT")
}

// Storm control must NOT collapse a genuine re-Register onto a shadow (B3): a shadow never emitted
// a CONNECTED, so handing back its regId+epoch (as recentRegistration does for a young live entry)
// would leave the device with no presence at all. The shadow's connectedAt is backdated past the
// replace backoff so recentRegistration never matches it.
func TestStormControlDoesNotCollapseOntoShadow(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil) // ReplaceBackoff = 5s

	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	shadowRegId, _ := r.shadowRegIDFor("dev-1")

	// Re-Register immediately (no clock advance): well inside the replace backoff. It must NOT be
	// collapsed onto the shadow — it mints a fresh session and emits a real CONNECTED.
	result, liveRegId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterOK, result)
	assert.NotEqual(t, shadowRegId, liveRegId, "a re-Register onto a shadow must not be storm-collapsed")
	assert.Len(t, em.connects(), 1, "the re-Register emits a CONNECTED (a collapse would emit none)")
}

// At shadow expiry the lazy lookup-only resolve returning not-found (the device was deleted during
// the failover blackout) drops the shadow SILENTLY — no DISCONNECT for a device that no longer
// exists (F5-1).
func TestReconstructShadowExpiryResolveNotFoundDropsSilently(t *testing.T) {
	res := &fakeResolver{outcome: adapter.ResolveDropped}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	regId, _ := r.shadowRegIDFor("dev-1")

	r.onExpiry(regId, 0)
	assert.Empty(t, em.all(), "a deleted device produces no DISCONNECT")
	_, ok := r.entrySnapshot(regId)
	assert.False(t, ok, "the shadow is dropped")
}

// The reconstruction path is its OWN backstop, so a shadow whose expiry-DISCONNECT cannot be
// emitted (durable store down) RE-ARMS rather than dropping (F5-2) — otherwise a genuinely-dead
// device would sit CONNECTED until the next failover. Once the store recovers, the re-armed timer
// completes the DISCONNECT.
func TestReconstructShadowExpiryReArmsOnEmitFailure(t *testing.T) {
	res := okResolver()
	em := &fakeEmitter{failFirst: 100} // every emit fails
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	regId, _ := r.shadowRegIDFor("dev-1")

	r.onExpiry(regId, 0)
	assert.Empty(t, em.disconnects(), "the emit failed")
	snap, ok := r.entrySnapshot(regId)
	require.True(t, ok, "a failed shadow DISCONNECT must RE-ARM, not drop (F5-2)")
	assert.True(t, snap.shadow)

	// The store recovers; the re-armed timer fires and completes the DISCONNECT.
	em.mu.Lock()
	em.failFirst = 0
	em.mu.Unlock()
	r.onExpiry(regId, 0)
	assert.Len(t, em.disconnects(), 1, "once the store recovers the re-armed shadow DISCONNECTS")
	_, ok = r.entrySnapshot(regId)
	assert.False(t, ok, "the shadow is removed after a successful DISCONNECT")
}

// A DISCONNECT epoch is minted ABOVE the floor at reconstruction and stored on the shadow, so even
// if the wall clock steps BACK between install and expiry the DISCONNECT still carries a session
// number that exceeds every stored one and wins by session (F6) — immune to the OccurredAt
// fragility a same-session DISCONNECT inherits. Absolute pin on the stored session.
func TestReconstructShadowStepBackStillDisconnects(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	// A high stored session (as if device-state held a large epoch); the reconstruction floors
	// above it, so the shadow's pre-minted epoch exceeds it.
	const storedSession = uint64(1_900_000_000_000_000_000)
	r.epoch.SetFloor(storedSession)
	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	regId, _ := r.shadowRegIDFor("dev-1")
	snap, _ := r.entrySnapshot(regId)
	require.Equal(t, storedSession+1, snap.epoch, "the shadow epoch is minted above the floor")

	// The clock steps far back (an NTP correction across the failover); the DISCONNECT still
	// carries the pre-minted high epoch.
	clk.set(time.Unix(1_600_000_000, 0))
	r.onExpiry(regId, 0)
	dis := em.disconnects()
	require.Len(t, dis, 1)
	assert.Equal(t, storedSession+1, dis[0].ev.SessionId,
		"the shadow DISCONNECT wins by session number regardless of the stepped-back clock (F6)")
	assert.Greater(t, dis[0].ev.SessionId, storedSession, "and exceeds the stored session it supersedes")
}

// An asserted device whose credential was decommissioned (no current binding) is DISCONNECTED now
// (B6): it can never re-handshake, so a shadow timer would only delay the honest terminal state.
// The epoch is minted fresh (above the floor) and the resolve is lookup-only.
func TestReconstructDisconnectEmitsOrphanAtFreshEpoch(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	r.ReconstructDisconnect(context.Background(), "acme", "plant-a/orphan-9")

	dis := em.disconnects()
	require.Len(t, dis, 1)
	assert.Equal(t, "reconstruct-orphan", dis[0].ev.Reason)
	assert.Equal(t, uint64(1_700_000_000_000_000_000), dis[0].ev.SessionId, "the orphan DISCONNECT is at a fresh minted epoch")
	assert.Equal(t, "plant-a/orphan-9", dis[0].ev.ExternalId)
	assert.False(t, res.lastCall().policy.AutoRegister, "the orphan resolve is LOOKUP-ONLY (F5-1)")
}

// An orphan resolve returning not-found emits nothing — the device no longer exists.
func TestReconstructDisconnectResolveNotFoundNoEmit(t *testing.T) {
	res := &fakeResolver{outcome: adapter.ResolveDropped}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	r.ReconstructDisconnect(context.Background(), "acme", "plant-a/gone")
	assert.Empty(t, em.all(), "a deleted orphan produces no DISCONNECT")
}

// clampLifetime caps a too-long lifetime at the MAX ceiling (F2) — including the absent-lt default
// when the ceiling is tightened below it — so no live registration can outlast the shadow timer.
func TestClampLifetimeCeiling(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, func(o *Options) {
		o.MinLifetime = 60 * time.Second
		o.DefaultLife = 24 * time.Hour
		o.MaxLife = time.Hour
	})
	assert.Equal(t, time.Hour, r.clampLifetime(0), "an absent lt defaults then caps at the ceiling")
	assert.Equal(t, time.Hour, r.clampLifetime(7*24*3600), "a too-long lt is capped at the ceiling")
	assert.Equal(t, 300*time.Second, r.clampLifetime(300), "an lt under the ceiling is kept")
	assert.Equal(t, 60*time.Second, r.clampLifetime(5), "a too-short lt is still raised to the floor")
}

// A Stopped (evicted) registry refuses reconstruction too: a shadow install or an orphan
// DISCONNECT in a dead term must not arm a timer or emit on a standby.
func TestReconstructRefusedAfterStop(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)
	r.Stop()

	assert.False(t, r.ReconstructShadow("dev-1", testBinding), "a stopped registry installs no shadow")
	r.ReconstructDisconnect(context.Background(), "acme", "plant-a/x")
	assert.Empty(t, em.all(), "a stopped registry emits no orphan DISCONNECT")
}

// A real Register must UNCONDITIONALLY supersede a reconstruction shadow — even one whose
// pre-minted epoch is HIGHER than the Register's. The in-term retry (F4) installs a shadow while
// serving and mints UNDER the lock, so it can out-mint a racing Register that minted (outside the
// lock) first: without the shadow exemption in compare-on-epoch, the live device would be handed
// the shadow's regId (an empty-token entry) and end up stuck CONNECTED. The gated emitter forces
// the exact interleaving deterministically (Register minted E1 and is blocked in its emit → shadow
// mints E2>E1 → Register unblocks and reaches compare-on-epoch with cur = shadow@E2 >= E1).
func TestReRegisterSupersedesHigherEpochShadow(t *testing.T) {
	res := okResolver()
	em := newGatedEmitter()
	r := New(res, em, adapter.NewEpochSource(nil), Metrics{}, Options{ReplaceBackoff: time.Nanosecond})

	type regRet struct {
		result    RegisterResult
		regId     string
		epoch     uint64
		establish bool
	}
	retCh := make(chan regRet, 1)
	go func() {
		result, rid, ep, est := r.Register(context.Background(), "dev-1", testBinding, 300)
		retCh <- regRet{result, rid, ep, est}
	}()
	e1 := <-em.arrived // the Register minted E1 and is blocked in the emitter, before the table lock

	// While the Register is blocked, the in-term retry installs a shadow — it mints E2 > E1.
	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	r.mu.Lock()
	shadow := r.byIdentity["dev-1"]
	shadowRegId, shadowEpoch, isShadow := shadow.regId, shadow.epoch, shadow.shadow
	r.mu.Unlock()
	require.True(t, isShadow)
	require.Greater(t, shadowEpoch, e1, "the shadow must mint a HIGHER epoch than the blocked Register")

	// Release the Register; it reaches compare-on-epoch with cur = shadow@E2 >= E1.
	em.release(e1)
	got := <-retCh

	assert.Equal(t, RegisterOK, got.result)
	assert.NotEqual(t, shadowRegId, got.regId, "the live device must get its OWN location, never the shadow's")
	assert.True(t, got.establish, "a real Register establishes observations; it did not lose to the shadow")

	r.mu.Lock()
	live := r.byIdentity["dev-1"]
	var liveShadow bool
	var liveToken, liveRegId string
	if live != nil {
		liveShadow, liveToken, liveRegId = live.shadow, live.token, live.regId
	}
	_, shadowStillPresent := r.byRegId[shadowRegId]
	r.mu.Unlock()
	require.NotNil(t, live)
	assert.False(t, liveShadow, "the stored entry is the live registration, not the shadow")
	assert.Equal(t, got.regId, liveRegId)
	assert.NotEmpty(t, liveToken, "the live entry carries a real token (a shadow's is empty)")
	assert.False(t, shadowStillPresent, "the shadow is dropped by the superseding Register")

	// Firing the shadow's cancelled timer must not DISCONNECT the now-live device.
	r.onExpiry(shadowRegId, 0)
	em.mu.Lock()
	var disconnects int
	for _, e := range em.events {
		if !e.ev.Connected {
			disconnects++
		}
	}
	em.mu.Unlock()
	assert.Zero(t, disconnects, "the superseded shadow never DISCONNECTS the live device")
}

// A shadow's regId is never servable (defense-in-depth for the compare-on-epoch exemption): an
// Update or Deregister naming a shadow's regId gets a uniform not-found — so a shadow's day-long
// timer can never be kept alive by an Update, nor its EMPTY token ever reach a Deregister emit.
// The reconstruction counter also increments per shadow (a takeover marker, NIT 6).
func TestShadowRegIdIsNotServable(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	shadows := prometheus.NewCounter(prometheus.CounterOpts{Name: "shadows_total"})
	epoch := adapter.NewEpochSource(clk.now)
	r := New(res, em, epoch, Metrics{ShadowsReconstructed: shadows}, Options{Now: clk.now, NewRegID: func() string { return "shadow-reg" }})

	require.True(t, r.ReconstructShadow("dev-1", testBinding))
	assert.Equal(t, float64(1), testutil.ToFloat64(shadows), "each shadow increments the takeover counter")

	assert.Equal(t, UpdateUnknown, r.Update("dev-1", "shadow-reg", 300), "a shadow's regId is not Updatable")
	assert.False(t, r.Deregister("dev-1", "shadow-reg"), "a shadow's regId is not Deregisterable")
	assert.Empty(t, em.all(), "a rejected shadow Deregister emits nothing (never its empty token)")

	// The shadow is intact and still expires on its own timer.
	r.mu.Lock()
	e := r.byIdentity["dev-1"]
	r.mu.Unlock()
	require.NotNil(t, e)
	assert.True(t, e.shadow)
}
