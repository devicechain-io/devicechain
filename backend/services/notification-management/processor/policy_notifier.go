// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
)

// retryBackoffBase is the base gap between per-channel delivery attempts; the nth
// retry waits n × base, so the dispatcher's own retry is bounded and short (the
// durable consumer, not this loop, is the reliability backstop for a longer outage).
const retryBackoffBase = 500 * time.Millisecond

// PolicyNotifier is the real dispatch Notifier (ADR-017 N.C) that replaces the
// first-slice LogNotifier: for each alarm transition it evaluates the tenant's
// notification policies, delivers matching alarms through the configured channels
// (SMTP, webhook), and maintains the per-alarm NotificationState the escalation
// scheduler (N.D) reads. It satisfies the Notifier contract (notifier.go) by owning a
// bounded per-channel retry loop rather than leaning on the processor's crude
// redelivery, and by returning an error to the processor ONLY when nothing was
// attempted (so a nak/redelivery can never double-send a channel that already
// succeeded).
type PolicyNotifier struct {
	api      *model.Api
	store    secrets.SecretStore
	adapters map[string]ChannelAdapter
	attempts int
	timeout  time.Duration
}

// NewPolicyNotifier builds the dispatcher over the persistence API and the secret
// store (ADR-059, from which each channel's delivery secret is resolved server-
// internal at delivery time), with attempts per-channel delivery tries and timeout
// bounding a single attempt.
func NewPolicyNotifier(api *model.Api, store secrets.SecretStore, attempts int, timeout time.Duration) *PolicyNotifier {
	if attempts < 1 {
		attempts = 1
	}
	return &PolicyNotifier{
		api:      api,
		store:    store,
		adapters: newAdapterRegistry(),
		attempts: attempts,
		timeout:  timeout,
	}
}

// Notify routes one alarm transition. RAISED/ESCALATED are delivered through the
// matching channels; the lifecycle transitions only update the per-alarm state so the
// escalation scheduler (N.D) can stop or re-tier — none of them page. A returned
// error means the transition was not applied and is safe to redeliver (no delivery
// was attempted); a delivery failure is handled inside dispatch and never surfaced,
// so the processor acks.
func (n *PolicyNotifier) Notify(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	switch event.EventType {
	case dmmodel.AlarmEventAcknowledged:
		// Idempotent tombstone upsert; a DB error is safe to retry (no side effects).
		return n.api.MarkAcknowledged(ctx, event.AlarmToken, event.AlarmKey, event.Severity, event.OccurredTime)
	case dmmodel.AlarmEventCleared:
		return n.api.MarkCleared(ctx, event.AlarmToken, event.AlarmKey, event.Severity, event.OccurredTime)
	case dmmodel.AlarmEventDeescalated:
		return n.api.TouchSeverity(ctx, event.AlarmToken, event.Severity)
	case dmmodel.AlarmEventRaised, dmmodel.AlarmEventEscalated:
		return n.dispatch(ctx, event)
	default:
		log.Warn().Str("eventType", event.EventType.String()).Str("alarm", event.AlarmToken).
			Msg("Ignoring unknown alarm event type")
		return nil
	}
}

// dispatch evaluates policies and delivers a RAISED/ESCALATED transition.
func (n *PolicyNotifier) dispatch(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	tenant, _ := core.TenantFromContext(ctx)

	policies, err := n.api.EnabledNotificationPolicies(ctx)
	if err != nil {
		// Nothing attempted yet: safe to nak for redelivery.
		return fmt.Errorf("loading notification policies: %w", err)
	}
	if len(policies) == 0 {
		return nil
	}

	// Load the current per-alarm state once as the throttle basis.
	states, err := n.api.NotificationStatesByAlarmToken(ctx, []string{event.AlarmToken})
	if err != nil {
		return fmt.Errorf("loading notification state: %w", err)
	}
	var state *model.NotificationState
	if len(states) > 0 {
		state = states[0]
	}

	deliveries := n.plan(event, policies, state)
	if len(deliveries) == 0 {
		return nil
	}

	rendered := renderNotification(event)
	delivered := 0
	for _, d := range deliveries {
		secret, err := n.resolveChannelSecret(ctx, d.channel.ID)
		if err != nil {
			// A store error (not "no secret") is transient infra: skip this channel, so
			// it counts as not-delivered. If EVERY delivery is skipped/fails the event is
			// naked for redelivery (delivered == 0 below); if another channel already
			// delivered, the event is acked and this channel's page is dropped — matching
			// the Notifier at-least-once contract (a partial failure is not redelivered,
			// to avoid double-sending the channels that succeeded).
			log.Error().Err(err).Str("tenant", tenant).Str("channel", d.channel.Token).
				Msg("Skipping channel: failed to resolve delivery secret")
			continue
		}
		d.secret = secret
		if n.deliverWithRetry(ctx, d, rendered) {
			delivered++
		}
	}

	// Nothing delivered though targets existed: return an error so the durable consumer
	// redelivers (its own retry is the reliability backstop for an outage longer than
	// this dispatcher's in-line retry). This is double-send-safe precisely because NO
	// channel succeeded — there is nothing to re-send twice. Once any channel succeeds
	// we must ack (return nil) so redelivery can't double-send the ones that worked.
	if delivered == 0 {
		return fmt.Errorf("all %d channel deliveries failed for alarm %q", len(deliveries), event.AlarmToken)
	}

	// At least one channel delivered: record the notification so the throttle/escalation
	// state reflects it. A failure here is logged, not naked — the sends already
	// happened, and redelivery would double-send them.
	if err := n.api.RecordNotification(ctx, event.AlarmToken, event.AlarmKey, event.Severity, event.OccurredTime); err != nil {
		log.Error().Err(err).Str("tenant", tenant).Str("alarm", event.AlarmToken).
			Msg("Delivered notification but failed to record notification state")
	}
	return nil
}

