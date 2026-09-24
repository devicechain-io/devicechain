// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// The harness for the per-device gate tests (gate_test.go). It drives the REAL Run loop — reader,
// shards, park pool and drain turns — against a fake command-delivery that keeps rows, because the
// properties under test are about ORDER across the live road and the backlog road, and only a
// store that moves rows through SENT, PARKED and back to SENT can show a command delivered twice or
// out of turn. Every wait is bounded: a hang is reported as a failure, never waited out.

// storeRow is one command as command-delivery holds it.
type storeRow struct {
	id      int
	token   string
	device  string
	status  string // SENT, PARKED or HELD — the states this adapter moves between
	nonce   string
	name    string
	payload string
}

// fakeStore models the slice of command-delivery the dispatcher talks to: the drain fetch (oldest
// first, drainable states only, bounded by the page), the drain claim, the live confirmation and
// the park — each a conditional transition on the row, like the real ones.
type fakeStore struct {
	mu     sync.Mutex
	rows   []*storeRow
	byTok  map[string]*storeRow
	minted int

	parkErrs     map[string]int           // token → park attempts that fail before one succeeds
	parkHolds    map[string]chan struct{} // token → a park that blocks until the channel closes
	dispatchErrs map[string]int           // token → live confirmations that fail
	claimErrs    map[string]int           // token → drain claims that fail
	fetchErrs    int                      // drain fetches that fail

	fetches   int
	parkCalls []string
	claims    []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byTok:        map[string]*storeRow{},
		parkErrs:     map[string]int{},
		parkHolds:    map[string]chan struct{}{},
		dispatchErrs: map[string]int{},
		claimErrs:    map[string]int{},
	}
}

func (s *fakeStore) mint() string { s.minted++; return fmt.Sprintf("n%d", s.minted) }

// add creates a row in the given status and returns its nonce.
func (s *fakeStore) add(token, device, status string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &storeRow{id: len(s.rows) + 1, token: token, device: device, status: status, nonce: s.mint(),
		name: CommandRead, payload: fmt.Sprintf(`{"path":"/%s"}`, token)}
	s.rows = append(s.rows, r)
	s.byTok[token] = r
	return r.nonce
}

func (s *fakeStore) status(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byTok[token].status
}

func (s *fakeStore) fetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

func (s *fakeStore) parked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.parkCalls...)
}

func (s *fakeStore) claimed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.claims...)
}

func (s *fakeStore) Pending(_ context.Context, _, deviceToken string, limit int) ([]DrainCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	if s.fetchErrs > 0 {
		s.fetchErrs--
		return nil, errors.New("command-delivery unreachable")
	}
	var out []DrainCommand
	for _, r := range s.rows { // rows are in id order: oldest first, as the server's ORDER BY
		if r.device != deviceToken || (r.status != statusParked && r.status != statusHeld) {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, DrainCommand{Token: r.token, Name: r.name, Payload: []byte(r.payload), Status: r.status})
	}
	return out, nil
}

func (s *fakeStore) Claim(_ context.Context, _, token string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, token)
	if s.claimErrs[token] > 0 {
		s.claimErrs[token]--
		return "", false, errors.New("command-delivery unreachable")
	}
	r := s.byTok[token]
	if r.status != statusParked && r.status != statusHeld {
		return "", false, nil
	}
	r.status, r.nonce = "SENT", s.mint()
	return r.nonce, true, nil
}

func (s *fakeStore) ClaimDispatch(_ context.Context, _, token, quoted string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispatchErrs[token] > 0 {
		s.dispatchErrs[token]--
		return "", false, errors.New("command-delivery unreachable")
	}
	r := s.byTok[token]
	if r.status != "SENT" || r.nonce != quoted {
		return "", false, nil
	}
	r.nonce = s.mint()
	return r.nonce, true, nil
}

func (s *fakeStore) Park(ctx context.Context, _, token, quoted string) (bool, error) {
	s.mu.Lock()
	s.parkCalls = append(s.parkCalls, token)
	hold := s.parkHolds[token]
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parkErrs[token] > 0 {
		s.parkErrs[token]--
		return false, errors.New("command-delivery unreachable")
	}
	r := s.byTok[token]
	if r.status != "SENT" || r.nonce != quoted {
		return false, nil
	}
	r.status = statusParked // a park keeps the nonce, as the real one does
	return true, nil
}

// orderExecutor records the command each op carried, in the order the ops ran. onOp, when set,
// runs inside the op on the worker goroutine — to block it, or to act while it is in flight.
type orderExecutor struct {
	mu   sync.Mutex
	ran  []string
	onOp func(token string)
}

func (e *orderExecutor) Execute(_ context.Context, _ mux.Conn, _ string, payload []byte) OpResult {
	var p cmdPayload
	_ = json.Unmarshal(payload, &p)
	token := p.Path[1:]
	e.mu.Lock()
	e.ran = append(e.ran, token)
	fn := e.onOp
	e.mu.Unlock()
	if fn != nil {
		fn(token)
	}
	return OpResult{Op: labelRead, Success: true}
}

func (e *orderExecutor) order() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ran...)
}

// chanReader hands out whatever the test pushes, in order, and counts what it has handed out.
type chanReader struct {
	ch   chan messaging.Message
	mu   sync.Mutex
	read int
}

