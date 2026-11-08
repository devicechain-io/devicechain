// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package evaluate runs a profile's detection rules over SUPPLIED events in EVENT TIME and reports
// the edges the rules derive plus the REACT intents those edges would produce, executing nothing.
// It is the engine behind the evaluateRules query, and the reason a rule can be unit-tested.
//
// WHAT IT SHARES WITH PRODUCTION, AND WHAT IT DOES NOT. It drives the SAME pieces the live loop
// drives, in the same order: the registry's Plan (the fan-out that turns a resolved event into core
// events), the keyed-streaming detection Engine, the Publisher's Prepare (the step that stamps
// the derived event's kind, edge and dedup identity) and the REACT Dispatcher (the step that decides
// which actions a detection triggers, renders their templates and builds the requests). The only
// things replaced are the ENDS: the events come from the caller instead of a stream, and the
// Dispatcher's sinks record the request they are handed instead of sending it. So an intent reported
// here is the request production would have sent for that edge, rendered by the code that renders it
// there, not a re-implementation of it that could drift.
//
// ISOLATION. Like the replay preview (package preview), a run builds a fresh Engine, a fresh
// registry holding only the rules it was given, and a Publisher with no writer: it has no way to
// publish, snapshot, acknowledge or enqueue anything. The Dispatcher's sinks are in this package and
// only append to a slice. The run touches no stream, so it cannot move the live watermark, advance
// a durable consumer or perturb the live singleton, which is the property a test of a tenant's
// rules needs from it.
//
// TIME. A run has no wall clock. The Engine's frontier moves only with the supplied events'
// instants and with one final Advance, so a rule that waits seven days is tested by advancing
// seven days, which is arithmetic, not waiting. The run is a pure function of its input: the same
// rules and events produce the same edges and intents, byte for byte, on any machine at any hour.
// The two places a wall clock could leak in are both pinned: a derived event's TriggeredAt is the
// virtual instant of the input that caused the detection, and a command's expiry is measured from
// that instant (react.Dispatcher.SetClock) instead of from time.Now.
//
// 🔴 WHAT IT DOES NOT MODEL, said here so a green test is not read as more than it is. It resolves
// no device attributes (a dynamic threshold reads none, as in the preview), it has no device
// registry, so a command is not checked against the device's command vocabulary (command-delivery
// does that at enqueue), a connector reference is not checked to exist, and a rule scoped to an
// entity group is not evaluated at all (an event here carries no group memberships): it is skipped
// and named in Result.Skipped rather than evaluated against a membership that was never supplied.
package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

const (
	// DefaultMaxEvents is how many events one run accepts when the caller names no limit. A size
	// cap replaces the preview's 24-hour window: a test is judged on what it supplies, not on how
	// long ago it happened, so the bound that keeps a run finite is its size.
	DefaultMaxEvents = 100_000
	// HardMaxEvents is the most a caller may raise the event limit to.
	HardMaxEvents = 250_000
	// DefaultMaxIntents is how many intents one run keeps when the caller names no limit.
	DefaultMaxIntents = 50_000
	// HardMaxIntents is the most a caller may raise the intent limit to.
	HardMaxIntents = 250_000
	// MaxMetricsPerEvent bounds the readings one supplied event carries.
	MaxMetricsPerEvent = 256
)

// Rule is one rule to evaluate: the token it is filed under (what the edges and intents name it by)
// and its authored definition. The definition's own ID is ignored; the run composes the id.
type Rule struct {
	Token string
	Def   rules.Rule
}

// Metric is one reading of a supplied event.
type Metric struct {
	Name  string
	Value float64
}

// Event is one supplied measurement event: a device reports readings that occurred at At.
type Event struct {
	Device  string
	At      time.Time
	Metrics []Metric
}

// Params is one run's input.
type Params struct {
	// Tenant and ProfileVersion are the scope the rules are filed under, exactly as production
	// files them: the events are stamped with ProfileVersion, which is what selects the rules.
	Tenant         string
	ProfileVersion string
	Rules          []Rule
	// Events are handed to the engine in the order given, which is the order of DELIVERY. An event
	// listed after one with a later instant is delivered late, which is how a reordering fault is
	// expressed; the engine treats it as it treats it live.
	Events []Event
	// AdvanceTo is the instant the engine's frontier is moved to after the last event, which is
	// what makes a timer-driven rule (absence, a held duration, a closing window) decide. Zero
	// means the last event's instant.
	AdvanceTo time.Time
	// MaxEvents and MaxIntents are the caller's limits; zero means the default. Neither may exceed
	// its hard ceiling.
	MaxEvents  int
	MaxIntents int
	// AppliedGuard applies the live duplicate-event guard (on exactly when event envelopes are on),
	// so a duplicate is dropped here when it would be dropped there.
	AppliedGuard bool
}

