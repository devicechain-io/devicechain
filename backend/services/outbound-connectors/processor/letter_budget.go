// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// Match REACT's default shed-letter budgets without adding rolling-upgrade config keys.
const (
	dispatchLetterPerSecond    = 1
	dispatchLetterBurst        = 60
	dispatchLetterGlobalRate   = 10
	dispatchLetterGlobalBurst  = 100
	dispatchLetterWindow       = time.Minute
	dispatchLetterFlushTimeout = 10 * time.Second
)

// Both budgets cover the verbatim copy, index entry and terminal Error log together.
// Refused letters are counted by bounded action/outcome labels and aggregated per
// tenant per window. Final-delivery retry exhaustion remains outside this budget.
type dispatchLetterBudget struct {
	mu          sync.Mutex
	perTenant   *core.TenantRateLimiter
	global      *rate.Limiter
	unlettered  map[string]map[string]int
	windowStart time.Time
	summary     *deadletter.Sink
	now         func() time.Time
	window      time.Duration
}

func newDispatchLetterBudget(producer *deadletter.Producer, writer deadletter.Writer) *dispatchLetterBudget {
	var summary *deadletter.Sink
	if writer != nil {
		summary = producer.NewSink(writer)
	}
	return &dispatchLetterBudget{
		perTenant:   core.NewTenantRateLimiter(core.StaticCeiling(dispatchLetterPerSecond, dispatchLetterBurst)),
		global:      rate.NewLimiter(dispatchLetterGlobalRate, dispatchLetterGlobalBurst),
		unlettered:  map[string]map[string]int{},
		windowStart: time.Now().UTC(),
		summary:     summary,
		now:         func() time.Time { return time.Now().UTC() },
		window:      dispatchLetterWindow,
	}
}

func (s *dispatchLetterBudget) admit(tenant, action, outcome string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	r := s.global.ReserveN(now, 1)
	if r.OK() {
		if r.DelayFrom(now) == 0 && s.perTenant.AllowAt(tenant, now) {
			return true
		}
		// Cancel at the reservation's instant so a tenant refusal cannot spend
		// the global allowance needed by another tenant. Serialize reservations
		// and cancellation, including across workers.
		r.CancelAt(now)
	}
	if s.unlettered[tenant] == nil {
		s.unlettered[tenant] = map[string]int{}
	}
	s.unlettered[tenant][action+"/"+outcome]++
	return false
}

func (c *DispatchConsumer) budgetedDeadLetter(ctx context.Context, msg messaging.Message, rule, action, outcome string, cause error) {
	tenant, _ := core.TenantFromContext(ctx)
	// Preserve deadLetter's deleted-tenant refusal, including its outcome and ACK,
	// before touching the budget or retaining a summary for that tenant.
	if c.deadLetters.TenantDeleted(tenant) {
		c.deadLetter(ctx, msg, rule, action, outcome)
		return
	}
	if !c.letters.admit(tenant, action, outcome) {
		c.metrics.recordUnlettered(action, outcome)
		c.metrics.recordOutcome(action, outcome)
		c.ack(msg)
		return
	}
	if cause != nil {
		log.Error().Err(cause).Str("rule", rule).Str("tenant", tenant).
			Msg("Connector dispatch is terminally undeliverable; dead-lettering.")
	}
	c.deadLetter(ctx, msg, rule, action, outcome)
}

func (s *dispatchLetterBudget) run(ctx context.Context) {
	ticker := time.NewTicker(s.window)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

// Summaries contain counts only, never a payload, endpoint or error text. A lost
// summary is counted by the producer's usual dead_letter_lost_total hook.
func (s *dispatchLetterBudget) flush(ctx context.Context) {
	s.mu.Lock()
	counts, start, end := s.unlettered, s.windowStart, s.now()
	s.unlettered = map[string]map[string]int{}
	s.windowStart = end
	s.mu.Unlock()
	if s.summary == nil {
		return
	}
	for tenant, byOutcome := range counts {
		parts := make([]string, 0, len(byOutcome))
		reason := deadletter.ReasonShed
		for key, count := range byOutcome {
			parts = append(parts, fmt.Sprintf("%s=%d", key, count))
			if !strings.HasSuffix(key, "/"+outcomeRateLimited) {
				reason = deadletter.ReasonUnprocessable
			}
		}
		sort.Strings(parts)
		if err := s.summary.Write(core.WithTenant(ctx, tenant), deadletter.Envelope{
			Kind:       deadletter.KindConnectorDispatch,
			Reason:     reason,
			Summary:    "outbound connector dispatches were counted but not individually recorded because their dead-letter budget was exceeded",
			Detail:     fmt.Sprintf("%s window=%s/%s", strings.Join(parts, " "), start.Format(time.RFC3339), end.Format(time.RFC3339)),
			OccurredAt: end,
		}); err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("LOST the summary dead letter for over-budget connector dispatches.")
		}
	}
}
