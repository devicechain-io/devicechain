// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// EscalationScheduler periodically re-notifies open alarms that stay unresolved
// (ADR-017 N.D), and releases the notifications policies' quiet hours held once the quiet
// stretch has ended (PolicyNotifier.ReleaseHeld) — the same timed, every-replica,
// claim-before-send loop, because a release is driven by the passage of time too. The event-driven dispatcher (N.C) pages once on RAISED/ESCALATED; this
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
	nextTenant   atomic.Uint64
}

// NewEscalationScheduler builds the scheduler. interval is the tick period (the
// granularity at which escalation windows are checked); defaultMax is the service-wide
// escalation cap applied to a policy that does not set its own MaxEscalations.
func NewEscalationScheduler(ms *core.Microservice, api *model.Api, notifier *PolicyNotifier,
	interval time.Duration, defaultMax int, callbacks core.LifecycleCallbacks) *EscalationScheduler {
	s := &EscalationScheduler{Microservice: ms, Api: api, Notifier: notifier, defaultMax: defaultMax}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "escalation-scheduler", interval,
		s.runOnce, callbacks, ms.NewPeriodicTaskMetrics("escalation_scheduler"))
	return s
}

// escalationWorkers bounds concurrent tenants, never concurrent alarms of one tenant.
const escalationWorkers = 4

// tenantPass belongs to one worker for one tenant, and is never shared across jobs.
// Its deadline is an admission bound, not a context deadline: a claimed alarm must
// retain its entire dispatch budget. The memo expires with this job.
//
// The slice is WORK-CONSERVING: it only ends a tenant's job while another tenant of the
// same pass is still waiting for a worker (contended). A tenant alone with its backlog
// keeps going rather than idling until the next tick. A nil contended always enforces.
type tenantPass struct {
	deadline         time.Time
	contended        func() bool
	exhausted        bool
	timedOut         map[uint]bool
	suppressions     map[string]SuppressionStatus
	oldestDueSeconds float64
}

type tenantPassKey struct{}

func passFromContext(ctx context.Context) *tenantPass {
	p, _ := ctx.Value(tenantPassKey{}).(*tenantPass)
	return p
}

func sliceExpired(ctx context.Context) bool {
	p := passFromContext(ctx)
	if p != nil && !time.Now().Before(p.deadline) && (p.contended == nil || p.contended()) {
		p.exhausted = true
		return true
	}
	return false
}

type tenantOutcome struct {
	failed           bool
	partial          bool
	lastErr          error
	oldestDueSeconds float64
}

func (o *tenantOutcome) record(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, core.ErrPassPartial) {
		o.partial = true
	} else {
		o.failed = true
		o.lastErr = err
	}
}

// runOnce serves tenants on a bounded pool. Rotating admission order prevents a
// stable prefix of slow tenants from always occupying the first worker wave.
func (s *EscalationScheduler) runOnce(ctx context.Context) error {
	tenants, err := s.Api.DistinctStateTenants(core.WithSystemContext(ctx))
	if err != nil {
		return fmt.Errorf("listing tenants with notification state: %w", err)
	}
	holding, err := s.Api.DistinctDeferralTenants(core.WithSystemContext(ctx))
	if err != nil {
		return fmt.Errorf("listing tenants holding notifications for quiet hours: %w", err)
	}
	tenants = s.tenantOrder(unionSorted(tenants, holding))
	jobs := make(chan string)
	// waiting counts tenants of this pass not yet taken by a worker; the slice only
	// binds while it is non-zero (see tenantPass).
	var waiting atomic.Int64
	waiting.Store(int64(len(tenants)))
	contended := func() bool { return waiting.Load() > 0 }
	var workers sync.WaitGroup
	var mu sync.Mutex
	var visited, failed, partial int
	var lastErr error
	var oldestDueSeconds float64
	for i := 0; i < escalationWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for tenant := range jobs {
				waiting.Add(-1)
				outcome := s.serveTenant(ctx, tenant, contended)
				mu.Lock()
				visited++
				oldestDueSeconds = max(oldestDueSeconds, outcome.oldestDueSeconds)
				if outcome.failed {
					failed++
					lastErr = outcome.lastErr
				}
				if outcome.partial {
					partial++
				}
				mu.Unlock()
			}
		}()
	}
