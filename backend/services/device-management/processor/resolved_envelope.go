// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Resolved envelopes (infrastructure.messaging.envelopes.resolved): k resolved events of one
// tenant in one message on resolved-events, in place of k messages.
//
// 🔴 DO NOT ENABLE THE FLAG before event-processing's per-member DETECT guard (E4) and these
// fixes ship together: a duplicate that escapes the protocol below is applied twice by DETECT.
//
// This is the INTERIM grouping of the design (slice E3, before event-sources publishes inbound
// envelopes): device-management groups the events it resolved from LEGACY inbound messages. The
// flag-off path is the code in inbound.go, untouched; nothing here runs unless the flag is on.
//
// # Grouping, and the id a regrouping cannot break
//
// The events of one tenant resolved from one BUCKET of the inbound stream form an envelope, the
// members in source order. A bucket is a range of bucketSize sequences AND a bucketSeconds span
// of the broker's own append time (a stable property of the inbound message, the same on every
// redelivery). The envelope's Nats-Msg-Id names (tenant, bucket, round), never the members:
//
//	G:<tenant>:<source b36>:<round b36>      source = sequence bucket << 28 | time bucket
//
// The design keyed it on the FIRST inbound sequence of the group. That id changes when a
// redelivery regroups, and the regrouped envelope is then stored a second time. A bucket does not
// move: the same source is always in the same bucket however it is grouped.
//
// 🔴 THE TIME HALF IS LOAD-BEARING. The inbound stream is shared by every tenant, so a range of
// 256 sequences spans however long 256 inbound events take, which at a few messages a second is
// minutes. A redelivery walks rounds from 0, and a round whose id has aged out of the stream's
// duplicate window looks free, so the event would be stored again. Bounding a bucket to
// bucketSeconds keeps (bucket span + republish span) inside the window; see
// TestEnvelopeIdsFitTheDuplicateWindow.
//
// # Settlement rounds
//
// Round 0 of a bucket is published first. If the broker answers it as a DUPLICATE, the PubAck
// names the sequence of the message that holds the id. That message is read back, the members it
// carries are the members already published, and only the rest are published as round 1, and so
// on. So a redelivery after a lost ack publishes nothing, and one after a partial publish
// publishes exactly the complement. Two replicas that hold different sources of one bucket reach
// the same ids with different members; the same protocol makes the later one take the next round.
//
// Everything a process learns about a bucket is shared by all of its goroutines: a high-water
// mark (the next round to try, raised by every round it stores and every duplicate answer) and a
// cache of what each stored round carries (a stored round never changes), so the probes of one
// bucket are paid once, not once per chunk.
//
// 🔴 A ROUND THE BROKER REJECTED IS RETRIED UNDER THE SAME ID until its outcome is known (the pod
// is alive and the sources are unacknowledged). Skipping it would leave a hole below a stored
// round, and a redelivery walk would fill the hole with events the later round already holds.
//
// # What is not grouped
//
// An event with no source sequence, a member of an inbound envelope or a copy split out of one
// (those are named by their own coordinate and have per-event ids that are already exact), an
// event past the fan-out slots a member index can name, or an event larger than an envelope can
// hold is published in the legacy shape exactly as with the flag off. A groupable event is NEVER
// published under a per-event id: when the encoder refuses a chunk (size, a token that is not
// UTF-8) the chunk is split, down to single members, which are published in the legacy shape
// under the SAME group id with HeaderMemberIndex naming which member it is, so a duplicate probe
// can read it.
const (
	// bucketSize is how many inbound stream sequences one bucket spans. 256 is the inbound
	// fetch size, so a fetch lands in one or two buckets.
	bucketSize = 256
	// bucketSeconds is how long, in the broker's append time, one bucket spans. The stream's
	// duplicate window must cover this plus the republish span of a source.
	bucketSeconds = 30
	// fanSlots is how many resolved events one source can have in a member index. A source
	// fanning out to more publishes its extra events in the legacy shape.
	fanSlots = 64
	// collectLinger bounds how long the envelope submitter waits for more events to group after
	// the first. Device-management cannot see a fetch's boundary past the reader, and without a
	// wait the submitter, which is much faster than the resolvers, sees one event at a time.
	collectLinger = 2 * time.Millisecond
	// collectMax bounds the events one collection holds.
	collectMax = 256
	// maxRounds bounds the rounds one bucket can need: its members cannot exceed
	// bucketSize*fanSlots, and every round carries at least one. Reaching it is an error (the
	// sources are left unacknowledged), never a loop.
	maxRounds = bucketSize * fanSlots
	// stateMax bounds the shared per-bucket state; past it the maps are dropped, which costs the
	// next publish of a bucket its probes and nothing else.
	stateMax = 8192
	// recoveryWorkers bounds the walks (duplicate recoveries and retried rounds) in flight at
	// once. A new one waits for a slot, which holds the writer's settle loop, which holds the
	// submitter: the walks are back-pressure, not an unbounded pile of goroutines.
	recoveryWorkers = 16
	// recoveryTimeout bounds one request of a walk.
	recoveryTimeout = 10 * time.Second
	// retryGiveUp bounds how long a rejected round is retried before its sources are left to
	// the broker's redelivery.
	retryGiveUp = 5 * time.Minute
	// walkBeat is how often a long walk tells the broker its sources are still being worked on.
	walkBeat = 10 * time.Second
)

