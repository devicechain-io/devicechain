// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package react is DETECT's REACT stage (ADR-051 / ADR-054): the near-stateless dispatcher that
// turns a derived detection event into its authored side effects. It consumes the derived-event
// stream, resolves the rule that fired from the durable rule projection (NOT from the wire event,
// so an action-chain edit takes effect without re-publishing events), and dispatches each of the
// rule's bounded, declarative actions.
//
// REACT is deliberately separate from the DETECT single-writer engine: DETECT is a stateful,
// replay-correct keyed-streaming loop; REACT is a queue-group-ready, at-least-once consumer whose
// only durability requirement is that each action dispatch be idempotent under redelivery. That
// idempotency is carried by a DETERMINISTIC per-(detection, action-index) token the downstream
// sink dedups on (command-delivery is idempotent on the command token, ADR-051 slice 5b-1) — so a
// redelivered event, a DETECT replay that re-publishes the same detection, and a retry after a
// transient failure all collapse downstream rather than double-acting. There is no permanent-vs-
// transient error classification here: any dispatch failure is retried (the event is not acked),
// and a genuinely un-dispatchable event is bounded by the consumer's redelivery cap (poison), not
// by fragile per-error interpretation.
package react

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/rs/zerolog/log"
)

// Outcome is the disposition of one derived event's dispatch, telling the consumer whether to ack.
type Outcome int

const (
	// Done: every action was dispatched (or a not-yet-enabled action was skipped, or the rule was
	// gone) — ack the event; there is nothing a redelivery would achieve.
	Done Outcome = iota
	// Retry: a failure the dispatcher cannot resolve now (the rule store or a sink was unreachable)
	// — do NOT ack. The event redelivers; the deterministic idempotency tokens make the re-run safe,
	// and the consumer's redelivery cap bounds a permanently-failing event.
	Retry
)

// RuleResolver resolves a rule's REACT layer by the composed runtime rule id (ADR-051 slice 5b).
// It reads the authoritative rule definition from the durable projection — the same source the
// engine rebuilds from — so the dispatcher never trusts the (leaner, snapshot) wire event for the
// action chain.
type RuleResolver interface {
	// Resolve returns the decoded rule for id. found=false means no such rule is persisted (the
	// rule was removed after the detection fired — an orphan, dropped). A non-nil error is a
	// TRANSIENT store failure: the caller retries, never treating "store down" as "rule gone".
	Resolve(ctx context.Context, ruleID string) (rules.Rule, bool, error)
}

// CommandRequest is one send-command dispatch (ADR-043): a command enqueued for a device, carrying
// the deterministic idempotency Token command-delivery dedups on.
type CommandRequest struct {
	Tenant      string
	Token       string
	DeviceToken string
	Command     string
	Payload     string
}

// CommandSink enqueues a command for a device (ADR-043), implemented over command-delivery. Send
// returns nil on success (a fresh enqueue OR an idempotent replay of an already-enqueued token,
// which command-delivery collapses) and a non-nil error on any failure — the dispatcher retries
// every error, so the sink need not classify. A repeat with the same Token never enqueues a second
// command (slice 5b-1), which is what makes at-least-once retry safe.
type CommandSink interface {
	Send(ctx context.Context, req CommandRequest) error
}

