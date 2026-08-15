// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// --- fakes -----------------------------------------------------------------

type fakeAck struct{ n int32 }

func (a *fakeAck) Ack() error  { atomic.AddInt32(&a.n, 1); return nil }
func (a *fakeAck) count() int  { return int(atomic.LoadInt32(&a.n)) }
func newAck() *fakeAck         { return &fakeAck{} }
func (a *fakeAck) acked() bool { return a.count() > 0 }

// cmdMsg builds a consumed device-commands message: subject {inst}.{tenant}.device-commands.{token}
// carrying a delivery envelope, with a fake ack handle. The command token defaults to "cmd-"+token
// (fine for a single command per device); a test issuing MULTIPLE distinct commands to the SAME
// device must use cmdMsgTok with distinct command tokens, exactly as production does (commands are
// per-tenant unique, ADR-042) — otherwise the drain/live dedup correctly treats them as one command.
func cmdMsg(tenant, token, name, payload string, ack messaging.Acknowledger) messaging.Message {
	return cmdMsgTok(tenant, token, "cmd-"+token, name, payload, ack)
}

func cmdMsgTok(tenant, deviceToken, cmdToken, name, payload string, ack messaging.Acknowledger) messaging.Message {
	return cmdMsgNonce(tenant, deviceToken, cmdToken, name, payload, "", ack)
}

// cmdMsgNonce is cmdMsgTok plus the DISPATCH NONCE the delivery sweep stamps on the publish it
// is making. Every other builder in this file delegates here with an empty nonce, which is not
// a shortcut but the pre-nonce wire shape a command published by an older build (or by anything
// other than the sweep) still has — the case park() must decline rather than guess at.
//
// The bytes are marshalled ONCE, here, so a nonce test and a non-nonce test cannot end up
// asserting against two different notions of what went on the wire.
func cmdMsgNonce(tenant, deviceToken, cmdToken, name, payload, nonce string, ack messaging.Acknowledger) messaging.Message {
	env := deliveryEnvelope{Token: cmdToken, DeviceToken: deviceToken, Name: name, DispatchNonce: nonce}
	if payload != "" {
		raw := json.RawMessage(payload)
		env.Payload = &raw
	}
	value, _ := json.Marshal(env)
	subject := "inst." + tenant + ".device-commands." + deviceToken
	return messaging.NewConsumedMessage(subject, value, 1, nil, ack)
}

type fakeLookup struct {
	conn    mux.Conn
	reaches map[string]Reach
}

func (l *fakeLookup) Lookup(tenant, token string) (mux.Conn, Reach) {
	r, ok := l.reaches[tenant+"/"+token]
	if !ok || r == ReachNotServed {
		return nil, ReachNotServed
	}
	if r == ReachLive {
		return l.conn, ReachLive
	}
	return nil, ReachOffline
}

type fakePublisher struct {
	mu   sync.Mutex
	sent []responseEnvelope
	fail bool
}

func (p *fakePublisher) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("command-responses publish failed")
	}
	for _, m := range msgs {
		var e responseEnvelope
		_ = json.Unmarshal(m.Value, &e)
		p.sent = append(p.sent, e)
	}
	return nil
}

func (p *fakePublisher) responses() []responseEnvelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]responseEnvelope(nil), p.sent...)
}

type fakeExecutor struct {
	mu           sync.Mutex
	calls        int
	result       OpResult
	started      chan string        // if non-nil, receives each command's device token as it begins
	gate         chan struct{}      //
	cancelOnCall context.CancelFunc // if set, cancels the term ctx DURING the op (models eviction mid-op)
}

func (e *fakeExecutor) Execute(_ context.Context, _ mux.Conn, _ string, payload []byte) OpResult {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	if e.cancelOnCall != nil {
		e.cancelOnCall() // the leadership term is lost while this op is in flight
	}
	if e.started != nil {
		var env cmdPayload
		_ = json.Unmarshal(payload, &env)
		e.started <- env.Path
	}
	if e.gate != nil {
		<-e.gate
	}
	return e.result
}

func (e *fakeExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func newDispatcher(rdr reader, pub responsePublisher, look connLookup, exec executor) *Dispatcher {
	return NewDispatcher(rdr, pub, look, exec, nil, nil, Metrics{}, Options{})
}

// fakeFetcher is a stand-in wake-drain source: it returns a canned command list (or an error).
type fakeFetcher struct {
	mu    sync.Mutex
	cmds  []DrainCommand
	err   error
	calls int
}

func (f *fakeFetcher) Pending(_ context.Context, _, _ string, _ time.Time) ([]DrainCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]DrainCommand(nil), f.cmds...), nil
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeClaimer is a stand-in for command-delivery's markCommandSent. It records the command
// tokens it was asked to claim — not merely a count — because the properties under test are
// "WHICH commands were claimed" and "was the claim issued BEFORE the op", and a bare counter
// cannot tell a claim of the right command from a claim of the wrong one.
type fakeClaimer struct {
	mu       sync.Mutex
	won      bool  // what a successful claim reports: true = we own it, false = someone else won
	err      error // if set, the claim could not be established at all
	claimed  []string
	observer func() // called inside Claim, to observe the world AT claim time (e.g. ops run so far)
}

func (c *fakeClaimer) Claim(_ context.Context, _, commandToken string) (bool, error) {
	c.mu.Lock()
	c.claimed = append(c.claimed, commandToken)
	obs := c.observer
	c.mu.Unlock()
	if obs != nil {
		obs()
	}
	if c.err != nil {
		return false, c.err
	}
	return c.won, nil
}

func (c *fakeClaimer) claims() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.claimed...)
}

// parkCall records ONE call to the parker, with every argument it carried. The arguments are
// the property under test, not the fact of the call: a park quotes the dispatch nonce so a
// redelivered publish cannot drag a since-re-claimed row back into the dispatchable set, and a
// bare counter cannot tell a park of the right dispatch from a park of the wrong one.
type parkCall struct {
	tenant       string
	commandToken string
	nonce        string
}