// envelopeEngine publishes grouped resolved events. publishBatch is called by ONE goroutine,
// the processor's resolved loop; walks run on goroutines of their own and share the state below.
type envelopeEngine struct {
	iproc  *InboundEventsProcessor
	writer messaging.AckedWriter
	rounds messaging.RoundPublisher
	sem    chan struct{}
	// ctx ends the walks; the processor cancels it shortly after it stops submitting.
	ctx context.Context

	mu sync.Mutex
	// hw is the next round to try for a bucket: above every round this process has stored or been
	// told is stored.
	hw map[bucketKey]uint32
	// known is what each round this process has seen stored carries.
	known map[roundKey]map[uint32]struct{}

	// Counters, for tests and logs.
	envelopes  atomic.Int64 // messages published in the envelope shape
	members    atomic.Int64 // events inside them
	singles    atomic.Int64 // groups of one, published legacy-shaped under the envelope id
	legacy     atomic.Int64 // events published exactly as with the flag off
	duplicates atomic.Int64 // PubAcks answered as duplicates
	recovered  atomic.Int64 // members published by a later round
	probes     atomic.Int64 // requests a walk made: publishes and read-backs
	retries    atomic.Int64 // rejected rounds published again under the same id
	// giveUp overrides retryGiveUp (tests).
	giveUp time.Duration
	// beatEvery is how often a walk tells the broker its sources are alive (walkBeat; tests shorten it).
	beatEvery time.Duration
}

type bucketKey struct {
	tenant string
	bucket uint64 // inbound sequence / bucketSize
	epoch  uint64 // inbound append time (Unix seconds) / bucketSeconds
}

type roundKey struct {
	b     bucketKey
	round uint32
}

// source is the number the id names: the sequence bucket and the time bucket together.
func (k bucketKey) source() uint64 { return k.bucket<<28 | k.epoch&(1<<28-1) }

func newEnvelopeEngine(ctx context.Context, iproc *InboundEventsProcessor, w messaging.AckedWriter, rp messaging.RoundPublisher) *envelopeEngine {
	return &envelopeEngine{iproc: iproc, writer: w, rounds: rp, ctx: ctx, sem: make(chan struct{}, recoveryWorkers),
		beatEvery: walkBeat, hw: make(map[bucketKey]uint32), known: make(map[roundKey]map[uint32]struct{})}
}

// member is one resolved event ready to publish: its bytes, its place in its bucket, and the
// coordinators of every source whose acknowledgement waits on it (more than one when a source and
// its redelivery are queued together: they are the same event, so they are one member).
type member struct {
	item   resolvedItem
	bytes  []byte
	index  uint32
	extras []*ackCoord
}

func (m member) each(f func(*ackCoord)) {
	f(m.item.coord)
	for _, c := range m.extras {
		f(c)
	}
}

