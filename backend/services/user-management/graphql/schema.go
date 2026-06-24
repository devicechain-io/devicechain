// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
)

//go:embed schema.gql
var SchemaContent string

// ContextIdentityKey injects the identity Manager into the GraphQL request
// context so the login/refresh resolvers can reach it.
const ContextIdentityKey = gqlcore.ContextKey("identity")

type SchemaResolver struct{}

// getIdentityManager retrieves the identity Manager from context.
func (r *SchemaResolver) getIdentityManager(ctx context.Context) *identity.Manager {
	return ctx.Value(ContextIdentityKey).(*identity.Manager)
}

// Ping resolves the liveness placeholder query.
func (r *SchemaResolver) Ping() string { return "pong" }
