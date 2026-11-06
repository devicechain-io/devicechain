// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
)

// fakeBroker is a stream that is a map: it stores a message under its dedup id and answers a
// repeated id as a duplicate naming the first.
type fakeBroker struct {
	mu         sync.Mutex
	msgs       []Message
	stored     map[string]uint64
	bySeq      map[uint64]Message
	seq        uint64
	rejectOnce map[string]bool
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{stored: map[string]uint64{}, bySeq: map[uint64]Message{}, rejectOnce: map[string]bool{}}
}

func (f *fakeBroker) store(msg Message) (PubAckInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	if seq, ok := f.stored[msg.DedupID]; ok && msg.DedupID != "" {
		return PubAckInfo{Sequence: seq, Duplicate: true}, nil
	}
	f.seq++
	if msg.DedupID != "" {
		f.stored[msg.DedupID] = f.seq
	}
	f.bySeq[f.seq] = msg
	return PubAckInfo{Sequence: f.seq}, nil
}

func (f *fakeBroker) reject(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectOnce[id] {
		delete(f.rejectOnce, id)
		return true
	}
	return false
}

type fakeWriter struct{ b *fakeBroker }

func (w fakeWriter) Publish(_ context.Context, msg Message, done func(error)) {
	_, err := w.b.store(msg)
	done(err)
}
func (w fakeWriter) Fail(err error, done func(error)) { done(err) }
func (w fakeWriter) Draining()                        {}
func (w fakeWriter) Close()                           {}
func (w fakeWriter) PublishAcked(_ context.Context, msg Message, done func(PubAckInfo, error)) {
	if w.b.reject(msg.DedupID) {
		done(PubAckInfo{}, errors.New("rejected: no responders"))
		return
	}
	ack, err := w.b.store(msg)
	done(ack, err)
}

type fakeRoundPub struct{ b *fakeBroker }

func (r fakeRoundPub) Publish(_ context.Context, msg Message) (PubAckInfo, error) {
	return r.b.store(msg)
}
func (r fakeRoundPub) Get(_ context.Context, tenant string, seq uint64) (StoredMessage, error) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	m, ok := r.b.bySeq[seq]
	if !ok {
		return StoredMessage{}, fmt.Errorf("no message at %d: %w", seq, nats.ErrMsgNotFound)
	}
	return StoredMessage{Sequence: seq, Subject: "i." + tenant + ".inbound-events", Value: m.Value, Headers: m.Headers}, nil
}

// pb is a well-formed PUnresolvedEvent-shaped message: field 1 = a string.
func pb(s string) []byte { return append([]byte{0x0a, byte(len(s))}, s...) }

func newTestEngine(t *testing.T, b *fakeBroker) *RoundEngine {
	t.Helper()
	e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C", Writer: fakeWriter{b}, Rounds: fakeRoundPub{b}})
	if err != nil {
		t.Fatal(err)
	}
	e.SetGiveUp(2 * time.Second)
	return e
}

type roundOutcomes struct {
	mu   sync.Mutex
	n    map[uint32]int
	errs map[uint32]error
}

func newRoundOutcomes() *roundOutcomes {
	return &roundOutcomes{n: map[uint32]int{}, errs: map[uint32]error{}}
}

func (o *roundOutcomes) member(idx uint32, redelivered bool) RoundMember {
	return RoundMember{Index: idx, Bytes: pb(fmt.Sprintf("dev-%d", idx)), Redelivered: redelivered,
		Settle: func(err error) { o.mu.Lock(); o.n[idx]++; o.errs[idx] = err; o.mu.Unlock() }}
}

func storedIndexes(t *testing.T, b *fakeBroker, seq uint64) []uint32 {
	t.Helper()
	m := b.bySeq[seq]
	if m.Headers[eventenvelope.HeaderName] == "" {
		return []uint32{1000 + uint32(len(m.Headers[HeaderMemberIndex]))} // a single: see singles below
	}
	r, err := eventenvelope.Parse(m.Value)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uint32, r.Len())
	for i := range out {
		out[i] = r.Index(i)
	}
	return out
}

