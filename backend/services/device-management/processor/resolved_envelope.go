// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
)

// Resolved envelopes (infrastructure.messaging.envelopes.resolved): k resolved events of one
// tenant in one message on resolved-events, in place of k messages.
//
// 🔴 DO NOT ENABLE THE FLAG before event-processing's per-member DETECT guard (E4) and these
// fixes ship together: a duplicate that escapes the protocol is applied twice by DETECT.
//
// The settlement protocol - the id that names (source, round) and never the members, the
// duplicate PubAck that names the stored original, the leader read-back, publishing only the
// complement as the next round, the same-id retry of a rejected round, the encoder-refusal split
// - is messaging.RoundEngine's, which event-sources' capture hop runs too. This file decides only
// WHICH events form an envelope and under which key; the engine publishes and settles them.
//
// # What forms an envelope
//
//   - An event resolved from a member of an INBOUND ENVELOPE (or from a copy split out of one) is
//     mapped 1:1: the events of one inbound envelope are the members of one resolved envelope, in
//     member order, under the id R:<tenant>:<inbound envelope seq b36>:<round b36> (RoundKey.Mapped).
//     The source is one immutable message, so the id can never regroup. A member that failed or was
//     split out simply has no event in it. Members the resolver pool finishes after the collection
//     window close go as the NEXT round of the same id, which the protocol makes exact, so "one
//     envelope per inbound envelope" is the common case and never a correctness condition.
//   - Events from LEGACY inbound messages (the interim grouping): one tenant's events from one
//     BUCKET - 256 inbound sequences x 30 s of broker append time - under G:<tenant>:<bucket>:<round>.
//   - An event with no source sequence, or past the fan-out slots a member index can name, or
//     larger than an envelope can hold, is published in the legacy shape exactly as with the flag
//     off. A groupable event is NEVER published under a per-event id.
const (
	// bucketSize and bucketSeconds are the shared engine's bucket (messaging.RoundBucketSize and
	// RoundBucketSeconds): the stream's duplicate window must cover a bucket's span plus a source's
	// republish span.
	bucketSize    = messaging.RoundBucketSize
	bucketSeconds = messaging.RoundBucketSeconds
	// fanSlots is how many resolved events one source can have in a member index. A source
	// fanning out to more publishes its extra events in the legacy shape.
	fanSlots = 64
	// collectLinger bounds how long the envelope submitter waits for more events to group after
	// the first. Device-management cannot see a fetch's boundary past the reader, and without a
	// wait the submitter, which is much faster than the resolvers, sees one event at a time.
	collectLinger = 2 * time.Millisecond
	// collectMax bounds the events one collection holds.
	collectMax = 256
	// mappedKind is the id kind letter of an envelope mapped 1:1 from an inbound envelope.
	mappedKind = "R"
)

// bucketKey names the group an event belongs to.
type bucketKey = messaging.RoundKey

// envelopeEngine groups resolved events and publishes the groups on the shared engine.
// publishBatch is called by ONE goroutine, the processor's resolved loop.
type envelopeEngine struct {
	*messaging.RoundEngine
	iproc *InboundEventsProcessor

	// legacy counts events published exactly as with the flag off (for tests and logs).
	legacy atomic.Int64
}

func newEnvelopeEngine(ctx context.Context, iproc *InboundEventsProcessor, w messaging.AckedWriter, rp messaging.RoundPublisher) *envelopeEngine {
	eng, err := messaging.NewRoundEngine(messaging.RoundEngineConfig{
		Kind:            eventenvelope.KindResolved,
		DedupKind:       messaging.EnvelopeDedupKind,
		MappedDedupKind: mappedKind,
		Writer:          w,
		Rounds:          rp,
		Ctx:             ctx,
		ChunkMax:        func() int { return iproc.envelopeChunkMax },
		Metrics: messaging.NewEnvelopeCounters(iproc.metrics.envDuplicates, iproc.metrics.envRecoverySteps,
			iproc.metrics.envProbeFailures, iproc.metrics.envRetries),
	})
	if err != nil {
		// The kinds above are constants and the writer and publisher were checked by the caller.
		panic("device-management: resolved envelope engine: " + err.Error())
	}
	return &envelopeEngine{RoundEngine: eng, iproc: iproc}
}

// memberIndex names a resolved event within its group: the group's key and the event's place in
// it. ok is false for an event that cannot be grouped.
func memberIndex(item resolvedItem, size int) (k bucketKey, index uint32, ok bool) {
	if item.coord == nil || item.index < 0 || item.index >= fanSlots || size > eventenvelope.MaxRawBytes {
		return k, 0, false
	}
	src := item.coord.src
	if src.StreamSeq == 0 {
		return k, 0, false
	}
	// A member of an inbound envelope, or a copy split out of one, is named by the coordinate it
	// came from: the origin envelope's sequence and the member's place in it.
	if seq, member, named := src.SourceCoordinate(); named {
		if seq == 0 || member >= 1<<20/fanSlots {
			return k, 0, false
		}
		return bucketKey{Tenant: item.tenant, Bucket: seq, Mapped: true}, member*fanSlots + uint32(item.index), true
	}
	k = bucketKey{Tenant: item.tenant, Bucket: src.StreamSeq / bucketSize}
	if !src.AppendTime.IsZero() {
		k.Epoch = uint64(src.AppendTime.Unix() / bucketSeconds)
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

// publishBatch groups the batch by key and hands each group to the engine.
func (e *envelopeEngine) publishBatch(ctx context.Context, batch []resolvedItem) {
	groups := map[bucketKey][]messaging.RoundMember{}
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
		coord := item.coord
		groups[k] = append(groups[k], messaging.RoundMember{
			Index:       idx,
			Bytes:       bytes,
			Key:         []byte(item.event.SourceDeviceToken),
			Correlation: item.correlation,
			Redelivered: coord.src.NumDelivered > 1,
			Settle:      func(err error) { e.iproc.settleResolved(coord, err) },
			Beat:        func() { _ = coord.src.InProgress() },
		})
	}
	for _, k := range order {
		// The engine puts the members in source order, not completion order, and folds a source
		// and its redelivery queued together into one member that settles both.
		e.Publish(ctx, k, groups[k])
	}
}
