// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
)

// The outcomes devicechain_notificationmanagement_quiet_hours_total is labelled with. A
// closed set: the label is never fed from an error string.
const (
	quietHeld             = "held"
	quietReleased         = "released"
	quietDroppedResolved  = "dropped_resolved"
	quietDroppedPolicy    = "dropped_policy"
	quietDroppedExhausted = "dropped_exhausted"
	// superseded: a page about the alarm as it is now went out through the policy outside
	// quiet hours (or above the bypass), which answers what the policy was holding.
	quietSuperseded = "superseded"
)

// maxReleaseAttempts is how many times a held notification whose release reached no
// channel is put back for another pass before it is dropped. It matches the durable
// consumer's redelivery budget for an ordinary notification, so a held page is retried as
// hard as one that was never held.
const maxReleaseAttempts = 5

// releaseBatch bounds how many held notifications one tenant releases per pass. The rest
// stay due and go on the next pass, a tick later; a tenant cannot hold the loop for every
// tenant behind it with a morning's backlog.
const releaseBatch = 200

// quietHold reports whether policy p holds a notification of the given alarm severity at
// `now`, and when the quiet stretch it falls in ends.
//
// 🔴 A STORED CONFIGURATION THAT NO LONGER VALIDATES PAGES. It was valid when written —
// the write validates it — so this is a hand-edited row or a rule tightened since, and the
// choice is between reading it as "no quiet hours" and reading it as "always quiet". For
// the service that carries alarms to humans the second is a silent outage; the first is a
// page at night. It pages, and says so in the log every time it does.
func quietHold(p *model.NotificationPolicy, severity string, now time.Time) (bool, time.Time) {
	q, err := p.QuietHours()
	if err != nil {
		log.Error().Err(err).Str("policy", p.Token).
			Msg("The policy's stored quiet hours do not validate; ignoring them and paging. " +
				"Re-save the policy's quiet hours to fix this.")
		return false, time.Time{}
	}
	return q.Holds(severity, now)
}

// holdQuiet splits the enabled policies into those that send this notification now and
// those whose quiet hours hold it, returning a deferral for each of the second.
//
// A policy is held only when it would actually have sent something: it is not scoped
// (scoping is skipped exactly as plan skips it), its throttle does not suppress the
// notification (throttling comes FIRST — a notification the throttle suppresses is
// suppressed, not held for the morning), and at least one of its rules matches the alarm's
// severity. Everything else passes through to plan unchanged, which applies the same
// filters to the policies it is given.
func (n *PolicyNotifier) holdQuiet(event *dmmodel.AlarmStateChangeEvent, policies []*model.NotificationPolicy,
	state *model.NotificationState, now time.Time) ([]*model.NotificationPolicy, []*model.NotificationDeferral) {
	sendNow := make([]*model.NotificationPolicy, 0, len(policies))
	var held []*model.NotificationDeferral
	var encoded datatypes.JSON
	for _, p := range policies {
		if p.DeviceTypeToken.Valid && p.DeviceTypeToken.String != "" ||
			n.throttled(event, p, state) || !anyRuleMatches(p, event.Severity) {
			sendNow = append(sendNow, p)
			continue
		}
		hold, until := quietHold(p, event.Severity, now)
		if !hold {
			sendNow = append(sendNow, p)
			continue
		}
		if encoded == nil {
			raw, err := json.Marshal(event)
			if err != nil {
				// An alarm event that cannot be encoded cannot be held. Page now.
				log.Error().Err(err).Str("alarm", event.AlarmToken).
					Msg("Could not encode the alarm event to hold it for quiet hours; paging now")
				sendNow = append(sendNow, p)
				continue
			}
			encoded = datatypes.JSON(raw)
		}
		held = append(held, &model.NotificationDeferral{
			AlarmToken:   event.AlarmToken,
			PolicyId:     p.ID,
			AlarmKey:     event.AlarmKey,
			Severity:     event.Severity,
			OccurredTime: event.OccurredTime,
			ReleaseAt:    until,
			Event:        encoded,
		})
	}
	return sendNow, held
}

// anyRuleMatches reports whether any of the policy's rules matches the severity: a policy
// that would send nothing for this alarm has nothing to hold.
func anyRuleMatches(p *model.NotificationPolicy, severity string) bool {
	for i := range p.Rules {
		if severityMatches(p.Rules[i].Severity, severity) {
			return true
		}
	}
	return false
}

