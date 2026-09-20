// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACancelledPassIsNotAFailure is the reason classifyPass exists as its own function.
//
// 🔴🔴 THIS IS THE ONE ASSERTION IN THE FILE THAT IS NOT ABOUT MECHANISM. A pass interrupted
// by shutdown almost always ALSO returns an error — the context error itself, or whatever its
// first cancelled read produced — so an implementation that consulted the error before the
// context would record a FAILURE on every graceful stop that caught a pass mid-flight. Thirteen
// loops were about to get this metric; that is thirteen series an operator pages on, each
// incrementing once per deploy, forever. The table below feeds the cancelled context EVERY
// error shape a pass can return, because "cancelled wins" is only true if it wins over all
// of them, and the cheap version of this test — one nil error and one cancelled context —
// passes just as well when the precedence is inverted.
func TestACancelledPassIsNotAFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, err := range []error{
		nil,
		context.Canceled,
		errors.New("some read failed"),
		fmt.Errorf("wrapped: %w", context.Canceled),
		ErrPassSkipped,
		ErrPassPartial,
		fmt.Errorf("3 of 9 tenants: %w", ErrPassPartial),
	} {
		got := classifyPass(cancelled, err)
		assert.Equalf(t, PassCancelled, got,
			"a cancelled context outranks every error shape, including %v", err)
	}
}

// TestAnUncancelledPassIsClassifiedByItsError is the other half: with a live context the error
// decides, and each of the four outcomes is reachable. Without this, "return cancelled always"
// would satisfy the test above.
func TestAnUncancelledPassIsClassifiedByItsError(t *testing.T) {
	live := context.Background()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"no error", nil, PassComplete},
		{"skipped", ErrPassSkipped, PassSkipped},
		{"skipped, wrapped", fmt.Errorf("lock held: %w", ErrPassSkipped), PassSkipped},
		{"partial", ErrPassPartial, PassPartial},
		{"partial, wrapped", fmt.Errorf("3 of 9: %w", ErrPassPartial), PassPartial},
		{"anything else", errors.New("listing tenants: connection refused"), PassFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyPass(live, tc.err))
		})
	}
}

// newTaskMetrics builds the three signals on a registry the test can read back.
func newTaskMetrics(t *testing.T, name string) (*PeriodicTaskMetrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ms := &Microservice{FunctionalArea: "test-area"}
	ms.UseMetricsRegistry(reg)
	return ms.NewPeriodicTaskMetrics(name), reg
}

// histogramCount returns how many observations a histogram has taken. testutil.ToFloat64
// refuses a histogram and CollectAndCount counts metric FAMILIES — it answers 1 for a
// histogram with no observations at all, so an assertion built on it cannot see the
// difference this test is about.
func histogramCount(t *testing.T, reg *prometheus.Registry, name string) uint64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			return m.GetHistogram().GetSampleCount()
		}
	}
	return 0
}

// TestOnlyAPassThatDidWorkMovesTheLastSuccessGauge pins the distinction ErrPassSkipped exists
// for.
//
// 🔴 SKIPPED MUST NOT MOVE IT. A skipped pass is a replica declining because a peer holds the
// lock, and if that counted as success then an instance whose every replica was skipping — a
// lock nobody released — would report itself freshly swept forever, which is precisely the
// outage this gauge is meant to reveal. Partial DOES move it, because work was done and
// "nothing has succeeded since T" would be a false statement.
func TestOnlyAPassThatDidWorkMovesTheLastSuccessGauge(t *testing.T) {
	for _, tc := range []struct {
		outcome  string
		expectTs bool
	}{
		{PassComplete, true},
		{PassPartial, true},
		{PassSkipped, false},
		{PassFailed, false},
		{PassCancelled, false},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			m, reg := newTaskMetrics(t, "sweep")
			m.record(tc.outcome, time.Second, time.Unix(1700000000, 0))

			got := testutil.ToFloat64(m.lastSuccess)
			if tc.expectTs {
				assert.EqualValues(t, 1700000000, got)
			} else {
				// NaN, not zero: the gauge starts at NaN so `time() - X > threshold`
				// stays silent until a pass has actually succeeded. Asserting zero here
				// would pass for a gauge that had never been initialised either.
				assert.Truef(t, math.IsNaN(got),
					"%s must not be reported as a successful sweep (got %v)", tc.outcome, got)
			}
			assert.EqualValues(t, 1, testutil.ToFloat64(m.passes.WithLabelValues(tc.outcome)),
				"every outcome is counted, whether or not it moved the gauge")
			assert.EqualValues(t, map[bool]int{true: 1, false: 0}[tc.outcome != PassSkipped],
				histogramCount(t, reg, "devicechain_testarea_sweep_pass_duration_seconds"),
				"every outcome but skipped is timed")
		})
	}
}