// Members of one bucket are one envelope in source order under the id of (tenant, bucket,
// round 0); each source is settled once, after the PubAck.
func TestRoundEnginePublishesABucketAsOneEnvelopeInSourceOrder(t *testing.T) {
	b := newFakeBroker()
	e := newTestEngine(t, b)
	o := newRoundOutcomes()
	k, _, _ := RoundKeyOf("acme", 515, time.Time{})
	e.Publish(context.Background(), k, []RoundMember{o.member(3, false), o.member(1, false), o.member(2, false)})
	e.Wait()

	if len(b.msgs) != 1 {
		t.Fatalf("%d messages, want 1", len(b.msgs))
	}
	if b.msgs[0].DedupID != EnvelopeDedupID("C", "acme", k.Source(), 0) {
		t.Fatalf("id %q", b.msgs[0].DedupID)
	}
	got := storedIndexes(t, b, 1)
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("members %v, want source order [1 2 3]", got)
	}
	for i := uint32(1); i <= 3; i++ {
		if o.n[i] != 1 || o.errs[i] != nil {
			t.Fatalf("member %d settled %d times, err %v", i, o.n[i], o.errs[i])
		}
	}
}

// The group id is the SAME for a retry after a crash: a fresh engine (a restarted pod) given the
// redelivered sources stores nothing new and settles every one.
func TestRoundEngineRetryAfterCrashReusesTheIdAndStoresNothingNew(t *testing.T) {
	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 515, time.Unix(1_700_000_000, 0))
	first := newTestEngine(t, b)
	first.Publish(context.Background(), k, []RoundMember{newRoundOutcomes().member(1, false), newRoundOutcomes().member(2, false)})
	first.Wait()
	if len(b.bySeq) != 1 {
		t.Fatal("setup: first publish not stored")
	}

	second := newTestEngine(t, b) // a restarted pod: no memory of the first
	o := newRoundOutcomes()
	second.Publish(context.Background(), k, []RoundMember{o.member(1, true), o.member(2, true)})
	second.Wait()
	if len(b.bySeq) != 1 {
		t.Fatalf("a redelivery stored %d messages, want the original only", len(b.bySeq))
	}
	if o.n[1] != 1 || o.n[2] != 1 || o.errs[1] != nil || o.errs[2] != nil {
		t.Fatalf("redelivered sources not settled once: %v %v", o.n, o.errs)
	}
	if second.Duplicates.Load() != 1 {
		t.Fatalf("duplicates %d, want 1", second.Duplicates.Load())
	}
}

// A redelivery that REGROUPS (one of the original members, plus a new one) publishes exactly the
// complement as the next round, never the member already stored.
func TestRoundEngineRegroupedRedeliveryPublishesOnlyTheComplement(t *testing.T) {
	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 515, time.Time{})
	newTestEngine(t, b).Publish(context.Background(), k, []RoundMember{newRoundOutcomes().member(1, false), newRoundOutcomes().member(2, false)})

	e := newTestEngine(t, b)
	o := newRoundOutcomes()
	e.Publish(context.Background(), k, []RoundMember{o.member(2, true), o.member(5, true), o.member(6, true)})
	e.Wait()
	if len(b.bySeq) != 2 {
		t.Fatalf("stored %d messages, want 2 (round 0 and the complement)", len(b.bySeq))
	}
	if got := fmt.Sprint(storedIndexes(t, b, 2)); got != "[5 6]" {
		t.Fatalf("round 1 holds %s, want [5 6]", got)
	}
	if b.msgs[len(b.msgs)-1].DedupID != EnvelopeDedupID("C", "acme", k.Source(), 1) {
		t.Fatalf("complement id %q", b.msgs[len(b.msgs)-1].DedupID)
	}
	for _, i := range []uint32{2, 5, 6} {
		if o.n[i] != 1 || o.errs[i] != nil {
			t.Fatalf("member %d: settled %d err %v", i, o.n[i], o.errs[i])
		}
	}
}

// A round the broker rejects is retried under the SAME id until stored; nothing is lost and the
// members settle once.
func TestRoundEngineRejectedRoundIsRetriedUnderTheSameId(t *testing.T) {
	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 3, time.Time{})
	b.rejectOnce[EnvelopeDedupID("C", "acme", k.Source(), 0)] = true
	e := newTestEngine(t, b)
	o := newRoundOutcomes()
	e.Publish(context.Background(), k, []RoundMember{o.member(3, false), o.member(4, false)})
	e.Wait()
	if len(b.bySeq) != 1 || e.Retries.Load() != 1 {
		t.Fatalf("stored %d, retries %d; want the one round stored by its retry", len(b.bySeq), e.Retries.Load())
	}
	if o.n[3] != 1 || o.n[4] != 1 || o.errs[3] != nil {
		t.Fatalf("roundOutcomes %v %v", o.n, o.errs)
	}
}