// ReleaseHeld sends the caller tenant's held notifications whose quiet stretch has ended.
// The escalation scheduler calls it on every pass, for every tenant holding any, with the
// tenant's ENABLED policies already loaded.
//
// For each due deferral, grouped by alarm:
//
//   - its policy is gone or disabled: dropped (a disabled policy sends nothing);
//   - the alarm was acknowledged or cleared AFTER the held event occurred: dropped — the
//     page would be about an alarm a human has already dealt with. A stamp from BEFORE it
//     belongs to an earlier cycle of the same alarm token and does not count;
//   - the held event is of an EARLIER alarm cycle than the notification state is on:
//     dropped the same way, since that cycle has cleared (see heldForEarlierCycle);
//   - the policy holds it again (its quiet hours were edited, or the next stretch began):
//     moved to the new end;
//   - otherwise it is LEASED (a compare-and-swap so exactly one replica wins; the row stays
//     until the send is decided, see Api.LeaseNotificationDeferral) and sent through the
//     policy's rules, deduplicated across every policy releasing the same alarm in this
//     pass.
//
// When at least one channel takes it, the rows are removed and the alarm's notification
// state is stamped NOW, which starts the escalation clock at the moment somebody was
// actually paged. When none does and a retry could help, the rows are put back with their
// attempt count raised and a growing delay (releaseRetryDelay); the last of
// maxReleaseAttempts that fails is dead-lettered, as the ordinary dispatch dead-letters an
// alarm that reached nobody.
func (n *PolicyNotifier) ReleaseHeld(ctx context.Context, policies []*model.NotificationPolicy, now time.Time) error {
	tenant, _ := core.TenantFromContext(ctx)
	// ADR-077: a deleted tenant pages nobody, and its rows are the purge's to erase.
	if n.tenantDeleted(ctx) {
		return nil
	}
	due, err := n.api.DueNotificationDeferrals(ctx, now, releaseBatch)
	if err != nil {
		if errors.Is(err, rdb.ErrTenantPurged) {
			return nil
		}
		return fmt.Errorf("loading held notifications: %w", err)
	}
	if len(due) == 0 {
		return nil
	}
	byID := make(map[uint]*model.NotificationPolicy, len(policies))
	for _, p := range policies {
		byID[p.ID] = p
	}
	// Group by alarm, preserving the order the rows came in.
	var order []string
	groups := make(map[string][]*model.NotificationDeferral)
	for _, d := range due {
		if _, ok := groups[d.AlarmToken]; !ok {
			order = append(order, d.AlarmToken)
		}
		groups[d.AlarmToken] = append(groups[d.AlarmToken], d)
	}
	var lastErr error
	for _, alarm := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := n.releaseAlarm(ctx, alarm, groups[alarm], byID, now); err != nil {
			if errors.Is(err, rdb.ErrTenantPurged) {
				return nil
			}
			log.Warn().Err(err).Str("tenant", tenant).Str("alarm", alarm).
				Msg("Releasing held notifications failed; will retry next pass")
			lastErr = err
		}
	}
	return lastErr
}