// AlarmRequest is one alarm-contributor dispatch (ADR-041 / ADR-057): raise/escalate (Edge=raised) or
// clear/de-escalate (Edge=resolved) this rule's contribution to a device's alarm. It carries no
// idempotency token — device-management's alarm integrator is an upsert keyed on (device, alarmKey)
// whose contributor-set mutations are idempotent and ordered by (OccurredTime, Edge): an older edge is
// ignored and at an equal OccurredTime a resolve wins a raise (RaiseAlarmRequest.OccurredTime), so an
// at-least-once redelivery in ANY order re-derives the same state without a sequence field.
type AlarmRequest struct {
	Tenant      string
	DeviceToken string
	AlarmKey    string
	MetricKey   string
	Severity    string
	// RuleID is the CONTRIBUTOR identity the alarm object reference-counts by — a Raised adds/updates
	// this rule's tier, a Resolved removes it, the alarm clears when the set empties (ADR-057). It is
	// the VERSION-FREE stable rule identity (stableContributorID), NOT the composed runtime id: the
	// composed id embeds the profile version token, which rotates every publish, so keying on it would
	// fork a stranded contributor per version (D6). Carried on every edge.
	RuleID string
	// Edge is "raised" (rising) or "resolved" (falling) — runtime.EdgeRaised / EdgeResolved. A raised
	// request raises/escalates; a resolved request removes this rule's contribution (and clears the
	// alarm if it was the last contributor).
	Edge         string
	OccurredTime time.Time
	// Value is the triggering scalar the detection carried, stamped on the alarm so a re-raise
	// annotates the real last value rather than a zero. It is nil when the rule shape has none — a
	// silence-driven absence/duration fire, or a metric-less raw-CEL leaf — and device-management then
	// leaves the alarm's last value NULL rather than writing a fabricated 0. A value-bearing rule
	// (threshold/repeating crossing sample, deltaRate/aggregate computed scalar) carries a real value.
	// A resolved edge carries no value (the condition ceased, not a reading).
	Value *float64
}

// AlarmSink raises/escalates or clears/de-escalates an alarm contributor for a device (ADR-041 /
// ADR-057), implemented by publishing an alarm request to device-management (slice 5c / 6d-pre-2c).
// Dispatch returns a non-nil error on any failure; the dispatcher retries every error (the event
// redelivers, the idempotent contributor upsert makes the re-run safe). A nil AlarmSink means alarm
// dispatch is DISABLED (the default until slice 6), and the dispatcher treats a raiseAlarm action
// (and its paired resolve) as recognized-but-inert.
type AlarmSink interface {
	Dispatch(ctx context.Context, req AlarmRequest) error
}

// ConnectorRequest is one outbound-connector dispatch (ADR-060): a rendered httpCall/publish action
// to hand to the outbound-connectors service over NATS. REACT does NOT execute it (no HTTP call, no
// broker publish, no secret resolution here) — it publishes a durable connector-dispatch request that
// the dedicated service consumes, so the heavy connector dep-tree and any credential handling stay out
// of this replay-correct binary (ADR-060 §4). It carries the CEL payload template ALREADY RENDERED to
// bytes (so the connectors service never imports cel — the determinism/supply-chain firewall) plus the
// deterministic idempotency Token the connectors service dedups on under at-least-once redelivery.
type ConnectorRequest struct {
	Tenant       string
	DeviceToken  string
	RuleID       string
	Edge         string
	OccurredTime time.Time
	// Token is the content-addressed idempotency key (idempotencyToken) — the SAME token family the
	// command sink dedups on, so a redelivery/replay collapses downstream rather than re-executing.
	Token string
	// Payload is the rendered template output (the request body / message payload). Empty when the
	// action declares no template — the connectors service then sends an empty body.
	Payload string
	// Action is the resolved connector action (httpCall or publish variant) the sink flattens onto the
	// wire. Passing the authored action keeps all wire-shaping in the sink (one place), and the
	// dispatcher free of transport detail.
	Action rules.Action
}

// ConnectorSink hands a rendered connector action to the outbound-connectors service (ADR-060),
// implemented by publishing a connector-dispatch request onto the per-tenant NATS subject the service
// consumes. Dispatch returns a non-nil error on any failure (a marshal or broker-write failure); the
// dispatcher retries every error (the event redelivers, the idempotency Token makes the re-run safe),
// so the sink need not classify. A nil ConnectorSink DISABLES connector dispatch: an httpCall/publish
// action is then recognized-but-inert (RecordNotEnabled), exactly like a nil command/alarm sink.
type ConnectorSink interface {
	Dispatch(ctx context.Context, req ConnectorRequest) error
}

