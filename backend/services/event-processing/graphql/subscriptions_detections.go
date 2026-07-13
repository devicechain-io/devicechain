// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// GetNats returns the NATS manager injected as a graphql provider in main.go (ADR-037). It
// backs the live detection subscription (SubscribeLive); the manager is connected before the
// subscription server accepts a client, so the provider is always present at subscribe time.
func (r *SchemaResolver) GetNats(ctx context.Context) *messaging.NatsManager {
	return ctx.Value(gqlcore.ContextNatsKey).(*messaging.NatsManager)
}

// DetectionStream streams a device profile's DETECT detections to the subscriber as they fire
// (ADR-037 / ADR-057, slice 7c). It taps the caller tenant's derived-event feed — the same
// first-class signal the REACT dispatcher consumes — and pushes every firing whose rule belongs
// to the given profile, including a rule with NO alarm action (a pure emitted signal), which the
// alarm stream would miss: a detection feed must show ALL firings, so it taps the derived-event
// stream, not the alarm stream. Both edges (raised and resolved, ADR-057) are streamed. The feed
// is live from subscribe time onward with no backfill, and torn down when the client
// unsubscribes or disconnects (ctx cancelled). Tenant-scoped; gated on device:read.
func (r *SchemaResolver) DetectionStream(ctx context.Context, args struct {
	ProfileToken string
}) (<-chan *DetectionEventResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	if args.ProfileToken == "" {
		return nil, fmt.Errorf("detectionStream: a profileToken is required")
	}

	// SubscribeLive scopes to "{instance}.{tenant}.derived-events", so every delivered event is
	// already this tenant's — the per-event filter narrows to the requested profile only.
	live, err := r.GetNats(ctx).SubscribeLive(ctx, tenant, config.SUBJECT_DERIVED_EVENTS)
	if err != nil {
		return nil, err
	}

	out := make(chan *DetectionEventResolver)
	go func() {
		defer close(out)
		for msg := range live {
			var de runtime.DerivedEvent
			if err := json.Unmarshal(msg.Value, &de); err != nil {
				log.Debug().Err(err).Msg("detection subscription: skipping undecodable derived event")
				continue
			}
			profileToken, ruleToken, ok := runtime.ProfileAndRuleToken(de.RuleID)
			if !ok || profileToken != args.ProfileToken {
				continue
			}
			select {
			case out <- &DetectionEventResolver{de: de, ruleToken: ruleToken}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// DetectionEventResolver resolves one detection on the live feed. ruleToken is parsed from the
// rule id at fan-out so the client can link a firing back to its authored rule.
type DetectionEventResolver struct {
	de        runtime.DerivedEvent
	ruleToken string
}

func (r *DetectionEventResolver) RuleId() string    { return r.de.RuleID }
func (r *DetectionEventResolver) RuleToken() string { return r.ruleToken }
func (r *DetectionEventResolver) Kind() string      { return r.de.Kind }
func (r *DetectionEventResolver) Edge() string      { return r.de.Edge }
func (r *DetectionEventResolver) Series() string    { return r.de.Series }

// OccurredTime is the logical (event) time the detection is stamped at, formatted with the
// API's uniform RFC3339 convention.
func (r *DetectionEventResolver) OccurredTime() string {
	return r.de.OccurredTime.UTC().Format(time.RFC3339)
}

// Severity is the detection's tier snapshot, null when the rule declares none.
func (r *DetectionEventResolver) Severity() *string {
	if r.de.Severity == "" {
		return nil
	}
	s := r.de.Severity
	return &s
}

// Value is the triggering scalar, present only on a raised edge (a resolve carries none).
func (r *DetectionEventResolver) Value() *float64 { return r.de.Value }
