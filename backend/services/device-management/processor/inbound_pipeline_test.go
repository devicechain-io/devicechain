// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// The publish stage of the inbound processor, driven over a real embedded JetStream broker
// and the writers main.go builds (newGateProcessor). Where the thing under test is what the
// broker SAYS — held, refused, never answered — the stream is replaced by a stub responder on
// its subject (brokerStub), which answers each publish request only when the test tells it to.
//
// Sources are submitted through OnResolvedEvent, not through the resolver pool: the pool
// finishes events out of arrival order on purpose, and these tests need to know the order
// they submitted in.

const gateTenant = "acme"

// startGateNats runs an in-process JetStream server and a started NatsManager on it, and
// returns the manager with the server's client URL (for the stub's own connection).
func startGateNats(t *testing.T) (*messaging.NatsManager, string) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	ms := &core.Microservice{InstanceId: "gate", FunctionalArea: "device-management",
		Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: u.Hostname(), Port: uint32(port)}
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize nats manager: %v", err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		t.Fatalf("start nats manager: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr, srv.ClientURL()
}

// sliceReader hands out its messages in order and then reports the end of the stream.
type sliceReader struct {
	mu   sync.Mutex
	msgs []messaging.Message
}

func (r *sliceReader) ReadMessage(context.Context) (messaging.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.msgs) == 0 {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[0]
	r.msgs = r.msgs[1:]
	return m, nil
}

func (r *sliceReader) HandleResponse(error) {}

// ackLog records which sources were acked, in what order and when.
type ackLog struct {
	mu    sync.Mutex
	order []int
	at    map[int]time.Time
}

func newAckLog() *ackLog { return &ackLog{at: map[int]time.Time{}} }

func (l *ackLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.order)
}

func (l *ackLog) snapshot() ([]int, map[int]time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := make(map[int]time.Time, len(l.at))
	for k, v := range l.at {
		at[k] = v
	}
	return append([]int(nil), l.order...), at
}

type recAck struct {
	i   int
	log *ackLog
}

func (a recAck) Ack() error {
	a.log.mu.Lock()
	defer a.log.mu.Unlock()
	a.log.order = append(a.log.order, a.i)
	a.log.at[a.i] = time.Now()
	return nil
}

// gateSource is inbound message i as a reader would deliver it: acked into log, at inbound
// stream sequence seq.
func gateSource(nmgr *messaging.NatsManager, i int, seq uint64, log *ackLog) messaging.Message {
	src := messaging.NewConsumedMessage(
		messaging.ScopedSubject(nmgr.Microservice.InstanceId, gateTenant, streams.InboundEvents),
		nil, 1, nil, recAck{i: i, log: log})
	src.StreamSeq = seq
	return src
}

// gateEvent is a resolved event whose device token names i, so a stored copy says which
// submission it was.
func gateEvent(i int) EventResolutionResults {
	return EventResolutionResults{Resolved: &dmodel.ResolvedEvent{
		Source:            "gate",
		SourceDeviceToken: fmt.Sprintf("dev-%d", i),
		EventType:         esmodel.Measurement,
		Payload:           &dmodel.ResolvedMeasurementsPayload{},
		OccurredTime:      time.Now(),
		ProcessedTime:     time.Now(),
	}}
}

// startGateProcessor builds, initializes and starts the processor.
func startGateProcessor(t *testing.T, nmgr *messaging.NatsManager, reader messaging.MessageReader) *InboundEventsProcessor {
	t.Helper()
	if reader == nil {
		reader = &sliceReader{}
	}
	iproc := newGateProcessor(t, nmgr, reader, nil)
	if err := iproc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize processor: %v", err)
	}
	if err := iproc.Start(context.Background()); err != nil {
		t.Fatalf("start processor: %v", err)
	}
	return iproc
}

