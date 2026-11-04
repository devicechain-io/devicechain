// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAckedWriter is an AckedWriter whose broker is a map: it stores what it is given under its
// dedup id, answers a repeated id as a duplicate naming the first, and calls done inline.
type fakeAckedWriter struct {
	mu     sync.Mutex
	msgs   []messaging.Message // everything published, in order, duplicates included
	stored map[string]uint64   // dedup id -> sequence
	bySeq  map[uint64]messaging.Message
	seq    uint64
	// fail, when set, fails every publish.
	fail error
	// legacy is what plain Publish (the flag-off shape) was given.
	legacy []messaging.Message
	// rejectOnce makes the ordered writer's publish of an id fail once, storing nothing.
	rejectOnce map[string]bool
}

func newFakeAckedWriter() *fakeAckedWriter {
	return &fakeAckedWriter{stored: map[string]uint64{}, bySeq: map[uint64]messaging.Message{}}
}

func (f *fakeAckedWriter) store(msg messaging.Message) (messaging.PubAckInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	if f.fail != nil {
		return messaging.PubAckInfo{}, f.fail
	}
	if msg.DedupID != "" {
		if seq, ok := f.stored[msg.DedupID]; ok {
			return messaging.PubAckInfo{Sequence: seq, Duplicate: true}, nil
		}
	}
	f.seq++
	if msg.DedupID != "" {
		f.stored[msg.DedupID] = f.seq
	}
	f.bySeq[f.seq] = msg
	return messaging.PubAckInfo{Sequence: f.seq}, nil
}

func (f *fakeAckedWriter) PublishAcked(_ context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	f.mu.Lock()
	if f.rejectOnce[msg.DedupID] {
		delete(f.rejectOnce, msg.DedupID)
		f.mu.Unlock()
		done(messaging.PubAckInfo{}, errors.New("rejected: no responders"))
		return
	}
	f.mu.Unlock()
	ack, err := f.store(msg)
	done(ack, err)
}

func (f *fakeAckedWriter) Publish(_ context.Context, msg messaging.Message, done func(error)) {
	f.mu.Lock()
	f.legacy = append(f.legacy, msg)
	f.mu.Unlock()
	done(nil)
}
func (f *fakeAckedWriter) Fail(err error, done func(error)) { done(err) }
func (f *fakeAckedWriter) Draining()                        {}
func (f *fakeAckedWriter) Close()                           {}

// fakeRounds is the RoundPublisher over the same fake broker.
type fakeRounds struct {
	w *fakeAckedWriter
	// getErr, when set, fails every Get.
	getErr error
	gets   atomic.Int32
}

func (r *fakeRounds) Publish(_ context.Context, msg messaging.Message) (messaging.PubAckInfo, error) {
	return r.w.store(msg)
}

func (r *fakeRounds) Get(_ context.Context, tenant string, seq uint64) (messaging.StoredMessage, error) {
	r.gets.Add(1)
	if r.getErr != nil {
		return messaging.StoredMessage{}, r.getErr
	}
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	m, ok := r.w.bySeq[seq]
	if !ok {
		return messaging.StoredMessage{}, fmt.Errorf("no message at %d", seq)
	}
	return messaging.StoredMessage{Sequence: seq, Subject: "i." + tenant + ".resolved-events", Value: m.Value, Headers: m.Headers}, nil
}

type countAck struct{ n atomic.Int32 }

func (a *countAck) Ack() error { a.n.Add(1); return nil }

// envTestSource is an inbound message at stream sequence seq, and the count of its acks.
func envTestSource(seq uint64) (messaging.Message, *countAck) {
	ack := &countAck{}
	m := messaging.NewConsumedMessage("i.acme.inbound-events", nil, 1, nil, ack)
	m.StreamSeq = seq
	return m, ack
}

// envTestItem is the fan-out-th resolved event of source, of a coordinator that expects fanout
// of them.
func envTestItem(coord *ackCoord, fan int, device string) resolvedItem {
	at := time.Unix(1700000000, int64(coord.src.StreamSeq)*1000+int64(fan)).UTC()
	return resolvedItem{
		tenant: "acme", coord: coord, index: fan, correlation: "corr-" + strconv.FormatUint(coord.src.StreamSeq, 10),
		event: resolvedevent.ResolvedEvent{
			Source: "mqtt", SourceDeviceToken: device, DeviceTypeToken: "thermostat", ProfileVersionToken: "thermostat@1",
			OccurredTime: at, ProcessedTime: at.Add(time.Millisecond), EventType: eventtype.Measurement,
			Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
				OccurredTime: at,
				Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "t", Value: "21.5"}},
			}}},
		},
	}
}