func (r *chanReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	select {
	case m := <-r.ch:
		r.mu.Lock()
		r.read++
		r.mu.Unlock()
		return m, nil
	case <-ctx.Done():
		return messaging.Message{}, ctx.Err()
	}
}
func (r *chanReader) HandleResponse(error) {}
func (r *chanReader) count() int           { r.mu.Lock(); defer r.mu.Unlock(); return r.read }

// switchLookup is a conn table whose answers a test can change while the dispatcher runs.
type switchLookup struct {
	mu      sync.Mutex
	reaches map[string]Reach
}

func (l *switchLookup) set(device string, r Reach) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reaches["acme/"+device] = r
}

func (l *switchLookup) Lookup(tenant, token string) (mux.Conn, Reach) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.reaches[tenant+"/"+token] {
	case ReachLive:
		return &fakeConn{}, ReachLive
	case ReachOffline:
		return nil, ReachOffline
	}
	return nil, ReachNotServed
}

// fakeClock is a manual clock: time moves only when the test advances it, and a timer fires only
// then. It is what lets "no retry before the delay" be asserted without waiting the delay out.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// pending reports the deadlines of timers still armed, earliest first.
func (c *fakeClock) pending() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Time
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			out = append(out, t.at)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !c.now.Before(t.at) {
			t.fired = true
			due = append(due, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// gateMetrics builds REAL, unregistered counters for everything the gate tests read.
func gateMetrics() Metrics {
	c := func(name string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{Name: name})
	}
	m := parkMetrics()
	m.LiveClaimErrors = c("live_claim_errors")
	m.StaleDispatch = c("stale_dispatch")
	m.DrainClaimErrors = c("drain_claim_errors")
	return m
}

// harness is one running dispatcher over the fakes above.
type harness struct {
	t     *testing.T
	d     *Dispatcher
	store *fakeStore
	exec  *orderExecutor
	rdr   *chanReader
	look  *switchLookup
	clk   *fakeClock
	m     Metrics
	stop  func()
}

// newHarness builds a dispatcher with the given worker count; configure mutates the fakes before
// Run starts.
func newHarness(t *testing.T, workers int, configure func(h *harness)) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		store: newFakeStore(),
		exec:  &orderExecutor{},
		rdr:   &chanReader{ch: make(chan messaging.Message, 1024)},
		look:  &switchLookup{reaches: map[string]Reach{}},
		clk:   newFakeClock(),
		m:     gateMetrics(),
	}
	if configure != nil {
		configure(h)
	}
	h.d = NewDispatcher(h.rdr, &fakePublisher{}, h.look, h.exec, h.store, h.store, h.m,
		Options{Workers: workers, Parker: h.store,
			ReadPacer: core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock())})
	h.d.clock = h.clk
	h.stop = runDispatcher(t, h.d)
	t.Cleanup(func() {
		if h.stop != nil {
			h.stop()
		}
	})
	return h
}

// send publishes a live command for a device: its row goes to SENT on a fresh nonce and its
// envelope onto the stream.
func (h *harness) send(token, device string) *fakeAck {
	nonce := h.store.add(token, device, "SENT")
	return h.deliver(token, device, nonce)
}

// deliver puts an envelope for an existing row on the stream, quoting the given nonce — a
// redelivery quotes the one the row was published with.
func (h *harness) deliver(token, device, nonce string) *fakeAck {
	ack := newAck()
	h.rdr.ch <- cmdMsgNonce("acme", device, token, CommandRead, fmt.Sprintf(`{"path":"/%s"}`, token), nonce, ack)
	return ack
}

// nonceOf reads a row's current nonce (the one a redelivery of its envelope would quote, as long
// as nothing has rotated it).
func (h *harness) nonceOf(token string) string {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return h.store.byTok[token].nonce
}

// waitOrder waits (bounded) until the executor has run exactly want, and fails with what it saw.
func (h *harness) waitOrder(want ...string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return len(h.exec.order()) >= len(want) }, 3*time.Second, 2*time.Millisecond,
		"ops ran: %v, want %v", h.exec.order(), want)
	require.Equal(h.t, want, h.exec.order())
}

// shard returns the running term's only shard (the gate tests use one worker, so every device
// shares it and the alternation between them is observable).
func (h *harness) shard() *shardState {
	p := h.d.shardsPtr.Load()
	require.NotNil(h.t, p, "no running term")
	return (*p)[0]
}

// queuedLive reads how many live tasks sit in the shard's queue.
func (h *harness) queuedLive() int { return len(h.shard().ch) }

// gated reports whether a device's gate is up.
func (h *harness) gated(device string) bool {
	s := h.shard()
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.devs[deviceKey{"acme", device}]
	return g != nil && g.gated
}

// holdOps makes every op block until the returned release is called (and none after it). The
// returned channel receives the token of each op as it starts.
func (h *harness) holdOps() (holding <-chan string, release func()) {
	gate := make(chan struct{})
	started := make(chan string, 64)
	h.exec.mu.Lock()
	h.exec.onOp = func(token string) {
		started <- token
		<-gate
	}
	h.exec.mu.Unlock()
	var once sync.Once
	return started, func() { once.Do(func() { close(gate) }) }
}

// recv waits (bounded) for one value.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// never asserts a condition stays false for a short, bounded window.
func never(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatal(what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