// TestNilMetricsRecordNothingAndDoNotPanic covers the nil-safety ProcessorMetrics also has:
// a task built without metrics must behave identically, so a unit test can build one.
func TestNilMetricsRecordNothingAndDoNotPanic(t *testing.T) {
	var m *PeriodicTaskMetrics
	assert.NotPanics(t, func() { m.record(PassComplete, time.Second, time.Now()) })
}

// TestJitterStaysInsideItsBandAndVaries pins both halves, because each alone is satisfied by a
// mistake: returning the exact interval every time is "inside the band", and returning a random
// duration unrelated to the interval "varies".
func TestJitterStaysInsideItsBandAndVaries(t *testing.T) {
	// interval is a variable so the bounds below are computed at run time. As constants
	// they do not compile: 1.1 x 60e9 is not an exact integer in binary, so the untyped
	// float constant has no time.Duration representation.
	interval := time.Minute
	task := &PeriodicTask{interval: interval, jitter: defaultPassJitter}
	lo := time.Duration(float64(interval) * (1 - defaultPassJitter))
	hi := time.Duration(float64(interval) * (1 + defaultPassJitter))

	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := task.next()
		require.GreaterOrEqual(t, d, lo, "jittered interval fell below the band")
		require.LessOrEqual(t, d, hi, "jittered interval rose above the band")
		seen[d] = true
	}
	assert.Greater(t, len(seen), 100,
		"200 draws that produced fewer than 100 distinct values are not decorrelating anything")
}

// TestTheDefaultIsJittered pins that NewPeriodicTask APPLIES the default, which the test
// above does not: it builds the struct literally and so exercises next() while saying nothing
// about how a task built the normal way is configured. Found by mutation — dropping the
// jitter field from the constructor left every other test in this file green.
func TestTheDefaultIsJittered(t *testing.T) {
	task := NewPeriodicTask("test-area", "sweep", time.Minute, func(context.Context) error {
		return nil
	}, NewNoOpLifecycleCallbacks())
	assert.Equal(t, defaultPassJitter, task.jitter,
		"a task built without options must be jittered; replicas that tick in lockstep are the "+
			"reason the default is not 'exact'")

	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[task.next()] = true
	}
	assert.Greater(t, len(seen), 25, "and the field must actually reach next()")
}

// TestWithoutJitterIsExact is the counterweight: a task whose spacing is its contract gets
// exactly the interval, so the option is not decorative.
func TestWithoutJitterIsExact(t *testing.T) {
	task := &PeriodicTask{interval: time.Minute}
	WithoutJitter()(task)
	for i := 0; i < 20; i++ {
		assert.Equal(t, time.Minute, task.next())
	}
}

// newTestTask builds a started task over run, and returns a stop function.
func newTestTask(t *testing.T, run func(context.Context) error, opts ...PeriodicTaskOption) *PeriodicTask {
	t.Helper()
	task := NewPeriodicTask("test-area", "sweep", 20*time.Millisecond, run, NewNoOpLifecycleCallbacks(), opts...)
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	t.Cleanup(func() { _ = task.Stop(context.Background()) })
	return task
}

