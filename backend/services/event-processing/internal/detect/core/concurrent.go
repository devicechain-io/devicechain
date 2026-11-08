// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ConcurrentSharded is Sharded with a goroutine per shard.
//
// It answers every method of Detector exactly as one engine would, and its snapshot is the single
// engine's bytes. What changes against Sharded is WHEN the work happens, never WHAT it is.
//
// THE MODEL. One goroutine (the dispatcher: the processor's single-writer loop) drives the type.
// Each shard owns one Engine and one FIFO queue, and is the only goroutine that touches that
// Engine while it is running. The dispatcher sends operations down the queues and does not wait
// for them. Four kinds of call exist:
//
//   - hot path, in band, no wait: ProcessReceived, ProcessReceivedMember (one item per message to
//     EVERY shard, empty ones included: that tick is what keeps every shard's watermark and
//     position equal), Skip (to every shard), and the per-key SetExpected, RemoveExpected and
//     ClearRaised (to the owning shard). Each shard applies its queue in order, so a shard sees
//     the same operations in the same order as the single engine. Descope is in band too, but the
//     dispatcher waits for its answer from the owner;
//   - barrier: everything that reads or changes state across shards (a snapshot, a rule upsert,
//     Advance, every query). The dispatcher sends a barrier down every queue and waits for all of
//     them, so every shard has applied everything sent before the call and is idle; the call then
//     runs on the dispatcher, directly on the shard engines, exactly as Sharded does;
//   - mirrored: LastSeq, which the loop reads on every message, is held by the dispatcher itself
//     (the engines' rule for it is three lines and it is checked against every shard at every
//     snapshot), so a read does not wait for the queues;
//   - telemetry: DrainLateSamples sums atomics the shards keep, and Collect hands over the
//     detections the shards have produced so far, neither waiting.
//
// DETECTIONS. A shard drains its engine after every operation and keeps the detections, each with
// the Tag the operation was sent under (the platform time of the message that caused it) and the
// operation's number and its place inside the operation (ord.go). Collect and Drain return them
// ordered by (operation number, place, shard), which is exactly the sequence the single engine emits
// them in, across series as well as within one. Detections made by a barrier call are collected
// right after it.
//
// WHY THIS CANNOT CHANGE A RESULT. A key's state changes only through the operations addressed to
// it, applied in queue order by its one owner, against a watermark that is the same function of the
// same prefix in every shard. Every cross-shard read happens at a barrier, where all queues are
// drained. A shard that diverged (a lost tick, a lost Skip) is caught at the next snapshot, which
// refuses to merge shards that are not at one watermark and one position and one that is not at the
// dispatcher's mirror, and counts it.
//
// LIFETIME. NewConcurrentSharded starts the goroutines; Close stops and joins them, after which the
// engine keeps working but runs every operation on the caller, as Sharded does. A shard that panics takes the process with it, as a panic in the
// single loop would; no snapshot is ever taken from a partial set of shards.
type ConcurrentSharded struct {
	s       *Sharded // the shard engines; each is touched only by its worker, or by the dispatcher at a barrier
	workers []*shardWorker
	obs     PipelineObserver

	// The dispatcher's own state. Single goroutine, no locking.
	lastSeq uint64   // mirror of every shard's LastSeq
	sent    []uint64 // per shard, the number of the last operation queued to it
	item    uint64   // number of the last operation sent, the merge key for detections
	tag     Tag      // the tag for the next operation that carries one, then reset
	closed  bool

	evPool sync.Pool // *[]Event, the per-shard event batches handed to the workers
	wg     sync.WaitGroup
}

// Tag is what a detection is stamped with when it is made: the platform time of the input that
// caused it and whether that input came from the replayable log (true) or from the wall clock
// (false). A zero At is untagged.
type Tag struct {
	At         time.Time
	Replayable bool
}

