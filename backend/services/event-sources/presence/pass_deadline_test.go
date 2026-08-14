// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// slowProjection records the order tenants were read in and stalls on each read, so a
// pass with a small deadline reliably runs out part-way through the tenant list.
type slowProjection struct {
	delay time.Duration
	seen  []string
}

func (s *slowProjection) AssertedStates(ctx context.Context, tenant, _ string) (map[string]StoredDevice, error) {
	s.seen = append(s.seen, tenant)
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]StoredDevice{}, nil
}

// recordingOutcomes captures the outcome a pass reports, through a real CounterVec — the
// same object production hands the reconciler, so what is asserted is the label a pass
// actually writes rather than a stand-in for it.
type recordingOutcomes struct{ counter *prometheus.CounterVec }

func (r *recordingOutcomes) vec() *prometheus.CounterVec {
	if r.counter == nil {
		r.counter = prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "test_reconcile_runs_total"}, []string{"outcome"})
	}
	return r.counter
}

// only asserts that the pass wrote exactly one outcome, and that it was want.
//
// 🔑 COUNT THE CHILDREN FIRST, THEN READ THE VALUE — the order is the assertion. Checking
// only that `want` was incremented would pass for a pass that ALSO wrote a second, wrong
// outcome; and WithLabelValues CREATES the child it names, so counting afterwards would
// count the one the assertion itself just brought into existence. An earlier version of
// this helper enumerated a fixed list of known labels instead, which could not see an
// outcome outside the list — the opposite of what it claimed to guarantee.
func (r *recordingOutcomes) only(t *testing.T, want string) {
	t.Helper()
	require.Equal(t, 1, testutil.CollectAndCount(r.vec()),
		"a pass must write exactly one outcome")
	require.Positive(t, testutil.ToFloat64(r.vec().WithLabelValues(want)),
		"the pass did not report outcome %q", want)
}

// tenantList builds n tenant tokens in a stable order.
func tenantList(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "tenant-"+strconv.Itoa(i))
	}
	return out
}

// emptyInventory is a broker holding no connections, which keeps these tests about the
// pass's control flow rather than about the diff.
func emptyInventory() *fakeRequester {
	return &fakeRequester{servers: []string{"A"}, claimed: 1, conns: map[string][]connzConn{}}
}

// TestAPassThatRunsOutOfTimeSaysSo is the visibility half, and without it the deadline
// would make things WORSE.
//
// 🔴🔴 THE LOOP LEAVES ON ctx.Err() AND USED TO FALL STRAIGHT THROUGH TO outcome
// "complete" AND return nil. Adding a deadline to that shape produces a pass that skips
// every tenant it never reached, reports itself healthy, and logs nothing — a silent
// reduction in coverage that no metric distinguishes from a quiet fleet. A bound that
// cannot be observed is not a bound, it is a hiding place.
func TestAPassThatRunsOutOfTimeSaysSo(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{delay: 50 * time.Millisecond}
	outcomes := &recordingOutcomes{}

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenantList(10)}, reads,
		ReconcileMetrics{Runs: outcomes.vec()}, time.Millisecond, time.Now, 120*time.Millisecond)

	err := r.Run(context.Background())
	require.Error(t, err, "a pass cut short by its own deadline must be reported, not swallowed")
	require.Contains(t, err.Error(), "ran out of time")
	// 🔑 THE METRIC AND THE ERROR ARE BOTH ASSERTED, because they are written in different
	// places and an operator reads the metric. Measured: with only the error checked, a
	// mutation making a cut-short pass report "complete" survived the whole package — the
	// error said one thing while the series an alert watches said another.
	outcomes.only(t, "timeout")
	require.Less(t, len(reads.seen), 10, "the fixture must actually cut the pass short, or this test measures nothing")
}

// TestTheNextPassResumesWhereTheLastOneStopped is the starvation fix.
//
// 🔴 WITHOUT IT A LARGE FLEET RECONCILES ITS FIRST FEW TENANTS FOREVER. The tenant list
// comes back in a stable order and the walk used to restart at index 0 every pass, so a
// pass that could never finish would visit the same prefix on every attempt and the
// tenants at the end of the list would never be reconciled at all — not late, never. And
// this reconciler is the only account of the deaths a graceful broker shutdown does not
// announce, so "never reconciled" means those devices stay asserted-online forever.
func TestTheNextPassResumesWhereTheLastOneStopped(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{delay: 50 * time.Millisecond}
	tenants := tenantList(6)

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenants}, reads,
		ReconcileMetrics{}, time.Millisecond, time.Now, 120*time.Millisecond)

	require.Error(t, r.Run(context.Background()))
	firstPass := append([]string(nil), reads.seen...)
	require.NotEmpty(t, firstPass)
	require.Less(t, len(firstPass), len(tenants), "pass 1 must not have reached every tenant")

	reads.seen = nil
	_ = r.Run(context.Background())
	require.NotEmpty(t, reads.seen)

	// 🔑 THE ASSERTION IS "IT DID NOT START OVER", not "it visited tenant N". Naming the
	// exact index would pin the fixture's timing rather than the property.
	require.NotEqual(t, firstPass[0], reads.seen[0],
		"the second pass restarted at the same tenant; the tail of the list is starved forever")
	require.Equal(t, tenants[len(firstPass)], reads.seen[0],
		"the second pass must resume at the first tenant the previous pass did not reach")
}

