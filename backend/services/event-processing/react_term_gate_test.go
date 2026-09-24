// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// These tests drive the REACT reader main actually builds — wireReactDispatcher and
// newReactReader — against a real embedded broker, because what they pin is broker
// behaviour: which replica's reader the durable hands messages to.
//
// termGatePollBound is two of core/messaging's termGatePoll (50ms): the longest a
// term-gated reader keeps handing messages out after its gate closes.
const termGatePollBound = 100 * time.Millisecond

// reactFixture is one replica's view of the broker: the manager its REACT reader is built
// on, and a JetStream context the test inspects the shared durable through.
type reactFixture struct {
	area string
	js   nats.JetStreamContext
}

// startReactFixture brings up an embedded broker and sets the globals wireReactDispatcher
// reads, restoring them afterwards. Every manager built with newManager shares one
// Microservice identity, so every REACT reader in the test binds the SAME durable — which
// is what two replicas of the service do.
func startReactFixture(t *testing.T) *reactFixture {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevGate := Microservice, Configuration, DetectTermGate
	prevDead, prevMetrics, prevReact, prevRules := DeadLetters, ReactMetrics, ReactDispatcher, DetectRuleStore
	t.Cleanup(func() {
		Microservice, Configuration, DetectTermGate = prevMs, prevCfg, prevGate
		DeadLetters, ReactMetrics, ReactDispatcher, DetectRuleStore = prevDead, prevMetrics, prevReact, prevRules
	})

	area := fmt.Sprintf("react-term-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: area, FunctionalArea: area, Readiness: core.NewReadinessGate()}
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Configuration = &config.EventProcessingConfiguration{
		OutboundMessagesPerSecond: config.DefaultOutboundMessagesPerSecond,
		OutboundBurst:             config.DefaultOutboundBurst,
	}
	DeadLetters = deadletter.NewProducer(Microservice)
	ReactMetrics = processor.NewReactMetrics(Microservice)
	DetectRuleStore = nil
	DetectTermGate = processor.NewTermGate()

	nc, err := nats.Connect(fmt.Sprintf("nats://%s:%d", host, port))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return &reactFixture{area: area, js: js}
}

// newManager starts one NatsManager, as one replica's process would.
func (f *reactFixture) newManager(t *testing.T) *messaging.NatsManager {
	t.Helper()
	nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	return nmgr
}

// wire runs the real wireReactDispatcher against nmgr with gate as the DETECT term gate,
// and returns the dispatcher it built.
func (f *reactFixture) wire(t *testing.T, nmgr *messaging.NatsManager, gate *processor.TermGate) *processor.ReactDispatcher {
	t.Helper()
	DetectTermGate = gate
	require.NoError(t, wireReactDispatcher(nmgr))
	require.NotNil(t, ReactDispatcher)
	return ReactDispatcher
}

// consumer reads the shared REACT durable's state off the broker.
func (f *reactFixture) consumer(t *testing.T) *nats.ConsumerInfo {
	t.Helper()
	info, err := f.js.ConsumerInfo(messaging.StreamName(f.area, streams.DerivedEvents),
		messaging.DurableName(f.area, f.area, streams.DerivedEvents))
	require.NoError(t, err)
	return info
}

// publish writes n derived events for one tenant. The payload is undecodable on purpose:
// the dispatcher acks an undecodable event without touching a sink, so an acked message is
// exactly a message a dispatcher was handed.
func (f *reactFixture) publish(t *testing.T, nmgr *messaging.NatsManager, n int) {
	t.Helper()
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	ctx := core.WithTenant(context.Background(), "acme")
	subject := messaging.ScopedSubject(f.area, "acme", streams.DerivedEvents)
	for i := 0; i < n; i++ {
		require.NoError(t, w.WriteMessages(ctx, messaging.Message{Subject: subject, Value: []byte("not a derived event")}))
	}
}

func stopDispatcher(t *testing.T, rd *processor.ReactDispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rd.Stop(ctx))
}