// TaggedDetection is a detection with its Tag.
type TaggedDetection struct {
	Detection
	Tag Tag
}

// PipelineObserver receives what only the concurrent engine can see. A nil observer is allowed.
type PipelineObserver interface {
	// BarrierWait is how long the dispatcher waited for every shard to drain its queue.
	BarrierWait(d time.Duration)
	// InvariantViolation is called when the shards disagreed on the watermark or position, or
	// with the dispatcher on the position, at a snapshot.
	InvariantViolation()
}

// Pipelined is the engine as the live loop drives it when the shards run concurrently: a Detector
// plus the calls that let the loop stamp and collect detections without a barrier per message.
type Pipelined interface {
	Detector
	// Tag stamps the next message, advance or other tagged operation.
	Tag(at time.Time, replayable bool)
	// Sync waits until every shard has applied everything sent so far.
	Sync()
	// Collect returns the detections shards have made so far without waiting, in merge order.
	Collect() []TaggedDetection
	// ShardStats reports each shard's queue depth and cumulative busy time without waiting.
	ShardStats() []ShardStat
	// ShardLiveKeys reports each shard's live keys. It is a barrier.
	ShardLiveKeys() []int
	// SetObserver installs the observer (before the first operation).
	SetObserver(o PipelineObserver)
	// Close stops the shard goroutines and waits for them. It is idempotent.
	Close()
}

// ShardStat is one shard's load.
type ShardStat struct {
	QueueDepth int
	BusyNanos  uint64
}

var (
	_ Detector  = (*ConcurrentSharded)(nil)
	_ Pipelined = (*ConcurrentSharded)(nil)
)

// ShardQueueDepth is how many operations a shard's queue holds before the dispatcher blocks. It
// bounds memory (K queues of this many messages) and is the back-pressure: a slow shard slows the
// loop rather than growing without limit.
const ShardQueueDepth = 1024

type opKind uint8

const (
	opMsg opKind = iota
	opMember
	opSkip
	opFn
	opBarrier
)

type shardOp struct {
	kind opKind
	seq  uint64
	t    time.Time
	r    Receipt
	evs  *[]Event
	item uint64
	tag  Tag
	fn   func(e *Engine)
	done bool // signal w.done after applying
}

type taggedOut struct {
	item  uint64
	shard int
	TaggedDetection
}

type shardWorker struct {
	idx  int
	e    *Engine
	ch   chan shardOp
	done chan struct{} // capacity 1; one outstanding barrier or sync call per worker
	c    *ConcurrentSharded

	mu  sync.Mutex
	out []taggedOut

	late atomic.Uint64
	busy atomic.Uint64
	// doneItem is the number of the last operation this shard has finished applying and harvesting.
	doneItem atomic.Uint64
}

// NewConcurrentSharded builds an empty K-way engine with a goroutine per shard. K is clamped to
// [1, MaxShards] and allowedLateness has the meaning it has for NewEngine.
func NewConcurrentSharded(k int, rules []Rule, allowedLateness time.Duration) *ConcurrentSharded {
	return wrapConcurrent(newSharded(k, 0, rules, allowedLateness))
}

// RestoreConcurrentSharded rebuilds a K-way concurrent engine from a snapshot written by an engine
// of ANY shard count, or by the plain Engine.
func RestoreConcurrentSharded(k int, rules []Rule, allowedLateness time.Duration, data []byte) (*ConcurrentSharded, error) {
	s, err := restoreSharded(k, 0, rules, allowedLateness, data)
	if err != nil {
		return nil, err
	}
	return wrapConcurrent(s), nil
}

func wrapConcurrent(s *Sharded) *ConcurrentSharded {
	c := &ConcurrentSharded{s: s, lastSeq: s.shards[0].LastSeq(), sent: make([]uint64, len(s.shards))}
	c.evPool.New = func() any { b := make([]Event, 0, 8); return &b }
	c.workers = make([]*shardWorker, len(s.shards))
	for i, e := range s.shards {
		e.trackOrd = true
		w := &shardWorker{idx: i, e: e, ch: make(chan shardOp, ShardQueueDepth), done: make(chan struct{}, 1), c: c}
		c.workers[i] = w
		c.wg.Add(1)
		go w.run()
	}
	return c
}