// TestTheRotationKeepsAdvancingAcrossSUCCESSIVECutShortPasses is what makes the rotation
// a rotation rather than a two-step.
//
// 🔴 IT TAKES THREE PASSES TO SEE THIS, WHICH IS WHY TWO WERE NOT ENOUGH. On the first
// pass the walk starts at index 0, so "where the pass began" and "zero" are the same
// number and any confusion between them is invisible. The second pass starts partway in —
// and if the next resume point is computed from zero rather than from where that pass
// actually began, the third pass rewinds to tenants the second one already covered. The
// tail of the list is then never reached, which is precisely the starvation the rotation
// exists to prevent, restored in a form that looks like it is rotating.
//
// Measured: with only the two-pass test present, the mutation that returns 0 as the start
// index instead of the real one SURVIVED the whole package.
func TestTheRotationKeepsAdvancingAcrossSUCCESSIVECutShortPasses(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{delay: 40 * time.Millisecond}
	tenants := tenantList(12)

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenants}, reads,
		ReconcileMetrics{}, time.Millisecond, time.Now, 100*time.Millisecond)

	// Three passes, each cut short. Track where each one began and how far it got, then
	// require that every pass starts exactly where its predecessor stopped.
	covered := 0
	for pass := 1; pass <= 3; pass++ {
		reads.seen = nil
		require.Error(t, r.Run(context.Background()), "pass %d must be cut short for this test to mean anything", pass)
		require.NotEmpty(t, reads.seen, "pass %d visited nothing", pass)
		require.Less(t, len(reads.seen), len(tenants), "pass %d must not have finished", pass)

		want := tenants[covered%len(tenants)]
		require.Equal(t, want, reads.seen[0],
			"pass %d began at %q; it must resume where pass %d stopped, at %q",
			pass, reads.seen[0], pass-1, want)
		covered += len(reads.seen)
	}

	// And the rotation genuinely reached past the prefix a non-advancing walk would have
	// been stuck on — otherwise the assertions above could all hold within pass 1's range.
	require.Greater(t, covered, len(tenants)/2,
		"three cut-short passes covered only %d of %d tenants; the walk is not advancing", covered, len(tenants))
}

// TestAPassThatFinishesStartsTheNextOneAtTheTop keeps the rotation from becoming a
// permanent drift: on a healthy instance every pass covers everyone, so the cursor must
// return to zero rather than marching forward forever.
func TestAPassThatFinishesStartsTheNextOneAtTheTop(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{} // no delay: every pass finishes
	tenants := tenantList(4)

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenants}, reads,
		ReconcileMetrics{}, time.Millisecond, time.Now, time.Minute)

	require.NoError(t, r.Run(context.Background()))
	require.Equal(t, tenants, reads.seen)

	reads.seen = nil
	require.NoError(t, r.Run(context.Background()))
	require.Equal(t, tenants, reads.seen, "a healthy pass must start at the top, in the list's own order")
}

// failingProjection cannot read any tenant — a device-state outage, or the mixed window of
// a rolling upgrade where the query's own shape is refused.
type failingProjection struct{ seen []string }

func (f *failingProjection) AssertedStates(_ context.Context, tenant, _ string) (map[string]StoredDevice, error) {
	f.seen = append(f.seen, tenant)
	return nil, errors.New("device-state is unavailable")
}

// TestAPassThatCouldReadNOTHINGDoesNotReportComplete closes the gap a per-tenant
// warn-and-continue leaves behind.
//
// 🔴🔴 EACH TENANT'S READ FAILURE IS CORRECTLY TOLERATED, AND THE AGGREGATE IS NOT. Skipping
// one tenant so the others still get their pass is right; skipping EVERY tenant is
// reconciliation being entirely off, and it used to be reported as a completed pass. The
// two situations that produce it are the two that matter most — a device-state outage, and
// the mixed-version window of a rolling upgrade — and in both, an operator watching the
// outcome metric would have seen a healthy instance repairing nothing.
func TestAPassThatCouldReadNOTHINGDoesNotReportComplete(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &failingProjection{}
	outcomes := &recordingOutcomes{}

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenantList(3)}, reads,
		ReconcileMetrics{Runs: outcomes.vec()}, time.Millisecond, time.Now, time.Minute)

	err := r.Run(context.Background())
	require.Error(t, err, "a pass that reconciled nothing must say so")
	require.Contains(t, err.Error(), "no tenant")
	require.Len(t, reads.seen, 3, "every tenant must still be attempted; one failure does not abort the rest")
	outcomes.only(t, "failed")
}