// envHarness is an envelope engine over the fake broker, with n sources at the given sequences,
// each fanning out to fanout resolved events.
type envHarness struct {
	t      *testing.T
	w      *fakeAckedWriter
	rounds *fakeRounds
	iproc  *InboundEventsProcessor
	eng    *envelopeEngine
	items  []resolvedItem
	acks   map[uint64]*countAck
}

func newEnvHarness(t *testing.T) *envHarness {
	w := newFakeAckedWriter()
	iproc := &InboundEventsProcessor{ResolvedEventsWriter: w}
	r := &fakeRounds{w: w}
	return &envHarness{t: t, w: w, rounds: r, iproc: iproc, eng: testEngine(iproc, w, r), acks: map[uint64]*countAck{}}
}

func (h *envHarness) source(seq uint64, fanout int) {
	src, ack := envTestSource(seq)
	h.acks[seq] = ack
	coord := &ackCoord{src: src, remaining: fanout}
	for f := 0; f < fanout; f++ {
		h.items = append(h.items, envTestItem(coord, f, fmt.Sprintf("dev-%d", seq)))
	}
}

func (h *envHarness) publish() {
	items := h.items
	h.items = nil
	h.eng.publishBatch(context.Background(), items)
	h.iproc.recoverWG.Wait()
}

// members returns the source indices of the stored message at seq: an envelope's members, or the
// single member of a legacy-shaped one.
func (h *envHarness) members(seq uint64) []uint32 {
	h.t.Helper()
	m := h.w.bySeq[seq]
	if m.Headers[eventenvelope.HeaderName] == "" {
		idx, err := strconv.ParseUint(m.Headers[messaging.HeaderMemberIndex], 36, 32)
		require.NoError(h.t, err)
		return []uint32{uint32(idx)}
	}
	r, err := eventenvelope.Parse(m.Value)
	require.NoError(h.t, err)
	out := make([]uint32, r.Len())
	for i := range out {
		out[i] = r.Index(i)
	}
	return out
}

// The events of one tenant from one bucket are one envelope, the members in source order however
// they arrived, under the id of (tenant, bucket, round 0). Every source is acked once, after.
func TestEventsOfOneBucketBecomeOneEnvelopeInSourceOrder(t *testing.T) {
	h := newEnvHarness(t)
	for _, seq := range []uint64{515, 513, 514, 520} { // all in bucket 2 (512..767)
		h.source(seq, 1)
	}
	h.publish()

	require.Len(t, h.w.msgs, 1, "one message for the four events")
	msg := h.w.msgs[0]
	assert.Equal(t, messaging.EnvelopeDedupID("G", "acme", bucketKey{bucket: 2}.source(), 0), msg.DedupID)
	assert.Equal(t, eventenvelope.HeaderValue, msg.Headers[eventenvelope.HeaderName])
	assert.Nil(t, msg.Key, "an envelope has no single device to key on")
	assert.Equal(t, "corr-513", msg.Headers[messaging.HeaderCorrelationID], "the first member's correlation id")
	r, err := eventenvelope.Parse(msg.Value)
	require.NoError(t, err)
	assert.Equal(t, eventenvelope.KindResolved, r.Header().Kind)
	assert.EqualValues(t, 512, r.Header().SourceSeq)
	assert.Equal(t, []uint32{1 * fanSlots, 2 * fanSlots, 3 * fanSlots, 8 * fanSlots},
		[]uint32{r.Index(0), r.Index(1), r.Index(2), r.Index(3)}, "member indices are (place in bucket, fan-out position), ascending")
	for i, want := range []string{"dev-513", "dev-514", "dev-515", "dev-520"} {
		b, err := r.Member(i)
		require.NoError(t, err)
		ev, err := resolvedevent.UnmarshalResolvedEvent(b)
		require.NoError(t, err)
		assert.Equal(t, want, ev.SourceDeviceToken)
	}
	for seq, a := range h.acks {
		assert.EqualValues(t, 1, a.n.Load(), "source %d acked once", seq)
	}
	assert.EqualValues(t, 4, h.eng.members.Load())
}

