// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DETECTION LAG: the oracle's missing dimension.
//
// alarm-liveness reads the probes' alarms AFTER the run and asks whether each completed
// raised→cleared. That is an END STATE, and a pipeline running 18 minutes behind real time
// reaches the same end state eventually, having already reached it in the first cycles: the
// release-candidate run passed this oracle with its detection input 17m43s late, because the
// probes' early cycles had left every alarm CLEARED long before the late ones were read.
//
// A cycle's falling edge is emitted at a time the harness knows. The durable alarm's
// clearedTime is the occurred-time the sim stamped on the event that cleared it (the
// raise-alarm consumer copies the event's occurredTime), so it comes from the HARNESS'S OWN
// clock, with no skew against the emit times recorded here. The alarm of an edge probe is
// therefore current when its clearedTime has reached the emit time of the newest falling
// edge, and each falling edge has a deadline: its alarm must show it within the lag bound.
// The check does not need the live stream (which cannot outlive a short-lived access token)
// and it cannot be satisfied by an earlier cycle's state: that is the difference from
// alarm-liveness.

// lagClockTolerance absorbs the sim's microsecond truncation of the occurred-time it stamps.
const lagClockTolerance = time.Millisecond

// DefaultDetectionLagBound is how late a cycle's alarm transition may be: the time from the
// harness emitting the cycle's falling edge to the durable alarm showing it. A healthy run
// measures a few seconds (alarmSettleSeconds 2.1 s in the release-candidate run's early
// cycles), so this is a bound a healthy pipeline sits well inside and a backlog does not.
const DefaultDetectionLagBound = 30 * time.Second

// InvDetectionLag is the invariant's report key.
const InvDetectionLag = "detection-lag"

// probeLag is one edge probe's progress through its cycles.
type probeLag struct {
	// next is the index of the oldest falling edge not yet seen in the durable alarm.
	next int
	// lags[i] is how long after falling edge i was emitted it was first seen in the alarm.
	lags []time.Duration
	// polls counts the successful reads of this probe; a probe never read proves nothing.
	polls int
}

// lagTracker turns the durable alarm reads taken DURING the run into per-cycle detection
// lags and deadline misses. It is pure apart from its lock: the caller supplies the times.
type lagTracker struct {
	bound time.Duration

	mu         sync.Mutex
	probes     map[string]*probeLag
	pollErrors int
	// missed is the first deadline miss, kept for the report; missCount counts them all.
	missed    string
	missCount int
}

func newLagTracker(bound time.Duration, edgeTokens []string) *lagTracker {
	t := &lagTracker{bound: bound, probes: make(map[string]*probeLag, len(edgeTokens))}
	for _, tok := range edgeTokens {
		t.probes[tok] = &probeLag{}
	}
	return t
}

func (t *lagTracker) pollFailed() {
	t.mu.Lock()
	t.pollErrors++
	t.mu.Unlock()
}

// observe records one successful read of a probe's alarm at time now. falling holds the times
// the harness emitted each cycle's falling edge, oldest first.
func (t *lagTracker) observe(now time.Time, token string, snap alarmSnapshot, falling []time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.probes[token]
	if p == nil {
		return
	}
	p.polls++
	var clearedAt time.Time
	if snap.Found && snap.ClearedAt != "" {
		if ts, err := time.Parse(time.RFC3339Nano, snap.ClearedAt); err == nil {
			clearedAt = ts
		}
		// An unparseable time leaves clearedAt zero, which detects nothing: fail closed.
	}
	// The alarm is a single row, so a clearedTime at or after a falling edge's emit time shows
	// that edge and every earlier one.
	for p.next < len(falling) && !clearedAt.IsZero() &&
		!clearedAt.Before(falling[p.next].Add(-lagClockTolerance)) {
		p.lags = append(p.lags, now.Sub(falling[p.next]))
		p.next++
	}
	// The deadline: the oldest unseen falling edge, if it is already past the bound.
	if p.next < len(falling) {
		if late := now.Sub(falling[p.next]); late > t.bound {
			t.missCount++
			if t.missed == "" {
				t.missed = fmt.Sprintf("%s: cycle %d's falling edge was emitted %s ago and its alarm shows only "+
					"%d of %d cycles (bound %s)", token, p.next+1, late.Round(time.Second), p.next, len(falling), t.bound)
			}
		}
	}
}