// SetObserver installs the observer. It must be called before the first operation.
func (c *ConcurrentSharded) SetObserver(o PipelineObserver) { c.obs = o }

// Shards reports K.
func (c *ConcurrentSharded) Shards() int { return len(c.workers) }

func (w *shardWorker) run() {
	defer w.c.wg.Done()
	for op := range w.ch {
		w.apply(op)
		if op.done {
			w.done <- struct{}{}
		}
	}
}

// apply runs one operation on the shard's engine and collects what it emitted. It runs on the
// shard's goroutine, or on the dispatcher once the engine is closed.
func (w *shardWorker) apply(op shardOp) {
	start := time.Now()
	switch op.kind {
	case opMsg:
		w.e.ProcessReceived(op.seq, op.t, op.r, evsOf(op.evs))
	case opMember:
		w.e.ProcessReceivedMember(op.seq, op.t, op.r, evsOf(op.evs))
	case opSkip:
		w.e.Skip(op.seq)
	case opFn:
		op.fn(w.e)
	case opBarrier:
	}
	if op.evs != nil {
		// The engine copies events by value and keeps no reference to the slice, so it can go
		// back to the pool. Clearing drops the references the events hold.
		clear(*op.evs)
		*op.evs = (*op.evs)[:0]
		w.c.evPool.Put(op.evs)
	}
	w.harvest(op.item, op.tag)
	if n := w.e.DrainLateSamples(); n > 0 {
		w.late.Add(n)
	}
	w.busy.Add(uint64(time.Since(start)))
	w.doneItem.Store(op.item) // after the harvest: a reader of doneItem sees every detection up to it
}

func evsOf(p *[]Event) []Event {
	if p == nil {
		return nil
	}
	return *p
}

// harvest moves what the engine emitted into the shard's output, under the operation's number and
// tag. It runs on the shard's goroutine, or on the dispatcher at a barrier when the shard is idle.
func (w *shardWorker) harvest(item uint64, tag Tag) {
	dets := w.e.drainOrdered()
	if len(dets) == 0 {
		return
	}
	w.mu.Lock()
	for _, d := range dets {
		w.out = append(w.out, taggedOut{item: item, shard: w.idx, TaggedDetection: TaggedDetection{Detection: d, Tag: tag}})
	}
	w.mu.Unlock()
}

// send queues op on shard i, numbering it. It blocks while the queue is full.
func (c *ConcurrentSharded) send(i int, op shardOp) {
	op.item = c.item
	c.sent[i] = op.item
	if c.closed {
		// A closed engine has no goroutines left and keeps working, synchronously, so a call that
		// outlives the term it belonged to computes the right answer rather than panicking.
		c.workers[i].apply(op)
		return
	}
	c.workers[i].ch <- op
}

// next starts a new operation number and returns the pending tag, which stays pending: it belongs
// to the message or advance it was set for, and the descopes and arming calls around that operation
// share it.
func (c *ConcurrentSharded) next() Tag {
	c.item++
	return c.tag
}

// consume is next for the operations that the tag was set for (a message, a member, an Advance):
// the tag is spent.
func (c *ConcurrentSharded) consume() Tag {
	t := c.next()
	c.tag = Tag{}
	return t
}

// Tag stamps the next tagged operation (a message, a member or an Advance).
func (c *ConcurrentSharded) Tag(at time.Time, replayable bool) {
	c.tag = Tag{At: at, Replayable: replayable}
}

// Sync waits until every shard has applied everything sent so far.
func (c *ConcurrentSharded) Sync() { c.quiesce() }

