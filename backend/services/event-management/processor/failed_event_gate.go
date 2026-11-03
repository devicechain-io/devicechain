// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
)

// FailedEventGate is the flood bound of the failed-events index.
//
// 🔴 WHY IT EXISTS. Unauthenticated, DeviceNotFound and NotGatewayChild arrive at the full
// inbound rate from a misconfigured fleet (one wrong credential on ten thousand devices is ten
// thousand rejections a second), and a probe of invented tokens sends a different device every
// time. An index that stored each would be a second event store holding nothing but refusals,
// written at the rate the platform is least able to afford and read by nobody. So the index
// admits at most FailedEventsPerDeviceHour rows per (tenant, reason, claimed device) in any
// sliding hour, and folds every record beyond it into the `suppressed` count of the latest
// row. A second bound, FailedEventsPerReasonHour per (tenant, reason), covers the case the
// first cannot: every record naming a different device. Records past it fold into one overflow
// row per reason (model.OverflowDeviceToken).
//
// IT DECIDES FROM THE RECORD'S OWN TIMELINE, not the wall clock: the window is measured in the
// broker's append times, so a backlog read after an outage is bounded exactly as the live flow
// would have been, instead of looking like one enormous instant.
//
// THE DATABASE IS THE TRUTH AND THIS IS ITS CACHE. A key this process has not seen (a restart,
// or an eviction) is hydrated from the table before it decides, so the bound survives a
// restart.
//
// IT HOLDS ACROSS REPLICAS WITHOUT A LOCK. With --ha, every replica reads the same durable and
// each keeps its own cache, which knows only the rows IT admitted. So an admit is never decided
// from the cache alone: before every ADMIT (per-device, per-reason and overflow alike) the
// gate re-counts the key's rows in the window from the table, and suppresses when the table
// already holds the bound. Admits are rare by construction (at most ten per key an hour), so the
// re-count costs one indexed read per row that is then inserted anyway. What remains is a race
// between two replicas admitting the same key in the same instant, which can overshoot a bound
// by at most the replica count - and is not worth a distributed lock. A suppressed record is
// folded onto the newest row this replica knows of for the key (which may be another
// replica's); the summary adds the counts up either way.
//
// WHAT IT CAN LOSE, said here rather than found. Suppressed counts are written to the table
// once a second, AFTER the record has been acknowledged: the stream does not hold a suppressed
// record for the count's sake, because holding one would turn a flood into a backlog. So during
// a database outage the counts accumulate in memory (Flush keeps them and retries), and a
// restart inside that window loses them. They are a measure of MAGNITUDE ("about N more like
// these"), never an audit; a record redelivered after a crash can likewise be counted once
// more. Admitted rows are never lost this way: they are inserted before the record is
// acknowledged. The reason bound's age is also approximate after a restart (rows loaded from
// the table are counted as if admitted just now, which only ever suppresses more).
//
// Decide and Admitted are one decision in two steps because the second must follow the INSERT:
// a row that failed to store was not admitted and must not use up a slot.
type FailedEventGate struct {
	store GateStore
	now   func() time.Time

	mu      sync.Mutex
	devices map[deviceKey]*deviceState
	reasons map[reasonKey]*reasonState
	// pending holds the suppressed counts not yet written, per target row.
	pending map[pendingKey]int64

	// recount is whether an admit is confirmed against the table first. Always true outside a
	// test; the one test that shows the cross-replica bound needs it off to show that the
	// re-count is what holds the bound, not the harness.
	recount bool

	// flushFailing is whether the last Flush could not write, so a Warn is logged once per
	// outage and the recovery once.
	flushFailing bool

	// maxKeys bounds devices. Past it, expired keys go first and then an arbitrary tenth: a
	// dropped key is not forgotten, it is re-read from the table the next time it is seen.
	maxKeys int
}

// GateStore is what the gate reads and writes. *model.Api satisfies it; every call carries the
// tenant's context.
type GateStore interface {
	RecentFailedEvents(ctx context.Context, reason int32, device string, since time.Time, limit int) ([]model.FailedEventRef, error)
	CountFailedEvents(ctx context.Context, reason int32, since time.Time) (int64, error)
	BumpFailedEventSuppressed(ctx context.Context, ref model.FailedEventRef, n int64) error
}

// DefaultGateMaxKeys bounds the gate's per-device state. At the roughly two hundred bytes a key
// takes with its full ring, it is a few tens of megabytes.
const DefaultGateMaxKeys = 100_000

const (
	reasonBucket  = 5 * time.Minute
	reasonBuckets = int(model.FailedEventsWindow / reasonBucket)
)

