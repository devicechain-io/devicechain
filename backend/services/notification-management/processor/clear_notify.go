// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// notifyClear tells the channels of every policy that opted in (NotifyOnClear) that an
// alarm it paged has cleared. It runs BEFORE the clear is stamped on the notification
// state, because the state is what says whether this cycle was ever paged and whether this
// clear was already announced: stamping first would make a redelivery after a failed send
// look like a clear that had been handled, and the announcement would be lost.
//
// What it will NOT do, and why:
//
//   - Announce a clear for an alarm nobody was paged about (no state, no notification
//     counted for this cycle). A throttled, held-and-dropped or unrouted raise never reached
//     a human, and a "cleared" about it would be noise with no raise to pair with.
//   - Announce twice. A state already stamped cleared for this cycle is a redelivery.
//   - Throttle. A clear is a distinct fact, not a repeat of the raise the throttle is for.
//   - Hold for quiet hours. A clear is informational; a policy that is quiet right now
//     simply does not send it (it is dropped, not deferred), because a held "cleared"
//     released hours later reports an old fact as news.
//   - Record anything. RecordNotification would count the clear as a page and re-arm the
//     escalation clock of an alarm that is over.
//
// It returns an error only when it could not READ what it needs (the policies or the state),
// before anything was sent, so the durable consumer redelivers and nothing is sent twice. A
// delivery that fails is not retried by redelivery: see the comment at the end. Like every
// dispatch, delivery is at-least-once: a crash between the send and the stamp that follows
// re-sends the clear on redelivery.
func (n *PolicyNotifier) notifyClear(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	tenant, _ := core.TenantFromContext(ctx)

	deadline := time.Now().Add(n.wholeDispatchBudget())
	if ackDeadline, ok := messaging.AckDeadlineFrom(ctx); ok {
		if capped := ackDeadline.Add(-dispatchMargin); capped.Before(deadline) {
			deadline = capped
		}
	}
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	policies, err := n.api.EnabledNotificationPolicies(dctx)
	if err != nil {
		if errors.Is(err, rdb.ErrTenantPurged) {
			return nil
		}
		return fmt.Errorf("loading notification policies: %w", err)
	}
	optedIn := make([]*model.NotificationPolicy, 0, len(policies))
	for _, p := range policies {
		if p.NotifyOnClear {
			optedIn = append(optedIn, p)
		}
	}
	if len(optedIn) == 0 {
		return nil
	}

	states, err := n.api.NotificationStatesByAlarmToken(dctx, []string{event.AlarmToken})
	if err != nil {
		if errors.Is(err, rdb.ErrTenantPurged) {
			return nil
		}
		return fmt.Errorf("loading notification state: %w", err)
	}
	if len(states) == 0 || !clearOwed(states[0], event) {
		return nil
	}

	now := n.clock()
	seen := make(map[string]bool)
	deliveries := make([]delivery, 0)
	for _, p := range optedIn {
		// Device-type scoping is skipped exactly as plan skips it.
		if p.DeviceTypeToken.Valid && p.DeviceTypeToken.String != "" {
			continue
		}
		if hold, _ := quietHold(p, event.Severity, now); hold {
			log.Debug().Str("policy", p.Token).Str("alarm", event.AlarmToken).
				Msg("Not announcing the clear: the policy is in quiet hours")
			continue
		}
		deliveries = n.appendRuleDeliveries(p.Token, event.Severity, p.Rules, seen, deliveries)
	}
	if len(deliveries) == 0 {
		return nil
	}

	tally := n.deliverAll(dctx, deliveries, renderNotification(event), event.AlarmToken, model.AttemptTriggerAlarm)
	if tally.delivered == 0 {
		// 🔴 A failed clear is LOGGED, NOT RETURNED. Returning it would skip the stamp that
		// follows, and that stamp is what stops this alarm escalating and drops what quiet hours
		// hold about it: an endpoint that stays down would leave a resolved alarm paging people
		// until the consumer gave up. The clear is informational and each channel has already had
		// its in-line retries; the failure is in the delivery ledger and the log.
		log.Error().Str("tenant", tenant).Str("alarm", event.AlarmToken).
			Int("refused", tally.refused).Int("failed", tally.failed).Int("unattempted", tally.unattempted).
			Msg("No channel took this alarm's clear; it is not retried, and the alarm is recorded as cleared.")
	}
	return nil
}

// clearOwed reports whether the clear is one a human is owed: the alarm's current cycle was
// paged and is not yet recorded as cleared. A state row for a different cycle than the
// event's describes some other raise, so it says nothing about this one.
func clearOwed(state *model.NotificationState, event *dmmodel.AlarmStateChangeEvent) bool {
	if state.NotifyCount <= 0 || state.ClearedAt.Valid {
		return false
	}
	if state.CycleRaisedAt.Valid && !event.RaisedTime.IsZero() &&
		!state.CycleRaisedAt.Time.Equal(event.RaisedTime.UTC().Truncate(time.Microsecond)) {
		return false
	}
	return true
}