// delivery is one deduplicated (channel, recipients) target the dispatcher will send
// the rendered notification to. secret is the channel's delivery secret, resolved
// from the store server-internal just before delivery (empty when unconfigured); it
// is not persisted on the channel model.
type delivery struct {
	channel    *model.NotificationChannel
	recipients []string
	secret     string
}

// plan resolves the enabled policies to the deduplicated set of channel deliveries for
// this event. It matches each rule by severity, drops rules whose channel is missing,
// disabled, or has no adapter, and collapses duplicate (channel, recipients) targets
// so two policies routing to the same place send once.
func (n *PolicyNotifier) plan(event *dmmodel.AlarmStateChangeEvent,
	policies []*model.NotificationPolicy, state *model.NotificationState) []delivery {
	seen := make(map[string]bool)
	out := make([]delivery, 0)
	for _, p := range policies {
		// Device-type scoping is deferred within N.C: evaluating it needs a
		// cross-service originator→device-type resolution (ADR-044), and the alarm
		// event still carries the originator as an id, not a token. Until that lands,
		// a device-type-scoped policy is skipped (not applied tenant-wide, which would
		// over-notify) so only tenant-wide policies deliver.
		if p.DeviceTypeToken.Valid && p.DeviceTypeToken.String != "" {
			log.Debug().Str("policy", p.Token).Str("deviceType", p.DeviceTypeToken.String).
				Msg("Skipping device-type-scoped policy (scoping deferred to a later slice)")
			continue
		}
		if n.throttled(event, p, state) {
			log.Debug().Str("policy", p.Token).Str("alarm", event.AlarmToken).
				Msg("Skipping policy: within throttle window")
			continue
		}
		out = n.appendRuleDeliveries(p.Token, event.Severity, p.Rules, seen, out)
	}
	return out
}

// throttled reports whether the policy's minimum-gap throttle suppresses this
// notification. An ESCALATED transition always passes (a worsening alarm is a new
// fact worth paging); otherwise a notification within ThrottleSeconds of the last one
// for this alarm is suppressed. In event-driven N.C this is effectively inert for a
// first RAISED (a re-raise is a new alarm token with no prior state); it is the
// substrate the scheduled re-notification in N.D enforces.
func (n *PolicyNotifier) throttled(event *dmmodel.AlarmStateChangeEvent,
	p *model.NotificationPolicy, state *model.NotificationState) bool {
	if event.EventType == dmmodel.AlarmEventEscalated {
		return false
	}
	if state == nil || !state.LastNotifiedAt.Valid || !p.ThrottleSeconds.Valid {
		return false
	}
	gap := event.OccurredTime.Sub(state.LastNotifiedAt.Time)
	return gap < time.Duration(p.ThrottleSeconds.Int64)*time.Second
}

