// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/rs/zerolog/log"
)

// Resolved envelopes (infrastructure.messaging.envelopes.resolved): k resolved events of one
// tenant in one message on resolved-events, in place of k messages.
//
// This is the INTERIM grouping of the design (slice E3, before event-sources publishes inbound
// envelopes): device-management groups the events it resolved from LEGACY inbound messages. The
// flag-off path is the code in inbound.go, untouched; nothing here runs unless the flag is on.
//
// # Grouping, and the id a regrouping cannot break
//
// The events of one tenant resolved from one BUCKET of the inbound stream (a fixed range of
// bucketSize sequences) form an envelope, the members in source order. The envelope's
// Nats-Msg-Id names (tenant, bucket, round), never the members:
//
//	G:<tenant>:<bucket b36>:<round b36>
//
// The design keyed it on the FIRST inbound sequence of the group. That id changes when a
// redelivery regroups (a source acked before the crash is missing, or the pool drains the same
// sources in different runs), and the regrouped envelope is then stored a second time. A bucket
// does not move: the same source is always in the same bucket however it is grouped, so a
// redelivery lands on the id its predecessor used and the broker says so.
//
// # Settlement rounds
//
// Round 0 of a bucket is published first. If the broker answers it as a DUPLICATE, the PubAck
// names the sequence of the message that holds the id. That message is read back, the members it
// carries are the members already published, and only the rest are published as round 1, and so
// on. So a redelivery after a lost ack publishes nothing, and one after a partial publish
// publishes exactly the complement: exactly once per event, whatever grouping the redelivery
// takes. Two replicas that hold different sources of one bucket reach the same ids with
// different members; the same protocol makes the later one take the next round.
//
// The common path (a first delivery) costs no more than a publish: the round is the process's
// own count for the bucket, and the probe runs only for an id the broker already holds.
//
// # What is not grouped
//
// An event with no source sequence, a member of an inbound envelope or a copy split out of one
// (those are named by their own coordinate and have per-event ids that are already exact), an
// event past the fan-out slots a member index can name, an event larger than an envelope can
// hold, or one the format refuses, is published in the legacy shape exactly as with the flag
// off. A group that comes out as ONE event is published in the legacy shape too (an envelope of
// one is never encoded), under the same envelope id, with HeaderMemberIndex naming which member
// it is, so a duplicate probe can read it.
const (
	// bucketSize is how many inbound stream sequences one bucket spans. 256 is the inbound
	// fetch size, so a fetch lands in one or two buckets.
	bucketSize = 256
	// fanSlots is how many resolved events one source can have in a member index. A source
	// fanning out to more publishes its extra events in the legacy shape.
	fanSlots = 64
	// collectLinger bounds how long the envelope submitter waits for more events to group after
	// the first. Device-management cannot see a fetch's boundary past the reader, and without a
	// wait the submitter, which is much faster than the resolvers, sees one event at a time.
	// Only the flag-on path waits; the end-to-end design has no linger on this hop once event-
	// sources publishes envelopes (the inbound envelope is the batch).
	collectLinger = 2 * time.Millisecond
	// collectMax bounds the events one collection holds.
	collectMax = 256
	// maxRounds bounds the rounds one bucket can need after a crash: a bucket's members cannot
	// exceed bucketSize*fanSlots, and every round carries at least one. Reaching it is an error
	// (the sources are left unacked), never a loop.
	maxRounds = 1024
	// roundStateMax bounds the per-bucket round counters; past it the map is dropped, which
	// costs the next publish of a bucket one probe and nothing else.
	roundStateMax = 8192
	// recoveryWorkers bounds the duplicate recoveries in flight at once.
	recoveryWorkers = 16
)

// envelopeEngine publishes grouped resolved events. Its submit side (publishBatch) is touched by
// ONE goroutine, the processor's resolved loop; recovery runs on goroutines of its own.
type envelopeEngine struct {
	iproc  *InboundEventsProcessor
	writer messaging.AckedWriter
	rounds messaging.RoundPublisher
	sem    chan struct{}

	// next is the round the next publish of a bucket uses. Submitter-only.
	next map[bucketKey]uint32

	// Counters, for tests and logs.
	envelopes  atomic.Int64 // messages published in the envelope shape
	members    atomic.Int64 // events inside them
	singles    atomic.Int64 // groups of one, published legacy-shaped under the envelope id
	legacy     atomic.Int64 // events published exactly as with the flag off
	duplicates atomic.Int64 // PubAcks answered as duplicates
	recovered  atomic.Int64 // members published by a later round
}

