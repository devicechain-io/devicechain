// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

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
	"github.com/rs/zerolog/log"
)

// RoundEngine publishes the events of a SEQUENCED source (a stream the producer consumes, whose
// messages are redelivered until it acks them) as envelopes, exactly once per event in the
// common case and at-least-once always. It is the capture hop's settlement: event-sources reads
// the capture stream and publishes its events to inbound-events.
//
// It is the protocol device-management's interim resolved grouping runs (the settlement rounds of
// the envelope design), generalised over the member type.
//
// # The id a regrouping cannot break
//
// The events of one tenant read from one BUCKET of the source stream form an envelope, members in
// source order. A bucket is a range of RoundBucketSize sequences AND a RoundBucketSeconds span of
// the broker's append time (a stable property of a source message, the same on every
// redelivery). The envelope's Nats-Msg-Id names (tenant, bucket, round), never its members:
//
//	<kind>:<tenant>:<bucket<<28 | time bucket, b36>:<round b36>
//
// so a redelivery that regroups - a different fetch boundary, a different linger - still addresses
// the same ids. The id of an envelope that groups by FIRST member would change with the grouping
// and the regrouped envelope would be stored a second time.
//
// 🔴 THE TIME HALF IS LOAD-BEARING: a redelivery walks rounds from 0, and a round whose id has
// aged out of the stream's duplicate window looks free, so its events would be stored again.
// Bounding a bucket to RoundBucketSeconds keeps (bucket span + redelivery span) inside the window
// (RoundBucketSeconds + the redelivery span of the source <= the window).
//
// # Rounds
//
// A source's first publish takes the bucket's next round (a process-wide high-water mark). If the
// broker answers a publish as a DUPLICATE its PubAck names the sequence of the message holding
// the id; that message is read back (from the stream leader), the members it carries are the
// members already published, and only the rest are published as the next round, and so on. So a
// redelivery after a lost ack publishes nothing, one after a partial publish publishes the
// complement, and two replicas that hold different sources of one bucket reach the same ids with
// different members and the later one takes the next round.
//
// A source that is being redelivered (Redelivered) may have been published in ANY earlier round
// of its bucket - by a pod that died, or by this one - so its chunk starts at round 0 and walks up,
// skipping rounds already known to be stored and probing the rest.
//
// 🔴 A ROUND THE BROKER REJECTED IS RETRIED UNDER THE SAME ID until its outcome is known (the pod
// is alive and the sources are unacknowledged). Skipping it would leave a hole below a stored
// round, and a redelivery walk would fill the hole with events the later round already holds.
//
// An event whose envelope the encoder refuses (a size, a token that is not UTF-8) is split down to
// single members, published in the legacy shape under the SAME group id with HeaderMemberIndex
// naming which member it is - never under a per-event id, which no walk could see.
const (
	// RoundBucketSize is how many source stream sequences one bucket spans. 256 is a fetch.
	RoundBucketSize = 256
	// RoundBucketSeconds is how long, in the broker's append time, one bucket spans.
	RoundBucketSeconds = 30

	// maxRoundsPerBucket bounds the rounds one bucket can need: a member per sequence, one round
	// at least per member. Reaching it is an error (the sources stay unacknowledged), never a loop.
	maxRoundsPerBucket = RoundBucketSize
	roundStateMax      = 8192
	roundWorkers       = 16
	roundTimeout       = 10 * time.Second
	roundGiveUp        = 5 * time.Minute
	roundBeat          = 10 * time.Second
)

// RoundKey names a bucket of one tenant's sequenced source.
type RoundKey struct {
	Tenant string
	// Bucket is source stream sequence / RoundBucketSize.
	Bucket uint64
	// Epoch is the source message's append time (Unix seconds) / RoundBucketSeconds; 0 when the
	// message carries none.
	Epoch uint64
	// Mapped marks an envelope mapped 1:1 from ONE source message (device-management's resolved
	// envelope for an inbound envelope): Bucket is then that message's own stream sequence, there
	// is no time half (a source message is immutable, so its id never regroups), and the id is
	// the engine's MappedDedupKind.
	Mapped bool
}

