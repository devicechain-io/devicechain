// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
)

//go:embed schema.graphql
var SchemaContent string

// FunctionalArea is the functional-area name this service serves; surfaced by the
// scaffold query and matching the Helm/functionalarea catalog entry (ADR-051).
const FunctionalArea = "event-processing"

// SchemaResolver is the GraphQL root resolver. It is intentionally state-free: its
// surfaces (the scaffold identity probe and the ADR-044 detection-rule validation gate,
// resolvers_detection_rules.go) are pure — the validation gate compiles rules through the
// stateless DETECT compiler, reading and writing nothing.
type SchemaResolver struct{}

// EventProcessingInfo resolves the scaffold identity query.
func (r *SchemaResolver) EventProcessingInfo(ctx context.Context) *EventProcessingInfoResolver {
	return &EventProcessingInfoResolver{}
}

// EventProcessingInfoResolver resolves the EventProcessingInfo type.
type EventProcessingInfoResolver struct{}

// FunctionalArea resolves the functionalArea field.
func (r *EventProcessingInfoResolver) FunctionalArea() string { return FunctionalArea }
