// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	_ "embed"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver for the outbound-connectors GraphQL surface. In slice C3 it
// carries only the scaffold health/metrics server (a trivial service-identity query); the versioned
// Connector CRUD is added here in slice C4.
type SchemaResolver struct {
	// Area is the deployed functional area, returned by outboundConnectorsInfo.
	Area string
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