// A replica that does not hold the DETECT term dispatches nothing. REACT charges each
// tenant's outbound ceiling in memory per process, so a standby that consumed a share of
// the stream charged a second copy of every tenant's ceiling.
//
// Three phases, on the dispatcher main wires: closed (nothing delivered), open (the event
// is dispatched and acked — so the gate is not simply shut for good), closed again
// (nothing more is delivered).
func TestReactReaderParksWithoutTheDetectTerm(t *testing.T) {
	f := startReactFixture(t)
	nmgr := f.newManager(t)
	gate := processor.NewTermGate()
	rd := f.wire(t, nmgr, gate)
	require.NoError(t, rd.Start(context.Background()))
	defer stopDispatcher(t, rd)

	f.publish(t, nmgr, 1)
	time.Sleep(3 * time.Second)
	require.Zero(t, f.consumer(t).Delivered.Stream,
		"the REACT reader consumed a derived event on a replica that does not hold the DETECT term")

	gate.Enter(func() bool { return true })
	require.Eventually(t, func() bool { return f.consumer(t).AckFloor.Stream == 1 }, 5*time.Second, 20*time.Millisecond,
		"the REACT reader did not dispatch once the term was held")

	gate.Exit()
	// Longer than a fetch long-poll, so a Fetch in flight when the gate closed has expired
	// and the next message cannot be delivered into it.
	time.Sleep(1500 * time.Millisecond)
	f.publish(t, nmgr, 1)
	time.Sleep(2 * time.Second)
	require.Equal(t, uint64(1), f.consumer(t).Delivered.Stream,
		"the REACT reader kept consuming after the DETECT term ended")
}

// Two replicas share the REACT durable. With neither holding the term, the durable
// delivers nothing; with only A holding it, A takes all twenty and acks all twenty.
//
// The second phase's counts are what a standby that had pulled a batch would disturb:
// deliveries held in B's buffer would sit unacked, leaving Delivered above AckFloor and
// NumAckPending above zero.
func TestStandbyDoesNotSplitTheDurable(t *testing.T) {
	f := startReactFixture(t)
	nmgrA, nmgrB := f.newManager(t), f.newManager(t)
	gateA, gateB := processor.NewTermGate(), processor.NewTermGate()
	rdA := f.wire(t, nmgrA, gateA)
	rdB := f.wire(t, nmgrB, gateB)
	require.NoError(t, rdA.Start(context.Background()))
	defer stopDispatcher(t, rdA)
	require.NoError(t, rdB.Start(context.Background()))
	defer stopDispatcher(t, rdB)

	f.publish(t, nmgrA, 20)
	time.Sleep(3 * time.Second)
	require.Zero(t, f.consumer(t).Delivered.Consumer,
		"with neither replica holding the DETECT term, the shared REACT durable still delivered")

	gateA.Enter(func() bool { return true })
	require.Eventually(t, func() bool { return f.consumer(t).AckFloor.Consumer == 20 }, 10*time.Second, 20*time.Millisecond,
		"the replica holding the term did not dispatch all twenty")
	info := f.consumer(t)
	require.Equal(t, uint64(20), info.Delivered.Consumer,
		"the durable delivered more than the twenty the term holder acked: another replica took deliveries")
	require.Zero(t, info.NumAckPending)
}