// Metrics is the REACT observability sink (bounded cardinality — no per-tenant labels, the ADR-023
// G.3 lesson). action is a fixed, small enum ("sendCommand"/"raiseAlarm"/"clearAlarm"/"httpCall"/
// "publish" — "clearAlarm" is the structural falling-edge clear, ADR-057), never a tenant/rule value.
type Metrics interface {
	// RecordDispatched: one action successfully handed to its sink (includes idempotent replays,
	// which command-delivery collapses — so on a redelivery this counts the accepted attempt).
	RecordDispatched(action string)
	// RecordOrphan: one derived event whose rule was gone from the projection (nothing dispatched).
	RecordOrphan()
	// RecordNotEnabled: one action recognized but whose sink is disabled (nil) — raiseAlarm/clearAlarm
	// before slice 6, or send-command on a deploy without command-delivery configured.
	RecordNotEnabled(action string)
}

// Dispatcher turns a derived event into its authored actions. It holds no per-detection state —
// idempotency lives entirely in the deterministic token the command sink dedups on (and the
// upsert-keyed alarm) — so it is safe to run as a queue group once the DETECT singleton constraint
// is lifted (slice 6). Each action kind has its own sink; a nil sink means that kind is DISABLED and
// its actions are recognized-but-inert (RecordNotEnabled), so send-command and raise-alarm are
// independently gateable.
type Dispatcher struct {
	resolver   RuleResolver
	commands   CommandSink
	alarms     AlarmSink
	connectors ConnectorSink
	metrics    Metrics

	// templates caches a compiled payload-template program per distinct template source (ADR-060),
	// mirroring the guard cache below: the dispatcher resolves rules.Rule (not a compiled form) fresh
	// per event, so without a cache it would recompile a connector action's CEL template on every
	// dispatch. Same monotonic, publish-gated growth bound as guards (a template string can only enter
	// via a rule that cleared the publish cost gate — never attacker-controlled at dispatch), so no
	// eviction. sync.Map for lock-free reads on the concurrent dispatch path.
	templates sync.Map // template source string → *rules.CompiledTemplate

	// guards caches a compiled guard program per distinct guard source (rules.Action.Guard), keyed by
	// the source string. The dispatcher resolves rules.Rule (not a compiled form) fresh per event, so
	// without a cache it would recompile every guard on every dispatch — instead a guard compiles once
	// and is reused. It grows MONOTONICALLY: every distinct guard string ever dispatched is retained for
	// the process lifetime (there is no eviction), so republish churn and since-deleted rules leave their
	// programs resident. That growth is slow and publish-gated (a guard string is not attacker-controlled
	// at dispatch — it can only enter via a rule that cleared the publish cost gate), so it is bounded in
	// practice by the distinct guards a tenant set authors, not the event rate. sync.Map for lock-free
	// reads on the concurrent (queue-group) dispatch path.
	guards sync.Map // guard source string → *rules.CompiledGuard
}

// NewDispatcher builds a REACT dispatcher over a rule resolver and its action sinks. Any sink may be
// nil to disable that action kind: a nil commands sink disables send-command, a nil alarms sink
// disables raise-alarm, a nil connectors sink disables httpCall/publish (ADR-060). In production since
// 6d the alarms sink is always wired (the sole alarm path); a nil alarms sink is a test-only
// configuration. A dispatcher with all sinks nil dispatches nothing (every action inert).
func NewDispatcher(resolver RuleResolver, commands CommandSink, alarms AlarmSink, connectors ConnectorSink, metrics Metrics) *Dispatcher {
	return &Dispatcher{resolver: resolver, commands: commands, alarms: alarms, connectors: connectors, metrics: metrics}
}