func (c *ConcurrentSharded) quiesce() {
	if c.closed {
		return
	}
	start := time.Now()
	for i := range c.workers {
		c.workers[i].ch <- shardOp{kind: opBarrier, done: true, item: c.item}
	}
	for _, w := range c.workers {
		<-w.done
	}
	if c.obs != nil {
		c.obs.BarrierWait(time.Since(start))
	}
}

// direct runs f on the dispatcher with every shard idle, then collects whatever f made the engines
// emit under a fresh operation number and the pending tag.
func (c *ConcurrentSharded) direct(f func()) {
	c.quiesce()
	f()
	c.harvestAll(c.next())
}

func (c *ConcurrentSharded) harvestAll(tag Tag) {
	for _, w := range c.workers {
		w.harvest(c.item, tag)
	}
}

// directResult is direct for a call with a result.
func directResult[T any](c *ConcurrentSharded, f func() T) T {
	var r T
	c.direct(func() { r = f() })
	return r
}

// ownerCall runs f on the shard that owns key and waits for it. Anything the shard did before f
// is applied first, and the other shards are not waited for.
func (c *ConcurrentSharded) ownerCall(i int, f func(e *Engine)) {
	tag := c.next()
	c.send(i, shardOp{kind: opFn, fn: f, done: true, tag: tag})
	if !c.closed {
		<-c.workers[i].done
	}
}

// ownerAsync queues f for the shard that owns a key.
func (c *ConcurrentSharded) ownerAsync(i int, f func(e *Engine)) {
	tag := c.next()
	c.send(i, shardOp{kind: opFn, fn: f, tag: tag})
}

func (c *ConcurrentSharded) ownerOf(rule, series string) int {
	return shardOf(rule, series, len(c.workers), c.s.salt)
}

// UpsertRule installs the rule in every shard, at a barrier.
func (c *ConcurrentSharded) UpsertRule(r Rule) { c.direct(func() { c.s.UpsertRule(r) }) }

// RemoveRule evicts the rule and its state from every shard, at a barrier.
//
// The single Engine also discards the removed rule's detections it has not yet handed to Drain. The
// shards hand theirs over as they make them, so those are already the caller's (collected, or
// waiting to be) exactly as a loop that drains after every message has them in its own buffer, and
// they are not recalled. A live loop therefore sees the same thing from either engine; only a
// caller that removes a rule between an operation and its Drain can tell, and none does.
func (c *ConcurrentSharded) RemoveRule(id string) { c.direct(func() { c.s.RemoveRule(id) }) }

// RemoveMatching is Sharded.RemoveMatching, at a barrier.
func (c *ConcurrentSharded) RemoveMatching(match func(ruleID string) bool) int {
	return directResult(c, func() int { return c.s.RemoveMatching(match) })
}

// Descope applies a membership flip in the shard that owns the series, after everything that
// shard was sent before it, and reports whether it changed anything.
func (c *ConcurrentSharded) Descope(ruleID, series string, at time.Time) bool {
	var moved bool
	c.ownerCall(c.ownerOf(ruleID, series), func(e *Engine) { moved = e.Descope(ruleID, series, at) })
	return moved
}

// DropSeries is Sharded.DropSeries, at a barrier.
func (c *ConcurrentSharded) DropSeries(series string, stale func(ruleID string, raisedAt time.Time, raised bool) bool) int {
	return directResult(c, func() int { return c.s.DropSeries(series, stale) })
}

// SeriesKeys is the union of the shards' keys, at a barrier.
func (c *ConcurrentSharded) SeriesKeys() []SeriesKey {
	return directResult(c, func() []SeriesKey { return c.s.SeriesKeys() })
}

// ProcessEvent applies one event.
func (c *ConcurrentSharded) ProcessEvent(ev Event) { c.ProcessResolved(ev.Seq, ev.Time, []Event{ev}) }

