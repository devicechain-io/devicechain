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
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
)

// retryBackoffBase is the base gap between per-channel delivery attempts; the nth
// retry waits n × base, so the dispatcher's own retry is bounded and short (the
// durable consumer, not this loop, is the reliability backstop for a longer outage).
const retryBackoffBase = 500 * time.Millisecond

// The budgets one alarm's dispatch is spent against.
//
// 🔴 THE PER-ATTEMPT TIMEOUT IS NOT A WHOLE-DISPATCH BOUND, AND READING IT AS ONE COST US
// A DUPLICATE PAGE. The config's DeliverySeconds bounds a SINGLE adapter call; a channel
// then gets DeliveryAttempts of them with a backoff between each, and dispatch walks the
// planned channels SEQUENTIALLY. At the shipped defaults that retry loop alone was
// 3 × 10s + 1.5s of backoff = 31.5s per channel, so two slow-but-alive channels on one
// alarm spent 63s against the consumer's 60s AckWait — and the secret lookup in front of
// each of them was not bounded at all. JetStream redelivered a message a worker was still
// working, a second worker re-sent every channel from the top, and a human was paged
// twice. Nothing capped the walk: plan() emits as many deliveries as the tenant's policies
// match.
//
// So the bound is enforced here, as a deadline on the dispatch, rather than asserted in
// prose about the per-attempt number. dispatch and Escalate both derive their context from
// dispatchBudget and deliverAll stops walking channels once it is spent. perChannelWorstCase
// is the post-fix unit the budget is SIZED against (36.5s at the defaults, the secret
// lookup now included); the 31.5s above is what the defect actually cost, and the two are
// different numbers on purpose.
const (
	// secretResolveTimeout bounds one channel's delivery-secret lookup (ADR-059). It is
	// separate from the per-attempt delivery timeout because it is different work — a
	// local envelope decrypt against this service's own database, not a call to a
	// tenant-supplied endpoint — and because the whole-dispatch budget has to afford BOTH
	// for every channel it plans.
	secretResolveTimeout = 5 * time.Second

	// dispatchMargin is the headroom deliberately left between the whole-dispatch budget
	// and messaging.AckWait. It has to cover everything that happens to a message OUTSIDE
	// the budgeted section and still leave the broker's ack window unspent: the ADR-077
	// lifecycle gate's cold-cache fetch to user-management (bounded by governance's own
	// fetch timeout), the post-delivery bookkeeping write (recordTimeout, which is
	// deliberately smaller than this margin), and the ack round trip.
	dispatchMargin = 20 * time.Second

	// dispatchBudget bounds ONE alarm's whole dispatch — policy load, state load, and
	// every planned channel's secret resolve and delivery attempts together. It is
	// DERIVED from messaging.AckWait rather than copied from it, because AckWait is
	// exported precisely so callers stop copying the number (see its comment): if the
	// platform's ack window ever moves, this budget moves with it.
	dispatchBudget = messaging.AckWait - dispatchMargin

	// recordTimeout bounds a state write that is NOT part of the delivery budget: the
	// post-delivery RecordNotification, and the three lifecycle stamps in applyLifecycle.
	// The first is run off the PARENT context on purpose — a dispatch that spent its whole
	// budget delivering must still be able to record that it delivered, or the alarm never
	// enters the escalation substrate at all.
	recordTimeout = 5 * time.Second
)

// perChannelWorstCase is the longest ONE channel's delivery can take: its secret resolve,
// then every attempt at the full per-attempt timeout, plus the linear backoffs between
// them.
//
// It is a function rather than a sentence in a comment because the sentence was there,
// and it was wrong — it described this quantity as if it bounded the whole dispatch. The
// relation between it, dispatchBudget and messaging.AckWait is pinned in
// TestDispatchBudgetFitsUnderAckWait so a change to any of the three constants fails for
// the stated reason instead of silently re-opening the duplicate-page path.
func perChannelWorstCase(attempts int, timeout time.Duration) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	total := secretResolveTimeout + time.Duration(attempts)*timeout
	for i := 1; i < attempts; i++ {
		total += time.Duration(i) * retryBackoffBase
	}
	return total
}

