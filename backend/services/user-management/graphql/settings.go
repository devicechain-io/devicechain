// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/settings"
)

//go:embed settings_schema.gql
var SettingsSchemaContent string

// ContextSettingsKey injects the settings Service into the settings GraphQL
// request context (ADR-042 P2). Like the admin API, the settings schema is served
// on its own /settings/graphql endpoint with its own resolver root, keeping the
// store's extraction seam pre-cut — these resolvers import settings, not iam.
const ContextSettingsKey = gqlcore.ContextKey("settings")

// SettingsResolver is the root resolver for the instance-scoped settings schema.
type SettingsResolver struct{}

// settingsFromContext retrieves the settings Service injected into a served
// request's context (main.go puts it in BOTH provider maps, so it is present on the
// tenant data plane and on the identity lane alike).
//
// 🔴 It panics when the key is absent, which is correct for a served request and
// wrong for one that should already have been refused — so a resolver must run its
// auth check BEFORE calling this, never pass the result in as an argument. Taking
// the service as a parameter is what made an unauthenticated token-masks read panic
// instead of returning ErrUnauthenticated; TestSettingsFailClosed caught it.
func settingsFromContext(ctx context.Context) *settings.Service {
	return ctx.Value(ContextSettingsKey).(*settings.Service)
}

// getSettingsService retrieves the settings Service from context.
func (r *SettingsResolver) getSettingsService(ctx context.Context) *settings.Service {
	return settingsFromContext(ctx)
}
