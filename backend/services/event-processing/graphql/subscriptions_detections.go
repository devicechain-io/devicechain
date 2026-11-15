// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
)

// liveDetection carries a decoded event and its parsed rule token, shared read-only.
type liveDetection struct {
	de        runtime.DerivedEvent
	ruleToken string
}

// Factories are test seams, replaced only before creating a hub.
var detectionLiveOpener = func(nmgr *messaging.NatsManager) messaging.LiveOpener {
	return func(ctx context.Context, tenant string) (<-chan messaging.Message, error) {
		return nmgr.SubscribeLive(ctx, tenant, streams.DerivedEvents)
	}
}
var detectionDecode = decodeDetections
var detectionDropHook func()

// InitLiveSubscriptionMetrics runs once during process initialization, never per connection.
func InitLiveSubscriptionMetrics(ms *core.Microservice) {
	counter := ms.NewCounterVec("live_subscription_dropped_total", "Live values dropped for slow subscribers.", []string{"suffix"}).WithLabelValues(streams.DerivedEvents)
	detectionDropHook = counter.Inc
}

func detectionHub(nmgr *messaging.NatsManager) *messaging.LiveHub[liveDetection] {
	return messaging.SharedLiveHub(nmgr, streams.DerivedEvents, func() *messaging.LiveHub[liveDetection] {
		hub := messaging.NewLiveHub(streams.DerivedEvents, detectionLiveOpener(nmgr), detectionDecode, messaging.DefaultMaxLiveSubscribersPerTenant)
		hub.OnDrop(detectionDropHook)
		return hub
	})
}

func decodeDetections(msg messaging.Message, _ string) []messaging.Keyed[liveDetection] {
	var de runtime.DerivedEvent
	if err := json.Unmarshal(msg.Value, &de); err != nil {
		log.Debug().Err(err).Msg("detection subscription: skipping undecodable derived event")
		return nil
	}
	profileToken, ruleToken, ok := runtime.ProfileAndRuleToken(de.RuleID)
	if !ok {
		return nil
	}
	return []messaging.Keyed[liveDetection]{{Key: profileToken, Value: liveDetection{de: de, ruleToken: ruleToken}}}
}

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
// is live from subscribe time onward with no backfill. The shared feed is torn down when
// its last subscriber disconnects (ctx cancelled). Tenant-scoped; gated on device:read.
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
	// already this tenant's — hub dispatch narrows to the requested profile only.
	live, err := detectionHub(r.GetNats(ctx)).Subscribe(ctx, tenant, []string{args.ProfileToken})
	if err != nil {
		return nil, err
	}

	out := make(chan *DetectionEventResolver)
	go func() {
		defer close(out)
		for batch := range live {
			for _, ev := range batch {
				select {
				case out <- &DetectionEventResolver{de: ev.de, ruleToken: ev.ruleToken}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// DetectionEventResolver resolves one detection on the live feed. ruleToken is parsed from the
// rule id during decode so the client can link a firing back to its authored rule.
// The event and its pointer fields are shared read-only across subscribers.
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
// API's uniform RFC3339Nano convention — an event time is addressable, so it is published
// at the resolution it was recorded at. See AlarmEventResolver.OccurredTime.
func (r *DetectionEventResolver) OccurredTime() string {
	return r.de.OccurredTime.UTC().Format(time.RFC3339Nano)
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
