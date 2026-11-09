// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// announceClear tells the destinations that paged an alarm that it has cleared, for the
// policies that opted in (NotifyOnClear). It runs AFTER the clear has been stamped on the
// notification state, and that order is the point:
//
//   - the stamp is what stops escalation, so no "still unresolved" page can follow the clear
//     while it is being sent, and a failed send can be retried without the alarm paging on;
//   - the announcement is claimed separately (clear_notified_at, a compare-and-set) before it is
//     sent, so each cycle's clear goes out once across replicas and redeliveries, and a
//     redelivery after a transient failure of the stamp itself does not send it again.
//
// WHERE IT GOES. Only to the (channel, recipients) targets that actually delivered a page in
// this cycle (NotificationState.PagedTargets), restricted to those some opted-in policy routes.
// Routing by the severity at clear time would be wrong twice over: an alarm raised CRITICAL and
// de-escalated to MAJOR would resolve nothing on the channel that paged it, and a policy that
// never paged (throttled, held for quiet hours, created after the raise) would announce a clear
// with no raise to pair it with.
//
// WHAT IT WILL NOT DO. It does not throttle (a clear is a distinct fact), does not count as a
// page or arm escalation, and does not hold for quiet hours: a policy that is quiet right now
// skips the clear and the claim is spent, because a "cleared" released hours later reports old
// news. A clear that reached no channel and could still be retried returns an error after
// giving the claim back, so the durable consumer redelivers; a clear that reached at least one
// channel, or that was refused, is final. Delivery is at-most-once past the claim: a crash
// between claiming and sending loses the clear rather than repeating it.
func (n *PolicyNotifier) announceClear(ctx context.Context, clear *dmmodel.AlarmStateChangeEvent) error {
	tenant, _ := core.TenantFromContext(ctx)

	deadline := time.Now().Add(n.wholeDispatchBudget())
	if ackDeadline, ok := messaging.AckDeadlineFrom(ctx); ok {
		if capped := ackDeadline.Add(-dispatchMargin); capped.Before(deadline) {
			deadline = capped
		}
	}
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	err := n.announceClearIn(dctx, tenant, clear)
	if errors.Is(err, rdb.ErrTenantPurged) {
		return nil
	}
	return err
}

func (n *PolicyNotifier) announceClearIn(ctx context.Context, tenant string, clear *dmmodel.AlarmStateChangeEvent) error {
	states, err := n.api.NotificationStatesByAlarmToken(ctx, []string{clear.AlarmToken})
	if err != nil {
		return fmt.Errorf("loading notification state: %w", err)
	}
	if len(states) == 0 {
		return nil
	}
	state := states[0]
	if !state.ClearedAt.Valid || state.ClearAnnounced() || !state.SameCycleAs(clear.RaisedTime) {
		return nil
	}
	paged := state.PagedTargetSet()
	if len(paged) == 0 {
		return nil
	}

	policies, err := n.api.EnabledNotificationPolicies(ctx)
	if err != nil {
		return fmt.Errorf("loading notification policies: %w", err)
	}
	optedIn := 0
	now := n.clock()
	seen := make(map[string]bool)
	deliveries := make([]delivery, 0)
	for _, p := range policies {
		if !p.NotifyOnClear {
			continue
		}
		optedIn++
		// Device-type scoping is skipped exactly as plan skips it.
		if p.DeviceTypeToken.Valid && p.DeviceTypeToken.String != "" {
			continue
		}
		if hold, _ := quietHold(p, clear.Severity, now); hold {
			log.Debug().Str("policy", p.Token).Str("alarm", clear.AlarmToken).
				Msg("Not announcing the clear: the policy is in quiet hours")
			continue
		}
		deliveries = n.appendPagedDeliveries(p.Token, p.Rules, paged, seen, deliveries)
	}
	if optedIn == 0 {
		return nil
	}

	claimedAt := time.Now().UTC()
	won, err := n.api.ClaimClearAnnouncement(ctx, clear.AlarmToken, clear.RaisedTime, claimedAt)
	if err != nil {
		return fmt.Errorf("claiming the clear announcement: %w", err)
	}
	if !won || len(deliveries) == 0 {
		return nil
	}

	tally := n.deliverAll(ctx, deliveries, renderNotification(clear), clear.AlarmToken, model.AttemptTriggerAlarm)
	if tally.delivered > 0 {
		return nil
	}
	if tally.retryable() == 0 {
		log.Error().Str("tenant", tenant).Str("alarm", clear.AlarmToken).Int("channels", tally.refused).
			Msg("Every channel for this alarm's clear was refused; not retrying, because redelivery " +
				"cannot change that.")
		return nil
	}
	// Nothing took it and a retry could: give the claim back so the redelivery can send it. The
	// clear is already stamped, so the alarm is not paging in the meantime.
	// Under its own bound, not the delivery deadline: a delivery that spent that deadline is
	// exactly when the claim most needs to be given back.
	uctx, ucancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer ucancel()
	if uerr := n.api.UnclaimClearAnnouncement(uctx, clear.AlarmToken, claimedAt); uerr != nil {
		log.Error().Err(uerr).Str("tenant", tenant).Str("alarm", clear.AlarmToken).
			Msg("Could not give back the clear announcement; the clear will not be retried")
		return nil
	}
	return fmt.Errorf("all %d clear deliveries failed for alarm %q", len(deliveries), clear.AlarmToken)
}