// TestTheImmediateFirstPassRunsBeforeTheInterval pins the option the purge coordinator needs.
// The interval here is far longer than the wait, so a pass observed at all can only have been
// the immediate one.
func TestTheImmediateFirstPassRunsBeforeTheInterval(t *testing.T) {
	var passes atomic.Int64
	task := NewPeriodicTask("test-area", "sweep", time.Hour, func(context.Context) error {
		passes.Add(1)
		return nil
	}, NewNoOpLifecycleCallbacks(), WithImmediateFirstPass(), WithoutJitter())
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	defer func() { _ = task.Stop(context.Background()) }()

	assert.Eventually(t, func() bool { return passes.Load() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"an hour-long interval means the only pass that can have run is the immediate one")
}

// TestWithoutTheOptionNoPassRunsAtStart is the negative control for the test above: the option
// has to be what causes the pass, not the start.
func TestWithoutTheOptionNoPassRunsAtStart(t *testing.T) {
	var passes atomic.Int64
	task := NewPeriodicTask("test-area", "sweep", time.Hour, func(context.Context) error {
		passes.Add(1)
		return nil
	}, NewNoOpLifecycleCallbacks(), WithoutJitter())
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	defer func() { _ = task.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, passes.Load(), "with no immediate-pass option the first pass waits a full interval")
}

// TestStopWaitsForAPassInFlight pins that ExecuteStop joins rather than abandoning. A pass
// still writing when Stop returned would be writing after the database it uses was told to
// close.
func TestStopWaitsForAPassInFlight(t *testing.T) {
	release := make(chan struct{})
	var finished atomic.Bool
	task := NewPeriodicTask("test-area", "sweep", time.Millisecond, func(context.Context) error {
		<-release
		finished.Store(true)
		return nil
	}, NewNoOpLifecycleCallbacks(), WithoutJitter())
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))

	// Wait until the pass is definitely in flight, then stop while it is.
	time.Sleep(50 * time.Millisecond)
	stopped := make(chan error, 1)
	go func() { stopped <- task.Stop(context.Background()) }()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a pass was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-stopped)
	assert.True(t, finished.Load(), "Stop must not return before the in-flight pass finishes")
}

// TestADetachedTaskSurvivesTheInitializeContext pins the divergence WithDetachedContext names.
//
// Core cancels the root context before it calls teardown, so whether a task holds that context
// decides whether its in-flight pass is cut short there or runs on until Stop. One adopter was
// already written the detached way by discarding its Initialize argument; this is what that
// choice actually does, asserted in both directions.
func TestADetachedTaskSurvivesTheInitializeContext(t *testing.T) {
	for _, tc := range []struct {
		name         string
		opts         []PeriodicTaskOption
		wantCancel   bool
		descriptions string
	}{
		{"attached", []PeriodicTaskOption{WithoutJitter()}, true,
			"a task holding the Initialize context stops when that context is cancelled"},
		{"detached", []PeriodicTaskOption{WithoutJitter(), WithDetachedContext()}, false,
			"a detached task keeps running until Stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var passes atomic.Int64
			task := NewPeriodicTask("test-area", "sweep", 10*time.Millisecond, func(context.Context) error {
				passes.Add(1)
				return nil
			}, NewNoOpLifecycleCallbacks(), tc.opts...)

			initCtx, cancelInit := context.WithCancel(context.Background())
			require.NoError(t, task.Initialize(initCtx))
			require.NoError(t, task.Start(context.Background()))
			defer func() { _ = task.Stop(context.Background()) }()

			require.Eventually(t, func() bool { return passes.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)
			cancelInit()
			time.Sleep(60 * time.Millisecond)
			before := passes.Load()
			time.Sleep(120 * time.Millisecond)
			after := passes.Load()

			if tc.wantCancel {
				assert.Equal(t, before, after, tc.descriptions)
			} else {
				assert.Greater(t, after, before, tc.descriptions)
			}
		})
	}
}

// TestTheIntervalIsTheGapBetweenPasses pins the timer.Reset-after-the-pass choice. With a
// ticker, a pass that outruns its interval leaves no gap at all: the next tick is already
// queued and fires the instant the pass returns, so a slow sweep becomes a hot loop.
func TestTheIntervalIsTheGapBetweenPasses(t *testing.T) {
	var passes atomic.Int64
	// newTestTask's interval, named here so the arithmetic below cannot drift from it.
	const interval = 20 * time.Millisecond
	const passTime = 60 * time.Millisecond // deliberately longer than the interval
	task := newTestTask(t, func(context.Context) error {
		passes.Add(1)
		time.Sleep(passTime)
		return nil
	}, WithoutJitter())
	require.NotNil(t, task)

	const window = 500 * time.Millisecond
	time.Sleep(window)
	// Each cycle costs at least interval+passTime = 80ms, so the window admits at most 6
	// (plus one for the pass already running when it ends). With ticker semantics the next
	// tick is already queued when a slow pass returns, so there is no gap at all and the
	// count runs to window/passTime = 8 and upward.
	const atMost = int64(window/(interval+passTime)) + 1
	assert.LessOrEqualf(t, passes.Load(), atMost,
		"passes are not separated by the interval; the timer is firing as fast as the pass "+
			"returns (got %d, at most %d)", passes.Load(), atMost)
}

