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

// EdgeKind discriminates the two edges of an alarm-bearing detection (ADR-057). A rule is
// LEVEL over time — a series is either in its satisfied (alarming) state or not — and the
// engine emits a signal only on the TRANSITIONS: a Raised on the rising edge (a series
// entering the satisfied state) and a Resolved on the falling edge (leaving it). The raise/
// resolve pair is what lets the alarm object downstream integrate per-rule edges into a
// cleared-when-the-condition-ceases lifecycle, replacing the retired level-triggered
// measurement evaluator's clear-on-unsatisfied semantics (ADR-041 → ADR-057).
//
// The zero value is EdgeRaised, so every legacy fire site is a raise by default and only the
// explicit resolve path stamps EdgeResolved. Edge is part of the detection's dedup identity (it is
// not zeroed by the identity projection), so a Raised and a Resolved that happen to share an At — a
// single kind never emits both for one event, but a gap-driven falling edge can coincide with a
// fresh rising edge at the same timestamp on ADJACENT events — never collapse into one another
// under a downstream at-least-once dedup. Note that (rule, series, kind, At, edge) still cannot
// distinguish two SEPARATE raise episodes at the identical timestamp (match/non-match/match all at
// one At); that residual same-At-same-edge collapse is a documented 6d-pre-2 concern — the alarm
// object's monotonic decision-time guard resolves it, and it requires multiple events at a byte-
// identical event time, which real per-device telemetry does not produce.
//
// The edges of one series are also NOT globally time-ordered: with the D2/D5 falling edge (6d-pre-2a),
// a bounded-late matching event can open a fresh window and stamp a Raised at an event time EARLIER
// than an already-emitted Resolved for the same key (resolve@12 then a late match@9 → raise@9). Both
// survive dedup (distinct At), but they reach the alarm object out of At order — so the 6d-pre-2 alarm
// integrator's per-contributor monotonic decision-time guard (not arrival order) is what must decide
// the final raised/cleared state; this is explicitly on the hook, not just the same-At case above.
type EdgeKind uint8

const (
	EdgeRaised   EdgeKind = iota // the series entered its satisfied (alarming) state
	EdgeResolved                 // the series left it (the condition ceased)
)

// Silence and the falling edge (ADR-057). A falling edge is observed differently per kind: the
// timer-driven kinds resolve off the watermark even with no further events — Absence resolves on
// the recovering heartbeat, Duration/Session on their run/gap breaking — but the EVENT-driven
// window kinds (Threshold non-match, DeltaRate, Repeating, SlidingAgg, Aggregate, CountWindow,
// Correlation) only re-evaluate when an event arrives, so a series that goes FULLY silent while
// raised stays raised until it reports again. This is intentional for v1: an idle watermark
// advance carries no reading to re-evaluate a level against, and a device that simply stops is the
// Absence rule's job to catch, not a threshold's. Operators pair a level rule with an Absence rule
// when "stopped reporting" must also clear (or escalate) the alarm.
//
// Non-matching gate-metric events and the falling edge (ADR-057 reviews D2/D5, 6d-pre-2a). A rule with
// a filtering `when` leaf (e.g. count of readings where mode==heating) receives NON-matching samples
// that carry its metric, and must resolve a raised alarm as the condition ceases rather than staying
// raised while the device actively reports non-matching values. Coverage per kind:
//   - Threshold / DeltaRate: resolve directly on a non-matching sample (apply / applyDeltaRate).
//   - Repeating / SlidingAgg / Correlation (sliding, time-eviction): advance eviction on EVERY delivered
//     event and record only the matching sample, so the falling edge is observed as qualifying samples
//     age out of the trailing window. Their RISING edge is match-only (a non-match never grows the
//     window), so a non-match can only age toward the falling edge.
//   - Aggregate (tumbling TIME window): a raised series opens an empty pane on a non-matching event so
//     an all-non-matching window still closes on the watermark and resolves (an empty pane never
//     satisfies, closePanes).
//   - CountWindow / Session: INHERENT residual — a window counted in matching events (no time axis) and
//     a session opened only by a matching event cannot observe a falling edge from pure non-matching
//     traffic. A raised alarm persists until the next completed window/session; operators pair with an
//     Absence rule (see each function's KNOWN RESIDUAL note).
// A match-every-leaf rule (the common case) is byte-identical to the pre-D2/D5 behavior — every event
// matches, so nothing is ever skipped and no falling edge is reachable from non-matching traffic. This
// is the NON-matching-traffic falling edge; the FULLY-silent case is the note directly above.