// A member the encoder refuses (not a protobuf) splits the chunk down to single members,
// published legacy-shaped UNDER THE GROUP ID with the member index; never a per-event id.
func TestRoundEngineEncoderRefusalSplitsToSinglesUnderGroupIds(t *testing.T) {
	b := newFakeBroker()
	e := newTestEngine(t, b)
	k, _, _ := RoundKeyOf("acme", 3, time.Time{})
	o := newRoundOutcomes()
	bad := o.member(4, false)
	bad.Bytes = []byte{0xff, 0xff}
	e.Publish(context.Background(), k, []RoundMember{o.member(3, false), bad})
	e.Wait()
	if len(b.bySeq) != 2 {
		t.Fatalf("stored %d, want 2 singles", len(b.bySeq))
	}
	for _, m := range b.msgs {
		if m.Headers[HeaderMemberIndex] == "" || m.Headers[eventenvelope.HeaderName] != "" {
			t.Fatalf("not a legacy-shaped single: %+v", m.Headers)
		}
		if len(m.DedupID) < 2 || m.DedupID[:2] != "C:" {
			t.Fatalf("single published under %q, want a group id", m.DedupID)
		}
	}
	if o.n[3] != 1 || o.n[4] != 1 {
		t.Fatalf("roundOutcomes %v", o.n)
	}
}

// A source and its redelivery queued together are one member that settles both.
func TestRoundEngineFoldsASourceAndItsRedeliveryQueuedTogether(t *testing.T) {
	b := newFakeBroker()
	e := newTestEngine(t, b)
	k, _, _ := RoundKeyOf("acme", 3, time.Time{})
	var settled atomic.Int32
	mk := func() RoundMember {
		return RoundMember{Index: 3, Bytes: pb("d"), Settle: func(error) { settled.Add(1) }}
	}
	e.Publish(context.Background(), k, []RoundMember{mk(), mk()})
	e.Wait()
	if len(b.bySeq) != 1 || settled.Load() != 2 {
		t.Fatalf("stored %d, settled %d; want one message, both sources settled", len(b.bySeq), settled.Load())
	}
}

// Group ids can never be a previous build's per-event id: the capture kind letter is upper case
// and the id has four ':' separated fields, where a per-event id is tenant.<b36> and an
// integration id is tenant:<hex>.
func TestRoundEngineIdsAreDisjointFromPerEventIds(t *testing.T) {
	id := EnvelopeDedupID("C", "acme", 515, 0)
	for _, other := range []string{"acme.ek", "acme.m1.ek", "acme:" + fmt.Sprintf("%064x", 1), "x:acme:ek:0", "r:acme:ek:0", "f:acme:ek:0", "G:acme:ek:0", "H:acme:ek:0"} {
		if id == other {
			t.Fatalf("%q collides with %q", id, other)
		}
	}
	if _, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "c", Writer: fakeWriter{newFakeBroker()}, Rounds: fakeRoundPub{newFakeBroker()}}); err == nil {
		t.Fatal("a lower-case kind letter was accepted")
	}
}

// A bucket is bounded in time as well as sequence.
func TestRoundKeyIsBoundedInTimeAsWellAsSequence(t *testing.T) {
	t0 := time.Unix(1_700_000_010, 0)
	a, _, _ := RoundKeyOf("acme", 10, t0)
	if b, _, _ := RoundKeyOf("acme", 10, t0); a != b {
		t.Fatal("a redelivery is in another bucket")
	}
	if b, _, _ := RoundKeyOf("acme", 11, t0.Add(RoundBucketSeconds*time.Second)); a == b {
		t.Fatal("a source a bucket-span later shares the bucket")
	}
	if _, _, ok := RoundKeyOf("acme", 0, t0); ok {
		t.Fatal("a message with no sequence was given a key")
	}
}

// evict drops the stored message at seq while the broker still remembers its dedup id: the state
// of a stream whose retention discarded a message inside the duplicate window.
func (f *fakeBroker) evict(seq uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bySeq, seq)
}

