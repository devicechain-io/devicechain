// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// scriptedWalker plays a sequence of per-pass answers, one entry per tenant per pass, so a
// test can model the ONE property that makes this a drain rather than a job: a released row
// leaves the set the next pass walks.
type scriptedWalker struct {
	mu     sync.Mutex
	passes [][]StoredDevice
	err    error
	calls  int
}

func (w *scriptedWalker) WalkAsserted(ctx context.Context, tenant, source string, fn func(StoredDevice) error) error {
	w.mu.Lock()
	i := w.calls
	w.calls++
	w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if i >= len(w.passes) {
		return nil
	}
	for _, d := range w.passes[i] {
		d.Tenant = tenant
		if err := fn(d); err != nil {
			return err
		}
	}
	return nil
}

type fixedTenants []string

func (f fixedTenants) TenantTokens(context.Context) ([]string, error) { return f, nil }

type failingTenants struct{ err error }

func (f failingTenants) TenantTokens(context.Context) ([]string, error) { return nil, f.err }

// nowaitWaiter paces nothing, so the drain's own logic is what the tests measure.
type nowaitWaiter struct{ calls int }

func (w *nowaitWaiter) Wait(context.Context) error { w.calls++; return nil }

func rows(tokens ...string) []StoredDevice {
	out := make([]StoredDevice, 0, len(tokens))
	for i, t := range tokens {
		out = append(out, StoredDevice{DeviceToken: t, SessionId: uint64(100 + i), Active: true})
	}
	return out
}

func demoterFor(t *testing.T, w AssertedWalker, gate Gate, tenants ...string) (*Demoter, *recordingEmitter, DemoterMetrics) {
	t.Helper()
	e := newRecordingEmitter()
	m := DemoterMetrics{
		Released:  prometheus.NewCounter(prometheus.CounterOpts{Name: "released"}),
		Remaining: prometheus.NewGauge(prometheus.GaugeOpts{Name: "remaining"}),
	}
	d := NewDemoter("mqtt1", NewPublisher("mqtt1", e, gate, testMetrics()),
		fixedTenants(tenants), w, &nowaitWaiter{}, m)
	return d, e, m
}

func allow(string, string, time.Time, bool) bool { return true }

// TestTheDrainEmptiesItself is the property that lets this be a ticker with no cursor, no
// retry budget and no persisted progress. A successful demotion removes the row from the
// set the walk reads, so an interrupted pass resumes for free and there is never a moment
// where the drain must decide whether to give up — which is the decision that would leave
// rows frozen forever while the condition that froze them is still true.
func TestTheDrainEmptiesItself(t *testing.T) {
	w := &scriptedWalker{passes: [][]StoredDevice{
		rows("a", "b", "c"),
		rows("a"), // two were applied; one was still asserted when this pass read
		{},        // and now the source has nothing left asserted
	}}
	d, e, m := demoterFor(t, w, allow, "acme")

	for pass, wantRemaining := range []float64{3, 1, 0} {
		require.NoError(t, d.Run(context.Background(), time.Unix(1_700_000_000, 0)))
		require.Equal(t, wantRemaining, testutil.ToFloat64(m.Remaining), "pass %d", pass)
	}
	require.Equal(t, 4.0, testutil.ToFloat64(m.Released))
	require.Len(t, e.all(), 4)
	for _, rec := range e.all() {
		require.NotNil(t, rec.Demotion, "the drain emitted a connectivity claim")
	}
}

// TestARefusedRowIsLeftForTheNextPass. A refusal is the platform declining to act — a
// deleted tenant, or one at its ingest ceiling — and it is emphatically not an error to
// abort the walk on. The row stays asserted, so it is still in the set next time, which is
// the same mechanism that makes the drain resumable.
func TestARefusedRowIsLeftForTheNextPass(t *testing.T) {
	w := &scriptedWalker{passes: [][]StoredDevice{rows("a", "b"), rows("a", "b")}}
	refuse := func(string, string, time.Time, bool) bool { return false }
	d, e, m := demoterFor(t, w, refuse, "acme")

	require.NoError(t, d.Run(context.Background(), time.Unix(1_700_000_000, 0)))
	require.Empty(t, e.all(), "a refused row still reached the stream")
	require.Equal(t, 0.0, testutil.ToFloat64(m.Released))
	require.Equal(t, 2.0, testutil.ToFloat64(m.Remaining), "a refused row must still count as outstanding")

	// The walk did not abort: both rows were visited, and the second pass sees them again.
	require.NoError(t, d.Run(context.Background(), time.Unix(1_700_000_060, 0)))
	require.Equal(t, 2.0, testutil.ToFloat64(m.Remaining))
}