// Detection is an emitted signal. Its identity (RuleID, Series, Kind, At, Edge) is stable and
// deterministic, so at-least-once re-emission across a restart is dedup-collapsible
// downstream via an idempotency key (ADR-051 §8) — the property the replay test asserts.
type Detection struct {
	RuleID string
	Series string
	Kind   RuleKind
	// Edge is the transition this detection reports (ADR-057): Raised on the rising edge,
	// Resolved on the falling edge. Part of the dedup identity.
	Edge EdgeKind
	// At is the logical (event) time the detection is stamped at, and is part of its dedup
	// identity: the triggering event time for Threshold, DeltaRate, CountWindow, SlidingAgg,
	// and Correlation; the elapsed deadline for Absence, Duration, and Session; the window end
	// for Aggregate. A Resolved is stamped at the event/deadline time the falling edge was
	// observed.
	At time.Time
	// Value is the scalar the detection is ABOUT, when one is meaningful: the crossing sample for
	// Threshold/Repeating (the event's own gate-metric reading, via emitSample), the computed
	// delta/rate for DeltaRate, and the window/session aggregate for CountWindow/SlidingAgg/Aggregate/
	// Session. It is an informational payload the raise-alarm REACT action stamps on the alarm (so a
	// re-raise carries the real triggering value, not a zero), NOT part of the dedup identity above —
	// a downstream at-least-once collapse keys only on (RuleID, Series, Kind, At, Edge). HasValue
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
	// raised is the ADR-057 two-edge latch: a series currently in its satisfied (alarming) state,
	// mapped to the EVENT TIME of its rising edge. The time is what makes a falling edge reject a
	// STALE out-of-order event: a non-matching/dipping reading older than the raise cannot resolve
	// an alarm the latest reading still supports (review D3/F2). It is snapshotted, so the level and
	// its origin survive a restart.
	raised map[SeriesKey]time.Time
	out    []Detection
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
		raised:   map[SeriesKey]time.Time{},
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
	deleteSeriesKeys(e.raised, id)
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

// Descope drops ALL keyed state for one exact (rule, series) and resolves a raised alarm — the
// ADR-062 S4 membership-flip. When a group-scoped rule's series (device) leaves the scoped
// group, the resolved event stops carrying the rule's group@v (ScopeMemberships), so the
// runtime feeds a descope here instead of a normal sample: any raised alarm resolves and every
// window/timer state for the series is torn down, so nothing fires spuriously for a series the
// rule no longer covers (a mid-hold Duration timer or an open Aggregate pane would otherwise
// fire off the watermark with no event to re-check scope). It is the per-exact-SeriesKey
// analogue of RemoveRule (which is per-rule, too coarse — other series of the rule keep running).
//
// Idempotent and cheap in the common case: most events are out of scope for any given scoped
// rule, and a series with no state and no latch is a no-op. Replay-safe and deterministic: the
// only input is the immutable event's ScopeMemberships plus snapshotted state. The falling-edge
// resolve is stale-guarded exactly like resolve() (an out-of-order event older than the rising
// edge does not clear an alarm the latest reading still supports). Runs on the single-writer
// loop. Returns whether it changed any state (so the caller can mark the checkpoint dirty).
func (e *Engine) Descope(ruleID, series string, at time.Time) bool {
	r, ok := e.rules[ruleID]
	if !ok {
		// The rule is gone (removed between fan-out and here): its state, if any, was GC'd by
		// RemoveRule. Nothing to resolve or drop.
		return false
	}
	key := SeriesKey{Rule: ruleID, Series: series}
	before := len(e.out)
	// Resolve a raised alarm. Unlike a value falling edge, a descope is NOT subject to the D3/F2
	// event-time stale guard: membership is stamped at RESOLUTION and is monotone with stream
	// sequence, so a bounded-late out-of-scope event is still the current word on membership (in
	// seq order) — suppressing its resolve would strand the alarm raised forever if the device
	// never reports again. So resolve at max(at, rising-edge): never before the raise (which
	// would be a nonsensical negative-duration alarm) but always emitted.
	if raisedAt, raised := e.raised[key]; raised {
		resolveAt := at
		if resolveAt.Before(raisedAt) {
			resolveAt = raisedAt
		}
		e.resolve(r, key, resolveAt) // resolveAt >= raisedAt, so resolve() always emits + clears
	}
	dropped := e.dropSeriesKey(key, r.Kind)
	return dropped || len(e.out) > before
}

// dropSeriesKey garbage-collects one exact SeriesKey across the per-series structures, reporting
// whether anything was removed. It deliberately does NOT touch the raised latch (Descope's
// resolve owns it) and does NOT drop buffered out-detections: a resolved event is wholly in- or
// out-of-scope for a given rule (memberships are per-event, not per-sample), so a descope never
// coincides with that same rule's freshly-buffered raise.
//
// It stays O(1) for the overwhelmingly common no-state descope (most events are out of scope for
// any given scoped rule): the eight SeriesKey maps are direct-key deletes; the pane map + close
// heap are swept ONLY for Aggregate (the sole kind that opens them), so every other kind skips
// the O(all-panes)/O(heap) walk; and the timer is cancelled LAZILY (an O(1) generation bump)
// only when the key actually has a live timer — the snapshot persists only live timers, so the
// invalidated entry never reaches durability and popDue discards it when its deadline passes.
func (e *Engine) dropSeriesKey(key SeriesKey, kind RuleKind) bool {
	n := 0
	n += dropOneKey(e.active, key)
	n += dropOneKey(e.sliding, key)
	n += dropOneKey(e.lastVal, key)
	n += dropOneKey(e.counts, key)
	n += dropOneKey(e.session, key)
	n += dropOneKey(e.slides, key)
	n += dropOneKey(e.corr, key)
	n += dropOneKey(e.expected, key)
	if kind == Aggregate {
		for pk := range e.panes {
			if pk.Rule == key.Rule && pk.Series == key.Series {
				n++
				delete(e.panes, pk)
			}
		}
		e.closes.purgeSeriesKey(key)
	}
	if _, hasTimer := e.wheel.live[key]; hasTimer {
		e.wheel.cancel(key)
		n++
	}
	return n > 0
}

// dropOneKey deletes one exact SeriesKey from a state map, returning 1 if it was present
// (else 0) — the per-key counterpart to deleteSeriesKeys, used by dropSeriesKey to report
// whether a descope actually removed state.
func dropOneKey[V any](m map[SeriesKey]V, key SeriesKey) int {
	if _, ok := m[key]; ok {
		delete(m, key)
		return 1
	}
	return 0
}

func countSeriesKeys[V any](counts map[string]int, m map[SeriesKey]V) {
	for k := range m {
		counts[k.Rule]++
	}
}

// LiveKeyCounts returns, per rule id, the number of live keyed-state ENTRIES the rule holds — its
// open windows, running timers, and accumulators. This is the rule's contribution to the engine's
// memory footprint, which the per-tenant runtime state budget (ADR-023 amendment, ADR-051 slice 6c)
// is measured against; the caller rolls rule ids up to tenants via the id's tenant prefix.
//
// It counts ENTRIES (a memory proxy), NOT distinct series: a timer-bearing key is counted BOTH in
// its state map AND in the timer wheel's LIVE set, because each is a real, separately-allocated entry
// — a Duration or Session key holds an active/session accumulator plus a wheel timer (two entries,
// ~two units of memory). Crucially, counting the wheel is what captures an ABSENCE rule armed by a
// device REPORTING: that heartbeat timer lives ONLY in the wheel (no state-map entry — see apply's
// Absence case), so a state-maps-only count would report 0 live keys for the common absence case and
// blind the budget to the flagship feature's memory. A Correlation series is counted as its anchor
// key PLUS its live distinct-member set (each member is a retained entry), so a correlation rule is
// not under-measured by up to MemberCap the way an anchor-only count would be.
//
// It counts the wheel's LIVE deadlines (wheel.live), not its gens ledger: gens retains one generation
// counter per (rule, series) that ever held a timer and is swept only on rule removal (the anti-gen-
// recycle invariant), so it is a separate, slowly-accumulating overhead under device churn — real but
// distinct from current live load, which is what a suppression budget should target; it is bounded
// per rule by distinct-series-ever-timed and reclaimed on RemoveRule. Computed on demand on the
// single-writer loop (no lock, no hot-path cost).
func (e *Engine) LiveKeyCounts() map[string]int {
	counts := make(map[string]int)
	countSeriesKeys(counts, e.active)
	countSeriesKeys(counts, e.sliding)
	countSeriesKeys(counts, e.lastVal)
	countSeriesKeys(counts, e.counts)
	countSeriesKeys(counts, e.session)
	countSeriesKeys(counts, e.slides)
	countSeriesKeys(counts, e.expected)
	countSeriesKeys(counts, e.raised)     // ADR-057 two-edge latch: a raised series is a live entry
	countSeriesKeys(counts, e.wheel.live) // heartbeat-armed absence timers live ONLY here
	// Correlation: the anchor key plus each retained distinct member (the real memory).
	for k, members := range e.corr {
		counts[k.Rule] += 1 + len(members)
	}
	for pk := range e.panes {
		counts[pk.Rule]++
	}
	return counts
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
	// A device leaving an absence rule's scope ends any absence currently raised for it: resolve so
	// the latch clears (review H1/D6). Clearing is what matters for correctness — otherwise a reused/
	// re-created token under the same rule id inherits the dead incarnation's latch and its dead-man
	// raise is suppressed forever. A resolve (rather than a bare delete) also hands 6d-pre-2's alarm
	// object the clear; it is stamped at the watermark, since a control-plane departure has no event
	// time. If the rule is gone (a stale expected entry restored for a removed rule), clear directly.
	if r, ok := e.rules[key.Rule]; ok {
		e.resolve(r, key, e.wm.now)
	} else {
		delete(e.raised, key)
	}
}

// ExpectedKeys returns every series currently dead-man-armed (fired or not), so the runtime's
// restart reconciliation (slice 4c-2b-2b) can diff the engine's armed set against the roster +
// active-version read-models and RemoveExpected the entries whose membership is gone (a device
// deleted, or a profile version superseded, while the process was down) — INCLUDING an entry for
// a rule the restored rule set no longer holds (Restore reloads expected unconditionally), which
// the reconciliation must treat as membership-gone or it persists in every future snapshot. It
// returns a point-in-time copy of the keys, so mutating the engine while iterating it is safe.
// The armed set is bounded by (absence rules × rostered devices) — a key-cardinality class (not event
// volume) that LiveKeyCounts measures against the ADR-023 state budget (the expected map is counted).
// The wheel's gens ledger is a related but separate per-ever-timed-series overhead that LiveKeyCounts
// does NOT count (see its note). Order is unspecified.
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
		// Level over time (ADR-057): raise on the rising edge (first matching event) and resolve
		// on the falling edge (first non-matching event). The metric-scoped feed contract (slice 3)
		// delivers BOTH matching and metric-present-non-matching events to a Threshold rule, so the
		// falling edge is observable; a series that goes fully silent stays raised (see the package
		// note on silence — Absence covers the went-dark case).
		if ev.Match {
			e.emitSample(r, ev)
		} else {
			e.resolve(r, ev.Key, ev.Time)
		}
	case Absence:
		// Any event is a heartbeat: it both (re)arms the dead-man timer — only ever FORWARD, so a
		// late out-of-order heartbeat cannot shrink a deadline a newer one already set — AND, if an
		// absence for this series is currently raised, IS the recovery that resolves it. The
		// heartbeat is the natural falling edge for absence (ADR-057): the device reporting again is
		// precisely the condition ceasing.
		e.resolve(r, ev.Key, ev.Time)
		e.wheel.scheduleForward(ev.Key, ev.Time.Add(r.Timeout))
	case Duration:
		if ev.Match {
			// Open a matched run and arm one hold timer — but only while NOT already raised: once
			// the hold elapses and raises the alarm (fire), further matches sustain it with no
			// re-arm and no re-fire (the raised latch holds the alarm until the run breaks).
			_, held := e.active[ev.Key]
			_, alreadyRaised := e.raised[ev.Key]
			if !held && !alreadyRaised {
				e.active[ev.Key] = ev.Time
				e.wheel.schedule(ev.Key, ev.Time.Add(r.Hold))
			}
		} else {
			// The matched run broke: cancel a pending (pre-raise) hold and resolve a raised alarm.
			// resolve is a no-op when the run never matured, so a cancelled-before-hold run emits
			// nothing — matching the old evaluator, which only cleared an alarm it had raised.
			delete(e.active, ev.Key)
			e.wheel.cancel(ev.Key)
			e.resolve(r, ev.Key, ev.Time)
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
		// The hold elapsed with the run intact: raise (latched). active is consumed — the raised
		// latch now holds the alarm until the run breaks (apply's non-match branch resolves it).
		if _, held := e.active[key]; held {
			e.emit(r, key, deadline)
			delete(e.active, key)
		}
	case Session:
		// The gap elapsed: the session is closed. Evaluate its aggregate — a satisfied close raises
		// (latched across sessions), an unsatisfied close resolves a prior raise (ADR-057 falling
		// edge: this series' latest session no longer breaches). Either way clear it; the next event
		// opens a fresh session (re-arms the wheel).
		if pa, ok := e.session[key]; ok {
			if cmp(r.Op, pa.value(r.Agg), r.Thresh) {
				e.emitValue(r, key, deadline, pa.value(r.Agg))
			} else {
				e.resolve(r, key, deadline)
			}
			delete(e.session, key)
		}
	}
}

