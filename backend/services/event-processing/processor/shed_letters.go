// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// shedSummaryWindow is how often the over-budget shed counts are summarised, one letter per
// tenant per window.
const shedSummaryWindow = time.Minute

// shedFlushTimeout bounds the summary flush Stop makes on its way out.
const shedFlushTimeout = 10 * time.Second

// ShedLetterBudget bounds the dead letters REACT writes about connector actions its source gate
// shed: a per-tenant budget and one across every tenant. main passes the typed configuration's
// values, which ApplyDefaults has already filled (config.DefaultShedLetterPerSecond and friends).
// A zero budget is NOT defaulted here: it letters nothing, and every shed is counted and
// summarised instead.
type ShedLetterBudget struct {
	PerTenantPerSecond float64
	PerTenantBurst     int
	GlobalPerSecond    float64
	GlobalBurst        int
}

// shedLetterer records each connector action REACT's source gate shed as a dead letter with reason
// shed, so a shed is something an operator can inspect rather than only a counter.
//
// 🔴 IT IS BUDGETED, AND THE BUDGET IS THE POINT. A shed means a tenant is over its outbound
// ceiling, which is exactly when it is producing actions fastest; a letter per shed would copy the
// flood onto the dead-letter stream. So each letter is charged against a per-tenant budget and a
// global one. A shed over either is counted on react_connector_shed_unlettered_total and folded
// into ONE summary letter per tenant per shedSummaryWindow, so nothing goes unrecorded — it is
// only recorded in aggregate.
//
// Letters are written synchronously on REACT's goroutine; the global budget bounds that to
// GlobalPerSecond writes a second in steady state.
type shedLetterer struct {
	dead *deadletter.Sink
	// perTenant is a core limiter over a static ceiling. Every tenant here comes from REACT's own
	// derived-events subject, which only the platform writes, so each gets a bucket of its own.
	perTenant *core.TenantRateLimiter
	global    *rate.Limiter
	metrics   *ReactMetrics

	mu          sync.Mutex
	unlettered  map[string]map[string]int // tenant → action kind → count
	windowStart time.Time
	now         func() time.Time
}

// newShedLetterer builds the letterer over the service's dead-letter sink. A nil sink (the test
// shape; see ReactDispatcher.dead) yields a letterer that only counts.
func newShedLetterer(dead *deadletter.Sink, b ShedLetterBudget, m *ReactMetrics) *shedLetterer {
	return &shedLetterer{
		dead:        dead,
		perTenant:   core.NewTenantRateLimiter(core.StaticCeiling(b.PerTenantPerSecond, b.PerTenantBurst)),
		global:      rate.NewLimiter(rate.Limit(b.GlobalPerSecond), b.GlobalBurst),
		metrics:     m,
		unlettered:  map[string]map[string]int{},
		windowStart: time.Now().UTC(),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// letter records the sheds of one derived event that REACT is about to ack. It is called only on a
// Done dispatch: a Retry re-runs the whole event, sheds included, and a letter written then would
// be about an attempt that did not stand.
//
// Each shed within budget is written with WriteForPart, keyed on the action's idempotency token:
// two shed actions of one event are two letters, a redelivery of the same event re-writes neither,
// and neither collides with the whole-message id the exhausted arm and the max-delivery recorder
// share. A write that fails is counted as a loss by the sink and never blocks the ack.
func (s *shedLetterer) letter(ctx context.Context, msg messaging.Message, ev runtime.DerivedEvent, sheds []react.ShedAction) {
	for _, shed := range sheds {
		if !s.perTenant.Allow(ev.Tenant) || !s.global.Allow() {
			s.metrics.recordShedUnlettered(shed.Kind)
			s.mu.Lock()
			byKind := s.unlettered[ev.Tenant]
			if byKind == nil {
				byKind = map[string]int{}
				s.unlettered[ev.Tenant] = byKind
			}
			byKind[shed.Kind]++
			s.mu.Unlock()
			continue
		}
		if s.dead == nil {
			continue
		}
		err := s.dead.WriteForPart(ctx, msg, shed.Token, deadletter.Envelope{
			Reason: deadletter.ReasonShed,
			Summary: "a detection's outbound connector action was refused at the source because the " +
				"tenant was over its outbound rate",
			Detail:     shed.Kind + "/shed/" + shed.Token,
			Reference:  ev.RuleID,
			OccurredAt: s.now(),
			Payload:    msg.Value,
		})
		if err != nil {
			// Counted on dead_letter_lost_total by the sink.
			log.Error().Err(err).Str("rule", ev.RuleID).Str("action", shed.Kind).
				Msg("LOST the dead letter for a shed connector action.")
			continue
		}
		s.metrics.recordShedDeadLettered(shed.Kind)
	}
}

// run summarises the over-budget sheds every shedSummaryWindow until ctx is done.
func (s *shedLetterer) run(ctx context.Context) {
	t := time.NewTicker(shedSummaryWindow)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flush(ctx)
		}
	}
}

// flush writes ONE summary letter per tenant with over-budget sheds in the window that just
// closed, then starts a new window. The letter is not about one consumed message, so it goes
// through Write, and its kind is the derived-events stream's own declaration — the same kind
// the individual letters carry — so no second place decides it.
func (s *shedLetterer) flush(ctx context.Context) {
	s.mu.Lock()
	counts := s.unlettered
	start, end := s.windowStart, s.now()
	s.unlettered = map[string]map[string]int{}
	s.windowStart = end
	s.mu.Unlock()
	if len(counts) == 0 || s.dead == nil {
		return
	}
	tenants := make([]string, 0, len(counts))
	for tenant := range counts {
		tenants = append(tenants, tenant)
	}
	sort.Strings(tenants)
	kind := deadletter.Kind(streams.DeadLetterKindFor(streams.DerivedEvents))
	for _, tenant := range tenants {
		byKind := counts[tenant]
		err := s.dead.Write(core.WithTenant(ctx, tenant), deadletter.Envelope{
			Kind:   kind,
			Reason: deadletter.ReasonShed,
			Summary: "outbound connector actions refused at the source were counted but not " +
				"individually recorded, because the tenant exceeded its dead-letter budget",
			Detail: fmt.Sprintf("httpCall=%d publish=%d window=%s/%s", byKind[string(rules.ActionHTTPCall)],
				byKind[string(rules.ActionPublish)], start.Format(time.RFC3339), end.Format(time.RFC3339)),
			OccurredAt: end,
		})
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).
				Msg("LOST the summary dead letter for over-budget shed connector actions.")
		}
	}
}
