// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// These tests drive the recorder end to end on a real embedded broker, because what they
// are about is broker behaviour: the advisory is the broker's, the dedup window is the
// broker's, and the work-queue removal is the broker's. Each keeps a puller on the original
// durable, since the broker sends the advisory only on the next pull.

var recorderAreaSeq atomic.Int64

// recorderRig is one area assembled the way core/service assembles it: a Producer, a
// manager with the recorder installed, readers on suffixes, and a one-second AckWait.
type recorderRig struct {
	t       *testing.T
	area    string
	nmgr    *messaging.NatsManager
	js      nats.JetStreamContext
	readers []messaging.MessageReader
	p       *Producer
	reg     *prometheus.Registry
}

func startBroker(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	return srv
}

func newRecorderRig(t *testing.T, srv *natsserver.Server, suffixes ...string) *recorderRig {
	t.Helper()
	addr := srv.Addr().(*net.TCPAddr)
	area := fmt.Sprintf("rec-area-%d", recorderAreaSeq.Add(1))
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: area, Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = config.NatsConfiguration{Hostname: addr.IP.String(), Port: uint32(addr.Port)}
	ms.Readiness.MarkReadyWithoutAuthSurface()
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	rig := &recorderRig{t: t, area: area, p: NewProducer(ms), reg: reg}
	rig.nmgr = messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		for _, s := range suffixes {
			r, err := n.NewReader(s)
			if err != nil {
				return err
			}
			rig.readers = append(rig.readers, r)
		}
		return nil
	})
	rig.nmgr.RecordMaxDeliveries(MaxDeliveryRecorder(rig.p))
	ctx := context.Background()
	require.NoError(t, rig.nmgr.Initialize(ctx))
	rig.nmgr.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, rig.nmgr.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rig.nmgr.Stop(stopCtx)
	})
	js, err := rig.nmgr.Conn().JetStream()
	require.NoError(t, err)
	rig.js = js
	return rig
}

// publish puts one message for tenant acme on suffix and returns its sequence.
func (r *recorderRig) publish(suffix string, body []byte, headers map[string]string) uint64 {
	r.t.Helper()
	m := &nats.Msg{Subject: messaging.ScopedSubject("test", "acme", suffix), Data: body, Header: nats.Header{}}
	for k, v := range headers {
		m.Header.Set(k, v)
	}
	ack, err := r.js.PublishMsg(m)
	require.NoError(r.t, err)
	return ack.Sequence
}

// deliverAll reads the reader's next message MaxDeliver times without acking and returns
// the final delivery.
func (r *recorderRig) deliverAll(reader messaging.MessageReader) messaging.Message {
	r.t.Helper()
	var msg messaging.Message
	for i := 1; i <= messaging.MaxDeliver; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m, err := reader.ReadMessage(ctx)
		cancel()
		require.NoError(r.t, err, "delivery %d never arrived", i)
		require.Equal(r.t, i, m.NumDelivered)
		msg = m
	}
	return msg
}

func (r *recorderRig) keepPulling(reader messaging.MessageReader) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if m, err := reader.ReadMessage(ctx); err == nil {
				_ = m.Ack()
			}
		}
	}()
	r.t.Cleanup(func() { cancel(); <-done })
}

// held is every message on suffix's stream, oldest first; none when it does not exist.
func (r *recorderRig) held(suffix string) []*nats.RawStreamMsg {
	r.t.Helper()
	name := messaging.StreamName("test", suffix)
	info, err := r.js.StreamInfo(name)
	if errors.Is(err, nats.ErrStreamNotFound) {
		return nil
	}
	require.NoError(r.t, err)
	var out []*nats.RawStreamMsg
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		m, err := r.js.GetMsg(name, seq)
		if errors.Is(err, nats.ErrMsgNotFound) {
			continue
		}
		require.NoError(r.t, err)
		out = append(out, m)
	}
	return out
}

func (r *recorderRig) letters() []Envelope {
	r.t.Helper()
	var out []Envelope
	for _, m := range r.held(streams.DeadLetters) {
		e, err := Unmarshal(m.Data)
		require.NoError(r.t, err)
		out = append(out, e)
	}
	return out
}