// TestTheLoopFilesTheOutcomeItActuallySaw runs a REAL task, through its own loop, with metrics
// attached, and reads the counter back.
//
// 🔴🔴 IT EXISTS BECAUSE EVERY OTHER TEST HERE CALLS classifyPass AND record DIRECTLY, WHICH
// LEAVES THE ONE LINE THAT CONNECTS THEM UNTESTED. Two mutations survived the whole file:
// passing context.Background() to classifyPass instead of the task's own context — which files
// every shutdown-interrupted pass as a FAILURE, precisely the false alert this design was built
// to prevent — and hard-coding the outcome passed to record. Both are single-token edits on the
// only line where the classification reaches the metric, and the suite was green for both.
// Testing a function is not testing its caller.
func TestTheLoopFilesTheOutcomeItActuallySaw(t *testing.T) {
	t.Run("a pass cut short by Stop is filed as cancelled, not failed", func(t *testing.T) {
		m, _ := newTaskMetrics(t, "sweep")
		started := make(chan struct{})
		var once sync.Once
		task := NewPeriodicTask("test-area", "sweep", time.Hour, func(ctx context.Context) error {
			once.Do(func() { close(started) })
			<-ctx.Done()
			// The shape that makes this worth pinning: the pass reports an error, and
			// it is the context's own. Classified by the error alone it is a failure.
			return ctx.Err()
		}, NewNoOpLifecycleCallbacks(), WithImmediateFirstPass(), WithoutJitter(), WithPassMetrics(m))
		require.NoError(t, task.Initialize(context.Background()))
		require.NoError(t, task.Start(context.Background()))
		<-started
		require.NoError(t, task.Stop(context.Background()))

		assert.EqualValues(t, 1, testutil.ToFloat64(m.passes.WithLabelValues(PassCancelled)),
			"a pass interrupted by shutdown is cancelled")
		assert.Zero(t, testutil.ToFloat64(m.passes.WithLabelValues(PassFailed)),
			"and it must not also land on the series operators page on")
		assert.True(t, math.IsNaN(testutil.ToFloat64(m.lastSuccess)),
			"no pass has completed, so there is no last success to report")
	})

	t.Run("a skipped pass is filed as skipped and is not timed", func(t *testing.T) {
		m, reg := newTaskMetrics(t, "sweep")
		done := make(chan struct{})
		var once sync.Once
		task := NewPeriodicTask("test-area", "sweep", time.Hour, func(context.Context) error {
			once.Do(func() { close(done) })
			return ErrPassSkipped
		}, NewNoOpLifecycleCallbacks(), WithImmediateFirstPass(), WithoutJitter(), WithPassMetrics(m))
		require.NoError(t, task.Initialize(context.Background()))
		require.NoError(t, task.Start(context.Background()))
		<-done
		require.NoError(t, task.Stop(context.Background()))

		assert.EqualValues(t, 1, testutil.ToFloat64(m.passes.WithLabelValues(PassSkipped)))
		assert.True(t, math.IsNaN(testutil.ToFloat64(m.lastSuccess)),
			"declining to run is not succeeding; an instance where every replica skipped "+
				"would otherwise report itself freshly swept forever")
		assert.EqualValues(t, 0, histogramCount(t, reg, "devicechain_testarea_sweep_pass_duration_seconds"),
			"a lock decline is not a pass, and timing it would swamp the histogram on a "+
				"multi-replica service")
	})

	t.Run("a completed pass is filed as complete and stamps the gauge", func(t *testing.T) {
		m, _ := newTaskMetrics(t, "sweep")
		done := make(chan struct{})
		var once sync.Once
		task := NewPeriodicTask("test-area", "sweep", time.Hour, func(context.Context) error {
			once.Do(func() { close(done) })
			return nil
		}, NewNoOpLifecycleCallbacks(), WithImmediateFirstPass(), WithoutJitter(), WithPassMetrics(m))
		require.NoError(t, task.Initialize(context.Background()))
		require.NoError(t, task.Start(context.Background()))
		<-done
		require.NoError(t, task.Stop(context.Background()))

		assert.EqualValues(t, 1, testutil.ToFloat64(m.passes.WithLabelValues(PassComplete)))
		assert.False(t, math.IsNaN(testutil.ToFloat64(m.lastSuccess)),
			"a completed pass is what the last-success gauge is for")
	})
}