type deviceKey struct {
	tenant string
	reason int32
	device string
}

type reasonKey struct {
	tenant string
	reason int32
}

type pendingKey struct {
	tenant string
	ref    model.FailedEventRef
}

// deviceState is one key's admitted rows inside the window, oldest first, newest last.
type deviceState struct {
	admitted []model.FailedEventRef
}

// reasonState counts a (tenant, reason)'s admitted rows in five-minute buckets.
type reasonState struct {
	buckets [12]struct {
		id int64
		n  int
	}
}

func (r *reasonState) count(at time.Time) int {
	cur := bucketID(at)
	total := 0
	for _, b := range r.buckets {
		if b.id > cur-int64(reasonBuckets) && b.id <= cur {
			total += b.n
		}
	}
	return total
}

func (r *reasonState) add(at time.Time, n int) {
	id := bucketID(at)
	b := &r.buckets[id%int64(reasonBuckets)]
	if b.id != id {
		b.id, b.n = id, 0
	}
	b.n += n
}

// raiseTo makes the count at least n, for a table that holds more than this process admitted.
func (r *reasonState) raiseTo(at time.Time, n int) {
	if c := r.count(at); n > c {
		r.add(at, n-c)
	}
}

func bucketID(t time.Time) int64 { return t.Unix() / int64(reasonBucket/time.Second) }

// NewFailedEventGate builds the gate over its store. now is the wall clock, used only to decide
// when a flushed key is old enough to drop; every decision uses the record's own time.
func NewFailedEventGate(store GateStore) *FailedEventGate {
	return &FailedEventGate{
		store:   store,
		now:     time.Now,
		devices: map[deviceKey]*deviceState{},
		reasons: map[reasonKey]*reasonState{},
		pending: map[pendingKey]int64{},
		recount: true,
		maxKeys: DefaultGateMaxKeys,
	}
}

// Verdict is the gate's answer for one record.
type Verdict int

const (
	// VerdictAdmit: insert a row, then call Admitted.
	VerdictAdmit Verdict = iota
	// VerdictAdmitOverflow: insert the reason's overflow row (device model.OverflowDeviceToken),
	// then call Admitted with Decision.Key.
	VerdictAdmitOverflow
	// VerdictSuppress: the record was folded into an existing row's count. Nothing to insert.
	VerdictSuppress
	// VerdictDuplicate: the record is one the table already holds (a redelivery of an admitted
	// row). Nothing to insert and nothing to count.
	VerdictDuplicate
)

// Decision is what to do with one record.
type Decision struct {
	Verdict Verdict
	// key is the state the verdict concerns (the overflow key for an overflow verdict).
	key deviceKey
	// reason is the reason key an Admit verdict charges.
	reason reasonKey
}

// Decide says what to do with the record at stream position seq, appended at `at`, for a
// claimed device. ctx is the tenant's.
//
// The per-reason bound is decided FIRST. Once a reason is over it, every further record belongs
// to the overflow row whatever device it names, so a probe of invented tokens must not cost a
// table read per token to find that out: a device this process has no state for is NOT
// hydrated, and the record is folded without touching the table. (A redelivery of a row that
// was admitted before the bound was reached is, in that one case, counted rather than
// recognised; the count is a magnitude.)
func (g *FailedEventGate) Decide(ctx context.Context, tenant string, reasonCode int32, device string,
	seq int64, at time.Time) (Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	dk := deviceKey{tenant: tenant, reason: reasonCode, device: device}
	rk := reasonKey{tenant: tenant, reason: reasonCode}
	rs, rsFresh, err := g.reasonState(ctx, rk, at)
	if err != nil {
		return Decision{}, err
	}
	if rs.count(at) >= model.FailedEventsPerReasonHour {
		return g.decideOverflow(ctx, dk, rk, seq, at)
	}

	ds, dsFresh, err := g.deviceState(ctx, dk, model.FailedEventsPerDeviceHour, at)
	if err != nil {
		return Decision{}, err
	}
	if ds.holds(seq) {
		return Decision{Verdict: VerdictDuplicate, key: dk, reason: rk}, nil
	}
	ds.prune(at)
	if len(ds.admitted) >= model.FailedEventsPerDeviceHour {
		g.addPending(tenant, ds.newest(), 1)
		return Decision{Verdict: VerdictSuppress, key: dk, reason: rk}, nil
	}

	// About to admit: confirm against the table, which other replicas also write. A state that
	// was just read for this decision has already been confirmed.
	if g.recount && !dsFresh {
		refs, err := g.store.RecentFailedEvents(ctx, dk.reason, dk.device, at.Add(-model.FailedEventsWindow), model.FailedEventsPerDeviceHour)
		if err != nil {
			return Decision{}, err
		}
		ds.replace(refs)
		if ds.holds(seq) {
			return Decision{Verdict: VerdictDuplicate, key: dk, reason: rk}, nil
		}
		if len(ds.admitted) >= model.FailedEventsPerDeviceHour {
			g.addPending(tenant, ds.newest(), 1)
			return Decision{Verdict: VerdictSuppress, key: dk, reason: rk}, nil
		}
	}
	if g.recount && !rsFresh {
		n, err := g.store.CountFailedEvents(ctx, rk.reason, at.Add(-model.FailedEventsWindow))
		if err != nil {
			return Decision{}, err
		}
		rs.raiseTo(at, int(n))
		if rs.count(at) >= model.FailedEventsPerReasonHour {
			return g.decideOverflow(ctx, dk, rk, seq, at)
		}
	}
	return Decision{Verdict: VerdictAdmit, key: dk, reason: rk}, nil
}