// A source that fans out to several events is acked once, after the last of them, even when the
// events are in different envelopes.
func TestAFanOutAcksItsSourceOnceAfterTheLastMember(t *testing.T) {
	h := newEnvHarness(t)
	h.source(10, 3)
	h.source(11, 2)
	h.publish()
	require.Len(t, h.w.msgs, 1)
	assert.Equal(t, []uint32{10 * fanSlots, 10*fanSlots + 1, 10*fanSlots + 2, 11 * fanSlots, 11*fanSlots + 1}, h.members(1))
	assert.EqualValues(t, 1, h.acks[10].n.Load())
	assert.EqualValues(t, 1, h.acks[11].n.Load())
}

// Different tenants and different buckets are different envelopes; a bucket's members past 64
// continue as its next round, and the bytes cap cuts too.
func TestGroupsAreCutPerTenantBucketAndBound(t *testing.T) {
	h := newEnvHarness(t)
	for seq := uint64(1); seq <= 70; seq++ { // 70 sources of bucket 0
		h.source(seq, 1)
	}
	h.source(300, 1) // bucket 1
	h.source(301, 1)
	other := h.items[len(h.items)-1]
	other.tenant = "globex"
	h.items = append(h.items[:len(h.items)-1], other)
	h.publish()

	ids := []string{}
	for _, m := range h.w.msgs {
		ids = append(ids, m.DedupID)
	}
	assert.Equal(t, []string{gid("acme", 0, 0), gid("acme", 0, 1), gid("acme", 1, 0), gid("globex", 1, 0)}, ids)
	assert.Equal(t, 64, len(h.members(1)), "the first round holds the lowest 64")
	assert.Equal(t, 6, len(h.members(2)))
	assert.Equal(t, 1, len(h.members(3)))
	assert.EqualValues(t, 2, h.eng.singles.Load(), "a group of one is published legacy-shaped")
	assert.Equal(t, "", h.w.msgs[2].Headers[eventenvelope.HeaderName], "a group of one is not an envelope")
	assert.Equal(t, strconv.FormatUint((300%bucketSize)*fanSlots, 36), h.w.msgs[2].Headers[messaging.HeaderMemberIndex])
	for seq, a := range h.acks {
		assert.EqualValues(t, 1, a.n.Load(), "source %d acked once", seq)
	}
}

// What cannot be grouped is published exactly as with the flag off: no stream sequence, a member
// of an inbound envelope, a copy split out of one, a fan-out position past the slots.
func TestWhatCannotBeGroupedKeepsTheLegacyShape(t *testing.T) {
	h := newEnvHarness(t)
	h.source(0, 1) // no sequence
	// A member of an inbound envelope, made the way the readers make it.
	env := inboundEnvelope(t, 50, &envCountingAck{}, 1, undecodableInstant(t, "a"), undecodableInstant(t, "b"))
	units, _, err := messaging.SplitEnvelope(env, "tenant1", func(string) {}, nil)
	require.NoError(t, err)
	member := units[1].Msg
	h.items = append(h.items, envTestItem(&ackCoord{src: member, remaining: 1}, 0, "member-sourced"))
	split := messaging.Message{StreamSeq: 9, Headers: map[string]string{messaging.HeaderRetry: messaging.RetryHeader(77, 3, 0)}, NumDelivered: 1}
	messaging.ApplyRetryHeader(&split)
	h.items = append(h.items, envTestItem(&ackCoord{src: split, remaining: 1}, 0, "split"))
	src, _ := envTestSource(5)
	h.items = append(h.items, envTestItem(&ackCoord{src: src, remaining: 1}, fanSlots, "wide-fan"))
	h.publish()

	assert.Empty(t, h.w.msgs, "nothing went down the envelope path")
	assert.Len(t, h.w.legacy, 4)
	for _, m := range h.w.legacy {
		assert.Empty(t, m.Headers[eventenvelope.HeaderName])
		assert.Empty(t, m.Headers[messaging.HeaderMemberIndex])
		assert.Regexp(t, `^(r:acme:[0-9a-z]+:[0-9a-z]+\.?[0-9a-z]*)?$`, m.DedupID)
	}
	assert.EqualValues(t, 4, h.eng.legacy.Load())
}