// appendPagedDeliveries is appendRuleDeliveries for a clear: the policy's rules, whatever
// their severity, narrowed to the targets that paged. The channel checks are the same.
func (n *PolicyNotifier) appendPagedDeliveries(policyToken string, rules []model.NotificationRule,
	paged map[string]bool, seen map[string]bool, out []delivery) []delivery {
	for i := range rules {
		rule := rules[i]
		if rule.Channel == nil || !rule.Channel.Enabled {
			continue
		}
		if _, ok := n.adapters[rule.Channel.ChannelType]; !ok {
			continue
		}
		recipients := parseRecipients(rule.Recipients)
		key := rule.Channel.Token + "|" + strings.Join(recipients, ",")
		if !paged[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, delivery{channel: rule.Channel, recipients: recipients, policy: policyToken})
	}
	return out
}

// pagedTargets is the identity of the deliveries that went out, for the state row.
func pagedTargets(ds []delivery) []model.PagedTarget {
	out := make([]model.PagedTarget, 0, len(ds))
	for _, d := range ds {
		out = append(out, model.PagedTarget{Channel: d.channel.Token, Recipients: d.recipients})
	}
	return out
}

// announceLateClear covers the clear that overtook the page it follows. The worker pool is
// unordered, so a CLEARED can be processed before the RAISED's delivery is recorded: it finds no
// paged targets and announces nothing. Once the page has recorded its targets, if the row is
// already stamped cleared and the clear is still unannounced, the announcement is owed now.
// Errors are logged, not returned: the page that led here has gone out, and returning would
// resend it.
func (n *PolicyNotifier) announceLateClear(ctx context.Context, event *dmmodel.AlarmStateChangeEvent,
	recorded *model.NotificationState) {
	if recorded == nil || !recorded.ClearedAt.Valid || recorded.ClearAnnounced() ||
		!recorded.SameCycleAs(event.RaisedTime) {
		return
	}
	clear := *event
	clear.EventType = dmmodel.AlarmEventCleared
	clear.State = "CLEARED"
	clear.OccurredTime = recorded.ClearedAt.Time
	if err := n.announceClear(ctx, &clear); err != nil {
		tenant, _ := core.TenantFromContext(ctx)
		log.Error().Err(err).Str("tenant", tenant).Str("alarm", event.AlarmToken).
			Msg("Could not announce a clear that overtook its page")
	}
}
