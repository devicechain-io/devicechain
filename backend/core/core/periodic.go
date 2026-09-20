// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog/log"
)

// A PeriodicTask runs one maintenance pass on a fixed interval for the life of a component,
// and is a LifecycleComponent so it can be registered like any other.
//
// It exists because five services had written the same thing: the eight-method lifecycle
// octet, a procCtx/procCancel/WaitGroup trio, and a loop that selects on a ticker and a done
// channel. The loop bodies were identical to the character. What was NOT identical is the
// part that matters, so read the options below before assuming a site can adopt this: the
// context the loop runs under, whether a pass runs immediately at start, and what a pass is
// allowed to report are all genuine differences between those five, each argued in place.
//
// 🔴 IF YOU EMBED IT, DO NOT ALSO DEFINE ExecuteStop (or any other Execute* method) ON THE
// OUTER TYPE. The lifecycle manager holds the *PeriodicTask as its Component, so it keeps
// calling the task's methods; an outer definition shadows the promoted one for direct callers
// while the lifecycle never reaches it, and the outer one silently never runs. Work an adopter
// needs at stop belongs in its own LifecycleComponent, or in the pass.
//
// 🔴 WHAT IT DELIBERATELY DOES NOT OWN. The pass itself decides how to iterate tenants, and
// whether to take an advisory lock first. Three of the five adopters loop over tenants and
// two do not; of the three that do, one deliberately never calls WithTenant because its unit
// of work is one cross-table transaction keyed by token. Those are three different contracts
// wearing a family resemblance, and folding them in here would need a flag that changes the
// shape of the work rather than its schedule. A PeriodicTask schedules; it does not sweep.
type PeriodicTask struct {
	lifecycle LifecycleManager

	name      string
	interval  time.Duration
	run       func(context.Context) error
	immediate bool
	jitter    float64
	detached  bool
	metrics   *PeriodicTaskMetrics

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
}

// ErrPassSkipped is what a pass returns when it correctly did NOT run — the case that exists
// today is a replica declining because a peer holds the coordinator's advisory lock.
//
// 🔴 IT IS A DISTINCT OUTCOME BECAUSE THE ALTERNATIVES ARE BOTH WRONG. Reported as success it
// would move the last-success timestamp, so an instance whose every replica was skipping —
// a lock nobody releases — would look freshly swept forever. Reported as failure it would
// put a standing error rate on the normal operation of any service running more than one
// replica, which is the shape operators learn to mute.
var ErrPassSkipped = errors.New("pass skipped")

// ErrPassPartial marks a pass that ran and did some of its work. Wrap it (%w) when some
// items were swept and others could not be: `fmt.Errorf("%d of %d tenants: %w", n, total,
// ErrPassPartial)`. It is separate from failure because the two ask different questions of
// an operator — "is this sweep working" versus "is this sweep keeping up".
var ErrPassPartial = errors.New("pass partially completed")

// PassResult turns a per-item sweep's tally into the error a pass should return.
//
// 🔴 ALL-FAILED AND SOME-FAILED ARE DIFFERENT ANSWERS AND THAT IS THE WHOLE POINT. Every
// adopter loops over items logging a warning and continuing, which is right per item and
// wrong in aggregate: with the dependency down, EVERY item fails and the only alertable
// signal reads as healthy — a sweep that did nothing at all, reported as a sweep that found
// nothing to do. Those two states are indistinguishable in a log full of per-item warnings
// and they are the two an operator most needs to tell apart.
//
// All failed is ONE cause, not N: the database is down, the peer service is unreachable, or
// its query shape is refused during a rolling upgrade. So it is a plain failure and lands on
// the series operators page on. Some failed is ErrPassPartial — work is getting done, the
// sweep is falling behind on part of it — which answers "is this keeping up" rather than
// "is this working". A caller with nothing to tally should return its own error directly.
//
// visited of zero is success: a sweep with no work is working.
//
// 🔴 THE ALL-FAILED INFERENCE NEEDS A POPULATION, SO IT REQUIRES AT LEAST TWO. "Every item
// failed, therefore one cause" is a claim about a set, and a set of one licenses no such
// claim: one item failing is simply that item failing. It matters because one adopter's
// COMMON case is a single item — tenant purges are rare, so a pass usually visits one tenant
// — and without this a single wedged tenant would page every minute as though the database
// were down. With one item the honest report is partial: something did not complete, and
// nothing can yet be said about why.
func PassResult(visited, failed int, lastErr error) error {
	if failed == 0 {
		return nil
	}
	if lastErr == nil {
		// Defensive, and it earns its place because this is exported: every call site in
		// the tree sets lastErr in the same branch as failed++, but %w on a nil renders
		// "%!w(<nil>)" and produces an error that wraps nothing, so errors.Is on it
		// answers false for every sentinel and the pass classifies as a plain failure.
		lastErr = errors.New("no error was recorded")
	}
	if failed >= visited && visited > 1 {
		return fmt.Errorf("all %d failed: %w", visited, lastErr)
	}
	return fmt.Errorf("%d of %d not completed (last: %v): %w",
		failed, visited, lastErr, ErrPassPartial)
}