// ProcessResolved is ProcessReceived with an unknown receipt.
func (c *ConcurrentSharded) ProcessResolved(seq uint64, t time.Time, evs []Event) {
	c.ProcessReceived(seq, t, Receipt{}, evs)
}

// ProcessReceived ticks every shard with the message's (seq, t) and hands each the events it owns.
// It returns once the message is queued, not once it is applied.
//
// OWNERSHIP. The events are copied by value into the shards' batches, but an Event's reference
// fields (Presence, and anything else it points at) are shared with the caller until the shard has
// applied it. The caller must not change them after this returns; the processor builds a fresh set
// per message, and the single Engine, which consumes them before it returns, does not need to say so.
func (c *ConcurrentSharded) ProcessReceived(seq uint64, t time.Time, r Receipt, evs []Event) {
	if seq <= c.lastSeq {
		c.tag = Tag{}
		return // every shard would ignore it: the engine's idempotency guard
	}
	c.lastSeq = seq
	c.dispatch(opMsg, seq, t, r, evs)
}

// ProcessReceivedMember is ProcessReceived for one member of an event envelope.
func (c *ConcurrentSharded) ProcessReceivedMember(seq uint64, t time.Time, r Receipt, evs []Event) {
	if seq < c.lastSeq {
		c.tag = Tag{}
		return
	}
	c.lastSeq = seq
	c.dispatch(opMember, seq, t, r, evs)
}

func (c *ConcurrentSharded) dispatch(kind opKind, seq uint64, t time.Time, r Receipt, evs []Event) {
	tag := c.consume()
	k := len(c.workers)
	var batches []*[]Event
	if len(evs) > 0 {
		batches = make([]*[]Event, k)
		for i := range evs {
			o := shardOf(evs[i].Key.Rule, evs[i].Key.Series, k, c.s.salt)
			if batches[o] == nil {
				batches[o] = c.evPool.Get().(*[]Event)
			}
			ev := evs[i]
			ev.idx = int32(i)
			*batches[o] = append(*batches[o], ev)
		}
	}
	for i := 0; i < k; i++ {
		var b *[]Event
		if batches != nil {
			b = batches[i]
		}
		c.send(i, shardOp{kind: kind, seq: seq, t: t, r: r, evs: b, tag: tag})
	}
}

// Skip records a poison message's sequence in every shard, in band.
func (c *ConcurrentSharded) Skip(seq uint64) bool {
	if seq == 0 || seq <= c.lastSeq {
		return false
	}
	c.lastSeq = seq
	c.next()
	for i := range c.workers {
		c.send(i, shardOp{kind: opSkip, seq: seq})
	}
	return true
}

// Advance moves every shard's logical time to w, at a barrier, and reports whether anything changed.
func (c *ConcurrentSharded) Advance(w time.Time) bool {
	c.quiesce()
	changed := c.s.Advance(w)
	c.harvestAll(c.consume())
	return changed
}

// SetExpected arms a dead-man in the owning shard, in band.
func (c *ConcurrentSharded) SetExpected(key SeriesKey, since time.Time) {
	c.ownerAsync(c.ownerOf(key.Rule, key.Series), func(e *Engine) { e.SetExpected(key, since) })
}

// RemoveExpected disarms a dead-man in the owning shard, in band, where it resolves at the
// watermark every shard holds at that position.
func (c *ConcurrentSharded) RemoveExpected(key SeriesKey) {
	c.ownerAsync(c.ownerOf(key.Rule, key.Series), func(e *Engine) { e.RemoveExpected(key) })
}

// ClearRaised drops a raised latch in the owning shard, in band.
func (c *ConcurrentSharded) ClearRaised(key SeriesKey) {
	c.ownerAsync(c.ownerOf(key.Rule, key.Series), func(e *Engine) { e.ClearRaised(key) })
}

