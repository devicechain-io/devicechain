// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

// The checkpoint's publish phase: every buffered detection delivered to the broker before the
// snapshot that covers it is saved.
//
// A detection is published in three steps, and only the middle one leaves the loop:
//
//  1. PREPARE, on the loop. The supersession gate (dropSupersededDetections), the registry
//     lookup, the tenant backstop, the payload and its dedup id. The result is an immutable
//     runtime.PreparedOutput; a refusal is a terminal disposition, counted and dead-lettered
//     there, and never sent.
//  2. SEND, off the loop when more than one publish may be outstanding. A worker writes the
//     prepared bytes and waits for the broker's acknowledgement. It reads nothing but the
//     prepared value and posts an immutable outcome back; it never touches the registry, the
//     engine, the buffer or the rule-health recorder.
//  3. SETTLE, on the loop. The loop applies each outcome, lets the series' next detection go,
//     retires the contiguous prefix of the buffer that is settled, and records each retired
//     detection's rule-health fire in buffer order.
//
// ORDER. A lane is (owning tenant, series): all of a device's detections, across every rule and
// every profile version, or all of a correlation anchor's. At most one publish per lane is
// unacknowledged at a time, and a lane's next detection is sent only after its predecessor was
// POSITIVELY acknowledged (or was a terminal drop). That is stronger than settling in order: a
// failed earlier publish followed by a successful later one would already be an inversion in the
// stream. Different lanes may interleave; downstream state is keyed by series (an alarm is filed
// under its device and folds its contributors' edges by event time), so nothing ordered spans two
// lanes. With a window of 1 the publishes go in buffer order exactly, which is the serial path.
//
// MEMORY. maxBufferedBytes bounds the payload bytes the buffer holds for publishing: every payload
// that is prepared and not yet positively acknowledged, whether it is in flight, waiting for room,
// or kept from an earlier pass for a retry (a failed or unknown item keeps its bytes and dedup id).
// A new pass starts with the retained payloads already counted. An acknowledged payload is released
// at its acknowledgement, so a settled-but-not-retired item holds its identity and no bytes.
// Preparation is lazy (one item at a time, only when the window has room) and stops while the
// count is over the bound and anything is in flight, so the count does not creep with mixed sizes:
// the item that crossed the bound waits for room, and nothing is prepared behind it. With nothing
// in flight the next item is prepared and sent whatever the count (that is how a payload larger
// than the whole bound gets out, alone), so payloads already held there (one waiting for room, or
// ones kept from a failed pass) can carry the count over the bound by one payload more. A retired
// entry is zeroed out of the buffer's backing array, not merely sliced off.
//
// FAILURE. The first failed or unknown outcome (a timeout may have been stored) stops further
// sends; what is already in flight settles under the same deadline. Items are judged by their
// place in the buffer, not by when their outcome arrived: the settled prefix is retired, and
// everything after it stays buffered — a failed item keeps its prepared bytes and dedup id for the
// retry, an acknowledged one is marked so it is never sent again, an unsent one is untouched. The
// checkpoint is deferred until a later pass settles the whole buffer, and a snapshot is saved
// only then.
//
// LIFETIME. A pass's workers, channels and outcomes belong to that pass. It does not return
// until every publish it sent has an outcome — each is bounded by the checkpoint's own deadline,
// which the writer enforces — and it joins its workers before returning, so no worker, channel
// or late outcome outlives the checkpoint that started it, let alone the term.

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// outputState is what a buffered detection's publishing has reached, carried across passes.
type outputState uint8

const (
	// outputNew: not prepared yet.
	outputNew outputState = iota
	// outputPrepared: prepared, and an attempt failed or its outcome is unknown. A retry sends
	// the SAME prepared bytes and dedup id.
	outputPrepared
	// outputAcked: positively acknowledged in this term but not yet retired, because something
	// before it in the buffer is not settled. Never sent again; its fire is recorded when the
	// prefix before it settles.
	outputAcked
)

// itemStatus is one buffered detection's status within one pass.
type itemStatus uint8

const (
	itemWaiting  itemStatus = iota // not sent in this pass (yet)
	itemChecked                    // prepared or rechecked in this pass, waiting for room
	itemInFlight                   // sent, outcome not back
	itemAcked                      // positively acknowledged (in this pass or an earlier one)
	itemDropped                    // terminal disposition: never sent, leaves the buffer
	itemFailed                     // failed or unknown outcome in this pass
)

// Outcome labels for detect_derived_publish_failures_total. A closed set.
const (
	publishOutcomeFailed  = "failed"
	publishOutcomeUnknown = "unknown"
)

