// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// RuleKind is the temporal shape of a rule. Slice 0 proved the timer-driven shapes
// (Absence, Duration), the trivial one (Threshold), and the first windowed shapes
// (Repeating, Aggregate). Slice 2b completes the set — the keyed-running-state shapes
// (DeltaRate, SlidingAgg), the count/session windows, and distinct-count Correlation —
// all on the same watermark + snapshot spine so replay stays correct for every one.
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
	// DeltaRate fires when the change between a series' consecutive matching samples
	// (Rate ⇒ divided by the elapsed seconds) satisfies Op vs Thresh. Level-triggered;
	// the first sample for a series only primes the state.
	DeltaRate
	// CountWindow fires when an aggregate (Agg) over every Count matching events satisfies
	// Op vs Thresh, then resets — a tumbling window measured in events, not time.
	CountWindow
	// Session fires when an aggregate over a session — a run of matching events whose
	// successive gaps stay under Gap — satisfies Op vs Thresh, evaluated when the session
	// closes on the watermark (an event exactly Gap after the last closes it and opens a new one).
	Session
	// SlidingAgg fires when an aggregate over the trailing sliding Window satisfies Op vs
	// Thresh, re-evaluated on each event (edge-triggered: once per crossing, re-armed on
	// falling back). Running min/max are O(1)-amortized via a monotonic deque.
	SlidingAgg
	// Correlation fires when the number of DISTINCT members (Event.Member) seen for an
	// anchor (the series key) within a sliding Window reaches Count — area/fleet correlation.
	Correlation
)

// Rule is a compiled detection rule keyed by ID (tenant-token prefixed in the real
// service). Only the fields relevant to a kind are used.
type Rule struct {
	ID        string
	Kind      RuleKind
	Timeout   time.Duration // Absence
	Hold      time.Duration // Duration
	Gap       time.Duration // Session (max inter-event silence within a session)
	Window    time.Duration // Repeating + SlidingAgg (sliding) · Aggregate (tumbling) · Correlation
	Count     int           // Repeating / CountWindow: N · Correlation: distinct-member threshold
	Rate      bool          // DeltaRate: compare per-second rate instead of raw change
	MemberCap int           // Correlation: hard cap on distinct members retained per anchor (memory backstop)
	Agg       AggOp         // Aggregate / CountWindow / Session / SlidingAgg
	Op        CmpOp         // Aggregate / CountWindow / Session / SlidingAgg / DeltaRate
	Thresh    float64       // Aggregate / CountWindow / Session / SlidingAgg / DeltaRate
}

// Event is one resolved measurement fed to the core. Match is the result of the rule's
// CEL predicate (set by the predicate layer in the real service; set directly in tests).
// For Absence, any event is a heartbeat and Match is ignored.
type Event struct {
	Seq   uint64
	Key   SeriesKey
	Time  time.Time
	Value float64
	// HasValue reports whether Value is a real reading rather than a zero default: true when the
	// event was built from a designated value/gate metric, false for a metric-less shape (a raw-CEL
	// leaf with no single metric). It is what a gate-kind fire (Threshold/Repeating) propagates to
	// its detection so a metric-less rule carries no value instead of a fabricated 0. Value-consuming
	// kinds (DeltaRate, aggregates) fold Value regardless and stamp their own computed value.
	HasValue bool
	Match    bool
	Member   string // Correlation: the distinct contributor (device token) under the anchor key; ignored otherwise
}