// Collect returns the detections the shards have made so far without waiting, in exactly the order
// one engine would have emitted them: by operation, then by the place inside the operation (ord.go:
// timers, then panes, then released samples, then the message's events in message order), then by
// shard for the control-plane emissions that have no finer place. Two rules firing on one device in
// one message therefore come out in the order the plain engine emits them, whichever shards own them.
//
// It returns only the detections of operations every shard has finished: a frontier F such that no
// operation numbered at or below F is still queued anywhere. A fast shard's detection from a later
// operation is held back until the slower shards have caught up to it, so successive calls hand over
// a strictly advancing prefix of the single engine's emission order. Without that, a series' detections
// from different rules (different shards) could be collected, and so published, out of the order the
// single engine made them in. A shard with nothing queued does not hold the frontier back.
func (c *ConcurrentSharded) Collect() []TaggedDetection {
	frontier := c.item
	for i, w := range c.workers {
		if done := w.doneItem.Load(); done < c.sent[i] && done < frontier {
			frontier = done
		}
	}
	var all []taggedOut
	for _, w := range c.workers {
		w.mu.Lock()
		if len(w.out) > 0 {
			keep := w.out[:0]
			for _, o := range w.out {
				if o.item <= frontier {
					all = append(all, o)
				} else {
					keep = append(keep, o)
				}
			}
			clear(w.out[len(keep):])
			w.out = keep
		}
		w.mu.Unlock()
	}
	if len(all) == 0 {
		return nil
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].item != all[j].item {
			return all[i].item < all[j].item
		}
		if all[i].ord != all[j].ord {
			return all[i].ord.less(all[j].ord)
		}
		return all[i].shard < all[j].shard
	})
	out := make([]TaggedDetection, len(all))
	for i := range all {
		out[i] = all[i].TaggedDetection
		out[i].Detection.ord = emitOrd{}
	}
	return out
}

// Drain waits for every shard and returns all the detections made, without their tags.
func (c *ConcurrentSharded) Drain() []Detection {
	c.quiesce()
	tagged := c.Collect()
	if len(tagged) == 0 {
		return nil
	}
	out := make([]Detection, len(tagged))
	for i := range tagged {
		out[i] = tagged[i].Detection
	}
	return out
}

// DrainLateSamples sums the shards' late-sample telemetry as of now. It does not wait, so a
// message still queued has not been counted yet; the count is never lost.
func (c *ConcurrentSharded) DrainLateSamples() uint64 {
	var n uint64
	for _, w := range c.workers {
		n += w.late.Swap(0)
	}
	return n
}

// LastSeq is the position every shard will hold once its queue is drained.
func (c *ConcurrentSharded) LastSeq() uint64 { return c.lastSeq }

// Watermark is the shared frontier, at a barrier.
func (c *ConcurrentSharded) Watermark() time.Time {
	return directResult(c, func() time.Time { return c.s.Watermark() })
}

// RestoredFrontier is Sharded.RestoredFrontier, at a barrier.
func (c *ConcurrentSharded) RestoredFrontier() int {
	return directResult(c, func() int { return c.s.RestoredFrontier() })
}

// HasPendingWork is Sharded.HasPendingWork, at a barrier.
func (c *ConcurrentSharded) HasPendingWork() bool {
	return directResult(c, func() bool { return c.s.HasPendingWork() })
}

// PendingTimerCount is Sharded.PendingTimerCount, at a barrier.
func (c *ConcurrentSharded) PendingTimerCount() int {
	return directResult(c, func() int { return c.s.PendingTimerCount() })
}

// LiveKeyCounts is Sharded.LiveKeyCounts, at a barrier.
func (c *ConcurrentSharded) LiveKeyCounts() map[string]int {
	return directResult(c, func() map[string]int { return c.s.LiveKeyCounts() })
}

// RetainedSampleCounts is Sharded.RetainedSampleCounts, at a barrier.
func (c *ConcurrentSharded) RetainedSampleCounts() map[string]int {
	return directResult(c, func() map[string]int { return c.s.RetainedSampleCounts() })
}