// TestTheDrainIsMeteredAsLiveTraffic. The gate routes anything older than its backlog
// threshold to a separate limiter that accrues from the last timestamp it saw, so feeding
// it any event time re-accrues to burst on the next forward jump. Every emit path passes
// the zero time; a fleet-wide drain is the worst place to get that wrong.
func TestTheDrainIsMeteredAsLiveTraffic(t *testing.T) {
	var stamps []time.Time
	w := &scriptedWalker{passes: [][]StoredDevice{rows("a", "b")}}
	d, _, _ := demoterFor(t, w, func(_, _ string, sentAt time.Time, _ bool) bool {
		stamps = append(stamps, sentAt)
		return true
	}, "acme")

	require.NoError(t, d.Run(context.Background(), time.Unix(1_700_000_000, 0)))
	require.Len(t, stamps, 2)
	for _, s := range stamps {
		require.True(t, s.IsZero(), "the drain was metered at an event time: %v", s)
	}
}

// TestASessionlessRowIsReleasedRatherThanSkipped. A producer may send no session id, so an
// asserted row can hold zero — and a demotion applies only against the session on file, so
// zero is exactly what releases it. Skipping such rows would make them the one population
// the drain can never empty, and the gauge would stand at their count forever.
func TestASessionlessRowIsReleasedRatherThanSkipped(t *testing.T) {
	w := &scriptedWalker{passes: [][]StoredDevice{{{DeviceToken: "no-session", SessionId: 0, Active: true}}}}
	d, e, m := demoterFor(t, w, allow, "acme")

	require.NoError(t, d.Run(context.Background(), time.Unix(1_700_000_000, 0)))
	require.Equal(t, 1.0, testutil.ToFloat64(m.Released))
	rec := e.all()
	require.Len(t, rec, 1)
	require.NotNil(t, rec[0].Demotion)
	require.Equal(t, uint64(0), rec[0].Demotion.SessionId, "the row's own session must be carried, zero included")
}

// TestAnUnreadableTenantDoesNotStopTheOthers. The walk is all-or-nothing PER TENANT — a
// half-read page set is a wrong answer, not a short one — but one unreachable tenant must
// not stop every other tenant on the instance being repaired.
func TestAnUnreadableTenantDoesNotStopTheOthers(t *testing.T) {
	// The walker fails on its first call and succeeds after, so "acme" is skipped and
	// "beta" is drained in the same pass.
	w := &perTenantWalker{fail: map[string]error{"acme": errors.New("unreachable")},
		rows: map[string][]StoredDevice{"beta": rows("b1", "b2")}}
	d, e, _ := demoterFor(t, w, allow, "acme", "beta")

	require.NoError(t, d.Run(context.Background(), time.Unix(1_700_000_000, 0)),
		"one unreadable tenant failed the whole pass")
	require.Len(t, e.all(), 2)
	for _, rec := range e.all() {
		require.Equal(t, "beta", rec.Tenant)
	}
}

type perTenantWalker struct {
	fail map[string]error
	rows map[string][]StoredDevice
}

func (w *perTenantWalker) WalkAsserted(ctx context.Context, tenant, source string, fn func(StoredDevice) error) error {
	if err, bad := w.fail[tenant]; bad {
		return err
	}
	for _, d := range w.rows[tenant] {
		d.Tenant = tenant
		if err := fn(d); err != nil {
			return err
		}
	}
	return nil
}

// TestAFailedTenantListFailsThePass, in contrast: without the tenant list there is no work
// to do rather than less work, and reporting success would let the loop log nothing while
// the whole fleet stays frozen.
func TestAFailedTenantListFailsThePass(t *testing.T) {
	e := newRecordingEmitter()
	d := NewDemoter("mqtt1", NewPublisher("mqtt1", e, allow, testMetrics()),
		failingTenants{errors.New("user-management is down")}, &scriptedWalker{}, &nowaitWaiter{},
		DemoterMetrics{})
	require.Error(t, d.Run(context.Background(), time.Unix(1_700_000_000, 0)))
}

// TestTheDrainStopsWhenCancelled. A pass can be walking a very large cumulative set at 25
// rows a second, so shutdown has to be able to interrupt it mid-walk rather than wait it
// out — and what it leaves behind is simply rows that are still asserted.
func TestTheDrainStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := newRecordingEmitter()
	w := &scriptedWalker{passes: [][]StoredDevice{rows("a", "b", "c", "d")}}
	d := NewDemoter("mqtt1", NewPublisher("mqtt1", e, allow, testMetrics()),
		fixedTenants{"acme"}, w, &cancellingWaiter{after: 2, cancel: cancel}, DemoterMetrics{})

	err := d.Run(ctx, time.Unix(1_700_000_000, 0))
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, e.all(), 2, "the drain kept emitting after cancellation")
}

type cancellingWaiter struct {
	after  int
	calls  int
	cancel context.CancelFunc
}

func (w *cancellingWaiter) Wait(ctx context.Context) error {
	w.calls++
	if w.calls > w.after {
		w.cancel()
		return ctx.Err()
	}
	return nil
}

