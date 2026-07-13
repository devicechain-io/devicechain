// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-outbound-connectors/model"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver for the outbound-connectors GraphQL surface: the
// service identity plus the per-tenant, versioned Connector CRUD (ADR-060 slice C4).
type SchemaResolver struct {
	// Area is the deployed functional area, returned by outboundConnectorsInfo.
	Area string
}

// GetApi returns the connector api from context (injected as a provider in main).
func (r *SchemaResolver) GetApi(ctx context.Context) *model.Api {
	return ctx.Value(gqlcore.ContextApiKey).(*model.Api)
}

// serviceInfoResolver resolves the ServiceInfo type.
type serviceInfoResolver struct {
	area string
}

// OutboundConnectorsInfo returns the non-sensitive service identity.
func (r *SchemaResolver) OutboundConnectorsInfo() *serviceInfoResolver {
	return &serviceInfoResolver{area: r.Area}
}

// Area resolves ServiceInfo.area.
func (r *serviceInfoResolver) Area() string { return r.area }