// A refused publish fails every member's source: none is acked, so each is redelivered.
func TestAFailedEnvelopePublishLeavesEverySourceUnacked(t *testing.T) {
	h := newEnvHarness(t)
	h.source(1, 1)
	h.source(2, 1)
	h.w.fail = errors.New("broker down")
	h.publish()
	for seq, a := range h.acks {
		assert.EqualValues(t, 0, a.n.Load(), "source %d must not be acked", seq)
	}
}

// The settlement protocol. A redelivery after a lost ack finds its id already stored: the probe
// reads what the original carried, and publishes only the rest.
func TestADuplicateRoundPublishesOnlyTheMembersTheOriginalLacked(t *testing.T) {
	// The first delivery stored sources 1 and 2 as round 0 and was killed before acking.
	first := newEnvHarness(t)
	first.source(1, 1)
	first.source(2, 1)
	first.publish()
	require.Len(t, first.w.msgs, 1)

	// The replacement receives 1, 2 and 3 again (3 was never published), and a fresh engine,
	// which starts the bucket at round 0.
	second := &envHarness{t: t, w: first.w, acks: map[uint64]*countAck{}}
	second.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	second.rounds = &fakeRounds{w: first.w}
	second.eng = testEngine(second.iproc, first.w, second.rounds)
	second.source(1, 1)
	second.source(2, 1)
	second.source(3, 1)
	second.publish()

	require.Len(t, first.w.bySeq, 2, "two stored messages: round 0, and round 1 with the one new member")
	assert.Equal(t, []uint32{1 * fanSlots, 2 * fanSlots}, first.members(1))
	assert.Equal(t, []uint32{3 * fanSlots}, first.members(2), "only the complement is stored")
	assert.EqualValues(t, 1, second.eng.duplicates.Load())
	assert.EqualValues(t, 1, second.eng.recovered.Load())
	for seq, a := range second.acks {
		assert.EqualValues(t, 1, a.n.Load(), "source %d acked once", seq)
	}
}

// A redelivery of everything that was stored publishes nothing and acks every source.
func TestARedeliveryOfAStoredEnvelopePublishesNothing(t *testing.T) {
	first := newEnvHarness(t)
	for seq := uint64(1); seq <= 5; seq++ {
		first.source(seq, 1)
	}
	first.publish()
	stored := len(first.w.bySeq)

	second := &envHarness{t: t, w: first.w, acks: map[uint64]*countAck{}}
	second.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	second.rounds = &fakeRounds{w: first.w}
	second.eng = testEngine(second.iproc, first.w, second.rounds)
	// Regrouped: only the sources the first run had not acked, in a different order.
	second.source(4, 1)
	second.source(2, 1)
	second.publish()

	assert.Len(t, first.w.bySeq, stored, "nothing new is stored")
	for seq, a := range second.acks {
		assert.EqualValues(t, 1, a.n.Load(), "source %d acked once", seq)
	}
}

// A group that was stored legacy-shaped (one event) is recognised by its member index, so a
// redelivery that regroups it into an envelope does not store it again.
func TestAMemberStoredAloneIsRecognisedWhenARedeliveryGroupsIt(t *testing.T) {
	first := newEnvHarness(t)
	first.source(1, 1)
	first.publish()
	require.Len(t, first.w.bySeq, 1)
	require.Equal(t, "", first.w.bySeq[1].Headers[eventenvelope.HeaderName], "stored alone, legacy-shaped")

	second := &envHarness{t: t, w: first.w, acks: map[uint64]*countAck{}}
	second.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	second.rounds = &fakeRounds{w: first.w}
	second.eng = testEngine(second.iproc, first.w, second.rounds)
	second.source(1, 1)
	second.source(2, 1)
	second.publish()

	require.Len(t, first.w.bySeq, 2, "source 1 is not stored again")
	assert.Equal(t, []uint32{2 * fanSlots}, first.members(2))
	for seq, a := range second.acks {
		assert.EqualValues(t, 1, a.n.Load(), "source %d acked once", seq)
	}
}

