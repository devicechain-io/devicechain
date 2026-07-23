// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package observe

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
)

// --- fakes -----------------------------------------------------------------

// fakeObservation records whether it was cancelled (a Cancel is the deregister the manager
// issues when a session is superseded, ended, or an observation terminates).
type fakeObservation struct {
	mu       sync.Mutex
	canceled bool
}

func (o *fakeObservation) Cancel(_ context.Context, _ ...message.Option) error {
	o.mu.Lock()
	o.canceled = true
	o.mu.Unlock()
	return nil
}

func (o *fakeObservation) Canceled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.canceled
}

// fakeConn is a mux.Conn that records Observe requests, stores each observation's notify
// callback so a test can deliver a Notify on the read loop (deliver), and drives the
// AddOnClose reap on close. It embeds mux.Conn so the ~20 methods the manager never calls are
// nil (calling one panics — deliberately, so an unexpected transport call is loud).
type fakeConn struct {
	mux.Conn
	id          int
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	shuttingDwn bool // ctx cancelled + onClose consumed but done not yet closed (the go-coap teardown window)
	onClose     []func()
	notify      map[string]func(*pool.Message)
	obs         map[string]*fakeObservation
	seen        []string
	refuse      map[string]bool // paths whose DoObserve fails (a 1.0-only client's 4.06)
	oneShot     map[string]bool // paths the device answers 2.05-without-observe (a dead one-shot handle)
	gate        chan struct{}   // if set, DoObserve blocks on it after creating the obs (interleaving hook)
	arrived     chan struct{}   // signalled when a gated DoObserve has created its obs and is about to block
}

func newFakeConn(id int) *fakeConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeConn{
		id:      id,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		notify:  map[string]func(*pool.Message){},
		obs:     map[string]*fakeObservation{},
		refuse:  map[string]bool{},
		oneShot: map[string]bool{},
	}
}

func (c *fakeConn) Context() context.Context { return c.ctx }

func (c *fakeConn) NewObserveRequest(ctx context.Context, path string, _ ...message.Option) (*pool.Message, error) {
	m := pool.NewMessage(ctx)
	m.SetCode(codes.GET)
	m.SetObserve(0)
	_ = m.SetPath(path)
	return m, nil
}

func (c *fakeConn) ReleaseMessage(*pool.Message) {}

func (c *fakeConn) DoObserve(req *pool.Message, observeFunc func(*pool.Message)) (mux.Observation, error) {
	path, _ := req.Path()
	c.mu.Lock()
	if c.refuse[path] {
		c.mu.Unlock()
		return nil, errors.New("4.06 Not Acceptable")
	}
	o := &fakeObservation{canceled: c.oneShot[path]} // a one-shot resource yields an already-cancelled handle
	c.seen = append(c.seen, path)
	c.notify[path] = observeFunc
	c.obs[path] = o
	gate, arrived := c.gate, c.arrived
	c.mu.Unlock()
	if gate != nil {
		if arrived != nil {
			arrived <- struct{}{}
		}
		<-gate // block mid-establish so a test can interleave a competing Establish/Cancel
	}
	return o, nil
}

func (c *fakeConn) Done() <-chan struct{} { return c.done }

func (c *fakeConn) AddOnClose(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shuttingDwn {
		return // onClose list already consumed by shutdown — mirror go-coap: this never fires
	}
	select {
	case <-c.done:
		return
	default:
		c.onClose = append(c.onClose, f)
	}
}

func (c *fakeConn) Close() error { c.close(); return nil }

// beginShutdown models the go-coap teardown WINDOW: Close()→cancel(ctx) has run and onClose has
// been consumed (popOnClose), but close(Done()) has NOT happened yet. A reap registered now is
// lost, and Done() is still open — only Context().Done() reveals the death.
func (c *fakeConn) beginShutdown() {
	c.mu.Lock()
	c.cancel()
	c.shuttingDwn = true
	c.onClose = nil
	c.mu.Unlock()
}

