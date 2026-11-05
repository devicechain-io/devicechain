// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
)

// The processor's side of a concurrent engine (detectShards above 1): the loop queues messages for
// the shards and does not wait for them, so a few things the single engine did inline have to be
// done at a defined point instead.
//
//   - A detection is stamped with the platform time of the message that caused it when it is MADE,
//     by the shard, from a tag that travels with the message (tagNext), not when the loop drains.
//   - The loop takes the shards' detections in (collectDetections) on each tick, without waiting,
//     so ready ones can publish early; and (syncDetector) at every checkpoint, after waiting for
//     every queued message, so the snapshot never covers a message whose detections are not buffered.
//   - The shard goroutines belong to the term: they are stopped wherever the engine is replaced
//     or the term ends (closeDetector).
//
// None of this exists for the plain engine, which has no goroutines to wait for and drains inline.

// observed hands the shard goroutines' metrics sink to a concurrent engine.
func (rp *ResolvedEventsProcessor) observed(c *detectcore.ConcurrentSharded) *detectcore.ConcurrentSharded {
	rp.metrics.bindShards(c.Shards())
	c.SetObserver(rp.metrics)
	return c
}

// pipelined is the live engine when its shards run concurrently, and nil for the plain engine.
func (rp *ResolvedEventsProcessor) pipelined() detectcore.Pipelined {
	p, _ := rp.engine.(detectcore.Pipelined)
	return p
}

// closeDetector stops the shard goroutines of the live engine, if it has any. It is called wherever
// the engine is replaced or its term ends, so no shard outlives the state it belongs to.
func (rp *ResolvedEventsProcessor) closeDetector() {
	if p := rp.pipelined(); p != nil {
		p.Close()
	}
}

// tagNext stamps the next operation sent to a concurrent engine with the platform time of the input
// that causes it, and whether that input is replayable.
func (rp *ResolvedEventsProcessor) tagNext(at time.Time, replayable bool) {
	if p := rp.pipelined(); p != nil {
		p.Tag(at, replayable)
	}
}

// syncDetector waits for the shards of a concurrent engine and takes in every detection they have
// made. A checkpoint calls it before anything is published or saved.
func (rp *ResolvedEventsProcessor) syncDetector() {
	if p := rp.pipelined(); p != nil {
		p.Sync()
		rp.absorb(p.Collect())
	}
}

// collectDetections takes in the detections the shards have finished, without waiting for any shard.
func (rp *ResolvedEventsProcessor) collectDetections() {
	if p := rp.pipelined(); p != nil {
		rp.absorb(p.Collect())
	}
}

// sampleShardLoad publishes each shard's queue depth and busy time. It runs before anything on the
// tick that waits for the shards, which would otherwise always find the queues empty.
func (rp *ResolvedEventsProcessor) sampleShardLoad() {
	if p := rp.pipelined(); p != nil {
		rp.metrics.sampleShards(p.ShardStats())
	}
}

// absorb buffers detections collected from a concurrent engine, each under the stamp it was made
// with.
//
// KNOWN DIFFERENCE FROM THE SINGLE ENGINE, accepted and pinned by a test. A detection made by an
// operation with no message behind it (a dead-man disarm resolves at the watermark) has no stamp.
// With the single engine it takes the platform time of the next drain, which is the next message's
// or advance's; here it takes the loop's clock when it is taken in. That time only meters REACT's
// outbound budget, it is not part of the detection's identity (its DedupID does not read it), and
// such a detection is never published early, so replays and duplicates collapse exactly as before.
func (rp *ResolvedEventsProcessor) absorb(dets []detectcore.TaggedDetection) int {
	var now time.Time
	for _, td := range dets {
		at := td.Tag.At
		if at.IsZero() {
			if now.IsZero() {
				now = rp.clock.Now()
			}
			at = now
		}
		rp.detOrd++
		rp.pendingDets = append(rp.pendingDets, pendingDetection{
			Detection: td.Detection, triggeredAt: at, ord: rp.detOrd,
			early: td.Tag.Replayable && rp.earlyPublishable(td.Detection),
		})
	}
	if len(dets) > 0 {
		rp.dirty = true
	}
	return len(dets)
}