// PolicyNotifier is the real dispatch Notifier (ADR-017 N.C) that replaces the
// first-slice LogNotifier: for each alarm transition it evaluates the tenant's
// notification policies, delivers matching alarms through the configured channels
// (SMTP, webhook), and maintains the per-alarm NotificationState the escalation
// scheduler (N.D) reads. It satisfies the Notifier contract (notifier.go) by owning a
// bounded per-channel retry loop rather than leaning on the processor's crude
// redelivery, and by returning an error to the processor ONLY when nothing was
// attempted (so a redelivery can never double-send a channel that already
// succeeded).
type PolicyNotifier struct {
	api      *model.Api
	store    secrets.SecretStore
	adapters map[string]ChannelAdapter
	attempts int
	timeout  time.Duration

	// budget is the whole-dispatch deadline one alarm is worth, defaulted from
	// dispatchBudget by the constructor. It is a FIELD rather than the constant read
	// directly because a mutation run showed the constant made an entire input class
	// unreachable: with a 40-second budget, nothing can exercise "the budget is already
	// spent when the deliveries finish", which is the exact state the post-delivery
	// bookkeeping has to survive. Zero means the constant (this struct is also built by
	// literal in tests) — read it through wholeDispatchBudget, never directly.
	budget time.Duration

	// TenantDeleted reports whether a tenant has been through the ADR-077 delete door.
	// A closure rather than the governance resolver type so this package needs no live
	// user-management to test. MAY BE NIL — read it through tenantDeleted, never
	// directly: the test fixtures in this package build a PolicyNotifier by struct
	// literal, so the constructor is not the only way one comes into existence.
	TenantDeleted func(tenant string) bool
}

// NewPolicyNotifier builds the dispatcher over the persistence API and the secret
// store (ADR-059, from which each channel's delivery secret is resolved server-
// internal at delivery time), with attempts per-channel delivery tries and timeout
// bounding a single attempt. tenantDeleted is the ADR-077 lifecycle gate (nil disables
// the refusal).
func NewPolicyNotifier(api *model.Api, store secrets.SecretStore, attempts int, timeout time.Duration,
	tenantDeleted func(string) bool, guard *egress.Guard) *PolicyNotifier {
	if attempts < 1 {
		attempts = 1
	}
	// An operator can configure a retry policy a single channel cannot finish inside the
	// whole-dispatch budget. That is not unsafe — the budget still stops the overlap — but
	// it means the configured attempts are not all reachable, which is worth saying once at
	// startup rather than leaving as a silent truncation at 3am.
	if worst := perChannelWorstCase(attempts, timeout); worst > dispatchBudget {
		log.Warn().Dur("perChannelWorstCase", worst).Dur("dispatchBudget", dispatchBudget).
			Msg("The configured delivery attempts and timeout cannot all be spent on one channel " +
				"inside the whole-dispatch budget; a channel's last retries will be cut short.")
	}
	return &PolicyNotifier{
		api:           api,
		store:         store,
		adapters:      newAdapterRegistry(guard),
		attempts:      attempts,
		timeout:       timeout,
		budget:        dispatchBudget,
		TenantDeleted: tenantDeleted,
	}
}

// wholeDispatchBudget is the deadline one alarm's dispatch runs under, falling back to
// the platform-derived constant when the field is unset — which is how every struct-literal
// construction in this package's tests comes into existence.
func (n *PolicyNotifier) wholeDispatchBudget() time.Duration {
	if n.budget > 0 {
		return n.budget
	}
	return dispatchBudget
}

// tenantDeleted reads the ADR-077 lifecycle gate, treating an unconfigured gate as "not
// deleted". Every consultation goes through here so the nil check and the call cannot be
// separated — the field is exported and built by literal in tests, so a direct call is a
// nil-dereference waiting for a deployment without user-management configured.
func (n *PolicyNotifier) tenantDeleted(ctx context.Context) bool {
	if n.TenantDeleted == nil {
		return false
	}
	tenant, ok := core.TenantFromContext(ctx)
	// A context with no tenant cannot be attributed, so it cannot be refused. Both entry
	// points scope their context before calling (the consumer from the message subject,
	// the scheduler from its own tenant list), so this is unreachable rather than lenient
	// — but the alternative, refusing everything an unscoped context carries, would turn a
	// missing scope into a silent instance-wide notification outage.
	return ok && n.TenantDeleted(tenant)
}