func (c *fakeConn) close() {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	c.cancel() // go-coap cancels the ctx first (Close), before running callbacks and closing done
	c.shuttingDwn = true
	fns := append([]func(){}, c.onClose...)
	c.onClose = nil
	close(c.done)
	c.mu.Unlock()
	for _, f := range fns {
		f() // fire the reap synchronously so the test is deterministic
	}
}

// deliver invokes an observed path's notify callback, simulating a Notify on the read loop.
func (c *fakeConn) deliver(path string, msg *pool.Message) {
	c.mu.Lock()
	fn := c.notify[path]
	c.mu.Unlock()
	if fn != nil {
		fn(msg)
	}
}

func (c *fakeConn) observedPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func (c *fakeConn) observation(path string) *fakeObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.obs[path]
}

// ingestCall records one Ingest so a test can assert the correlation (tenant/policy/externalId)
// and the decoded samples.
type ingestCall struct {
	tenant     string
	policy     adapter.IngestPolicy
	externalId string
	samples    []adapter.Sample
}

type fakeIngester struct {
	mu    sync.Mutex
	calls []ingestCall
	err   error
}

func (f *fakeIngester) Ingest(_ context.Context, tenant string, policy adapter.IngestPolicy, externalId string, samples []adapter.Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ingestCall{tenant, policy, externalId, append([]adapter.Sample(nil), samples...)})
	return f.err
}

func (f *fakeIngester) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeIngester) last() ingestCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// --- helpers ---------------------------------------------------------------

var testTarget = Target{
	Tenant:     "acme",
	ExternalId: "plant-a/sensor-1",
	Policy:     adapter.IngestPolicy{Source: "lwm2m", DeviceTypeToken: "sensor", AutoRegister: true},
}

// newHarness builds an UNGATED Manager (nil limiter) over a fake ingester with real
// (assertable) metrics and tiny timeouts. The Now clock is fixed but unused by the
// absolute-time SenML the tests send. Gating tests use newGatedHarness.
func newHarness(t *testing.T) (*Manager, *fakeIngester, Metrics) {
	t.Helper()
	m, ing, _, metrics := newGatedHarness(t, nil)
	return m, ing, metrics
}

// newGatedHarness is newHarness with a caller-supplied limiter (a *fakeLimiter for the L2c
// gating tests, or nil for ungated). It also returns the limiter for assertion convenience.
func newGatedHarness(t *testing.T, limiter ingestLimiter) (*Manager, *fakeIngester, ingestLimiter, Metrics) {
	t.Helper()
	ing := &fakeIngester{}
	metrics := Metrics{
		NotifiesReceived:        prometheus.NewCounter(prometheus.CounterOpts{Name: "notifies_received"}),
		DecodeFailures:          prometheus.NewCounter(prometheus.CounterOpts{Name: "decode_failures"}),
		UnknownContentFormat:    prometheus.NewCounter(prometheus.CounterOpts{Name: "unknown_cf"}),
		ObserveEstablishRefused: prometheus.NewCounter(prometheus.CounterOpts{Name: "establish_refused"}),
		TerminalNotifications:   prometheus.NewCounter(prometheus.CounterOpts{Name: "terminal"}),
		SamplesTruncated:        prometheus.NewCounter(prometheus.CounterOpts{Name: "samples_truncated"}),
		IngestDropped:           prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_dropped"}),
		ActiveObservations:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "active_observations"}),
	}
	m := NewManager(ing, limiter, metrics, Options{
		Now:            func() time.Time { return time.Unix(1_700_000_500, 0).UTC() },
		ObserveTimeout: time.Second,
		CancelTimeout:  time.Second,
		IngestTimeout:  time.Second,
	})
	return m, ing, limiter, metrics
}

// fakeLimiter is a scripted ingest limiter: allowMessage / allowSamples control admission, and
// it records what it was asked so a test can assert the CHARGE (e.g. samples charged with the
// decoded count, terminal notifications never charged).
type fakeLimiter struct {
	allowMessage bool
	allowSamples bool
	messageCalls int
	sampleCharge []int // one entry per AllowSamples call: the n it was charged
}

