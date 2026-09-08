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
// idempotency is carried by a DETERMINISTIC token the downstream sink dedups on (command-delivery
// is idempotent on the command token, ADR-051 slice 5b-1) — so a redelivered event, a DETECT replay
// that re-publishes the same detection, and a retry after a transient failure all collapse
// downstream rather than double-acting. The token is derived from the detection's dedup identity
// plus the action's CONTENT, deliberately NOT its index in the action list: the rule is resolved
// fresh per attempt, so an author reordering the chain between attempts would, under an index-keyed
// token, re-send whichever action now sits at the old index under the old action's token. See
// idempotencyToken for the full argument. Dispatch failures are retried by DEFAULT — the event is
// not acked, and a genuinely un-dispatchable event is bounded by the consumer's redelivery cap
// (poison) — with ONE narrow exception: a sink that returns a *PermanentRejection is stating that
// the downstream service DECIDED the request is invalid, and that decision cannot change under
// redelivery. Those are dropped instead of retried. The asymmetry is deliberate and is not an
// invitation to interpret errors generally: a sink may only raise a permanent rejection from a
// typed verdict the downstream service actually returned, never from a string it recognized.
package react

import (
	"context"
	"errors"
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

// PermanentRejection is a sink's report that the downstream service DECIDED the request is
// invalid, in a form that cannot become valid by being sent again: the device does not exist, the
// command is not in the device's published vocabulary, the payload violates its schema.
//
// It exists because the dispatcher's retry-everything default has one genuinely wrong case. A
// permanently-invalid command was retried until the consumer's redelivery cap gave up, which spent
// the whole poison budget on a request that was answered correctly the first time and made a
// deleted device look exactly like a command-delivery outage.
//
// 🔴 IT MAY ONLY BE RAISED FROM A TYPED VERDICT THE DOWNSTREAM SERVICE RETURNED — command-delivery's
// createCommand rejection payload carries a stable code for exactly this purpose. Never from an
// error string, an HTTP status, or a substring match: a wrongly-permanent classification DROPS a
// real actuation with no retry and no record beyond a log line, while a wrongly-transient one costs
// a bounded number of retries. That asymmetry is why the default stays "retry" and why anything a
// sink cannot positively classify must NOT be wrapped in this.
type PermanentRejection struct {
	// Code is the downstream service's machine-readable classification (it is logged and
	// metered, never parsed).
	Code string
	// Reason is the human-readable explanation, relayed verbatim for the log.
	Reason string
}

func (e *PermanentRejection) Error() string {
	return fmt.Sprintf("permanently rejected (%s): %s", e.Code, e.Reason)
}

// CommandSink enqueues a command for a device (ADR-043), implemented over command-delivery. Send
// returns nil on success (a fresh enqueue OR an idempotent replay of an already-enqueued token,
// which command-delivery collapses) and a non-nil error on failure. The dispatcher retries every
// error EXCEPT a *PermanentRejection, which it drops (see PermanentRejection for why the default
// is retry and what a sink must have in hand before it may say otherwise). A repeat with the same
// Token never enqueues a second command (slice 5b-1), which is what makes at-least-once retry safe.
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
// dispatch is DISABLED, and the dispatcher treats a raiseAlarm action (and its paired resolve) as
// recognized-but-inert. That is a TEST-ONLY configuration now: since the 6d cutover the sink is
// always wired in production (see NewDispatcher), because it needs only a NATS writer and there is
// no peer evaluator left to double-raise against.
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

// ConnectorRateGate is the per-tenant OUTBOUND egress cost-gate charged at the SOURCE (ADR-060
// SD-3). Before REACT publishes a connector-dispatch (httpCall/publish), the dispatcher charges the
// tenant's outbound budget; a false result means over-quota, and the action is DROPPED (shed at the
// source), NOT retried — so a runaway rule cannot flood the connector-dispatch stream and the
// downstream outbound-connectors service. It is an IMMEDIATE, non-blocking test (a token-bucket
// Allow), never a wait: REACT's at-least-once derived-event consumer must not block on egress. A shed
// drops ONLY this connector action (ack-progress) — other actions of the same detection still fire,
// and the event is acked, so a shed connector action does NOT wedge or redeliver the event. A nil
// gate DISABLES source-charging (every connector dispatch is admitted); the outbound-connectors
// egress limiter (C3b.2) still meters as the defense-in-depth backstop. *core.TenantRateLimiter
// satisfies this (its Allow), kept as a narrow interface so this replay-correct package does not
// depend on core's limiter type.
//
// DETERMINISM (ADR-056 boundary): charging a wall-clock rate limiter here is safe because REACT holds
// NO replay-correct state — it is a separate, at-least-once, DECOUPLED consumer of the derived-event
// stream (unlike the DETECT single-writer loop). A shed is a dropped SIDE EFFECT, never a mutation of
// engine state, so it cannot diverge a replay. One consequence of at-least-once: if ANOTHER action of
// the same detection fails and the whole event redelivers, this connector action re-charges the gate
// on the re-run (a bounded over-count, capped by the consumer's redelivery cap) — an accepted cost of
// keeping the gate a pure per-attempt Allow rather than threading dispatch state.
//
// SCOPE: the gate (and its resolver cache) is PER-PROCESS. REACT deploys as the DETECT singleton
// today (one process), so the configured per-tenant ceiling is the effective one. If REACT is later
// scaled to a queue group (post-GA, ADR-052), N replicas would each hold their own bucket and the
// effective source ceiling becomes N× the configured value — at which point this gate would need a
// shared/among-replicas limiter (or a per-replica fraction) to keep matching the single sink-side
// egress limiter it is sized against. Harmless at the singleton deploy; called out so the caveat is
// not lost when scale-out lands.
type ConnectorRateGate interface {
	Allow(tenant string) bool
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
	// RecordNotEnabled: one action recognized but whose sink is disabled (nil) — in production that
	// means send-command on a deploy without command-delivery configured, or a connector action
	// (httpCall/publish) on a deploy without the connector writer. The alarm sink is always wired
	// since the 6d cutover, so raiseAlarm/clearAlarm reach this only in a test configuration.
	RecordNotEnabled(action string)
	// RecordConnectorShed: one connector dispatch ATTEMPT (httpCall/publish) shed at the source for
	// being over the tenant's outbound egress quota (ADR-060 SD-3). Per-attempt, not per permanently-
	// dropped action: a sibling-failure redelivery may shed this action on one attempt and admit it on
	// a later one. action is the same "httpCall"/"publish" enum — no per-tenant label (the ADR-023 G.3
	// cardinality lesson).
	RecordConnectorShed(action string)
	// RecordPermanentlyRejected: one action DROPPED because the downstream service returned a
	// typed rejection that a retry cannot change (a *PermanentRejection) — today, a
	// send-command for a device that no longer exists or a command outside the device's
	// published vocabulary. It counts a permanently-dropped action, unlike RecordConnectorShed
	// which counts an attempt that a later redelivery may still admit. A non-zero rate here is
	// an AUTHORING defect (a rule pointed at commands its devices cannot accept), not an
	// infrastructure one — which is exactly why it must be countable separately from the
	// retries it replaces. The label is the same fixed action enum, never a tenant or rule
	// value, and never the rejection code (that is logged, not labelled).
	RecordPermanentlyRejected(action string)
}

// Dispatcher turns a derived event into its authored actions. It holds no per-detection state —
// idempotency lives entirely in the deterministic token the command sink dedups on (and the
// upsert-keyed alarm) — so it would be safe to run as a queue group. It is not run that way today:
// the DETECT loop is still a single writer over one partition ("singleton"), fenced by a stale-
// checkpoint check at startup, and REACT rides the same deployment. That constraint has outlived
// the slice this comment used to blame for it, so do not read the singleton as imminent — read it
// as the current design, which REACT's idempotency does not depend on.
// Each action kind has its own sink; a nil sink means that kind is DISABLED and
// its actions are recognized-but-inert (RecordNotEnabled), so send-command and raise-alarm are
// independently gateable.
type Dispatcher struct {
	resolver   RuleResolver
	commands   CommandSink
	alarms     AlarmSink
	connectors ConnectorSink
	// connectorRate is the SOURCE-side per-tenant outbound egress cost-gate (ADR-060 SD-3); nil
	// disables source-charging (every connector dispatch admitted). See ConnectorRateGate.
	connectorRate ConnectorRateGate
	metrics       Metrics

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

	// alarmKeys caches a compiled alarm-key template per distinct source, on exactly the same terms as
	// templates/guards above (publish-gated, monotonic, no eviction, lock-free reads). It is a separate
	// cache rather than a shared one with templates because the two compile against DIFFERENT
	// environments — an alarm key sees `series` alone (rules/alarmkey.go) — so one source string could
	// legitimately mean two different programs, and sharing the map would hand an alarm key a program
	// built against the wider guard env.
	alarmKeys sync.Map // alarm-key template source string → *rules.CompiledAlarmKeyTemplate
}

// NewDispatcher builds a REACT dispatcher over a rule resolver and its action sinks. Any sink may be
// nil to disable that action kind: a nil commands sink disables send-command, a nil alarms sink
// disables raise-alarm, a nil connectors sink disables httpCall/publish (ADR-060). In production since
// 6d the alarms sink is always wired (the sole alarm-raise path); a nil alarms sink is a test-only
// configuration. A dispatcher with all sinks nil dispatches nothing (every action inert). connectorRate
// is the SOURCE-side outbound egress cost-gate (ADR-060 SD-3); a nil gate disables source-charging
// (every connector dispatch admitted, metered only by the downstream outbound-connectors egress limiter).
func NewDispatcher(resolver RuleResolver, commands CommandSink, alarms AlarmSink, connectors ConnectorSink, connectorRate ConnectorRateGate, metrics Metrics) *Dispatcher {
	return &Dispatcher{resolver: resolver, commands: commands, alarms: alarms, connectors: connectors, connectorRate: connectorRate, metrics: metrics}
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
		// Same defensive nil-guard as the raiseAlarm branch below, and for exactly the same reason: the
		// publish gate's populatedVariants check is NOT re-run when a rule is decoded from the durable
		// projection, so a hand-edited row declaring type sendCommand with no sendCommand payload
		// reaches the bare dereference in the CommandRequest literal and nil-panics the shared consumer
		// loop into a redelivery crash-loop. Guarding one variant and not its sibling leaves the class
		// open, which is what happened here.
		if a.SendCommand == nil {
			log.Error().Str("rule", ev.RuleID).
				Msg("REACT: dropping a sendCommand action whose payload variant is missing (malformed/forged rule).")
			return Done
		}
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
			var permanent *PermanentRejection
			if errors.As(err, &permanent) {
				// command-delivery DECIDED this command is invalid, and redelivering the same
				// event produces the same deterministic request and the same verdict. Drop it
				// (ack-progress) rather than spending the consumer's whole redelivery budget
				// re-asking a question that has been answered — which is what made a deleted
				// device indistinguishable from command-delivery being down. Logged at warn
				// because a rule firing at a device that cannot accept its command is an
				// AUTHORING defect the operator has to see; the metric makes it countable.
				log.Warn().Str("tenant", ev.Tenant).Str("device", ev.Series).
					Str("command", a.SendCommand.Command).Str("code", permanent.Code).
					Str("reason", permanent.Reason).
					Msg("REACT send-command permanently rejected; dropping (a retry cannot change the verdict).")
				d.metrics.RecordPermanentlyRejected("sendCommand")
				return Done
			}
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

		// Defensive nil-guard, mirroring the connector branch below and added for the same reason: the
		// publish gate's populatedVariants check is NOT re-run when a rule is decoded from the durable
		// projection, so a hand-edited row declaring type raiseAlarm with no raiseAlarm payload can
		// reach here and would nil-panic the shared consumer loop into a redelivery crash-loop (there is
		// no recover on it). Drop it fail-closed, exactly as the default case drops an unknown action.
		if a.RaiseAlarm == nil {
			log.Error().Str("rule", ev.RuleID).
				Msg("REACT: dropping a raiseAlarm action whose payload variant is missing (malformed/forged rule).")
			return Done
		}

		// Resolve the key BEFORE the sink call, on BOTH edges, from the same inputs. An alarm-key
		// template reads only the series (rules/alarmkey.go), which is identical on a rule's rising and
		// falling edge, so the raise and its structural clear are guaranteed to name the same alarm —
		// the property the whole restricted vocabulary exists to buy. A resolution failure is therefore
		// also identical on both edges: it means this rule never raised anything for this device, so
		// skipping the clear too strands nothing.
		alarmKey, ok := d.resolveAlarmKey(ev, a.RaiseAlarm)
		if !ok {
			return Done
		}
		contributorID := stableContributorID(ev.RuleID)
		req := AlarmRequest{
			Tenant:       ev.Tenant,
			DeviceToken:  ev.Series,
			AlarmKey:     alarmKey,
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
		// SOURCE-side egress cost-gate (ADR-060 SD-3): charge the tenant's outbound budget immediately
		// before the publish, so an over-quota tenant sheds at the source rather than flooding the
		// connector-dispatch stream and the downstream service. A shed DROPS only this connector action
		// (ack-progress) — it is NOT a Retry (a retry would loop the whole event and never drain under
		// sustained overload) and NOT a dispatch (do not count it as one). It is charged AFTER the guard
		// (a guarded-out action is a non-effect, not an emission) and AFTER renderPayload (so only an
		// action that would TRULY publish consumes a token — a deterministic render failure never burns
		// budget, keeping the only over-count the bounded sibling-redelivery one documented on
		// ConnectorRateGate), and only when a gate is configured (a nil gate leaves metering to the
		// downstream egress limiter). renderPayload is a cached compile + a cost-gated CEL eval, so
		// charging after it costs a bounded eval, not the network publish the shed actually avoids.
		if d.connectorRate != nil && !d.connectorRate.Allow(ev.Tenant) {
			d.metrics.RecordConnectorShed(kind)
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

// resolveAlarmKey returns the alarm key this raiseAlarm action files under, and ok=true. A literal
// AlarmKey (or an absent one, which defaults to the rule's stable identity) resolves without
// touching CEL — an already-published static key renders to ITSELF because nothing renders it, which
// is what makes this change invisible to every rule authored before it. An AlarmKeyTemplate is
// rendered against the detection's series and the RESULT is grammar-checked (CompiledAlarmKeyTemplate.Eval).
//
// It fails CLOSED (ok=false): a build error, an evaluation error, or a rendered key that is not a
// valid ADR-042 token skips the action rather than filing an alarm under a key the platform cannot
// store or trust. Fail-closed is the right direction here specifically because the failure is
// DETERMINISTIC for this rule — the template is a pure function of the series — so a Retry would
// loop the same event to the poison cap without ever succeeding, and a fallback to some other key
// would file the alarm somewhere the author never named and the falling edge would not find it. The
// same determinism is why skipping is safe on the falling edge: the rising edge failed identically,
// so there is no contribution left un-cleared. It mirrors renderPayload / guardAllows, which log the
// defect and skip for the same reasons.
func (d *Dispatcher) resolveAlarmKey(ev runtime.DerivedEvent, a *rules.RaiseAlarmAction) (string, bool) {
	if a.AlarmKeyTemplate == "" {
		return defaultAlarmKey(a.AlarmKey, ev.RuleID), true
	}
	prog, err := d.alarmKeyProgram(a.AlarmKeyTemplate)
	if err != nil {
		log.Error().Err(err).Str("rule", ev.RuleID).
			Msg("REACT: a published alarm-key template failed to build; skipping the action (fail closed).")
		return "", false
	}
	key, err := prog.Eval(ev.Series)
	if err != nil {
		// Includes the grammar rejection of a rendered key, which is the expected shape of this
		// failure: the device token is what varies, so a template can pass publish and still render an
		// over-long or malformed key for some devices. Logged with the device so an operator can see
		// WHICH devices the rule is inert on rather than only that it is.
		log.Error().Err(err).Str("rule", ev.RuleID).Str("device", ev.Series).
			Msg("REACT: an alarm-key template did not yield a usable key; skipping the action (fail closed).")
		return "", false
	}
	return key, true
}

// alarmKeyProgram returns the compiled alarm-key template for a source string, building and caching
// it on first use (d.alarmKeys), mirroring guardProgram / templateProgram. The cache is bounded by
// the distinct alarm-key templates across published rules, so it needs no eviction. A build error is
// a bug (the template passed the publish gate); it is returned to resolveAlarmKey, which fails closed.
func (d *Dispatcher) alarmKeyProgram(source string) (*rules.CompiledAlarmKeyTemplate, error) {
	if v, ok := d.alarmKeys.Load(source); ok {
		return v.(*rules.CompiledAlarmKeyTemplate), nil
	}
	t, err := rules.BuildAlarmKeyTemplateProgram(source)
	if err != nil {
		return nil, err
	}
	// LoadOrStore so a concurrent build of the same source resolves to one shared program.
	actual, _ := d.alarmKeys.LoadOrStore(source, t)
	return actual.(*rules.CompiledAlarmKeyTemplate), nil
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
	// 🔴 EVERY branch below nil-guards its variant, and that is a property of this function rather
	// than of its callers. Two of the four did not, while the httpCall branch carried a comment
	// claiming the helper "can never nil-panic regardless of caller" — an invariant asserted in a
	// switch that did not hold it. A nil variant degenerates to the type string; that token is never
	// used (dispatchAction drops the malformed action before minting one), so the collision between
	// two differently-malformed actions of one type is harmless. TestActionContentKeyIsTotal pins
	// this for every variant field declared on rules.Action, so a fifth action type cannot be added
	// with a bare dereference.
	switch a.Type {
	case rules.ActionSendCommand:
		if a.SendCommand == nil {
			return string(a.Type)
		}
		return "sendCommand\x00" + a.SendCommand.Command + "\x00" + a.SendCommand.Payload + guardSeg
	case rules.ActionRaiseAlarm:
		if a.RaiseAlarm == nil {
			return string(a.Type)
		}
		// A rendered key is discriminated by the SAME exported prefix rules.ActionDedupKey uses, so the
		// gate's duplicate identity and this content key induce the one equivalence relation (the
		// lockstep the dedup-key comment describes) rather than two mirrored copies that can drift. The
		// literal case is byte-for-byte what it was before alarm-key templates existed.
		key := a.RaiseAlarm.AlarmKey
		if a.RaiseAlarm.AlarmKeyTemplate != "" {
			key = rules.AlarmKeyIdentitySeparator + a.RaiseAlarm.AlarmKeyTemplate
		}
		return "raiseAlarm\x00" + key + guardSeg
	case rules.ActionHTTPCall:
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
