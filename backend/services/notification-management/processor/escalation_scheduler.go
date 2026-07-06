// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
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
	Microservice *core.Microservice
	Api          *model.Api
	Notifier     *PolicyNotifier
	interval     time.Duration
	defaultMax   int

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
	lifecycle  core.LifecycleManager
}

// NewEscalationScheduler builds the scheduler. interval is the tick period (the
// granularity at which escalation windows are checked); defaultMax is the service-wide
// escalation cap applied to a policy that does not set its own MaxEscalations.
func NewEscalationScheduler(ms *core.Microservice, api *model.Api, notifier *PolicyNotifier,
	interval time.Duration, defaultMax int, callbacks core.LifecycleCallbacks) *EscalationScheduler {
	s := &EscalationScheduler{
		Microservice: ms, Api: api, Notifier: notifier, interval: interval, defaultMax: defaultMax,
	}
	s.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-escalation-scheduler", ms.FunctionalArea), s, callbacks)
	return s
}

func (s *EscalationScheduler) Initialize(ctx context.Context) error {
	return s.lifecycle.Initialize(ctx)
}

func (s *EscalationScheduler) ExecuteInitialize(ctx context.Context) error {
	s.procCtx, s.procCancel = context.WithCancel(ctx)
	return nil
}

func (s *EscalationScheduler) Start(ctx context.Context) error {
	return s.lifecycle.Start(ctx)
}

func (s *EscalationScheduler) ExecuteStart(context.Context) error {
	s.wg.Add(1)
	go s.loop()
	return nil
}

func (s *EscalationScheduler) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.procCtx.Done():
			return
		case <-ticker.C:
			s.runOnce(s.procCtx)
		}
	}
}

func (s *EscalationScheduler) Stop(ctx context.Context) error {
	return s.lifecycle.Stop(ctx)
}

func (s *EscalationScheduler) ExecuteStop(context.Context) error {
	if s.procCancel != nil {
		s.procCancel()
	}
	s.wg.Wait()
	return nil
}

func (s *EscalationScheduler) Terminate(ctx context.Context) error {
	return s.lifecycle.Terminate(ctx)
}

func (s *EscalationScheduler) ExecuteTerminate(context.Context) error {
	return nil
}

// runOnce re-notifies every tenant's due open alarms. It loads each tenant's enabled
// policies once and, only when at least one has escalation enabled, its open alarm
// states — so a tenant that uses no escalation costs one policy query and no per-alarm
// work.
func (s *EscalationScheduler) runOnce(ctx context.Context) {
	now := time.Now()
	tenants, err := s.Api.DistinctStateTenants(core.WithSystemContext(ctx))
	if err != nil {
		log.Error().Err(err).Msg("Escalation scheduler: failed to list tenants")
		return
	}
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return
		default:
		}
		tctx := core.WithTenant(ctx, tenant)
		policies, err := s.Api.EnabledNotificationPolicies(tctx)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Escalation scheduler: failed to load policies")
			continue
		}
		if !anyEscalationEnabled(policies) {
			continue
		}
		states, err := s.Api.OpenNotificationStates(tctx)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Escalation scheduler: failed to load open states")
			continue
		}
		for _, state := range states {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := s.Notifier.Escalate(tctx, state, policies, now, s.defaultMax); err != nil {
				log.Warn().Err(err).Str("tenant", tenant).Str("alarm", state.AlarmToken).
					Msg("Escalation scheduler: re-notification failed; will retry next tick")
			}
		}
	}
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