// Notify routes one alarm transition. RAISED/ESCALATED are delivered through the
// matching channels; the lifecycle transitions only update the per-alarm state so the
// escalation scheduler (N.D) can stop or re-tier — none of them page. A returned
// error means the transition was not applied and is safe to redeliver (no delivery
// was attempted); a delivery failure is handled inside dispatch and never surfaced,
// so the processor acks.
//
// Every branch goes through a guarded helper — dispatch for the two that page,
// applyLifecycle for the three that only write. That symmetry is the fix for a real gap:
// the three write-only branches used to call straight through to the api, so they were
// the one door into this service that neither consulted the ADR-077 lifecycle gate nor
// recognized the erasure fence's refusal.
func (n *PolicyNotifier) Notify(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	switch event.EventType {
	case dmmodel.AlarmEventAcknowledged:
		// Idempotent tombstone upsert; a DB error is safe to retry (no side effects).
		return n.applyLifecycle(ctx, event.AlarmToken, "acknowledgement", func(c context.Context) error {
			return n.api.MarkAcknowledged(c, event.AlarmToken, event.AlarmKey, event.Severity, event.OccurredTime)
		})
	case dmmodel.AlarmEventCleared:
		return n.applyLifecycle(ctx, event.AlarmToken, "clear", func(c context.Context) error {
			return n.api.MarkCleared(c, event.AlarmToken, event.AlarmKey, event.Severity, event.OccurredTime)
		})
	case dmmodel.AlarmEventDeescalated:
		return n.applyLifecycle(ctx, event.AlarmToken, "de-escalation", func(c context.Context) error {
			return n.api.TouchSeverity(c, event.AlarmToken, event.Severity)
		})
	case dmmodel.AlarmEventRaised, dmmodel.AlarmEventEscalated:
		return n.dispatch(ctx, event)
	default:
		log.Warn().Str("eventType", event.EventType.String()).Str("alarm", event.AlarmToken).
			Msg("Ignoring unknown alarm event type")
		return nil
	}
}

// applyLifecycle runs one of the three state-only transitions (acknowledge, clear,
// de-escalate) under the two guards the paging path already had. Neither guard is about
// paging, which is why "these branches do not notify anyone" was never a reason to skip
// them: both are about WRITING for a tenant.
//
// 🔴 THE ADR-077 GATE BELONGS HERE. Each of these transitions is an upsert on this
// service's own tenant-scoped table — markTerminal CREATES a row when none exists — so a
// deleted tenant's late CLEARED wrote a fresh row into a schema the purge sweep was in
// the middle of erasing. That is the same failure Escalate refuses above ClaimEscalation
// (a store that erases rows loses its clean-since, so the settle window restarts and the
// purge cannot complete), reached through a different door.
//
// 🔴 AND ErrTenantPurged IS PERMANENT, NOT TRANSIENT. The gate above fails open by design
// — 60s TTL, and it goes blind the moment completion removes the tenant row — so the
// erasure FENCE is what actually refuses the write. That refusal used to reach the
// processor as an ordinary error, which the processor reads as retryable: five
// redeliveries, AckWait apart, refused identically every time, and then a dead letter
// claiming an alarm reached nobody — for a tenant there is nobody left to page for. Ack
// and drop instead. The row this write would have stamped is one the sweep is erasing.
//
// The write also gets a bounded context. It had none: the consumer's workers run on
// context.Background(), so a stalled database held a worker on a state stamp with nothing
// to stop it, which the Notifier contract's "bound its own duration" clause forbids.
func (n *PolicyNotifier) applyLifecycle(ctx context.Context, alarm, transition string,
	write func(context.Context) error) error {
	if n.tenantDeleted(ctx) {
		log.Debug().Str("alarm", alarm).Str("transition", transition).
			Msg("Not recording an alarm transition: the tenant has been deleted.")
		return nil
	}
	wctx, cancel := context.WithTimeout(ctx, recordTimeout)
	defer cancel()
	err := write(wctx)
	if errors.Is(err, rdb.ErrTenantPurged) {
		log.Info().Str("alarm", alarm).Str("transition", transition).
			Msg("Dropping an alarm transition for a deleted tenant; this area's notification " +
				"state has been erased, so there is nothing left to stamp and no attempt that " +
				"could succeed.")
		return nil
	}
	return err
}