// ExpectedKeys is Sharded.ExpectedKeys, at a barrier.
func (c *ConcurrentSharded) ExpectedKeys() []SeriesKey {
	return directResult(c, func() []SeriesKey { return c.s.ExpectedKeys() })
}

// HeartbeatAbsenceKeys is Sharded.HeartbeatAbsenceKeys, at a barrier.
func (c *ConcurrentSharded) HeartbeatAbsenceKeys() []SeriesKey {
	return directResult(c, func() []SeriesKey { return c.s.HeartbeatAbsenceKeys() })
}

// ShardStats reports each shard's queue depth and cumulative busy time without waiting.
func (c *ConcurrentSharded) ShardStats() []ShardStat {
	out := make([]ShardStat, len(c.workers))
	for i, w := range c.workers {
		out[i] = ShardStat{QueueDepth: len(w.ch), BusyNanos: w.busy.Load()}
	}
	return out
}

// ShardLiveKeys reports the number of live keys each shard holds, at a barrier.
func (c *ConcurrentSharded) ShardLiveKeys() []int {
	return directResult(c, func() []int {
		out := make([]int, len(c.workers))
		for i, e := range c.s.shards {
			for _, n := range e.LiveKeyCounts() {
				out[i] += n
			}
		}
		return out
	})
}

// Snapshot merges the shards into the single engine's snapshot, byte for byte, at a barrier. Each
// shard builds its own sorted state on its own goroutine, in parallel; the dispatcher merges the K
// sorted runs of every section and serializes once. It refuses when the shards are not at one
// watermark and one position, or when that position is not the dispatcher's own: a state no single
// engine could be in.
func (c *ConcurrentSharded) Snapshot() ([]byte, error) {
	c.quiesce()
	parts := make([]snapshot, len(c.workers))
	c.everyShard(func(i int, e *Engine) { parts[i] = e.snapshotState() })
	if err := c.alignedParts(parts); err != nil {
		if c.obs != nil {
			c.obs.InvariantViolation()
		}
		return nil, err
	}
	return json.Marshal(mergeSnapshots(parts))
}

// everyShard runs f on every shard's goroutine at once and waits for all of them.
func (c *ConcurrentSharded) everyShard(f func(i int, e *Engine)) {
	for i := range c.workers {
		i := i
		c.send(i, shardOp{kind: opFn, fn: func(e *Engine) { f(i, e) }, done: true})
	}
	if c.closed {
		return
	}
	for _, w := range c.workers {
		<-w.done
	}
}

// alignedParts is checkAligned over states already taken: every shard at one watermark and one
// position, and that position the dispatcher's own.
func (c *ConcurrentSharded) alignedParts(parts []snapshot) error {
	if got := parts[0].LastSeq; got != c.lastSeq {
		return fmt.Errorf("the shards are at sequence %d but the dispatcher recorded %d: refusing to snapshot", got, c.lastSeq)
	}
	w, q := parts[0].Watermark, parts[0].LastSeq
	for i := 1; i < len(parts); i++ {
		if !parts[i].Watermark.Equal(w) || parts[i].LastSeq != q {
			return fmt.Errorf("shard %d is at (watermark %s, seq %d) but shard 0 is at (%s, %d): refusing to snapshot a state no single engine could be in",
				i, parts[i].Watermark.Format(time.RFC3339Nano), parts[i].LastSeq, w.Format(time.RFC3339Nano), q)
		}
	}
	return nil
}

// Close stops the shard goroutines after they drain their queues and waits for them. The engine
// keeps answering afterwards, but synchronously, on the caller.
func (c *ConcurrentSharded) Close() {
	if c.closed {
		return
	}
	for _, w := range c.workers {
		close(w.ch)
	}
	c.wg.Wait()
	c.closed = true
}