// TestAPassCutShortBySHUTDOWNIsNotATimeout keeps the metric the docs point operators at
// from carrying a sample that means something else.
//
// 🔑 THE PASS DEADLINE IS A CHILD OF THE SERVICE'S OWN CONTEXT, so a stop mid-pass is
// indistinguishable from running out of budget when viewed from inside the loop. Counting
// it as a timeout would put one spurious sample on every shutdown — on the exact series an
// operator is told to watch for sustained runs of.
func TestAPassCutShortBySHUTDOWNIsNotATimeout(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{delay: 50 * time.Millisecond}
	outcomes := &recordingOutcomes{}

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenantList(10)}, reads,
		ReconcileMetrics{Runs: outcomes.vec()}, time.Millisecond, time.Now, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	err := r.Run(ctx)
	require.NoError(t, err, "shutting down is not a failure the caller needs to be told about")
	outcomes.only(t, "cancelled") // a pass stopped by shutdown must not be counted as a timeout
	require.Less(t, len(reads.seen), 10, "the fixture must actually cut the pass short")
}

// TestTheResumePointSurvivesTheTenantListSHRINKING keeps the bookkeeping honest across a
// resize.
//
// 🔑 THE REMEMBERED INDEX DESCRIBES A LIST THAT MAY NO LONGER EXIST. Stop at index 8 of
// ten tenants; the instance drops to five; the next pass cannot start at 8, so it starts
// at 0 — and if the NEXT resume point is computed from the remembered 8 rather than from
// where the pass truly began, it names a tenant this pass never reached. Nothing is
// starved forever either way, which is exactly why this needs its own test: the symptom
// is a rotation that skips and repeats rather than one that stops.
func TestTheResumePointSurvivesTheTenantListSHRINKING(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{}
	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenantList(5)}, reads,
		ReconcileMetrics{}, time.Millisecond, time.Now, time.Minute)

	// Stand in for "a previous pass over a longer list stopped at index 8".
	r.resumeAt = 8

	require.NoError(t, r.Run(context.Background()))
	require.Equal(t, tenantList(5), reads.seen,
		"a resume index past the end of the current list must fall back to starting at the top")
	require.Equal(t, 0, r.resumeAt, "a pass that finished must reset the resume point")
}

// TestThePassBudgetLeavesRoomInsideTheConfiguredInterval keeps a bound from quietly
// becoming a permanent background scan.
//
// A budget longer than the interval means passes never stop — see PassTimeoutFor for why
// that turns a bounded pass into a continuous background scan.
func TestThePassBudgetLeavesRoomInsideTheConfiguredInterval(t *testing.T) {
	for _, interval := range []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, time.Hour} {
		budget := PassTimeoutFor(interval)
		require.Positive(t, budget, "interval %s produced a non-positive budget", interval)
		require.Less(t, budget, interval,
			"a %s interval got a %s budget; passes would run back to back with no idle", interval, budget)
		require.LessOrEqual(t, budget, DefaultPassTimeout,
			"a long interval must not raise the budget past the platform default")
	}
	// A short interval must still leave a usable budget rather than collapsing toward zero.
	require.GreaterOrEqual(t, PassTimeoutFor(30*time.Second), 20*time.Second)
	// And an unset interval falls back to the default rather than to nothing — the same
	// fail-safe direction every other bound here takes.
	require.Equal(t, DefaultPassTimeout, PassTimeoutFor(0))
}

// TestAZeroPassTimeoutMeansTheDefaultNotAnInstantExpiry is the fail-safe on the bound
// itself.
//
// 🔑 A ZERO DURATION HANDED STRAIGHT TO context.WithTimeout IS A DEADLINE ALREADY IN THE
// PAST, so every pass would end before its first tenant and reconciliation would be off
// while looking configured. The harmless-looking value has to land on the default, which
// is the same direction every other bound in the platform takes.
func TestAZeroPassTimeoutMeansTheDefaultNotAnInstantExpiry(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	reads := &slowProjection{}

	r := NewReconciler(tap, emptyInventory(), fakeTenants{tenantList(3)}, reads,
		ReconcileMetrics{}, time.Millisecond, time.Now, 0)

	require.Equal(t, DefaultPassTimeout, r.passTimeout)
	require.NoError(t, r.Run(context.Background()))
	require.Len(t, reads.seen, 3, "a zero timeout must not end the pass before it starts")
}