// dispatch evaluates policies and delivers a RAISED/ESCALATED transition.
func (n *PolicyNotifier) dispatch(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	tenant, _ := core.TenantFromContext(ctx)

	// ADR-077: a deleted tenant is not paged. Refused here, at the top, rather than left
	// to the funnel in deliverWithRetry, because everything below this line either queries
	// or writes on the tenant's behalf — and returning nil is what tells the durable
	// consumer to ACK the event. Refusing further down would leave it unacked and churn
	// the redelivery cap for a tenant that will never be notified.
	if n.tenantDeleted(ctx) {
		log.Debug().Str("tenant", tenant).Str("alarm", event.AlarmToken).
			Msg("Not notifying: the tenant has been deleted.")
		return nil
	}

	// 🔴 THE WHOLE-DISPATCH DEADLINE, and it starts HERE rather than around the delivery
	// loop alone: the policy and state queries are work this dispatch does on the same
	// message, so they belong inside the same budget the broker is timing. See
	// dispatchBudget — this is the mechanism that makes "the dispatch finishes inside
	// AckWait" true, instead of a claim about the per-attempt timeout that never was.
	dctx, cancel := context.WithTimeout(ctx, n.wholeDispatchBudget())
	defer cancel()

	policies, err := n.api.EnabledNotificationPolicies(dctx)
	if err != nil {
		// Nothing attempted yet: safe to leave unacked for redelivery.
		return fmt.Errorf("loading notification policies: %w", err)
	}
	if len(policies) == 0 {
		return nil
	}

	// Load the current per-alarm state once as the throttle basis.
	states, err := n.api.NotificationStatesByAlarmToken(dctx, []string{event.AlarmToken})
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

	tally := n.deliverAll(dctx, deliveries, renderNotification(event), event.AlarmToken)

	// Nothing delivered though targets existed: return an error so the durable consumer
	// redelivers (its own retry is the reliability backstop for an outage longer than
	// this dispatcher's in-line retry). This is double-send-safe precisely because NO
	// channel succeeded — there is nothing to re-send twice. Once any channel succeeds
	// we must ack (return nil) so redelivery can't double-send the ones that worked.
	//
	// 🔴 Unless every target was REFUSED rather than failed. Redelivery exists to ride
	// out an endpoint being down; it cannot make a private address public. Returning an
	// error here would churn the redelivery budget to reach the same refusal and then
	// dead-letter it as an ordinary exhausted delivery — which tells an operator the
	// endpoint is unreachable when the truth is that the channel is pointed somewhere
	// this platform will not go. A mix still redelivers, because the failed ones deserve
	// the retry the refused ones do not — and so does a channel the budget never let us
	// try, which is why unattempted counts against the ack too.
	if tally.delivered == 0 {
		if tally.retryable() == 0 && tally.refused > 0 {
			log.Error().Str("tenant", tenant).Str("alarm", event.AlarmToken).Int("channels", tally.refused).
				Msg("Every channel for this alarm points at an address outbound traffic is not " +
					"permitted to reach; acking rather than redelivering, because redelivery cannot change that.")
			return nil
		}
		return fmt.Errorf("all %d channel deliveries failed for alarm %q", len(deliveries), event.AlarmToken)
	}

	// At least one channel delivered: record the notification so the throttle/escalation
	// state reflects it. A failure here is logged, not left for redelivery — the sends
	// already happened, and redelivery would double-send them.
	//
	// 🔑 IT RUNS OFF THE PARENT CONTEXT, NOT dctx, ON PURPOSE. A dispatch that spent its
	// whole budget delivering would otherwise find its own budget already expired at the
	// one write that has to happen: with no state row the alarm never enters the
	// escalation substrate, so the page that just went out would never be followed up.
	// recordTimeout is smaller than dispatchMargin so this still lands inside AckWait.
	rctx, rcancel := context.WithTimeout(ctx, recordTimeout)
	defer rcancel()
	if err := n.api.RecordNotification(rctx, event.AlarmToken, event.AlarmKey, event.Severity, event.OccurredTime); err != nil {
		log.Error().Err(err).Str("tenant", tenant).Str("alarm", event.AlarmToken).
			Msg("Delivered notification but failed to record notification state")
	}
	return nil
}

