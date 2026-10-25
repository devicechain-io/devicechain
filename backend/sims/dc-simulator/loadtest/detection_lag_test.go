// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/loadtest/detmonitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var lagT0 = time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)

func fallingEdges(n int, every time.Duration) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = lagT0.Add(time.Duration(i) * every)
	}
	return out
}

func clearedAtSnap(ts time.Time) alarmSnapshot {
	return alarmSnapshot{Found: true, State: alarmStateCleared, Severity: harnessAlarmSeverityWire,
		ClearedAt: ts.Format(time.RFC3339Nano)}
}

// A pipeline that keeps up: every poll finds the newest falling edge already in the alarm.
func TestLagTrackerKeepingUpPasses(t *testing.T) {
	falling := fallingEdges(100, 2*time.Second)
	tr := newLagTracker(30*time.Second, []string{"e1", "e2"})
	for i, e := range falling {
		now := e.Add(3 * time.Second)
		for _, tok := range []string{"e1", "e2"} {
			tr.observe(now, tok, clearedAtSnap(falling[i]), falling[:i+1])
		}
	}
	rep, inv := tr.report(len(falling))
	assert.True(t, inv.Passed, inv.Detail)
	assert.Equal(t, 200, rep.Detected)
	assert.Zero(t, rep.Undetected)
	assert.InDelta(t, 3, rep.MaxSeconds, 0.01)
}

// NEGATIVE CONTROL, the shape of the failure this invariant exists for: the early cycles are
// detected promptly, then detection stops while the probes keep cycling. Every probe's FINAL
// state is still "raised and cleared" (it reached that state in the early cycles), which is
// all alarm-liveness reads, so the old oracle passes this run. The lag invariant must fail it.
func TestLagTrackerStalledDetectionFailsWhereLivenessPasses(t *testing.T) {
	falling := fallingEdges(100, 2*time.Second)
	const detectedUpTo = 30 // cycles 0..29 are detected; detection then stops
	tr := newLagTracker(30*time.Second, []string{"e1"})
	stale := clearedAtSnap(falling[detectedUpTo-1])
	for i, e := range falling {
		now := e.Add(time.Second)
		snap := clearedAtSnap(falling[i])
		if i >= detectedUpTo {
			snap = stale
		}
		tr.observe(now, "e1", snap, falling[:i+1])
	}
	// The run ends and the final authoritative read still shows the stale state.
	tr.observe(falling[len(falling)-1].Add(2*time.Second), "e1", stale, falling)
	rep, inv := tr.report(len(falling))

	require.False(t, inv.Passed, "a pipeline that stopped detecting 70 cycles ago must not pass")
	assert.Contains(t, inv.Detail, "DETECTION FELL BEHIND")
	assert.Equal(t, 70, rep.Undetected)
	assert.Positive(t, rep.DeadlineMisses)

	// The end-state oracle sees nothing wrong with the same run.
	invs := classifyAlarms(map[string]alarmSnapshot{"s1": {}}, map[string]alarmSnapshot{"e1": stale}, true,
		DetectionConfig{MinAccepted: 1}, 10)
	for _, in := range invs {
		assert.True(t, in.Passed, "alarm-liveness and the rest pass this run, which is the gap: %s %s", in.Name, in.Detail)
	}
}

// Detection that is late but eventually catches up is still a finding: the deadline is per cycle.
func TestLagTrackerLateCycleFailsEvenAfterCatchingUp(t *testing.T) {
	falling := fallingEdges(10, 2*time.Second)
	tr := newLagTracker(30*time.Second, []string{"e1"})
	// Nothing is seen for 45 s after the first edge, then the alarm jumps to the newest edge.
	tr.observe(falling[0].Add(45*time.Second), "e1", alarmSnapshot{}, falling)
	tr.observe(falling[0].Add(50*time.Second), "e1", clearedAtSnap(falling[9]), falling)
	rep, inv := tr.report(10)
	assert.False(t, inv.Passed, inv.Detail)
	assert.Zero(t, rep.Undetected, "everything was seen in the end")
	assert.Greater(t, rep.MaxSeconds, 30.0)
}