func (f *fakeLimiter) AllowMessage(tenant string) bool {
	f.messageCalls++
	return f.allowMessage
}

func (f *fakeLimiter) AllowSamples(tenant string, n int) bool {
	f.sampleCharge = append(f.sampleCharge, n)
	return f.allowSamples
}

func senmlNotify(body string) *pool.Message {
	m := pool.NewMessage(context.Background())
	m.SetCode(codes.Content)
	m.SetContentFormat(message.AppSenmlJSON)
	m.SetBody(bytes.NewReader([]byte(body)))
	return m
}

func terminalNotify() *pool.Message {
	m := pool.NewMessage(context.Background())
	m.SetCode(codes.NotFound) // 4.04 — the observed instance was deleted (RFC 7641 terminal)
	return m
}

// white-box slot accessors (this is a package-internal test).
func hasSlot(m *Manager, identity string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.slots[identity]
	return ok
}

func slotEpoch(m *Manager, identity string) (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.slots[identity]
	if !ok {
		return 0, false
	}
	return s.epoch, true
}

func slotObsCount(m *Manager, identity string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.slots[identity]
	if !ok {
		return -1
	}
	return len(s.obs)
}

func slotConn(m *Manager, identity string) mux.Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.slots[identity]
	if !ok {
		return nil
	}
	return s.conn
}

// --- tests -----------------------------------------------------------------

// Guard 6 (baseline): a 2.05 SenML Notify on an observed instance decodes and ingests under
// the binding's tenancy (tenant/policy/externalId), with absolute-literal sample values — the
// full Observe→decode→ingest path. This transitively pins that the initial 2.05 yields a batch.
func TestNotifyDecodesAndIngestsWithBindingTenancy(t *testing.T) {
	m, ing, _ := newHarness(t)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))
	require.Equal(t, []string{"/3303/0"}, c.observedPaths())

	c.deliver("/3303/0", senmlNotify(`[{"bn":"/3303/0/","n":"5700","v":21.5,"bt":1700000500}]`))

	require.Equal(t, 1, ing.callCount())
	call := ing.last()
	assert.Equal(t, "acme", call.tenant)
	assert.Equal(t, "plant-a/sensor-1", call.externalId)
	assert.Equal(t, "lwm2m", call.policy.Source)
	require.Len(t, call.samples, 1)
	assert.Equal(t, "/3303/0/5700", call.samples[0].Name)
	assert.Equal(t, 21.5, call.samples[0].Value)
	assert.Equal(t, int64(1_700_000_500_000), call.samples[0].Time, "an absolute SenML time resolves to ms")
}

// Guard 1: a higher-epoch Register supersedes the current session (cancels its observations),
// and an equal-epoch establish on the SAME live conn is a no-op. Severing the `epoch > cur.epoch`
// term reddens the "epoch 2 wins" assertion.
func TestHigherEpochWinsEqualEpochSameConnNoOp(t *testing.T) {
	m, _, _ := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))
	obs1 := c1.observation("/3303/0")
	require.NotNil(t, obs1)

	c2 := newFakeConn(2)
	require.True(t, m.Establish("id-1", 2, c2, testTarget, []string{"/3303/0"}), "a higher epoch must win")
	require.Eventually(t, obs1.Canceled, time.Second, time.Millisecond, "the superseded session's obs must be cancelled")
	ep, ok := slotEpoch(m, "id-1")
	require.True(t, ok)
	assert.Equal(t, uint64(2), ep)

	assert.False(t, m.Establish("id-1", 2, c2, testTarget, []string{"/3303/0"}), "equal epoch on the same live conn is a no-op")
}

