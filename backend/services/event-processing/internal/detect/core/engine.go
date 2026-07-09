// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"sort"
	"time"
)

// RuleKind is the temporal shape of a rule. Slice 0 proves the timer-driven shapes
// (Absence, Duration) plus the trivial one (Threshold); windowed shapes (Repeating,
// Aggregate, Correlation) land in Slice 2 on top of the same watermark+snapshot spine.
type RuleKind int

const (
	// Threshold fires immediately on any event whose predicate matched. Level-triggered.
	Threshold RuleKind = iota
	// Absence fires when no event arrives for a series within Timeout of the last one.
	Absence
	// Duration fires when the predicate stays matched continuously for Hold.
	Duration
	// Repeating fires when >= Count matching events fall inside a sliding Window
	// (edge-triggered: once per crossing, re-armed after the count drops below Count).
	Repeating
	// Aggregate fires when an aggregate (Agg) of matching values over a tumbling Window
	// satisfies Op vs Threshold, evaluated when the window closes on the watermark.
	Aggregate
)

// Rule is a compiled detection rule keyed by ID (tenant-token prefixed in the real
// service). Only the fields relevant to a kind are used.
type Rule struct {
	ID      string
	Kind    RuleKind
	Timeout time.Duration // Absence
	Hold    time.Duration // Duration
	Window  time.Duration // Repeating (sliding) + Aggregate (tumbling)
	Count   int           // Repeating: N
	Agg     AggOp         // Aggregate
	Op      CmpOp         // Aggregate
	Thresh  float64       // Aggregate
}

// Event is one resolved measurement fed to the core. Match is the result of the rule's
// CEL predicate (set by the predicate layer in the real service; set directly in tests).
// For Absence, any event is a heartbeat and Match is ignored.
type Event struct {
	Seq   uint64
	Key   SeriesKey
	Time  time.Time
	Value float64
	Match bool
}

// Detection is an emitted signal. Its identity (RuleID, Series, Kind, At) is stable and
// deterministic, so at-least-once re-emission across a restart is dedup-collapsible
// downstream via an idempotency key (ADR-051 §8) — the property the replay test asserts.
type Detection struct {
	RuleID string
	Series string
	Kind   RuleKind
	At     time.Time // logical time: event time (Threshold) or deadline (Absence/Duration)
}

// Engine is the single-writer detection core for one partition. It is fed Events (and
// idle Advances) in order; the watermark (logical time) only moves forward, driven by
// event timestamps and idle wall-clock advance. All firing decisions are a function of
// the watermark and the snapshotted state — never wall-clock in the replay path.
type Engine struct {
	lateness time.Duration
	rules    map[string]Rule
	wheel    *timerWheel
	wm       time.Time                 // watermark = current logical time
	lastSeq  uint64                    // seq of the last event whose effect is in the state
	active   map[SeriesKey]time.Time   // Duration: when the current matched run began
	sliding  map[SeriesKey][]time.Time // Repeating: trailing matching-event times
	panes    map[paneKey]*paneAgg      // Aggregate: open tumbling window panes
	closes   closeHeap                 // Aggregate: pending pane closes, ordered by end
	out      []Detection
}

// NewEngine builds an empty engine. allowedLateness bounds how far event time is held
// back before it advances the watermark (0 for the deterministic timer tests).
func NewEngine(rules []Rule, allowedLateness time.Duration) *Engine {
	m := make(map[string]Rule, len(rules))
	for _, r := range rules {
		m[r.ID] = r
	}
	return &Engine{
		lateness: allowedLateness,
		rules:    m,
		wheel:    newTimerWheel(),
		active:   map[SeriesKey]time.Time{},
		sliding:  map[SeriesKey][]time.Time{},
		panes:    map[paneKey]*paneAgg{},
	}
}

// Drain returns and clears the detections emitted since the last Drain.
func (e *Engine) Drain() []Detection {
	out := e.out
	e.out = nil
	return out
}

// LastSeq is the highest event sequence whose effect is captured in the state. The
// runtime acks JetStream up to LastSeq only AFTER a snapshot commits — so a restart
// replays from LastSeq+1 and re-derives identical firings.
func (e *Engine) LastSeq() uint64 { return e.lastSeq }

// Watermark is the engine's current logical time. The runtime denormalizes it onto
// the checkpoint row so the operations surface can measure watermark lag (event
// time vs wall clock) without deserializing the snapshot payload.
func (e *Engine) Watermark() time.Time { return e.wm }

