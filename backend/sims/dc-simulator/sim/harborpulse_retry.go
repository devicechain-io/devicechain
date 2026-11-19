// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	HarborpulseDefectBurstDropOn429 = "burst-drop-on-429"
	// HarborpulseDefectBurstUnthrottled is planted BY THE RIG with --tier/--tier-config
	// at the ordinary showcase tier. The sim validates and names it only: its tenant-admin
	// identity cannot change a tenant's tier or lower/raise its ingest ceiling.
	HarborpulseDefectBurstUnthrottled = "burst-unthrottled"
	harborRestoreMaxAttempts          = 8
)

type harborRestoreEntry struct {
	device        DeviceInstance
	reading       harborBuffered
	day, attempts int
	refused       bool
}

// One worker owns the FIFO head across network calls. No queue lock is held during
// ingress I/O, so telemetry, the clock and commands keep running even on a stalled POST.
// The cap is PER DEVICE, matching harborBufferCapFor's physical charger buffer.
type harborRestoreQueue struct {
	mu             sync.Mutex
	started        bool
	wake           chan struct{}
	done           chan struct{}
	entries        []*harborRestoreEntry
	tick, waitTick uint64
	ordinary       bool
	resume         time.Time
	lastTickAt     time.Time
	dropped        map[int]bool
}

func (q *harborRestoreQueue) beginTick() {
	q.mu.Lock()
	q.ordinary = true
	q.mu.Unlock()
}

func (s *harborpulse) drainDay(day int) *ScenarioRestoreDrain {
	// Caller holds s.s5.mu. Always available, including telemetry-only unit fixtures.
	if s.s5.ledger == nil {
		s.s5.ledger = &ScenarioS5{}
	}
	for i := range s.s5.ledger.RestoreDrains {
		if s.s5.ledger.RestoreDrains[i].Day == day {
			return &s.s5.ledger.RestoreDrains[i]
		}
	}
	s.s5.ledger.RestoreDrains = append(s.s5.ledger.RestoreDrains, ScenarioRestoreDrain{Day: day})
	return &s.s5.ledger.RestoreDrains[len(s.s5.ledger.RestoreDrains)-1]
}

func (q *harborRestoreQueue) completeTick(s *harborpulse, day int, ordinaryShed int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s.s5.mu.Lock()
	s.drainDay(day).OrdinaryShed += ordinaryShed
	s.s5.mu.Unlock()
	q.ordinary = false
	q.tick++
	q.lastTickAt = time.Now()
	q.signal()
}