// records reads devicechain_<area>_max_delivery_records_total{stream,outcome} by its exported
// name, failing when the series does not exist.
func (r *recorderRig) records(suffix, outcome string) float64 {
	r.t.Helper()
	name := "devicechain_" + strings.ReplaceAll(r.area, "-", "") + "_max_delivery_records_total"
	families, err := r.reg.Gather()
	require.NoError(r.t, err)
	stream := messaging.StreamName("test", suffix)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["stream"] == stream && labels["outcome"] == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	r.t.Fatalf("no %s{stream=%q,outcome=%q}", name, stream, outcome)
	return 0
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "timed out waiting for %s", what)
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *recorderRig) armSink() *Sink {
	r.t.Helper()
	w, err := r.nmgr.NewWriter(streams.DeadLetters)
	require.NoError(r.t, err)
	return r.p.NewSink(w)
}

func armLetter() Envelope {
	return Envelope{Reason: ReasonExhausted, Summary: "an edge could not be applied after every delivery attempt",
		OccurredAt: time.Now().UTC()}
}

// GUARD: the ordinary arm path is unchanged by the recorder. An arm that writes its letter
// through WriteFor and acks within the window produces ONE letter — its own — and no advisory
// at all, because an acked message never runs out of deliveries.
func TestHandledFinalDeliveryWritesOneLetter(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.RaiseAlarm)
	sink := rig.armSink()
	rig.publish(streams.RaiseAlarm, []byte(`{"k":1}`), nil)
	final := rig.deliverAll(rig.readers[0])
	require.NoError(t, sink.WriteFor(core.WithTenant(context.Background(), "acme"), final, armLetter()))
	require.NoError(t, final.Ack())
	rig.keepPulling(rig.readers[0])

	time.Sleep(3 * time.Second)
	letters := rig.letters()
	require.Len(t, letters, 1)
	require.Equal(t, ReasonExhausted, letters[0].Reason)
	require.Equal(t, KindDetectionAction, letters[0].Kind)
	require.Empty(t, rig.held(streams.MaxDeliveries), "an acked message produced a max-delivery advisory")
}

// GUARD: kills "the arm writes with Write instead of WriteFor" (no dedup id, so a second
// letter). The arm overruns its window: the broker gives up, the recorder letters the delivery,
// and THEN the arm writes its own letter about the same delivery. Exactly one letter survives.
//
// It does NOT pin the stream's dedup window: the two writes here are seconds apart, and the
// broker's own two-minute default dedups those too. The window is pinned by
// TestDeadLetterStreamsCarryTheThirtyMinuteDedupWindow.
func TestLateArmLetterDedupsAgainstTheRecorder(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.RaiseAlarm)
	sink := rig.armSink()
	seq := rig.publish(streams.RaiseAlarm, []byte(`{"k":1}`), nil)
	final := rig.deliverAll(rig.readers[0])
	rig.keepPulling(rig.readers[0])
	eventually(t, 6*time.Second, "the recorder's letter", func() bool { return len(rig.letters()) == 1 })
	require.Equal(t, ReasonNoOutcome, rig.letters()[0].Reason)

	require.NoError(t, sink.WriteFor(core.WithTenant(context.Background(), "acme"), final, armLetter()))
	held := rig.held(streams.DeadLetters)
	require.Len(t, held, 1, "the arm's late letter was stored beside the recorder's")
	stream := messaging.StreamName("test", streams.RaiseAlarm)
	durable := messaging.DurableName("test", rig.area, streams.RaiseAlarm)
	require.Equal(t, "mdl."+stream+"."+durable+".1", held[0].Header.Get(nats.MsgIdHdr))
	require.EqualValues(t, 1, seq)
}

// GUARD: an original the stream no longer holds is "gone" and not lettered — there is nothing
// to attribute it to, and a letter with no tenant is not one anybody can file.
//
// It is driven through the built func rather than the broker, and that is forced: a message
// deleted BEFORE its advisory is never advised at all (the broker drops a missing message from
// its pending set on the next pull), so "gone" is reachable only when the original disappears
// between the capture and the recorder's fetch. core/messaging's
// TestAnOriginalDeletedAfterCaptureReachesTheRecordAsGone pins that the handler hands the func
// a nil Original then.
func TestGoneOriginalIsCountedNotLettered(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.RaiseAlarm)
	record, err := MaxDeliveryRecorder(rig.p)(rig.nmgr)
	require.NoError(t, err)
	outcome, err := record(context.Background(), messaging.MaxDelivery{
		Suffix: streams.RaiseAlarm, Stream: messaging.StreamName("test", streams.RaiseAlarm),
		Consumer: "c", StreamSeq: 9, Deliveries: 5, At: time.Now(), Original: nil, Final: true,
	})
	require.NoError(t, err)
	require.Equal(t, messaging.MaxDeliveryGone, outcome)
	require.Empty(t, rig.letters())
	require.Equal(t, 0.0, lostCount(t, rig.reg, rig.area), "a gone original is not a lost letter")
}