var publishFailureOutcomes = []string{publishOutcomeFailed, publishOutcomeUnknown}

// outputConfig is the configured publish window, or the defaults when none was given.
func (rp *ResolvedEventsProcessor) outputConfig() runtime.OutputConfig {
	if rp.cfg.Output == (runtime.OutputConfig{}) {
		return runtime.DefaultOutputConfig()
	}
	return rp.cfg.Output
}

// publishJob is one prepared output handed to a worker. Immutable.
type publishJob struct {
	ord int32
	out runtime.PreparedOutput
}

// publishOutcome is one send's result, posted back to the loop. Immutable.
type publishOutcome struct {
	pass uint64
	ord  int32
	wait time.Duration
	err  error
}

// ordHeap is the set of ready items, smallest buffer position first: each lane contributes at
// most its next item, so a busy lane holds at most one slot of the window and cannot starve the
// others, and with one slot the order is the buffer's.
type ordHeap []int32

func (h ordHeap) Len() int           { return len(h) }
func (h ordHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h ordHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *ordHeap) Push(x any)        { *h = append(*h, x.(int32)) }
func (h *ordHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

type laneKey struct{ tenant, series string }

// publishPass is one run of publishPending. Loop-owned except for the two channels.
type publishPass struct {
	rp  *ResolvedEventsProcessor
	pub *runtime.Publisher
	ctx context.Context
	id  uint64
	cfg runtime.OutputConfig

	status []itemStatus
	next   []int32 // the next item in the same lane, -1 for none
	ready  ordHeap

	inFlight int
	bytes    int64 // payload bytes of the publishes in flight (the outstanding-bytes metric)
	// held is the byte budget's count: the payload bytes of every item that is prepared and not
	// positively acknowledged, in flight or not.
	held      int64
	cursor    int // items before it are retired
	stopped   bool
	leaseLost bool
	firstErr  error

	// The workers, when the window is above 1. With a window of 1 the one send runs on the loop
	// inside await, and inline holds it until then.
	jobs    chan publishJob
	results chan publishOutcome
	workers sync.WaitGroup
	inline  *publishJob

	windowFull time.Duration
	laneWaits  int
	stale      int
}

// publishPending durably delivers every buffered detection on its owning tenant's derived-event
// subject (see the file comment). It returns true when the whole buffer is retired: every
// detection positively acknowledged or terminally dropped (an empty buffer counts). On false the
// caller must defer the checkpoint; the buffer keeps what is not settled, as described above.
func (rp *ResolvedEventsProcessor) publishPending(ctx context.Context) bool {
	rp.dropSupersededDetections()
	n := len(rp.pendingDets)
	if n == 0 {
		return true
	}
	start := time.Now()
	rp.publishPasses++
	p := rp.newPublishPass(ctx, n)
	if p.cfg.MaxInFlight > 1 {
		workers := min(p.cfg.MaxInFlight, n)
		p.jobs = make(chan publishJob, p.cfg.MaxInFlight)
		// Every outcome fits without a reader: at most MaxInFlight are ever outstanding. A worker
		// therefore never blocks on the way out, even if the loop has stopped reading.
		p.results = make(chan publishOutcome, p.cfg.MaxInFlight)
		p.workers.Add(workers)
		for range workers {
			go p.work()
		}
	}
	for {
		p.admit()
		if p.inFlight == 0 {
			break
		}
		p.await()
	}
	if p.jobs != nil {
		close(p.jobs)
		p.workers.Wait()
	}
	ok := p.finish()
	rp.metrics.recordPublishPass(time.Since(start), p.windowFull, p.laneWaits, p.stale)
	return ok
}

// newPublishPass sets up a pass over the n buffered detections, counting against the byte budget
// the payloads already retained from earlier passes.
func (rp *ResolvedEventsProcessor) newPublishPass(ctx context.Context, n int) *publishPass {
	p := &publishPass{rp: rp, pub: rp.publisher, ctx: ctx, id: rp.publishPasses, cfg: rp.outputConfig(),
		status: make([]itemStatus, n), next: make([]int32, n)}
	for i := range rp.pendingDets {
		if rp.pendingDets[i].state == outputPrepared {
			p.held += int64(len(rp.pendingDets[i].out.Payload))
		}
	}
	p.buildLanes()
	return p
}

// buildLanes links every item to the next one in its lane and makes each lane's first item ready.
// With a window of 1 the whole buffer is one lane, so the order is the buffer's and no map is built.
func (p *publishPass) buildLanes() {
	n := len(p.next)
	if p.cfg.MaxInFlight == 1 {
		for i := range n - 1 {
			p.next[i] = int32(i + 1)
		}
		p.next[n-1] = -1
		p.ready = ordHeap{0}
		return
	}
	last := make(map[laneKey]int32)
	for i := range n {
		p.next[i] = -1
		d := &p.rp.pendingDets[i]
		// The lane's tenant is the rule id's. For every detection that is actually sent it equals
		// the owning tenant (the backstop refuses the rest), so the two cannot disagree about an
		// output that reaches the broker.
		tenant, _ := runtime.RuleTenant(d.RuleID)
		k := laneKey{tenant: tenant, series: d.Series}
		if j, ok := last[k]; ok {
			p.next[j] = int32(i)
		} else {
			p.ready = append(p.ready, int32(i)) // ascending, so already a heap
		}
		last[k] = int32(i)
	}
}

// admit sends ready items while the window and the byte budget have room.
func (p *publishPass) admit() {
	rp := p.rp
	for !p.stopped && p.inFlight < p.cfg.MaxInFlight && len(p.ready) > 0 {
		if rp.leadershipEnabled() && !rp.heldNow() {
			// Lost the partition mid-pass: send nothing more. What is in flight cannot be
			// recalled; it settles, and anything stored is the successor's replay to collapse.
			p.stopped, p.leaseLost = true, true
			return
		}
		i := p.ready[0]
		d := &rp.pendingDets[i]
		if p.status[i] == itemWaiting {
			switch d.state {
			case outputAcked:
				heap.Pop(&p.ready)
				p.status[i] = itemAcked
				p.advanceLane(i)
				p.retire()
				continue
			case outputNew:
				if p.inFlight > 0 && p.held > p.cfg.MaxBufferedBytes {
					// Already over the budget: prepare nothing more until something settles. An
					// acknowledgement can promote a lower-numbered item of another lane to the top,
					// and preparing that one too would let the count creep with mixed sizes.
					return
				}
				out, ok := p.pub.Prepare(d.Detection, d.triggeredAt)
				if !ok {
					p.drop(i)
					continue
				}
				d.out, d.state = out, outputPrepared
				p.held += int64(len(out.Payload))
			case outputPrepared:
				// A retry: the same bytes, unless the registry no longer lets this detection out.
				if !p.pub.Recheck(d.Detection, d.out) {
					p.held -= int64(len(d.out.Payload))
					p.drop(i)
					continue
				}
			}
			p.status[i] = itemChecked
		}
		size := int64(len(d.out.Payload))
		if p.inFlight > 0 && p.held > p.cfg.MaxBufferedBytes {
			// No room until something settles (held already counts this item). An item larger
			// than the whole budget is sent when nothing else is outstanding, and nothing is
			// sent beside it.
			return
		}
		heap.Pop(&p.ready)
		p.status[i] = itemInFlight
		p.inFlight++
		p.bytes += size
		rp.metrics.setPublishOutstanding(p.inFlight, p.bytes)
		job := publishJob{ord: i, out: d.out}
		if p.jobs != nil {
			p.jobs <- job // never blocks: inFlight <= MaxInFlight == cap
		} else {
			p.inline = &job
		}
	}
}

// drop retires a terminal disposition from the ready set: it is never sent, and its lane's next
// item may go.
func (p *publishPass) drop(i int32) {
	heap.Pop(&p.ready)
	p.status[i] = itemDropped
	p.advanceLane(i)
	p.retire()
}

func (p *publishPass) advanceLane(i int32) {
	if j := p.next[i]; j >= 0 {
		heap.Push(&p.ready, j)
		if p.cfg.MaxInFlight > 1 {
			p.laneWaits++
		}
	}
}

// work is one worker: it sends what it is given and posts the outcome. Nothing else.
func (p *publishPass) work() {
	defer p.workers.Done()
	for job := range p.jobs {
		wait, err := p.pub.Send(p.ctx, job.out)
		p.results <- publishOutcome{pass: p.id, ord: job.ord, wait: wait, err: err}
	}
}

// await takes one outcome and applies it.
func (p *publishPass) await() {
	// Waiting while something else was ready to go is time the window (or the byte budget) was
	// full: the cost a wider window would remove. A detection whose own lane is busy is not ready;
	// that wait is the lane's, and no window removes it. At window 1 the whole buffer is one lane,
	// so there "ready" is simply "not the last".
	blocked := !p.stopped && len(p.ready) > 0
	start := time.Now()
	var o publishOutcome
	if p.jobs != nil {
		o = <-p.results
	} else {
		job := *p.inline
		p.inline = nil
		blocked = !p.stopped && p.next[job.ord] >= 0
		wait, err := p.pub.Send(p.ctx, job.out)
		o = publishOutcome{pass: p.id, ord: job.ord, wait: wait, err: err}
	}
	if blocked {
		p.windowFull += time.Since(start)
	}
	p.apply(o)
}

// apply settles one outcome on the loop.
func (p *publishPass) apply(o publishOutcome) {
	if o.pass != p.id || o.ord < 0 || int(o.ord) >= len(p.status) || p.status[o.ord] != itemInFlight {
		// Not an outstanding publish of this pass: refused, never applied. Structurally this
		// cannot happen (the channel belongs to this pass and every item is sent once), so it is
		// counted rather than trusted.
		p.stale++
		return
	}
	d := &p.rp.pendingDets[o.ord]
	p.inFlight--
	p.bytes -= int64(len(d.out.Payload))
	p.rp.metrics.setPublishOutstanding(p.inFlight, p.bytes)
	if o.err != nil {
		p.status[o.ord] = itemFailed
		p.rp.metrics.recordPublishFailure(publishFailureOutcome(o.err))
		if p.firstErr == nil {
			p.firstErr = o.err
		}
		p.stopped = true
		return
	}
	p.status[o.ord] = itemAcked
	d.state = outputAcked
	// A known positive is kept only so its fire is recorded in order once the items before it
	// settle, and that needs the identity, not the bytes. Releasing them here is what keeps the
	// byte budget a bound on everything held: an acknowledged-but-unretired payload holds nothing.
	p.held -= int64(len(d.out.Payload))
	d.out.Payload = nil
	p.pub.Published(o.wait)
	p.advanceLane(o.ord)
	p.retire()
}

// retire moves the cursor over the settled prefix, recording each acknowledged item's rule-health
// fire in buffer order.
func (p *publishPass) retire() {
	for p.cursor < len(p.status) {
		switch p.status[p.cursor] {
		case itemAcked:
			p.pub.RecordFire(p.ctx, p.rp.pendingDets[p.cursor].out)
		case itemDropped:
		default:
			return
		}
		p.cursor++
	}
}

// finish compacts the buffer to what is not retired and reports whether nothing is left.
func (p *publishPass) finish() bool {
	rp := p.rp
	if p.firstErr != nil {
		log.Error().Err(p.firstErr).Msg("Failed to publish a derived event; deferring checkpoint (will retry).")
		rp.metrics.recordCheckpointFailure(checkpointStagePublish)
	}
	if p.leaseLost {
		log.Warn().Str("partition", rp.cfg.PartitionId).
			Msg("DETECT lost the partition while publishing a checkpoint's detections; the rest are not sent and the messages stay unacked")
	}
	old := len(rp.pendingDets)
	if p.cursor == len(p.status) {
		// Zero the retired entries, not just the length: the backing array outlives this pass,
		// and a slot past the length still reaches its prepared payload.
		clear(rp.pendingDets[:old])
		rp.pendingDets = rp.pendingDets[:0]
		return !p.leaseLost
	}
	kept := rp.pendingDets[:0]
	for i := p.cursor; i < len(p.status); i++ {
		d := rp.pendingDets[i]
		switch p.status[i] {
		case itemDropped:
			continue
		}
		kept = append(kept, d)
	}
	// kept shares the backing array: zero what compaction left behind the new length (the retired
	// prefix's tail and the dropped entries), so their payloads are collectable.
	clear(rp.pendingDets[len(kept):old])
	rp.pendingDets = kept
	return false
}

// jsErrCodeDuplicateMsgIdInProcess is JetStream's answer when a publish's Nats-Msg-Id is still
// being replicated: another publish under the same id is in progress and its fate is not known.
// Seen here when the publisher's own re-send is still racing its original at the deadline.
const jsErrCodeDuplicateMsgIdInProcess nats.ErrorCode = 10158

// publishFailureOutcome tells a publish that may have been stored (the outcome is unknown: a
// timeout or cancellation, a connection lost before the acknowledgement arrived, or an id the
// broker says is still in process) from one the broker refused. Both are retried the same way,
// with the same bytes and dedup id; the split is for the operator.
func publishFailureOutcome(err error) string {
	var apiErr *nats.APIError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.Is(err, nats.ErrTimeout),
		errors.Is(err, nats.ErrConnectionClosed), errors.Is(err, nats.ErrDisconnected),
		errors.Is(err, nats.ErrConnectionDraining),
		errors.As(err, &apiErr) && apiErr.ErrorCode == jsErrCodeDuplicateMsgIdInProcess:
		return publishOutcomeUnknown
	}
	return publishOutcomeFailed
}