// Guard 2 (flagship): a conn close PARKS the slot (keeps epoch+paths), and the device's next
// Update re-establishes the stored paths on the new conn — the queue-mode-sleeper heal. Making
// reap delete the slot instead of parking reddens the "paths follow to the new conn" assertion.
func TestReapParksThenUpdateReestablishesOnNewConn(t *testing.T) {
	m, ing, _ := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 5, c1, testTarget, []string{"/3303/0"}))

	c1.close() // idle reaper / NAT timeout closes the sleeper's conn
	require.True(t, hasSlot(m, "id-1"), "conn close must PARK the slot, not delete it")
	assert.Nil(t, slotConn(m, "id-1"), "the parked slot drops the dead conn")

	c2 := newFakeConn(2)
	m.Reestablish("id-1", c2, testTarget) // the sleeper wakes on a new conn and sends Update
	require.Equal(t, []string{"/3303/0"}, c2.observedPaths(), "the sleeper's paths must follow it to the new conn")

	c2.deliver("/3303/0", senmlNotify(`[{"bn":"/3303/0/","n":"5700","v":7,"bt":1700000500}]`))
	assert.Equal(t, 1, ing.callCount(), "telemetry resumes on the new conn")
}

// Guard 3: a stale Cancel for an OLD session (a slow expiry hook) must not kill the successor
// that already took over the identity. Loosening the `cur.epoch > epoch` protection reddens
// the "slot still holds the successor" assertion (deterministic — slot removal is synchronous).
func TestStaleCancelDoesNotKillSuccessor(t *testing.T) {
	m, ing, _ := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))
	c2 := newFakeConn(2)
	require.True(t, m.Establish("id-1", 2, c2, testTarget, []string{"/3303/0"}))
	obs2 := c2.observation("/3303/0")

	m.Cancel("id-1", 1) // the epoch-1 session's expiry hook fires late

	ep, ok := slotEpoch(m, "id-1")
	require.True(t, ok, "the successor slot must survive a stale cancel")
	assert.Equal(t, uint64(2), ep)
	assert.False(t, obs2.Canceled(), "the successor's observation must not be cancelled")
	c2.deliver("/3303/0", senmlNotify(`[{"bn":"/3303/0/","n":"5700","v":1,"bt":1700000500}]`))
	assert.Equal(t, 1, ing.callCount())
}

// Guard 4a: a terminal non-2.05 notification (4.04) yields no sample AND drops the handle
// (cancels it, decrements the gauge). Treating a non-2.05 as decodable reddens the "handle
// dropped" assertion.
func TestTerminalNotificationDropsHandleNoIngest(t *testing.T) {
	m, ing, metrics := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))
	obs := c1.observation("/3303/0")

	c1.deliver("/3303/0", terminalNotify())

	require.Eventually(t, func() bool { return slotObsCount(m, "id-1") == 0 }, time.Second, time.Millisecond,
		"a terminal notification must drop the observation handle")
	require.Eventually(t, obs.Canceled, time.Second, time.Millisecond)
	assert.Equal(t, 0, ing.callCount(), "a terminal notification must not ingest")
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.TerminalNotifications))
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ActiveObservations))
}

// Guard 4b: dropObservation is guarded by CONN as well as epoch — a stale terminal arriving on
// a superseded conn (an equal-epoch conn swap left the old conn's token handler live) must not
// remove the successor's handle. Severing the `cur.conn != conn` guard reddens this. Called
// directly (white-box) so the guard is exercised deterministically, not through a race.
func TestStaleTerminalFromOldConnDoesNotDropSuccessor(t *testing.T) {
	m, _, _ := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 5, c1, testTarget, []string{"/3303/0"}))
	c2 := newFakeConn(2) // device re-handshaked on c2 and Updated (SAME session epoch 5)
	require.True(t, m.Establish("id-1", 5, c2, testTarget, []string{"/3303/0"}))
	require.True(t, slotConn(m, "id-1") == c2)

	m.dropObservation("id-1", 5, c1, "/3303/0") // a stale terminal on the OLD conn's closure

	assert.Equal(t, 1, slotObsCount(m, "id-1"), "a stale terminal from the old conn must not drop the successor's handle")
	assert.False(t, c2.observation("/3303/0").Canceled())
}

