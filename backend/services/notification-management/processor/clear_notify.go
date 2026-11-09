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
func (n *PolicyNotifier) announceClear(ctx context.Context, clear *dmmodel.AlarmStateChangeEvent, fresh, cutoff bool) error {
	tenant, _ := core.TenantFromContext(ctx)

	// fresh: the caller is not this clear's own message (a RAISED that finished first, or the
	// scheduler), so the message's ack deadline, already spent, must not bound it.
	deadline := time.Now().Add(n.wholeDispatchBudget())
	if ackDeadline, ok := messaging.AckDeadlineFrom(ctx); ok && !fresh {
		if capped := ackDeadline.Add(-dispatchMargin); capped.Before(deadline) {
			deadline = capped
		}
	}
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	err := n.announceClearIn(dctx, tenant, clear, cutoff)
	if errors.Is(err, rdb.ErrTenantPurged) {
		return nil
	}
	return err
}

// pendingClearWindow is how long after a clear the scheduler keeps trying to announce it. A
// clear older than this is news no longer worth sending.
const pendingClearWindow = 30 * time.Minute

// pendingClearBackoff is how long a clear whose send failed is left alone before the scheduler
// tries it again.
const pendingClearBackoff = 2 * time.Minute

// pendingClearBatch bounds one tenant's pickup per tick.
const pendingClearBatch = 50

// pendingClearScan is how many candidate rows one tick reads, so rows that cannot be announced
// (a paged set that belongs to another cycle) do not occupy the batch.
const pendingClearScan = 4 * pendingClearBatch

// AnnouncePendingClears is the durable half of the late-clear path. A clear can overtake its page,
// or fail to send when it first came due, after both of its messages have been acknowledged, so
// no redelivery will carry it. The scheduler walks the alarms that are stamped cleared, paged
// someone and have no claimed announcement, and announces each; the claim keeps it once across
// pods. It does nothing for a tenant with no policy that opted in.
func (n *PolicyNotifier) AnnouncePendingClears(ctx context.Context, policies []*model.NotificationPolicy, now time.Time) error {
	if n.tenantDeleted(ctx) {
		return nil
	}
	optedIn := false
	var optedSince time.Time
	for _, p := range policies {
		if p.NotifyOnClear {
			if cut := p.ClearCutoff(); !optedIn || cut.Before(optedSince) {
				optedSince = cut
			}
			optedIn = true
		}
	}
	if !optedIn {
		return nil
	}
	// ONE budget for the whole pass, about one dispatch's worth, and the caller's cancellation is
	// kept: a webhook that is down must not stall the escalations behind this, nor a stop.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, n.wholeDispatchBudget())
	defer cancel()
	// Only clears that happened after a policy opted in: switching the option on must not
	// announce the last half hour's clears.
	since := now.Add(-pendingClearWindow)
	if optedSince.After(since) {
		since = optedSince
	}
	states, err := n.api.PendingClearStates(ctx, since, now.Add(-pendingClearBackoff), pendingClearScan)
	if err != nil {
		if errors.Is(err, rdb.ErrTenantPurged) {
			return nil
		}
		return fmt.Errorf("listing clears awaiting announcement: %w", err)
	}
	var lastErr error
	sent, announced := 0, 0
	// spent: the pass ran out of ITS OWN budget (the caller is still going) after announcing
	// something (a row handled without error: sent, or settled by another replica). That is a healthy destination with a backlog longer than one pass, and the rest
	// waits for the next tick; reporting it as a failed pass would page an operator about a
	// destination that is working. With nothing announced it still is a failure.
	spent := func(err error) bool {
		return announced > 0 && parent.Err() == nil && errors.Is(err, context.DeadlineExceeded)
	}
	for _, st := range states {
		if err := ctx.Err(); err != nil {
			if spent(err) {
				log.Info().Int("announced", announced).Msg("Pending clears: pass budget spent with a backlog left; continuing next tick")
				return lastErr
			}
			return err
		}
		if sent >= pendingClearBatch {
			break
		}
		sent++
		clear := &dmmodel.AlarmStateChangeEvent{
			EventType: dmmodel.AlarmEventCleared, AlarmToken: st.AlarmToken, AlarmKey: st.AlarmKey,
			Severity: st.Severity, State: "CLEARED",
			RaisedTime: st.CycleRaisedAt.Time, OccurredTime: st.ClearedAt.Time,
		}
		if err := n.announceClear(ctx, clear, true, true); err != nil {
			if spent(ctx.Err()) {
				log.Info().Int("announced", announced).Msg("Pending clears: pass budget spent with a backlog left; continuing next tick")
				return lastErr
			}
			log.Warn().Err(err).Str("alarm", st.AlarmToken).Msg("Announcing a pending clear failed; will retry next tick")
			lastErr = err
			continue
		}
		announced++
	}
	return lastErr
}

func (n *PolicyNotifier) announceClearIn(ctx context.Context, tenant string, clear *dmmodel.AlarmStateChangeEvent, cutoff bool) error {
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
		// A clear picked up later than its own message only goes through a policy saved before
		// it happened: a policy opted in afterwards must not announce what predates its opt-in.
		if cutoff && !clear.OccurredTime.IsZero() && p.ClearCutoff().After(clear.OccurredTime) {
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
	won, err := n.api.ClaimClearAnnouncement(ctx, clear.AlarmToken, state.CycleRaisedAt, claimedAt)
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
		key := model.PagedTarget{ChannelID: rule.Channel.ID, Channel: rule.Channel.Token, Recipients: recipients}.Key()
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
		out = append(out, model.PagedTarget{ChannelID: d.channel.ID, Channel: d.channel.Token, Recipients: d.recipients})
	}
	return out
}

// announceLateClear covers the clear that overtook the page it follows. The worker pool is
// unordered, so a CLEARED can be processed before the RAISED's delivery is recorded: it finds no
// paged targets and announces nothing. Once the page has recorded its targets, if the row is
// already stamped cleared and the clear is still unannounced, the announcement is owed now.
// Errors are logged, not returned: the page that led here has gone out, and returning would
// resend it. The scheduler (AnnouncePendingClears) picks up what this could not send.
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
	if err := n.announceClear(ctx, &clear, true, false); err != nil {
		tenant, _ := core.TenantFromContext(ctx)
		log.Error().Err(err).Str("tenant", tenant).Str("alarm", event.AlarmToken).
			Msg("Could not announce a clear that overtook its page; the scheduler will retry it")
	}
}