// Detection is an emitted signal. Its identity (RuleID, Series, Kind, At) is stable and
// deterministic, so at-least-once re-emission across a restart is dedup-collapsible
// downstream via an idempotency key (ADR-051 §8) — the property the replay test asserts.
type Detection struct {
	RuleID string
	Series string
	Kind   RuleKind
	// At is the logical (event) time the detection is stamped at, and is part of its dedup
	// identity: the triggering event time for Threshold, DeltaRate, CountWindow, SlidingAgg,
	// and Correlation; the elapsed deadline for Absence, Duration, and Session; the window end
	// for Aggregate.
	At time.Time
	// Value is the scalar the detection is ABOUT, when one is meaningful: the crossing sample for
	// Threshold/Repeating (the event's own gate-metric reading, via emitSample), the computed
	// delta/rate for DeltaRate, and the window/session aggregate for CountWindow/SlidingAgg/Aggregate/
	// Session. It is an informational payload the raise-alarm REACT action stamps on the alarm (so a
	// re-raise carries the real triggering value, not a zero), NOT part of the dedup identity above —
	// a downstream at-least-once collapse keys only on (RuleID, Series, Kind, At). HasValue
	// distinguishes "value is 0.0" from "no value": timer-driven silence fires (Absence, Duration),
	// Correlation, and a metric-less Threshold/Repeating leaf (a raw-CEL gate that reads no single
	// sample) all carry none (HasValue=false).
	Value    float64
	HasValue bool
}

// Engine is the single-writer detection core for one partition. It is fed Events (and
// idle Advances) in order; the watermark (logical time) only moves forward, driven by
// event timestamps and idle wall-clock advance. All firing decisions are a function of
// the watermark and the snapshotted state — never wall-clock in the replay path.
type Engine struct {
	rules    map[string]Rule
	wheel    *timerWheel
	wm       watermark                      // logical clock: bounded-out-of-order event-time frontier
	lastSeq  uint64                         // seq of the last event whose effect is in the state
	active   map[SeriesKey]time.Time        // Duration: when the current matched run began
	sliding  map[SeriesKey][]time.Time      // Repeating: trailing matching-event times
	panes    map[paneKey]*paneAgg           // Aggregate: open tumbling window panes
	closes   closeHeap                      // Aggregate: pending pane closes, ordered by end
	lastVal  map[SeriesKey]deltaState       // DeltaRate: last sample per series
	counts   map[SeriesKey]*paneAgg         // CountWindow: open event-count accumulator per series
	session  map[SeriesKey]*paneAgg         // Session: open session accumulator per series
	slides   map[SeriesKey]*slidingState    // SlidingAgg: trailing sliding-window state per series
	corr     map[SeriesKey]map[string]int64 // Correlation: anchor → member → last unix-nanos seen
	expected map[SeriesKey]expectedState    // Absence: dead-man arming for never-seen devices (ADR-051 slice 4c-2b)
	out      []Detection
}

// expectedState is the dead-man arming record for one rostered device under one Absence rule
// (ADR-051 slice 4c-2b): the resolved grace base the dead-man timer was armed from, plus a
// once-fired latch. It exists so a device that has NEVER reported still gets an absence timer
// — a heartbeat-armed timer (apply, Absence) only ever covers a device that reported at least
// once and then went silent, so a device that never reports at all would otherwise never fire
// absence, the exact case absence detection exists for.
//
// since doubles as the epoch marker, and it is FORWARD-ONLY. Done makes the dead-man fire AT
// MOST ONCE PER EPOCH: restart reconciliation (slice 4c-2b-2b) re-arms every still-expected
// series with its recomputed base, and without the latch a series whose dead-man already fired
// — its wheel timer consumed, its live deadline gone — would be re-armed at the SAME base and
// fire a second time. A STRICTLY-later base is a different epoch (a re-publish or rollback, which
// device-management stamps with a fresh publishedAt precisely to grant a fresh grace window):
// it clears the latch and re-arms. So the latch suppresses a re-arm at the same-or-earlier base
// only — the once-across-restart guarantee — while honoring a genuinely advanced grace base.
type expectedState struct {
	since time.Time // grace base = max(device.expectedSince, rule.publishedAt); also the forward-only epoch marker
	done  bool      // fired at `since`; suppresses a re-arm at the same-or-earlier base, a later base re-arms
}