// When the term is lost, the REACT reader gives up what it had buffered, so a replica
// that later regains the term does not hand out a copy another replica has since
// dispatched.
//
// The reader is newReactReader — the function main calls. It reads one of three messages
// (leaving two buffered), then makes ONE read that starts with the term lost and is still
// in flight, parked, when the term comes back after more than two gate polls. That is how
// the dispatcher's loop sees a term loss: a parked read does not return until the gate
// reopens, so the release at the park is the only one that can run — a read ended by a
// context timeout would be released by the end-of-stream path instead and show nothing
// about the park. The stale copy is sequence 2 on its FIRST delivery; what may come out is
// a redelivery of 2 or 3 (the broker hands a released message out again) or sequence 4.
func TestReactReaderReleasesItsBufferOnTermLoss(t *testing.T) {
	f := startReactFixture(t)
	nmgr := f.newManager(t)
	var held atomic.Bool
	held.Store(true)
	DetectTermGate.Enter(held.Load)
	reader, err := newReactReader(nmgr)
	require.NoError(t, err)

	f.publish(t, nmgr, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.StreamSeq)
	require.NoError(t, first.Ack())
	require.Equal(t, uint64(3), f.consumer(t).Delivered.Consumer,
		"the first read did not fetch all three, so nothing is buffered and this test shows nothing")

	held.Store(false)
	f.publish(t, nmgr, 1)
	type result struct {
		msg messaging.Message
		err error
	}
	done := make(chan result, 1)
	go func() {
		msg, err := reader.ReadMessage(ctx)
		done <- result{msg, err}
	}()
	time.Sleep(3 * termGatePollBound)
	select {
	case r := <-done:
		t.Fatalf("a read returned while the term was not held (err=%v)", r.err)
	default:
	}
	held.Store(true)

	r := <-done
	require.NoError(t, r.err)
	next := r.msg
	require.False(t, next.StreamSeq == 2 && next.NumDelivered == 1,
		"the reader handed out the copy it had buffered before losing the term")
	if next.StreamSeq != 4 {
		require.Contains(t, []uint64{2, 3}, next.StreamSeq)
		require.Equal(t, 2, next.NumDelivered, "a buffered message came out on its first delivery")
	}
}

// A standby's dispatcher runs parked for as long as the other replica leads, so Stop has
// to unwind a parked read promptly: a stop that waited out the lease would stall every
// rolling restart of a standby.
func TestReactStopReturnsWhileParked(t *testing.T) {
	f := startReactFixture(t)
	nmgr := f.newManager(t)
	rd := f.wire(t, nmgr, processor.NewTermGate())
	require.NoError(t, rd.Start(context.Background()))
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	stopDispatcher(t, rd)
	require.Less(t, time.Since(start), time.Second, "stopping a parked REACT dispatcher took longer than a second")
}

// REACT's share of the handover overlap: once Held goes false, no message is handed out
// later than two gate polls after it — including one that arrives in a Fetch that was
// already waiting when the term ended. The rest of the ~5 s overlap is the old owner's
// Held overshooting the server-side expiry, which is not REACT's to close.
func TestReactParksWithinOnePollOfHeldGoingFalse(t *testing.T) {
	f := startReactFixture(t)
	nmgr := f.newManager(t)
	var held atomic.Bool
	held.Store(true)
	DetectTermGate.Enter(held.Load)
	reader, err := newReactReader(nmgr)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var handedOut []time.Time
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			msg, err := reader.ReadMessage(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			handedOut = append(handedOut, time.Now())
			mu.Unlock()
			_ = msg.Ack()
		}
	}()
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	go func() {
		defer wg.Done()
		tctx := core.WithTenant(context.Background(), "acme")
		subject := messaging.ScopedSubject(f.area, "acme", streams.DerivedEvents)
		for ctx.Err() == nil {
			_ = w.WriteMessages(tctx, messaging.Message{Subject: subject, Value: []byte("x")})
			time.Sleep(5 * time.Millisecond)
		}
	}()

	time.Sleep(700 * time.Millisecond)
	closedAt := time.Now()
	held.Store(false)
	time.Sleep(1500 * time.Millisecond)
	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	before, late := 0, 0
	for _, at := range handedOut {
		if at.Before(closedAt) {
			before++
		} else if at.After(closedAt.Add(termGatePollBound)) {
			late++
		}
	}
	require.Positive(t, before, "nothing was handed out while the term was held, so this test shows nothing")
	require.Zero(t, late, "the REACT reader handed messages out more than two gate polls after the term ended")
}