// releaseAlarm releases one alarm's due deferrals. See ReleaseHeld.
func (n *PolicyNotifier) releaseAlarm(ctx context.Context, alarm string, due []*model.NotificationDeferral,
	byID map[uint]*model.NotificationPolicy, now time.Time) error {
	states, err := n.api.NotificationStatesByAlarmToken(ctx, []string{alarm})
	if err != nil {
		return fmt.Errorf("loading notification state: %w", err)
	}
	var state *model.NotificationState
	if len(states) > 0 {
		state = states[0]
	}

	seen := make(map[string]bool)
	var deliveries []delivery
	var claimed []*model.NotificationDeferral
	var latest *model.NotificationDeferral
	for _, d := range due {
		p := byID[d.PolicyId]
		if p == nil {
			if err := n.drop(ctx, d, quietDroppedPolicy); err != nil {
				return err
			}
			continue
		}
		if resolvedSince(state, d.OccurredTime) || heldForEarlierCycle(state, d) {
			if err := n.drop(ctx, d, quietDroppedResolved); err != nil {
				return err
			}
			continue
		}
		if hold, until := quietHold(p, d.Severity, now); hold {
			if err := n.api.RescheduleNotificationDeferral(ctx, d, until); err != nil {
				return fmt.Errorf("re-holding notification: %w", err)
			}
			continue
		}
		// LEASED, not deleted: the row stays until the send is decided, so a sender that
		// dies here leaves it to come due again when the lease runs out. The lease is
		// measured from the wall clock at THIS moment, not the pass's `now`, which is as old
		// as every release this pass has already sent.
		won, err := n.api.LeaseNotificationDeferral(ctx, d, n.clock().Add(n.releaseLease()))
		if err != nil {
			return fmt.Errorf("leasing held notification: %w", err)
		}
		if !won {
			// Another replica is releasing it, or a newer event replaced it since it was
			// read; either way this copy must not be sent.
			continue
		}
		claimed = append(claimed, d)
		deliveries = n.appendRuleDeliveries(p.Token, d.Severity, p.Rules, seen, deliveries)
		if latest == nil || d.OccurredTime.After(latest.OccurredTime) {
			latest = d
		}
	}
	if len(claimed) == 0 {
		return nil
	}
	if len(deliveries) == 0 {
		// Leased, but the policies route this severity nowhere any more (a rule was
		// edited, a channel disabled). Nobody to page.
		n.finish(ctx, alarm, claimed)
		n.countQuiet(quietDroppedPolicy, len(claimed))
		return nil
	}

	var event dmmodel.AlarmStateChangeEvent
	if err := json.Unmarshal(latest.Event, &event); err != nil {
		// Unreadable is permanent; putting it back would fail the same way forever.
		err = fmt.Errorf("decoding held alarm event: %w", err)
		n.finish(ctx, alarm, claimed)
		n.countQuiet(quietDroppedExhausted, len(claimed))
		n.deadLetterHeld(ctx, latest, deadletter.ReasonUnprocessable, err)
		return err
	}

	rctx, cancel := context.WithTimeout(ctx, n.wholeDispatchBudget())
	defer cancel()
	tally := n.deliverAll(rctx, deliveries, renderNotification(&event), alarm)
	if tally.delivered > 0 {
		n.finish(ctx, alarm, claimed)
		n.countQuiet(quietReleased, len(claimed))
		// Settled, not logged-and-dropped: the page is out and its deferral rows are deleted, so
		// this write is the only record that it happened. Without it the alarm has no throttle
		// basis and never escalates. Held on the notifier's own Holder (the scheduler has no
		// message to beat); at-least-once, as dispatch documents.
		if err := n.settle(ctx, func(c context.Context) error {
			sctx, scancel := context.WithTimeout(c, recordTimeout)
			defer scancel()
			// The cycle is the HELD event's (its RaisedTime travels in the stored event), not
			// whatever cycle the state row is on now: a page held through a clear and re-raise
			// describes the cycle it was raised in, and RecordNotification ignores it as stale
			// rather than writing it into the newer cycle as that cycle's first page.
			return n.api.RecordNotification(sctx, alarm, latest.AlarmKey, latest.Severity,
				event.RaisedTime, now)
		}); err != nil {
			log.Error().Err(err).Str("alarm", alarm).
				Msg("Released a held notification but failed to record notification state")
		}
		return nil
	}
	if tally.retryable() == 0 {
		// Every channel refused it; a retry cannot change a refusal. The ordinary dispatch
		// acks this case too, and deliveries_refused_total has already counted it.
		n.finish(ctx, alarm, claimed)
		n.countQuiet(quietDroppedExhausted, len(claimed))
		return nil
	}
	failure := fmt.Errorf("all %d deliveries failed releasing held notifications for alarm %q",
		len(deliveries), alarm)
	for _, d := range claimed {
		if d.Attempts+1 >= maxReleaseAttempts {
			n.finish(ctx, alarm, []*model.NotificationDeferral{d})
			n.countQuiet(quietDroppedExhausted, 1)
			log.Error().Str("alarm", alarm).Uint("policy", d.PolicyId).Int("attempts", d.Attempts+1).
				Msg("Dead-lettering a held notification: every release attempt reached no channel, " +
					"so nobody was paged about it")
			n.deadLetterHeld(ctx, d, deadletter.ReasonExhausted, failure)
			continue
		}
		// If this write fails the row keeps its lease, and comes due again when it ends.
		if err := n.api.RetryNotificationDeferral(ctx, d, now.Add(releaseRetryDelay(d.Attempts+1))); err != nil {
			return fmt.Errorf("putting back held notifications after a failed release: %w", err)
		}
	}
	return failure
}

// releaseLease is how long a leased deferral is left alone by every other release: long
// enough for this one to send it (the whole-dispatch budget) and then record what happened.
func (n *PolicyNotifier) releaseLease() time.Duration {
	return n.wholeDispatchBudget() + 2*recordTimeout
}

// releaseRetryDelay is how long a held notification whose release reached no channel waits
// before attempt number `attempt` (1-based count of failures so far): 1, 2, 4 then 8
// minutes, so the five attempts span about a quarter of an hour of channel outage rather
// than five consecutive scheduler ticks. A notification that has waited all night can wait
// minutes more; one dropped because a mail relay restarted at the end of the night cannot
// be taken back.
func releaseRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 4 {
		attempt = 4
	}
	return time.Minute << (attempt - 1)
}

