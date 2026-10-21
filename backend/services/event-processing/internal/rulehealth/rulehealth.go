// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package rulehealth takes the console's per-rule fire counters off the detection loop.
//
// Each derived event the loop durably publishes counts one fire of its rule. That count used to
// be written inline: one erasure-fence read and one upsert per detection, on the loop, between
// one broker acknowledgement and the next publish. On PostgreSQL that write cost more than the
// publish it followed, and it was spent on a number that is approximate by contract and that no
// detection ever reads.
//
// An Accumulator replaces it. The loop ADMITS a fire (Accumulator.RecordFire) with a constant
// amount of in-memory work and never waits for the database. Fires coalesce per (tenant, rule
// id) into one count delta and the newest (event time, edge). One background worker per
// leadership term periodically detaches what has coalesced and writes it, one tenant per
// transaction, through model.RuleStatStore.RecordFireBatch.
//
// # What it promises and what it does not
//
//   - The loop never blocks on it. Admission takes the accumulator's lock, and every holder of
//     that lock does a constant amount of work under it: no SQL, no sorting, no scan of the
//     pending set (Purge's scan is of one tenant's entries and runs on the loop itself, which is
//     the producer). A contended admission waits only for such a section and is counted
//     (Metrics.RuleStatContended).
//   - Every pending fire is either written or COUNTED as discarded, by reason. Admission past the
//     key or byte budget is discarded at once (DropFull); a batch whose transaction fails or
//     times out is discarded, not retried (DropWriteFailed), because a failed commit may have
//     landed and retrying it would add its counts twice; a tenant purged before its batch was
//     written is discarded (DropPurged); whatever is left when a term ends is discarded
//     (DropShutdown), after a bounded final flush on a graceful stop.
//   - Under a healthy store, a key's updates reach the table in the order they were admitted,
//     equal event times included: a newer time wins, and of two at the same time the later
//     admission wins, as the per-fire upsert's >= always did. That holds because there is one
//     worker and it never detaches a new generation while an older one is unwritten.
//   - Nothing about freshness under overload or a failing store. On a healthy store a fire is
//     normally visible within the flush interval plus one batch's write time.
//   - A crash loses what is pending (bounded by the budget), and a replay counts again what it
//     re-publishes. The counter was always approximate; this changes by how much, not whether.
//
// # Erasure
//
// The store's erasure fence is what stops a purged tenant's row coming back, not this package:
// RecordFireBatch runs the production fence callback inside each tenant's transaction, so a
// batch for a fenced tenant is refused by the database. Purge removes the tenant's pending
// entries (they hold its rule ids) and cancels and WAITS FOR a write of that tenant already in
// flight, so the processor's eviction answer is sent only once nothing of the tenant's is still
// on its way to the table. An in-flight write counts as evicted for the rules it had not yet
// written (a cancelled commit may still have landed); a non-zero eviction makes the purge
// coordinator sweep again, and its sweep is what removes such a row. Admissions after a term ends are refused by
// that term's closed accumulator, and a new term gets a new one, so no list of purged tokens is
// kept anywhere.
package rulehealth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
)

// Store is the batch write the worker makes: model.RuleStatStore.RecordFireBatch.
type Store interface {
	RecordFireBatch(ctx context.Context, tenant string, deltas []model.RuleFireDelta) (int, error)
}

var _ Store = (*model.RuleStatStore)(nil)

// DropReason is why pending fires were discarded. A closed enum: it is a metric label.
type DropReason string

const (
	// DropFull: admission refused because the pending key or byte budget was exhausted.
	DropFull DropReason = "full"
	// DropOverflow: the rule's pending count is already the largest an int64 can hold.
	DropOverflow DropReason = "overflow"
	// DropClosed: admitted after this term's accumulator was stopped.
	DropClosed DropReason = "closed"
	// DropWriteFailed: the tenant batch's transaction failed or timed out, and was not retried.
	DropWriteFailed DropReason = "write_failed"
	// DropPurged: the tenant was purged before its fires were written, or the store refused
	// them because its erasure fence stands.
	DropPurged DropReason = "purged"
	// DropShutdown: still pending when the term ended (leadership lost, or the final flush on a
	// graceful stop did not reach it in time).
	DropShutdown DropReason = "shutdown"
)

// DropReasons is every DropReason, for a metric that pre-creates its label values.
var DropReasons = []DropReason{DropFull, DropOverflow, DropClosed, DropWriteFailed, DropPurged, DropShutdown}