// RoundKeyOf is the bucket a source message with stream sequence seq and broker append time at
// belongs to, and the member index it takes in it. ok is false for a message with no sequence,
// which cannot be named and is published as it always was.
func RoundKeyOf(tenant string, seq uint64, at time.Time) (k RoundKey, index uint32, ok bool) {
	if seq == 0 {
		return k, 0, false
	}
	k = RoundKey{Tenant: tenant, Bucket: seq / RoundBucketSize}
	if !at.IsZero() {
		k.Epoch = uint64(at.Unix() / RoundBucketSeconds)
	}
	return k, uint32(seq % RoundBucketSize), true
}

// Source is the number the id names: the sequence bucket and the time bucket together.
func (k RoundKey) Source() uint64 {
	if k.Mapped {
		return k.Bucket
	}
	return k.Bucket<<28 | k.Epoch&(1<<28-1)
}

// headerSeq is the SourceSeq an envelope of this bucket declares.
func (k RoundKey) headerSeq() uint64 {
	if k.Mapped {
		return k.Bucket
	}
	return k.Bucket * RoundBucketSize
}

type roundKey struct {
	b     RoundKey
	round uint32
}

// RoundMember is one event ready to publish.
type RoundMember struct {
	// Index is the member's place in its bucket (RoundKeyOf).
	Index uint32
	// Bytes is the event, as its marshaller wrote it.
	Bytes []byte
	// Key is the producer-local message key (the device token); not transmitted.
	Key []byte
	// Correlation, when set, is stamped on the message that carries the member (the first
	// member's names an envelope).
	Correlation string
	// Redelivered says the source has been delivered before (NumDelivered > 1).
	Redelivered bool
	// Settle reports the outcome: nil only once the broker holds the member (stored by this
	// publish, or found stored by a read-back), non-nil to leave the source unacknowledged. It is
	// called exactly once, from the writer's settle goroutine or a walk's.
	Settle func(error)
	// Beat, when set, tells the source's broker the member is still being worked on (a long
	// walk). Optional.
	Beat func()
}

// RoundEngineConfig wires a RoundEngine.
type RoundEngineConfig struct {
	// Kind is the envelope kind published (and expected on read-back).
	Kind eventenvelope.Kind
	// DedupKind is the id's kind letter. It must be upper case and disjoint from every other id
	// shape on the stream.
	DedupKind string
	// MappedDedupKind is the kind letter of RoundKey.Mapped buckets; required to publish any.
	MappedDedupKind string
	// Writer publishes the first attempt of each round; Rounds publishes the later ones.
	Writer AckedWriter
	Rounds RoundPublisher
	// Ctx ends the walks.
	Ctx context.Context
	// ChunkMax, when set, caps k (read at every publish; zero or beyond the format bound is
	// eventenvelope.MaxMembers). Tests fix k with it.
	ChunkMax func() int
	// Metrics may be nil.
	Metrics *EnvelopeMetrics
}

// RoundEngine is documented above. Publish is called by ONE goroutine (the Writer's submitter);
// walks run on goroutines of their own and share the state below.
type RoundEngine struct {
	cfg RoundEngineConfig
	sem chan struct{}
	wg  sync.WaitGroup

	mu    sync.Mutex
	hw    map[RoundKey]uint32
	known map[roundKey]map[uint32]struct{}

	// Counters, for tests and logs.
	Envelopes  atomic.Int64 // messages published in the envelope shape
	Members    atomic.Int64 // events inside them
	Singles    atomic.Int64 // groups of one, published legacy-shaped under the envelope id
	Duplicates atomic.Int64 // PubAcks answered as duplicates
	Recovered  atomic.Int64 // members published by a later round
	Probes     atomic.Int64 // requests a walk made: publishes and read-backs
	Retries    atomic.Int64 // rejected rounds published again under the same id

	// giveUp overrides roundGiveUp and beatEvery overrides roundBeat (tests).
	giveUp    time.Duration
	beatEvery time.Duration
}

