// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-event-processing/model"
)

//go:embed schema.graphql
var SchemaContent string

// FunctionalArea is the functional-area name this service serves; surfaced by the
// scaffold query and matching the Helm/functionalarea catalog entry (ADR-051).
const FunctionalArea = "event-processing"

// SchemaResolver is the GraphQL root resolver. The scaffold identity probe and the ADR-044
// detection-rule validation gate (resolvers_detection_rules.go) are pure/state-free; the
// rule-health read (resolvers_rule_health.go, slice 7b) reads the durable rule + firing
// projections, so the resolver carries their stores. The stores are nil on the pure-validation
// path (a test constructing the bare resolver); the rule-health resolver nil-checks them and
// fails closed rather than panicking.
type SchemaResolver struct {
	DetectRules *model.DetectRuleStore
	RuleStats   *model.RuleStatStore
	Profiles    *model.ProfileActiveStore
	// Drafter is the ADR-056 NL→rule drafting orchestrator (slice 1). It is nil when the
	// inference path is not wired (ai-inference is an opt-in area); the draft resolver
	// nil-checks it and returns an unavailable result rather than panicking.
	Drafter *nldraft.Drafter
}

// EventProcessingInfo resolves the scaffold identity query.
func (r *SchemaResolver) EventProcessingInfo(ctx context.Context) *EventProcessingInfoResolver {
	return &EventProcessingInfoResolver{}
}

// EventProcessingInfoResolver resolves the EventProcessingInfo type.
type EventProcessingInfoResolver struct{}

// FunctionalArea resolves the functionalArea field.
func (r *EventProcessingInfoResolver) FunctionalArea() string { return FunctionalArea }
