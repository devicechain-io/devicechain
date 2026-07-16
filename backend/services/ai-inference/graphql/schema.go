// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-ai-inference/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver for the ai-inference GraphQL surface: the
// service identity plus the instance-scoped AIProvider CRUD (ADR-056 §4).
type SchemaResolver struct {
	// Area is the deployed functional area, returned by aiInferenceInfo.
	Area string
}

// GetApi returns the provider api from context (injected as a provider in main).
func (r *SchemaResolver) GetApi(ctx context.Context) *model.Api {
	return ctx.Value(gqlcore.ContextApiKey).(*model.Api)
}

// serviceInfoResolver resolves the ServiceInfo type.
type serviceInfoResolver struct {
	area string
}

// AiInferenceInfo returns the non-sensitive service identity.
func (r *SchemaResolver) AiInferenceInfo() *serviceInfoResolver {
	return &serviceInfoResolver{area: r.Area}
}

// Area resolves ServiceInfo.area.
func (r *serviceInfoResolver) Area() string { return r.area }