// decideOverflow handles a record whose reason is over its bound: it belongs to the reason's
// overflow row, or to its own device's row when this process already knows that device is full.
// It reads the table only for the overflow key, and only the first time this process sees it.
func (g *FailedEventGate) decideOverflow(ctx context.Context, dk deviceKey, rk reasonKey, seq int64, at time.Time) (Decision, error) {
	if ds, ok := g.devices[dk]; ok {
		if ds.holds(seq) {
			return Decision{Verdict: VerdictDuplicate, key: dk, reason: rk}, nil
		}
		ds.prune(at)
		if len(ds.admitted) >= model.FailedEventsPerDeviceHour {
			g.addPending(dk.tenant, ds.newest(), 1)
			return Decision{Verdict: VerdictSuppress, key: dk, reason: rk}, nil
		}
	}
	ok := deviceKey{tenant: dk.tenant, reason: dk.reason, device: model.OverflowDeviceToken}
	os, fresh, err := g.deviceState(ctx, ok, 1, at)
	if err != nil {
		return Decision{}, err
	}
	if os.holds(seq) {
		return Decision{Verdict: VerdictDuplicate, key: ok, reason: rk}, nil
	}
	os.prune(at)
	if len(os.admitted) < 1 && g.recount && !fresh {
		// About to admit this hour's overflow row: another replica may have written it.
		refs, err := g.store.RecentFailedEvents(ctx, ok.reason, ok.device, at.Add(-model.FailedEventsWindow), 1)
		if err != nil {
			return Decision{}, err
		}
		os.replace(refs)
		if os.holds(seq) {
			return Decision{Verdict: VerdictDuplicate, key: ok, reason: rk}, nil
		}
	}
	if len(os.admitted) < 1 {
		return Decision{Verdict: VerdictAdmitOverflow, key: ok, reason: rk}, nil
	}
	g.addPending(ok.tenant, os.newest(), 1)
	return Decision{Verdict: VerdictSuppress, key: ok, reason: rk}, nil
}

// Admitted records that the row for d was stored, so it counts against its bounds. A
// duplicate insert (the table already held it) is also recorded: it was admitted once.
func (g *FailedEventGate) Admitted(d Decision, seq int64, at time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ds := g.devices[d.key]
	if ds == nil {
		ds = &deviceState{}
		g.devices[d.key] = ds
	}
	ds.admitted = append(ds.admitted, model.FailedEventRef{RecordSeq: seq, OccurredTime: at})
	if d.Verdict == VerdictAdmit {
		if rs := g.reasons[d.reason]; rs != nil {
			rs.add(at, 1)
		}
	}
}

// holds reports whether seq is one of the admitted rows.
func (s *deviceState) holds(seq int64) bool {
	for _, r := range s.admitted {
		if r.RecordSeq == seq {
			return true
		}
	}
	return false
}

// newest is the most recently admitted row. Callers check that there is one.
func (s *deviceState) newest() model.FailedEventRef { return s.admitted[len(s.admitted)-1] }

// replace sets the state to what the table holds (refs newest first, as the table returns
// them), which is the truth when another replica has written rows this process did not.
func (s *deviceState) replace(refs []model.FailedEventRef) {
	s.admitted = s.admitted[:0]
	for i := len(refs) - 1; i >= 0; i-- {
		s.admitted = append(s.admitted, refs[i])
	}
}