// finish removes leased deferrals whose release is decided. It is settled (held through a
// store outage, see settle), because after a send it is what stops the page going out again:
// a row that outlives its page comes due when its lease ends and is released a second time.
// A failure that survives the hold is logged and not returned: the row keeps its lease and
// comes due again, so the residual cost is that same duplicate — the side to err on.
func (n *PolicyNotifier) finish(ctx context.Context, alarm string, done []*model.NotificationDeferral) {
	for _, d := range done {
		if err := n.settle(ctx, func(c context.Context) error {
			fctx, cancel := context.WithTimeout(c, recordTimeout)
			defer cancel()
			return n.api.FinishNotificationDeferral(fctx, d)
		}); err != nil {
			log.Error().Err(err).Str("alarm", alarm).Uint("policy", d.PolicyId).
				Msg("Could not remove a released held notification; it will be released again " +
					"when its lease ends")
		}
	}
}

// deadLetterHeld records a held notification that will never be sent, the way the
// ordinary dispatch records an alarm that reached nobody: a counter and a log line are not
// enough for a page that was never sent.
func (n *PolicyNotifier) deadLetterHeld(ctx context.Context, d *model.NotificationDeferral,
	reason deadletter.Reason, cause error) {
	sink := n.dead.Load()
	if sink == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	err := sink.Write(ctx, deadletter.Envelope{
		Kind:   deadletter.KindNotification,
		Reason: reason,
		Summary: "an alarm notification held for a policy's quiet hours could not be delivered " +
			"to any configured channel when the quiet hours ended, so nobody was paged about it",
		Detail:     detail,
		Attempts:   d.Attempts + 1,
		Reference:  d.AlarmToken,
		OccurredAt: n.clock().UTC(),
		Payload:    d.Event,
	})
	if err != nil {
		// The loss is already counted, on dead_letter_lost_total, by the sink.
		log.Error().Err(err).Str("alarm", d.AlarmToken).
			Msg("LOST notification: a held alarm notification reached nobody and could not be dead-lettered.")
	}
}

// drop claims a deferral and counts it as dropped for the given reason. A lost claim is
// not an error: another replica already decided it.
func (n *PolicyNotifier) drop(ctx context.Context, d *model.NotificationDeferral, outcome string) error {
	won, err := n.api.ClaimNotificationDeferral(ctx, d)
	if err != nil {
		return fmt.Errorf("dropping held notification: %w", err)
	}
	if won {
		n.countQuiet(outcome, 1)
	}
	return nil
}

// heldForEarlierCycle reports whether a deferral holds a page of an EARLIER alarm cycle
// than the one the notification state is on. That cycle has been cleared — an alarm only
// re-raises from cleared — so its page is dropped as resolved, exactly as it would have
// been had its CLEARED arrived first; releasing it would page about an alarm that has
// since cleared and been raised again, as if it were the current one. A held event that
// cannot be decoded names no cycle, and is left to the release, which dead-letters it.
func heldForEarlierCycle(state *model.NotificationState, d *model.NotificationDeferral) bool {
	if state == nil || !state.CycleRaisedAt.Valid {
		return false
	}
	var held dmmodel.AlarmStateChangeEvent
	if err := json.Unmarshal(d.Event, &held); err != nil {
		return false
	}
	return state.OnLaterCycleThan(held.RaisedTime)
}

// resolvedSince reports whether the alarm was acknowledged or cleared after `occurred`.
// The alarm token outlives a clear (a re-raise reuses it). The state's stamps describe the
// cycle the row is on (NotificationState.CycleRaisedAt), which can be OLDER than the held
// event's — a re-raise that quiet hours held records nothing — so a stamp from before the
// held event belongs to an earlier cycle and does not resolve this one.
func resolvedSince(state *model.NotificationState, occurred time.Time) bool {
	if state == nil {
		return false
	}
	if state.AcknowledgedAt.Valid && !state.AcknowledgedAt.Time.Before(occurred) {
		return true
	}
	return state.ClearedAt.Valid && !state.ClearedAt.Time.Before(occurred)
}

// settle runs a store write that follows a send, holding it through a store outage on the
// notifier's Holder (nil runs it once). See storehold.Settle for why the send is never re-run.
func (n *PolicyNotifier) settle(ctx context.Context, op func(ctx context.Context) error) error {
	if n.Hold == nil {
		return op(ctx)
	}
	return n.Hold.SettleWith(ctx, op)
}
