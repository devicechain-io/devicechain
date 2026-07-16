// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/devicechain-io/dc-ai-inference/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver for the ai-inference GraphQL surface: the
// service identity, the instance-scoped AIProvider CRUD (ADR-056 §4), and the
// fail-closed inference call (§inference).
type SchemaResolver struct {
	// Area is the deployed functional area, returned by aiInferenceInfo.
	Area string
	// Inference resolves and runs a provider call, fail-closed. Built once at startup
	// (a stateless singleton, like Area) and shared across requests; the inference
	// mutations dispatch through it. Nil disables the inference mutations (they return
	// an error), so a partial deployment cannot serve an unwired inference path.
	Inference *inference.Resolver
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