// A round whose message retention has evicted, while its id is still inside the duplicate window,
// answers a new member's publish as a duplicate of a message that can no longer be read. That is
// terminal for THAT round (nothing can ever say what it held), not a transient fault: the walk
// steps to the next round, the member is stored, its source settles, and the eviction is counted.
func TestRoundEngineAnEvictedRoundIsSteppedPastNotRetried(t *testing.T) {
	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 515, time.Time{})
	newTestEngine(t, b).Publish(context.Background(), k, []RoundMember{newRoundOutcomes().member(1, false)})
	b.evict(1)

	e := newTestEngine(t, b) // another pod: it has never seen round 0
	o := newRoundOutcomes()
	e.Publish(context.Background(), k, []RoundMember{o.member(7, false)})
	e.Wait()
	if o.n[7] != 1 || o.errs[7] != nil {
		t.Fatalf("a member behind an evicted round must be stored by the next round: settled %d err %v", o.n[7], o.errs[7])
	}
	if m, ok := b.bySeq[2]; !ok || m.DedupID != EnvelopeDedupID("C", "acme", k.Source(), 1) {
		t.Fatalf("the member was not stored as round 1: %+v", m)
	}
	if e.Evicted.Load() != 1 {
		t.Fatalf("evicted rounds %d, want 1", e.Evicted.Load())
	}
}

// Only an eviction is terminal for a round. Any other read-back failure still leaves the source
// unacknowledged.
func TestRoundEngineAFailedReadBackIsStillTransient(t *testing.T) {
	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 515, time.Time{})
	newTestEngine(t, b).Publish(context.Background(), k, []RoundMember{newRoundOutcomes().member(1, false)})
	e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C",
		Writer: fakeWriter{b}, Rounds: brokenRounds{fakeRoundPub{b}}})
	if err != nil {
		t.Fatal(err)
	}
	o := newRoundOutcomes()
	e.Publish(context.Background(), k, []RoundMember{o.member(7, false)})
	e.Wait()
	if o.n[7] != 1 || o.errs[7] == nil {
		t.Fatalf("a timed-out read-back must fail the source: settled %d err %v", o.n[7], o.errs[7])
	}
	if e.Evicted.Load() != 0 {
		t.Fatal("a timeout was counted as an eviction")
	}
}

type brokenRounds struct{ fakeRoundPub }

func (brokenRounds) Get(context.Context, string, uint64) (StoredMessage, error) {
	return StoredMessage{}, errors.New("nats: timeout")
}

// "Not found" also covers a message tenant erasure purged. For a tenant that is deleted or being
// erased the walk must NOT step to the next round (that would store the erased tenant's member
// again): the member is left unsettled with ErrTenantGone and nothing is published.
func TestRoundEngineAnErasedTenantsMissingRoundIsNotSteppedPast(t *testing.T) {
	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 515, time.Time{})
	newTestEngine(t, b).Publish(context.Background(), k, []RoundMember{newRoundOutcomes().member(1, false)})
	b.evict(1)

	var asked atomic.Int32
	e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C",
		Writer: fakeWriter{b}, Rounds: fakeRoundPub{b},
		TenantGone: func(tenant string) bool { asked.Add(1); return tenant == "acme" }})
	if err != nil {
		t.Fatal(err)
	}
	o := newRoundOutcomes()
	e.Publish(context.Background(), k, []RoundMember{o.member(7, false)})
	e.Wait()
	if o.n[7] != 1 || !errors.Is(o.errs[7], ErrTenantGone) {
		t.Fatalf("a member of an erased tenant must be left unsettled with ErrTenantGone: settled %d err %v", o.n[7], o.errs[7])
	}
	if _, stored := b.bySeq[2]; stored {
		t.Fatal("the erased tenant's member was stored again")
	}
	if asked.Load() != 1 || e.Evicted.Load() != 0 {
		t.Fatalf("asked %d, evicted %d", asked.Load(), e.Evicted.Load())
	}

	// A tenant that is not gone is stepped past as before.
	e2, _ := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C",
		Writer: fakeWriter{b}, Rounds: fakeRoundPub{b}, TenantGone: func(string) bool { return false }})
	o2 := newRoundOutcomes()
	e2.Publish(context.Background(), k, []RoundMember{o2.member(8, false)})
	e2.Wait()
	if o2.errs[8] != nil || e2.Evicted.Load() != 1 {
		t.Fatalf("a live tenant's evicted round must be stepped past: err %v evicted %d", o2.errs[8], e2.Evicted.Load())
	}
}