// fakeParker is a stand-in for command-delivery's parkCommand mutation. Like fakeClaimer it
// records ARGUMENTS rather than a count, and like it, its three outcomes are exactly the ones a
// fake that always succeeded would skip past: parked (true, nil), SETTLED (false, nil — the row
// moved on under us, an outcome and not a failure), and an error (the park could not be
// established at all, so the message must NOT be acked).
type fakeParker struct {
	mu     sync.Mutex
	parked bool  // what a successful park reports: true = this call moved the row
	err    error // if set, the park could not be established
	calls  []parkCall
	// onCall runs inside Park, before it returns — used to model the leadership term being
	// evicted while the park request is in flight.
	onCall func()
}

func (p *fakeParker) Park(_ context.Context, tenant, commandToken, dispatchNonce string) (bool, error) {
	p.mu.Lock()
	p.calls = append(p.calls, parkCall{tenant: tenant, commandToken: commandToken, nonce: dispatchNonce})
	fn := p.onCall
	p.mu.Unlock()
	if fn != nil {
		fn()
	}
	if p.err != nil {
		return false, p.err
	}
	return p.parked, nil
}

func (p *fakeParker) parks() []parkCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]parkCall(nil), p.calls...)
}

// parkMetrics builds REAL Prometheus counters for the park dispositions, so the metric
// assertions measure the production code's increments rather than a test double's bookkeeping.
// They are constructed with prometheus.NewCounter and NOT promauto: promauto registers against
// the process-global registry, which panics the second test that builds the same metric name.
func parkMetrics() Metrics {
	c := func(name string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{Name: name})
	}
	return Metrics{
		NotServed:     c("not_served"),
		ServedOffline: c("served_offline"),
		ParkErrors:    c("park_errors"),
		ParkSettled:   c("park_settled"),
		ParkSkipped:   c("park_skipped"),
	}
}

// liveWorkTok builds a live work item with an explicit command token (production commands are
// per-tenant unique, ADR-042).
func liveWorkTok(tenant, deviceToken, cmdToken, name, payload string, ack messaging.Acknowledger) work {
	msg := cmdMsgTok(tenant, deviceToken, cmdToken, name, payload, ack)
	var env deliveryEnvelope
	_ = json.Unmarshal(msg.Value, &env)
	return work{msg: msg, tenant: tenant, env: env}
}

// liveWorkNonce is liveWorkTok for a command the delivery sweep stamped with a dispatch nonce.
// It goes through the wire bytes (marshal then unmarshal) rather than building the envelope
// directly, so a nonce that failed to survive JSON — a missing or misspelled tag on
// deliveryEnvelope.DispatchNonce — is caught here rather than papered over.
func liveWorkNonce(tenant, deviceToken, cmdToken, name, payload, nonce string, ack messaging.Acknowledger) work {
	msg := cmdMsgNonce(tenant, deviceToken, cmdToken, name, payload, nonce, ack)
	var env deliveryEnvelope
	_ = json.Unmarshal(msg.Value, &env)
	return work{msg: msg, tenant: tenant, env: env}
}

// flakyLookup returns ReachLive for the first `liveFor` lookups of the device, then ReachOffline —
// modeling a device that drops mid-drain.
type flakyLookup struct {
	mu      sync.Mutex
	conn    mux.Conn
	liveFor int
	calls   int
}

func (l *flakyLookup) Lookup(_, _ string) (mux.Conn, Reach) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls <= l.liveFor {
		return l.conn, ReachLive
	}
	return nil, ReachOffline
}

// --- drain() disposition (ADR-075 L4b) --------------------------------------

// TestDrainDispatchesHeldCommands proves the core wake-drain: held commands are dispatched to the
// now-live device, in fetch order (the fetcher already sorts oldest-first), each publishing a response.
//
// The fixtures carry Status: statusParked — as does every pre-existing drain test below — and
// that models the row a device's own absence produced at DISPATCH time: published toward a
// device that had already gone quiet, found unreachable by this dispatcher, and handed back.
// It is not the only kind. The presence gate withholds commands to a device known absent
// BEFORE publishing, which is HELD, so a real backlog is a mixture of both — which is why the
// claim-then-dispatch tests above stage HELD explicitly. Stating the status on every fixture
// rather than leaving it zero is what makes that contrast expressible at all.
//
// These fixtures said statusSent until PARKED existed, and the rename is not cosmetic: a SENT
// row is no longer drained at all, so leaving them would have left this whole block asserting
// against a state the drain cannot see.
func TestDrainDispatchesHeldCommands(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusParked},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, &fakeClaimer{won: true}, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 2, exec.callCount(), "both held commands are dispatched")
	resp := pub.responses()
	require.Len(t, resp, 2)
	assert.Equal(t, "c1", resp[0].CommandToken, "in fetch (oldest-first) order")
	assert.Equal(t, "c2", resp[1].CommandToken)
}

// TestDrainSkipsCommandAlreadyDispatchedLive is the drain/live dedup guard: a command the live path
// just dispatched must NOT be re-actuated when a drain fetches its still-SENT row moments later.
func TestDrainSkipsCommandAlreadyDispatchedLive(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked}, // already fired live
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusParked},           // fresh
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, &fakeClaimer{won: true}, Metrics{}, Options{})

	// The live path dispatches c1 first (marks it dispatched).
	d.dispatch(context.Background(), liveWorkTok("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, newAck()))
	// Then a wake drain fetches c1 + c2: c1 must be skipped, only c2 dispatched.
	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 2, exec.callCount(), "live c1 (1) + drain c2 (1); c1 is NOT re-actuated by the drain")
}

