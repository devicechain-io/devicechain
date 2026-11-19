// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

// SuppressionLookup returns live suppression windows, omitting unsuppressed alarms.
// Implementations report lookup failures as errors; callers must fail toward paging
// by treating a failed lookup as though no alarm were suppressed.
type SuppressionLookup interface {
	LookupSuppressions(context.Context, []string) (map[string]SuppressionStatus, error)
}

// SuppressionStatus identifies the window and deadline of a live suppression.
// An empty window token or an expired deadline must never prevent a page.
type SuppressionStatus struct {
	WindowToken string
	Until       time.Time
}

const suppressionBatch = 200
const suppressionRecheck = 60 * time.Second
const suppressionUnsupportedFor = 2 * time.Minute

// ErrSuppressionUnsupported indicates a cached schema validation failure. It is
// silent and fails toward paging until the concrete lookup probes again.
var ErrSuppressionUnsupported = errors.New("suppression status is not served")

const alarmSuppressionStatusDocument = `query AlarmSuppressionStatus($tokens: [String!]!) { alarmSuppressionStatus(tokens: $tokens) { alarmToken suppression { windowToken until } } }`

type suppressionLookup struct {
	client           *svcclient.Client
	url              string
	now              func() time.Time
	mu               sync.Mutex
	unsupportedUntil time.Time
}

// NewSuppressionLookup builds a bounded, batched lookup independent of alarm lifecycles.
// An unavailable suppression field returns an error so callers can page normally
// during rolling upgrades without compromising the lifecycle backstop.
func NewSuppressionLookup(client *svcclient.Client, url string) SuppressionLookup {
	return &suppressionLookup{client: client, url: url, now: time.Now}
}

func distinctSuppressionTokens(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token != "" && !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

func (s *suppressionLookup) LookupSuppressions(ctx context.Context, tokens []string) (map[string]SuppressionStatus, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	s.mu.Lock()
	unsupported := s.now().Before(s.unsupportedUntil)
	s.mu.Unlock()
	if unsupported {
		return nil, ErrSuppressionUnsupported
	}
	found := make(map[string]SuppressionStatus)
	tokens = distinctSuppressionTokens(tokens)
	for start := 0; start < len(tokens); start += suppressionBatch {
		end := min(start+suppressionBatch, len(tokens))
		var out struct {
			Statuses *[]struct {
				AlarmToken  string `json:"alarmToken"`
				Suppression *struct {
					WindowToken string
					Until       string
				}
			} `json:"alarmSuppressionStatus"`
		}
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.client.Query(qctx, s.url, tenant, alarmSuppressionStatusDocument, map[string]any{"tokens": tokens[start:end]}, &out)
		cancel()
		if err != nil {
			if unsupportedSuppression(err) {
				s.mu.Lock()
				// Concurrent probes report only the first detection as a failure.
				cached := s.now().Before(s.unsupportedUntil)
				if !cached {
					s.unsupportedUntil = s.now().Add(suppressionUnsupportedFor)
				}
				s.mu.Unlock()
				if cached {
					return nil, ErrSuppressionUnsupported
				}
			}
			return nil, err
		}
		if out.Statuses == nil {
			return nil, fmt.Errorf("device-management returned no suppression status result")
		}
		requested := make(map[string]bool, end-start)
		for _, token := range tokens[start:end] {
			requested[token] = true
		}
		for _, a := range *out.Statuses {
			if !requested[a.AlarmToken] || a.Suppression == nil || a.Suppression.WindowToken == "" {
				continue
			}
			until, err := time.Parse(time.RFC3339Nano, a.Suppression.Until)
			if err == nil && until.After(s.now()) {
				found[a.AlarmToken] = SuppressionStatus{WindowToken: a.Suppression.WindowToken, Until: until}
			}
		}
	}
	return found, nil
}

// Only validation errors exclusively naming the added field establish incompatibility.
func unsupportedSuppression(err error) bool {
	var gqlErr *svcclient.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Messages) == 0 {
		return false
	}
	for _, message := range gqlErr.Messages {
		m := strings.ToLower(message)
		if !(strings.Contains(m, "cannot query field") || strings.Contains(m, "unknown field")) ||
			!strings.Contains(m, "alarmsuppressionstatus") {
			return false
		}
	}
	return true
}

// Shared by all notifier instances in this process; uncached errors count every call.
var suppressionLastWarning atomic.Int64

func (n *PolicyNotifier) lookupSuppressions(ctx context.Context, tokens []string, now time.Time, timeout time.Duration) map[string]SuppressionStatus {
	found := make(map[string]SuppressionStatus)
	if n.SuppressionLookup == nil {
		return found
	}
	tokens = distinctSuppressionTokens(tokens)
	for start := 0; start < len(tokens); start += suppressionBatch {
		end := min(start+suppressionBatch, len(tokens))
		lctx, cancel := context.WithTimeout(ctx, timeout)
		rows, err := n.SuppressionLookup.LookupSuppressions(lctx, tokens[start:end])
		cancel()
		if err != nil {
			if errors.Is(err, ErrSuppressionUnsupported) {
				return map[string]SuppressionStatus{}
			}
			n.countSuppression("lookup_failed", 1)
			if unsupportedSuppression(err) {
				log.Info().Err(err).Msg("Device-management does not serve suppression status yet; suppression is off until it does")
				return map[string]SuppressionStatus{}
			}
			wall := time.Now().UnixNano()
			last := suppressionLastWarning.Load()
			if (last == 0 || wall-last >= int64(time.Minute)) && suppressionLastWarning.CompareAndSwap(last, wall) {
				log.Warn().Err(err).Msg("Alarm suppression lookup failed; paging normally")
			}
			// A partial answer must never suppress anybody when a lookup failed.
			return map[string]SuppressionStatus{}
		}
		for token, status := range rows {
			if validWindowToken(status.WindowToken) && status.Until.After(now) {
				found[token] = status
			}
		}
	}
	return found
}

func (n *PolicyNotifier) countSuppression(outcome string, by int) {
	if n.suppression != nil && by > 0 {
		n.suppression.WithLabelValues(outcome).Add(float64(by))
	}
}

func suppressionReleaseAt(until, now time.Time) time.Time {
	if next := now.Add(suppressionRecheck); next.Before(until) {
		return next
	}
	return until
}

func (n *PolicyNotifier) escalationSuppressed(ctx context.Context, token string, now time.Time) bool {
	var statuses map[string]SuppressionStatus
	if pass := passFromContext(ctx); pass != nil {
		statuses = pass.suppressions
	} else {
		statuses = n.lookupSuppressions(ctx, []string{token}, now, 5*time.Second)
	}
	status, ok := statuses[token]
	if ok && validWindowToken(status.WindowToken) && status.Until.After(now) {
		n.countSuppression("escalation_skipped", 1)
		return true
	}
	return false
}

// maxWindowTokenLength is the width of notification_deferrals.held_by_window.
const maxWindowTokenLength = 128

// validWindowToken requires core's token grammar (an ASCII letter or digit followed by
// letters, digits, hyphens or underscores) and the hold audit column's length bound.
// A token that could not be stored would make
// the hold's write fail, and a write that fails before any send turns into redelivery and
// then a dead letter, so a page would be lost to an optional field. It is read as NOT
// suppressed instead, which pages.
func validWindowToken(token string) bool {
	return core.ValidateToken(token) == nil && len(token) <= maxWindowTokenLength
}