// Any failure of the probe leaves the sources unacked and publishes nothing further: an
// unreadable original is never taken for an empty one.
func TestAnUnreadableOriginalLeavesTheSourcesUnacked(t *testing.T) {
	for name, damage := range map[string]func(*envHarness){
		"get fails": func(h *envHarness) { h.rounds.getErr = errors.New("leader unavailable") },
		"stored message is a legacy event with no member index": func(h *envHarness) {
			for seq, m := range h.w.bySeq {
				delete(m.Headers, messaging.HeaderMemberIndex)
				delete(m.Headers, eventenvelope.HeaderName)
				h.w.bySeq[seq] = m
			}
		},
		"stored envelope is corrupt": func(h *envHarness) {
			for seq, m := range h.w.bySeq {
				m.Value = append([]byte(nil), m.Value...)
				m.Value[len(m.Value)/2] ^= 0xff
				h.w.bySeq[seq] = m
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			first := newEnvHarness(t)
			first.source(1, 1)
			first.source(2, 1)
			first.publish()
			damage(first)
			published := len(first.w.msgs)

			second := &envHarness{t: t, w: first.w, acks: map[uint64]*countAck{}}
			second.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
			second.rounds = first.rounds
			second.eng = testEngine(second.iproc, first.w, second.rounds)
			second.source(1, 1)
			second.source(2, 1)
			second.publish()

			assert.Len(t, first.w.msgs, published+1, "only the duplicate round-0 publish was attempted")
			for seq, a := range second.acks {
				assert.EqualValues(t, 0, a.n.Load(), "source %d must not be acked", seq)
			}
		})
	}
}

// A source redelivered to the SAME process (its ack was lost, or its recovery outlived AckWait)
// must not be published under the process's own round count: its events may sit in any earlier
// round. It walks from round 0 and publishes nothing new.
func TestARedeliveryToTheSameProcessWalksFromRoundZero(t *testing.T) {
	h := newEnvHarness(t)
	h.source(1, 1)
	h.source(2, 1)
	h.publish()
	h.source(3, 1)
	h.source(4, 1)
	h.publish() // rounds 0 and 1, both stored
	require.Len(t, h.w.bySeq, 2)

	// Sources 2 and 3 come back (delivery 2), regrouped, and with them a first-delivery 5.
	for _, seq := range []uint64{2, 3} {
		src, ack := envTestSource(seq)
		src.NumDelivered = 2
		h.acks[seq] = ack
		h.items = append(h.items, envTestItem(&ackCoord{src: src, remaining: 1}, 0, fmt.Sprintf("dev-%d", seq)))
	}
	h.publish()
	assert.Len(t, h.w.bySeq, 2, "the redelivered events were stored already, in rounds 0 and 1: nothing new")
	assert.EqualValues(t, 1, h.acks[2].n.Load())
	assert.EqualValues(t, 1, h.acks[3].n.Load())
	assert.EqualValues(t, 0, h.rounds.gets.Load(), "the rounds it walked were learned when stored: no read-back")
}

// carried: an envelope's SettledElsewhere counts as settled; an envelope of another tenant or
// kind is an error, never an empty set.
func TestCarriedReadsSettledElsewhereAndRefusesTheWrongEnvelope(t *testing.T) {
	h := newEnvHarness(t)
	item := envTestItem(&ackCoord{src: messaging.Message{StreamSeq: 1}}, 0, "d")
	ev, err := resolvedevent.MarshalResolvedEvent(&item.event)
	require.NoError(t, err)
	store := func(tenant string, kind eventenvelope.Kind) uint64 {
		body, err := eventenvelope.Encode(eventenvelope.Header{Kind: kind, Tenant: tenant, Round: 1, SettledElsewhere: []uint32{9}},
			[]eventenvelope.Event{{Index: 3, Bytes: ev}})
		require.NoError(t, err)
		h.w.seq++
		h.w.bySeq[h.w.seq] = messaging.Message{Value: body, Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}}
		return h.w.seq
	}
	got, err := h.eng.carried(context.Background(), "acme", store("acme", eventenvelope.KindResolved))
	require.NoError(t, err)
	assert.Equal(t, map[uint32]struct{}{3: {}, 9: {}}, got)
	_, err = h.eng.carried(context.Background(), "acme", store("globex", eventenvelope.KindResolved))
	assert.Error(t, err, "another tenant's envelope")
	_, err = h.eng.carried(context.Background(), "acme", store("acme", eventenvelope.KindUnresolved))
	assert.Error(t, err, "an inbound envelope")
}

