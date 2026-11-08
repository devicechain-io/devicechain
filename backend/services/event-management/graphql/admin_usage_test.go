// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUsage struct {
	gotTenant string
	days      []model.TenantEventUsageDay
}

func (f *fakeUsage) TenantEventUsage(_ context.Context, tenant string, from, to time.Time) ([]model.TenantEventUsageDay, error) {
	f.gotTenant = tenant
	return f.days, model.ValidateUsageRange(from, to)
}

func usageCtx(claims *auth.Claims, api UsageReader) context.Context {
	ctx := auth.WithClaims(context.Background(), claims)
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

func TestAdminSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("admin schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
}

const usageQuery = `{ tenantEventUsage(tenant: "acme", from: "2026-11-01", to: "2026-11-02") {
	tenant day eventsIngested devicesActive devicesPeak devicesAvg complete } }`

func TestTenantEventUsageServesTheDaysThroughTheSchema(t *testing.T) {
	api := &fakeUsage{days: []model.TenantEventUsageDay{
		{TenantId: "acme", Day: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), EventsIngested: 3_000_000_000, DevicesActive: 4,
			DevicesPeak: 3, DeviceHours: 36, Complete: true},
		{TenantId: "acme", Day: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
	}}
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	ctx := usageCtx(&auth.Claims{TokenType: auth.TokenTypeIdentity, Authorities: []string{"usage:read"}}, api)

	res := schema.Exec(ctx, usageQuery, "", nil)
	require.Empty(t, res.Errors)
	assert.Equal(t, "acme", api.gotTenant)
	assert.JSONEq(t, `{"tenantEventUsage":[
		{"tenant":"acme","day":"2026-11-01","eventsIngested":3000000000,"devicesActive":4,"devicesPeak":3,"devicesAvg":1.5,"complete":true},
		{"tenant":"acme","day":"2026-11-02","eventsIngested":0,"devicesActive":0,"devicesPeak":0,"devicesAvg":0,"complete":false}]}`,
		string(res.Data))
}

// A tenant admin's "*" token must not read the instance's usage record: tenant:read is system-tier.
func TestTenantEventUsageRefusesATenantToken(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	ctx := usageCtx(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Authorities: []string{string(auth.AuthorityAll)}}, &fakeUsage{})
	res := schema.Exec(ctx, usageQuery, "", nil)
	require.NotEmpty(t, res.Errors)
	assert.Contains(t, res.Errors[0].Message, "forbidden")
}

func TestTenantEventUsageRefusesAnIdentityWithoutTheAuthority(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	for _, held := range []auth.Authority{auth.SettingsRead, auth.TenantRead} {
		ctx := usageCtx(&auth.Claims{TokenType: auth.TokenTypeIdentity, Authorities: []string{string(held)}}, &fakeUsage{})
		res := schema.Exec(ctx, usageQuery, "", nil)
		require.NotEmpty(t, res.Errors, "%s does not read the usage record: usage:read does", held)
		assert.Contains(t, res.Errors[0].Message, "forbidden")
	}
}

func TestTenantEventUsageRejectsMalformedDays(t *testing.T) {
	r := &AdminResolver{}
	ctx := usageCtx(&auth.Claims{TokenType: auth.TokenTypeIdentity, Authorities: []string{string(auth.UsageRead)}}, &fakeUsage{})
	_, err := r.TenantEventUsage(ctx, struct{ Tenant, From, To string }{"acme", "yesterday", "2026-11-02"})
	assert.ErrorContains(t, err, "from must be a day")
	_, err = r.TenantEventUsage(ctx, struct{ Tenant, From, To string }{"acme", "2026-11-01", "11/02/2026"})
	assert.ErrorContains(t, err, "to must be a day")
	_, err = r.TenantEventUsage(ctx, struct{ Tenant, From, To string }{"acme", "2026-11-03", "2026-11-02"})
	assert.ErrorIs(t, err, model.ErrUsageRange)
}