// Guard 5: a retryable ingest error in the Notify path is COUNTED and dropped — never retried
// (which would stall the conn) or panicked. A retry loop would make callCount > 1.
func TestRetryableIngestErrorCountedNotRetried(t *testing.T) {
	m, ing, metrics := newHarness(t)
	ing.err = errors.New("nats unreachable")
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))

	c1.deliver("/3303/0", senmlNotify(`[{"bn":"/3303/0/","n":"5700","v":1,"bt":1700000500}]`))

	assert.Equal(t, 1, ing.callCount(), "the notify path must call ingest exactly once (no retry)")
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.IngestDropped))
}

// Guard 7: once a session is ended (tombstoned by Cancel), a late in-flight Establish for that
// same epoch must install nothing — no zombie observation for a device the registry called
// DISCONNECTED. Severing the tombstone check reddens this.
func TestTombstoneBlocksLateEstablishAfterCancel(t *testing.T) {
	m, _, _ := newHarness(t)
	m.Cancel("id-1", 5) // the session ended (Deregister/expiry) before a slow Establish(5) commits
	c1 := newFakeConn(1)

	assert.False(t, m.Establish("id-1", 5, c1, testTarget, []string{"/3303/0"}),
		"an Establish for an ended session must not resurrect a zombie")
	assert.False(t, hasSlot(m, "id-1"))
	assert.Empty(t, c1.observedPaths(), "a tombstoned session does no Observe I/O")
}

// Guard 8: a winning higher-epoch establish whose object list yields NO telemetry paths must
// still commit (an empty slot) and cancel the predecessor — never leave the old session's
// observations streaming. Early-returning on empty paths before the CAS reddens this.
func TestEmptyPathsWinningEstablishCancelsPredecessor(t *testing.T) {
	m, _, _ := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))
	obs1 := c1.observation("/3303/0")

	c2 := newFakeConn(2)
	require.True(t, m.Establish("id-1", 2, c2, testTarget, nil), "a higher epoch with no paths must still supersede")
	require.Eventually(t, obs1.Canceled, time.Second, time.Millisecond, "the predecessor's observation must be cancelled")
	ep, ok := slotEpoch(m, "id-1")
	require.True(t, ok)
	assert.Equal(t, uint64(2), ep)
	assert.Equal(t, 0, slotObsCount(m, "id-1"))
}

// A 1.0-only client 4.06s every SenML Observe: the establish is refused (counted), the slot is
// installed EMPTY (records the conn, avoids re-establish thrash on the same live conn), and the
// device keeps L1 presence with zero telemetry — the named LwM2M-1.0 boundary.
func TestAllObservesRefusedInstallsEmptySlot(t *testing.T) {
	m, _, metrics := newHarness(t)
	c1 := newFakeConn(1)
	c1.refuse["/3303/0"] = true
	c1.refuse["/3300/0"] = true

	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0", "/3300/0"}))
	assert.Equal(t, 0, slotObsCount(m, "id-1"), "no observation established, but the slot is recorded")
	assert.Equal(t, float64(2), testutil.ToFloat64(metrics.ObserveEstablishRefused))

	// An Update on the SAME live conn does not thrash a re-establish (canWin is false).
	m.Reestablish("id-1", c1, testTarget)
	assert.Equal(t, float64(2), testutil.ToFloat64(metrics.ObserveEstablishRefused), "no re-observe on the same live conn")
}

// A resource that answers the Observe GET with a 2.05 but NO Observe option (a one-shot /
// non-observable read) yields an already-cancelled go-coap handle. It must NOT be tracked as a
// live observation (which would overcount the gauge and never heal); it is counted as refused.
// Severing the obs.Canceled() check reddens the slot-count and refused-counter assertions.
func TestOneShotObservationNotCountedAsLive(t *testing.T) {
	m, _, metrics := newHarness(t)
	c := newFakeConn(1)
	c.oneShot["/3303/0"] = true // one path is a dead one-shot handle; the other is a real observation

	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3300/0", "/3303/0"}))
	assert.Equal(t, 1, slotObsCount(m, "id-1"), "the one-shot handle must not be tracked as a live observation")
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ObserveEstablishRefused))
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ActiveObservations))
}