// TestANonPositiveIntervalIsRefused pins the loud refusal a time.Ticker used to give for free.
//
// Every adopter built time.NewTicker(interval), which panics on a non-positive duration. This
// type schedules with a Timer, and time.NewTimer(0) fires immediately and keeps re-firing —
// measured at roughly 290,000 passes in 100ms, which is a service hammering its database
// rather than one that refuses to start. "Disabled" has to be expressed by not building it.
func TestANonPositiveIntervalIsRefused(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		assert.Panicsf(t, func() {
			NewPeriodicTask("test-area", "sweep", interval, func(context.Context) error { return nil },
				NewNoOpLifecycleCallbacks())
		}, "an interval of %s must be refused at construction, not become a hot loop", interval)
	}
	assert.NotPanics(t, func() {
		NewPeriodicTask("test-area", "sweep", time.Nanosecond, func(context.Context) error { return nil },
			NewNoOpLifecycleCallbacks())
	}, "the guard is on non-positive, not on small")
}

// TestPassResultSeparatesABacklogFromAnOutage covers the classification directly. It was
// previously reachable only through a test in another module, which meant core could change
// the rule and stay green.
func TestPassResultSeparatesABacklogFromAnOutage(t *testing.T) {
	boom := errors.New("connection refused")

	assert.NoError(t, PassResult(0, 0, nil), "a sweep with no work is working")
	assert.NoError(t, PassResult(9, 0, nil))

	partial := PassResult(9, 3, boom)
	require.Error(t, partial)
	assert.ErrorIs(t, partial, ErrPassPartial, "some failing is a backlog")

	all := PassResult(9, 9, boom)
	require.Error(t, all)
	assert.NotErrorIs(t, all, ErrPassPartial, "all failing is one cause and belongs on the failure series")

	// 🔴 A POPULATION OF ONE LICENSES NO INFERENCE. One adopter's common case is a single
	// item — tenant purges are rare — so without this a single wedged tenant would page
	// every minute as though the whole dependency were down.
	one := PassResult(1, 1, boom)
	require.Error(t, one)
	assert.ErrorIs(t, one, ErrPassPartial,
		"one item failing is that item failing; it says nothing about a common cause")

	// Exported, so the shapes no in-tree caller produces still have to behave.
	nilErr := PassResult(4, 4, nil)
	require.Error(t, nilErr)
	assert.NotContains(t, nilErr.Error(), "%!w",
		"a nil lastErr must not render as a formatting escape, and must still wrap something")
}

// TestRecordPassOnNilMetricsStillLogs pins the nil-safety the loops that cannot adopt
// PeriodicTask rely on.
//
// 🔑 THE LOG LINE IS NOT A METRIC. Three of the five adopting loops are reachable from tests
// and fixtures that pass no metrics at all — event-sources' demote loop is called with a nil
// in four tests — and a task without instruments is still a task doing work. Classifying and
// logging must survive; only the recording is skipped.
func TestRecordPassOnNilMetricsStillLogs(t *testing.T) {
	var m *PeriodicTaskMetrics
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"complete", context.Background(), nil},
		{"failed", context.Background(), errors.New("boom")},
		{"skipped", context.Background(), ErrPassSkipped},
		{"partial", context.Background(), ErrPassPartial},
		{"cancelled", cancelled, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				m.RecordPass(tc.ctx, tc.err, time.Now(), "sweep")
			})
		})
	}
}
