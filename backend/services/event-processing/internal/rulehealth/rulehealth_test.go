// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rulehealth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// fakeStore records every batch it is handed. block, when set, holds each call until it is
// closed or the call's context ends; fail names tenants whose batches fail; ignoreCtx makes a
// blocked call wait for block alone, the shape of a driver that cannot be interrupted.
type fakeStore struct {
	mu        sync.Mutex
	calls     []call
	block     chan struct{}
	entered   chan string
	fail      map[string]error
	ignoreCtx bool
}

type call struct {
	tenant string
	deltas []model.RuleFireDelta
	err    error
}

func (s *fakeStore) RecordFireBatch(ctx context.Context, tenant string, deltas []model.RuleFireDelta) (int, error) {
	if s.entered != nil {
		s.entered <- tenant
	}
	var err error
	if s.block != nil {
		if s.ignoreCtx {
			<-s.block
		} else {
			select {
			case <-s.block:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && s.fail != nil {
		err = s.fail[tenant]
	}
	s.calls = append(s.calls, call{tenant: tenant, deltas: append([]model.RuleFireDelta(nil), deltas...), err: err})
	if err != nil {
		return 0, err
	}
	return len(deltas), nil
}

func (s *fakeStore) snapshot() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.calls...)
}

// written sums what committed, per rule: the count, and the last edge/time in commit order as
// the additive monotonic upsert would leave it.
func (s *fakeStore) written() map[string]model.RuleFireDelta {
	out := map[string]model.RuleFireDelta{}
	for _, c := range s.snapshot() {
		if c.err != nil {
			continue
		}
		for _, d := range c.deltas {
			cur, ok := out[d.RuleID]
			if !ok {
				out[d.RuleID] = d
				continue
			}
			cur.Count += d.Count
			if !d.LastFiredAt.Before(cur.LastFiredAt) {
				cur.LastFiredAt, cur.LastEdge = d.LastFiredAt, d.LastEdge
			}
			out[d.RuleID] = cur
		}
	}
	return out
}

type fakeMetrics struct {
	mu                  sync.Mutex
	admitted, coalesced int64
	discarded           map[DropReason]int64
	contended           int64
	batches, failures   int
	written             int64
	flushPasses         []error // one per interval-tick flush, as RuleStatFlushPass filed it
}

func newFakeMetrics() *fakeMetrics { return &fakeMetrics{discarded: map[DropReason]int64{}} }

func (m *fakeMetrics) RuleStatAdmitted(coalesced bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.admitted++
	if coalesced {
		m.coalesced++
	}
}
func (m *fakeMetrics) RuleStatDiscarded(r DropReason, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.discarded[r] += n
}
func (m *fakeMetrics) RuleStatContended() { m.mu.Lock(); m.contended++; m.mu.Unlock() }
func (m *fakeMetrics) RuleStatBatch(_ int, _ int64, _ time.Duration, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches++
	if failed {
		m.failures++
	}
}
func (m *fakeMetrics) RuleStatWritten(n int64)                   { m.mu.Lock(); m.written += n; m.mu.Unlock() }
func (m *fakeMetrics) RuleStatPending(int, int64, time.Duration) {}
func (m *fakeMetrics) RuleStatFlushPass(_ context.Context, err error, _ time.Time) {
	m.mu.Lock()
	m.flushPasses = append(m.flushPasses, err)
	m.mu.Unlock()
}

func (m *fakeMetrics) passes() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]error(nil), m.flushPasses...)
}

// totals is what was admitted, written, and discarded AFTER admission: a fire refused at the
// door (full, overflow, closed) was never admitted, so it is not in either side of
// admitted == written + discarded.
func (m *fakeMetrics) totals() (admitted, written, discarded int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range []DropReason{DropWriteFailed, DropPurged, DropShutdown} {
		discarded += m.discarded[r]
	}
	return m.admitted, m.written, discarded
}

// testConfig never flushes on its own: a test flushes by stopping, or by filling a batch.
func testConfig() Config {
	c := DefaultConfig()
	c.FlushInterval = MaxFlushInterval
	return c
}

