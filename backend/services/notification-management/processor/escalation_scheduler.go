// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// EscalationScheduler periodically re-notifies open alarms that stay unresolved
// (ADR-017 N.D). The event-driven dispatcher (N.C) pages once on RAISED/ESCALATED; this
// scheduler is the timed follow-up: on each tick it re-evaluates every tenant's
// escalation-enabled policies against the per-alarm NotificationState the dispatcher
// maintains, and re-pages any alarm whose escalation window has elapsed without an
// acknowledgement or clear — up to a bounded number of tiers.
//
// It is a scheduled loop rather than an event consumer because escalation is driven by
// the PASSAGE of time (no new alarm event fires while an alarm sits unacknowledged), and
// it reads the local projection rather than the alarm's live state in device-management:
// the projection is kept race-consistent by the terminal-tombstone upsert
// (Api.markTerminal), and every escalation is capped (effectiveMaxEscalations), so even a
// terminal event lost past the consumer's redelivery cap can only re-page a bounded
// number of times. A fully-authoritative cross-service live re-verify (ADR-044 svcclient
// → device-management alarm-state-by-token) is a deferred hardening, not needed for a
// bounded, race-consistent loop.
//
// Like the retention sweep it is cross-tenant maintenance: it lists tenants under a
// system context, then does all per-tenant reads and deliveries under that tenant's
// context so the rdb tenant-scope predicate keeps every query and state write tenant-local.
//
// Unlike the durable consumer, this timer loop runs in EVERY replica (and in both pods
// during a rolling update), so it does not get JetStream's one-of-N delivery for free.
// It stays single-delivery per tier by claiming each escalation before sending
// (PolicyNotifier.Escalate → Api.ClaimEscalation, an atomic compare-and-swap on the
// tier): overlapping pods race on the claim and exactly one wins, so no operator is paged
// twice. No leader election is required.
type EscalationScheduler struct {
	// The embedded task supplies the lifecycle octet, the schedule and the pass metrics.
	*core.PeriodicTask

	Microservice *core.Microservice
	Api          *model.Api
	Notifier     *PolicyNotifier
	defaultMax   int
}

// NewEscalationScheduler builds the scheduler. interval is the tick period (the
// granularity at which escalation windows are checked); defaultMax is the service-wide
// escalation cap applied to a policy that does not set its own MaxEscalations.
func NewEscalationScheduler(ms *core.Microservice, api *model.Api, notifier *PolicyNotifier,
	interval time.Duration, defaultMax int, callbacks core.LifecycleCallbacks) *EscalationScheduler {
	s := &EscalationScheduler{Microservice: ms, Api: api, Notifier: notifier, defaultMax: defaultMax}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "escalation-scheduler", interval,
		s.runOnce, callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("escalation_scheduler")))
	return s
}

// runOnce re-notifies every tenant's due open alarms. It loads each tenant's enabled
// policies once and, only when at least one has escalation enabled, its open alarm
// states — so a tenant that uses no escalation costs one policy query and no per-alarm
// work.
func (s *EscalationScheduler) runOnce(ctx context.Context) error {
	now := time.Now()
	tenants, err := s.Api.DistinctStateTenants(core.WithSystemContext(ctx))
	if err != nil {
		return fmt.Errorf("listing tenants with notification state: %w", err)
	}
	var visited, failed int
	var lastErr error
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		visited++
		tctx := core.WithTenant(ctx, tenant)
		policies, err := s.Api.EnabledNotificationPolicies(tctx)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Escalation scheduler: failed to load policies")
			failed++
			lastErr = err
			continue
		}
		if !anyEscalationEnabled(policies) {
			continue
		}
		states, err := s.Api.OpenNotificationStates(tctx)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Escalation scheduler: failed to load open states")
			failed++
			lastErr = err
			continue
		}
		// 🔑 A TENANT WHOSE RE-NOTIFICATIONS FAILED IS A TENANT THAT WAS NOT SERVED, and it
		// counts. Tallying only the tenant ENUMERATION would report a pass that reached
		// every tenant and escalated nothing as complete — moving the last-success gauge
		// while no operator was paged about anything. That is the false-healthy this whole
		// change exists to remove, one layer further in.
		escalationFailed := false
		for _, state := range states {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if err := s.Notifier.Escalate(tctx, state, policies, now, s.defaultMax); err != nil {
				log.Warn().Err(err).Str("tenant", tenant).Str("alarm", state.AlarmToken).
					Msg("Escalation scheduler: re-notification failed; will retry next tick")
				escalationFailed = true
				lastErr = err
			}
		}
		if escalationFailed {
			failed++
		}
	}
	return core.PassResult(visited, failed, lastErr)
}

// anyEscalationEnabled reports whether any policy has escalation configured, so the
// scheduler can skip loading open states for a tenant that never escalates.
func anyEscalationEnabled(policies []*model.NotificationPolicy) bool {
	for _, p := range policies {
		if p.EscalateAfterSeconds.Valid && p.EscalateAfterSeconds.Int64 > 0 {
			return true
		}
	}
	return false
}