// Dispatch handles one derived event, returning whether the consumer may ack it (Done) or must let
// it redeliver (Retry). It resolves the rule that fired and dispatches each action in order. A
// failure at ANY action returns Retry immediately, leaving the event unacked; the redelivery re-runs
// the already-dispatched prefix idempotently (the command token collapses re-sends; the alarm
// contributor upsert collapses re-raises/re-clears) and reaches the failed action again. An orphan
// rule never wedges the loop.
//
// EDGE ROUTING (ADR-057). The detection's edge selects which side effects fire:
//   - a RAISED (rising) edge dispatches every action — raiseAlarm raises/escalates, sendCommand sends.
//   - a RESOLVED (falling) edge dispatches ONLY the paired clear for each raiseAlarm action (the
//     clearAlarm is structural, not a materialized action: a rule that declares raiseAlarm implicitly
//     clears the SAME alarm key on its falling edge). sendCommand has NO falling-edge twin — a command
//     is a one-shot side effect, so a Resolved must not re-send it (which would double-fire the LIVE
//     send-command path, slice 5b). This is the load-bearing correctness point of enabling Resolved on
//     the wire.
func (d *Dispatcher) Dispatch(ctx context.Context, ev runtime.DerivedEvent) Outcome {
	rule, found, err := d.resolver.Resolve(ctx, ev.RuleID)
	if err != nil {
		// Transient store failure — retry. Dropping the actions here would silently lose every
		// side effect for a detection whenever the rule store hiccups.
		return Retry
	}
	if !found {
		// The rule was removed after the detection fired and before REACT resolved it. There is
		// nothing to dispatch and a retry cannot bring it back — drop (count) and ack.
		d.metrics.RecordOrphan()
		return Done
	}
	for _, a := range rule.Actions {
		if out := d.dispatchAction(ctx, ev, rule, a); out == Retry {
			return Retry
		}
	}
	return Done
}