// Edge is one transition a rule derived.
type Edge struct {
	At        time.Time
	Series    string
	RuleToken string
	Kind      string // the rule type, "threshold", "absence", ...
	Severity  string
	Raise     bool
	Value     float64
	HasValue  bool
}

// Intent kinds, the fixed set of side effects REACT can request.
const (
	KindRaiseAlarm  = "raiseAlarm"
	KindClearAlarm  = "clearAlarm"
	KindSendCommand = "sendCommand"
	KindHTTPCall    = "httpCall"
	KindPublish     = "publish"
)

// Intent is one side effect an edge would have caused: the request REACT builds for it, which a
// run records and does not send.
type Intent struct {
	At        time.Time
	Series    string
	RuleToken string
	Raise     bool // the edge that caused it
	Kind      string
	// Token is the idempotency token the request would carry.
	Token string

	// Alarm (raiseAlarm, clearAlarm).
	AlarmKey string
	Severity string
	Value    *float64

	// Command (sendCommand): the command key and its argument object, as authored.
	Command   string
	Payload   string
	ExpiresAt *time.Time

	// Connector (httpCall, publish): the destination and the body REACT rendered.
	Connector string // publish: the connector token
	URL       string // httpCall: the endpoint
	Method    string // httpCall: the HTTP method
	Body      string // the rendered payload
	TimeoutMs int
}

// Stats are the run's counters.
type Stats struct {
	EventsFed      int
	EventsDropped  int // duplicates the applied guard refused
	EvalErrors     int
	LateReadings   int
	RulesEvaluated int
}

// Result is a completed run. Edges and Intents are in event order: by instant, then series, then
// rule, raise before resolve at a shared instant, and an edge's intents follow it in the rule's
// action order.
type Result struct {
	Edges   []Edge
	Intents []Intent
	Stats   Stats
	// Skipped names each rule the CALLER chose not to hand to the run, and why; Run itself
	// evaluates every rule it is given, so it leaves this empty.
	Skipped []string
	// Degraded is non-empty when the result is partial for a reason the author should know.
	Degraded string
}

// RuleError is a rule that does not compile. The caller maps it to a diagnostic.
type RuleError struct {
	Token string
	Err   error
}

func (e *RuleError) Error() string { return fmt.Sprintf("rule %q: %v", e.Token, e.Err) }
func (e *RuleError) Unwrap() error { return e.Err }

// LimitError is a run refused for exceeding a limit, before it did any work.
type LimitError struct{ Msg string }

func (e *LimitError) Error() string { return e.Msg }

// InputError is a run refused because its input is malformed.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// ErrNoRules is returned when the run was given nothing to evaluate.
var ErrNoRules = &InputError{Msg: "there are no rules to evaluate"}

