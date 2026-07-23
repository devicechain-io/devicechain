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
	env := deliveryEnvelope{Token: cmdToken, DeviceToken: deviceToken, Name: name}
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
	return NewDispatcher(rdr, pub, look, exec, nil, Metrics{}, Options{})
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

// liveWorkTok builds a live work item with an explicit command token (production commands are
// per-tenant unique, ADR-042).
func liveWorkTok(tenant, deviceToken, cmdToken, name, payload string, ack messaging.Acknowledger) work {
	msg := cmdMsgTok(tenant, deviceToken, cmdToken, name, payload, ack)
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
func TestDrainDispatchesHeldCommands(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`)},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`)},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, Metrics{}, Options{})

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
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`)}, // already fired live
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`)},           // fresh
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, Metrics{}, Options{})

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
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`)},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`)},
		{Token: "c3", Name: CommandRead, Payload: []byte(`{"path":"/3/0/0"}`)},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, Metrics{}, Options{})

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
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`)},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`)},
	}}
	d := NewDispatcher(nil, pub, look, exec, ff, Metrics{}, Options{})

	d.drain(ctx, drainJob{tenant: "acme", deviceToken: "pump-1"})

	assert.Equal(t, 1, exec.callCount(), "c1's op ran; the eviction stops the drain before c2")
	assert.Empty(t, pub.responses(), "no response from the evicted term (the c1 result is unreliable)")
}

// TestDrainFetchErrorNoDispatch: a fetch failure dispatches nothing and is retried on the next wake.
func TestDrainFetchErrorNoDispatch(t *testing.T) {
	exec := &fakeExecutor{}
	ff := &fakeFetcher{err: errors.New("command-delivery unreachable")}
	look := &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
	d := NewDispatcher(nil, &fakePublisher{}, look, exec, ff, Metrics{}, Options{})

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
	ff := &fakeFetcher{cmds: []DrainCommand{{Token: "c1", Name: CommandRead, Payload: []byte(`{"path":"/3/0/0"}`)}}}
	d := NewDispatcher(nil, &fakePublisher{}, &fakeLookup{}, &fakeExecutor{}, ff, Metrics{}, Options{})

	d.Drain("acme", "pump-1") // no active term → no-op

	assert.Equal(t, 0, ff.callCount(), "a standby (no serving term) does not fetch")
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
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"coaps://fw"}`)},
		{Token: "c2", Name: CommandExecute, Payload: []byte(`{"path":"/5/0/2"}`)},
	}}
	// An empty reader so Run's consume loop just parks on ctx — the drain is driven purely by the wake.
	d := NewDispatcher(&scriptReader{}, pub, connTable, exec, ff, Metrics{}, Options{})
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