// GUARD: a dead-letter READER that runs out of deliveries on a letter is never lettered (a
// letter about a letter would loop) — and it is not silent either: the letter will now age
// out unstored, which is a loss, and is counted on dead_letter_lost_total.
func TestDeadLetterReaderExhaustionIsCountedLost(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.DeadLetters)
	body, err := Marshal(Envelope{Kind: KindNotification, Reason: ReasonExhausted, Source: "x",
		Summary: "s", OccurredAt: time.Now().UTC()})
	require.NoError(t, err)
	rig.publish(streams.DeadLetters, body, nil)
	rig.deliverAll(rig.readers[0])
	rig.keepPulling(rig.readers[0])
	eventually(t, 6*time.Second, "the not-lettered count", func() bool {
		return rig.records(streams.DeadLetters, "not-lettered") == 1
	})
	require.Equal(t, 1.0, lostCount(t, rig.reg, rig.area))
	require.Len(t, rig.held(streams.DeadLetters), 1, "a letter was written about a letter")
}

// GUARD: a HOT stream's letter is a pointer — a flood of abandoned device traffic must not copy
// itself into the cold dead-letter stream.
func TestHotStreamLetterCarriesNoPayload(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.ResolvedEvents)
	seq := rig.publish(streams.ResolvedEvents, []byte(`{"measurement":21.5}`), nil)
	rig.deliverAll(rig.readers[0])
	rig.keepPulling(rig.readers[0])
	eventually(t, 6*time.Second, "the letter", func() bool { return len(rig.letters()) == 1 })
	e := rig.letters()[0]
	require.Equal(t, KindEvent, e.Kind)
	require.Equal(t, ReasonNoOutcome, e.Reason)
	require.Nil(t, e.Payload)
	require.Contains(t, e.Detail, messaging.StreamName("test", streams.ResolvedEvents)+"#1")
	require.EqualValues(t, 1, seq)
}

// GUARD: the counterweight — a COLD stream's letter carries the original, so the work can be
// understood after the stream has let it go.
func TestColdStreamLetterCarriesPayload(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.EntityDeleted)
	rig.publish(streams.EntityDeleted, []byte(`{"token":"dev-1"}`), map[string]string{messaging.HeaderCorrelationID: "c-9"})
	rig.deliverAll(rig.readers[0])
	rig.keepPulling(rig.readers[0])
	eventually(t, 6*time.Second, "the letter", func() bool { return len(rig.letters()) == 1 })
	e := rig.letters()[0]
	require.Equal(t, KindControlFact, e.Kind)
	require.Equal(t, []byte(`{"token":"dev-1"}`), e.Payload)
	require.Equal(t, "c-9", e.Correlation)
	require.Equal(t, rig.area, e.Source)
	require.EqualValues(t, messaging.MaxDeliver, e.Attempts)
	require.Empty(t, e.Detail)
}

// GUARD: connector-dispatch's contract holds for the recorder's letters too. The request goes
// VERBATIM to connector-dispatch.dead — body byte-identical, the original's headers, the
// reason header — and the letter is only its index entry: no payload. Both carry the id the
// connectors service's own arm derives, so the two writers dedupe on both streams.
func TestConnectorDispatchRecorderWritesTheVerbatimCopy(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.ConnectorDispatch)
	body := []byte(`{"ruleId":"r1","action":"httpCall","bytes":"é"}`)
	rig.publish(streams.ConnectorDispatch, body, map[string]string{
		messaging.HeaderCorrelationID: "corr-7", "X-Dc-Trace": "t-1", nats.MsgIdHdr: "producer-id",
	})
	rig.deliverAll(rig.readers[0])
	rig.keepPulling(rig.readers[0])
	eventually(t, 6*time.Second, "the letter", func() bool { return len(rig.letters()) == 1 })

	copies := rig.held(streams.ConnectorDispatchDead)
	require.Len(t, copies, 1)
	cp := copies[0]
	require.Equal(t, body, cp.Data)
	require.Equal(t, "t-1", cp.Header.Get("X-Dc-Trace"))
	require.Equal(t, "corr-7", cp.Header.Get(messaging.HeaderCorrelationID))
	require.Equal(t, DeadReasonNoOutcome, cp.Header.Get(HeaderDeadReason))
	id := "mdl." + messaging.StreamName("test", streams.ConnectorDispatch) + "." +
		messaging.DurableName("test", rig.area, streams.ConnectorDispatch) + ".1"
	require.Equal(t, id, cp.Header.Get(nats.MsgIdHdr), "the copy carries the letter's id, not the producer's")
	require.Equal(t, messaging.ScopedSubject("test", "acme", streams.ConnectorDispatchDead), cp.Subject)

	e := rig.letters()[0]
	require.Equal(t, KindConnectorDispatch, e.Kind)
	require.Nil(t, e.Payload, "the index entry must not duplicate the verbatim copy")
	require.Equal(t, "corr-7", e.Correlation)
	require.Equal(t, id, rig.held(streams.DeadLetters)[0].Header.Get(nats.MsgIdHdr))
}

