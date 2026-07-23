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
// carrying a delivery envelope, with a fake ack handle.
func cmdMsg(tenant, token, name, payload string, ack messaging.Acknowledger) messaging.Message {
	env := deliveryEnvelope{Token: "cmd-" + token, DeviceToken: token, Name: name}
	if payload != "" {
		raw := json.RawMessage(payload)
		env.Payload = &raw
	}
	value, _ := json.Marshal(env)
	subject := "inst." + tenant + ".device-commands." + token
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
	return NewDispatcher(rdr, pub, look, exec, Metrics{}, Options{})
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
		cmdMsg("acme", "pump-1", CommandWrite, `{"path":"/5/0/1","value":"a"}`, a1),
		cmdMsg("acme", "pump-1", CommandExecute, `{"path":"/5/0/2"}`, a2),
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