// A probe that was never read proves nothing: inconclusive is a failure, never a pass.
func TestLagTrackerUnreadProbeIsInconclusive(t *testing.T) {
	tr := newLagTracker(30*time.Second, []string{"e1", "e2"})
	falling := fallingEdges(3, time.Second)
	tr.observe(falling[2].Add(time.Second), "e1", clearedAtSnap(falling[2]), falling)
	tr.pollFailed()
	_, inv := tr.report(3)
	assert.False(t, inv.Passed)
	assert.Contains(t, inv.Detail, "inconclusive")
}

// An unparseable clearedTime detects nothing (fail closed) rather than being read as current.
func TestLagTrackerUnparseableTimeDetectsNothing(t *testing.T) {
	falling := fallingEdges(2, time.Second)
	tr := newLagTracker(time.Second, []string{"e1"})
	bad := alarmSnapshot{Found: true, State: alarmStateCleared, Severity: harnessAlarmSeverityWire, ClearedAt: "not a time"}
	tr.observe(falling[1].Add(time.Minute), "e1", bad, falling)
	rep, inv := tr.report(2)
	assert.False(t, inv.Passed)
	assert.Equal(t, 2, rep.Undetected)
}

// --- the watcher, end to end against a scripted tenant ---------------------

// lagTenant plays the platform's detection path for the lag watcher: the alarm it serves shows
// the newest falling edge whose emit time is at least `delay` in the past, and shows nothing
// newer than `frozenAt` once frozen. delay and frozen are changed mid-run by the test.
type lagTenant struct {
	ps *probeStats

	mu     sync.Mutex
	delay  time.Duration
	frozen *time.Time
}

func (l *lagTenant) setDelay(d time.Duration) { l.mu.Lock(); l.delay = d; l.mu.Unlock() }
func (l *lagTenant) freeze() {
	l.mu.Lock()
	now := time.Now()
	l.frozen = &now
	l.mu.Unlock()
}

func (l *lagTenant) respond(map[string]any) (json.RawMessage, error) {
	l.mu.Lock()
	delay, frozen := l.delay, l.frozen
	l.mu.Unlock()
	cutoff := time.Now().Add(-delay)
	if frozen != nil && frozen.Before(cutoff) {
		cutoff = *frozen
	}
	var newest time.Time
	for _, e := range l.ps.fallingEmitTimes() {
		if !e.After(cutoff) {
			newest = e
		}
	}
	if newest.IsZero() {
		return alarmsJSON(0), nil
	}
	return alarmsJSON(1, map[string]any{
		"state": "CLEARED", "raisedTime": newest.Format(time.RFC3339Nano),
		"clearedTime": newest.Format(time.RFC3339Nano), "severity": harnessAlarmSeverityWire,
	}), nil
}

// runLagScenario cycles an edge probe every 15 ms for `cycles` cycles under a watcher with a 200 ms
// bound, calls mid(cycle) after each cycle, and returns the invariant the run would report,
// including the final authoritative observation RunDetectionProbes makes.
func runLagScenario(t *testing.T, cycles int, mid func(cycle int, lt *lagTenant)) (DetectionLag, Invariant) {
	t.Helper()
	ps := &probeStats{}
	lt := &lagTenant{ps: ps, delay: 20 * time.Millisecond}
	o := &alarmOracle{session: &fakeSession{respond: lt.respond}}
	tr := newLagTracker(250*time.Millisecond, []string{"e1"})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		(&lagWatcher{oracle: o, edge: []string{"e1"}, ps: ps, tracker: tr, poll: 10 * time.Millisecond}).run(ctx)
	}()
	for c := 0; c < cycles; c++ {
		ps.markFalling(time.Now())
		mid(c, lt)
		time.Sleep(15 * time.Millisecond)
	}
	// The drain: the run keeps watching for a while after the last cycle, as awaitAlarmsDrained does.
	time.Sleep(400 * time.Millisecond)
	cancel()
	wg.Wait()
	final, err := o.snapshot(context.Background(), "e1")
	require.NoError(t, err)
	tr.observe(time.Now(), "e1", final, ps.fallingEmitTimes())
	return tr.report(len(ps.fallingEmitTimes()))
}

func TestLagWatcherHealthyRunPasses(t *testing.T) {
	rep, inv := runLagScenario(t, 60, func(int, *lagTenant) {})
	assert.True(t, inv.Passed, inv.Detail)
	assert.Zero(t, rep.Undetected)
}