// dispatchAction dispatches one action for the event's edge (see Dispatch). A sink failure is a Retry
// (the whole event redelivers); a success, a disabled action kind (nil sink → inert, counted), an
// action with no effect on this edge (sendCommand on a Resolved), and an unknown action (unreachable
// for a gate-validated rule) are all Done so the loop moves on. The rule is passed so a raiseAlarm
// action can read the rule-level severity + watched metric it raises with.
func (d *Dispatcher) dispatchAction(ctx context.Context, ev runtime.DerivedEvent, rule rules.Rule, a rules.Action) Outcome {
	resolved := ev.Edge == runtime.EdgeResolved
	switch a.Type {
	case rules.ActionSendCommand:
		if resolved {
			// A command has no falling-edge twin: the Resolved reports the condition ceased, which is
			// not a fresh trigger to re-send. Skip (no metric — it is a routine non-effect, not a drop).
			return Done
		}
		if d.commands == nil {
			d.metrics.RecordNotEnabled("sendCommand")
			return Done
		}
		if !d.guardAllows(ev, a) {
			// The action's branch guard evaluated false for this detection — a routine, deterministic
			// non-effect (like sendCommand on a Resolved). Skip and ack; a redelivery re-evaluates the
			// same guard to the same bit, so there is nothing a retry would change.
			return Done
		}
		req := CommandRequest{
			Tenant:      ev.Tenant,
			Token:       idempotencyToken(ev, a),
			DeviceToken: ev.Series,
			Command:     a.SendCommand.Command,
			Payload:     a.SendCommand.Payload,
		}
		if err := d.commands.Send(ctx, req); err != nil {
			return Retry
		}
		d.metrics.RecordDispatched("sendCommand")
		return Done
	case rules.ActionRaiseAlarm:
		// A raiseAlarm action is dispatched on BOTH edges: a Raised raises/escalates this rule's
		// contribution, a Resolved (the structural clearAlarm pairing) removes it. The alarm object
		// integrates the per-rule edges into the (device, alarmKey) lifecycle (slice 6d-pre-2c).
		action := "raiseAlarm"
		if resolved {
			action = "clearAlarm"
		}
		if d.alarms == nil {
			// No alarm sink (a test-only configuration; production always wires it since 6d).
			// Count it so its inertness is observable rather than silent.
			d.metrics.RecordNotEnabled(action)
			return Done
		}
		if !resolved && !d.guardAllows(ev, a) {
			// Guarded out on the RISING edge → do not raise this contribution. The guard is consulted
			// ONLY here, never on the falling edge: a raiseAlarm's Resolved is the STRUCTURAL clear of
			// the same alarm key, and gating that would strand an alarm active forever if the guard's
			// inputs changed between the raise and the resolve. Because we did not raise, no contributor
			// exists, so the always-dispatched falling-edge clear is a harmless idempotent no-op — the
			// contributor upsert removes a contribution that was never added.
			return Done
		}
		// The VERSION-FREE stable rule identity keys BOTH the default alarm key AND the contributor: the
		// composed rule id embeds the profile VERSION token, which rotates on EVERY publish, so keying on
		// it would fork a fresh alarm-object CONTRIBUTOR per version and strand the old one ACTIVE forever
		// (the D6 blocker). The stable identity is the correct one-logical-rule-across-versions key — the
		// new version's edges update the SAME contributor and clear it, exactly as StableRuleKey does for
		// the alarm key (ADR-041 dec 3). This is safe because a device's EVENT-driven kinds only fire for
		// its active version (the fan-out scopes by ProfileVersionToken), so old and new never race a
		// genuine event edge. The one exception is FRONTIER-triggered firings (Duration/Session timers,
		// Aggregate pane-closes) of a superseded-but-retained version, which ride the shared watermark
		// even while starved of events — those are dropped upstream at publish by the version gate
		// (processor.dropSupersededDetections / VersionSuperseded) so they can't contribute a false edge
		// (e.g. a stale unsatisfied pane-close resolving the active version's raise at the same timestamp).
		contributorID := stableContributorID(ev.RuleID)
		req := AlarmRequest{
			Tenant:       ev.Tenant,
			DeviceToken:  ev.Series,
			AlarmKey:     defaultAlarmKey(a.RaiseAlarm.AlarmKey, ev.RuleID),
			MetricKey:    ruleMetric(rule),
			Severity:     string(rule.Severity),
			RuleID:       contributorID,
			Edge:         edgeOrRaised(ev.Edge),
			OccurredTime: ev.OccurredTime,
			Value:        ev.Value,
		}
		if err := d.alarms.Dispatch(ctx, req); err != nil {
			return Retry
		}
		d.metrics.RecordDispatched(action)
		return Done
	case rules.ActionHTTPCall, rules.ActionPublish:
		// A connector action (ADR-060) is a one-shot outbound side effect, exactly like sendCommand: it
		// fires ONLY on the rising edge. A Resolved reports the condition ceased — not a fresh trigger to
		// re-POST/re-publish — so it has no falling-edge twin; skip it (no metric, a routine non-effect).
		if resolved {
			return Done
		}
		// A malformed/forged resolved rule whose declared type has no matching payload variant would
		// otherwise nil-panic when idempotencyToken dereferences the variant below — crashing the shared
		// DETECT+REACT process into a redelivery crash-loop (there is no recover on the consumer loop).
		// The publish gate's populatedVariants check is NOT re-run when a rule is decoded from the durable
		// projection, so a hand-edited row can reach here; drop it fail-closed (log + ack) exactly as the
		// default case drops an unknown action, so a forged definition never wedges the loop.
		if (a.Type == rules.ActionHTTPCall && a.HTTPCall == nil) || (a.Type == rules.ActionPublish && a.Publish == nil) {
			log.Error().Str("rule", ev.RuleID).Str("action", string(a.Type)).
				Msg("REACT: dropping a connector action whose payload variant is missing (malformed/forged rule).")
			return Done
		}
		kind := string(a.Type) // "httpCall" / "publish" — a fixed metric enum, never a tenant/rule value
		if d.connectors == nil {
			d.metrics.RecordNotEnabled(kind)
			return Done
		}
		if !d.guardAllows(ev, a) {
			// Branch guard false for this detection — a routine, deterministic non-effect. Skip and ack;
			// a redelivery re-evaluates the same guard to the same bit.
			return Done
		}
		payload, ok := d.renderPayload(ev, a)
		if !ok {
			// The payload template failed to build or errored at evaluation (a bug — it passed the
			// publish cost gate). Fail CLOSED: skip rather than dispatch an empty/partial body or retry
			// into a wedge (a render error is deterministic for this event, so a retry loops to poison).
			// renderPayload logs the defect.
			return Done
		}
		req := ConnectorRequest{
			Tenant:       ev.Tenant,
			DeviceToken:  ev.Series,
			RuleID:       ev.RuleID,
			Edge:         edgeOrRaised(ev.Edge),
			OccurredTime: ev.OccurredTime,
			Token:        idempotencyToken(ev, a),
			Payload:      payload,
			Action:       a,
		}
		if err := d.connectors.Dispatch(ctx, req); err != nil {
			return Retry
		}
		d.metrics.RecordDispatched(kind)
		return Done
	default:
		// The publish gate (rules.Compile) rejects unknown action types, so this is unreachable for
		// a gate-validated rule; a forged/hand-edited definition's unknown action is skipped (not a
		// wedge). No metric — it cannot happen through the supported authoring path.
		return Done
	}
}