// GUARD: a letter is not filed for a tenant already deleted — the purge would have to chase it.
func TestDeletedTenantIsNotLettered(t *testing.T) {
	prev := tenantLifecycleGate
	tenantLifecycleGate = func(config.UserManagementConfiguration, string, string) func(string) bool {
		return func(tenant string) bool { return tenant == "acme" }
	}
	t.Cleanup(func() { tenantLifecycleGate = prev })

	rig := newRecorderRig(t, startBroker(t), streams.RaiseAlarm)
	rig.publish(streams.RaiseAlarm, []byte(`{"k":1}`), nil)
	rig.deliverAll(rig.readers[0])
	rig.keepPulling(rig.readers[0])
	eventually(t, 6*time.Second, "the tenant-deleted count", func() bool {
		return rig.records(streams.RaiseAlarm, "tenant-deleted") == 1
	})
	require.Empty(t, rig.letters())
	require.Empty(t, rig.held(streams.MaxDeliveries), "the advisory was not acked")
}

// failingRecorder is an area's recorder built by hand around writers that refuse every write,
// so the outcome of a failed letter can be driven without a broker. Only the fields record
// reads are set: no tenant gate (nothing is deleted), a letter bound large enough for any
// payload here.
func failingRecorder(t *testing.T, area string) (*maxDeliveryRecorder, *prometheus.Registry, *fakeWriter, *fakeWriter) {
	t.Helper()
	p, reg := testProducer(t, area)
	letters := &fakeWriter{failures: 1 << 20, err: errors.New("the broker is refusing writes")}
	copies := &fakeWriter{failures: 1 << 20, err: errors.New("the broker is refusing writes")}
	return &maxDeliveryRecorder{
		producer:  p,
		sink:      &Sink{writer: letters, source: p.source, onLoss: func(error) {}},
		copies:    map[string]Writer{streams.ConnectorDispatchDead: copies},
		maxLetter: 1 << 20,
	}, reg, letters, copies
}

func abandoned(suffix string, final bool) messaging.MaxDelivery {
	return messaging.MaxDelivery{
		Suffix: suffix, Stream: messaging.StreamName("test", suffix), Consumer: "c",
		StreamSeq: 7, Deliveries: messaging.MaxDeliver, At: time.Now(), Final: final,
		Original: &nats.RawStreamMsg{Subject: messaging.ScopedSubject("test", "acme", suffix),
			Data: []byte(`{"k":1}`), Header: nats.Header{}},
	}
}

// GUARD: kills "the recorder's final write failure is never counted lost". On the capture
// durable's LAST delivery of an advisory, a letter that cannot be written is a loss: an error
// returned now would leave the advisory unacked on the work queue with nothing left to
// redeliver it, and nothing would count it. So the outcome is "lost", the producer's
// dead_letter_lost_total moves, and no error is returned (the advisory is acked). Both write
// paths are driven — the letter itself, and connector-dispatch's verbatim copy, which is
// written first.
//
// The counterweight is the same failure on an EARLIER delivery: there the error IS returned,
// so the advisory is redelivered and the write tried again, and nothing is counted lost.
func TestAFailedLetterIsLostOnlyOnTheFinalDelivery(t *testing.T) {
	for _, suffix := range []string{streams.RaiseAlarm, streams.ConnectorDispatch} {
		t.Run(suffix+"/final", func(t *testing.T) {
			area := "rec-final-" + strings.ReplaceAll(suffix, ".", "-")
			r, reg, letters, copies := failingRecorder(t, area)
			outcome, err := r.record(context.Background(), abandoned(suffix, true))
			require.NoError(t, err, "an error on the final delivery strands the advisory uncounted")
			require.Equal(t, messaging.MaxDeliveryLost, outcome)
			require.Equal(t, 1.0, lostCount(t, reg, area))
			require.Equal(t, writeAttempts, letters.calls+copies.calls, "the write was not retried before giving up")
		})
		t.Run(suffix+"/not-final", func(t *testing.T) {
			area := "rec-early-" + strings.ReplaceAll(suffix, ".", "-")
			r, reg, _, _ := failingRecorder(t, area)
			outcome, err := r.record(context.Background(), abandoned(suffix, false))
			require.Error(t, err, "a failure before the final delivery must be retried by redelivery")
			require.Empty(t, outcome)
			require.Equal(t, 0.0, lostCount(t, reg, area), "a failure that will be retried is not a loss")
		})
	}
}