// Escalate re-notifies one open alarm on the escalation scheduler's tick (ADR-017 N.D).
// It re-evaluates the tenant's enabled policies against the alarm's persisted state and,
// when any escalation-enabled policy's window has elapsed and its cap is not yet reached,
// atomically CLAIMS the next tier and then delivers through the due policies' channels.
// defaultMax is the service-wide escalation cap applied to a policy that does not set its
// own MaxEscalations.
//
// Claim-before-send (ClaimEscalation) makes the timer loop safe to run in every replica
// and across overlapping deploy pods: exactly one claimant delivers a given tier. A lost
// claim (another pod won, or the alarm resolved between the read and the claim) is a
// silent no-op. Because the tier is advanced by the claim, escalation stays bounded even
// if the subsequent send fails — the accepted tradeoff is a rare missed page if the pod
// dies between claim and send. An error is returned only for a DB error on the claim or
// when the claim was won but every delivery failed, so the scheduler logs and retries the
// alarm on a later tick.
//
// Escalation uses ONE shared clock and tier per alarm (state.LastNotifiedAt /
// EscalationLevel), not per policy: when several escalation-enabled policies match one
// alarm, the shortest window drives re-notification and every policy's cap is measured
// against the shared tier. See NotificationPolicy.EscalateAfterSeconds for the semantics
// and the deferred per-policy-clock enhancement.
func (n *PolicyNotifier) Escalate(ctx context.Context, state *model.NotificationState,
	policies []*model.NotificationPolicy, now time.Time, defaultMax int) error {
	deliveries := n.planEscalation(state, policies, now, defaultMax)
	if len(deliveries) == 0 {
		return nil
	}
	claimed, err := n.api.ClaimEscalation(ctx, state.AlarmToken, state.EscalationLevel, now)
	if err != nil {
		return fmt.Errorf("claiming escalation tier for alarm %q: %w", state.AlarmToken, err)
	}
	if !claimed {
		// Another replica escalated this tier first, or the alarm resolved between the
		// scheduler's read and here. Either way we must not deliver.
		return nil
	}
	rendered := renderEscalation(state, state.EscalationLevel+1)
	delivered := 0
	for _, d := range deliveries {
		secret, err := n.resolveChannelSecret(ctx, d.channel.ID)
		if err != nil {
			log.Error().Err(err).Str("alarm", state.AlarmToken).Str("channel", d.channel.Token).
				Msg("Skipping escalation channel: failed to resolve delivery secret")
			continue
		}
		d.secret = secret
		if n.deliverWithRetry(ctx, d, rendered) {
			delivered++
		}
	}
	if delivered == 0 {
		return fmt.Errorf("all %d escalation deliveries failed for alarm %q after claiming the tier",
			len(deliveries), state.AlarmToken)
	}
	return nil
}

// planEscalation resolves the enabled policies to the deduplicated channel deliveries
// for re-notifying an open alarm. A policy contributes only when escalation is enabled
// (EscalateAfterSeconds > 0), its window has elapsed since the alarm's last
// notification, and the alarm's current EscalationLevel is below the policy's effective
// cap. Matching, channel filtering, and (channel, recipients) dedup reuse the same
// per-rule logic as the event-driven plan. Device-type-scoped policies are skipped
// (scoping deferred, ADR-044), as in dispatch.
func (n *PolicyNotifier) planEscalation(state *model.NotificationState,
	policies []*model.NotificationPolicy, now time.Time, defaultMax int) []delivery {
	seen := make(map[string]bool)
	out := make([]delivery, 0)
	for _, p := range policies {
		if p.DeviceTypeToken.Valid && p.DeviceTypeToken.String != "" {
			continue
		}
		if !escalationDue(state, p, now, defaultMax) {
			continue
		}
		out = n.appendRuleDeliveries(p.Token, state.Severity, p.Rules, seen, out)
	}
	return out
}

// appendRuleDeliveries appends the deduplicated (channel, recipients) deliveries for the
// rules of one policy that match severity, skipping — and warning about — a rule whose
// channel is missing, disabled, or has no adapter. seen dedups across the whole plan so
// two policies routing to the same place send once. Shared by the event-driven plan and
// the escalation plan so a misconfigured channel is logged on both paths.
func (n *PolicyNotifier) appendRuleDeliveries(policyToken, severity string,
	rules []model.NotificationRule, seen map[string]bool, out []delivery) []delivery {
	for i := range rules {
		rule := rules[i]
		if !severityMatches(rule.Severity, severity) {
			continue
		}
		if rule.Channel == nil {
			log.Warn().Str("policy", policyToken).Msg("Rule references a missing channel; skipping")
			continue
		}
		if !rule.Channel.Enabled {
			continue
		}
		if _, ok := n.adapters[rule.Channel.ChannelType]; !ok {
			log.Warn().Str("channel", rule.Channel.Token).Str("type", rule.Channel.ChannelType).
				Msg("No adapter for channel type; skipping")
			continue
		}
		recipients := parseRecipients(rule.Recipients)
		key := rule.Channel.Token + "|" + strings.Join(recipients, ",")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, delivery{channel: rule.Channel, recipients: recipients})
	}
	return out
}