// TestDedupIsTenantScoped is the cross-tenant suppression guard (Blocker 1): device tokens and
// command tokens are only per-tenant unique (ADR-042), so two tenants can each have device "pump-1"
// carrying command "c1". One tenant's dispatch must NOT suppress the other's — else the second
// tenant's command is acked without executing and silently lost.
func TestDedupIsTenantScoped(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive, "globex/pump-1": ReachLive}}
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(context.Background(), liveWorkTok("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"a"}`, newAck()))
	d.dispatch(context.Background(), liveWorkTok("globex", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"b"}`, newAck()))

	assert.Equal(t, 2, exec.callCount(), "a second tenant's identically-tokened command is not suppressed")
	assert.Len(t, pub.responses(), 2, "both tenants' commands publish a response")
}

// TestDrainStopsWhenDeviceDropsMidDrain proves a device dropping mid-drain stops the drain cleanly —
// the remaining held commands stay SENT (unfetched-again) for the next wake, never dispatched to a
// dead conn.
func TestDrainStopsWhenDeviceDropsMidDrain(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &flakyLookup{conn: &fakeConn{}, liveFor: 1} // live for c1 only, offline thereafter
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusParked},
		{Token: "c3", Name: CommandRead, Payload: []byte(`{"path":"/3/0/0"}`), Status: statusParked},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, &fakeClaimer{won: true}, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 1, exec.callCount(), "only c1 dispatched; c2/c3 not sent to a dropped device")
}

// TestDrainStopsOnEviction: a term lost mid-drain stops after the in-flight op, leaving the rest for
// the next leader's wake (mirrors the live path's post-op eviction).
func TestDrainStopsOnEviction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}, cancelOnCall: cancel}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusParked},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, &fakeClaimer{won: true}, Metrics{}, Options{})

	d.drain(ctx, drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 1, exec.callCount(), "c1's op ran; the eviction stops the drain before c2")
	assert.Empty(t, pub.responses(), "no response from the evicted term (the c1 result is unreliable)")
}

// TestDrainFetchErrorNoDispatch: a fetch failure dispatches nothing and is retried on the next wake.
func TestDrainFetchErrorNoDispatch(t *testing.T) {
	exec := &fakeExecutor{}
	ff := &fakeFetcher{err: errors.New("command-delivery unreachable")}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := NewDispatcher(nil, &fakePublisher{}, look, exec, ff, nil, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 0, exec.callCount(), "a failed fetch dispatches nothing")
}

// TestDrainDisabledWhenFetcherNil: with no fetcher (command-delivery endpoint unset) the drain is a
// safe no-op — offline commands ride TTL to TIMEOUT (the L4a behavior).
func TestDrainDisabledWhenFetcherNil(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDispatcher(nil, &fakePublisher{}, &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}, exec)

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})
	d.Drain("acme", "pump-1") // also a no-op via the public trigger

	assert.Equal(t, 0, exec.callCount())
}

// TestDrainTriggerNoOpOnStandby: Drain called when this replica is not the serving leader (Run not
// active, so no published queues) is a no-op and never panics.
func TestDrainTriggerNoOpOnStandby(t *testing.T) {
	ff := &fakeFetcher{cmds: []DrainCommand{{Token: "c1", Name: CommandRead, Payload: []byte(`{"path":"/3/0/0"}`), Status: statusParked}}}
	d := NewDispatcher(nil, &fakePublisher{}, &fakeLookup{}, &fakeExecutor{}, ff, nil, Metrics{}, Options{})

	d.Drain("acme", "pump-1") // no active term → no-op

	assert.Equal(t, 0, ff.callCount(), "a standby (no serving term) does not fetch")
}

// --- claim-then-dispatch (ADR-075 L4b) --------------------------------------
//
// 🔴 A command is a PHYSICAL ACTUATION, and the delivery sweep publishes anything still
// DISPATCHABLE. So a drain that fired a HELD command's CoAP op without first taking the row
// out of that set would leave it for the next sweep tick to publish down the live path — the
// valve opened a second time. The in-memory dedupe does NOT cover this: it is per-pod and
// TTL-bounded, so it says nothing about another replica or about this pod after a restart,
// which are exactly the situations a leadership change produces.
//
// These four tests pin the ordering and all three of its outcomes. The two "must not
// actuate" branches (claim lost, claim errored) are the ones a fake that just returned
// success would silently skip past, so each asserts on the EXECUTOR — zero CoAP ops — rather
// than on a return value or a counter.

// TestDrainClaimsAHeldCommandBeforeDispatch proves the ORDERING, not merely that a claim
// happened: the claimer observes the executor's call count at claim time and it must be zero.
// A test that only checked "claim called && op called" would pass just as happily on
// dispatch-then-claim, which is the bug — the op is irreversible, so a claim taken afterwards
// protects nothing.
func TestDrainClaimsAHeldCommandBeforeDispatch(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusHeld},
	}}
	opsAtClaimTime := -1
	claimer := &fakeClaimer{won: true}
	claimer.observer = func() { opsAtClaimTime = exec.callCount() }
	d := NewDispatcher(nil, pub, look, exec, ff, claimer, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, []string{"c1"}, claimer.claims(), "the held command is claimed by token")
	assert.Equal(t, 0, opsAtClaimTime, "the claim must be taken BEFORE the CoAP op runs, not after")
	assert.Equal(t, 1, exec.callCount(), "having won the claim, the drain dispatches")
	require.Len(t, pub.responses(), 1)
	assert.Equal(t, "c1", pub.responses()[0].CommandToken)
}

// TestDrainDoesNotDispatchWhenTheClaimIsLost: another dispatcher (or the sweep) moved the
// command out of the dispatchable set first. That is the exclusion mechanism WORKING — this
// replica must decline, so no CoAP op may run.
func TestDrainDoesNotDispatchWhenTheClaimIsLost(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusHeld},
	}}
	claimer := &fakeClaimer{won: false} // someone else got there first
	d := NewDispatcher(nil, pub, look, exec, ff, claimer, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, []string{"c1"}, claimer.claims(), "the claim was attempted")
	assert.Equal(t, 0, exec.callCount(), "a LOST claim must not actuate — the winner dispatches it")
	assert.Empty(t, pub.responses(), "no outcome to report for a command we never sent")
}

// TestDrainDoesNotDispatchWhenTheClaimErrors is the FAIL-CLOSED direction. command-delivery
// being unreachable means we cannot prove we own the command; dispatching anyway would be
// irreversible, while declining costs one wake (the row is still dispatchable and the device
// re-triggers on its next Register/Update). So: no op.
func TestDrainDoesNotDispatchWhenTheClaimErrors(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusHeld},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusHeld},
	}}
	claimer := &fakeClaimer{err: errors.New("command-delivery unreachable")}
	d := NewDispatcher(nil, pub, look, exec, ff, claimer, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 0, exec.callCount(), "a claim that ERRORS must not actuate — an unconfirmed claim is not a claim")
	assert.Empty(t, pub.responses())
	assert.Len(t, claimer.claims(), 2,
		"a failed claim skips only ITS command; the rest of the backlog is still attempted (one bad row must not strand the queue)")
}

// TestDrainClaimsEveryRowItDispatches is the INVERSION of a test that used to assert the
// opposite, and the inversion is the point of the change that introduced PARKED.
//
// The old test pinned "a SENT row is dispatched WITHOUT claiming", on the argument that SENT
// has already left command-delivery's dispatchable set so there is nothing to claim. The
// argument was sound; the premise was not. A SENT row could equally be a command that went
// NOWHERE — published to a registered-but-sleeping device and ack-dropped by this very
// dispatcher — and re-dispatching one of those with no claim was the platform's only
// unclaimed dispatch, guarded solely by a 60-second per-pod cache. Those rows are PARKED now,
// and PARKED is claimable, so no row reaches a device without a claim.
//
// 🔴 won:false is the tripwire, kept from the old test and pointed the other way. If the
// drain fails to claim a parked row, this claimer reports the claim lost and NOTHING is
// dispatched — so the assertions below fail loudly on a missing claim rather than quietly
// passing on an extra one.
func TestDrainClaimsEveryRowItDispatches(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusHeld},
	}}
	claimer := &fakeClaimer{won: false}
	d := NewDispatcher(nil, pub, look, exec, ff, claimer, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.ElementsMatch(t, []string{"c1", "c2"}, claimer.claims(),
		"every drained row must be claimed before it actuates — the parked one no less than the held one")
	assert.Equal(t, 0, exec.callCount(), "a lost claim must not actuate")
	assert.Empty(t, pub.responses())
}

// TestDrainWithoutAClaimerRefusesAHeldCommand: claiming wired off (no command-delivery
// coordinate) must NOT degrade into dispatching held commands unclaimed. Fail closed — a
// command nobody can prove ownership of is one the sweep may still publish.
func TestDrainWithoutAClaimerRefusesAHeldCommand(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusHeld},
	}}
	d := NewDispatcher(nil, &fakePublisher{}, look, exec, ff, nil, Metrics{}, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 0, exec.callCount(), "with no claimer, a held command must not be actuated")
}

// --- dispatch() disposition (deterministic, no Run loop) --------------------

func liveWork(tenant, token, name, payload string, ack messaging.Acknowledger) work {
	msg := cmdMsg(tenant, token, name, payload, ack)
	var env deliveryEnvelope
	_ = json.Unmarshal(msg.Value, &env)
	return work{msg: msg, tenant: tenant, env: env}
}

func TestDispatchLiveExecutesPublishesAcks(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelRead, Success: true, Payload: strptr("42")}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(context.Background(), liveWork("acme", "pump-1", CommandRead, `{"path":"/3/0/9"}`, ack))

	assert.Equal(t, 1, exec.callCount(), "a live device is dispatched to")
	resp := pub.responses()
	require.Len(t, resp, 1, "a response is published")
	assert.Equal(t, "cmd-pump-1", resp[0].CommandToken)
	assert.True(t, resp[0].Success)
	assert.True(t, ack.acked(), "a dispatched command is acked (seal-fate)")
}

func TestDispatchNotServedAcksNoResponse(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{}
	pub := &fakePublisher{}
	look := &fakeLookup{reaches: map[string]Reach{}} // no entry ⇒ not served
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(context.Background(), liveWork("acme", "mqtt-dev", CommandWrite, `{"path":"/5/0/1","value":"x"}`, ack))

	assert.Equal(t, 0, exec.callCount(), "a device we do not serve is never dispatched to")
	assert.Empty(t, pub.responses(), "no response is published for a device we do not serve")
	assert.True(t, ack.acked(), "the message is acked (ack-drop, does not steal from another consumer)")
}

// TestDispatchOfflineAcksNoResponse pins the shape of the offline path with parking WIRED OFF
// (newDispatcher passes no Parker), which is the pre-PARKED behaviour and still the floor when
// no command-delivery coordinate is configured. The park dispositions themselves — including
// this same no-parker case with its ParkSkipped count — are in the park() block below.
func TestDispatchOfflineAcksNoResponse(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{}
	pub := &fakePublisher{}
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(context.Background(), liveWork("acme", "pump-1", CommandRead, `{"path":"/3/0/9"}`, ack))

	assert.Equal(t, 0, exec.callCount(), "an offline served device is not dispatched to")
	assert.Empty(t, pub.responses(), "no response — the command rides TTL to TIMEOUT")
	assert.True(t, ack.acked())
}

// Seal-fate (S1): after a successful CoAP op, the command is acked EVEN IF the response publish
// fails — it must never redeliver and re-actuate the device.
func TestSealFateAcksEvenWhenResponsePublishFails(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelExecute, Success: true}}
	pub := &fakePublisher{fail: true} // command-responses down
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(context.Background(), liveWork("acme", "pump-1", CommandExecute, `{"path":"/5/0/2"}`, ack))

	assert.Equal(t, 1, exec.callCount(), "the op ran")
	assert.True(t, ack.acked(), "seal-fate: ack after a run op even when the response could not be published")
}

// Eviction: a command whose term context is already cancelled must NOT be dispatched or acked — it
// redelivers to the next leader (a replica no longer serving must not act or ack).
func TestEvictionSkipsUnackedForRedelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelRead, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(ctx, liveWork("acme", "pump-1", CommandRead, `{"path":"/3/0/9"}`, ack))

	assert.Equal(t, 0, exec.callCount(), "an evicted term does not dispatch")
	assert.False(t, ack.acked(), "an evicted term leaves the message unacked to redeliver to the next leader")
}

// BLOCKER-1 regression / seal-fate at eviction: if the term is lost WHILE the CoAP op is in flight,
// the op was already issued (the device may have actuated), so the message must be ACKED — never
// redelivered (which would re-actuate) — and NO response published (the result is an unreliable
// artifact of losing the ctx). The command rides SENT→TIMEOUT.
func TestPostOpEvictionAcksSealFateNoResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelExecute, Success: true}, cancelOnCall: cancel}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := newDispatcher(nil, pub, look, exec)

	d.dispatch(ctx, liveWork("acme", "pump-1", CommandExecute, `{"path":"/5/0/2"}`, ack))

	assert.Equal(t, 1, exec.callCount(), "the op was issued")
	assert.True(t, ack.acked(), "an op issued before eviction is acked (sealed), never redelivered to re-actuate")
	assert.Empty(t, pub.responses(), "no response is published from an evicted term (the result is unreliable)")
}

// --- park(): handing an undeliverable command back (ADR-075 L4b) ------------
//
// 🔴 PRESENCE IS NOT REACHABILITY, and that gap is what these tests are about.
// command-delivery decides whether to publish from presence — is the device registered — and a
// queue-mode LwM2M device is registered and asleep. So the command is published, arrives here,
// and finds no conn to deliver over. Before parking, it was ack-dropped and the row stayed
// SENT: a state meaning "the device has it", which made the platform blame the device at TTL
// with TIMEOUT, tell an operator cancelling a fleet write that the command was beyond recall
// when it was not, and leave the row to be re-dispatched by the wake drain WITHOUT a claim.
//
// Two properties carry the whole design, and each is asserted on directly below rather than
// inferred from a call count:
//
//   - the NONCE. A park names the dispatch it is handing back. The request that reaches
//     command-delivery may be a REDELIVERY of a publish whose command has since been claimed by
//     a wake drain and actually RUN; a park matching on status alone would drag that row back
//     into the dispatchable set to be actuated a second time. So the nonce that went out on the
//     envelope must be the nonce that comes back, and an envelope carrying none must not be
//     parked at all.
//   - the ACK DECISION. Parked and settled ACK; an ERROR must NOT ack, because the unacked
//     message redelivering at AckWait is the entire retry mechanism. A park that acked on
//     failure would strand the row in SENT forever, which is the exact bug parking exists to
//     fix — and would do it silently, since the metric would show the error and the operator
//     would have no idea the command was never retried.

// TestOfflineCommandIsParkedQuotingItsDispatchNonce is the core of it: a served device with no
// live conn has its command handed back, and the nonce the envelope was PUBLISHED with is the
// nonce quoted to command-delivery. Asserting the whole call — tenant, command token, nonce —
// is deliberate: a park of the right command under the wrong nonce moves nothing, and a park
// under a nonce lifted from somewhere else (the command token, say) is the re-arm this design
// refuses. Only comparing the exact triple can tell those apart.
func TestOfflineCommandIsParkedQuotingItsDispatchNonce(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{}
	pub := &fakePublisher{}
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}} // registered, asleep
	parker := &fakeParker{parked: true}
	m := parkMetrics()
	d := NewDispatcher(nil, pub, look, exec, nil, nil, m, Options{Parker: parker})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "nonce-7", ack))

	assert.Equal(t, []parkCall{{tenant: "acme", commandToken: "c1", nonce: "nonce-7"}}, parker.parks(),
		"the command is parked under the tenant, command token and dispatch nonce it was published with")
	assert.Equal(t, 0, exec.callCount(), "an offline device is never actuated")
	assert.Empty(t, pub.responses(), "a parked command has no outcome to report — it was never sent")
	assert.True(t, ack.acked(), "a parked command is settled: the row is where it should be, so do not redeliver")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ServedOffline), "every offline command counts on ServedOffline")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkSettled), "a park that MOVED the row is not a settle")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkErrors))
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkSkipped))
}

// TestSettledParkAcksAndCountsSettled: (false, nil) means the row moved on under this request —
// answered, cancelled, expired, or re-claimed by a wake drain. That is an OUTCOME, not a
// failure. It must ACK (retrying would burn redeliveries on a row already where it belongs) and
// it must be counted apart from an error, or a normal race would look like a command-delivery
// outage on the graph an operator pages off.
func TestSettledParkAcksAndCountsSettled(t *testing.T) {
	ack := newAck()
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	parker := &fakeParker{parked: false} // the row moved on under us
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{Parker: parker})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", ack))

	assert.Len(t, parker.parks(), 1, "the park was attempted")
	assert.True(t, ack.acked(), "a settled park ACKS — the row is already where it should be")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ParkSettled), "a settle is counted, and counted apart from an error")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkErrors), "a settle is not a fault")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ServedOffline))
}

// TestFailedParkDoesNotAckSoItRedelivers is the RETRY mechanism, and it is asserted as an ack
// count of ZERO rather than "not acked" so the failure message names what actually happened.
//
// 🔴 If a failed park acked, the row would sit in SENT forever telling the old lie — the very
// state parking exists to eliminate — and nothing would ever try again. Leaving the message
// unacked lets it redeliver at AckWait (bounded by MaxDeliver, so a command-delivery outage
// that outlasts the budget degrades to the pre-PARKED behaviour rather than looping forever).
func TestFailedParkDoesNotAckSoItRedelivers(t *testing.T) {
	ack := newAck()
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	parker := &fakeParker{err: errors.New("command-delivery unreachable")}
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{Parker: parker})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", ack))

	assert.Len(t, parker.parks(), 1, "the park was attempted")
	assert.Equal(t, 0, ack.count(),
		"a park that could not be established must NOT ack — the redelivery is the only retry this row gets")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ParkErrors), "the fault is visible: commands are sitting in SENT")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkSettled), "an unreachable command-delivery is not a settled row")
	// 🔴 ServedOffline counts a SETTLED message, not an ATTEMPT. This message was not acked, so
	// it will be redelivered and pass through here again — up to MaxDeliver times. Counting on
	// entry would report one offline command as several, and the inflation would land during
	// exactly the outage an operator is reading this counter to understand.
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ServedOffline),
		"an unsettled park must not count the offline command yet; the redelivery will count it")
}

// TestRetriedParkCountsTheOfflineCommandOnce is the other half of that property, and it is the
// one a single-attempt test cannot express: the count must not grow with the retries.
func TestRetriedParkCountsTheOfflineCommandOnce(t *testing.T) {
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	parker := &fakeParker{err: errors.New("command-delivery unreachable")}
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{Parker: parker})

	// Two failed attempts — the message redelivering after ack-wait — and then a success.
	for i := 0; i < 2; i++ {
		d.dispatch(context.Background(),
			liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", newAck()))
	}
	parker.err = nil
	parker.parked = true
	ack := newAck()
	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", ack))

	assert.Len(t, parker.parks(), 3, "every attempt reached command-delivery")
	assert.Equal(t, 1, ack.count(), "only the attempt that landed settles the message")
	assert.Equal(t, 2.0, testutil.ToFloat64(m.ParkErrors), "both failures are visible as faults")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ServedOffline),
		"ONE offline command must count once however many redeliveries it took to hand back")
}

// TestCommandWithNoDispatchNonceIsNotParked: an empty nonce means the publisher stamped none —
// a command published by an older build, or by something other than the delivery sweep.
//
// 🔴 The assertion that matters is that the parker is NOT CALLED. Parking on a match that
// ignores the nonce is exactly the re-arm the nonce exists to prevent: the row could since have
// been claimed and RUN, and a status-only park would return it to the dispatchable set for a
// second physical actuation. Leaving it in SENT is worse operationally and safe; parking it
// blind is neither.
func TestCommandWithNoDispatchNonceIsNotParked(t *testing.T) {
	ack := newAck()
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	parker := &fakeParker{parked: true} // would happily park — the production code must not ask it to
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{Parker: parker})

	// liveWorkTok stamps no nonce — the pre-nonce wire shape.
	d.dispatch(context.Background(), liveWorkTok("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, ack))

	assert.Empty(t, parker.parks(), "a command with no dispatch nonce must NOT be parked — there is nothing to match on")
	assert.True(t, ack.acked(), "it is ack-dropped exactly as it was before parking existed")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ParkSkipped), "the declined park is visible rather than silent")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkErrors), "declining to park is not a fault")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ServedOffline))
}

// TestNilParkerDegradesToAckDrop: parking is wired OFF (no command-delivery coordinate). Unlike
// the claimer — whose absence fails CLOSED, because dispatching an unclaimed command is
// irreversible — the parker fails OPEN: the row stays SENT and rides its TTL, which is
// precisely the behaviour that existed before parking did. Refusing to ack instead would wedge
// the durable consumer on an instance that simply has no command-delivery endpoint configured.
func TestNilParkerDegradesToAckDrop(t *testing.T) {
	ack := newAck()
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{}) // no Parker

	// A nonce IS present: the only reason not to park here is the missing parker, so a nil-check
	// that had been dropped would fault on the nil interface rather than quietly take the
	// no-nonce branch and pass this test for the wrong reason.
	require.NotPanics(t, func() {
		d.dispatch(context.Background(),
			liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", ack))
	}, "an unwired parker must degrade, never fault")

	assert.True(t, ack.acked(), "with no parker the command is ack-dropped, as before parking existed")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ParkSkipped), "an unwired parker shows up on a graph")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ServedOffline))
}

// TestNotServedCommandIsNeverParked is the boundary between the two non-live reachabilities,
// and it matters because they are OTHER PEOPLE'S COMMANDS versus OURS. This cross-tenant
// consumer sees every other protocol adapter's device-commands traffic — the dominant case by
// volume. Parking one of those would hand back a command this adapter has no claim on and no
// knowledge of, moving another adapter's row out from under it. Only a device THIS adapter
// serves may be parked.
func TestNotServedCommandIsNeverParked(t *testing.T) {
	ack := newAck()
	look := &fakeLookup{reaches: map[string]Reach{}} // no index entry ⇒ another protocol's device
	parker := &fakeParker{parked: true}
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{Parker: parker})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "mqtt-dev", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", ack))

	assert.Empty(t, parker.parks(), "a device we do not serve is another protocol's — never park its command")
	assert.True(t, ack.acked(), "it is ack-dropped, advancing our cursor without stealing from another consumer")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.NotServed), "counted as not-served")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ServedOffline), "a not-served device is not a served-but-offline one")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkSkipped), "no park was declined, because none was ever appropriate")
}

// TestParkAbortedByEvictionLeavesTheMessageUnacked: the leadership term is lost WHILE the park
// request is in flight. The park's error is then an artifact of losing the context, not
// evidence that command-delivery is unhealthy — so it must not count as a ParkError (which
// would make every failover look like an outage) — and the message must be left UNACKED so the
// next leader redelivers it and parks it properly. Nothing was actuated, so there is no
// seal-fate reason to ack.
func TestParkAbortedByEvictionLeavesTheMessageUnacked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ack := newAck()
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	parker := &fakeParker{err: context.Canceled}
	parker.onCall = cancel // the term is evicted while this park is in flight
	m := parkMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, look, &fakeExecutor{}, nil, nil, m, Options{Parker: parker})

	d.dispatch(ctx, liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", ack))

	assert.Equal(t, 0, ack.count(), "an evicted term leaves the message for the next leader, never acks it")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.ParkErrors),
		"a park aborted by eviction is not a park failure — counting it would make every failover read as an outage")
}

// TestRunRoutesAnOfflineServedDeviceToItsWorker is the ROUTING half, and it exercises the real
// Run loop because nothing else can.
//
// 🔴 The reader's route-time pre-filter used to drop BOTH non-live reachabilities. It now drops
// only ReachNotServed, because parking is a network round trip and the JetStream read loop is
// the one goroutine that must not make one — so an offline SERVED device is routed to its shard
// worker, which parks it there. Every other test in this block calls dispatch() directly and
// would pass unchanged against a reader that still dropped offline commands at the door; only
// driving Run can tell the difference. The not-served message is carried alongside as the
// contrast: it must still be dropped at the door and never reach a worker or the parker.
func TestRunRoutesAnOfflineServedDeviceToItsWorker(t *testing.T) {
	offlineAck, mqttAck := newAck(), newAck()
	rdr := &scriptReader{msgs: []messaging.Message{
		// A device this adapter serves, registered but asleep: must reach the worker and be parked.
		cmdMsgNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "nonce-7", offlineAck),
		// Another protocol's device: must be dropped at route time, never parked.
		cmdMsgNonce("acme", "mqtt-dev", "c2", CommandRead, `{"path":"/3/0/9"}`, "nonce-8", mqttAck),
	}}
	exec := &fakeExecutor{}
	look := &fakeLookup{reaches: map[string]Reach{"acme/pump-1": ReachOffline}}
	parker := &fakeParker{parked: true}
	m := parkMetrics()
	d := NewDispatcher(rdr, &fakePublisher{}, look, exec, nil, nil, m, Options{Parker: parker})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return offlineAck.acked() && mqttAck.acked() }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, []parkCall{{tenant: "acme", commandToken: "c1", nonce: "nonce-7"}}, parker.parks(),
		"the offline SERVED device's command reached a worker and was parked there — the reader did not drop it")
	assert.Equal(t, 0, exec.callCount(), "neither device was actuated")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.NotServed), "the not-served command was dropped at route time")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ServedOffline))
}

// --- parse() poison ---------------------------------------------------------

func TestParsePoisonAcks(t *testing.T) {
	d := newDispatcher(nil, &fakePublisher{}, &fakeLookup{}, &fakeExecutor{})

	// Unparseable tenant in the subject.
	badSubjAck := newAck()
	badSubj := messaging.NewConsumedMessage("no-tenant-here", []byte(`{}`), 1, nil, badSubjAck)
	_, ok := d.parse(badSubj)
	assert.False(t, ok)
	assert.True(t, badSubjAck.acked(), "a subject with no parseable tenant is acked as poison")

	// Undecodable envelope body.
	badBodyAck := newAck()
	badBody := messaging.NewConsumedMessage("inst.acme.device-commands.pump-1", []byte(`not json`), 1, nil, badBodyAck)
	_, ok = d.parse(badBody)
	assert.False(t, ok)
	assert.True(t, badBodyAck.acked(), "an undecodable envelope is acked as poison")
}

// --- Run() end to end + per-device serialization ----------------------------

// scriptReader serves a fixed list of messages once, then blocks until ctx is cancelled.
type scriptReader struct {
	mu   sync.Mutex
	msgs []messaging.Message
	i    int
}

func (r *scriptReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.mu.Lock()
	if r.i < len(r.msgs) {
		m := r.msgs[r.i]
		r.i++
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}

func (r *scriptReader) HandleResponse(error) {}

func TestRunProcessesAndAcksEachMessage(t *testing.T) {
	liveAck, mqttAck := newAck(), newAck()
	rdr := &scriptReader{msgs: []messaging.Message{
		cmdMsg("acme", "pump-1", CommandRead, `{"path":"/3/0/9"}`, liveAck),   // served + live
		cmdMsg("acme", "mqtt-dev", CommandRead, `{"path":"/3/0/9"}`, mqttAck), // not served
	}}
	exec := &fakeExecutor{result: OpResult{Op: labelRead, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := newDispatcher(rdr, pub, look, exec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return liveAck.acked() && mqttAck.acked() }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, 1, exec.callCount(), "only the served+live device is dispatched to")
	assert.Len(t, pub.responses(), 1, "one response, for the live device")
}

// Per-device serialization (B3): two commands for the SAME device must not run concurrently — the
// second must not start while the first is in flight, or a firmware write/execute could reorder.
func TestPerDeviceCommandsAreSerialized(t *testing.T) {
	a1, a2 := newAck(), newAck()
	rdr := &scriptReader{msgs: []messaging.Message{
		cmdMsgTok("acme", "pump-1", "cmd-fw-1", CommandWrite, `{"path":"/5/0/1","value":"a"}`, a1),
		cmdMsgTok("acme", "pump-1", "cmd-fw-2", CommandExecute, `{"path":"/5/0/2"}`, a2),
	}}
	exec := &fakeExecutor{
		result:  OpResult{Op: labelWrite, Success: true},
		started: make(chan string, 2),
		gate:    make(chan struct{}),
	}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := newDispatcher(rdr, &fakePublisher{}, look, exec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	// The first command starts and blocks on the gate.
	first := <-exec.started
	assert.Equal(t, "/5/0/1", first, "commands run in stream order")
	// The second must NOT have started while the first is in flight (same device ⇒ same worker).
	select {
	case <-exec.started:
		t.Fatal("a second command for the same device started before the first completed (not serialized)")
	case <-time.After(150 * time.Millisecond):
	}
	// Release the first; the second now runs.
	close(exec.gate)
	second := <-exec.started
	assert.Equal(t, "/5/0/2", second)

	require.Eventually(t, func() bool { return a1.acked() && a2.acked() }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
}

// TestWakeDrainEndToEnd wires the WHOLE L4b chain in-process: a real ConnTable is the Dispatcher's
// conn lookup, SetOnLive is wired to Dispatcher.Drain, and Run's workers are live. Binding a device
// (a Register wake) must, with no live-stream command at all, drain the device's held commands from
// command-delivery (faked) and dispatch them to the now-live conn, oldest-first. This is the seam the
// per-unit drain()/onLive() tests do not exercise together: trigger → enqueue → worker → drain → op.
func TestWakeDrainEndToEnd(t *testing.T) {
	connTable := NewConnTable()
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"coaps://fw"}`), Status: statusParked},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`), Status: statusParked},
	}}
	// An empty reader so Run's consume loop just parks on ctx — the drain is driven purely by the wake.
	d := NewDispatcher(&scriptReader{}, pub, connTable, exec, ff, &fakeClaimer{won: true}, Metrics{}, Options{})
	connTable.SetOnLive(d.Drain)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	// Wait until Run signals ready (queues published), so the wake's Drain is not a standby no-op —
	// exactly what serveAsLeader waits on before serving the transport (the S1 ordering fix).
	select {
	case <-d.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not become ready")
	}

	// A Register wake: Bind installs the live conn AND fires onLive → Drain.
	connTable.Bind("acme", "pump-1", "id-1", 100, newFakeConn(1))

	require.Eventually(t, func() bool { return exec.callCount() == 2 }, 2*time.Second, 5*time.Millisecond,
		"both held commands are drained to the device on its wake")
	resp := pub.responses()
	require.Len(t, resp, 2)
	assert.Equal(t, "c1", resp[0].CommandToken, "drained oldest-first")
	assert.Equal(t, "c2", resp[1].CommandToken)

	cancel()
	<-done
}

// Cross-device concurrency: commands for DIFFERENT devices (on different shards) run concurrently.
func TestCrossDeviceCommandsRunConcurrently(t *testing.T) {
	// Pick two tokens that hash to different worker shards so they are genuinely parallel.
	d0 := &Dispatcher{workers: DefaultWorkers}
	tokX, tokY := "devX", "devY"
	require.NotEqual(t, d0.shard(tokX), d0.shard(tokY), "test precondition: tokens must land on different shards")

	rdr := &scriptReader{msgs: []messaging.Message{
		cmdMsg("acme", tokX, CommandRead, `{"path":"/3/0/1"}`, newAck()),
		cmdMsg("acme", tokY, CommandRead, `{"path":"/3/0/2"}`, newAck()),
	}}
	exec := &fakeExecutor{
		result:  OpResult{Op: labelRead, Success: true},
		started: make(chan string, 2),
		gate:    make(chan struct{}),
	}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/" + tokX: ReachLive, "acme/" + tokY: ReachLive}}
	d := newDispatcher(rdr, &fakePublisher{}, look, exec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	// BOTH commands should start even though neither has been released — proving concurrency.
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case p := <-exec.started:
			got[p] = true
		case <-time.After(2 * time.Second):
			t.Fatal("both cross-device commands did not start concurrently")
		}
	}
	assert.True(t, got["/3/0/1"] && got["/3/0/2"], "both devices' commands ran concurrently")
	close(exec.gate)
	cancel()
	<-done
}

// --- ADR-077 lifecycle gate -------------------------------------------------

// 🔴 A COMMAND IS A PHYSICAL ACTUATION, and there are TWO paths by which one reaches a
// device here. command-delivery's sweep gate stops NEW publishes into a deleted tenant,
// and stopping there would have looked complete:
//
//   - the LIVE path still receives commands already durable on the stream when the
//     delete happened;
//   - the WAKE-DRAIN re-fetches commands left in SENT and fires them when a sleeping
//     device next registers — arbitrarily long after the delete, off a (tenant, token)
//     remembered from a registration that predates it, on a path where nothing else
//     consults the lifecycle (PSK bindings are static config and Update never
//     re-resolves).
//
// So the gate sits at process(), the union both paths funnel through, and these are the
// tests for each half.
func TestProcessRefusesALiveCommandForADeletedTenant(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ack := &fakeAck{}
	d := NewDispatcher(nil, pub, look, exec, nil, nil, Metrics{},
		Options{TenantDeleted: func(tenant string) bool { return tenant == "acme" }})

	w := liveWorkTok("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, ack)
	d.process(context.Background(), task{deviceToken: "pump-1", live: &w})

	assert.Equal(t, 0, exec.callCount(), "no CoAP op may run for a deleted tenant")
	assert.Empty(t, pub.responses(), "a refusal is not a command outcome to report")
	assert.True(t, ack.acked(), "the message must be acked, or this refusal redelivers until MaxDeliver")
}

func TestProcessRefusesAWakeDrainForADeletedTenant(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, nil, Metrics{},
		Options{TenantDeleted: func(tenant string) bool { return tenant == "acme" }})

	d.process(context.Background(), task{deviceToken: "pump-1", drain: &drainJob{tenant: "acme", deviceToken: "pump-1"}})

	assert.Equal(t, 0, ff.callCount(), "a deleted tenant's held commands must not even be fetched")
	assert.Equal(t, 0, exec.callCount(), "no held command may actuate for a deleted tenant")
}

// The negative control for both: with the gate wired and answering "live", each path
// still actuates. Without it, a gate that refused everything would satisfy both tests
// above and silently stop every command on the instance.
func TestProcessStillActuatesWhenTheGateSaysLive(t *testing.T) {
	live := func() Options { return Options{TenantDeleted: func(string) bool { return false }} }

	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := NewDispatcher(nil, &fakePublisher{}, look, exec, nil, nil, Metrics{}, live())
	w := liveWorkTok("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, &fakeAck{})
	d.process(context.Background(), task{deviceToken: "pump-1", live: &w})
	assert.Equal(t, 1, exec.callCount(), "a live tenant's command must still actuate")

	exec2 := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	ff := &fakeFetcher{cmds: []DrainCommand{{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked}}}
	d2 := NewDispatcher(nil, &fakePublisher{}, look, exec2, ff, &fakeClaimer{won: true}, Metrics{}, live())
	d2.process(context.Background(), task{deviceToken: "pump-1", drain: &drainJob{tenant: "acme", deviceToken: "pump-1"}})
	assert.Equal(t, 1, exec2.callCount(), "a live tenant's held command must still drain")
}