// Within a process, a bucket's second publish is its next round, with no probe.
func TestTheNextPublishOfABucketIsTheNextRoundWithoutAProbe(t *testing.T) {
	h := newEnvHarness(t)
	h.source(1, 1)
	h.source(2, 1)
	h.publish()
	h.source(3, 1)
	h.source(4, 1)
	h.publish()
	assert.Equal(t, []string{gid("acme", 0, 0), gid("acme", 0, 1)}, []string{h.w.msgs[0].DedupID, h.w.msgs[1].DedupID})
	assert.EqualValues(t, 0, h.rounds.gets.Load(), "no read-back on the normal path")
	assert.EqualValues(t, 0, h.eng.duplicates.Load())
}

// Flag off, the message is today's, byte for byte: the dedup id, the key, the value and the
// headers, and nothing is routed to the envelope path. The expected values are written out, not
// computed from the code under test.
func TestFlagOffPublishesTodaysLegacyMessage(t *testing.T) {
	w := newFakeAckedWriter()
	iproc := &InboundEventsProcessor{ResolvedEventsWriter: w, resolved: make(chan resolvedItem, 4)}
	src, ack := envTestSource(123456789)
	coord := &ackCoord{src: src, remaining: 2}
	item := envTestItem(coord, 1, "dev-1")
	iproc.resolved <- item
	require.False(t, iproc.ProcessResolvedEvent(context.Background()))

	require.Empty(t, w.msgs, "the flag-off path never takes the acked/envelope route")
	require.Len(t, w.legacy, 1)
	m := w.legacy[0]
	want, err := resolvedevent.MarshalResolvedEvent(&item.event)
	require.NoError(t, err)
	assert.Equal(t, want, m.Value)
	assert.Equal(t, []byte("dev-1"), m.Key)
	assert.Equal(t, "r:acme:21i3v9:1", m.DedupID)
	assert.Equal(t, map[string]string{messaging.HeaderCorrelationID: "corr-123456789"}, m.Headers,
		"no envelope marker, no member index: the headers are the correlation id alone")
	assert.EqualValues(t, 0, ack.n.Load(), "the other fan-out member is still outstanding")
}

// A processor told to publish envelopes over a writer that cannot report a PubAck refuses to
// start: it will not publish what it cannot settle.
func TestStartRefusesEnvelopesItCannotSettle(t *testing.T) {
	for name, tc := range map[string]struct {
		writer messaging.OrderedWriter
		rounds messaging.RoundPublisher
	}{
		"a writer with no PubAck": {&plainOrdered{}, &fakeRounds{}},
		"no round publisher":      {newFakeAckedWriter(), nil},
	} {
		t.Run(name, func(t *testing.T) {
			iproc := readersProcessor(t, []messaging.MessageReader{&sliceReader{}})
			iproc.ResolvedEventsWriter = tc.writer
			WithResolvedEnvelopes(true, tc.rounds)(iproc)
			err := iproc.Start(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "envelopes.resolved")
		})
	}
}

// plainOrdered is an OrderedWriter that is not an AckedWriter.
type plainOrdered struct{}

func (plainOrdered) Publish(_ context.Context, _ messaging.Message, done func(error)) { done(nil) }
func (plainOrdered) Fail(err error, done func(error))                                 { done(err) }
func (plainOrdered) Draining()                                                        {}
func (plainOrdered) Close()                                                           {}

// testEngine is an engine whose rejected rounds are given up on quickly.
func testEngine(iproc *InboundEventsProcessor, w messaging.AckedWriter, r messaging.RoundPublisher) *envelopeEngine {
	e := newEnvelopeEngine(context.Background(), iproc, w, r)
	e.giveUp = 30 * time.Millisecond
	return e
}

// gid is the dedup id of round of the tenant's sequence bucket (the test sources carry no append time).
func gid(tenant string, bucket uint64, round uint32) string {
	return messaging.EnvelopeDedupID(messaging.EnvelopeDedupKind, tenant, bucketKey{bucket: bucket}.source(), round)
}
