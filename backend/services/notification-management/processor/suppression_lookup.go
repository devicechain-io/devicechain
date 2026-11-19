// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
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
const alarmSuppressionStatusDocument = `query AlarmSuppressionStatus($tokens: [String!]!) { alarmSuppressionStatus(tokens: $tokens) { alarmToken suppression { windowToken until } } }`

type suppressionLookup struct {
	client *svcclient.Client
	url    string
	now    func() time.Time
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

// Shared by all notifier instances in this process; errors still count on every call.
var suppressionLastWarning atomic.Int64

func (n *PolicyNotifier) lookupSuppressions(ctx context.Context, tokens []string, now time.Time) map[string]SuppressionStatus {
	found := make(map[string]SuppressionStatus)
	if n.SuppressionLookup == nil {
		return found
	}
	tokens = distinctSuppressionTokens(tokens)
	for start := 0; start < len(tokens); start += suppressionBatch {
		end := min(start+suppressionBatch, len(tokens))
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rows, err := n.SuppressionLookup.LookupSuppressions(lctx, tokens[start:end])
		cancel()
		if err != nil {
			n.countSuppression("lookup_failed", 1)
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
		statuses = n.lookupSuppressions(ctx, []string{token}, now)
	}
	status, ok := statuses[token]
	if ok && validWindowToken(status.WindowToken) && status.Until.After(now) {
		n.countSuppression("rechecked", 1)
		return true
	}
	return false
}

// maxWindowTokenLength is the width of notification_deferrals.held_by_window.
const maxWindowTokenLength = 128

// validWindowToken reports whether a window token can be honoured: non-empty, and short
// enough to be recorded as the hold's audit. A token that could not be stored would make
// the hold's write fail, and a write that fails before any send turns into redelivery and
// then a dead letter, so a page would be lost to an optional field. It is read as NOT
// suppressed instead, which pages.
func validWindowToken(token string) bool {
	return token != "" && len(token) <= maxWindowTokenLength
}