type bucketKey struct {
	tenant string
	bucket uint64
}

func newEnvelopeEngine(iproc *InboundEventsProcessor, w messaging.AckedWriter, rp messaging.RoundPublisher) *envelopeEngine {
	return &envelopeEngine{iproc: iproc, writer: w, rounds: rp, sem: make(chan struct{}, recoveryWorkers),
		next: make(map[bucketKey]uint32)}
}

// member is one resolved event ready to publish: its item, its marshalled bytes and its place
// in its bucket.
type member struct {
	item  resolvedItem
	bytes []byte
	index uint32
}

// memberIndex names a resolved event within its bucket: (source sequence's place in the bucket,
// fan-out position). ok is false for an event that cannot be grouped.
func memberIndex(item resolvedItem, size int) (bucket uint64, index uint32, ok bool) {
	if item.coord == nil || item.index < 0 || item.index >= fanSlots || size > eventenvelope.MaxRawBytes {
		return 0, 0, false
	}
	src := item.coord.src
	if src.StreamSeq == 0 {
		return 0, 0, false
	}
	if _, _, named := src.SourceCoordinate(); named {
		return 0, 0, false
	}
	bucket = src.StreamSeq / bucketSize
	return bucket, uint32(src.StreamSeq%bucketSize)*fanSlots + uint32(item.index), true
}

// ProcessResolvedGroup is ProcessResolvedEvent for the flag-on path: it collects the resolved
// events that arrive within collectLinger (at most collectMax), groups them and publishes the
// groups. It returns true when the channel is closed and drained.
func (iproc *InboundEventsProcessor) ProcessResolvedGroup(ctx context.Context) bool {
	item, more := <-iproc.resolved
	if !more {
		return true
	}
	limit := collectMax
	if iproc.envelopeCollectMax > 0 {
		limit = iproc.envelopeCollectMax
	}
	batch := append(make([]resolvedItem, 0, limit), item)
	closed := false
	timer := time.NewTimer(iproc.envLinger())
collect:
	for len(batch) < limit {
		select {
		case it, ok := <-iproc.resolved:
			if !ok {
				closed = true
				break collect
			}
			batch = append(batch, it)
		case <-timer.C:
			break collect
		}
	}
	timer.Stop()
	iproc.env.publishBatch(ctx, batch)
	return closed
}

func (iproc *InboundEventsProcessor) envLinger() time.Duration {
	if iproc.envelopeLinger != nil {
		return *iproc.envelopeLinger
	}
	return collectLinger
}

// publishBatch groups the batch by (tenant, bucket) and publishes each group in chunks.
func (e *envelopeEngine) publishBatch(ctx context.Context, batch []resolvedItem) {
	groups := map[bucketKey][]member{}
	var order []bucketKey
	for _, item := range batch {
		bytes, err := resolvedevent.MarshalResolvedEvent(&item.event)
		if err != nil {
			e.iproc.publishResolvedLegacy(ctx, item, nil, err)
			continue
		}
		bucket, idx, ok := memberIndex(item, len(bytes))
		if !ok {
			e.legacy.Add(1)
			e.iproc.publishResolvedLegacy(ctx, item, bytes, nil)
			continue
		}
		k := bucketKey{item.tenant, bucket}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], member{item: item, bytes: bytes, index: idx})
	}
	for _, k := range order {
		ms := groups[k]
		// Source order, not completion order: the resolver pool finishes out of order, and an
		// envelope's members are applied by every reader in the order they hold.
		slices.SortFunc(ms, func(a, b member) int { return int(int64(a.index) - int64(b.index)) })
		for len(ms) > 0 {
			n, size := 0, 0
			for n < len(ms) && n < e.chunkMax() && size+len(ms[n].bytes) <= eventenvelope.MaxRawBytes {
				size += len(ms[n].bytes)
				n++
			}
			e.publishChunk(ctx, k, ms[:n])
			ms = ms[n:]
		}
	}
}

