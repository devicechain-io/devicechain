// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// brokerManager is an event-processing manager on the embedded broker. build runs in the
// manager's create callback, where a reader has to be made.
func (b *detectBroker) brokerManager(t *testing.T, build func(*messaging.NatsManager) error) (*messaging.NatsManager, *core.Microservice, *deadletter.Producer) {
	t.Helper()
	ms := &core.Microservice{InstanceId: coveredInstance, FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: b.host, Port: b.port}
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), build)
	producer := deadletter.NewProducer(ms)
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(producer))
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	return nmgr, ms, producer
}

// streamHeld is how many messages the instance's stream for suffix holds.
func (b *detectBroker) streamHeld(t *testing.T, suffix string) uint64 {
	t.Helper()
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(messaging.StreamName(coveredInstance, suffix))
	if errors.Is(err, nats.ErrStreamNotFound) {
		return 0
	}
	require.NoError(t, err)
	return info.State.Msgs
}

// A shed letter and the exhausted letter about the same derived event are different records, and
// the broker's dedup window must keep both. The shed letter is written on a Done delivery whose
// ack is lost; the event then redelivers, a sibling starts failing, and at the cap it is lettered
// as exhausted. Were the shed letter written under the message's own dedup id, the broker would
// take the exhausted letter for a duplicate of it and drop it — the one record that the event's
// other actions never ran.
func TestAShedLetterDoesNotSwallowTheExhaustedLetter(t *testing.T) {
	b := startDetectBroker(t)
	nmgr, _, _ := b.brokerManager(t, func(*messaging.NatsManager) error { return nil })
	deadWriter, err := nmgr.NewWriter(streams.DeadLetters)
	require.NoError(t, err)

	commands := &flakySink{}
	rd, _ := reactWithConnectors(t, connectorRule(httpCallAction(), sendCommandAction()), commands, &connSink{},
		&meterGate{}, deadWriter, testShedBudget)

	commands.fail = false
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{})) // Done: the shed letter
	commands.fail = true
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, &fakeAck{})) // exhausted

	require.Equal(t, uint64(2), b.streamHeld(t, streams.DeadLetters),
		"the dead-letter stream must hold the shed letter AND the exhausted letter")
}

// Two shed actions of one event are two letters on the real stream, and a redelivery of the same
// event — its ack lost — adds neither again: each part's id collapses onto its first write inside
// the stream's real duplicate window.
func TestTwoShedActionsInOneEventAreTwoStoredLetters(t *testing.T) {
	b := startDetectBroker(t)
	nmgr, _, _ := b.brokerManager(t, func(*messaging.NatsManager) error { return nil })
	deadWriter, err := nmgr.NewWriter(streams.DeadLetters)
	require.NoError(t, err)
	rd, _ := reactWithConnectors(t, connectorRule(httpCallAction(), publishAction()), nil, &connSink{},
		&meterGate{}, deadWriter, testShedBudget)

	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{}))
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 2, &fakeAck{})) // the same delivery, redelivered

	require.Equal(t, uint64(2), b.streamHeld(t, streams.DeadLetters))
}

// flakySink is a command sink whose failure the test switches.
type flakySink struct{ fail bool }

func (s *flakySink) Send(context.Context, react.CommandRequest) error {
	if s.fail {
		return errors.New("command-delivery unreachable")
	}
	return nil
}