// Metrics is the accumulator's observability sink, implemented by the processor. A nil Metrics
// records nothing. Every count is of fire UPDATES (detections), not of SQL calls, except where
// the method says batch.
type Metrics interface {
	// RuleStatAdmitted counts one admitted fire; coalesced when it merged into a pending entry.
	RuleStatAdmitted(coalesced bool)
	// RuleStatDiscarded counts updates fires discarded for reason.
	RuleStatDiscarded(reason DropReason, updates int64)
	// RuleStatContended counts one admission that found the lock held and waited for it.
	RuleStatContended()
	// RuleStatBatch observes one tenant batch transaction: its distinct rows, the fire updates
	// it carried, how long it took, and whether it FAILED through the store's fault (a refusal
	// by the erasure fence, or a write cancelled by a purge or the end of the term, is not one).
	RuleStatBatch(rows int, updates int64, took time.Duration, failed bool)
	// RuleStatWritten counts updates fires committed to the store.
	RuleStatWritten(updates int64)
	// RuleStatPending reports the pending set: distinct keys and accounted bytes across the
	// active, detached and in-flight entries, and the age of the oldest pending admission.
	RuleStatPending(keys int, bytes int64, oldest time.Duration)
	// RuleStatFlushPass files one interval flush as a maintenance pass: the context it ran
	// under (what tells a stop from a fault), the error core.PassResult made of its tenants,
	// and when it started.
	RuleStatFlushPass(ctx context.Context, err error, started time.Time)
}

// Config bounds the accumulator. Validate states the limits.
type Config struct {
	// FlushInterval is how often the worker writes what has coalesced. It is not a detection
	// timer: nothing about a detection waits for it.
	FlushInterval time.Duration
	// MaxPendingKeys bounds the distinct (tenant, rule) entries across the active, detached and
	// in-flight sets together. A key pending in two of them counts twice.
	MaxPendingKeys int
	// MaxPendingBytes bounds the accounted bytes of those same entries (see EntryBytes).
	MaxPendingBytes int64
	// MaxBatchRows is the most distinct rule rows one tenant transaction carries; a tenant with
	// more pending is written in several. It also triggers an early flush: once the active set
	// holds this many keys the worker is woken without waiting for the interval.
	MaxBatchRows int
	// WriteTimeout is the deadline for one tenant batch's whole transaction.
	WriteTimeout time.Duration
}

// Defaults, and the bounds Validate enforces. The bounds are deliberately finite: every one of
// these sizes memory or a transaction, and "effectively unlimited" is not a budget.
const (
	DefaultFlushInterval   = time.Second
	DefaultMaxPendingKeys  = 4096
	DefaultMaxPendingBytes = 4 << 20
	DefaultMaxBatchRows    = 256
	DefaultWriteTimeout    = 2 * time.Second

	MinFlushInterval   = 50 * time.Millisecond
	MaxFlushInterval   = time.Minute
	MaxMaxPendingKeys  = 1 << 20
	MinMaxPendingBytes = 64 << 10
	MaxMaxPendingBytes = 256 << 20
	// MaxMaxBatchRows keeps one batch inside one INSERT statement: RuleStat binds five
	// parameters per row and the wire protocol allows 65535 per statement (13107 rows).
	MaxMaxBatchRows = 4096
	MinWriteTimeout = 100 * time.Millisecond
	MaxWriteTimeout = time.Minute
)

// DefaultConfig is the configuration the service starts with when none is given.
func DefaultConfig() Config {
	return Config{
		FlushInterval:   DefaultFlushInterval,
		MaxPendingKeys:  DefaultMaxPendingKeys,
		MaxPendingBytes: DefaultMaxPendingBytes,
		MaxBatchRows:    DefaultMaxBatchRows,
		WriteTimeout:    DefaultWriteTimeout,
	}
}

