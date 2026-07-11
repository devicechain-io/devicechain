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
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
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

// AlarmRequest is one raise-alarm dispatch (ADR-041 / slice 5c): raise or escalate an alarm for a
// device. Unlike a command it carries no idempotency token — device-management's raiseOrEscalateAlarm
// is an upsert keyed on (device, alarmKey) with occurred-time cross-cycle guards, so an at-least-once
// redelivery is idempotent without one.
type AlarmRequest struct {
	Tenant       string
	DeviceToken  string
	AlarmKey     string
	MetricKey    string
	Severity     string
	OccurredTime time.Time
}

// AlarmSink raises/escalates an alarm for a device (ADR-041), implemented by publishing a raise-alarm
// request to device-management (slice 5c). Raise returns a non-nil error on any failure; the
// dispatcher retries every error (the event redelivers, the upsert makes the re-raise safe). A nil
// AlarmSink means raise-alarm dispatch is DISABLED (the default until slice 6), and the dispatcher
// treats a raiseAlarm action as recognized-but-inert.
type AlarmSink interface {
	Raise(ctx context.Context, req AlarmRequest) error
}

// Metrics is the REACT observability sink (bounded cardinality — no per-tenant labels, the ADR-023
// G.3 lesson). action is a fixed, small enum ("sendCommand"/"raiseAlarm"), never a tenant/rule value.
type Metrics interface {
	// RecordDispatched: one action successfully handed to its sink (includes idempotent replays,
	// which command-delivery collapses — so on a redelivery this counts the accepted attempt).
	RecordDispatched(action string)
	// RecordOrphan: one derived event whose rule was gone from the projection (nothing dispatched).
	RecordOrphan()
	// RecordNotEnabled: one action recognized but whose sink is disabled (nil) — raiseAlarm before
	// slice 6, or send-command on a deploy without command-delivery configured.
	RecordNotEnabled(action string)
}

// Dispatcher turns a derived event into its authored actions. It holds no per-detection state —
// idempotency lives entirely in the deterministic token the command sink dedups on (and the
// upsert-keyed alarm) — so it is safe to run as a queue group once the DETECT singleton constraint
// is lifted (slice 6). Each action kind has its own sink; a nil sink means that kind is DISABLED and
// its actions are recognized-but-inert (RecordNotEnabled), so send-command and raise-alarm are
// independently gateable.
type Dispatcher struct {
	resolver RuleResolver
	commands CommandSink
	alarms   AlarmSink
	metrics  Metrics
}

// NewDispatcher builds a REACT dispatcher over a rule resolver and its action sinks. Either sink may
// be nil: a nil commands sink disables send-command, a nil alarms sink disables raise-alarm (the
// default until slice 6). A dispatcher with both nil dispatches nothing (every action inert).
func NewDispatcher(resolver RuleResolver, commands CommandSink, alarms AlarmSink, metrics Metrics) *Dispatcher {
	return &Dispatcher{resolver: resolver, commands: commands, alarms: alarms, metrics: metrics}
}

// Dispatch handles one derived event, returning whether the consumer may ack it (Done) or must let
// it redeliver (Retry). It resolves the rule that fired and dispatches each action in order. A
// failure at ANY action returns Retry immediately, leaving the event unacked; the redelivery re-runs
// the already-dispatched prefix idempotently (the command token collapses re-sends; the alarm upsert
// collapses re-raises) and reaches the failed action again. An orphan rule never wedges the loop.
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

// dispatchAction dispatches one action. A sink failure is a Retry (the whole event redelivers); a
// success, a disabled action kind (nil sink → inert, counted), and an unknown action (unreachable
// for a gate-validated rule) are all Done so the loop moves on. The rule is passed so a raiseAlarm
// action can read the rule-level severity + watched metric it raises with.
func (d *Dispatcher) dispatchAction(ctx context.Context, ev runtime.DerivedEvent, rule rules.Rule, a rules.Action) Outcome {
	switch a.Type {
	case rules.ActionSendCommand:
		if d.commands == nil {
			d.metrics.RecordNotEnabled("sendCommand")
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
		if d.alarms == nil {
			// Raise-alarm dispatch is disabled (the default until slice 6 retires the measurement
			// evaluator). Count it so its inertness is observable rather than silent.
			d.metrics.RecordNotEnabled("raiseAlarm")
			return Done
		}
		req := AlarmRequest{
			Tenant:      ev.Tenant,
			DeviceToken: ev.Series,
			// Default an empty authored key to the rule's VERSION-FREE stable identity so a rule's
			// repeated firings escalate ONE alarm in place (ADR-041 dec 3) even across routine profile
			// re-publishes — the composed rule id embeds the profile VERSION token, which rotates every
			// publish, so keying on it would fork a fresh alarm per version and orphan the prior one.
			AlarmKey:     defaultAlarmKey(a.RaiseAlarm.AlarmKey, ev.RuleID),
			MetricKey:    ruleMetric(rule),
			Severity:     string(rule.Severity),
			OccurredTime: ev.OccurredTime,
		}
		if err := d.alarms.Raise(ctx, req); err != nil {
			return Retry
		}
		d.metrics.RecordDispatched("raiseAlarm")
		return Done
	default:
		// The publish gate (rules.Compile) rejects unknown action types, so this is unreachable for
		// a gate-validated rule; a forged/hand-edited definition's unknown action is skipped (not a
		// wedge). No metric — it cannot happen through the supported authoring path.
		return Done
	}
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
// the authoring gate dedups on (rules.actionDedupKey), so two actions share a key iff the gate would
// reject them as duplicates. It anchors the idempotency token to WHAT is dispatched, not WHERE the
// action sits in the chain.
func actionContentKey(a rules.Action) string {
	switch a.Type {
	case rules.ActionSendCommand:
		return "sendCommand\x00" + a.SendCommand.Command + "\x00" + a.SendCommand.Payload
	case rules.ActionRaiseAlarm:
		return "raiseAlarm\x00" + a.RaiseAlarm.AlarmKey
	default:
		return string(a.Type)
	}
}