// stopWithin stops the processor and fails the test as a HANG if that takes longer than
// limit, returning how long it took.
func stopWithin(t *testing.T, iproc *InboundEventsProcessor, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		_ = iproc.Stop(context.Background())
		close(done)
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("HANG: processor Stop did not return within %v", limit)
		return 0
	}
}

// brokerStub replaces one stream with a responder that records every publish request and
// answers it only when told to — with a PubAck, or with a JetStream error.
type brokerStub struct {
	stream string

	mu         sync.Mutex
	reqs       []*nats.Msg
	answeredAt []time.Time
	auto       bool
	seq        uint64
}

// stubBroker deletes suffix's stream and subscribes the stub on the tenant's subject for it.
// With no stream there is no JetStream responder, and with the stub there is one, so a
// publish neither fails fast with "no responders" nor succeeds until the stub answers.
func stubBroker(t *testing.T, nmgr *messaging.NatsManager, clientURL, suffix string) *brokerStub {
	t.Helper()
	nc, err := nats.Connect(clientURL)
	if err != nil {
		t.Fatalf("connect stub: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("stub jetstream: %v", err)
	}
	stream := messaging.StreamName(nmgr.Microservice.InstanceId, suffix)
	if err := js.DeleteStream(stream); err != nil {
		t.Fatalf("delete stream %s: %v", stream, err)
	}
	b := &brokerStub{stream: stream}
	subject := messaging.ScopedSubject(nmgr.Microservice.InstanceId, gateTenant, suffix)
	if _, err := messaging.SubscribeSynced(nc, subject, b.handle); err != nil {
		t.Fatalf("subscribe stub: %v", err)
	}
	return b
}

func (b *brokerStub) handle(m *nats.Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqs = append(b.reqs, m)
	b.answeredAt = append(b.answeredAt, time.Time{})
	if b.auto {
		b.answerLocked(len(b.reqs)-1, false)
	}
}

// answerLocked answers request i once. The answer time is recorded BEFORE the reply is
// sent, so an ack caused by the reply can never appear to precede it.
func (b *brokerStub) answerLocked(i int, fail bool) {
	if !b.answeredAt[i].IsZero() {
		return
	}
	b.seq++
	body := fmt.Sprintf(`{"stream":%q,"seq":%d}`, b.stream, b.seq)
	if fail {
		body = `{"error":{"code":503,"err_code":10000,"description":"forced by the test"}}`
	}
	b.answeredAt[i] = time.Now()
	_ = b.reqs[i].Respond([]byte(body))
}

func (b *brokerStub) answer(i int, fail bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answerLocked(i, fail)
}

// answerEverythingFromNowOn answers every held request with a PubAck, and every later one
// as it arrives.
func (b *brokerStub) answerEverythingFromNowOn() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.auto = true
	for i := range b.reqs {
		b.answerLocked(i, false)
	}
}

func (b *brokerStub) seen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.reqs)
}

func (b *brokerStub) answeredTime(i int) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.answeredAt[i]
}