func newAcc(t *testing.T, s Store, cfg Config, m Metrics) *Accumulator {
	t.Helper()
	a, err := New(s, cfg, m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.Start()
	t.Cleanup(func() { a.Stop(context.Background(), false) })
	return a
}

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func fire(a *Accumulator, tenant, rule string, at time.Time, edge string) {
	a.RecordFire(context.Background(), tenant+"/p@1/"+rule, tenant, at, edge)
}

// §6.4 H: 10,000 acknowledged fires of one rule inside one flush interval are ONE row delta,
// not 10,000 upserts, and the delta carries all of them and the newest edge.
func TestTenThousandFiresOfOneRuleAreOneRowDelta(t *testing.T) {
	s := &fakeStore{}
	m := newFakeMetrics()
	a := newAcc(t, s, testConfig(), m)
	for i := 0; i < 10_000; i++ {
		edge := "raised"
		if i%2 == 1 {
			edge = "resolved"
		}
		fire(a, "acme", "hot", t0.Add(time.Duration(i)*time.Millisecond), edge)
	}
	if dropped := a.Stop(context.Background(), true); dropped != 0 {
		t.Fatalf("a graceful stop dropped %d", dropped)
	}
	calls := s.snapshot()
	if len(calls) != 1 || len(calls[0].deltas) != 1 {
		t.Fatalf("got %d store calls (%v); want one call with one row", len(calls), calls)
	}
	d := calls[0].deltas[0]
	if d.Count != 10_000 || d.LastEdge != "resolved" || !d.LastFiredAt.Equal(t0.Add(9999*time.Millisecond)) {
		t.Fatalf("delta %+v, want 10000 fires ending resolved at t0+9.999s", d)
	}
	if adm, wr, disc := m.totals(); adm != 10_000 || wr != 10_000 || disc != 0 {
		t.Fatalf("admitted %d written %d discarded %d", adm, wr, disc)
	}
	if m.coalesced != 9_999 {
		t.Fatalf("coalesced %d, want 9999", m.coalesced)
	}
}

// The tie rule, in memory: a newer fire wins, an older one never rewinds, and of two at the SAME
// time the later admission wins — the per-fire upsert's >=.
func TestCoalescingKeepsTheNewestAndTheLaterOfATie(t *testing.T) {
	s := &fakeStore{}
	a := newAcc(t, s, testConfig(), nil)
	fire(a, "acme", "a", t0.Add(time.Second), "raised")
	fire(a, "acme", "a", t0, "resolved")                // older: counted, does not rewind
	fire(a, "acme", "b", t0, "raised")                  //
	fire(a, "acme", "b", t0, "resolved")                // tie: the later admission wins
	fire(a, "acme", "c", t0, "raised")                  //
	fire(a, "acme", "c", t0.Add(time.Minute), "raised") // newer wins
	a.Stop(context.Background(), true)
	got := s.written()
	for id, want := range map[string]model.RuleFireDelta{
		"acme/p@1/a": {Count: 2, LastFiredAt: t0.Add(time.Second), LastEdge: "raised"},
		"acme/p@1/b": {Count: 2, LastFiredAt: t0, LastEdge: "resolved"},
		"acme/p@1/c": {Count: 2, LastFiredAt: t0.Add(time.Minute), LastEdge: "raised"},
	} {
		g := got[id]
		if g.Count != want.Count || g.LastEdge != want.LastEdge || !g.LastFiredAt.Equal(want.LastFiredAt) {
			t.Errorf("%s: %+v, want %+v", id, g, want)
		}
	}
}

// Across generations: an equal-time update admitted while an earlier one is in flight reaches
// the store AFTER it, so the store's own >= keeps the later admission's edge.
func TestGenerationsReachTheStoreInAdmissionOrder(t *testing.T) {
	s := &fakeStore{block: make(chan struct{}), entered: make(chan string, 8)}
	cfg := testConfig()
	cfg.MaxBatchRows = 1 // the first admission wakes the worker
	a := newAcc(t, s, cfg, nil)
	fire(a, "acme", "a", t0, "raised")
	<-s.entered                          // generation 1 is in flight
	fire(a, "acme", "a", t0, "resolved") // generation 2, same time, admitted later
	close(s.block)
	a.Stop(context.Background(), true)
	calls := s.snapshot()
	if len(calls) != 2 {
		t.Fatalf("%d calls, want 2", len(calls))
	}
	if calls[0].deltas[0].LastEdge != "raised" || calls[1].deltas[0].LastEdge != "resolved" {
		t.Fatalf("calls out of admission order: %+v", calls)
	}
	if g := s.written()["acme/p@1/a"]; g.LastEdge != "resolved" || g.Count != 2 {
		t.Fatalf("final row %+v; want 2 fires ending resolved", g)
	}
}

// One transaction per tenant, never two tenants in one call, rows sorted, and no call larger
// than MaxBatchRows.
func TestBatchesArePerTenantSortedAndBounded(t *testing.T) {
	s := &fakeStore{}
	cfg := testConfig()
	cfg.MaxBatchRows = 3
	a := newAcc(t, s, cfg, nil)
	for _, tenant := range []string{"acme", "beta"} {
		for _, r := range []string{"j", "c", "a", "h", "e", "b", "i", "d", "g", "f"} {
			fire(a, tenant, r, t0, "raised")
		}
	}
	a.Stop(context.Background(), true)
	perTenant := map[string]int{}
	for _, c := range s.snapshot() {
		if len(c.deltas) == 0 || len(c.deltas) > 3 {
			t.Fatalf("a call carried %d rows; want 1..3", len(c.deltas))
		}
		last := map[string]string{}
		for _, d := range c.deltas {
			if d.RuleID[:len(c.tenant)+1] != c.tenant+"/" {
				t.Fatalf("tenant %s's call carried %s", c.tenant, d.RuleID)
			}
			if d.RuleID <= last[c.tenant] {
				t.Fatalf("rows not in rule-id order: %s after %s", d.RuleID, last[c.tenant])
			}
			last[c.tenant] = d.RuleID
			perTenant[c.tenant]++
		}
	}
	if perTenant["acme"] != 10 || perTenant["beta"] != 10 {
		t.Fatalf("rows per tenant %v, want 10 each", perTenant)
	}
}

// A tenant whose batch fails loses that batch, counted in FIRES, not retried; every other
// tenant's batch is written.
func TestAFailedTenantBatchIsDiscardedAndOthersAreWritten(t *testing.T) {
	s := &fakeStore{fail: map[string]error{"bad": errors.New("boom")}}
	m := newFakeMetrics()
	a := newAcc(t, s, testConfig(), m)
	for i := 0; i < 5; i++ {
		fire(a, "bad", "x", t0, "raised")
		fire(a, "bad", "y", t0, "raised")
		fire(a, "good", "x", t0, "raised")
	}
	a.Stop(context.Background(), true)
	if m.discarded[DropWriteFailed] != 10 {
		t.Fatalf("discarded %d fires as write_failed, want 10 (the failed batch's fires, not its call count)", m.discarded[DropWriteFailed])
	}
	if m.failures != 1 {
		t.Fatalf("%d failed batches, want 1", m.failures)
	}
	bad := 0
	for _, c := range s.snapshot() {
		if c.tenant == "bad" {
			bad++
		}
	}
	if bad != 1 {
		t.Fatalf("the failed batch was attempted %d times; want exactly once (no retry of an ambiguous commit)", bad)
	}
	if g := s.written()["good/p@1/x"]; g.Count != 5 {
		t.Fatalf("the healthy tenant's row %+v, want 5 fires", g)
	}
	if adm, wr, disc := m.totals(); adm != wr+disc {
		t.Fatalf("admitted %d != written %d + discarded %d", adm, wr, disc)
	}
}

// A store that does not answer: the write times out, is discarded once, and the worker moves on.
func TestATimedOutWriteIsDiscardedNotRetried(t *testing.T) {
	s := &fakeStore{block: make(chan struct{})}
	cfg := testConfig()
	cfg.WriteTimeout = MinWriteTimeout
	m := newFakeMetrics()
	a := newAcc(t, s, cfg, m)
	fire(a, "acme", "a", t0, "raised")
	fire(a, "acme", "a", t0, "raised")
	a.Stop(context.Background(), true)
	if len(s.snapshot()) != 1 {
		t.Fatalf("%d attempts, want 1", len(s.snapshot()))
	}
	if m.discarded[DropWriteFailed] != 2 || m.failures != 1 {
		t.Fatalf("discarded %v failures %d; want 2 write_failed fires in 1 failed batch", m.discarded, m.failures)
	}
}

// The store's erasure-fence refusal is accounted as a purge, not a failing store.
func TestAFenceRefusalIsAPurgeNotAFailure(t *testing.T) {
	s := &fakeStore{fail: map[string]error{"gone": fmt.Errorf("wrapped: %w", rdb.ErrTenantPurged)}}
	m := newFakeMetrics()
	a := newAcc(t, s, testConfig(), m)
	fire(a, "gone", "a", t0, "raised")
	a.Stop(context.Background(), true)
	if m.discarded[DropPurged] != 1 || m.failures != 0 {
		t.Fatalf("discarded %v failures %d; want 1 purged, 0 failures", m.discarded, m.failures)
	}
}

// The producer never waits for the database: with the store wedged on its first batch, tens of
// thousands of admissions still return promptly, the budget turns the excess into COUNTED
// loss, and once the store answers every admitted fire is written or accounted for.
func TestAWedgedStoreNeverHoldsTheProducer(t *testing.T) {
	s := &fakeStore{block: make(chan struct{}), entered: make(chan string, 64)}
	cfg := testConfig()
	cfg.MaxPendingKeys = 64
	cfg.MaxBatchRows = 8
	m := newFakeMetrics()
	a := newAcc(t, s, cfg, m)
	for r := 0; r < 8; r++ {
		fire(a, "acme", fmt.Sprintf("r%02d", r), t0, "raised")
	}
	<-s.entered // the worker is now stuck inside the store
	start := time.Now()
	const n = 50_000
	for i := 0; i < n; i++ {
		fire(a, "acme", fmt.Sprintf("k%03d", i%500), t0.Add(time.Duration(i)), "raised")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("%d admissions took %s against a wedged store", n, el)
	}
	if keys, _, _ := a.Pending(); keys > cfg.MaxPendingKeys {
		t.Fatalf("%d keys pending past the %d budget (the in-flight set counts too)", keys, cfg.MaxPendingKeys)
	}
	if m.discarded[DropFull] == 0 {
		t.Fatal("500 distinct keys against a 64-key budget dropped nothing")
	}
	close(s.block)
	a.Stop(context.Background(), true)
	adm, wr, disc := m.totals()
	if adm+m.discarded[DropFull] != n+8 {
		t.Fatalf("admitted %d + refused %d, want %d attempts", adm, m.discarded[DropFull], n+8)
	}
	if adm != wr+disc {
		t.Fatalf("admitted %d != written %d + discarded %d: a fire went unaccounted", adm, wr, disc)
	}
	var stored int64
	for _, d := range s.written() {
		stored += d.Count
	}
	if stored != wr {
		t.Fatalf("the store holds %d fires, the counter says %d were written", stored, wr)
	}
}

// The byte budget binds on its own: long rule ids exhaust it before the key budget.
func TestTheByteBudgetBinds(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPendingBytes = MinMaxPendingBytes
	m := newFakeMetrics()
	a := newAcc(t, &fakeStore{}, cfg, m)
	long := string(make([]byte, 480))
	admitted := 0
	for i := 0; i < 1000; i++ {
		a.RecordFire(context.Background(), fmt.Sprintf("acme/%s%04d", long, i), "acme", t0, "raised")
	}
	_, bytes, _ := a.Pending()
	if bytes > cfg.MaxPendingBytes {
		t.Fatalf("%d bytes pending past the %d budget", bytes, cfg.MaxPendingBytes)
	}
	keys, _, _ := a.Pending()
	admitted = keys
	if admitted >= 1000 || m.discarded[DropFull] != int64(1000-admitted) {
		t.Fatalf("admitted %d keys, dropped %d full", admitted, m.discarded[DropFull])
	}
}

// A count at the int64 ceiling is not incremented into a negative number: the fire is dropped
// and counted.
func TestACountAtTheCeilingIsNotWrapped(t *testing.T) {
	m := newFakeMetrics()
	a := newAcc(t, &fakeStore{}, testConfig(), m)
	fire(a, "acme", "a", t0, "raised")
	a.mu.Lock()
	ts := a.active.tenants["acme"]
	e := ts.rules["acme/p@1/a"]
	e.count = math.MaxInt64
	ts.rules["acme/p@1/a"] = e
	a.mu.Unlock()
	fire(a, "acme", "a", t0, "raised")
	a.mu.Lock()
	got := a.active.tenants["acme"].rules["acme/p@1/a"].count
	a.mu.Unlock()
	if got != math.MaxInt64 || m.discarded[DropOverflow] != 1 {
		t.Fatalf("count %d, overflow drops %d", got, m.discarded[DropOverflow])
	}
}

// Purge removes the tenant's pending entries from every set, leaves other tenants alone, cancels
// and WAITS FOR its in-flight write, and reports what it removed.
func TestPurgeRemovesQueuedAndSettlesInFlight(t *testing.T) {
	s := &fakeStore{block: make(chan struct{}), entered: make(chan string, 8)}
	cfg := testConfig()
	cfg.MaxBatchRows = 2
	m := newFakeMetrics()
	a := newAcc(t, s, cfg, m)
	fire(a, "acme", "a", t0, "raised")
	fire(a, "acme", "b", t0, "raised") // fills a batch: the worker takes acme into flight
	<-s.entered
	fire(a, "acme", "c", t0, "raised") // active, behind the in-flight write
	fire(a, "beta", "a", t0, "raised")
	n, err := a.Purge(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 3 {
		t.Fatalf("Purge removed %d entries, want 3 (two in flight, one active)", n)
	}
	a.mu.Lock()
	inflight := a.inflight
	a.mu.Unlock()
	if inflight != nil && inflight.tenant == "acme" {
		t.Fatal("Purge returned while acme's write was still in flight")
	}
	close(s.block)
	a.Stop(context.Background(), true)
	if w := s.written(); len(w) != 1 || w["beta/p@1/a"].Count != 1 {
		t.Fatalf("written %+v; want only beta's row", w)
	}
	if m.discarded[DropPurged] != 3 {
		t.Fatalf("purged %d fires, want 3", m.discarded[DropPurged])
	}
}

// A tenant still waiting in the DETACHED generation, behind another tenant's write in flight, is
// removed by a purge and never reaches the store, and the worker goes on to finish the flush.
func TestPurgeRemovesADetachedTenantNotYetTaken(t *testing.T) {
	s := &fakeStore{block: make(chan struct{}), entered: make(chan string, 8)}
	cfg := testConfig()
	cfg.MaxBatchRows = 2
	m := newFakeMetrics()
	a := newAcc(t, s, cfg, m)
	fire(a, "acme", "a", t0, "raised")
	fire(a, "beta", "a", t0, "raised") // two keys: the worker detaches both tenants together
	first := <-s.entered
	other := map[string]string{"acme": "beta", "beta": "acme"}[first]
	a.mu.Lock()
	queued := a.queued.keys
	a.mu.Unlock()
	if queued != 1 {
		t.Fatalf("%d keys detached behind the write in flight, want 1", queued)
	}
	n, err := a.Purge(context.Background(), other)
	if err != nil || n != 1 {
		t.Fatalf("Purge(%s) = %d, %v; want 1 entry removed", other, n, err)
	}
	if keys, _, _ := a.Pending(); keys != 1 {
		t.Fatalf("%d keys pending after the purge, want only the one in flight", keys)
	}
	close(s.block)
	a.Stop(context.Background(), true)
	for _, c := range s.snapshot() {
		if c.tenant == other {
			t.Fatalf("the purged tenant %s was written: %+v", other, c)
		}
	}
	if w := s.written(); len(w) != 1 || w[first+"/p@1/a"].Count != 1 {
		t.Fatalf("written %+v; want only %s's row", w, first)
	}
	if m.discarded[DropPurged] != 1 {
		t.Fatalf("purged %d fires, want 1", m.discarded[DropPurged])
	}
}

// A write that ignores cancellation and does not settle within the purge's deadline makes
// Purge FAIL: the caller must not report the tenant clean while a write may still land.
func TestPurgeFailsWhenTheInFlightWriteDoesNotSettle(t *testing.T) {
	s := &fakeStore{block: make(chan struct{}), entered: make(chan string, 8), ignoreCtx: true}
	cfg := testConfig()
	cfg.MaxBatchRows = 1
	a := newAcc(t, s, cfg, nil)
	fire(a, "acme", "a", t0, "raised")
	<-s.entered
	// Released however the test ends: the cleanup's Stop joins the worker, which is inside this
	// uninterruptible write, so a failure that skipped the release would hang the package.
	defer close(s.block)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := a.Purge(ctx, "acme"); err == nil {
		t.Fatal("Purge reported success while the in-flight write was still running")
	}
}

// Leadership loss: Stop without a flush discards what is pending at once, cancels the
// in-flight write, joins the worker, and refuses every later admission.
func TestStopWithoutFlushDiscardsAndJoins(t *testing.T) {
	before := runtime.NumGoroutine()
	s := &fakeStore{block: make(chan struct{}), entered: make(chan string, 8)}
	cfg := testConfig()
	cfg.MaxBatchRows = 1
	m := newFakeMetrics()
	a, err := New(s, cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	fire(a, "acme", "a", t0, "raised")
	<-s.entered
	fire(a, "acme", "b", t0, "raised")
	start := time.Now()
	dropped := a.Stop(context.Background(), false)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("a no-flush stop took %s", el)
	}
	if dropped != 2 {
		t.Fatalf("Stop reported %d dropped, want 2 (one in flight, one pending)", dropped)
	}
	select {
	case <-a.done:
	default:
		t.Fatal("Stop returned before the worker exited")
	}
	fire(a, "acme", "c", t0, "raised")
	if m.discarded[DropClosed] != 1 || m.discarded[DropShutdown] != 2 {
		t.Fatalf("discarded %v; want 2 shutdown, 1 closed", m.discarded)
	}
	assertNoLeak(t, before)
}

// A graceful stop whose budget expires while the store hangs gives up at the deadline, discards
// the rest and still joins.
func TestAGracefulStopIsBoundedByItsDeadline(t *testing.T) {
	before := runtime.NumGoroutine()
	s := &fakeStore{block: make(chan struct{})}
	cfg := testConfig()
	a, err := New(s, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	fire(a, "acme", "a", t0, "raised")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if dropped := a.Stop(ctx, true); dropped != 1 {
		t.Fatalf("dropped %d, want 1", dropped)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("a bounded final flush took %s", el)
	}
	assertNoLeak(t, before)
}

// Stopping an accumulator that was never started discards and returns; stopping twice is safe.
func TestStopBeforeStartAndTwice(t *testing.T) {
	a, err := New(&fakeStore{}, testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fire(a, "acme", "a", t0, "raised")
	if n := a.Stop(context.Background(), true); n != 1 {
		t.Fatalf("dropped %d, want 1", n)
	}
	if n := a.Stop(context.Background(), true); n != 0 {
		t.Fatalf("second stop reported %d", n)
	}
	a.Start() // after a stop: no worker
	select {
	case <-a.done:
		t.Fatal("done closed: a worker ran after the stop")
	default:
	}
}

// The periodic flush writes without anyone stopping the accumulator.
func TestThePeriodicFlushWrites(t *testing.T) {
	s := &fakeStore{entered: make(chan string, 1)}
	cfg := testConfig()
	cfg.FlushInterval = MinFlushInterval
	a := newAcc(t, s, cfg, nil)
	fire(a, "acme", "a", t0, "raised")
	select {
	case <-s.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was written within 100 flush intervals")
	}
}

// The interval tick files each flush as a maintenance pass, with the outcome core.PassResult
// gives it: nil for a flush that wrote everything, a plain failure when the store refused every
// tenant. It goes through the worker's ticker (the caller), not flushOnce, so a worker that
// stopped filing -- or stopped flushing -- fails here.
func TestTheIntervalFlushFilesItsPass(t *testing.T) {
	cfg := testConfig()
	cfg.FlushInterval = MinFlushInterval

	t.Run("a flush that wrote everything is a clean pass", func(t *testing.T) {
		s := &fakeStore{}
		m := newFakeMetrics()
		a := newAcc(t, s, cfg, m)
		fire(a, "acme", "a", t0, "raised")
		deadline := time.Now().Add(5 * time.Second)
		for len(s.snapshot()) == 0 || len(m.passes()) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("no pass filed (writes=%d passes=%d)", len(s.snapshot()), len(m.passes()))
			}
			time.Sleep(time.Millisecond)
		}
		for _, err := range m.passes() {
			if err != nil {
				t.Fatalf("a flush that wrote everything was filed as %v", err)
			}
		}
	})

	t.Run("a store that refuses every tenant is a failed pass", func(t *testing.T) {
		boom := errors.New("boom")
		s := &fakeStore{fail: map[string]error{"a": boom, "b": boom}}
		m := newFakeMetrics()
		a := newAcc(t, s, cfg, m)
		fire(a, "a", "x", t0, "raised")
		fire(a, "b", "x", t0, "raised")
		deadline := time.Now().Add(5 * time.Second)
		for {
			var failed error
			for _, err := range m.passes() {
				if err != nil {
					failed = err
				}
			}
			if failed != nil {
				if errors.Is(failed, core.ErrPassPartial) {
					t.Fatalf("every tenant failed, but the pass was filed as partial: %v", failed)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("a flush the store refused entirely was never filed as a failure")
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func TestConfigValidation(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("the default is invalid: %v", err)
	}
	for name, mut := range map[string]func(*Config){
		"zero interval":   func(c *Config) { c.FlushInterval = 0 },
		"short interval":  func(c *Config) { c.FlushInterval = MinFlushInterval - 1 },
		"long interval":   func(c *Config) { c.FlushInterval = MaxFlushInterval + 1 },
		"zero keys":       func(c *Config) { c.MaxPendingKeys = 0 },
		"negative keys":   func(c *Config) { c.MaxPendingKeys = -1 },
		"too many keys":   func(c *Config) { c.MaxPendingKeys = MaxMaxPendingKeys + 1 },
		"too few bytes":   func(c *Config) { c.MaxPendingBytes = MinMaxPendingBytes - 1 },
		"too many bytes":  func(c *Config) { c.MaxPendingBytes = MaxMaxPendingBytes + 1 },
		"zero rows":       func(c *Config) { c.MaxBatchRows = 0 },
		"too many rows":   func(c *Config) { c.MaxBatchRows = MaxMaxBatchRows + 1 },
		"zero timeout":    func(c *Config) { c.WriteTimeout = 0 },
		"too long a wait": func(c *Config) { c.WriteTimeout = MaxWriteTimeout + 1 },
	} {
		c := DefaultConfig()
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, c)
		}
		if _, err := New(&fakeStore{}, c, nil); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	if _, err := New(nil, DefaultConfig(), nil); err == nil {
		t.Error("New accepted a nil store")
	}
}

// The admission path under -race, with the worker flushing concurrently and a purge running.
func TestConcurrentAdmissionFlushAndPurge(t *testing.T) {
	s := &fakeStore{}
	cfg := testConfig()
	cfg.FlushInterval = MinFlushInterval
	cfg.MaxBatchRows = 4
	m := newFakeMetrics()
	a := newAcc(t, s, cfg, m)
	deadline := time.Now().Add(300 * time.Millisecond)
	i := 0
	for time.Now().Before(deadline) {
		fire(a, []string{"acme", "beta"}[i%2], fmt.Sprintf("r%d", i%17), t0.Add(time.Duration(i)), "raised")
		if i%1000 == 999 {
			if _, err := a.Purge(context.Background(), "beta"); err != nil {
				t.Fatal(err)
			}
		}
		i++
	}
	a.Stop(context.Background(), true)
	if adm, wr, disc := m.totals(); adm != wr+disc {
		t.Fatalf("admitted %d != written %d + discarded %d", adm, wr, disc)
	}
}

// assertNoLeak waits briefly for the goroutine count to return to what it was. The repository
// carries no goroutine-leak checker; the count is the honest substitute.
func assertNoLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("%d goroutines after the stop, %d before", n, before)
	}
}