// emit raises a value-less detection (Absence, Duration, Correlation) on the RISING edge, and
// latches the series as raised (ADR-057). It is idempotent while raised: a rule whose condition
// stays satisfied — a duration held past a second hold, a still-absent device, a cohort that
// stays over the correlation line — emits exactly one Raised until a resolve clears the latch, so
// a sustained breach is one alarm, not a flood. The falling edge (resolve) emits the matching
// Resolved.
func (e *Engine) emit(r Rule, key SeriesKey, at time.Time) {
	if _, ok := e.raised[key]; ok {
		return
	}
	e.raised[key] = at
	e.out = append(e.out, Detection{RuleID: r.ID, Series: key.Series, Kind: r.Kind, At: at})
}

// emitValue raises a detection carrying a COMPUTED scalar the fire is about (the delta/rate, or a
// window/session aggregate) — always present, so HasValue is true. Latched exactly like emit.
func (e *Engine) emitValue(r Rule, key SeriesKey, at time.Time, value float64) {
	if _, ok := e.raised[key]; ok {
		return
	}
	e.raised[key] = at
	e.out = append(e.out, Detection{RuleID: r.ID, Series: key.Series, Kind: r.Kind, At: at, Value: value, HasValue: true})
}

// emitSample raises a detection carrying the triggering EVENT's own sample value (Threshold,
// Repeating). It propagates the event's HasValue, so a metric-less raw-CEL leaf (which reads no
// sample) carries no value rather than a fabricated 0 — the distinction a raiseAlarm action needs
// to avoid stamping a fake last value. Latched exactly like emit.
//
// A non-finite sample value is neutralized to no-value (HasValue=false): a NaN/±Inf reading that
// somehow matched the gate must never reach the detection, because the runtime publisher marshals
// Value to JSON and a non-finite float fails to marshal — a TERMINAL publish drop that, with the
// latch already set, would suppress every later raise for this series (review F5/H2). CEL numeric
// comparisons are false for NaN so a structured threshold cannot match one; this guards the raw-CEL
// gate that could match on another field while the value metric is non-finite.
func (e *Engine) emitSample(r Rule, ev Event) {
	if _, ok := e.raised[ev.Key]; ok {
		return
	}
	e.raised[ev.Key] = ev.Time
	value, hasValue := ev.Value, ev.HasValue
	if hasValue && (math.IsNaN(value) || math.IsInf(value, 0)) {
		value, hasValue = 0, false
	}
	e.out = append(e.out, Detection{RuleID: r.ID, Series: ev.Key.Series, Kind: r.Kind, At: ev.Time, Value: value, HasValue: hasValue})
}