// escalationDue reports whether policy p wants to re-notify the open alarm now: it has
// escalation enabled, its per-alarm window has elapsed since the last notification, and
// the alarm has not yet reached the policy's effective escalation cap.
//
// The window is measured wall-clock now minus LastNotifiedAt. LastNotifiedAt is first
// stamped by dispatch from the alarm event's OccurredTime, so an alarm whose RAISED is
// processed from a backlog (a restart replaying downtime) can be seen as already past its
// window and draw one spurious re-notification on the next tick — it self-corrects, since
// ClaimEscalation re-stamps the window with wall-clock time. Fully separating "when we
// notified" (wall clock) from the event's occurred time is a deferred refinement.
func escalationDue(state *model.NotificationState, p *model.NotificationPolicy,
	now time.Time, defaultMax int) bool {
	if !p.EscalateAfterSeconds.Valid || p.EscalateAfterSeconds.Int64 <= 0 {
		return false
	}
	if !state.LastNotifiedAt.Valid {
		return false
	}
	if state.EscalationLevel >= effectiveMaxEscalations(p, defaultMax) {
		return false
	}
	window := time.Duration(p.EscalateAfterSeconds.Int64) * time.Second
	return now.Sub(state.LastNotifiedAt.Time) >= window
}

// effectiveMaxEscalations is the policy's own MaxEscalations when set (> 0), else the
// service-wide default cap. Escalation is always bounded so a lost terminal event
// (a CLEARED/ACK dropped after the consumer's redelivery cap) can re-page at most this
// many times, not forever.
func effectiveMaxEscalations(p *model.NotificationPolicy, defaultMax int) int {
	if p.MaxEscalations.Valid && p.MaxEscalations.Int64 > 0 {
		return int(p.MaxEscalations.Int64)
	}
	return defaultMax
}

// deliverWithRetry sends one delivery, retrying up to n.attempts with a short linear
// backoff and bounding each attempt by n.timeout. It returns true on success. The
// adapter, not the processor, owns retry here (Notifier contract), so a final failure
// is logged and dropped rather than naked — a nak would redeliver the whole event and
// double-send the channels that already succeeded.
func (n *PolicyNotifier) deliverWithRetry(ctx context.Context, d delivery, rendered *RenderedNotification) bool {
	adapter := n.adapters[d.channel.ChannelType]
	for attempt := 1; attempt <= n.attempts; attempt++ {
		dctx, cancel := context.WithTimeout(ctx, n.timeout)
		err := adapter.Deliver(dctx, d.channel, d.secret, d.recipients, rendered)
		cancel()
		if err == nil {
			return true
		}
		log.Warn().Err(err).Str("channel", d.channel.Token).Str("type", d.channel.ChannelType).
			Int("attempt", attempt).Int("attempts", n.attempts).Msg("Notification delivery attempt failed")
		if attempt < n.attempts {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Duration(attempt) * retryBackoffBase):
			}
		}
	}
	log.Error().Str("channel", d.channel.Token).Str("type", d.channel.ChannelType).
		Int("attempts", n.attempts).Msg("Notification permanently dropped for channel after exhausting attempts")
	return false
}

// resolveChannelSecret returns the channel's delivery secret from the store, keyed
// by the tenant-scoped channel handle (ADR-059). A ref for which no secret is stored
// yields an empty string (the channel simply has no secret) rather than an error, so
// a secretless channel delivers normally; a genuine store error is returned so the
// caller can treat it as a transient delivery failure and let redelivery retry.
func (n *PolicyNotifier) resolveChannelSecret(ctx context.Context, channelID uint) (string, error) {
	ref, err := model.ChannelSecretRef(ctx, channelID)
	if err != nil {
		return "", err
	}
	value, err := n.store.Resolve(ctx, ref)
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// severityMatches reports whether a rule's severity selector matches an alarm's
// severity: the wildcard matches anything, otherwise an exact match.
func severityMatches(ruleSeverity, alarmSeverity string) bool {
	return ruleSeverity == model.SeverityAny || ruleSeverity == alarmSeverity
}

// parseRecipients decodes a rule's opaque recipients JSON array into strings. The
// value was validated as a string array on write, so a decode error yields no
// recipients rather than failing the dispatch.
func parseRecipients(raw *datatypes.JSON) []string {
	if raw == nil {
		return nil
	}
	var recipients []string
	if err := json.Unmarshal([]byte(*raw), &recipients); err != nil {
		log.Warn().Err(err).Msg("Ignoring unparseable rule recipients")
		return nil
	}
	return recipients
}