// Run evaluates p. It returns a typed error (RuleError, LimitError, InputError) for input it
// refuses and a plain error only for a cancelled context.
func Run(ctx context.Context, p Params) (Result, error) {
	maxEvents, maxIntents := p.MaxEvents, p.MaxIntents
	if maxEvents <= 0 {
		maxEvents = DefaultMaxEvents
	}
	if maxIntents <= 0 {
		maxIntents = DefaultMaxIntents
	}
	if maxEvents > HardMaxEvents {
		return Result{}, &LimitError{Msg: fmt.Sprintf("the event limit %d is above the maximum of %d", maxEvents, HardMaxEvents)}
	}
	if maxIntents > HardMaxIntents {
		return Result{}, &LimitError{Msg: fmt.Sprintf("the intent limit %d is above the maximum of %d", maxIntents, HardMaxIntents)}
	}
	if len(p.Events) > maxEvents {
		return Result{}, &LimitError{Msg: fmt.Sprintf("%d events were supplied, above the limit of %d", len(p.Events), maxEvents)}
	}
	if p.Tenant == "" || p.ProfileVersion == "" {
		return Result{}, &InputError{Msg: "a tenant and a profile version are required"}
	}
	if len(p.Rules) == 0 {
		return Result{}, ErrNoRules
	}
	if err := validateEvents(p.Events); err != nil {
		return Result{}, err
	}
	last := time.Time{}
	for _, e := range p.Events {
		if e.At.After(last) {
			last = e.At
		}
	}
	advanceTo := p.AdvanceTo
	if advanceTo.IsZero() {
		advanceTo = last
	}
	if advanceTo.Before(last) {
		return Result{}, &InputError{Msg: fmt.Sprintf("advanceTo %s is before the last event at %s",
			advanceTo.UTC().Format(time.RFC3339), last.UTC().Format(time.RFC3339))}
	}

	scoped, defs, err := compileAll(p)
	if err != nil {
		return Result{}, err
	}
	reg := runtime.NewRuleRegistry(scoped)
	engine := core.NewEngine(reg.Cores(), 0)
	pub := runtime.NewPublisher(nil, reg, nopRuntimeMetrics{})
	rec := &recorder{}
	disp := react.NewDispatcher(defResolver(defs), rec, alarmSink{rec}, connectorSink{rec}, nil, nopReactMetrics{})

	var applied *runtime.AppliedGuard
	if p.AppliedGuard {
		applied = runtime.NewAppliedGuard(0, 0)
	}

	run := &runState{pub: pub, disp: disp, rec: rec, maxIntents: maxIntents}
	res := Result{}
	res.Stats.RulesEvaluated = len(scoped)

	for i, e := range p.Events {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
		ev := resolve(p.ProfileVersion, e)
		if applied != nil {
			if id, ierr := runtime.EventIdentity(p.Tenant, ev); ierr == nil && !applied.Admit(e.At, id) {
				res.Stats.EventsDropped++
				continue
			}
		}
		seq := uint64(i + 1)
		plan := reg.Plan(seq, p.Tenant, ev, e.At, nil, nil)
		res.Stats.EvalErrors += plan.EvalErrors
		res.Stats.EventsFed++
		engine.ProcessReceived(seq, e.At, runtime.ReceiptOf(ev, e.At), plan.Events)
		run.collect(engine.Drain(), e.At)
	}

	// One nanosecond past the instant, as the preview does: the rate and count-window kinds decide
	// a reading only once the frontier is strictly past it, and advancing to the instant exactly
	// would replay the last reading and never decide it. A timer due in the nanosecond stamps its
	// edge after advanceTo, which the filter below drops.
	engine.Advance(advanceTo.Add(time.Nanosecond))
	run.collect(engine.Drain(), advanceTo)

	if n := engine.DrainLateSamples(); n > 0 {
		res.Stats.LateReadings = int(n)
		res.Degraded = joinReasons(res.Degraded, fmt.Sprintf(
			"%d reading(s) arrived too far behind the other readings to be used by the rule; firings may be incomplete", n))
	}
	if res.Stats.EvalErrors > 0 {
		res.Degraded = joinReasons(res.Degraded, "some events could not be evaluated (a rule predicate error); firings may be incomplete")
	}
	if run.intentsDropped > 0 {
		res.Degraded = joinReasons(res.Degraded, fmt.Sprintf(
			"the intent limit of %d was reached; %d later intent(s) were not recorded", maxIntents, run.intentsDropped))
	}
	if run.unattributed > 0 {
		res.Degraded = joinReasons(res.Degraded, fmt.Sprintf(
			"%d detection(s) could not be turned into a derived event and were dropped", run.unattributed))
	}

	// Deterministic order, then flatten: an edge's intents ride with it.
	sort.SliceStable(run.dets, func(i, j int) bool { return edgeLess(run.dets[i].edge, run.dets[j].edge) })
	for _, d := range run.dets {
		if d.edge.At.After(advanceTo) {
			continue
		}
		res.Edges = append(res.Edges, d.edge)
		res.Intents = append(res.Intents, d.intents...)
	}
	return res, nil
}

// edgeLess is the order a result lists edges in: by instant, then device, then rule, and raised
// before resolved at a shared instant, so a re-raise that lands on the instant of the resolve it
// follows is read in the order it happened.
func edgeLess(a, b Edge) bool {
	if !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	if a.Series != b.Series {
		return a.Series < b.Series
	}
	if a.RuleToken != b.RuleToken {
		return a.RuleToken < b.RuleToken
	}
	return a.Raise && !b.Raise
}

func joinReasons(dst, reason string) string {
	if dst == "" {
		return reason
	}
	return dst + "; " + reason
}