// Pass outcomes, as they appear in the outcome label.
//
// 🔴 THE PRECEDENCE IS LOAD-BEARING AND CANCELLED COMES FIRST. A pass interrupted by
// shutdown typically also returns an error — the context error, or whatever its first
// cancelled read produced — so an implementation that checked the error before the context
// would record a FAILURE on every graceful stop that caught a pass mid-flight. That is a
// false alert generated by the act of deploying, on a series an operator is meant to page
// on. presence.Reconciler reached the same conclusion independently and its comment says it
// plainly: cancelled "is the service stopping — which is not a fault and must not sit on the
// series operators alert from".
const (
	// PassComplete is a pass that ran and finished its work.
	PassComplete = "complete"
	// PassPartial is a pass that ran and did some of its work (see ErrPassPartial).
	PassPartial = "partial"
	// PassFailed is a pass that could not do its work.
	PassFailed = "failed"
	// PassSkipped is a pass that correctly did not run (see ErrPassSkipped).
	PassSkipped = "skipped"
	// PassCancelled is a pass cut short by shutdown. NOT a fault.
	PassCancelled = "cancelled"
)

// defaultPassJitter spreads each pass within ±10% of the interval.
//
// It is ON BY DEFAULT, which is a deliberate behaviour change for every site that adopts
// this: they ticked on an exact interval before. A fixed interval means N replicas that
// started together stay in step forever, so every sweep in the instance hits the database in
// the same instant and the load the interval exists to spread arrives as a spike. Ten percent
// is enough to decorrelate replicas within a few passes and small enough that no pass's
// spacing meaningfully changes.
const defaultPassJitter = 0.10

// PeriodicTaskMetrics are the operator signals for a maintenance pass. Nil-safe, like
// ProcessorMetrics: a task built without a Microservice records nothing and behaves the same.
type PeriodicTaskMetrics struct {
	passes      *prometheus.CounterVec
	duration    prometheus.Histogram
	lastSuccess prometheus.Gauge
}

// NewPeriodicTaskMetrics builds the three signals under this service's namespace.
func (ms *Microservice) NewPeriodicTaskMetrics(name string) *PeriodicTaskMetrics {
	sub, name := ms.requireMetricName("NewPeriodicTaskMetrics", name)
	auto := promauto.With(ms.MetricsRegisterer())
	m := &PeriodicTaskMetrics{
		passes: auto.NewCounterVec(prometheus.CounterOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_passes_total",
			Help: "Passes made by the " + name + " task, by outcome.",
		}, []string{"outcome"}),
		duration: auto.NewHistogram(prometheus.HistogramOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_pass_duration_seconds",
			Help: "How long one pass of the " + name + " task took.",
			// A maintenance pass is not a request. DefBuckets top out at ten seconds,
			// which would put every real sweep of a large instance in +Inf and answer
			// "how long does this take" with "more than ten seconds" forever.
			Buckets: []float64{.1, .5, 1, 5, 15, 60, 300, 900, 3600},
		}),
		lastSuccess: auto.NewGauge(prometheus.GaugeOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_last_success_timestamp_seconds",
			Help: "Unix time of the last pass of the " + name + " task that completed its work; " +
				"NaN until one has.",
		}),
	}
	// 🔴 NaN RATHER THAN THE ZERO A GAUGE IS BORN WITH, and it is the alert that makes this
	// matter. The natural rule over this series is `time() - X > threshold`, and against a
	// zero that reads as "last succeeded in 1970" — so every fresh pod fires it, on every
	// deploy, until its first pass lands. For an hourly sweep with no immediate pass that is
	// up to an hour of false alarm per replica, on the series this whole change exists to
	// give operators. NaN propagates through the subtraction and compares false, so the rule
	// stays silent until there is something true to say.
	m.lastSuccess.Set(math.NaN())
	return m
}