// NewEngine builds an empty engine. allowedLateness bounds how far event time is held
// back before it advances the watermark (0 for the deterministic timer tests).
func NewEngine(rules []Rule, allowedLateness time.Duration) *Engine {
	m := make(map[string]Rule, len(rules))
	for _, r := range rules {
		m[r.ID] = r
	}
	return &Engine{
		rules:    m,
		wheel:    newTimerWheel(),
		wm:       watermark{lateness: allowedLateness},
		active:   map[SeriesKey]time.Time{},
		sliding:  map[SeriesKey][]time.Time{},
		panes:    map[paneKey]*paneAgg{},
		lastVal:  map[SeriesKey]deltaState{},
		counts:   map[SeriesKey]*paneAgg{},
		session:  map[SeriesKey]*paneAgg{},
		slides:   map[SeriesKey]*slidingState{},
		corr:     map[SeriesKey]map[string]int64{},
		expected: map[SeriesKey]expectedState{},
	}
}

// UpsertRule adds or replaces a rule in the engine's live rule set — the mutable
// counterpart to NewEngine's construction-time set (ADR-051 slice 4b-3), applied on the
// single-writer loop so it never races the fan-out. A rule id embeds its profile-version
// token ("{tenant}/{profileToken}@{version}/{ruleKey}"), so an upsert of an existing id
// normally installs an IDENTICAL rule: its live window/timer state is deliberately PRESERVED
// — a redelivered published-rule fact must not reset a running rule. A brand-new id starts
// with empty state, which is correct: the engine detects forward from activation.
//
// If, however, an existing id arrives with a DIFFERENT body — reachable when a profile token
// is deleted and reused so a new profile re-mints an old rule id with different semantics —
// preserving the old rule's keyed state would graft a Duration hold or a window accumulator
// onto a rule that means something else (false or missed detections). So a changed body first
// GCs the old rule's state (RemoveRule); the replacement then starts clean. Rule is all
// comparable fields, so struct inequality is the exact "did the semantics change" test.
func (e *Engine) UpsertRule(r Rule) {
	if existing, ok := e.rules[r.ID]; ok && existing != r {
		e.RemoveRule(r.ID) // reused id, different rule: drop the stale rule's keyed state
	}
	e.rules[r.ID] = r
}

// RemoveRule evicts a rule and garbage-collects ALL of its keyed state in place, so the
// engine keeps running every OTHER rule's live windows and timers untouched. A full
// NewEngine rebuild would be far simpler but would discard every rule's state — the
// exact corruption this exists to avoid. A rule's state is scattered across the nine
// per-key structures below plus the timer wheel, the pane close-heap, and the pending-
// detection buffer; each is swept for entries whose rule component equals id. Removal is
// rare (a governance/teardown path, ADR-023/052 — the publish path only ever upserts,
// since versions are immutable and retained), so an O(live-state) linear sweep, with no
// reverse index to maintain on the hot path, is the right trade.
func (e *Engine) RemoveRule(id string) {
	delete(e.rules, id)
	deleteSeriesKeys(e.active, id)
	deleteSeriesKeys(e.sliding, id)
	deleteSeriesKeys(e.lastVal, id)
	deleteSeriesKeys(e.counts, id)
	deleteSeriesKeys(e.session, id)
	deleteSeriesKeys(e.slides, id)
	deleteSeriesKeys(e.corr, id)
	deleteSeriesKeys(e.expected, id)
	for pk := range e.panes {
		if pk.Rule == id {
			delete(e.panes, pk)
		}
	}
	e.closes.purgeRule(id)
	e.wheel.purgeRule(id)
	// Drop any buffered detections for the removed rule: deliver-before-checkpoint drains
	// e.out each message, so these have not been handed off yet, and once the rule is gone
	// the publisher's registry Lookup would treat them as orphans. Dropping keeps removal
	// atomic with respect to what the next checkpoint delivers. In-place filter: writes
	// never outrun reads.
	if len(e.out) > 0 {
		kept := e.out[:0]
		for _, d := range e.out {
			if d.RuleID != id {
				kept = append(kept, d)
			}
		}
		e.out = kept
	}
}