// guardAllows reports whether an action's branch guard permits it to dispatch for this detection
// (ADR-053 slice 9c). An action with no guard always dispatches (the pre-9c behaviour). A guard is a
// pure, stateless CEL boolean over the derived event's scalars (rules guard env), so a redelivery
// re-evaluates it to the same bit — safe on REACT's at-least-once path. It fails CLOSED: a guard that
// cannot be built (a bug — it passed the publish gate) or errors at evaluation (e.g. the runtime cost
// limit tripped) is treated as "do not dispatch" rather than dispatched un-gated or retried into a
// wedge, and is logged so the defect is visible. Called only on the rising edge (see dispatchAction),
// so it never gates a structural alarm clear.
func (d *Dispatcher) guardAllows(ev runtime.DerivedEvent, a rules.Action) bool {
	if a.Guard == "" {
		return true
	}
	g, err := d.guardProgram(a.Guard)
	if err != nil {
		log.Error().Err(err).Str("rule", ev.RuleID).Msg("REACT: a published action guard failed to build; skipping the action (fail closed).")
		return false
	}
	ok, err := g.Eval(rules.GuardInput{Value: ev.Value, Series: ev.Series})
	if err != nil {
		log.Error().Err(err).Str("rule", ev.RuleID).Msg("REACT: an action guard errored at evaluation; skipping the action (fail closed).")
		return false
	}
	return ok
}

// guardProgram returns the compiled guard for a source string, building and caching it on first
// use. The cache (d.guards) is bounded by the distinct guard strings across published rules, so it
// needs no eviction. A build error is a bug (the guard passed the publish gate); it is returned to
// guardAllows, which fails closed.
func (d *Dispatcher) guardProgram(source string) (*rules.CompiledGuard, error) {
	if v, ok := d.guards.Load(source); ok {
		return v.(*rules.CompiledGuard), nil
	}
	g, err := rules.BuildGuardProgram(source)
	if err != nil {
		return nil, err
	}
	// LoadOrStore so a concurrent build of the same source resolves to one shared program.
	actual, _ := d.guards.LoadOrStore(source, g)
	return actual.(*rules.CompiledGuard), nil
}

// renderPayload renders a connector action's CEL payload template against this detection (ADR-060),
// returning the rendered body and ok=true. An action with NO template renders "" (ok=true) — an empty
// body the connectors service sends as-is. It fails CLOSED (ok=false) on a build or evaluation error:
// the template passed the publish cost gate, so a failure here is a bug (or a forged/hand-edited
// non-string template), and the caller skips the action rather than send a partial body. Like a guard,
// a template is a pure, stateless function of the derived event's scalars, so a redelivery renders the
// same bytes — safe on REACT's at-least-once path.
func (d *Dispatcher) renderPayload(ev runtime.DerivedEvent, a rules.Action) (string, bool) {
	src := actionPayloadTemplate(a)
	if src == "" {
		return "", true
	}
	prog, err := d.templateProgram(src)
	if err != nil {
		log.Error().Err(err).Str("rule", ev.RuleID).Msg("REACT: a published connector payload template failed to build; skipping the action (fail closed).")
		return "", false
	}
	out, err := prog.Eval(rules.GuardInput{Value: ev.Value, Series: ev.Series})
	if err != nil {
		log.Error().Err(err).Str("rule", ev.RuleID).Msg("REACT: a connector payload template errored at evaluation; skipping the action (fail closed).")
		return "", false
	}
	return out, true
}