// If the conn enters the go-coap teardown window (ctx cancelled, onClose consumed, Done() not
// yet closed) during the establish, the AddOnClose reap is lost — so Establish's inline
// connDead check (which consults Context().Done()) must reap the slot itself, parking it rather
// than pinning a dead conn. Severing the Context().Done() arm of connDead reddens this.
func TestInlineReapWhenConnDiesDuringEstablish(t *testing.T) {
	m, _, _ := newHarness(t)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))
	c.beginShutdown() // the conn is dying: ctx cancelled, Done() still open

	// A re-Register at a higher epoch establishes onto a conn already in the teardown window.
	c2 := newFakeConn(2)
	c2.beginShutdown()
	require.True(t, m.Establish("id-1", 2, c2, testTarget, []string{"/3303/0"}))
	require.True(t, hasSlot(m, "id-1"))
	assert.Nil(t, slotConn(m, "id-1"), "an establish onto a dying conn must park (inline reap), not pin the dead conn")
	assert.Equal(t, 0, slotObsCount(m, "id-1"))
}

// The lost-commit path is the ONLY thing preventing a zombie observation that ingests forever:
// onNotify never consults the slot table, so a late lower-epoch establish that loses the commit
// CAS must cancel the observations it just made. A competitor commits during the I/O window
// (gated DoObserve); the loser's commit must fail and cancel its handle. Removing the
// cancelAll(newObs) on the lost path reddens the "loser's obs cancelled" assertion.
func TestLostCommitCancelsZombieObservation(t *testing.T) {
	m, _, _ := newHarness(t)
	c1 := newFakeConn(1)
	c1.gate = make(chan struct{})
	c1.arrived = make(chan struct{}, 1)

	done := make(chan bool, 1)
	go func() { done <- m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}) }()
	<-c1.arrived // c1's establish has created its observation and is blocked before its commit

	// A higher-epoch session installs while c1 is mid-I/O.
	c2 := newFakeConn(2)
	require.True(t, m.Establish("id-1", 2, c2, testTarget, []string{"/3303/0"}))

	close(c1.gate) // let c1's DoObserve return → its commit CAS must LOSE
	assert.False(t, <-done, "a late lower-epoch establish must lose the commit")

	obs1 := c1.observation("/3303/0")
	require.NotNil(t, obs1)
	require.Eventually(t, obs1.Canceled, time.Second, time.Millisecond, "the lost establish must cancel its zombie observation")
	ep, ok := slotEpoch(m, "id-1")
	require.True(t, ok)
	assert.Equal(t, uint64(2), ep, "the winner (epoch 2) owns the slot")
	assert.True(t, slotConn(m, "id-1") == c2)
}

// A Notify in a content format this slice does not decode (e.g. TLV, the LwM2M 1.0 default) is
// counted as unknown-content-format and dropped, never mis-parsed into a sample.
func TestUnknownContentFormatNotifyDropped(t *testing.T) {
	m, ing, metrics := newHarness(t)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))

	tlv := pool.NewMessage(context.Background())
	tlv.SetCode(codes.Content)
	tlv.SetContentFormat(message.AppLwm2mTLV)
	tlv.SetBody(bytes.NewReader([]byte{0x01, 0x02, 0x03}))
	c1.deliver("/3303/0", tlv)

	assert.Equal(t, 0, ing.callCount())
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.UnknownContentFormat))
}

// L2c Guard: STAGE 1 sheds a Notify at the per-tenant message gate BEFORE decode/ingest. The
// message limiter is charged; the sample limiter is never reached (no decode past the gate).
func TestNotifyShedAtMessageGate(t *testing.T) {
	lim := &fakeLimiter{allowMessage: false, allowSamples: true}
	m, ing, _, metrics := newGatedHarness(t, lim)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

	c.deliver("/3303/0", senmlNotify(`[{"bn":"/3303/0/","n":"5700","v":21.5,"bt":1700000500}]`))

	assert.Equal(t, 0, ing.callCount(), "a message shed at stage 1 must not ingest")
	assert.Equal(t, 1, lim.messageCalls, "the message gate was charged once")
	assert.Empty(t, lim.sampleCharge, "decode/stage-2 is never reached past a shed message")
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.NotifiesReceived), "the Notify is still counted (gate is after that)")
}