// ProcessEvent applies one event: advance the watermark, fire any timers the advance
// made due (as of the pre-event state), then apply the event's effect.
func (e *Engine) ProcessEvent(ev Event) {
	if ev.Seq <= e.lastSeq {
		return // already applied (idempotent re-feed guard)
	}
	e.advance(ev.Time)
	e.apply(ev)
	e.lastSeq = ev.Seq
}

// Advance moves logical time forward to wall time w without an event — the live
// idle-advance path that lets absence/duration fire when the stream is quiet. It carries
// no sequence (it is derived from the clock, not the log), so it is re-generated
// post-restart rather than replayed.
func (e *Engine) Advance(w time.Time) { e.advance(w) }

func (e *Engine) advance(t time.Time) {
	cand := t.Add(-e.lateness)
	if cand.After(e.wm) {
		e.wm = cand
	}
	for _, ft := range e.wheel.popDue(e.wm) {
		e.fire(ft.key, ft.deadline)
	}
	e.closePanes(e.wm)
}

func (e *Engine) apply(ev Event) {
	r, ok := e.rules[ev.Key.Rule]
	if !ok {
		return
	}
	switch r.Kind {
	case Threshold:
		if ev.Match {
			e.emit(r, ev.Key, ev.Time)
		}
	case Absence:
		// Any event is a heartbeat: (re)arm the dead-man timer.
		e.wheel.schedule(ev.Key, ev.Time.Add(r.Timeout))
	case Duration:
		if ev.Match {
			if _, held := e.active[ev.Key]; !held {
				e.active[ev.Key] = ev.Time
				e.wheel.schedule(ev.Key, ev.Time.Add(r.Hold))
			}
		} else {
			delete(e.active, ev.Key)
			e.wheel.cancel(ev.Key)
		}
	case Repeating:
		e.applyRepeating(ev, r)
	case Aggregate:
		e.applyAggregate(ev, r)
	}
}

func (e *Engine) fire(key SeriesKey, deadline time.Time) {
	r, ok := e.rules[key.Rule]
	if !ok {
		return
	}
	switch r.Kind {
	case Absence:
		e.emit(r, key, deadline) // stamped at the deadline the silence elapsed
		// one-shot: the wheel already consumed the timer; next heartbeat re-arms.
	case Duration:
		if _, held := e.active[key]; held {
			e.emit(r, key, deadline)
			delete(e.active, key)
		}
	}
}

func (e *Engine) emit(r Rule, key SeriesKey, at time.Time) {
	e.out = append(e.out, Detection{RuleID: r.ID, Series: key.Series, Kind: r.Kind, At: at})
}

// --- snapshot / restore (atomic-with-sequence in the real store; bytes here) ---

type snapActive struct {
	Rule   string    `json:"rule"`
	Series string    `json:"series"`
	Since  time.Time `json:"since"`
}

type snapshot struct {
	Watermark time.Time     `json:"watermark"`
	LastSeq   uint64        `json:"lastSeq"`
	Active    []snapActive  `json:"active"`
	Timers    []snapTimer   `json:"timers"`
	Gens      []snapGen     `json:"gens"`
	Sliding   []snapSliding `json:"sliding"`
	Panes     []snapPane    `json:"panes"`
}

// Snapshot serializes the full engine state. In the service this is committed to Postgres
// in the SAME transaction as LastSeq (ack-on-checkpoint); here it round-trips through
// bytes to prove the state is fully serializable.
func (e *Engine) Snapshot() ([]byte, error) {
	timers, gens := e.wheel.snapshot()
	active := make([]snapActive, 0, len(e.active))
	for k, since := range e.active {
		active = append(active, snapActive{Rule: k.Rule, Series: k.Series, Since: since})
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Rule != active[j].Rule {
			return active[i].Rule < active[j].Rule
		}
		return active[i].Series < active[j].Series
	})
	sliding, panes := e.snapshotWindows()
	return json.Marshal(snapshot{
		Watermark: e.wm,
		LastSeq:   e.lastSeq,
		Active:    active,
		Timers:    timers,
		Gens:      gens,
		Sliding:   sliding,
		Panes:     panes,
	})
}

// Restore rebuilds an engine from a snapshot and its rule set. The restored engine
// resumes as if it had processed every event up to LastSeq — the caller replays the log
// from LastSeq+1.
func Restore(rules []Rule, allowedLateness time.Duration, data []byte) (*Engine, error) {
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	e := NewEngine(rules, allowedLateness)
	e.wm = s.Watermark
	e.lastSeq = s.LastSeq
	for _, a := range s.Active {
		e.active[SeriesKey{Rule: a.Rule, Series: a.Series}] = a.Since
	}
	e.wheel = restoreTimerWheel(s.Timers, s.Gens)
	e.restoreWindows(s.Sliding, s.Panes)
	return e, nil
}