// deleteSeriesKeys removes every entry of a SeriesKey-keyed state map whose rule
// component equals rule — the per-map primitive RemoveRule's GC sweep is built from.
func deleteSeriesKeys[V any](m map[SeriesKey]V, rule string) {
	for k := range m {
		if k.Rule == rule {
			delete(m, k)
		}
	}
}

// SetExpected arms (or refreshes) the dead-man absence timer for a series the runtime resolved
// from the roster + active-version read-models — the (Absence rule, device) pair a rostered
// device that has never reported should still be watched on (ADR-051 slice 4c-2b). It is driven
// off device/rule facts and restart reconciliation on the single-writer loop, NOT the resolved-
// event stream, so it is made durable through the snapshot rather than replayed. since is the
// already-resolved grace base (max(device.expectedSince, rule.publishedAt), F1-clamped by the
// caller in slice 4c-2b-2b); the deadline is since+Timeout, exactly symmetric with a heartbeat's
// ev.Time+Timeout in apply.
//
// It is a no-op unless key.Rule is a KNOWN Absence rule — only absence has a dead-man, and
// guarding on the live rule keeps a stale/misrouted key from fabricating an entry with no timeout
// to arm against.
//
// It is FORWARD-ONLY on the grace base, which is both the armed deadline's origin and the epoch
// marker. A base that does not STRICTLY advance is either a restart-reconciliation duplicate of
// the current epoch or a stale/earlier arming: the recorded base and the forward-only wheel
// deadline are left untouched. When the current epoch already fired (done), that same guard IS
// the once-semantics — a reconcile must not re-arm and re-fire a dead-man at the elapsed base. A
// STRICTLY-later base is a NEW epoch (a re-publish/rollback fresh grace window): it clears any
// fired latch and re-arms forward (scheduleForward, so a device that later reports still
// supersedes it with a heartbeat's later deadline and neither ever shrinks a live deadline).
func (e *Engine) SetExpected(key SeriesKey, since time.Time) {
	r, ok := e.rules[key.Rule]
	if !ok || r.Kind != Absence {
		return
	}
	if st, armed := e.expected[key]; armed && !since.After(st.since) {
		return // same-or-earlier base: reconcile duplicate / stale arming (and the once-semantics latch)
	}
	e.expected[key] = expectedState{since: since}
	e.wheel.scheduleForward(key, since.Add(r.Timeout))
}

// RemoveExpected disarms a series' dead-man entirely — dropping its expected entry AND cancelling
// its absence timer in the wheel — when the device leaves the rule's scope (deleted, or re-typed
// off the profile; ADR-051 slice 4c-2b). Cancelling the wheel timer (not merely the entry) also
// silences a heartbeat-armed absence timer for a now-departed device, so a deleted device cannot
// fire one last false absence. A key with neither an entry nor a live timer is a clean no-op.
func (e *Engine) RemoveExpected(key SeriesKey) {
	delete(e.expected, key)
	e.wheel.cancel(key)
}

// ExpectedKeys returns every series currently dead-man-armed (fired or not), so the runtime's
// restart reconciliation (slice 4c-2b-2b) can diff the engine's armed set against the roster +
// active-version read-models and RemoveExpected the entries whose membership is gone (a device
// deleted, or a profile version superseded, while the process was down) — INCLUDING an entry for
// a rule the restored rule set no longer holds (Restore reloads expected unconditionally), which
// the reconciliation must treat as membership-gone or it persists in every future snapshot. It
// returns a point-in-time copy of the keys, so mutating the engine while iterating it is safe.
// The armed set is bounded by (absence rules × rostered devices) — the same key-cardinality class
// as the wheel's gens map (see timerWheel), governed by the ADR-023 state budget, not event volume.
// Order is unspecified.
func (e *Engine) ExpectedKeys() []SeriesKey {
	keys := make([]SeriesKey, 0, len(e.expected))
	for k := range e.expected {
		keys = append(keys, k)
	}
	return keys
}