// L2c Guard (load-bearing ORDER): a terminal notification is handled BEFORE the message gate,
// so it is never charged AND — even under a shedding limiter — still drops its dead observation.
// Placing stage 1 before the terminal branch would both charge protocol state and, when
// shedding, strand the observation. Severing the ordering reddens this.
func TestTerminalNotificationNotChargedAndDroppedWhileShedding(t *testing.T) {
	lim := &fakeLimiter{allowMessage: false, allowSamples: false} // a fully shedding limiter
	m, ing, _, metrics := newGatedHarness(t, lim)
	c1 := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c1, testTarget, []string{"/3303/0"}))

	c1.deliver("/3303/0", terminalNotify())

	require.Eventually(t, func() bool { return slotObsCount(m, "id-1") == 0 }, time.Second, time.Millisecond,
		"a terminal notification must drop the observation even while the limiter is shedding")
	assert.Equal(t, 0, lim.messageCalls, "a terminal notification must NOT be charged at the message gate")
	assert.Empty(t, lim.sampleCharge, "a terminal notification never reaches the sample gate")
	assert.Equal(t, 0, ing.callCount())
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.TerminalNotifications))
}

// L2c Guard: STAGE 2 sheds a decoded batch at the per-tenant sample gate AFTER decode, charged
// with the DECODED sample count. The message gate passed; ingest is not reached.
func TestNotifyShedAtSampleGateChargedWithCount(t *testing.T) {
	lim := &fakeLimiter{allowMessage: true, allowSamples: false}
	m, ing, _, _ := newGatedHarness(t, lim)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

	// A three-sample pack: the sample gate must be charged n=3, then shed the batch.
	c.deliver("/3303/0", senmlNotify(`[
      {"bn":"/3303/0/","bt":1700000500,"n":"5700","v":1},
      {"n":"5601","v":2},
      {"n":"5602","v":3}
    ]`))

	assert.Equal(t, 1, lim.messageCalls, "message gate charged once")
	assert.Equal(t, []int{3}, lim.sampleCharge, "sample gate charged with the decoded count")
	assert.Equal(t, 0, ing.callCount(), "a batch shed at stage 2 must not ingest")
}

// L2c Guard: the admitted happy path charges the message gate once and the sample gate with the
// decoded count, then ingests.
func TestNotifyAdmittedChargesBothStagesThenIngests(t *testing.T) {
	lim := &fakeLimiter{allowMessage: true, allowSamples: true}
	m, ing, _, _ := newGatedHarness(t, lim)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

	c.deliver("/3303/0", senmlNotify(`[
      {"bn":"/3303/0/","bt":1700000500,"n":"5700","v":1},
      {"n":"5601","v":2}
    ]`))

	assert.Equal(t, 1, lim.messageCalls)
	assert.Equal(t, []int{2}, lim.sampleCharge)
	assert.Equal(t, 1, ing.callCount(), "an admitted Notify ingests")
}

// L2c Guard (charge semantics, finding 7): a well-formed but non-numeric (zero-sample) Notify
// charges the MESSAGE gate — it is a message the tenant sent — but never the sample gate (a
// zero-sample batch is not a rate event and returns before stage 2).
func TestZeroSampleNotifyChargesMessageNotSamples(t *testing.T) {
	lim := &fakeLimiter{allowMessage: true, allowSamples: true}
	m, ing, _, _ := newGatedHarness(t, lim)
	c := newFakeConn(1)
	require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

	// A boolean-only IPSO record yields zero numeric samples (ADR-016).
	c.deliver("/3303/0", senmlNotify(`[{"bn":"/3303/0/","n":"5850","vb":true}]`))

	assert.Equal(t, 1, lim.messageCalls, "a zero-sample message still charges the message gate")
	assert.Empty(t, lim.sampleCharge, "a zero-sample batch never reaches the sample gate")
	assert.Equal(t, 0, ing.callCount())
}
