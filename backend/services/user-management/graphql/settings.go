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

// getSettingsService retrieves the settings Service from context.
func (r *SettingsResolver) getSettingsService(ctx context.Context) *settings.Service {
	return ctx.Value(ContextSettingsKey).(*settings.Service)
}