// templateProgram returns the compiled payload template for a source string, building and caching it
// on first use (d.templates), mirroring guardProgram. The cache is bounded by the distinct template
// strings across published rules, so it needs no eviction. A build error is a bug (the template passed
// the publish gate); it is returned to renderPayload, which fails closed.
func (d *Dispatcher) templateProgram(source string) (*rules.CompiledTemplate, error) {
	if v, ok := d.templates.Load(source); ok {
		return v.(*rules.CompiledTemplate), nil
	}
	t, err := rules.BuildTemplateProgram(source)
	if err != nil {
		return nil, err
	}
	// LoadOrStore so a concurrent build of the same source resolves to one shared program.
	actual, _ := d.templates.LoadOrStore(source, t)
	return actual.(*rules.CompiledTemplate), nil
}

// actionPayloadTemplate returns the CEL payload-template source of a connector action (empty for a
// non-connector action, or a connector action that declares no body). It reads the exported action
// fields directly (react imports rules), so it stays in lockstep with the schema without a rules export.
func actionPayloadTemplate(a rules.Action) string {
	switch a.Type {
	case rules.ActionHTTPCall:
		if a.HTTPCall != nil {
			return a.HTTPCall.BodyTemplate
		}
	case rules.ActionPublish:
		if a.Publish != nil {
			return a.Publish.PayloadTemplate
		}
	}
	return ""
}

// edgeOrRaised normalizes a wire edge to an explicit token: an empty (legacy/pre-edge) value decodes
// as the EdgeRaised default, matching the DerivedEvent.Edge contract, so the alarm request always
// carries a definite "raised"/"resolved" rather than propagating an ambiguous empty downstream.
func edgeOrRaised(edge string) string {
	if edge == runtime.EdgeResolved {
		return runtime.EdgeResolved
	}
	return runtime.EdgeRaised
}

// stableContributorID returns the version-free stable identity the alarm object reference-counts a
// rule by (ADR-057 / D6): "{profileToken}/{ruleToken}", which does NOT rotate on a profile republish,
// so one logical rule maps to ONE contributor across versions rather than forking (and stranding) a
// fresh one per version. It falls back to the raw composed id only if the id does not parse into the
// minted shape (defensive — the publish path guarantees it does); an unparseable id is at least
// self-consistent, keeping the raise and its resolve on the same contributor key.
func stableContributorID(ruleID string) string {
	if stable, ok := runtime.StableRuleKey(ruleID); ok {
		return stable
	}
	return ruleID
}

// defaultAlarmKey returns the authored alarm key, or the rule's version-free stable identity when
// none was authored. It falls back to the raw rule id only if the id does not parse into the minted
// shape (which the publish path guarantees, so the fallback is defensive) — an empty key would be
// dropped downstream as poison.
func defaultAlarmKey(authored, ruleID string) string {
	if authored != "" {
		return authored
	}
	if stable, ok := runtime.StableRuleKey(ruleID); ok {
		return stable
	}
	return ruleID
}

// ruleMetric is the best-effort metric a raise-alarm action stamps on the alarm for context: the
// VALUE metric the rule folds (deltaRate / non-count aggregate) when present, else the leaf's gate
// metric (threshold/duration/repeating). Value-first so a deltaRate/aggregate rule gated on a
// DIFFERENT metric annotates the alarm with the metric the detection is actually about, not the gate.
// Empty for a raw-CEL, count-aggregate, or metric-less shape — the alarm is still raised, just
// without a metric annotation.
func ruleMetric(rule rules.Rule) string {
	if rule.Metric != "" {
		return rule.Metric
	}
	return rule.When.Metric
}