// A DETECT restart that replays detections it had already published must neither store them twice
// nor have REACT charge the tenant for them twice.
//
// 🔴 THE DEFECT WAS THE REPLAY. DETECT publishes before it checkpoints, so a crash between the two
// replays every event past the last checkpoint and re-publishes every detection they fire. The
// derived-events stream stored each re-publish as a new message, REACT dispatched it again, and the
// re-dispatch was charged against the tenant's outbound ceiling on top of the original — so a
// tenant well inside its ceiling had its duplicates shed, and those sheds dead-lettered as if its
// own traffic had been over the limit. Derived events now carry a dedup id from their detection
// identity and the stream declares a duplicate window, so the broker stores the replay once.
//
// Everything that matters is real: the embedded broker's stream and dedup window, the DETECT
// replay reader (the manager's NewReplayReader) driven through replayToHead, the derived-events
// writer, REACT's durable reader and consumer, and the core limiter as the source gate. A tenant
// firing at 50 a second over the last 10 seconds, against a 100-a-second ceiling, is replayed from
// the start by a successor that lost the checkpoint.
func TestAReplayedDetectionIsStoredOnceAndNeverLettered(t *testing.T) {
	const events = 500
	b := startDetectBroker(t)

	var reactReader messaging.MessageReader
	nmgr, ms, producer := b.brokerManager(t, func(m *messaging.NatsManager) error {
		// The reader main.go builds, with the term gate held open: one slot, so this also
		// shows a one-at-a-time reader drains a replayed backlog.
		r, err := m.NewReader(streams.DerivedEvents, ReactReaderOptions(func() bool { return true })...)
		reactReader = r
		return err
	})
	derivedWriter, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	// device-management's writer is what creates resolved-events in production.
	_, err = nmgr.NewWriter(streams.ResolvedEvents)
	require.NoError(t, err)

	// REACT, the way main.go wires it.
	dead := &deadRecorder{}
	sink := &connSink{}
	gate := core.NewTenantRateLimiter(core.StaticCeiling(100, 200))
	rd := NewReactDispatcher(ms, reactReader, reactFakeResolver{rule: connectorRule(httpCallAction()), found: true},
		nil, nil, sink, gate, producer.NewSink(dead), testShedBudget, NewReactMetrics(ms))
	require.NoError(t, rd.Start(context.Background()))
	t.Cleanup(func() { _ = rd.Stop(context.Background()) })

	// The resolved events: 50 a second over the last 10 seconds of platform time.
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	start := time.Now().Add(-10 * time.Second)
	for i := 0; i < events; i++ {
		at := start.Add(time.Duration(i) * 20 * time.Millisecond)
		ev := &dmmodel.ResolvedEvent{
			Source: "http1", SourceDeviceToken: fmt.Sprintf("d%03d", i), ProfileVersionToken: "p@1",
			OccurredTime: at, ProcessedTime: at, EventType: esmodel.Measurement,
			Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
				OccurredTime: at, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: "90"}},
			}}},
		}
		body, err := dmproto.MarshalResolvedEvent(ev)
		require.NoError(t, err)
		_, err = js.Publish(messaging.ScopedSubject(coveredInstance, "acme", streams.ResolvedEvents), body)
		require.NoError(t, err)
	}

	// DETECT, twice: the first run publishes every detection; its successor lost the checkpoint
	// and replays the stream from the start.
	reg := thresholdReg(t)
	for run := 1; run <= 2; run++ {
		rp := &ResolvedEventsProcessor{
			Replay: nmgr,
			Store:  newTestStore(t),
			cfg: Config{
				PartitionId:        "singleton",
				Suffix:             streams.ResolvedEvents,
				CheckpointEvents:   1000,
				CheckpointInterval: time.Hour,
				TickInterval:       time.Hour,
				Clock:              detectcore.RealClock{},
			},
			registry:  reg,
			publisher: runtime.NewPublisher(derivedWriter, reg, (*DetectMetrics)(nil)),
			clock:     detectcore.RealClock{},
			procCtx:   context.Background(),
		}
		require.NoError(t, rp.restore(context.Background()))
		require.NoError(t, rp.replayToHead(), "run %d", run)
	}

	// REACT has consumed everything once the durable has nothing pending or in flight.
	deadline := time.Now().Add(30 * time.Second)
	for {
		pending, ackPending, err := reactReader.(interface {
			Backlog(context.Context) (uint64, uint64, error)
		}).Backlog(context.Background())
		require.NoError(t, err)
		if pending == 0 && ackPending == 0 && len(sink.snapshot()) >= events {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("REACT did not drain: pending=%d ackPending=%d dispatched=%d", pending, ackPending, len(sink.snapshot()))
		}
		time.Sleep(50 * time.Millisecond)
	}

	require.Equal(t, uint64(events), b.streamHeld(t, streams.DerivedEvents),
		"derived-events must hold each detection once after a replay")
	// The replay above is seconds old, well inside the broker's own two-minute default window, so
	// the collapse alone does not show the window a restart after a bad rollout needs. Read it off
	// the stream the writer created.
	info, err := js.StreamInfo(messaging.StreamName(coveredInstance, streams.DerivedEvents))
	require.NoError(t, err)
	require.GreaterOrEqual(t, info.Config.Duplicates, 15*time.Minute,
		"the derived-events stream's duplicate window is shorter than the restart it must cover")
	tokens := map[string]int{}
	for _, req := range sink.snapshot() {
		tokens[req.Token]++
	}
	require.Len(t, tokens, events, "REACT must dispatch every detection")
	for token, n := range tokens {
		require.Equal(t, 1, n, "detection %s was dispatched %d times", token, n)
	}
	for _, e := range dead.letters(t) {
		require.NotEqual(t, deadletter.ReasonShed, e.Reason, "a replayed detection was shed and lettered: %+v", e)
	}
}