// publishChunk publishes members (ascending by index, one bucket) as the bucket's next round.
func (e *envelopeEngine) publishChunk(ctx context.Context, k bucketKey, ms []member) {
	// 🔴 THE PROCESS'S OWN ROUND COUNT IS ONLY FOR EVENTS NOBODY HAS PUBLISHED. A source that is
	// being redelivered may have been published in ANY earlier round of its bucket - by a pod that
	// died, or by this one whose acknowledgement was lost or whose recovery is still running - so
	// a chunk with any redelivered source starts at round 0 and walks up: each stored round it
	// meets is read, and the first free one is where the rest goes. Starting at the counter would
	// skip the rounds below it, and store a second copy of whatever they hold.
	round := e.next[k]
	for _, m := range ms {
		if m.item.coord.src.NumDelivered > 1 {
			round = 0
			break
		}
	}
	if len(e.next) >= roundStateMax {
		clear(e.next)
	}
	e.next[k] = max(e.next[k], round+1)
	msg, err := buildRound(k, round, ms)
	if err != nil {
		// A group the format refuses is not lost: its events go out as they do with the flag off.
		log.Warn().Err(err).Str("tenant", k.tenant).Int("members", len(ms)).
			Msg("Could not encode a resolved envelope; publishing its events one by one")
		for _, m := range ms {
			e.legacy.Add(1)
			e.iproc.publishResolvedLegacy(ctx, m.item, m.bytes, nil)
		}
		return
	}
	chunk := ms
	e.writer.PublishAcked(core.WithTenant(ctx, k.tenant), msg, func(ack messaging.PubAckInfo, err error) {
		switch {
		case err != nil:
			e.settle(chunk, err)
		case ack.Duplicate:
			e.duplicates.Add(1)
			e.startRecovery(k, round, chunk, ack)
		default:
			e.settle(chunk, nil)
		}
	})
}

// buildRound builds the message for round of the bucket's members.
func buildRound(k bucketKey, round uint32, ms []member) (messaging.Message, error) {
	id := messaging.EnvelopeDedupID(messaging.EnvelopeDedupKind, k.tenant, k.bucket, round)
	corr := ms[0].item.correlation
	if len(ms) == 1 {
		return messaging.Message{
			Key:     []byte(ms[0].item.event.SourceDeviceToken),
			Value:   ms[0].bytes,
			DedupID: id,
			Headers: map[string]string{messaging.HeaderMemberIndex: strconv.FormatUint(uint64(ms[0].index), 36)},
		}.WithCorrelationID(corr), nil
	}
	events := make([]eventenvelope.Event, len(ms))
	for i, m := range ms {
		events[i] = eventenvelope.Event{Index: m.index, Bytes: m.bytes}
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{
		Kind: eventenvelope.KindResolved, Tenant: k.tenant, SourceSeq: k.bucket * bucketSize, Round: round,
	}, events)
	if err != nil {
		return messaging.Message{}, err
	}
	return messaging.Message{
		Value:   body,
		DedupID: id,
		Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue},
	}.WithCorrelationID(corr), nil
}

// settle reports the outcome of publishing ms to each member's source.
func (e *envelopeEngine) settle(ms []member, err error) {
	if err == nil {
		e.envelopes.Add(1)
		e.members.Add(int64(len(ms)))
		if len(ms) == 1 {
			e.singles.Add(1)
		}
	}
	for _, m := range ms {
		e.iproc.settleResolved(m.item.coord, err)
	}
}

// startRecovery settles a chunk whose round the broker answered as a duplicate, off the
// writer's goroutine: it reads, which the settle goroutine must not.
func (e *envelopeEngine) startRecovery(k bucketKey, round uint32, ms []member, ack messaging.PubAckInfo) {
	e.iproc.recoverWG.Add(1)
	go func() {
		defer e.iproc.recoverWG.Done()
		e.sem <- struct{}{}
		defer func() { <-e.sem }()
		e.recover(k, round, ms, ack)
	}()
}