// NewRoundEngine builds the engine. It refuses a configuration that cannot settle what it
// publishes.
func NewRoundEngine(cfg RoundEngineConfig) (*RoundEngine, error) {
	if cfg.Writer == nil || cfg.Rounds == nil {
		return nil, errors.New("messaging: a round engine needs a writer that reports the PubAck and a round publisher")
	}
	if cfg.MappedDedupKind != "" && (cfg.MappedDedupKind == cfg.DedupKind ||
		cfg.MappedDedupKind != upper(cfg.MappedDedupKind)) {
		return nil, fmt.Errorf("messaging: round engine mapped id kind %q must be an upper-case letter of its own", cfg.MappedDedupKind)
	}
	if cfg.DedupKind == "" || cfg.DedupKind != upper(cfg.DedupKind) {
		return nil, fmt.Errorf("messaging: round engine id kind %q must be an upper-case letter of its own", cfg.DedupKind)
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	return &RoundEngine{cfg: cfg, sem: make(chan struct{}, roundWorkers), beatEvery: roundBeat,
		hw: make(map[RoundKey]uint32), known: make(map[roundKey]map[uint32]struct{})}, nil
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// SetGiveUp and SetBeatEvery override how long a rejected round is retried and how often a long
// walk tells the broker its sources are alive. For tests; call before publishing.
func (e *RoundEngine) SetGiveUp(d time.Duration)    { e.giveUp = d }
func (e *RoundEngine) SetBeatEvery(d time.Duration) { e.beatEvery = d }

// Wait blocks until every walk has finished. Call it after the writer is closed, so no new walk
// can start.
func (e *RoundEngine) Wait() { e.wg.Wait() }

func (e *RoundEngine) chunkMax() int {
	if e.cfg.ChunkMax != nil {
		if n := e.cfg.ChunkMax(); n > 0 && n <= eventenvelope.MaxMembers {
			return n
		}
	}
	return eventenvelope.MaxMembers
}

// Publish publishes ms (members of the bucket k) in index order, in chunks. Two members with the
// same index are one event (a source and its redelivery queued together) and settle together.
func (e *RoundEngine) Publish(ctx context.Context, k RoundKey, ms []RoundMember) {
	slices.SortStableFunc(ms, func(a, b RoundMember) int { return int(int64(a.Index) - int64(b.Index)) })
	merged := ms[:0]
	for _, m := range ms {
		if n := len(merged); n > 0 && merged[n-1].Index == m.Index {
			merged[n-1].Settle, merged[n-1].Beat = chainSettle(merged[n-1].Settle, m.Settle), chainBeat(merged[n-1].Beat, m.Beat)
			merged[n-1].Redelivered = merged[n-1].Redelivered || m.Redelivered
			continue
		}
		merged = append(merged, m)
	}
	ms = merged
	for len(ms) > 0 {
		n, size := 0, 0
		for n < len(ms) && n < e.chunkMax() && size+len(ms[n].Bytes) <= eventenvelope.MaxRawBytes {
			size += len(ms[n].Bytes)
			n++
		}
		if n == 0 {
			n = 1
		}
		e.publishChunk(ctx, k, ms[:n])
		ms = ms[n:]
	}
}

func chainSettle(a, b func(error)) func(error) {
	return func(err error) {
		if a != nil {
			a(err)
		}
		if b != nil {
			b(err)
		}
	}
}

func chainBeat(a, b func()) func() {
	if a == nil || b == nil {
		if a != nil {
			return a
		}
		return b
	}
	return func() { a(); b() }
}

func redelivered(ms []RoundMember) bool {
	for _, m := range ms {
		if m.Redelivered {
			return true
		}
	}
	return false
}

func (e *RoundEngine) publishChunk(ctx context.Context, k RoundKey, ms []RoundMember) {
	// 🔴 THE HIGH-WATER MARK IS ONLY FOR EVENTS NOBODY HAS PUBLISHED (see the type's comment).
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
func (e *RoundEngine) publishPieces(ctx context.Context, k RoundKey, ms []RoundMember, round uint32, redel bool) {
	msg, err := e.buildRound(k, round, ms)
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
	e.cfg.Writer.PublishAcked(core.WithTenant(ctx, k.Tenant), msg, func(ack PubAckInfo, err error) {
		switch {
		case err != nil:
			// Rejected, or the outcome unknown: the round is retried under the SAME id until it is
			// known. Moving on would leave a hole below whatever is stored above it.
			e.cfg.Metrics.countRetry()
			e.Retries.Add(1)
			e.startWalk(func(ctx context.Context) { e.walk(ctx, k, round, chunk, nil, true) })
		case ack.Duplicate:
			e.Duplicates.Add(1)
			e.cfg.Metrics.countDuplicate()
			e.raise(k, round)
			e.startWalk(func(ctx context.Context) { e.walk(ctx, k, round, chunk, &ack, false) })
		default:
			e.learn(k, round, chunk)
			e.settle(chunk, nil)
		}
	})
}

// buildRound builds the message for round of the bucket's members.
func (e *RoundEngine) buildRound(k RoundKey, round uint32, ms []RoundMember) (Message, error) {
	kind := e.cfg.DedupKind
	if k.Mapped {
		kind = e.cfg.MappedDedupKind
	}
	id := EnvelopeDedupID(kind, k.Tenant, k.Source(), round)
	if len(ms) == 1 {
		m := Message{
			Key:     ms[0].Key,
			Value:   ms[0].Bytes,
			DedupID: id,
			Headers: map[string]string{HeaderMemberIndex: strconv.FormatUint(uint64(ms[0].Index), 36)},
		}
		if ms[0].Correlation != "" {
			m = m.WithCorrelationID(ms[0].Correlation)
		}
		return m, nil
	}
	events := make([]eventenvelope.Event, len(ms))
	for i, m := range ms {
		events[i] = eventenvelope.Event{Index: m.Index, Bytes: m.Bytes}
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{
		Kind: e.cfg.Kind, Tenant: k.Tenant, SourceSeq: k.headerSeq(), Round: round,
	}, events)
	if err != nil {
		return Message{}, err
	}
	m := Message{
		Value:   body,
		DedupID: id,
		Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue},
	}
	if ms[0].Correlation != "" {
		m = m.WithCorrelationID(ms[0].Correlation)
	}
	return m, nil
}

// settle reports the outcome of publishing ms to each member's source.
func (e *RoundEngine) settle(ms []RoundMember, err error) {
	if err == nil {
		e.Envelopes.Add(1)
		e.Members.Add(int64(len(ms)))
		if len(ms) == 1 {
			e.Singles.Add(1)
		}
	}
	for _, m := range ms {
		if m.Settle != nil {
			m.Settle(err)
		}
	}
}

// settleOK settles members that a stored round was found to carry.
func (e *RoundEngine) settleOK(ms []RoundMember) {
	for _, m := range ms {
		if m.Settle != nil {
			m.Settle(nil)
		}
	}
}

// ---- shared per-bucket state ------------------------------------------------------------

func (e *RoundEngine) take(k RoundKey) uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.hw[k]
	e.hw[k] = r + 1
	e.trimLocked()
	return r
}

func (e *RoundEngine) raise(k RoundKey, round uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hw[k] < round+1 {
		e.hw[k] = round + 1
	}
	e.trimLocked()
}

func (e *RoundEngine) learn(k RoundKey, round uint32, ms []RoundMember) {
	set := make(map[uint32]struct{}, len(ms))
	for _, m := range ms {
		set[m.Index] = struct{}{}
	}
	e.remember(k, round, set)
}

func (e *RoundEngine) remember(k RoundKey, round uint32, set map[uint32]struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.known[roundKey{k, round}] = set
	if e.hw[k] < round+1 {
		e.hw[k] = round + 1
	}
	e.trimLocked()
}

func (e *RoundEngine) lookup(k RoundKey, round uint32) (map[uint32]struct{}, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.known[roundKey{k, round}]
	return s, ok
}

// trimLocked drops the state when it outgrows its bound: the cost is probes, never correctness.
func (e *RoundEngine) trimLocked() {
	if len(e.hw) > roundStateMax {
		clear(e.hw)
	}
	if len(e.known) > roundStateMax {
		clear(e.known)
	}
}

// skipKnown walks rounds from `from` while they are known to be stored, settling the members
// they carry, and returns the members still unpublished and the first round not known.
func (e *RoundEngine) skipKnown(k RoundKey, ms []RoundMember, from uint32) ([]RoundMember, uint32) {
	round := from
	for len(ms) > 0 {
		held, ok := e.lookup(k, round)
		if !ok {
			break
		}
		var done, rest []RoundMember
		for _, m := range ms {
			if _, in := held[m.Index]; in {
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
func (e *RoundEngine) startWalk(f func(ctx context.Context)) {
	e.wg.Add(1)
	select {
	case e.sem <- struct{}{}:
	case <-e.cfg.Ctx.Done():
		// Stopping: the walk is not run and its sources stay unacknowledged for the next pod.
		e.wg.Done()
		return
	}
	go func() {
		defer e.wg.Done()
		defer func() { <-e.sem }()
		f(e.cfg.Ctx)
	}()
}

func (e *RoundEngine) beat(ms []RoundMember) {
	for _, m := range ms {
		if m.Beat != nil {
			m.Beat()
		}
	}
}

var errRoundWalkStopped = errors.New("envelope: stopped before the round's outcome was known")

// walk settles ms starting at round: publish it (or, with dup set, take the answer already in
// hand), and while the broker says a round is already stored, read what it carries, settle those
// members and publish the rest as the next round. A round whose publish fails is published again
// under the same id (retry) until its outcome is known. Any unrecoverable failure fails every
// member still unsettled, so its source is redelivered.
func (e *RoundEngine) walk(ctx context.Context, k RoundKey, round uint32, ms []RoundMember, dup *PubAckInfo, retry bool) {
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
		var ack PubAckInfo
		if dup != nil {
			ack, dup = *dup, nil
		} else {
			ms, round = e.skipKnown(k, ms, round)
			if len(ms) == 0 {
				return
			}
			if round-start > maxRoundsPerBucket {
				e.settle(ms, fmt.Errorf("envelope for bucket %d needed more than %d rounds", k.Bucket, maxRoundsPerBucket))
				return
			}
			msg, err := e.buildRound(k, round, ms)
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
					e.Recovered.Add(int64(len(ms)))
				}
				e.learn(k, round, ms)
				e.settle(ms, nil)
				return
			}
			e.Duplicates.Add(1)
			e.cfg.Metrics.countDuplicate()
			ack = next
		}
		// The round is stored: read what it carries.
		held, err := e.heldOf(ctx, k, round, ack.Sequence)
		if err != nil {
			e.cfg.Metrics.countProbeFailure()
			log.Warn().Err(err).Str("tenant", k.Tenant).Uint64("bucket", k.Bucket).Uint32("round", round).
				Msg("Could not read the envelope a duplicate publish named; leaving its sources unacknowledged")
			e.settle(ms, err)
			return
		}
		var done, rest []RoundMember
		for _, m := range ms {
			if _, in := held[m.Index]; in {
				done = append(done, m)
			} else {
				rest = append(rest, m)
			}
		}
		e.settleOK(done)
		ms = rest
		round++
		e.cfg.Metrics.countRecoveryStep()
	}
}

// publishRound publishes msg synchronously, again under the same id while it fails, until the
// outcome is known, the walk is stopped, or giveUp fires (nil: never).
func (e *RoundEngine) publishRound(ctx context.Context, k RoundKey, msg Message, giveUp <-chan time.Time) (PubAckInfo, error) {
	backoff := 200 * time.Millisecond
	for {
		e.Probes.Add(1)
		rctx, cancel := context.WithTimeout(core.WithTenant(ctx, k.Tenant), roundTimeout)
		ack, err := e.cfg.Rounds.Publish(rctx, msg)
		cancel()
		if err == nil {
			return ack, nil
		}
		if giveUp == nil {
			return ack, err
		}
		select {
		case <-ctx.Done():
			return ack, errRoundWalkStopped
		case <-giveUp:
			return ack, err
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 5*time.Second)
		e.cfg.Metrics.countRetry()
		e.Retries.Add(1)
	}
}

// heldOf returns what the stored round carries: from the cache, or by reading the message at seq.
func (e *RoundEngine) heldOf(ctx context.Context, k RoundKey, round uint32, seq uint64) (map[uint32]struct{}, error) {
	if s, ok := e.lookup(k, round); ok {
		return s, nil
	}
	e.Probes.Add(1)
	s, err := e.Carried(ctx, k.Tenant, seq)
	if err != nil {
		return nil, err
	}
	e.remember(k, round, s)
	return s, nil
}

// errNotAnEnvelope marks a stored message under an envelope id that is neither an envelope nor
// a member published alone: the id's protocol cannot be followed, so nothing is assumed.
var errNotAnEnvelope = errors.New("the stored message is neither an envelope nor a single member")

// Carried returns the source indices the stored message at seq settled: its members and the
// indices it says were settled elsewhere. Anything that does not parse as exactly that is an
// error - never an empty set, which would republish what is already stored.
func (e *RoundEngine) Carried(ctx context.Context, tenant string, seq uint64) (map[uint32]struct{}, error) {
	ctx, cancel := context.WithTimeout(ctx, roundTimeout)
	defer cancel()
	stored, err := e.cfg.Rounds.Get(ctx, tenant, seq)
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
		if r.Header().Kind != e.cfg.Kind {
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
	mi := stored.Headers[HeaderMemberIndex]
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

func (e *RoundEngine) giveUpAfter() time.Duration {
	if e.giveUp > 0 {
		return e.giveUp
	}
	return roundGiveUp
}