// record files one pass. Nil-safe.
//
// 🔴 ONLY complete AND partial MOVE THE LAST-SUCCESS GAUGE, and partial does so on purpose:
// it means work was done, so "nothing has succeeded since T" would be false. skipped does
// not move it — see ErrPassSkipped for why that distinction has to survive.
func (m *PeriodicTaskMetrics) record(outcome string, d time.Duration, now time.Time) {
	if m == nil {
		return
	}
	m.passes.WithLabelValues(outcome).Inc()
	// 🔑 A SKIPPED PASS IS NOT TIMED. It is a replica finding the lock held and returning in
	// about a millisecond, and the histogram carries no outcome label — so on a three-replica
	// service two thirds of the observations would be lock declines and the histogram could
	// no longer answer the one question it exists for, which is how long a pass takes.
	if outcome != PassSkipped {
		m.duration.Observe(d.Seconds())
	}
	if outcome == PassComplete || outcome == PassPartial {
		m.lastSuccess.Set(float64(now.Unix()))
	}
}

// PeriodicTaskOption configures a task at construction.
type PeriodicTaskOption func(*PeriodicTask)

// WithImmediateFirstPass runs one pass at start, before the first interval elapses.
//
// The case it exists for: an operator who just asked for something is watching, and a task
// that appears to do nothing for a whole interval is indistinguishable from one that is
// broken. Off by default, because for a sweep nobody is waiting on it only moves load to
// startup, where every other component is also starting.
func WithImmediateFirstPass() PeriodicTaskOption {
	return func(t *PeriodicTask) { t.immediate = true }
}

// WithoutJitter pins the task to an exact interval. Only for a task whose spacing is itself
// the contract; see defaultPassJitter for why the default is the other way.
func WithoutJitter() PeriodicTaskOption {
	return func(t *PeriodicTask) { t.jitter = 0 }
}

// WithDetachedContext runs the loop under a context derived from context.Background() rather
// than from the one Initialize was given, so only Stop ends it.
//
// 🔴 THIS IS A REAL DIFFERENCE AND IT IS AN OPTION RATHER THAN A NORMALISATION. Core cancels
// the root context BEFORE it calls teardown, so a task holding the Initialize context has its
// in-flight pass cut short at that moment, while a detached one runs the pass to completion
// and stops when Stop cancels it — bounded by the teardown budget either way. One adopter was
// already written this way, by discarding its Initialize argument; that read as an oversight
// and was indistinguishable from one. Naming it makes it a decision somebody can disagree with.
func WithDetachedContext() PeriodicTaskOption {
	return func(t *PeriodicTask) { t.detached = true }
}

// WithPassMetrics attaches the operator signals. Omitted, the task records nothing.
func WithPassMetrics(m *PeriodicTaskMetrics) PeriodicTaskOption {
	return func(t *PeriodicTask) { t.metrics = m }
}