// memberIndex names a resolved event within its bucket: (source sequence's place in the bucket,
// fan-out position). ok is false for an event that cannot be grouped.
func memberIndex(item resolvedItem, size int) (k bucketKey, index uint32, ok bool) {
	if item.coord == nil || item.index < 0 || item.index >= fanSlots || size > eventenvelope.MaxRawBytes {
		return k, 0, false
	}
	src := item.coord.src
	if src.StreamSeq == 0 {
		return k, 0, false
	}
	if _, _, named := src.SourceCoordinate(); named {
		return k, 0, false
	}
	k = bucketKey{tenant: item.tenant, bucket: src.StreamSeq / bucketSize}
	if !src.AppendTime.IsZero() {
		k.epoch = uint64(src.AppendTime.Unix() / bucketSeconds)
	}
	return k, uint32(src.StreamSeq%bucketSize)*fanSlots + uint32(item.index), true
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

func inc(c prometheus.Counter) {
	if c != nil {
		c.Inc()
	}
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
		k, idx, ok := memberIndex(item, len(bytes))
		if !ok {
			e.legacy.Add(1)
			e.iproc.publishResolvedLegacy(ctx, item, bytes, nil)
			continue
		}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		// A source and its redelivery can both be queued: the same index twice. They are one
		// event, so one member that settles both sources.
		merged := false
		for i := range groups[k] {
			if groups[k][i].index == idx {
				groups[k][i].extras = append(groups[k][i].extras, item.coord)
				merged = true
				break
			}
		}
		if !merged {
			groups[k] = append(groups[k], member{item: item, bytes: bytes, index: idx})
		}
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
			if n == 0 {
				n = 1
			}
			e.publishChunk(ctx, k, ms[:n])
			ms = ms[n:]
		}
	}
}

// redelivered reports whether any source of ms has been delivered before.
func redelivered(ms []member) bool {
	for _, m := range ms {
		if m.item.coord.src.NumDelivered > 1 {
			return true
		}
	}
	return false
}

// publishChunk publishes members (ascending by index, one bucket).
func (e *envelopeEngine) publishChunk(ctx context.Context, k bucketKey, ms []member) {
	// 🔴 THE PROCESS'S HIGH-WATER MARK IS ONLY FOR EVENTS NOBODY HAS PUBLISHED. A source that is
	// being redelivered may have been published in ANY earlier round of its bucket - by a pod that
	// died, or by this one - so a chunk with a redelivered source starts at round 0 and walks up,
	// skipping the rounds already known to be stored (no request) and probing the rest.
	redel := redelivered(ms)
	round := uint32(0)
	if !redel {
		round = e.take(k)
	} else {
		ms, round = e.skipKnown(k, ms, 0)
		if len(ms) == 0 {
			return
		}
	}
	e.publishPieces(ctx, k, ms, round, redel)
}

// publishPieces publishes ms at round, splitting the chunk when the encoder refuses it. The
// pieces take consecutive rounds; the first one is given.
func (e *envelopeEngine) publishPieces(ctx context.Context, k bucketKey, ms []member, round uint32, redel bool) {
	msg, err := buildRound(k, round, ms)
	if err != nil {
		if len(ms) == 1 {
			// A single member is never encoded, so this is not reachable; failing the source is
			// the loud outcome, never a per-event id.
			e.settle(ms, err)
			return
		}
		half := len(ms) / 2
		e.publishPieces(ctx, k, ms[:half], round, redel)
		next := round + 1
		if !redel {
			next = e.take(k)
		}
		e.publishPieces(ctx, k, ms[half:], next, redel)
		return
	}
	e.raise(k, round)
	chunk := ms
	e.writer.PublishAcked(core.WithTenant(ctx, k.tenant), msg, func(ack messaging.PubAckInfo, err error) {
		switch {
		case err != nil:
			// Rejected, or the outcome unknown: the round is retried under the SAME id until it is
			// known. Moving on would leave a hole below whatever is stored above it.
			inc(e.iproc.metrics.envRetries)
			e.retries.Add(1)
			e.startWalk(func(ctx context.Context) { e.walk(ctx, k, round, chunk, nil, true) })
		case ack.Duplicate:
			e.duplicates.Add(1)
			inc(e.iproc.metrics.envDuplicates)
			e.raise(k, round)
			e.startWalk(func(ctx context.Context) { e.walk(ctx, k, round, chunk, &ack, false) })
		default:
			e.learn(k, round, chunk)
			e.settle(chunk, nil)
		}
	})
}

// buildRound builds the message for round of the bucket's members.
func buildRound(k bucketKey, round uint32, ms []member) (messaging.Message, error) {
	id := messaging.EnvelopeDedupID(messaging.EnvelopeDedupKind, k.tenant, k.source(), round)
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
		m.each(func(c *ackCoord) { e.iproc.settleResolved(c, err) })
	}
}

