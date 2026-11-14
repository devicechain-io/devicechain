// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

const solarCampaignTrigger = `{"kind":"SOLAR","zone":"UTC","event":"SUNSET"}`
const cronCampaignTrigger = `{"kind":"CRON","zone":"UTC","cron":"0 18 * * *"}`
const calendarCampaignTrigger = `{"kind":"CALENDAR","zone":"UTC","dates":["2026-06-21"],"times":["18:00"]}`

func campaignMember(ctx context.Context, write bool, location string) context.Context {
	command := auth.CommandRead
	if write {
		command = auth.CommandWrite
	}
	held := []string{string(command), string(auth.DeviceRead)}
	if location != "" {
		held = append(held, location)
	}
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: held})
}

func newCampaignWireCtx(t *testing.T) (context.Context, *model.Api) {
	t.Helper()
	ctx, api := newWireTestCtx(t)
	db, err := api.RDB.Database.DB()
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&model.Campaign{}, &model.CampaignCycle{}, &model.CampaignElement{}))
	return ctx, api
}

// Seed planned rows directly: these tests exercise the read boundary, without making
// a live device-management call or depending on the scheduler's wall clock.
func seedCampaignTracking(t *testing.T, ctx context.Context, api *model.Api, trigger string) {
	t.Helper()
	name := "dim"
	c, err := api.CreateCampaign(ctx, &model.CampaignCreateRequest{
		Token: "campaign", Trigger: trigger, GroupToken: "lights",
		Steps: []model.CampaignStepInput{{Type: "COMMAND", Name: &name}},
	})
	require.NoError(t, err)
	cy := &model.CampaignCycle{CampaignId: c.ID, CycleKey: "cycle", Resolved: 2}
	require.NoError(t, api.RDB.DB(ctx).Create(cy).Error)
	for _, e := range []model.CampaignElement{
		{DeviceToken: "light", DueAt: time.Date(2026, 6, 21, 20, 13, 47, 0, time.UTC), Status: string(model.ElementPending)},
		{DeviceToken: "no-site", DueAt: time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), Status: string(model.ElementSkipped), Reason: sql.NullString{String: "NO_SITE", Valid: true}},
	} {
		e.CampaignId, e.CycleId = c.ID, cy.ID
		require.NoError(t, api.RDB.DB(ctx).Create(&e).Error)
	}
}

const campaignElementsQuery = `{ campaignElements(campaignToken: "campaign", cycleKey: "cycle") { deviceToken dueAt reason } }`

func requireCampaignForbidden(t *testing.T, res *gql.Response) {
	t.Helper()
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrForbidden), "got %v", res.Errors)
	// A non-null list refuses the query instead of withholding fields in its rows.
	require.JSONEq(t, `null`, string(res.Data))
}

func TestSolarCampaignElementsNeedInstalledPositionAuthority(t *testing.T) {
	ctx, api := newCampaignWireCtx(t)
	seedCampaignTracking(t, ctx, api, solarCampaignTrigger)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	requireCampaignForbidden(t, schema.Exec(campaignMember(ctx, false, ""), campaignElementsQuery, "", nil))
	for _, authority := range []string{"location:installed:read", "location:read"} {
		t.Run(authority, func(t *testing.T) {
			res := schema.Exec(campaignMember(ctx, false, authority), campaignElementsQuery, "", nil)
			require.Empty(t, res.Errors)
			require.JSONEq(t, `{"campaignElements":[{"deviceToken":"light","dueAt":"2026-06-21T20:13:47Z","reason":null},{"deviceToken":"no-site","dueAt":"2026-06-21T00:00:00Z","reason":"NO_SITE"}]}`, string(res.Data))
		})
	}
}