walk:
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			break walk
		case jobs <- tenant:
		}
	}
	close(jobs)
	workers.Wait()
	if s.Notifier.oldestDue != nil {
		s.Notifier.oldestDue.Set(oldestDueSeconds)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if failed == 0 && partial > 0 {
		return fmt.Errorf("%d of %d tenants not fully served: %w", partial, visited, core.ErrPassPartial)
	}
	return core.PassResult(visited, failed, lastErr)
}

func (s *EscalationScheduler) tenantOrder(tenants []string) []string {
	if len(tenants) == 0 {
		return tenants
	}
	offset := int((s.nextTenant.Add(1) - 1) % uint64(len(tenants)))
	return append(append(make([]string, 0, len(tenants)), tenants[offset:]...), tenants[:offset]...)
}

// serveTenant releases, escalates and picks up clears under this tenant's context.
// Only starting an alarm spends the slice; in-flight work keeps its own budget.
func (s *EscalationScheduler) serveTenant(ctx context.Context, tenant string, contended func() bool) (out tenantOutcome) {
	now := s.Notifier.clock()
	pass := &tenantPass{deadline: time.Now().Add(2 * s.Notifier.wholeDispatchBudget()),
		contended: contended, timedOut: make(map[uint]bool)}
	tctx := context.WithValue(core.WithTenant(ctx, tenant), tenantPassKey{}, pass)
	defer func() {
		out.oldestDueSeconds = pass.oldestDueSeconds
		if pass.exhausted {
			if s.Notifier.sliceExhausted != nil {
				s.Notifier.sliceExhausted.Inc()
			}
			log.Warn().Str("tenant", tenant).Msg("Notification tenant slice exhausted; unstarted alarms remain due")
		}
	}()
	policies, err := s.Api.EnabledNotificationPolicies(tctx)
	if err != nil {
		out.record(err)
		return
	}
	out.record(s.Notifier.ReleaseHeld(tctx, policies, now))
	if anyEscalationEnabled(policies) {
		states, err := s.Api.OpenNotificationStates(tctx)
		out.record(err)
		if err == nil {
			// Live suppression is asked in batches of the due escalations, and LAZILY: a batch
			// is looked up when the walk reaches its first alarm, so a tenant with thousands of
			// due escalations interleaves lookups with paging under the same slice check,
			// rather than spending its whole slice on lookups before the first page.
			var due []int
			if s.Notifier.SuppressionLookup != nil {
				for i, state := range states {
					if s.Notifier.escalationCandidate(state, policies, now, s.defaultMax) {
						due = append(due, i)
					}
				}
			}
			pass.suppressions = make(map[string]SuppressionStatus)
			nextDue := 0
			for i, state := range states {
				if err := ctx.Err(); err != nil {
					out.record(err)
					break
				}
				if sliceExpired(tctx) {
					out.record(core.ErrPassPartial)
					break
				}
				if nextDue < len(due) && due[nextDue] == i {
					if nextDue%suppressionBatch == 0 {
						end := min(nextDue+suppressionBatch, len(due))
						tokens := make([]string, 0, end-nextDue)
						for _, j := range due[nextDue:end] {
							tokens = append(tokens, states[j].AlarmToken)
						}
						for token, status := range s.Notifier.lookupSuppressions(tctx, tokens, now, 5*time.Second) {
							pass.suppressions[token] = status
						}
					}
					nextDue++
				}
				if err := s.Notifier.Escalate(tctx, state, policies, now, s.defaultMax); err != nil {
					log.Warn().Err(err).Str("tenant", tenant).Str("alarm", state.AlarmToken).
						Msg("Escalation scheduler: re-notification incomplete; will retry next tick")
					out.record(err)
				}
			}
		}
	}
	// Clears have their own bounded pickup budget, even when the page slice was
	// exhausted. They never wait for another tenant's backlog.
	out.record(s.Notifier.AnnouncePendingClears(tctx, policies, s.Notifier.clock()))
	return
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

// unionSorted merges two sorted, duplicate-free tenant lists.
func unionSorted(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j >= len(b) || (i < len(a) && a[i] < b[j]):
			out = append(out, a[i])
			i++
		case i >= len(a) || b[j] < a[i]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}