// Validate refuses a configuration outside the bounds, naming the field.
func (c Config) Validate() error {
	switch {
	case c.FlushInterval < MinFlushInterval || c.FlushInterval > MaxFlushInterval:
		return fmt.Errorf("rule-health flush interval must be between %s and %s, got %s", MinFlushInterval, MaxFlushInterval, c.FlushInterval)
	case c.MaxPendingKeys < 1 || c.MaxPendingKeys > MaxMaxPendingKeys:
		return fmt.Errorf("rule-health pending keys must be between 1 and %d, got %d", MaxMaxPendingKeys, c.MaxPendingKeys)
	case c.MaxPendingBytes < MinMaxPendingBytes || c.MaxPendingBytes > MaxMaxPendingBytes:
		return fmt.Errorf("rule-health pending bytes must be between %d and %d, got %d", MinMaxPendingBytes, MaxMaxPendingBytes, c.MaxPendingBytes)
	case c.MaxBatchRows < 1 || c.MaxBatchRows > MaxMaxBatchRows:
		return fmt.Errorf("rule-health batch rows must be between 1 and %d, got %d", MaxMaxBatchRows, c.MaxBatchRows)
	case c.WriteTimeout < MinWriteTimeout || c.WriteTimeout > MaxWriteTimeout:
		return fmt.Errorf("rule-health write timeout must be between %s and %s, got %s", MinWriteTimeout, MaxWriteTimeout, c.WriteTimeout)
	}
	return nil
}

// The byte accounting model. It is an estimate of what an entry RETAINS, deliberately on the
// high side, and it is applied to every entry wherever it is: active, detached or in flight.
//
//   - a rule entry costs ruleEntryBytes plus the length of its rule id: the map slot (key
//     string header, the entry's count, time and edge header), map growth slack, and the id's
//     bytes as if they were a private copy (in practice they are shared with the rule registry);
//   - a tenant's first entry in a set costs tenantSetBytes plus the tenant token's length: the
//     outer map slot and the per-tenant map's own header and initial buckets.
//
// Rule ids are at most 512 bytes and tenant tokens at most 128 (their column widths), so one
// entry is at most ruleEntryBytes+512 and a tenant's first at most that plus tenantSetBytes+128:
// the byte budget is a hard bound on retained payload, not a hint.
const (
	ruleEntryBytes = 160
	tenantSetBytes = 320
)

// EntryBytes is the accounted cost of one rule entry, and of the tenant set it opens when it is
// that tenant's first.
func EntryBytes(tenant, ruleID string, firstForTenant bool) int64 {
	n := int64(ruleEntryBytes + len(ruleID))
	if firstForTenant {
		n += int64(tenantSetBytes + len(tenant))
	}
	return n
}

// entry is one rule's coalesced fires.
type entry struct {
	count int64
	at    time.Time
	edge  string
}

// tenantSet is one tenant's pending entries, with their accounting.
type tenantSet struct {
	rules   map[string]entry
	updates int64
	bytes   int64
	// gone is set, under the lock, when a DETACHED set leaves the detached generation: taken
	// into flight, or removed by a purge. The detached generation's map is never written once
	// it is detached (see flushOnce), so a set that has left it is marked rather than deleted.
	gone bool
}

// generation is a set of entries admitted together, with the time of its first admission.
type generation struct {
	tenants map[string]*tenantSet
	keys    int
	bytes   int64
	updates int64
	since   time.Time
}

func newGeneration() generation { return generation{tenants: make(map[string]*tenantSet)} }

// inflight is the tenant set the worker has taken to write.
type inflight struct {
	tenant  string
	set     *tenantSet
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	purged  bool
	since   time.Time
	written int // keys already committed, out of len(set.rules)
}

// Accumulator coalesces one leadership term's rule fires and writes them in the background.
// Build one with New, Start it with the term, and Stop it when the term ends; a stopped
// accumulator refuses every later admission. RecordFire and Purge are for the detection loop;
// Start and Stop for the code that owns the term.
type Accumulator struct {
	cfg   Config
	store Store
	m     Metrics
	now   func() time.Time

	mu       sync.Mutex
	active   generation
	queued   generation // detached, not yet taken by the worker
	inflight *inflight
	closed   bool

	ready   chan struct{} // an early flush is wanted; capacity 1
	stopReq chan context.Context
	base    context.Context
	cancel  context.CancelFunc
	started bool
	done    chan struct{}
	stopMu  sync.Mutex
	stopped bool
	// shutdownDropped is the updates discarded because the term ended (DropShutdown), which
	// Stop reports. Guarded by mu.
	shutdownDropped int64
}