func TestCreateSolarCampaignNeedsInstalledPositionAuthority(t *testing.T) {
	ctx, api := newCampaignWireCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	const mutation = `mutation($request: CampaignCreateRequest!) { createCampaign(request: $request) { campaign { token } rejection { code } } }`
	vars := map[string]any{"request": map[string]any{
		"token": "solar", "trigger": solarCampaignTrigger, "groupToken": "lights",
		"steps": []any{map[string]any{"type": "COMMAND", "name": "dim"}},
	}}
	requireCampaignForbidden(t, schema.Exec(campaignMember(ctx, true, ""), mutation, "", vars))
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&model.Campaign{}).Count(&count).Error)
	require.Zero(t, count, "refused creation must store nothing")
	res := schema.Exec(campaignMember(ctx, true, "location:installed:read"), mutation, "", vars)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"createCampaign":{"campaign":{"token":"solar"},"rejection":null}}`, string(res.Data))
	require.NoError(t, api.RDB.DB(ctx).Model(&model.Campaign{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestSolarCampaignCyclesAndRetryNeedInstalledPositionAuthority(t *testing.T) {
	ctx, api := newCampaignWireCtx(t)
	seedCampaignTracking(t, ctx, api, solarCampaignTrigger)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, op := range []struct {
		name, query string
		write       bool
	}{
		{"cycles", `{ campaignCycles(campaignToken: "campaign") { key counts { skipped } } }`, false},
		{"retry", `mutation { retryCampaignFailed(campaignToken: "campaign", cycleKey: "cycle", includeUnknown: false) { retried held batches } }`, true},
	} {
		t.Run(op.name, func(t *testing.T) {
			requireCampaignForbidden(t, schema.Exec(campaignMember(ctx, op.write, ""), op.query, "", nil))
			for _, authority := range []string{"location:installed:read", "location:read"} {
				res := schema.Exec(campaignMember(ctx, op.write, authority), op.query, "", nil)
				require.Empty(t, res.Errors)
			}
		})
	}
	// Definition metadata has no per-device solar instants or site-specific errors.
	res := schema.Exec(campaignMember(ctx, false, ""), `{ campaign(token: "campaign") { token trigger plannedThrough lastError } campaigns { token } }`, "", nil)
	require.Empty(t, res.Errors)
}

func TestFixedCampaignsKeepExistingAuthorityGate(t *testing.T) {
	for _, trigger := range []string{cronCampaignTrigger, calendarCampaignTrigger} {
		t.Run(trigger, func(t *testing.T) {
			ctx, api := newCampaignWireCtx(t)
			// Create through the wire as a member with no location authority.
			request := map[string]any{"token": "campaign", "trigger": trigger, "groupToken": "lights", "steps": []any{map[string]any{"type": "COMMAND", "name": "dim"}}}
			data := exec(t, campaignMember(ctx, true, ""), `mutation($request: CampaignCreateRequest!) { createCampaign(request: $request) { campaign { token } rejection { code } } }`, map[string]any{"request": request})
			require.JSONEq(t, `{"createCampaign":{"campaign":{"token":"campaign"},"rejection":null}}`, string(data))
			var stored model.Campaign
			require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "campaign").First(&stored).Error)
			cy := model.CampaignCycle{CampaignId: stored.ID, CycleKey: "cycle"}
			require.NoError(t, api.RDB.DB(ctx).Create(&cy).Error)
			e := model.CampaignElement{CampaignId: stored.ID, CycleId: cy.ID, DeviceToken: "light", DueAt: time.Now().UTC().Truncate(time.Second), Status: string(model.ElementPending)}
			require.NoError(t, api.RDB.DB(ctx).Create(&e).Error)
			data = exec(t, campaignMember(ctx, false, ""), campaignElementsQuery, nil)
			var out struct {
				CampaignElements []struct{ DeviceToken string }
			}
			require.NoError(t, json.Unmarshal(data, &out))
			require.Len(t, out.CampaignElements, 1)
			require.Equal(t, "light", out.CampaignElements[0].DeviceToken)
			for _, op := range []struct {
				query string
				write bool
			}{
				{`{ campaignCycles(campaignToken: "campaign") { key counts { skipped } } }`, false},
				{`mutation { retryCampaignFailed(campaignToken: "campaign", cycleKey: "cycle", includeUnknown: false) { retried } }`, true},
			} {
				exec(t, campaignMember(ctx, op.write, ""), op.query, nil)
			}
		})
	}
}