// settleOK settles members that a stored round was found to carry.
func (e *envelopeEngine) settleOK(ms []member) {
	for _, m := range ms {
		m.each(func(c *ackCoord) { e.iproc.settleResolved(c, nil) })
	}
}

// ---- shared per-bucket state ------------------------------------------------------------

// take allocates the next round of a bucket for events nobody has published.
func (e *envelopeEngine) take(k bucketKey) uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.hw[k]
	e.hw[k] = r + 1
	e.trimLocked()
	return r
}

// raise records that round is in use, so no later chunk starts at or below it.
func (e *envelopeEngine) raise(k bucketKey, round uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hw[k] < round+1 {
		e.hw[k] = round + 1
	}
	e.trimLocked()
}

// learn records that round is stored and carries ms.
func (e *envelopeEngine) learn(k bucketKey, round uint32, ms []member) {
	set := make(map[uint32]struct{}, len(ms))
	for _, m := range ms {
		set[m.index] = struct{}{}
	}
	e.remember(k, round, set)
}

func (e *envelopeEngine) remember(k bucketKey, round uint32, set map[uint32]struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.known[roundKey{k, round}] = set
	if e.hw[k] < round+1 {
		e.hw[k] = round + 1
	}
	e.trimLocked()
}

func (e *envelopeEngine) lookup(k bucketKey, round uint32) (map[uint32]struct{}, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.known[roundKey{k, round}]
	return s, ok
}

// trimLocked drops the state when it outgrows its bound: the cost is probes, never correctness.
func (e *envelopeEngine) trimLocked() {
	if len(e.hw) > stateMax {
		clear(e.hw)
	}
	if len(e.known) > stateMax {
		clear(e.known)
	}
}

// skipKnown walks rounds from `from` while they are known to be stored, settling the members they
// carry, and returns the members still unpublished and the first round not known.
func (e *envelopeEngine) skipKnown(k bucketKey, ms []member, from uint32) ([]member, uint32) {
	round := from
	for len(ms) > 0 {
		held, ok := e.lookup(k, round)
		if !ok {
			break
		}
		var done, rest []member
		for _, m := range ms {
			if _, in := held[m.index]; in {
				done = append(done, m)
			} else {
				rest = append(rest, m)
			}
		}
		e.settleOK(done)
		ms = rest
		round++
	}
	return ms, round
}

// ---- walks ------------------------------------------------------------------------------

// startWalk runs a walk on a worker, waiting for a free one. It is called from the writer's
// settle goroutine, so a full pool back-pressures the writer and through it the submitter.
func (e *envelopeEngine) startWalk(f func(ctx context.Context)) {
	e.iproc.recoverWG.Add(1)
	select {
	case e.sem <- struct{}{}:
	case <-e.ctx.Done():
		// Stopping: the walk is not run and its sources stay unacknowledged for the next pod.
		e.iproc.recoverWG.Done()
		return
	}
	go func() {
		defer e.iproc.recoverWG.Done()
		defer func() { <-e.sem }()
		f(e.ctx)
	}()
}

// beat tells the broker the walk's sources are still being worked on.
func (e *envelopeEngine) beat(ms []member) {
	seen := map[*ackCoord]struct{}{}
	for _, m := range ms {
		m.each(func(c *ackCoord) {
			if _, dup := seen[c]; !dup {
				seen[c] = struct{}{}
				_ = c.src.InProgress()
			}
		})
	}
}

var errWalkStopped = errors.New("resolved envelope: stopped before the round's outcome was known")