// HeartbeatAbsenceKeys returns every series that holds a LIVE absence timer armed by its own
// heartbeat (apply, Absence) but has NO dead-man expected entry — precisely the timers the dead-man
// reconcile's ExpectedKeys sweep is blind to (ExpectedKeys enumerates only SetExpected entries, not
// the wheel). The restart reconcile uses it to cancel a departed/superseded device's lingering
// heartbeat absence timer, which would otherwise fire one false absence off the watermark. A key
// present in BOTH the wheel and e.expected is already covered by the dead-man sweep, so it is
// excluded here. The wheel's live map holds one deadline per key, so this reads current arming
// directly. Bounded by (absence rules × reporting devices) — the same state-budget class as the
// wheel; order unspecified; returns a point-in-time copy safe to mutate the engine while iterating.
func (e *Engine) HeartbeatAbsenceKeys() []SeriesKey {
	var keys []SeriesKey
	for k := range e.wheel.live {
		if _, dead := e.expected[k]; dead {
			continue // a dead-man entry: the ExpectedKeys sweep owns it
		}
		if r, ok := e.rules[k.Rule]; ok && r.Kind == Absence {
			keys = append(keys, k)
		}
	}
	return keys
}

// HasPendingWork reports whether any timer or pane close is scheduled — i.e. whether a
// wall-clock advance could fire ANYTHING. Every frontier-triggered firing goes through the
// timer wheel (Absence/Duration/Session) or the pane close-heap (Aggregate); the other kinds
// are event-triggered and never fire on a bare advance. So when this is false, an idle advance
// would only move the watermark with nothing to fire — and with no timer/window state, a later
// event re-derives the frontier itself, so leaving it at rest is replay-safe. Idle-advance
// consults this to stay quiet on an engine with no pending work instead of checkpointing a
// bare watermark move every interval forever (ADR-051 slice 4c).
func (e *Engine) HasPendingWork() bool {
	return e.wheel.hasTimers() || e.closes.Len() > 0
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
func (e *Engine) Watermark() time.Time { return e.wm.now }

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

// ProcessResolved applies one resolved MESSAGE that fans out to zero or more per-rule
// events. The runtime evaluates each applicable rule's leaf predicate for the message and
// builds one core Event per rule (all sharing the message's stream sequence and event
// time); it hands the batch here. The engine advances the watermark exactly ONCE — every
// event shares the message's occurred time, and the timers/pane-closes the advance fires
// must be evaluated against the pre-message frontier a single time, not once per rule —
// then applies each per-rule event, then records the message sequence.
//
// The single per-MESSAGE sequence, not the per-rule event, is the idempotency and
// checkpoint unit: a redelivered or replayed message at or below the recorded sequence is
// dropped whole (so N same-seq rule events cannot each trip — or worse, partially trip —
// the guard), and replay from lastSeq+1 re-derives the identical fan-out. evs may be empty
// (a message that matches no rule, or carries no metric any rule gates on): the watermark
// and sequence still advance so the checkpoint position tracks the live stream.
func (e *Engine) ProcessResolved(seq uint64, t time.Time, evs []Event) {
	if seq <= e.lastSeq {
		return // already applied (idempotent re-feed guard, at the message level)
	}
	e.advance(t)
	for i := range evs {
		e.apply(evs[i])
	}
	e.lastSeq = seq
}

// Advance moves logical time forward to wall time w without an event — the live
// idle-advance path that lets absence/duration fire when the stream is quiet. It carries
// no sequence (it is derived from the clock, not the log), so it is re-generated
// post-restart rather than replayed. It reports whether it changed any serializable state
// (the watermark moved, a timer fired, or a pane closed — INCLUDING a timer/pane that
// consumed state without emitting), so the caller can persist a wall-clock advance whose
// effect must survive a restart to keep replay identical (ADR-051 slice 4c).
func (e *Engine) Advance(w time.Time) bool { return e.advance(w) }

// advance folds a timestamp into the watermark, fires every timer the new frontier made
// due, and closes every pane it made due, reporting whether any of those mutated state.
func (e *Engine) advance(t time.Time) bool {
	moved := e.wm.observe(t)
	due := e.wheel.popDue(e.wm.now)
	for _, ft := range due {
		e.fire(ft.key, ft.deadline)
	}
	closed := e.closePanes(e.wm.now)
	return moved || len(due) > 0 || closed
}

func (e *Engine) apply(ev Event) {
	r, ok := e.rules[ev.Key.Rule]
	if !ok {
		return
	}
	// A non-finite measurement must never reach serializable aggregate state: a single NaN in
	// a running sum makes every subsequent Snapshot() fail (encoding/json rejects NaN/±Inf),
	// which silently halts checkpointing while the engine keeps running — the replay span then
	// grows without bound. Value-blind kinds (Threshold/Absence) still process the heartbeat.
	switch r.Kind {
	case Aggregate, DeltaRate, CountWindow, Session, SlidingAgg:
		if math.IsNaN(ev.Value) || math.IsInf(ev.Value, 0) {
			return
		}
	}
	switch r.Kind {
	case Threshold:
		if ev.Match {
			e.emitSample(r, ev)
		}
	case Absence:
		// Any event is a heartbeat: (re)arm the dead-man timer, but only ever FORWARD — a
		// late out-of-order heartbeat must not shrink a deadline a newer one already set.
		e.wheel.scheduleForward(ev.Key, ev.Time.Add(r.Timeout))
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
	case DeltaRate:
		e.applyDeltaRate(ev, r)
	case CountWindow:
		e.applyCountWindow(ev, r)
	case Session:
		e.applySession(ev, r)
	case SlidingAgg:
		e.applySlidingAgg(ev, r)
	case Correlation:
		e.applyCorrelation(ev, r)
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
		// Latch this epoch as fired so restart reconciliation (slice 4c-2b-2b) does not re-arm and
		// re-fire it: the wheel already consumed this timer, so a reconcile-driven SetExpected at
		// the same, now-elapsed grace base would schedule a fresh timer that fires immediately on
		// the next advance. The latch is scoped to `since` (the epoch), so a later re-publish base
		// still clears it and re-arms. done gates ONLY the dead-man arming (SetExpected) path, so
		// latching a heartbeat-armed absence's entry (if one lingers from before the device first
		// reported) is harmless — heartbeat re-arming (apply) never consults it.
		if st, ok := e.expected[key]; ok {
			st.done = true
			e.expected[key] = st
		}
		// one-shot: the wheel already consumed the timer; next heartbeat re-arms.
	case Duration:
		if _, held := e.active[key]; held {
			e.emit(r, key, deadline)
			delete(e.active, key)
		}
	case Session:
		// The gap elapsed: the session is closed. Evaluate its aggregate and clear it;
		// the next event for this key opens a fresh session (re-arms the wheel).
		if pa, ok := e.session[key]; ok {
			if cmp(r.Op, pa.value(r.Agg), r.Thresh) {
				e.emitValue(r, key, deadline, pa.value(r.Agg))
			}
			delete(e.session, key)
		}
	}
}

func (e *Engine) emit(r Rule, key SeriesKey, at time.Time) {
	e.out = append(e.out, Detection{RuleID: r.ID, Series: key.Series, Kind: r.Kind, At: at})
}

// emitValue emits a detection carrying a COMPUTED scalar the fire is about (the delta/rate, or a
// window/session aggregate) — always present, so HasValue is true. Value-less fires (Absence,
// Duration, Correlation) use emit instead.
func (e *Engine) emitValue(r Rule, key SeriesKey, at time.Time, value float64) {
	e.out = append(e.out, Detection{RuleID: r.ID, Series: key.Series, Kind: r.Kind, At: at, Value: value, HasValue: true})
}

// emitSample emits a detection carrying the triggering EVENT's own sample value (Threshold,
// Repeating). It propagates the event's HasValue, so a metric-less raw-CEL leaf (which reads no
// sample) carries no value rather than a fabricated 0 — the distinction a raiseAlarm action needs
// to avoid stamping a fake last value.
func (e *Engine) emitSample(r Rule, ev Event) {
	e.out = append(e.out, Detection{RuleID: r.ID, Series: ev.Key.Series, Kind: r.Kind, At: ev.Time, Value: ev.Value, HasValue: ev.HasValue})
}

// --- snapshot / restore (atomic-with-sequence in the real store; bytes here) ---

type snapActive struct {
	Rule   string    `json:"rule"`
	Series string    `json:"series"`
	Since  time.Time `json:"since"`
}

type snapExpected struct {
	Rule   string    `json:"rule"`
	Series string    `json:"series"`
	Since  time.Time `json:"since"`
	Done   bool      `json:"done"`
}

type snapshot struct {
	Watermark time.Time      `json:"watermark"`
	LastSeq   uint64         `json:"lastSeq"`
	Active    []snapActive   `json:"active"`
	Timers    []snapTimer    `json:"timers"`
	Gens      []snapGen      `json:"gens"`
	Sliding   []snapSliding  `json:"sliding"`
	Panes     []snapPane     `json:"panes"`
	Deltas    []snapDelta    `json:"deltas"`
	Counts    []snapCount    `json:"counts"`
	Sessions  []snapSession  `json:"sessions"`
	Slides    []snapSlide    `json:"slides"`
	Corr      []snapCorr     `json:"corr"`
	Expected  []snapExpected `json:"expected"`
}

// Snapshot serializes the full engine state. In the service this is committed to Postgres
// in the SAME transaction as LastSeq (ack-on-checkpoint); here it round-trips through
// bytes to prove the state is fully serializable.
//
// Pending detections (e.out) are deliberately NOT serialized: the runtime contract is
// deliver-before-checkpoint — Drain() and durably hand off every detection BEFORE snapshotting
// and acking the sequence that produced them. A snapshot taken with e.out non-empty would drop
// those detections on a crash (replay from LastSeq+1 re-derives state, not the already-emitted
// signals). The checkpoint loop (ADR-051 slice 2a) upholds this by draining on every event.
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
	expected := make([]snapExpected, 0, len(e.expected))
	for k, st := range e.expected {
		expected = append(expected, snapExpected{Rule: k.Rule, Series: k.Series, Since: st.since, Done: st.done})
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].Rule != expected[j].Rule {
			return expected[i].Rule < expected[j].Rule
		}
		return expected[i].Series < expected[j].Series
	})
	return json.Marshal(snapshot{
		Watermark: e.wm.now,
		LastSeq:   e.lastSeq,
		Active:    active,
		Timers:    timers,
		Gens:      gens,
		Sliding:   sliding,
		Panes:     panes,
		Deltas:    e.snapshotDeltas(),
		Counts:    e.snapshotCounts(),
		Sessions:  e.snapshotSessions(),
		Slides:    e.snapshotSlides(),
		Corr:      e.snapshotCorr(),
		Expected:  expected,
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
	e.wm.now = s.Watermark
	e.lastSeq = s.LastSeq
	for _, a := range s.Active {
		e.active[SeriesKey{Rule: a.Rule, Series: a.Series}] = a.Since
	}
	e.wheel = restoreTimerWheel(s.Timers, s.Gens)
	e.restoreWindows(s.Sliding, s.Panes)
	e.restoreDeltas(s.Deltas)
	e.restoreCounts(s.Counts)
	e.restoreSessions(s.Sessions)
	e.restoreSlides(s.Slides)
	e.restoreCorr(s.Corr)
	for _, x := range s.Expected {
		e.expected[SeriesKey{Rule: x.Rule, Series: x.Series}] = expectedState{since: x.Since, done: x.Done}
	}
	return e, nil
}