// NewPeriodicTask builds a task that calls run every interval. name is used for the lifecycle
// component name and in log lines; the metric names come from PeriodicTaskMetrics.
func NewPeriodicTask(area, name string, interval time.Duration, run func(context.Context) error,
	callbacks LifecycleCallbacks, opts ...PeriodicTaskOption) *PeriodicTask {
	// 🔴 A NON-POSITIVE INTERVAL PANICS, AND THAT IS A PROPERTY BEING PRESERVED RATHER THAN
	// ADDED. Every adopter built time.NewTicker(interval), which panics on a non-positive
	// duration — a loud refusal at start. This type schedules with a Timer instead, and
	// time.NewTimer(0) fires immediately and re-fires after every pass: measured at ~290,000
	// passes in 100ms, a hot loop hammering the database rather than a service that refuses
	// to start. Every current caller guards its config before constructing one; the next one
	// will not, and "disabled" must be expressed by not building the task at all.
	if interval <= 0 {
		panic(fmt.Sprintf("core: periodic task %q needs a positive interval, got %s — to disable "+
			"a task, do not construct it", name, interval))
	}
	t := &PeriodicTask{name: name, interval: interval, run: run, jitter: defaultPassJitter}
	for _, opt := range opts {
		opt(t)
	}
	t.lifecycle = NewLifecycleManager(fmt.Sprintf("%s-%s", area, name), t, callbacks)
	return t
}

func (t *PeriodicTask) Initialize(ctx context.Context) error { return t.lifecycle.Initialize(ctx) }

func (t *PeriodicTask) ExecuteInitialize(ctx context.Context) error {
	base := ctx
	if t.detached {
		base = context.Background()
	}
	t.procCtx, t.procCancel = context.WithCancel(base)
	return nil
}

func (t *PeriodicTask) Start(ctx context.Context) error { return t.lifecycle.Start(ctx) }

func (t *PeriodicTask) ExecuteStart(context.Context) error {
	t.wg.Add(1)
	go t.loop()
	return nil
}

func (t *PeriodicTask) Stop(ctx context.Context) error { return t.lifecycle.Stop(ctx) }

func (t *PeriodicTask) ExecuteStop(context.Context) error {
	if t.procCancel != nil {
		t.procCancel()
	}
	t.wg.Wait()
	return nil
}

func (t *PeriodicTask) Terminate(ctx context.Context) error { return t.lifecycle.Terminate(ctx) }

func (t *PeriodicTask) ExecuteTerminate(context.Context) error { return nil }

// loop is the body the five adopters each had their own copy of.
func (t *PeriodicTask) loop() {
	defer t.wg.Done()
	if t.immediate {
		t.once()
	}
	timer := time.NewTimer(t.next())
	defer timer.Stop()
	for {
		select {
		case <-t.procCtx.Done():
			return
		case <-timer.C:
			t.once()
			// Reset AFTER the pass, so the interval is the gap BETWEEN passes rather
			// than a deadline the pass shares with its own runtime. A ticker measures
			// from tick to tick, so a pass that outruns its interval leaves no gap at
			// all and the next fires the instant it returns.
			timer.Reset(t.next())
		}
	}
}

// next is the wait before the next pass, jittered.
func (t *PeriodicTask) next() time.Duration {
	if t.jitter <= 0 {
		return t.interval
	}
	// rand is fine here and crypto/rand is not wanted: this decorrelates replicas, it
	// does not defend against anything.
	spread := float64(t.interval) * t.jitter
	return time.Duration(float64(t.interval) - spread + rand.Float64()*2*spread)
}

// once runs a single pass and files what happened.
func (t *PeriodicTask) once() {
	start := time.Now()
	err := t.run(t.procCtx)
	outcome := classifyPass(t.procCtx, err)
	t.metrics.record(outcome, time.Since(start), time.Now())

	switch outcome {
	case PassComplete, PassCancelled:
		// Nothing to say. A completed pass logs its own findings if it has any, and a
		// cancelled one is the operator's own deploy.
	case PassSkipped:
		log.Debug().Str("task", t.name).Msg("Maintenance pass skipped; another replica holds the lock.")
	case PassPartial:
		log.Warn().Err(err).Str("task", t.name).Msg("Maintenance pass completed only part of its work.")
	default:
		log.Error().Err(err).Str("task", t.name).Msg("Maintenance pass failed.")
	}
}

// classifyPass maps a pass's context and error onto an outcome.
//
// The context is consulted FIRST and unconditionally — see the outcome constants for why
// that ordering is the whole point of this function existing separately from once().
func classifyPass(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return PassCancelled
	}
	switch {
	case err == nil:
		return PassComplete
	case errors.Is(err, ErrPassSkipped):
		return PassSkipped
	case errors.Is(err, ErrPassPartial):
		return PassPartial
	default:
		return PassFailed
	}
}