// NEGATIVE CONTROL: detection stops part-way through the run. The watcher must fail it.
func TestLagWatcherDetectionStoppedMidRunFails(t *testing.T) {
	rep, inv := runLagScenario(t, 60, func(c int, lt *lagTenant) {
		if c == 20 {
			lt.freeze()
		}
	})
	require.False(t, inv.Passed, "detection stopped at cycle 20 of 60 and the invariant passed: %s", inv.Detail)
	assert.Contains(t, inv.Detail, "DETECTION FELL BEHIND")
	assert.Positive(t, rep.Undetected)
}

// NEGATIVE CONTROL: detection is slowed, not stopped, past the bound mid-run, and then recovers.
func TestLagWatcherSlowedDetectionFails(t *testing.T) {
	_, inv := runLagScenario(t, 60, func(c int, lt *lagTenant) {
		switch c {
		case 20:
			lt.setDelay(800 * time.Millisecond) // over three times the bound
		case 45:
			lt.setDelay(20 * time.Millisecond)
		}
	})
	require.False(t, inv.Passed, "detection ran 800ms behind a 250ms bound and the invariant passed: %s", inv.Detail)
	assert.Contains(t, inv.Detail, "DETECTION FELL BEHIND")
}

// --- live socket rotation ---------------------------------------------------

type fakeSocket struct {
	id      int
	stopped bool
	report  detmonitor.Report
}

func (f *fakeSocket) Stop() error               { f.stopped = true; return nil }
func (f *fakeSocket) Report() detmonitor.Report { return f.report }

func TestRotatingMonitorReplacesTheSocketBeforeTheTokenExpires(t *testing.T) {
	var mu sync.Mutex
	var dialed []*fakeSocket
	var windows []time.Duration
	m := newRotatingMonitor(func(_ context.Context, window time.Duration) (liveSocket, error) {
		mu.Lock()
		defer mu.Unlock()
		s := &fakeSocket{id: len(dialed), report: detmonitor.Report{Observed: 2,
			BySeries: map[string]*detmonitor.SeriesTally{"e1": {Raised: 1, Resolved: 1}}}}
		dialed = append(dialed, s)
		windows = append(windows, window)
		return s, nil
	})
	m.every, m.retry = 30*time.Millisecond, 5*time.Millisecond
	require.NoError(t, m.Start(context.Background()))
	time.Sleep(200 * time.Millisecond)
	m.Stop()

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(dialed), 3, "a long run must re-dial")
	for _, s := range dialed {
		assert.True(t, s.stopped, "every segment is stopped, none leaks")
	}
	for _, w := range windows {
		assert.Equal(t, 30*time.Millisecond+tokenMargin, w, "each socket asks only for a token that outlives ITS window")
	}
	rep, redials := m.Report()
	assert.Equal(t, 2*len(dialed), rep.Observed, "tallies from every segment are merged")
	assert.Equal(t, len(dialed)-1, redials)
	assert.Equal(t, len(dialed), rep.BySeries["e1"].Raised)
	assert.Empty(t, rep.Violations)
}

// A failed re-dial is retried and reported; the run is not aborted and the failure is not hidden.
func TestRotatingMonitorReportsFailedRedials(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	m := newRotatingMonitor(func(context.Context, time.Duration) (liveSocket, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			return nil, assert.AnError
		}
		return &fakeSocket{}, nil
	})
	m.every, m.retry = 20*time.Millisecond, 5*time.Millisecond
	require.NoError(t, m.Start(context.Background()))
	time.Sleep(150 * time.Millisecond)
	m.Stop()
	rep, redials := m.Report()
	assert.Positive(t, redials, "the retry after the failure succeeded")
	require.NotEmpty(t, rep.Violations, "a blind interval is reported, not swallowed")
	assert.Equal(t, detmonitor.ViolLostView, rep.Violations[0].Kind)
}

func TestRotatingMonitorFirstDialFailureIsReturned(t *testing.T) {
	m := newRotatingMonitor(func(context.Context, time.Duration) (liveSocket, error) { return nil, assert.AnError })
	assert.Error(t, m.Start(context.Background()))
}
