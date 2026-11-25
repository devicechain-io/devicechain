// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package graphql

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// I-23 on the real schema/tenant callbacks: no refused resolver may append an event.
func TestFleetResolveI23OnPostgres(t *testing.T) {
	api := newReadinessPostgresApi(t)
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("resolveauth%d", time.Now().UnixNano()))
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	for _, tc := range []struct {
		name, code string
		change     func(*auth.Claims)
	}{
		{"agent", "AGENT_NOT_PERMITTED", func(c *auth.Claims) { c.ClientId = "agent" }},
		{"service", "AGENT_NOT_PERMITTED", func(c *auth.Claims) { c.TokenType = auth.TokenTypeService }},
		{"scoped", "FORBIDDEN", func(c *auth.Claims) { c.ScopeAnchors = []string{"customer:acme"} }},
		{"no device write", "FORBIDDEN", func(c *auth.Claims) { c.Authorities = []string{string(auth.FleetOperate)} }},
		{"no fleet operate", "FORBIDDEN", func(c *auth.Claims) { c.Authorities = []string{string(auth.DeviceWrite)} }},
		{"no second factor", "SECOND_FACTOR_REQUIRED", func(c *auth.Claims) { c.Amr = auth.AmrPasswordOnly(); c.MfaTime = nil }},
		{"stale second factor", "STEP_UP_REQUIRED", func(c *auth.Claims) { c.MfaTime.Time = c.MfaTime.Time.Add(-6 * time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := resolvePerson()
			tc.change(c)
			_, errs := execFleet(t, auth.WithClaims(ctx, c), resolveWire, resolveWireRequest("reason"))
			require.Len(t, errs, 1)
			require.True(t, strings.HasPrefix(errs[0], tc.code+"|"), errs[0])
		})
	}
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&model.FleetOperationEvent{}).Count(&n).Error)
	require.Zero(t, n)
}