// dispatchTally counts how one alarm's planned deliveries ended. delivered/refused/failed
// mirror deliveryResult; unattempted is the channels the whole-dispatch budget ran out
// before reaching.
//
// unattempted is kept APART from failed rather than folded into it because the two say
// different things to an operator reading the log: "we tried and the endpoint would not
// take it" versus "we never got to this channel". They agree on disposition — both want
// the redelivery — which is what retryable() expresses, and the fold is written once
// there so a later reader cannot accidentally make only one of them count.
type dispatchTally struct {
	delivered   int
	refused     int
	failed      int
	unattempted int
}

// retryable is the count of outcomes a redelivery could still turn into a page. A refusal
// is not one of them: no number of retries makes an address the egress boundary blocks
// reachable.
func (t dispatchTally) retryable() int { return t.failed + t.unattempted }

// deliverAll sends one rendered notification to every planned delivery, in order, under
// ONE shared budget — the caller's ctx, which both entry points derive from
// dispatchBudget.
//
// 🔴 THE BUDGET CHECK AT THE TOP OF THE LOOP IS THE FIX, not a tidy-up. This walk is
// sequential and used to be uncapped, so N slow-but-alive channels cost N times one
// channel's worst case; at the shipped defaults two of them exceeded the consumer's
// AckWait, the broker handed the message to a second worker while the first was still
// sending, and that worker started again at channel one. Someone got paged twice. Stopping
// here means the dispatch gives up BEFORE the broker decides it has, so a redelivery is
// something we asked for rather than something that happened underneath us.
func (n *PolicyNotifier) deliverAll(ctx context.Context, deliveries []delivery,
	rendered *RenderedNotification, alarm string) dispatchTally {
	tenant, _ := core.TenantFromContext(ctx)
	var tally dispatchTally
	for i, d := range deliveries {
		if ctx.Err() != nil {
			tally.unattempted = len(deliveries) - i
			log.Error().Str("tenant", tenant).Str("alarm", alarm).
				Int("channels", tally.unattempted).Dur("budget", n.wholeDispatchBudget()).
				Msg("The whole-dispatch budget was spent before every channel had been tried; " +
					"the rest were not attempted. They are counted as owed a retry, so unless " +
					"another channel already delivered this alarm is left for redelivery.")
			break
		}
		secret, err := n.resolveChannelSecret(ctx, d.channel.ID)
		if err != nil {
			// A store error (not "no secret") is transient infra: this channel FAILED. It is
			// counted, and counting it is the point — it used to `continue` without being
			// counted anywhere, so a plan of one secret-error channel plus one refused channel
			// looked like "every target was refused" and got acked, silently dropping the
			// retry the store error had earned.
			log.Error().Err(err).Str("tenant", tenant).Str("channel", d.channel.Token).
				Msg("Skipping channel: failed to resolve delivery secret")
			tally.failed++
			continue
		}
		d.secret = secret
		switch n.deliverWithRetry(ctx, d, rendered) {
		case deliveryOK:
			tally.delivered++
		case deliveryRefused:
			tally.refused++
		default:
			tally.failed++
		}
	}
	return tally
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
	// ADR-077: a deleted tenant's open alarms stop escalating.
	//
	// This check has to be HERE, above ClaimEscalation, and not only at the funnel below.
	// ClaimEscalation WRITES — it advances the alarm's escalation tier — so a refusal
	// further down would still have dirtied a table the purge is sweeping. A store that
	// erases rows loses its clean-since and the settle window restarts, so a deleted
	// tenant with an open alarm would hold its own purge open on every scheduler tick.
	// It would also fail forever: the claim succeeds, nothing delivers, and the error
	// return brings the scheduler back to the same state on the next tick.
	if n.tenantDeleted(ctx) {
		log.Debug().Str("alarm", state.AlarmToken).
			Msg("Not escalating: the tenant has been deleted.")
		return nil
	}

	deliveries := n.planEscalation(state, policies, now, defaultMax)
	if len(deliveries) == 0 {
		return nil
	}
	// The same whole-dispatch budget the event-driven path uses, for a different reason.
	// There is no AckWait here — the scheduler is a timer, not a consumer — but its tick
	// walks EVERY tenant and every open alarm on ONE goroutine, so an unbounded
	// re-notification is an outage for every tenant behind it in the loop, not just for
	// this alarm. Bounding both paths identically also means a channel cannot be slow
	// enough to matter on one and not the other.
	ectx, cancel := context.WithTimeout(ctx, n.wholeDispatchBudget())
	defer cancel()

	claimed, err := n.api.ClaimEscalation(ectx, state.AlarmToken, state.EscalationLevel, now)
	if err != nil {
		// 🔴 A PURGED TENANT IS TERMINAL HERE TOO. ClaimEscalation is an UPDATE, so the
		// ADR-077 erasure fence refuses it — and this is the path the gate above is least
		// able to catch, because the gate goes blind exactly when the purge completes. The
		// scheduler retries a returned error on every tick, so surfacing the refusal would
		// log a failure once a minute, per open alarm, until the sweep removed the rows.
		// There is nothing to claim and no tier worth advancing: report success and let
		// the sweep finish.
		if errors.Is(err, rdb.ErrTenantPurged) {
			log.Info().Str("alarm", state.AlarmToken).
				Msg("Not escalating: the tenant has been deleted and this area's notification " +
					"state has been erased.")
			return nil
		}
		return fmt.Errorf("claiming escalation tier for alarm %q: %w", state.AlarmToken, err)
	}
	if !claimed {
		// Another replica escalated this tier first, or the alarm resolved between the
		// scheduler's read and here. Either way we must not deliver.
		return nil
	}
	tally := n.deliverAll(ectx, deliveries, renderEscalation(state, state.EscalationLevel+1), state.AlarmToken)
	if tally.delivered == 0 {
		// Same reasoning as dispatch: a refusal is not an outage, so redelivering it only
		// delays the same answer. The tier has already been claimed either way.
		if tally.retryable() == 0 && tally.refused > 0 {
			log.Error().Str("alarm", state.AlarmToken).Int("channels", tally.refused).
				Msg("Every escalation channel points at an address outbound traffic is not " +
					"permitted to reach; acking rather than redelivering.")
			return nil
		}
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

// deliveryResult says how one delivery ended.
//
// It replaced a bool because "did not deliver" turned out to be two facts with OPPOSITE
// dispositions, and collapsing them meant the caller could only pick one. A transient
// failure should be redelivered by the durable consumer; a destination the egress
// boundary refuses should not, because no amount of redelivery makes an address public.
// With a bool, refusing a channel still returned "not delivered", the caller still
// returned an error, and the event still churned the redelivery budget and died as an
// ordinary exhausted delivery — the exact operator-facing outcome the refusal exists to
// replace, one layer up from where it was fixed.
type deliveryResult int

const (
	// deliveryOK — the adapter accepted it.
	deliveryOK deliveryResult = iota
	// deliveryFailed — a transient failure worth redelivering.
	deliveryFailed
	// deliveryRefused — the destination is one this platform will not connect to.
	// Terminal by construction.
	deliveryRefused
)

// deliverWithRetry sends one delivery, retrying up to n.attempts with a short linear
// backoff and bounding each attempt by n.timeout. The adapter, not the processor, owns
// retry here (Notifier contract), so a final failure is logged and dropped rather than
// left for redelivery — a redelivery would resend the whole event and double-send the
// channels that already succeeded.
func (n *PolicyNotifier) deliverWithRetry(ctx context.Context, d delivery, rendered *RenderedNotification) deliveryResult {
	// ADR-077, and this one is the GUARANTEE rather than the disposition.
	//
	// dispatch and Escalate both refuse a deleted tenant above, and today they are the
	// only two callers — so on every path that exists this check is unreachable. It is
	// here anyway because this is the single function through which every notification
	// this service has ever sent passes, and an email or a webhook POST cannot be
	// un-sent. A third caller added later inherits the refusal instead of having to
	// remember it, which is the only property worth buying with a redundant check.
	//
	// Its own test therefore drives it directly rather than through a caller: a
	// backstop reached only via paths that already refuse cannot be observed failing.
	if n.tenantDeleted(ctx) {
		log.Warn().Str("channel", d.channel.Token).
			Msg("Refusing to deliver a notification for a deleted tenant; this should have been refused upstream.")
		// Refused, not failed: a deleted tenant does not come back, so redelivering this
		// would churn the budget to reach the same answer.
		return deliveryRefused
	}

	adapter := n.adapters[d.channel.ChannelType]
	for attempt := 1; attempt <= n.attempts; attempt++ {
		dctx, cancel := context.WithTimeout(ctx, n.timeout)
		err := adapter.Deliver(dctx, d.channel, d.secret, d.recipients, rendered)
		cancel()
		if err == nil {
			return deliveryOK
		}
		// A destination the egress boundary refused is terminal: retrying cannot make an
		// address public, so the remaining attempts would be pure delay in front of the
		// same answer, and the log line an operator eventually reads would say
		// "exhausted attempts" when the truth is "this channel points somewhere it may
		// not go". Stop on the first one and say so.
		if errors.Is(err, egress.ErrBlocked) {
			log.Error().Err(err).Str("channel", d.channel.Token).Str("type", d.channel.ChannelType).
				Msg("Notification channel points at an address outbound traffic is not permitted to reach; not retrying.")
			return deliveryRefused
		}
		log.Warn().Err(err).Str("channel", d.channel.Token).Str("type", d.channel.ChannelType).
			Int("attempt", attempt).Int("attempts", n.attempts).Msg("Notification delivery attempt failed")
		if attempt < n.attempts {
			select {
			case <-ctx.Done():
				return deliveryFailed
			case <-time.After(time.Duration(attempt) * retryBackoffBase):
			}
		}
	}
	log.Error().Str("channel", d.channel.Token).Str("type", d.channel.ChannelType).
		Int("attempts", n.attempts).Msg("Notification permanently dropped for channel after exhausting attempts")
	return deliveryFailed
}

// resolveChannelSecret returns the channel's delivery secret from the store, keyed
// by the tenant-scoped channel handle (ADR-059), under its OWN bounded timeout. A ref for
// which no secret is stored yields an empty string (the channel simply has no secret)
// rather than an error, so a secretless channel delivers normally; a genuine store error
// is returned so the caller can treat it as a transient delivery failure and let
// redelivery retry.
//
// 🔴 THE TIMEOUT IS LOAD-BEARING. This ran on whatever context the caller held, and for
// the durable consumer that is a worker's context.Background(), which never fires — while
// the adapter call one line later was deadline-wrapped. So the ONE step in a delivery that
// had no deadline was the one that talks to the database: a hung secret store stalled a
// dispatch worker indefinitely, one of five, and graceful shutdown behind it. It is
// bounded here rather than left to the whole-dispatch budget so that one wedged lookup
// costs its own channel and not every channel after it.
func (n *PolicyNotifier) resolveChannelSecret(ctx context.Context, channelID uint) (string, error) {
	ref, err := model.ChannelSecretRef(ctx, channelID)
	if err != nil {
		return "", err
	}
	sctx, cancel := context.WithTimeout(ctx, secretResolveTimeout)
	defer cancel()
	value, err := n.store.Resolve(sctx, ref)
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
