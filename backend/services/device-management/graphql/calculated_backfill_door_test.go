// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The backfill doors hand a worker a tenant's formulas and every device of a profile, so they take
// device:read like every other device read, refuse a customer-scoped caller whatever it holds, and
// answer only the caller's tenant.

type ceilingOf int

func (c ceilingOf) Resolve(context.Context, string) (int, error) { return int(c), nil }

// calcDoorApi is the reconcile fixture's tenant "acme" (profile "prof", type "sensor", device "d1")
// with a calculated metric published as version 2.
func calcDoorApi(t *testing.T) *model.Api {
	t.Helper()
	api := reconcileDoorApi(t)
	api.CalculatedMetricsCeiling = ceilingOf(5)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, req := range []*model.MetricDefinitionCreateRequest{
		{Token: "md-v", DeviceProfileToken: "prof", MetricKey: "voltage", DataType: "DOUBLE"},
		{Token: "md-p", DeviceProfileToken: "prof", MetricKey: "power", DataType: "DOUBLE",
			Expression: strPtr("voltage * device.gain")},
	} {
		_, err := api.CreateMetricDefinition(ctx, req)
		require.NoError(t, err)
	}
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	return api
}

func strPtr(s string) *string { return &s }

const planQuery = `query($p: String!) { calculatedMetricPlan(profileToken: $p) {
	versionToken version activeSince constantKeys
	measured { key dataType }
	calculated { key expression dataType classifier newInVersion changedSinceEarlier } } }`

const rosterQuery = `query($p: String!, $k: [String!]!, $l: Int!) { calculatedBackfillRoster(profileToken: $p, constantKeys: $k, limit: $l) {
	nextCursor total entries { deviceId deviceToken createdAt constants { key number } } } }`

func TestBackfillDoorsAreServedToDeviceReadOnly(t *testing.T) {
	api := calcDoorApi(t)
	acme := reconcileCtx(api, "acme")

	// No claims, no answer.
	for _, q := range []struct {
		query string
		vars  map[string]any
	}{
		{planQuery, map[string]any{"p": "prof"}},
		{rosterQuery, map[string]any{"p": "prof", "k": []any{"gain"}, "l": 10}},
	} {
		res := serve(t, acme, q.query, q.vars)
		assert.NotEmpty(t, res.errs, "an unauthenticated caller must be refused")
		// A caller without device:read is refused too.
		res = serve(t, withAuthorities(acme, auth.EventRead), q.query, q.vars)
		assert.NotEmpty(t, res.errs, "device:read is the gate")
	}

	plan := serve(t, withAuthorities(acme, auth.DeviceRead), planQuery, map[string]any{"p": "prof"})
	require.Empty(t, plan.errs)
	p := plan.data["calculatedMetricPlan"].(map[string]any)
	assert.Equal(t, "prof@2", p["versionToken"])
	assert.Equal(t, []any{"gain"}, p["constantKeys"])
	calc := p["calculated"].([]any)
	require.Len(t, calc, 1)
	assert.Equal(t, "power", calc[0].(map[string]any)["key"])
	assert.Equal(t, true, calc[0].(map[string]any)["newInVersion"])

	roster := serve(t, withAuthorities(acme, auth.DeviceRead), rosterQuery,
		map[string]any{"p": "prof", "k": []any{"gain"}, "l": 10})
	require.Empty(t, roster.errs)
	page := roster.data["calculatedBackfillRoster"].(map[string]any)
	assert.Equal(t, float64(1), page["total"])
	entries := page["entries"].([]any)
	require.Len(t, entries, 1)
	assert.Equal(t, "d1", entries[0].(map[string]any)["deviceToken"])
	assert.NotEmpty(t, entries[0].(map[string]any)["createdAt"])
	assert.Nil(t, page["nextCursor"])
}

// A customer-scoped member holds device:read and is refused by the scope, not served a filtered view.
func TestBackfillDoorsRefuseAScopedCaller(t *testing.T) {
	api := calcDoorApi(t)
	scoped := auth.WithClaims(core.WithTenant(context.Background(), "acme"), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Authorities:  []string{string(auth.DeviceRead), string(auth.EventRead)},
		ScopeAnchors: []string{"customer:acme-co"}})
	scoped = reconcileCtxFrom(scoped, api)

	requireScopeRefusal(t, serve(t, scoped, planQuery, map[string]any{"p": "prof"}), "calculatedMetricPlan")
	requireScopeRefusal(t, serve(t, scoped, rosterQuery,
		map[string]any{"p": "prof", "k": []any{"gain"}, "l": 10}), "calculatedBackfillRoster")
}

func reconcileCtxFrom(ctx context.Context, api *model.Api) context.Context {
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// Another tenant's profile is a miss, with the same NOT_FOUND a nonexistent one gets.
func TestBackfillDoorsAnswerOnlyTheCallersTenant(t *testing.T) {
	api := calcDoorApi(t)
	other := withAuthorities(reconcileCtx(api, "globex"), auth.DeviceRead)

	foreign := serve(t, other, planQuery, map[string]any{"p": "prof"})
	missing := serve(t, other, planQuery, map[string]any{"p": "nope"})
	require.Len(t, foreign.errs, 1)
	require.Len(t, missing.errs, 1)
	assert.Equal(t, "NOT_FOUND", foreign.errs[0].Extensions["code"])
	assert.Equal(t, missing.errs[0].Message, foreign.errs[0].Message)

	res := serve(t, other, rosterQuery, map[string]any{"p": "prof", "k": []any{"gain"}, "l": 10})
	require.Len(t, res.errs, 1)
	assert.Equal(t, "NOT_FOUND", res.errs[0].Extensions["code"])
}

// A draft formula is not a plan, and a malformed cursor is refused rather than read as "start over".
func TestBackfillDoorsRefuseADraftAndABadCursor(t *testing.T) {
	api := calcDoorApi(t)
	ctx := withAuthorities(reconcileCtx(api, "acme"), auth.DeviceRead)

	res := serve(t, ctx, `query { calculatedMetricPlan(profileToken: "prof", version: 3) { version } }`, nil)
	require.Len(t, res.errs, 1)
	assert.Equal(t, "NOT_FOUND", res.errs[0].Extensions["code"])

	res = serve(t, ctx, `query { calculatedBackfillRoster(profileToken: "prof", constantKeys: [], afterId: "12abc", limit: 5) { total } }`, nil)
	assert.NotEmpty(t, res.errs)
	res = serve(t, ctx, `query { calculatedBackfillRoster(profileToken: "prof", constantKeys: [], limit: 501) { total } }`, nil)
	require.Len(t, res.errs, 1)
	assert.Equal(t, "INVALID_VALUE", res.errs[0].Extensions["code"], "the SDL promises INVALID_VALUE for a limit out of range")
	res = serve(t, ctx, `query { calculatedBackfillRoster(profileToken: "prof", constantKeys: [], deviceTokens: ["ghost"], limit: 5) { total } }`, nil)
	require.Len(t, res.errs, 1)
	assert.Contains(t, res.errs[0].Message, "ghost")
	assert.Equal(t, "INVALID_VALUE", res.errs[0].Extensions["code"])
}
