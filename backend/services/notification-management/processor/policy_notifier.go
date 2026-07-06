// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
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
	adapters map[string]ChannelAdapter
	attempts int
	timeout  time.Duration
}

// NewPolicyNotifier builds the dispatcher over the persistence API, with attempts
// per-channel delivery tries and timeout bounding a single attempt.
func NewPolicyNotifier(api *model.Api, attempts int, timeout time.Duration) *PolicyNotifier {
	if attempts < 1 {
		attempts = 1
	}
	return &PolicyNotifier{
		api:      api,
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
		// Idempotent state update; a DB error is safe to retry (no side effects).
		return n.api.MarkAcknowledged(ctx, event.AlarmToken, event.OccurredTime)
	case dmmodel.AlarmEventCleared:
		return n.api.MarkCleared(ctx, event.AlarmToken, event.OccurredTime)
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
// the rendered notification to.
type delivery struct {
	channel    *model.NotificationChannel
	recipients []string
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
		for i := range p.Rules {
			rule := p.Rules[i]
			if !severityMatches(rule.Severity, event.Severity) {
				continue
			}
			if rule.Channel == nil {
				log.Warn().Str("policy", p.Token).Msg("Rule references a missing channel; skipping")
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

// deliverWithRetry sends one delivery, retrying up to n.attempts with a short linear
// backoff and bounding each attempt by n.timeout. It returns true on success. The
// adapter, not the processor, owns retry here (Notifier contract), so a final failure
// is logged and dropped rather than naked — a nak would redeliver the whole event and
// double-send the channels that already succeeded.
func (n *PolicyNotifier) deliverWithRetry(ctx context.Context, d delivery, rendered *RenderedNotification) bool {
	adapter := n.adapters[d.channel.ChannelType]
	for attempt := 1; attempt <= n.attempts; attempt++ {
		dctx, cancel := context.WithTimeout(ctx, n.timeout)
		err := adapter.Deliver(dctx, d.channel, d.recipients, rendered)
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