// idempotencyToken derives the stable, deterministic command token for one detection + action. It
// is a pure function of the detection's dedup identity (RuleID, Series, Kind, OccurredTime) plus the
// action's CONTENT — NOT its list index. Content-addressing (rather than index-addressing) is what
// makes it correct when the rule's action chain is resolved fresh per attempt (react_rule_resolver):
// if an author reorders a rule's actions between a first attempt and a retry/replay, an index-keyed
// token would re-send the action now sitting at the old index under the old action's token —
// silently swallowing one action (its command-delivery token already names a different command) and
// duplicating another. Keying on content means the SAME authored action always maps to the SAME
// token regardless of position, so a reorder is a no-op and each distinct action enqueues exactly
// once. Distinctness is guaranteed by the authoring gate, which forbids two identical actions in one
// rule (rules.validateReact), so no two actions of one detection can collide to one token. The event
// time is UnixNano for a stable encoding; the result is a hex SHA-256, grammar-safe by construction
// (ADR-042: leading alphanumeric, <= 128 chars).
func idempotencyToken(ev runtime.DerivedEvent, a rules.Action) string {
	raw := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s",
		ev.RuleID, ev.Series, ev.Kind, ev.OccurredTime.UTC().UnixNano(), actionContentKey(a))
	return hashToken(raw)
}

// actionContentKey is a stable textual identity of an action's dispatch content — the same identity
// the authoring gate dedups on (rules.ActionDedupKey), so two actions share a key iff the gate would
// reject them as duplicates. It anchors the idempotency token to WHAT is dispatched, not WHERE the
// action sits in the chain.
func actionContentKey(a rules.Action) string {
	// The guard is part of the content identity, mirroring the authoring gate's ActionDedupKey: two
	// actions that differ only by guard are distinct dispatches (raise-if-hot vs raise-if-cold), so
	// their idempotency tokens must differ or one guarded variant would collapse onto the other's token
	// and swallow a dispatch.
	//
	// The guard segment is appended ONLY when non-empty, so an UNGUARDED action's token is byte-for-byte
	// what it was before 9c: the idempotency token is durable (command-delivery dedups on it), and a
	// blanket suffix would re-key every in-flight/replayed unguarded sendCommand at the deploy boundary,
	// minting a fresh token for an already-enqueued command and double-sending it once. This is
	// collision-safe: a validated JSON-object payload cannot contain a raw NUL, so an unguarded key can
	// never equal a guarded key (which has an extra \x00-delimited guard suffix the payload can't forge).
	guardSeg := ""
	if a.Guard != "" {
		guardSeg = "\x00" + a.Guard
	}
	switch a.Type {
	case rules.ActionSendCommand:
		return "sendCommand\x00" + a.SendCommand.Command + "\x00" + a.SendCommand.Payload + guardSeg
	case rules.ActionRaiseAlarm:
		return "raiseAlarm\x00" + a.RaiseAlarm.AlarmKey + guardSeg
	case rules.ActionHTTPCall:
		// Defensive nil-guard: a malformed action with no variant is dropped upstream (dispatchAction)
		// before its token is minted, so this is belt-and-braces — but keeping the helper TOTAL means it
		// can never nil-panic regardless of caller. A nil variant degenerates to the type string; that
		// token is never used (the action was dropped), so the collision is harmless.
		if a.HTTPCall == nil {
			return string(a.Type)
		}
		// Mirror rules.ActionDedupKey's httpCall identity EXACTLY, reusing the SAME exported
		// normalization (rules.MethodOrPost / rules.HeaderKey) so this durable token can never drift from
		// the gate's duplicate identity — two actions that render the identical request (empty vs "POST"
		// method, header-name case) map to one token, so a redelivery/replay after a semantics-preserving
		// republish dedups downstream rather than double-executing. httpCall/publish never dispatched
		// before C2b, so there is no pre-existing token format to preserve (unlike the frozen
		// sendCommand/raiseAlarm segments above).
		h := a.HTTPCall
		return "httpCall\x00" + h.URL + "\x00" + rules.MethodOrPost(h.Method) + "\x00" + h.BodyTemplate + "\x00" + h.SecretRef + "\x00" + rules.HeaderKey(h.Headers) + guardSeg
	case rules.ActionPublish:
		if a.Publish == nil {
			return string(a.Type)
		}
		p := a.Publish
		return "publish\x00" + p.ConnectorRef + "\x00" + p.PayloadTemplate + guardSeg
	default:
		return string(a.Type)
	}
}
