// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/admin"
)

//go:embed admin_schema.gql
var AdminSchemaContent string

// ContextAdminKey injects the admin control-plane Service into the admin GraphQL
// request context (ADR-033). It is distinct from ContextIdentityKey: the admin
// schema is served on its own /admin/graphql endpoint with its own resolver root.
const ContextAdminKey = gqlcore.ContextKey("admin")

// AdminResolver is the root resolver for the admin (instance-scoped) schema.
type AdminResolver struct{}

// getAdminService retrieves the admin Service from context.
func (r *AdminResolver) getAdminService(ctx context.Context) *admin.Service {
	return ctx.Value(ContextAdminKey).(*admin.Service)
}

// Ping resolves the liveness placeholder query.
func (r *AdminResolver) Ping() string { return "pong" }