// resolve emits a Resolved detection (ADR-057 falling edge) for a series that is currently raised,
// and clears the latch. It is a no-op for a series with no live alarm, so every falling-edge site
// can call it unconditionally — the latch guarantees at most one Resolved per Raised, keeping the
// two edges balanced (never a resolve without a preceding raise, never two in a row). A Resolved
// carries no value: it reports that the condition ceased, not a reading. It is stamped at the
// event time (or elapsed deadline) the falling edge was observed.
//
// It IGNORES a STALE falling edge — one whose event time precedes the rising edge it would clear
// (review D3/F2). Under bounded lateness an out-of-order older reading can arrive after a newer one
// raised the alarm; that older reading is not evidence the condition ended (the latest reading
// still supports it), so resolving on it would spuriously clear-then-re-raise. Every value-folding
// kind already rejects stale samples (DeltaRate, Correlation) or schedules forward-only (Absence,
// Session); this extends the same discipline to the level kinds' falling edges.
func (e *Engine) resolve(r Rule, key SeriesKey, at time.Time) {
	raisedAt, ok := e.raised[key]
	if !ok || at.Before(raisedAt) {
		return
	}
	delete(e.raised, key)
	e.out = append(e.out, Detection{RuleID: r.ID, Series: key.Series, Kind: r.Kind, At: at, Edge: EdgeResolved})
}