// DetectionLag is the report section for the lag check.
type DetectionLag struct {
	BoundSeconds float64 `json:"boundSeconds"`
	// Cycles is the falling edges emitted per probe; Detected is how many, summed over the
	// probes, the durable alarms showed by the end.
	Cycles     int `json:"cyclesPerProbe"`
	Detected   int `json:"cyclesDetected"`
	Undetected int `json:"cyclesUndetected"`
	// Lag percentiles are over every detected cycle of every probe: seconds from the
	// harness emitting the falling edge to the first read that showed it. Upper bounds, to
	// the polling interval.
	P50Seconds float64 `json:"lagP50Seconds"`
	P99Seconds float64 `json:"lagP99Seconds"`
	MaxSeconds float64 `json:"lagMaxSeconds"`
	// DeadlineMisses is how many reads found a cycle past its deadline and still unseen.
	DeadlineMisses int `json:"deadlineMisses"`
	Polls          int `json:"polls"`
	PollErrors     int `json:"pollErrors"`
}

// report closes the tracker over the run's falling-edge count. Every cycle not seen by now is
// undetected, whatever the clock says.
func (t *lagTracker) report(cycles int) (DetectionLag, Invariant) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rep := DetectionLag{BoundSeconds: t.bound.Seconds(), Cycles: cycles, DeadlineMisses: t.missCount, PollErrors: t.pollErrors}
	var all []time.Duration
	unread := 0
	for _, p := range t.probes {
		rep.Detected += len(p.lags)
		rep.Undetected += cycles - len(p.lags)
		rep.Polls += p.polls
		all = append(all, p.lags...)
		if p.polls == 0 {
			unread++
		}
	}
	if len(all) > 0 {
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		rep.P50Seconds = all[len(all)/2].Seconds()
		rep.P99Seconds = all[min(len(all)-1, (len(all)*99)/100)].Seconds()
		rep.MaxSeconds = all[len(all)-1].Seconds()
	}
	inv := Invariant{Name: InvDetectionLag}
	summary := fmt.Sprintf("%d of %d cycles detected over %d edge probe(s); lag p50 %.1fs p99 %.1fs max %.1fs (bound %s)",
		rep.Detected, cycles*len(t.probes), len(t.probes), rep.P50Seconds, rep.P99Seconds, rep.MaxSeconds, t.bound)
	switch {
	case unread > 0 || rep.Polls == 0:
		inv.Detail = fmt.Sprintf("inconclusive: %d edge probe(s) were never read during the run (%d read error(s)); no lag was measured", unread, t.pollErrors)
	case rep.Undetected > 0:
		inv.Detail = fmt.Sprintf("DETECTION FELL BEHIND: %d cycle(s) never showed in the durable alarm; %s", rep.Undetected, summary)
		if t.missed != "" {
			inv.Detail += "; first miss: " + t.missed
		}
	case t.missCount > 0 || rep.MaxSeconds > t.bound.Seconds():
		inv.Detail = fmt.Sprintf("DETECTION FELL BEHIND: a cycle was seen later than the bound; %s", summary)
		if t.missed != "" {
			inv.Detail += "; first miss: " + t.missed
		}
	default:
		inv.Passed = true
		inv.Detail = summary
	}
	return rep, inv
}

// lagWatcher polls the edge probes' durable alarms while the run is driven and drained.
type lagWatcher struct {
	oracle  *alarmOracle
	edge    []string
	ps      *probeStats
	tracker *lagTracker
	poll    time.Duration
}

// pollOnce reads every edge probe once and feeds the tracker.
func (w *lagWatcher) pollOnce(ctx context.Context) {
	falling := w.ps.fallingEmitTimes()
	for _, tok := range w.edge {
		snap, err := w.oracle.snapshot(ctx, tok)
		now := time.Now()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A read blip is not a lag finding; a probe that is never read is (see report).
			log.Warn().Err(err).Str("device", tok).Msg("transient alarm read during the lag watch")
			w.tracker.pollFailed()
			continue
		}
		w.tracker.observe(now, tok, snap, falling)
	}
}

// run polls until ctx ends.
func (w *lagWatcher) run(ctx context.Context) {
	tick := time.NewTicker(w.poll)
	defer tick.Stop()
	for {
		w.pollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