// recover runs the settlement protocol for ms, whose round was answered as a duplicate by
// ack: read what the original carried, publish the rest as the next round, repeat until a round
// is stored as new or nothing is left. Any failure leaves every member still unsettled failed,
// so its source is redelivered.
func (e *envelopeEngine) recover(k bucketKey, round uint32, ms []member, ack messaging.PubAckInfo) {
	start := round
	for {
		held, err := e.carried(k.tenant, ack.Sequence)
		if err != nil {
			log.Warn().Err(err).Str("tenant", k.tenant).Uint64("bucket", k.bucket).Uint32("round", round).
				Msg("Could not read the envelope a duplicate publish named; leaving its sources unacknowledged")
			e.settle(ms, err)
			return
		}
		var done, rest []member
		for _, m := range ms {
			if _, ok := held[m.index]; ok {
				done = append(done, m)
			} else {
				rest = append(rest, m)
			}
		}
		// What the original carried is published: its sources count it as settled.
		for _, m := range done {
			e.iproc.settleResolved(m.item.coord, nil)
		}
		if len(rest) == 0 {
			return
		}
		round++
		if round-start > maxRounds {
			e.settle(rest, fmt.Errorf("resolved envelope for bucket %d needed more than %d rounds", k.bucket, maxRounds))
			return
		}
		msg, err := buildRound(k, round, rest)
		if err != nil {
			e.settle(rest, err)
			return
		}
		ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), k.tenant), recoveryTimeout)
		next, err := e.rounds.Publish(ctx, msg)
		cancel()
		if err != nil {
			e.settle(rest, err)
			return
		}
		e.recovered.Add(int64(len(rest)))
		if !next.Duplicate {
			e.settle(rest, nil)
			return
		}
		e.duplicates.Add(1)
		ms, ack = rest, next
	}
}

// recoveryTimeout bounds one round's publish in a recovery.
const recoveryTimeout = 10 * time.Second

// errNotAnEnvelope marks a stored message under an envelope id that is neither an envelope nor
// a member published alone: the id's protocol cannot be followed, so nothing is assumed.
var errNotAnEnvelope = errors.New("the stored message is neither a resolved envelope nor a single member")

// carried returns the source indices the stored message at seq settled: its members and the
// indices it says were settled elsewhere. Anything that does not parse as exactly that is an
// error - never an empty set, which would republish what is already stored.
func (e *envelopeEngine) carried(tenant string, seq uint64) (map[uint32]struct{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), recoveryTimeout)
	defer cancel()
	stored, err := e.rounds.Get(ctx, tenant, seq)
	if err != nil {
		return nil, err
	}
	out := map[uint32]struct{}{}
	if v := stored.Headers[eventenvelope.HeaderName]; v != "" {
		if v != eventenvelope.HeaderValue {
			return nil, fmt.Errorf("%w: marker %q", errNotAnEnvelope, v)
		}
		r, err := eventenvelope.Parse(stored.Value)
		if err != nil {
			return nil, err
		}
		if err := r.VerifyTenant(tenant); err != nil {
			return nil, err
		}
		if r.Header().Kind != eventenvelope.KindResolved {
			return nil, fmt.Errorf("%w: kind %s", errNotAnEnvelope, r.Header().Kind)
		}
		for i := 0; i < r.Len(); i++ {
			out[r.Index(i)] = struct{}{}
		}
		for _, i := range r.Header().SettledElsewhere {
			out[i] = struct{}{}
		}
		return out, nil
	}
	mi := stored.Headers[messaging.HeaderMemberIndex]
	if mi == "" {
		return nil, errNotAnEnvelope
	}
	idx, err := strconv.ParseUint(mi, 36, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: member index %q", errNotAnEnvelope, mi)
	}
	out[uint32(idx)] = struct{}{}
	return out, nil
}

// chunkMax is how many members one envelope holds: the format bound, unless a test fixes k.
func (e *envelopeEngine) chunkMax() int {
	if n := e.iproc.envelopeChunkMax; n > 0 && n <= eventenvelope.MaxMembers {
		return n
	}
	return eventenvelope.MaxMembers
}