// ClearRaised drops the two-edge latch for a series WITHOUT emitting a Resolved — the escape hatch
// for the runtime when it TERMINALLY drops a Raised detection (a stale-roster absence, an orphan/
// backstop reject, a marshal failure) after emit already latched it. Without it the latch would
// suppress every later raise for the series even though downstream never saw the first (review
// H2/F5). Emitting no Resolved is correct: downstream never observed a Raise, so it is owed no
// Resolve. Called on the single-writer loop.
func (e *Engine) ClearRaised(key SeriesKey) { delete(e.raised, key) }

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

// snapRaised persists one entry of the ADR-057 two-edge latch: a (rule, series) currently in its
// raised (alarming) state, with the event time of its rising edge. Without it a restart would lose
// the level and re-raise an already-active alarm (a duplicate Raised, never balanced by the Resolved
// the original raise still owes); losing the At would also disarm the stale-falling-edge guard.
type snapRaised struct {
	Rule   string    `json:"rule"`
	Series string    `json:"series"`
	At     time.Time `json:"at"`
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
	Raised    []snapRaised   `json:"raised"`
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
	raised := make([]snapRaised, 0, len(e.raised))
	for k, at := range e.raised {
		raised = append(raised, snapRaised{Rule: k.Rule, Series: k.Series, At: at})
	}
	sort.Slice(raised, func(i, j int) bool {
		if raised[i].Rule != raised[j].Rule {
			return raised[i].Rule < raised[j].Rule
		}
		return raised[i].Series < raised[j].Series
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
		Raised:    raised,
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
	for _, x := range s.Raised {
		e.raised[SeriesKey{Rule: x.Rule, Series: x.Series}] = x.At
	}
	return e, nil
}