// TestStartDelayAppliesOnlyToTheAmbiguousReasons. A written `enabled: false` is a decision
// an operator made — Enabled is a *bool precisely so false is distinguishable from unset —
// so waiting on it would only delay a repair. A MISSING system-account credential is
// ambiguous: a bring-up mints it in the same run that starts the services, so an absent
// value can be a race with the run creating it, and demoting a fleet on the strength of a
// value that appears ninety seconds later would be a self-inflicted outage. An UNREACHABLE
// BROKER is ambiguous on the same clock and for the same reason — the bring-up rolls the
// NATS StatefulSet alongside the Deployments — so it waits too.
func TestStartDelayAppliesOnlyToTheAmbiguousReasons(t *testing.T) {
	const jitter = 7 * time.Second
	// 🔴 THE EXPECTED VALUES ARE LITERALS, NOT `SettleWindow + jitter`. Built from the
	// constant they are checking, they agree with it wherever it moves — including to
	// zero, which turns the settle window into no window at all and lets a fleet be
	// released the instant a bring-up races its own broker roll. The number is published
	// as two minutes in the concept page, the deployment page and the chart, so it is
	// asserted here the way a reader would check it.
	require.Equal(t, jitter, StartDelayFor(TapOffDisabled, jitter))
	require.Equal(t, 2*time.Minute+jitter, StartDelayFor(TapOffNoSystemCredential, jitter))
	require.Equal(t, 2*time.Minute+jitter, StartDelayFor(TapOffBrokerUnreachable, jitter))
	// The remaining reasons never start a drain at all, but the function is total and must
	// not accidentally hand one of them the settle window if that ever changes.
	for _, r := range []TapOffReason{TapOffNoGatewaySource, TapOffNoServiceAuth,
		TapOffSubscribeFailed} {
		require.Equal(t, jitter, StartDelayFor(r, jitter), "reason %s", r)
	}
}

// TestAllTapOffReasonsCoversTheVocabulary. The healthy path zeroes the gauge across this
// list; a reason missing from it can be SET and then never cleared, which for a
// restart-scoped signal is worse than no gauge — it reports a problem already fixed.
func TestAllTapOffReasonsCoversTheVocabulary(t *testing.T) {
	all := AllTapOffReasons()
	seen := map[TapOffReason]bool{}
	for _, r := range all {
		require.False(t, seen[r], "reason %s listed twice", r)
		require.NotEmpty(t, string(r))
		seen[r] = true
	}
	for _, r := range []TapOffReason{TapOffDisabled, TapOffNoSystemCredential, TapOffNoGatewaySource,
		TapOffNoServiceAuth, TapOffBrokerUnreachable, TapOffSubscribeFailed} {
		require.True(t, seen[r], "reason %s is declared but never cleared on the healthy path", r)
	}
}

// countingRunner records when each pass ran, so the loop's own timing is measurable without
// a projection or a stream behind it.
type countingRunner struct {
	mu    sync.Mutex
	runs  int
	err   error
	fired chan struct{}
}

func (r *countingRunner) Run(context.Context, time.Time) error {
	r.mu.Lock()
	r.runs++
	r.mu.Unlock()
	select {
	case r.fired <- struct{}{}:
	default:
	}
	return r.err
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

// TestTheLoopWaitsOutTheStartDelayThenRunsImmediately. Both halves matter: the delay is
// what stops a half-written configuration triggering a fleet-wide release, and running
// immediately AFTER it is what stops the first look at an already-frozen fleet waiting a
// full reconcile interval, which is measured in minutes.
func TestTheLoopWaitsOutTheStartDelayThenRunsImmediately(t *testing.T) {
	r := &countingRunner{fired: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunDemoteLoop(ctx, r, time.Hour, 60*time.Millisecond, time.Now)

	// Before the delay elapses, nothing has run.
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, 0, r.count(), "the drain ran before its start delay elapsed")

	select {
	case <-r.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the drain never ran after its start delay")
	}
	// The interval is an hour, so a second run would mean the loop ignored it.
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, r.count())
}

// TestTheLoopKeepsGoingAfterAFailedPass. A failed pass is the ordinary case while
// user-management is restarting, and giving up on it would leave the fleet frozen for the
// life of the process.
func TestTheLoopKeepsGoingAfterAFailedPass(t *testing.T) {
	r := &countingRunner{fired: make(chan struct{}, 8), err: errors.New("user-management is down")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunDemoteLoop(ctx, r, 15*time.Millisecond, 0, time.Now)

	for i := 0; i < 3; i++ {
		select {
		case <-r.fired:
		case <-time.After(2 * time.Second):
			t.Fatalf("the loop stopped after %d failed passes", i)
		}
	}
}

// TestTheLoopHonoursCancellationDuringTheStartDelay. A disabled instance that is shut down
// inside its settle window must not hold shutdown open for the whole window.
func TestTheLoopHonoursCancellationDuringTheStartDelay(t *testing.T) {
	r := &countingRunner{fired: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunDemoteLoop(ctx, r, time.Hour, time.Hour, time.Now); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop ignored cancellation during its start delay")
	}
	require.Equal(t, 0, r.count())
}