func validateEvents(events []Event) error {
	for i, e := range events {
		if e.Device == "" {
			return &InputError{Msg: fmt.Sprintf("event %d has no device", i)}
		}
		if e.At.IsZero() {
			return &InputError{Msg: fmt.Sprintf("event %d has no occurred time", i)}
		}
		if len(e.Metrics) == 0 {
			return &InputError{Msg: fmt.Sprintf("event %d carries no readings", i)}
		}
		if len(e.Metrics) > MaxMetricsPerEvent {
			return &InputError{Msg: fmt.Sprintf("event %d carries %d readings, above the maximum of %d", i, len(e.Metrics), MaxMetricsPerEvent)}
		}
		for _, m := range e.Metrics {
			if m.Name == "" {
				return &InputError{Msg: fmt.Sprintf("event %d has a reading with no name", i)}
			}
			if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
				return &InputError{Msg: fmt.Sprintf("event %d reading %q is not a finite number", i, m.Name)}
			}
		}
	}
	return nil
}

// resolve builds the resolved event production would hold for a supplied event: stamped with the
// profile version that selects the rules, received at the instant it occurred (a reading that
// reached the stream promptly, which is the live default).
func resolve(profileVersion string, e Event) *resolvedevent.ResolvedEvent {
	entries := make([]resolvedevent.ResolvedMeasurementEntry, len(e.Metrics))
	for i, m := range e.Metrics {
		entries[i] = resolvedevent.ResolvedMeasurementEntry{Name: m.Name, Value: strconv.FormatFloat(m.Value, 'g', -1, 64)}
	}
	return &resolvedevent.ResolvedEvent{
		Source:              "evaluate",
		SourceDeviceToken:   e.Device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        e.At,
		ProcessedTime:       e.At,
		EventType:           eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: e.At,
			Entries:      entries,
		}}},
	}
}

// compileAll files every rule under the production id and compiles it with the production limits.
func compileAll(p Params) ([]runtime.ScopedRule, map[string]rules.Rule, error) {
	limits := rules.DefaultLimits()
	scoped := make([]runtime.ScopedRule, 0, len(p.Rules))
	defs := make(map[string]rules.Rule, len(p.Rules))
	seen := make(map[string]bool, len(p.Rules))
	for _, r := range p.Rules {
		if r.Token == "" {
			return nil, nil, &InputError{Msg: "a rule has no token"}
		}
		if seen[r.Token] {
			return nil, nil, &InputError{Msg: fmt.Sprintf("two rules share the token %q", r.Token)}
		}
		seen[r.Token] = true
		def := r.Def
		def.ID = runtime.PublishedRuleID(p.Tenant, p.ProfileVersion, r.Token)
		compiled, err := rules.Compile(def, limits)
		if err != nil {
			return nil, nil, &RuleError{Token: r.Token, Err: err}
		}
		if compiled.RequiresPosition {
			return nil, nil, &RuleError{Token: r.Token, Err: errors.New(
				"the rule tests geofence containment, which needs a position and a fence set that an evaluation does not supply")}
		}
		scoped = append(scoped, runtime.ScopedRule{Tenant: p.Tenant, ProfileVersionToken: p.ProfileVersion, Compiled: compiled})
		defs[def.ID] = def
	}
	return scoped, defs, nil
}

// detection is one drained detection with the intents it caused.
type detection struct {
	edge    Edge
	intents []Intent
}

type runState struct {
	pub            *runtime.Publisher
	disp           *react.Dispatcher
	rec            *recorder
	maxIntents     int
	intents        int
	intentsDropped int
	unattributed   int
	dets           []detection
}

// collect turns drained detections into edges and the intents they cause. triggeredAt is the
// virtual platform time of the input that moved the engine.
func (s *runState) collect(dets []core.Detection, triggeredAt time.Time) {
	for _, det := range dets {
		po, ok := s.pub.Prepare(det, triggeredAt)
		if !ok {
			s.unattributed++
			continue
		}
		var de runtime.DerivedEvent
		if err := json.Unmarshal(po.Payload, &de); err != nil {
			s.unattributed++
			continue
		}
		_, token, _ := runtime.ProfileAndRuleToken(det.RuleID)
		edge := Edge{
			At: det.At, Series: det.Series, RuleToken: token, Kind: de.Kind, Severity: de.Severity,
			Raise: det.Edge == core.EdgeRaised, Value: det.Value, HasValue: det.HasValue && det.Edge == core.EdgeRaised,
		}
		s.rec.begin(edge)
		s.disp.SetClock(func() time.Time { return triggeredAt })
		s.disp.Dispatch(context.Background(), de)
		intents := s.rec.take()
		kept := intents[:0]
		for _, in := range intents {
			if s.intents >= s.maxIntents {
				s.intentsDropped++
				continue
			}
			s.intents++
			kept = append(kept, in)
		}
		s.dets = append(s.dets, detection{edge: edge, intents: kept})
	}
}