// New builds an accumulator. cfg must be valid (Validate); a nil Metrics records nothing.
func New(store Store, cfg Config, m Metrics) (*Accumulator, error) {
	if store == nil {
		return nil, errors.New("rulehealth: no store")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	return &Accumulator{
		cfg: cfg, store: store, m: m, now: time.Now,
		active: newGeneration(), queued: newGeneration(),
		ready: make(chan struct{}, 1), stopReq: make(chan context.Context, 1),
		base: base, cancel: cancel, done: make(chan struct{}),
	}, nil
}

// Start launches the worker. It is called once; a second call does nothing.
func (a *Accumulator) Start() {
	a.stopMu.Lock()
	defer a.stopMu.Unlock()
	if a.started || a.stopped {
		return
	}
	a.started = true
	go a.run()
}

func (a *Accumulator) lock() {
	if !a.mu.TryLock() {
		if a.m != nil {
			a.m.RuleStatContended()
		}
		a.mu.Lock()
	}
}

func (a *Accumulator) discarded(reason DropReason, updates int64) {
	if a.m != nil && updates > 0 {
		a.m.RuleStatDiscarded(reason, updates)
	}
}

// RecordFire admits one positively acknowledged fire. It implements runtime.FireRecorder and
// never touches the database: it coalesces into the active entry for (tenant, ruleID), or
// creates one if the budget allows, or counts the fire as discarded. ctx is not used.
func (a *Accumulator) RecordFire(_ context.Context, ruleID, tenant string, at time.Time, edge string) {
	a.lock()
	if a.closed {
		a.mu.Unlock()
		a.discarded(DropClosed, 1)
		return
	}
	ts := a.active.tenants[tenant]
	if ts != nil {
		if e, ok := ts.rules[ruleID]; ok {
			if e.count == math.MaxInt64 {
				a.mu.Unlock()
				a.discarded(DropOverflow, 1)
				return
			}
			e.count++
			if !at.Before(e.at) { // newer wins; on a tie the later admission wins
				e.at, e.edge = at, edge
			}
			ts.rules[ruleID] = e
			ts.updates++
			a.active.updates++
			a.mu.Unlock()
			if a.m != nil {
				a.m.RuleStatAdmitted(true)
			}
			return
		}
	}
	cost := EntryBytes(tenant, ruleID, ts == nil)
	keys, bytes := a.pendingLocked()
	if keys+1 > a.cfg.MaxPendingKeys || bytes+cost > a.cfg.MaxPendingBytes {
		a.mu.Unlock()
		a.discarded(DropFull, 1)
		return
	}
	if ts == nil {
		ts = &tenantSet{rules: make(map[string]entry, 4)}
		a.active.tenants[tenant] = ts
	}
	ts.rules[ruleID] = entry{count: 1, at: at, edge: edge}
	ts.updates++
	ts.bytes += cost
	if a.active.keys == 0 {
		a.active.since = a.now()
	}
	a.active.keys++
	a.active.bytes += cost
	a.active.updates++
	wake := a.active.keys >= a.cfg.MaxBatchRows
	a.mu.Unlock()
	if a.m != nil {
		a.m.RuleStatAdmitted(false)
	}
	if wake {
		select {
		case a.ready <- struct{}{}:
		default:
		}
	}
}

// pendingLocked is the budgeted totals across the active, detached and in-flight sets.
func (a *Accumulator) pendingLocked() (int, int64) {
	keys, bytes := a.active.keys+a.queued.keys, a.active.bytes+a.queued.bytes
	if f := a.inflight; f != nil {
		keys += len(f.set.rules)
		bytes += f.set.bytes
	}
	return keys, bytes
}

// Pending reports the budgeted totals and the age of the oldest pending admission.
func (a *Accumulator) Pending() (keys int, bytes int64, oldest time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys, bytes = a.pendingLocked()
	var since time.Time
	for _, t := range []time.Time{a.active.since, a.queued.since} {
		if !t.IsZero() && (since.IsZero() || t.Before(since)) {
			since = t
		}
	}
	if f := a.inflight; f != nil && !f.since.IsZero() && (since.IsZero() || f.since.Before(since)) {
		since = f.since
	}
	if keys > 0 && !since.IsZero() {
		oldest = a.now().Sub(since)
	}
	return keys, bytes, oldest
}

// Purge removes every pending entry of tenant, cancels a write of that tenant already in
// flight and waits, within ctx, for it to settle. It returns how many pending entries it
// removed, the in-flight write's rules not yet written included (a cancelled commit may still
// have landed, so those are counted as evicted). An error means the in-flight write had not settled by ctx's end: the caller
// must not report the tenant clean.
func (a *Accumulator) Purge(ctx context.Context, tenant string) (int64, error) {
	a.mu.Lock()
	var n, updates int64
	for _, g := range []*generation{&a.active, &a.queued} {
		if ts := g.tenants[tenant]; ts != nil && !ts.gone {
			n += int64(len(ts.rules))
			updates += ts.updates
			g.keys -= len(ts.rules)
			g.bytes -= ts.bytes
			g.updates -= ts.updates
			if g == &a.active {
				delete(g.tenants, tenant)
			} else {
				// The worker may be ranging over the detached map without the lock: mark it,
				// never write the map (see flushOnce).
				ts.gone = true
			}
			if g.keys == 0 {
				g.since = time.Time{}
			}
		}
	}
	var wait chan struct{}
	if f := a.inflight; f != nil && f.tenant == tenant {
		f.purged = true
		f.cancel()
		n += int64(len(f.set.rules) - f.written)
		wait = f.done
	}
	a.mu.Unlock()
	a.discarded(DropPurged, updates)
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return n, fmt.Errorf("the rule-health write in flight for tenant %q had not settled: %w", tenant, ctx.Err())
		}
	}
	return n, nil
}