// settledCount waits until the stub's request count has not moved for 300ms (at most 3s)
// and returns it: how many publishes the processor has in flight at once.
func (b *brokerStub) settledCount() int {
	last, since := -1, time.Now()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n := b.seen()
		if n != last {
			last, since = n, time.Now()
		} else if time.Since(since) >= 300*time.Millisecond {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return b.seen()
}

// waitAcks waits up to limit for log to hold want acks and returns the count it saw.
func waitAcks(log *ackLog, want int, limit time.Duration) int {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if n := log.count(); n >= want {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
	return log.count()
}

// THE GATE. The processor keeps many resolved-event publishes awaiting their PubAck at once:
// with 32 submitted and the broker answering none, the broker holds all 32 requests — and
// not one source is acked, because none of them has been stored.
//
// A loop that waits for each PubAck before sending the next holds exactly one request here,
// however many resolvers feed it, which is what made one publish round trip the ceiling on
// resolve throughput.
func TestResolvedPublishesOverlapAtTheBroker(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	iproc := startGateProcessor(t, nmgr, nil)
	stub := stubBroker(t, nmgr, clientURL, streams.ResolvedEvents)
	acks := newAckLog()
	t.Cleanup(func() {
		stub.answerEverythingFromNowOn()
		stopWithin(t, iproc, 60*time.Second)
	})

	const submitted = 32
	for i := 0; i < submitted; i++ {
		iproc.OnResolvedEvent(gateSource(nmgr, i, uint64(1000+i), acks), gateTenant, []EventResolutionResults{gateEvent(i)})
	}

	inFlight := stub.settledCount()
	acked := acks.count()
	if inFlight != submitted {
		t.Errorf("publish requests held by the broker at once: got %d, want %d", inFlight, submitted)
	}
	if acked != 0 {
		t.Errorf("sources acked while no publish has been acknowledged: got %d, want 0", acked)
	}
}

// A source is acked only once its event is stored, and the acks come in SUBMISSION order —
// not in the order the broker answers — with a refused publish leaving its own source
// unacked and nobody else's.
func TestNoSourceIsAckedBeforeItsResolvedEventIsStored(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	iproc := startGateProcessor(t, nmgr, nil)
	stub := stubBroker(t, nmgr, clientURL, streams.ResolvedEvents)
	acks := newAckLog()
	t.Cleanup(func() {
		stub.answerEverythingFromNowOn()
		stopWithin(t, iproc, 60*time.Second)
	})

	const submitted, refused = 10, 5
	for i := 0; i < submitted; i++ {
		iproc.OnResolvedEvent(gateSource(nmgr, i, uint64(2000+i), acks), gateTenant, []EventResolutionResults{gateEvent(i)})
	}
	if got := stub.settledCount(); got != submitted {
		t.Fatalf("publish requests held: got %d, want %d (the rest of this test needs every one in hand)", got, submitted)
	}
	if got := acks.count(); got != 0 {
		t.Fatalf("sources acked with every publish still held: got %d, want 0", got)
	}

	for i := submitted - 1; i >= 0; i-- {
		stub.answer(i, i == refused)
	}
	waitAcks(acks, submitted-1, 5*time.Second)
	// One more beat, so an ack of the refused source — the defect — has time to show.
	time.Sleep(200 * time.Millisecond)

	order, at := acks.snapshot()
	want := []int{0, 1, 2, 3, 4, 6, 7, 8, 9}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("acked sources: got %v, want %v (submission order, without the refused one)", order, want)
	}
	for _, i := range order {
		if at[i].Before(stub.answeredTime(i)) {
			t.Errorf("source %d was acked at %v, before the broker answered its publish at %v",
				i, at[i], stub.answeredTime(i))
		}
	}
}

// What reaches the stream is what was submitted, in the order it was submitted.
func TestResolvedEventsReachTheStreamInSubmissionOrder(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	iproc := startGateProcessor(t, nmgr, nil)
	acks := newAckLog()

	const submitted = 2000
	for i := 0; i < submitted; i++ {
		iproc.OnResolvedEvent(gateSource(nmgr, i, uint64(3000+i), acks), gateTenant, []EventResolutionResults{gateEvent(i)})
	}
	if got := waitAcks(acks, submitted, 30*time.Second); got != submitted {
		t.Fatalf("sources acked: got %d, want %d", got, submitted)
	}
	stopWithin(t, iproc, 30*time.Second)

	nc, err := nats.Connect(clientURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	stream := messaging.StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
	for seq := uint64(1); seq <= submitted; seq++ {
		raw, err := js.GetMsg(stream, seq)
		if err != nil {
			t.Fatalf("stream sequence %d: %v", seq, err)
		}
		ev, err := proto.UnmarshalResolvedEvent(raw.Data)
		if err != nil {
			t.Fatalf("stream sequence %d: %v", seq, err)
		}
		if want := fmt.Sprintf("dev-%d", seq-1); ev.SourceDeviceToken != want {
			t.Fatalf("stream sequence %d holds %s, want %s: the stream's order is not the submission order",
				seq, ev.SourceDeviceToken, want)
		}
	}
}

// Stopping the processor waits for every publish already handed to the writer: the broker
// is holding all of them, Stop does not return, and once they are answered every source
// is acked before it does.
func TestStopSettlesEveryInFlightPublishBeforeReturning(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	iproc := startGateProcessor(t, nmgr, nil)
	stub := stubBroker(t, nmgr, clientURL, streams.ResolvedEvents)
	acks := newAckLog()

	const submitted = 20
	for i := 0; i < submitted; i++ {
		iproc.OnResolvedEvent(gateSource(nmgr, i, uint64(4000+i), acks), gateTenant, []EventResolutionResults{gateEvent(i)})
	}
	stub.settledCount()

	stopped := make(chan struct{})
	go func() {
		_ = iproc.Stop(context.Background())
		close(stopped)
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case <-stopped:
		t.Fatalf("Stop returned with every publish still held; acked %d of %d", acks.count(), submitted)
	default:
	}
	if got := acks.count(); got != 0 {
		t.Fatalf("sources acked with every publish still held: got %d, want 0", got)
	}

	stub.answerEverythingFromNowOn()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("HANG: Stop did not return after every publish was answered")
	}
	if got := acks.count(); got != submitted {
		t.Errorf("sources acked by the time Stop returned: got %d, want %d", got, submitted)
	}
}

// A broker that never answers cannot hold Stop for longer than the publish ceiling, and
// nothing is acked on the way out: every source stays for redelivery.
func TestStopAgainstASilentBrokerIsBoundedByTheCeilingAndAcksNothing(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	iproc := startGateProcessor(t, nmgr, nil)
	stub := stubBroker(t, nmgr, clientURL, streams.ResolvedEvents)
	acks := newAckLog()

	const submitted = 20
	for i := 0; i < submitted; i++ {
		iproc.OnResolvedEvent(gateSource(nmgr, i, uint64(5000+i), acks), gateTenant, []EventResolutionResults{gateEvent(i)})
	}
	if got := stub.settledCount(); got != submitted {
		t.Fatalf("publish requests held: got %d, want %d", got, submitted)
	}

	// The ruled ceiling (5 s) plus slack, as a literal: a drift in the constant should fail
	// this, not move with it.
	const bound = 5*time.Second + 2*time.Second
	took := stopWithin(t, iproc, 3*bound)
	if took > bound {
		t.Errorf("Stop against a silent broker took %v, want at most %v", took, bound)
	}
	if got := acks.count(); got != 0 {
		t.Errorf("sources acked although no publish was ever acknowledged: got %d, want 0", got)
	}
}

// A source that is redelivered — its first publish stored, but the PubAck lost — publishes
// the same event again, and the broker stores it ONCE. The id is the source's own inbound
// stream sequence and the event's place in its fan-out, both of which a redelivery keeps.
func TestARedeliveredSourceIsStoredOnce(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	iproc := startGateProcessor(t, nmgr, nil)
	acks := newAckLog()

	nc, err := nats.Connect(clientURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	stream := messaging.StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
	stored := func() uint64 {
		t.Helper()
		info, err := js.StreamInfo(stream)
		if err != nil {
			t.Fatal(err)
		}
		return info.State.Msgs
	}
	// The premise: resolved-events declares no window of its own, so the broker's default
	// applies, and it outlasts the first redelivery (one AckWait, 60 s, after the fetch).
	info, err := js.StreamInfo(stream)
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.Duplicates != 2*time.Minute {
		t.Fatalf("resolved-events duplicate window is %v, want the broker default 2m", info.Config.Duplicates)
	}

	submit := func(i int, seq uint64, events ...EventResolutionResults) {
		iproc.OnResolvedEvent(gateSource(nmgr, i, seq, acks), gateTenant, events)
	}
	// The first delivery and its redelivery: same inbound sequence, same single event.
	submit(0, 77, gateEvent(0))
	submit(1, 77, gateEvent(0))
	if got := waitAcks(acks, 2, 10*time.Second); got != 2 {
		t.Fatalf("both deliveries must be acked (the second by the broker's duplicate PubAck): got %d", got)
	}
	if got := stored(); got != 1 {
		t.Errorf("messages stored for one source published twice: got %d, want 1", got)
	}

	// The counterweights: different sources, different fan-out positions, and sources with
	// no stream sequence are all stored — dedup must not swallow distinct events.
	submit(2, 78, gateEvent(2))
	submit(3, 79, gateEvent(3))
	submit(4, 80, gateEvent(4), gateEvent(5))
	submit(5, 0, gateEvent(6))
	submit(6, 0, gateEvent(7))
	if got := waitAcks(acks, 7, 10*time.Second); got != 7 {
		t.Fatalf("sources acked: got %d, want 7", got)
	}
	if got := stored(); got != 1+2+2+2 {
		t.Errorf("messages stored: got %d, want 7 (two sources, a fan-out of two, two unsequenced sources)", got)
	}
	stopWithin(t, iproc, 30*time.Second)
}

// A failed-event record the broker refuses leaves the inbound message it is about UNACKED,
// so it is redelivered rather than forgotten with no record anywhere.
func TestAFailedEventRecordThatIsNotStoredLeavesItsSourceUnacked(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	acks := newAckLog()
	bad := gateSource(nmgr, 0, 6000, acks)
	bad.Value = undecodableMessage().Value
	iproc := startGateProcessor(t, nmgr, nil)
	stub := stubBroker(t, nmgr, clientURL, streams.FailedEvents)
	// Handed to the resolvers only once the stub is up, so the stub is the only thing that
	// can answer the record's publish.
	iproc.messages <- bad

	deadline := time.Now().Add(10 * time.Second)
	for stub.seen() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stub.seen() != 1 {
		t.Fatalf("failed-event publish requests: got %d, want 1", stub.seen())
	}
	stub.answer(0, true)
	stopWithin(t, iproc, 30*time.Second)

	if got := acks.count(); got != 0 {
		t.Errorf("sources acked although their failed-event record was refused: got %d, want 0", got)
	}
}

// And the other half: a stored record acks its source — only after the broker stored it.
func TestAFailedEventSourceIsAckedOnlyAfterItsRecordIsStored(t *testing.T) {
	nmgr, clientURL := startGateNats(t)
	acks := newAckLog()
	bad := gateSource(nmgr, 0, 6100, acks)
	bad.Value = undecodableMessage().Value
	iproc := startGateProcessor(t, nmgr, nil)
	stub := stubBroker(t, nmgr, clientURL, streams.FailedEvents)
	t.Cleanup(func() {
		stub.answerEverythingFromNowOn()
		stopWithin(t, iproc, 30*time.Second)
	})
	// Delivered through the resolvers after the stub is up, so the stub is the only thing
	// that can answer.
	iproc.messages <- bad

	deadline := time.Now().Add(10 * time.Second)
	for stub.seen() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stub.seen() != 1 {
		t.Fatalf("failed-event publish requests: got %d, want 1", stub.seen())
	}
	time.Sleep(200 * time.Millisecond)
	if got := acks.count(); got != 0 {
		t.Fatalf("source acked while its failed-event record was still unacknowledged: got %d, want 0", got)
	}

	stub.answer(0, false)
	if got := waitAcks(acks, 1, 10*time.Second); got != 1 {
		t.Fatalf("source acked after its record was stored: got %d, want 1", got)
	}
	_, at := acks.snapshot()
	if at[0].Before(stub.answeredTime(0)) {
		t.Errorf("source acked at %v, before its record was acknowledged at %v", at[0], stub.answeredTime(0))
	}
}