// prune drops admitted rows that have left the window ending at `at`.
func (s *deviceState) prune(at time.Time) {
	cutoff := at.Add(-model.FailedEventsWindow)
	i := 0
	for i < len(s.admitted) && !s.admitted[i].OccurredTime.After(cutoff) {
		i++
	}
	if i > 0 {
		s.admitted = append(s.admitted[:0], s.admitted[i:]...)
	}
}

// deviceState returns the key's state, reading it from the table when this process has none.
// fresh says the state was just read, so the table has already been asked.
func (g *FailedEventGate) deviceState(ctx context.Context, k deviceKey, limit int, at time.Time) (s *deviceState, fresh bool, err error) {
	if s, ok := g.devices[k]; ok {
		return s, false, nil
	}
	refs, err := g.store.RecentFailedEvents(ctx, k.reason, k.device, at.Add(-model.FailedEventsWindow), limit)
	if err != nil {
		return nil, false, err
	}
	s = &deviceState{admitted: make([]model.FailedEventRef, 0, len(refs))}
	s.replace(refs)
	g.evictIfFull(at)
	g.devices[k] = s
	return s, true, nil
}

func (g *FailedEventGate) reasonState(ctx context.Context, k reasonKey, at time.Time) (s *reasonState, fresh bool, err error) {
	if s, ok := g.reasons[k]; ok {
		return s, false, nil
	}
	n, err := g.store.CountFailedEvents(ctx, k.reason, at.Add(-model.FailedEventsWindow))
	if err != nil {
		return nil, false, err
	}
	s = &reasonState{}
	// Counted as admitted just now: conservative, it expires an hour from now rather than when
	// the rows really aged out.
	if n > 0 {
		s.add(at, int(n))
	}
	g.reasons[k] = s
	return s, true, nil
}

// evictIfFull keeps the per-device map within maxKeys.
func (g *FailedEventGate) evictIfFull(at time.Time) {
	if len(g.devices) < g.maxKeys {
		return
	}
	for k, s := range g.devices {
		s.prune(at)
		if len(s.admitted) == 0 {
			delete(g.devices, k)
		}
	}
	drop := g.maxKeys / 10
	for k := range g.devices {
		if len(g.devices) < g.maxKeys-drop {
			break
		}
		delete(g.devices, k)
	}
	// A reason state is small and few; it is bounded by tenants x reasons, which are bounded
	// by the tenant count and the vocabulary.
}

func (g *FailedEventGate) addPending(tenant string, ref model.FailedEventRef, n int64) {
	g.pending[pendingKey{tenant: tenant, ref: ref}] += n
}

// Pending reports the suppressed counts waiting to be written.
func (g *FailedEventGate) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

// Flush writes the suppressed counts accumulated since the last flush, one UPDATE per target
// row however many records it absorbed. A count that could not be written is kept for the next
// flush; a count for a row that is gone (retention dropped it) updates nothing and is dropped.
//
// An outage is logged once, with how many rows' counts are waiting, and its end once: a flush
// runs every second, so a line per key per second would bury the log in the very outage the
// operator is trying to read it through. The first failure also ends the pass, keeping the rest
// for the next one, rather than trying every remaining row against a store that just refused.
func (g *FailedEventGate) Flush(ctx context.Context) {
	g.mu.Lock()
	batch := g.pending
	g.pending = map[pendingKey]int64{}
	g.mu.Unlock()
	var failure error
	kept := 0
	for k, n := range batch {
		if failure != nil {
			g.keep(k, n)
			kept++
			continue
		}
		tctx := core.WithTenant(ctx, k.tenant)
		if err := g.store.BumpFailedEventSuppressed(tctx, k.ref, n); err != nil {
			if errors.Is(err, rdb.ErrTenantPurged) {
				// The tenant's data was erased: its count has nowhere to go and must not be
				// retried forever.
				continue
			}
			failure = err
			g.keep(k, n)
			kept++
		}
	}
	g.mu.Lock()
	wasFailing := g.flushFailing
	g.flushFailing = failure != nil
	g.mu.Unlock()
	switch {
	case failure != nil && !wasFailing:
		log.Warn().Err(failure).Int("pendingKeys", kept).
			Msg("Could not write failed-event suppressed counts; keeping them in memory and retrying every flush (logged once per outage; a restart before it clears loses them)")
	case failure == nil && wasFailing:
		log.Info().Msg("Failed-event suppressed counts are being written again")
	}
}

func (g *FailedEventGate) keep(k pendingKey, n int64) {
	g.mu.Lock()
	g.pending[k] += n
	g.mu.Unlock()
}