// Stop ends the term's accumulator: it refuses every later admission, and either makes a final
// flush bounded by ctx (flush true, a graceful stop) or discards what is pending at once (flush
// false: leadership lost, which must not wait on a health projection). It joins the worker and
// returns the updates discarded at the stop. It is idempotent; only the first call acts.
func (a *Accumulator) Stop(ctx context.Context, flush bool) int64 {
	a.stopMu.Lock()
	defer a.stopMu.Unlock()
	if a.stopped {
		return 0
	}
	a.stopped = true
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	if !a.started {
		a.cancel()
		a.discardAll()
		return a.droppedAtStop()
	}
	if flush && ctx.Err() == nil {
		a.stopReq <- ctx
		select {
		case <-a.done:
		case <-ctx.Done():
			a.cancel()
			<-a.done
		}
	} else {
		a.cancel()
		<-a.done
	}
	a.cancel()
	return a.droppedAtStop()
}

func (a *Accumulator) droppedAtStop() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shutdownDropped
}

// run is the worker: one per term.
func (a *Accumulator) run() {
	defer close(a.done)
	ticker := time.NewTicker(a.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.base.Done():
			a.discardAll()
			return
		case ctx := <-a.stopReq:
			fctx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(a.base, cancel)
			for a.hasActive() && fctx.Err() == nil {
				a.flushOnce(fctx)
			}
			stop()
			cancel()
			a.discardAll()
			return
		case <-ticker.C:
			started := time.Now()
			err := a.flushOnce(a.base)
			if a.m != nil {
				a.m.RuleStatFlushPass(a.base, err, started)
			}
		case <-a.ready:
			a.flushOnce(a.base)
		}
		a.reportPending()
	}
}

func (a *Accumulator) hasActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active.keys > 0
}

func (a *Accumulator) reportPending() {
	if a.m == nil {
		return
	}
	a.m.RuleStatPending(a.Pending())
}

// discardAll counts everything still pending as discarded at shutdown and empties it.
func (a *Accumulator) discardAll() {
	a.mu.Lock()
	updates := a.active.updates + a.queued.updates
	a.active, a.queued = newGeneration(), newGeneration()
	a.shutdownDropped += updates
	a.mu.Unlock()
	a.discarded(DropShutdown, updates)
	a.reportPending()
}

