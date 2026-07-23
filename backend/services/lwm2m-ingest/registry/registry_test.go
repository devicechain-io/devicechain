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

// Register resolves with the CREDENTIAL's binding (tenant + external id), never any value
// a client could assert — and emits CONNECTED under that tenant at a fresh epoch. This is
// the D1 tenancy invariant at the Registry seam (the handler ignoring `ep` is pinned by
// the integration test).
func TestRegisterEmitsConnectedWithBindingTenancy(t *testing.T) {
	res, em := okResolver(), &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	_, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	_, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	_, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	_, regId := r.Register(context.Background(), "dev-1", testBinding, 300)

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

	_, regId1 := r.Register(context.Background(), "dev-1", testBinding, 300)
	epoch1 := em.connects()[0].ev.SessionId

	clk.advance(10 * time.Second) // past the replace backoff
	_, regId2 := r.Register(context.Background(), "dev-1", testBinding, 300)

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

	_, regId1 := r.Register(context.Background(), "dev-1", testBinding, 300)
	// No clock advance: still inside the replace backoff.
	result, regId2 := r.Register(context.Background(), "dev-1", testBinding, 300)

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

	_, regId := r.Register(context.Background(), "dev-1", testBinding, 300)

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

	result, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	result, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	_, regId := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	result, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
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

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			r.Register(context.Background(), "dev-1", testBinding, 300)
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
}