// GUARD: kills "dead-letters (or connector-dispatch.dead) has no 30-minute dedup window".
// Without the declared window the broker falls back to its own two-minute default, which
// still dedups two writes seconds apart — so TestLateArmLetterDedupsAgainstTheRecorder cannot
// tell the difference. The window is what makes an arm's late letter and the recorder's land
// once when they are MINUTES apart (the recorder waits for the next pull, and an area can be
// down far longer than two minutes), so it is asserted on the streams as the broker holds them.
func TestDeadLetterStreamsCarryTheThirtyMinuteDedupWindow(t *testing.T) {
	rig := newRecorderRig(t, startBroker(t), streams.ConnectorDispatch)
	for _, suffix := range []string{streams.DeadLetters, streams.ConnectorDispatchDead} {
		info, err := rig.js.StreamInfo(messaging.StreamName("test", suffix))
		require.NoError(t, err, "the recorder's build did not create %s", suffix)
		require.Equal(t, 30*time.Minute, info.Config.Duplicates, "%s's dedup window", suffix)
	}
}

// GUARD: kills "the consumer is not part of the letter's dedup id". Two areas whose durables
// read ONE stream — resolved-events is read by device-state and event-management alike — each
// exhaust their deliveries on the SAME sequence. Those are two losses, one per area, and each
// is owed its own letter. The dedup id is the only thing between them: were it derived from
// the stream and sequence alone, the second area's letter would be dropped by the broker as
// a duplicate of the first, and that area's loss would be recorded nowhere.
func TestTwoAreasExhaustingOneSequenceEachGetALetter(t *testing.T) {
	srv := startBroker(t)
	// Both rigs exist before the publish, so both durables are delivered sequence 1.
	a := newRecorderRig(t, srv, streams.ResolvedEvents)
	b := newRecorderRig(t, srv, streams.ResolvedEvents)
	seq := a.publish(streams.ResolvedEvents, []byte(`{"measurement":21.5}`), nil)
	require.EqualValues(t, 1, seq)

	a.deliverAll(a.readers[0])
	b.deliverAll(b.readers[0])
	a.keepPulling(a.readers[0])
	b.keepPulling(b.readers[0])
	eventually(t, 10*time.Second, "one letter per area", func() bool {
		return a.records(streams.ResolvedEvents, string(messaging.MaxDeliveryLettered)) == 1 &&
			b.records(streams.ResolvedEvents, string(messaging.MaxDeliveryLettered)) == 1
	})
	// Both recorders report lettering; the broker is the one that could still have dropped a
	// write as a duplicate, so the stream it holds is the evidence.
	held := a.held(streams.DeadLetters)
	require.Len(t, held, 2, "one area's letter was swallowed by the other's")

	stream := messaging.StreamName("test", streams.ResolvedEvents)
	want := map[string]string{
		a.area: "mdl." + stream + "." + messaging.DurableName("test", a.area, streams.ResolvedEvents) + ".1",
		b.area: "mdl." + stream + "." + messaging.DurableName("test", b.area, streams.ResolvedEvents) + ".1",
	}
	got := map[string]string{}
	for _, m := range held {
		e, err := Unmarshal(m.Data)
		require.NoError(t, err)
		require.Equal(t, ReasonNoOutcome, e.Reason)
		require.Contains(t, e.Detail, stream+"#1")
		got[e.Source] = m.Header.Get(nats.MsgIdHdr)
	}
	require.Equal(t, want, got, "each area's letter must name its own durable")
}