// walk settles ms starting at round: publish it (or, with dup set, take the answer already in
// hand), and while the broker says a round is already stored, read what it carries, settle those
// members and publish the rest as the next round. A round whose publish fails is published again
// under the same id (retry) until its outcome is known. Any unrecoverable failure fails every
// member still unsettled, so its source is redelivered.
func (e *envelopeEngine) walk(ctx context.Context, k bucketKey, round uint32, ms []member, dup *messaging.PubAckInfo, retry bool) {
	start := round
	lastBeat := time.Now()
	var giveUp <-chan time.Time
	if retry {
		t := time.NewTimer(e.giveUpAfter())
		defer t.Stop()
		giveUp = t.C
	}
	for len(ms) > 0 {
		if time.Since(lastBeat) >= e.beatEvery {
			e.beat(ms)
			lastBeat = time.Now()
		}
		var ack messaging.PubAckInfo
		if dup != nil {
			ack, dup = *dup, nil
		} else {
			ms, round = e.skipKnown(k, ms, round)
			if len(ms) == 0 {
				return
			}
			if round-start > maxRounds {
				e.settle(ms, fmt.Errorf("resolved envelope for bucket %d needed more than %d rounds", k.bucket, maxRounds))
				return
			}
			msg, err := buildRound(k, round, ms)
			if err != nil && len(ms) > 1 {
				half := len(ms) / 2
				e.walk(ctx, k, round, ms[:half], nil, retry)
				e.walk(ctx, k, round, ms[half:], nil, retry)
				return
			}
			if err != nil {
				e.settle(ms, err)
				return
			}
			next, err := e.publishRound(ctx, k, msg, giveUp)
			if err != nil {
				e.settle(ms, err)
				return
			}
			if !next.Duplicate {
				if round > start {
					e.recovered.Add(int64(len(ms)))
				}
				e.learn(k, round, ms)
				e.settle(ms, nil)
				return
			}
			e.duplicates.Add(1)
			inc(e.iproc.metrics.envDuplicates)
			ack = next
		}
		// The round is stored: read what it carries.
		held, err := e.heldOf(ctx, k, round, ack.Sequence)
		if err != nil {
			inc(e.iproc.metrics.envProbeFailures)
			log.Warn().Err(err).Str("tenant", k.tenant).Uint64("bucket", k.bucket).Uint32("round", round).
				Msg("Could not read the envelope a duplicate publish named; leaving its sources unacknowledged")
			e.settle(ms, err)
			return
		}
		var done, rest []member
		for _, m := range ms {
			if _, in := held[m.index]; in {
				done = append(done, m)
			} else {
				rest = append(rest, m)
			}
		}
		e.settleOK(done)
		ms = rest
		round++
		inc(e.iproc.metrics.envRecoverySteps)
	}
}

// publishRound publishes msg synchronously, again under the same id while it fails, until the
// outcome is known, the walk is stopped, or giveUp fires (nil: never).
func (e *envelopeEngine) publishRound(ctx context.Context, k bucketKey, msg messaging.Message, giveUp <-chan time.Time) (messaging.PubAckInfo, error) {
	backoff := 200 * time.Millisecond
	for {
		e.probes.Add(1)
		rctx, cancel := context.WithTimeout(core.WithTenant(ctx, k.tenant), recoveryTimeout)
		ack, err := e.rounds.Publish(rctx, msg)
		cancel()
		if err == nil {
			return ack, nil
		}
		if giveUp == nil {
			return ack, err
		}
		select {
		case <-ctx.Done():
			return ack, errWalkStopped
		case <-giveUp:
			return ack, err
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 5*time.Second)
		inc(e.iproc.metrics.envRetries)
		e.retries.Add(1)
	}
}

// heldOf returns what the stored round carries: from the cache, or by reading the message at seq.
func (e *envelopeEngine) heldOf(ctx context.Context, k bucketKey, round uint32, seq uint64) (map[uint32]struct{}, error) {
	if s, ok := e.lookup(k, round); ok {
		return s, nil
	}
	e.probes.Add(1)
	s, err := e.carried(ctx, k.tenant, seq)
	if err != nil {
		return nil, err
	}
	e.remember(k, round, s)
	return s, nil
}

// errNotAnEnvelope marks a stored message under an envelope id that is neither an envelope nor
// a member published alone: the id's protocol cannot be followed, so nothing is assumed.
var errNotAnEnvelope = errors.New("the stored message is neither a resolved envelope nor a single member")

// carried returns the source indices the stored message at seq settled: its members and the
// indices it says were settled elsewhere. Anything that does not parse as exactly that is an
// error - never an empty set, which would republish what is already stored.
func (e *envelopeEngine) carried(ctx context.Context, tenant string, seq uint64) (map[uint32]struct{}, error) {
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
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

func (e *envelopeEngine) giveUpAfter() time.Duration {
	if e.giveUp > 0 {
		return e.giveUp
	}
	return retryGiveUp
}