func (q *harborRestoreQueue) signal() {
	if q.wake != nil {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
}

func (q *harborRestoreQueue) enqueue(ctx context.Context, s *harborpulse, rt *Runtime, pending []harborBackfill, day int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && len(pending) > 0 {
		q.started = true
		q.wake, q.done = make(chan struct{}, 1), make(chan struct{})
		q.dropped = map[int]bool{}
		go q.run(ctx, s, rt)
	}
	devices := map[string]DeviceInstance{}
	counts := map[string]int{}
	for _, d := range rt.Devices {
		devices[d.Token] = d
	}
	for _, e := range q.entries {
		counts[e.device.Token]++
	}
	s.s5.mu.Lock()
	defer s.s5.mu.Unlock()
	for _, p := range pending {
		for _, r := range p.readings {
			l := s.drainDay(day)
			l.Offered++
			l.CaughtUpAt = ""
			d, exists := devices[p.token]
			if !exists || counts[p.token] >= harborBufferCapFor(s.dayLength, s.load.Interval()) {
				l.DeadLettered++
				log.Error().Str("device", p.token).Time("taken", r.at).Int("day", day).Msg("harborpulse restore dead-letter: queue overflow or missing device")
				continue
			}
			// Snapshot values and microsecond time; retries cannot observe later model changes.
			values := make(map[string]float64, len(r.values))
			for k, v := range r.values {
				values[k] = v
			}
			r.values, r.at = values, r.at.UTC().Truncate(time.Microsecond)
			q.entries = append(q.entries, &harborRestoreEntry{device: d, reading: r, day: day})
			counts[p.token]++
		}
	}
}

func (q *harborRestoreQueue) run(ctx context.Context, s *harborpulse, rt *Runtime) {
	defer close(q.done)
	for {
		if ctx.Err() != nil {
			return
		}
		q.mu.Lock()
		ready := len(q.entries) > 0 && !q.ordinary && q.tick > q.waitTick && !q.lastTickAt.Before(q.resume)
		if !ready {
			q.mu.Unlock()
			// Resume only at a completed ordinary tick after the deadline, never from
			// a timer midway to the following tick that could consume its allowance.
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		e := q.entries[0]
		e.attempts++
		q.mu.Unlock()
		err := emitMeasurementsAt(ctx, rt, e.device, e.reading.at.Format(time.RFC3339Nano), wireMeasurements(e.reading.values))
		q.mu.Lock()
		s.s5.mu.Lock()
		l := s.drainDay(e.day)
		remove := err == nil
		if err == nil {
			l.Accepted++
			rt.Stats.Emitted.Add(1)
		} else {
			backoff := time.Second
			var classified *ingressOutcomeError
			if errors.As(err, &classified) && classified.retryAfter > 0 {
				backoff = classified.retryAfter
			}
			switch {
			case errors.Is(err, ErrBackpressured):
				l.Backpressured++
			case errors.Is(err, ErrShed):
				l.Refused429++
				if s.defects.burstDropOn429 && !q.dropped[e.day] {
					q.dropped[e.day], remove = true, true
				}
			case errors.Is(err, ErrConnectRefused):
			case classified != nil && classified.outcome == OutcomeRefused:
			default:
				l.Ambiguous++
			}
			definitive := errors.Is(err, ErrShed) || errors.Is(err, ErrConnectRefused) || (classified != nil && classified.outcome == OutcomeRefused)
			if definitive && l.FirstRefusalAt == "" {
				l.FirstRefusalAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			if definitive && !e.refused {
				e.refused = true
				q.recordRefused(l, e)
			}
			// Cancellation preserves the pending entry and its ambiguous count for inspection.
			if ctx.Err() == nil && !remove && e.attempts >= harborRestoreMaxAttempts {
				l.DeadLettered++
				remove = true
				log.Error().Err(err).Str("device", e.device.Token).Time("taken", e.reading.at).Int("attempts", e.attempts).Msg("harborpulse restore dead-letter: retry bound exhausted")
			}
			q.resume, q.waitTick = time.Now().Add(backoff), q.tick
		}
		if remove {
			q.entries[0] = nil
			q.entries = q.entries[1:]
			pending := false
			for _, other := range q.entries {
				if other.day == e.day {
					pending = true
					break
				}
			}
			if !pending {
				l.CaughtUpAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		s.s5.mu.Unlock()
		q.mu.Unlock()
	}
}

func (q *harborRestoreQueue) recordRefused(l *ScenarioRestoreDrain, e *harborRestoreEntry) {
	stamp := e.reading.at.Format(time.RFC3339Nano)
	for i := range l.Refused {
		if l.Refused[i].Device == e.device.Token {
			l.Refused[i].Times = append(l.Refused[i].Times, stamp)
			return
		}
	}
	l.Refused = append(l.Refused, ScenarioRestoreRefused{Device: e.device.Token, Times: []string{stamp}})
}

// HarborpulseIngestBaseline measures the default day at its actual emission cadence.
// HTTP charges every metric value plus the truck location (one). Integration ingress
// uses DecoderCharge: these small two-output FRAME plans cost less than 4000, so each
// uplink costs two even when the legacy layout fails and stores none. Link metrics are off.
type HarborpulseIngestBaseline struct {
	MinTick, MaxTick                                                     int
	BeforeRestoreMin, BeforeRestoreMax, AfterRestoreMin, AfterRestoreMax int
	TickAtRestore, RestoreOffered                                        int
}

func HarborpulseDefaultIngestBaseline() HarborpulseIngestBaseline {
	m := newHarborModel(1)
	// Model the clean sampling command being applied on the 16:30 uplink. Taking
	// the fastest possible delivery is conservative for the day's maximum; a later
	// command only leaves the north gauge at its ordinary five-second cadence longer.
	nextNorth := time.Duration(0)
	b := HarborpulseIngestBaseline{MinTick: int(^uint(0) >> 1), BeforeRestoreMin: int(^uint(0) >> 1), AfterRestoreMin: int(^uint(0) >> 1)}
	for elapsed := time.Duration(0); elapsed < harborpulseDayLength; elapsed += DefaultEmitInterval {
		m.advanceTo(elapsed)
		at := time.Unix(0, 0).Add(elapsed)
		n := 0
		for _, d := range harborLayout() {
			n += len(m.reading(d.token, elapsed, at))
			if _, ok := m.fix(d.token, elapsed); ok {
				n++
			}
		}
		for _, u := range m.loraUplinks(elapsed) {
			if u.devEUI == HarborpulseSamplingDevEUI {
				if elapsed < nextNorth {
					continue
				}
				interval := DefaultEmitInterval
				if elapsed >= time.Duration((16.5-harborpulseStartHour)/24*float64(harborpulseDayLength)) {
					interval = HarborpulseSamplingSeconds * time.Second
				}
				nextNorth = elapsed + interval
			}
			n += 2
		}
		b.MinTick, b.MaxTick = min(b.MinTick, n), max(b.MaxTick, n)
		h := harborHour(elapsed)
		if h >= harborOutageTo-0.25 && h < harborOutageTo {
			b.BeforeRestoreMin, b.BeforeRestoreMax = min(b.BeforeRestoreMin, n), max(b.BeforeRestoreMax, n)
		}
		if h >= harborOutageTo && h < harborOutageTo+0.25 {
			b.AfterRestoreMin, b.AfterRestoreMax = min(b.AfterRestoreMin, n), max(b.AfterRestoreMax, n)
		}
		for _, p := range m.takeBackfill(elapsed) {
			b.TickAtRestore = n
			for _, r := range p.readings {
				b.RestoreOffered += len(r.values)
			}
		}
	}
	return b
}

// Keep the measured baseline useful in diagnostic output without a second formatter.
func (b HarborpulseIngestBaseline) String() string {
	return fmt.Sprintf("ordinary=%d..%d before=%d..%d after=%d..%d restoreTick=%d restore=%d", b.MinTick, b.MaxTick, b.BeforeRestoreMin, b.BeforeRestoreMax, b.AfterRestoreMin, b.AfterRestoreMax, b.TickAtRestore, b.RestoreOffered)
}