// flushOnce detaches the active generation and writes it, one tenant at a time. It is the only
// place a generation is detached, and it does not return while any of it is unwritten, so a
// key's updates reach the store in the order they were admitted.
//
// It returns the pass's outcome as core.PassResult states it: nil when every tenant written (or
// nothing was pending) succeeded, a failure when the store refused all of them, partial when it
// refused some. Only the interval tick files it.
func (a *Accumulator) flushOnce(ctx context.Context) error {
	a.mu.Lock()
	if a.active.keys == 0 && a.queued.keys == 0 {
		a.mu.Unlock()
		return nil
	}
	if a.queued.keys == 0 { // constant time: the maps are swapped, not copied
		a.queued, a.active = a.active, newGeneration()
	}
	// From here no one writes this map: admissions go to the new active generation, Purge marks
	// a detached set gone instead of deleting it, and take only marks too. So the worker ranges
	// over it WITHOUT the lock. Ranging under the lock, deleting as it went, would cost a scan
	// of the map's slots per tenant taken, and an almost-drained map of many tenants is almost
	// all empty slots: a lock hold that grows with the tenants pending, not a constant one.
	tenants := a.queued.tenants
	a.mu.Unlock()

	var visited, failed int
	var firstErr error
	for tenant, ts := range tenants { // any order: tenants never share a row
		f := a.take(ctx, tenant, ts)
		if f == nil {
			continue
		}
		visited++
		if err := a.writeTenant(f); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	a.mu.Lock()
	a.queued = newGeneration() // every set in the spent map has gone, taken or purged
	a.mu.Unlock()
	if failed > 0 {
		// One line per flush, not per batch: a store outage would otherwise log once per tenant
		// per interval. The counts are in detect_rule_stat_updates_discarded_total.
		log.Warn().Err(firstErr).Int("tenants", failed).
			Msg("Rule-health fire counters could not be written for some tenants and were discarded, not " +
				"retried; detection is unaffected, but the console's fire counts are short by what they held.")
	}
	return core.PassResult(visited, failed, firstErr)
}

// take moves one detached tenant set into flight, or returns nil when a purge has already
// removed it. Constant time under the lock: it marks the set, it does not touch the map.
func (a *Accumulator) take(ctx context.Context, tenant string, ts *tenantSet) *inflight {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ts.gone {
		return nil
	}
	ts.gone = true
	a.queued.keys -= len(ts.rules)
	a.queued.bytes -= ts.bytes
	a.queued.updates -= ts.updates
	since := a.queued.since
	if a.queued.keys == 0 {
		a.queued.since = time.Time{}
	}
	fctx, cancel := context.WithCancel(ctx)
	f := &inflight{tenant: tenant, set: ts, ctx: fctx, cancel: cancel, done: make(chan struct{}), since: since}
	a.inflight = f
	return f
}

// writeTenant writes one in-flight tenant set in chunks of MaxBatchRows, in rule-id order, each
// chunk its own transaction under WriteTimeout. A chunk that fails is discarded with every chunk
// after it, never retried. It returns the store's error when the loss was the store's fault: a
// refusal by the erasure fence, a purge that cancelled the write, or a term that ended under it
// is accounted to its own reason and is not a failing store.
func (a *Accumulator) writeTenant(f *inflight) error {
	ids := make([]string, 0, len(f.set.rules))
	for id := range f.set.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	deltas := make([]model.RuleFireDelta, 0, min(len(ids), a.cfg.MaxBatchRows))
	var lost int64
	var failure error
	stopped := false
	for start := 0; start < len(ids); start += a.cfg.MaxBatchRows {
		end := min(start+a.cfg.MaxBatchRows, len(ids))
		deltas = deltas[:0]
		var updates int64
		for _, id := range ids[start:end] {
			e := f.set.rules[id]
			deltas = append(deltas, model.RuleFireDelta{RuleID: id, Count: e.count, LastFiredAt: e.at, LastEdge: e.edge})
			updates += e.count
		}
		if stopped || f.ctx.Err() != nil {
			stopped = true
			lost += updates
			continue
		}
		wctx, cancel := context.WithTimeout(f.ctx, a.cfg.WriteTimeout)
		began := time.Now()
		_, err := a.store.RecordFireBatch(wctx, f.tenant, deltas)
		took := time.Since(began)
		cancel()
		storeFault := err != nil && f.ctx.Err() == nil && !errors.Is(err, rdb.ErrTenantPurged)
		if a.m != nil {
			a.m.RuleStatBatch(len(deltas), updates, took, storeFault)
		}
		if err != nil {
			stopped = true
			lost += updates
			if storeFault {
				failure = err
			} else if errors.Is(err, rdb.ErrTenantPurged) {
				a.mu.Lock()
				f.purged = true
				a.mu.Unlock()
			}
			continue
		}
		if a.m != nil {
			a.m.RuleStatWritten(updates)
		}
		a.mu.Lock()
		f.written += end - start
		a.mu.Unlock()
	}
	a.mu.Lock()
	purged := f.purged
	a.inflight = nil
	a.mu.Unlock()
	f.cancel()
	close(f.done)
	switch {
	case purged:
		a.discarded(DropPurged, lost)
	case failure != nil:
		a.discarded(DropWriteFailed, lost)
	default: // the term's context ended under the write
		a.discarded(DropShutdown, lost)
		a.mu.Lock()
		a.shutdownDropped += lost
		a.mu.Unlock()
	}
	return failure
}
