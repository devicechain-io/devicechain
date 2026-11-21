// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func facetImportFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api, ctx := newFacetAuthoringTestApi(t)
	newDevice(t, api, ctx, "d1")
	newDevice(t, api, ctx, "d2")
	declareFacet(t, api, ctx, "floors", "LONG")
	declareFacet(t, api, ctx, "climate", "STRING")
	return api, ctx
}

func facetOutcomes(rows []*FacetValueImportRowResult) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.Outcome
	}
	return out
}

func TestFacetImportDryRunRerunAndTypedBrowse(t *testing.T) {
	api, ctx := facetImportFixture(t)
	rows := []*FacetValueImportRow{
		{EntityType: "device", Token: "d1", Key: "floors", Value: "3.0"},
		{EntityType: "device", Token: "d2", Key: "floors", Value: "bad"},
	}
	preview, err := api.ImportFacetValues(ctx, rows, true)
	require.NoError(t, err)
	require.Equal(t, []string{"SET", "INVALID"}, facetOutcomes(preview))
	require.Zero(t, countOf(t, api, ctx, &EntityAttribute{}))
	actual, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, preview, actual)
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["floors"] == 3`))
	var before EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).First(&before).Error)
	require.Equal(t, "SHARED", before.Scope)
	require.Equal(t, "LONG", before.ValueType)
	require.Equal(t, "3", before.Value.String)
	rows[1].Value = "4"
	again, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"EXISTS", "SET"}, facetOutcomes(again))
	var after EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).First(&after, before.ID).Error)
	require.Equal(t, before, after, "an identical row must not be written again")
	require.Equal(t, int64(2), countOf(t, api, ctx, &EntityAttribute{}))
	rows[0].Value = "5"
	changed, err := api.ImportFacetValues(ctx, rows[:1], false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET"}, facetOutcomes(changed))
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["floors"] == 5`))
}

func TestFacetImportValidatesEveryRowAndBounds(t *testing.T) {
	api, ctx := facetImportFixture(t)
	declareFacet(t, api, ctx, "json", "JSON")
	rows := []*FacetValueImportRow{
		{EntityType: "device", Token: "d1", Key: "climate", Value: "arid"},
		{EntityType: "device", Token: "d1", Key: "climate", Value: "cold"},
		{EntityType: "device", Token: "absent", Key: "climate", Value: "arid"},
		{EntityType: "device", Token: "d1", Key: "undeclared", Value: "arid"},
		{EntityType: "group", Token: "d1", Key: "climate", Value: "arid"},
		{EntityType: "device", Token: "d1", Key: "floors", Value: "12345678901234567890"},
		{EntityType: "device", Token: "d1", Key: "json", Value: "bad json"},
		{EntityType: "device", Token: "d2", Key: "climate", Value: strings.Repeat("x", MaxAttributeValueBytes+1)},
		nil,
	}
	got, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET", "INVALID", "INVALID", "INVALID", "INVALID", "INVALID", "INVALID", "INVALID", "INVALID"}, facetOutcomes(got))
	for i, row := range got {
		require.Equal(t, int32(i+1), row.Row)
		if i > 0 {
			require.NotNil(t, row.Code)
			require.NotEmpty(t, *row.Message)
		}
	}
	require.Equal(t, "DUPLICATE_IN_FILE", *got[1].Code)
	require.Equal(t, int64(1), countOf(t, api, ctx, &EntityAttribute{}))
	_, err = api.ImportFacetValues(ctx, nil, false)
	require.Error(t, err)
	_, err = api.ImportFacetValues(context.Background(), rows, false)
	require.Error(t, err)
	large := make([]*FacetValueImportRow, 1001)
	_, err = api.ImportFacetValues(ctx, large, false)
	require.ErrorContains(t, err, "1000")
	_, err = api.ImportFacetValues(ctx, large[:1000], true)
	require.NoError(t, err, "the cap itself is accepted")
}

func TestFacetImportTenantIsolationAndAllFamilies(t *testing.T) {
	api, ctx := facetImportFixture(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&Asset{}, &Area{}, &Customer{}))
	asset, area, customer := &Asset{}, &Area{}, &Customer{}
	asset.Token, area.Token, customer.Token = "a1", "a1", "a1"
	for _, m := range []any{asset, area, customer} {
		require.NoError(t, api.RDB.DB(ctx).Create(m).Error)
	}
	rows := []*FacetValueImportRow{{EntityType: "device", Token: "d1", Key: "climate", Value: "arid"}}
	for _, family := range []string{"asset", "area", "customer"} {
		_, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: family, Key: "climate", ValueType: "STRING"})
		require.NoError(t, err)
		rows = append(rows, &FacetValueImportRow{EntityType: family, Token: "a1", Key: "climate", Value: "arid"})
	}
	got, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET", "SET", "SET", "SET"}, facetOutcomes(got))
	other := core.WithTenant(context.Background(), "other")
	got, err = api.ImportFacetValues(other, rows[:1], false)
	require.NoError(t, err)
	require.Equal(t, "facet key is not declared for this member family", *got[0].Message,
		"another tenant's facet declaration must not be visible")
	_, err = api.SetFacetKey(other, &FacetKeySetRequest{MemberType: "device", Key: "climate", ValueType: "STRING"})
	require.NoError(t, err)
	got, err = api.ImportFacetValues(other, rows[:1], false)
	require.NoError(t, err)
	require.Equal(t, []string{"INVALID"}, facetOutcomes(got), "another tenant's token must not resolve")
	require.Zero(t, countOf(t, api, other, &EntityAttribute{}))
	_, err = api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "sensor"})
	require.NoError(t, err)
	newDevice(t, api, other, "d1")
	rows[0].Value = "cold"
	got, err = api.ImportFacetValues(other, rows[:1], false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET"}, facetOutcomes(got))
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["climate"] == "arid"`))
	require.Equal(t, []string{}, browseMatches(t, api, other, `attr["climate"] == "arid"`))
}

func TestFacetImportReportsWriteFailureAndRollsBackRow(t *testing.T) {
	api, ctx := facetImportFixture(t)
	// A failed membership recomputation is after the attribute insert in the row's
	// transaction. It must roll back that insert while other rows still land.
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("facet-import-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_group_facet_refs" && strings.Contains(fmt.Sprint(tx.Statement.Vars), "floors") {
			tx.AddError(fmt.Errorf("injected reconciliation failure"))
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Query().Remove("facet-import-failure") })
	rows := []*FacetValueImportRow{
		{EntityType: "device", Token: "d1", Key: "climate", Value: "arid"},
		{EntityType: "device", Token: "d1", Key: "floors", Value: "3"},
		{EntityType: "device", Token: "d2", Key: "climate", Value: "cold"},
	}
	got, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET", "REFUSED", "SET"}, facetOutcomes(got))
	require.Equal(t, "IMPORT_FAILED", *got[1].Code)
	require.Equal(t, int64(2), countOf(t, api, ctx, &EntityAttribute{}))
	require.NoError(t, api.RDB.Database.Callback().Query().Remove("facet-import-failure"))
	got, err = api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"EXISTS", "SET", "EXISTS"}, facetOutcomes(got))
}

func TestFacetImportDoesNotMistakeOtherScopeOrTypeForExisting(t *testing.T) {
	api, ctx := facetImportFixture(t)
	authorValue(t, api, ctx, "d1", "CLIENT", "floors", "LONG", "3")
	authorValue(t, api, ctx, "d1", "SHARED", "floors", "STRING", "3")
	got, err := api.ImportFacetValues(ctx, []*FacetValueImportRow{{EntityType: "device", Token: "d1", Key: "floors", Value: "3"}}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET"}, facetOutcomes(got))
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["floors"] == 3`))
	var client EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).Where("scope = ?", "CLIENT").First(&client).Error)
	require.Equal(t, "3", client.Value.String)
}

func TestFacetImportTypesAndUnwritableSource(t *testing.T) {
	api, ctx := facetImportFixture(t)
	declareFacet(t, api, ctx, "enabled", "BOOLEAN")
	declareFacet(t, api, ctx, "gain", "DOUBLE")
	declareFacet(t, api, ctx, "object", "JSON")
	rows := []*FacetValueImportRow{
		{EntityType: "device", Token: "d1", Key: "enabled", Value: "TRUE"},
		{EntityType: "device", Token: "d1", Key: "gain", Value: "1e3"},
		{EntityType: "device", Token: "d1", Key: "object", Value: `{"hello":1}`},
		{EntityType: "device", Token: "d1", Key: "climate", Value: " arid "},
		{EntityType: "device", Token: "d2", Key: "enabled", Value: "maybe"},
		{EntityType: "device", Token: "d2", Key: "gain", Value: "NaN"},
		{EntityType: "device", Token: "d2", Key: "climate", Value: " "},
	}
	got, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"SET", "SET", "SET", "SET", "INVALID", "INVALID", "INVALID"}, facetOutcomes(got))
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["enabled"] == true`))
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["gain"] == 1000`))
	require.Equal(t, []string{"d1"}, browseMatches(t, api, ctx, `attr["climate"] == "arid"`))
	got, err = api.ImportFacetValues(ctx, rows[:4], false)
	require.NoError(t, err)
	require.Equal(t, []string{"EXISTS", "EXISTS", "EXISTS", "EXISTS"}, facetOutcomes(got),
		"canonical values, including padded strings, must not be written again")
	require.NoError(t, api.RDB.DB(ctx).Model(&FacetKey{}).Where("attr_key = ?", "climate").Update("source", "system").Error)
	got, err = api.ImportFacetValues(ctx, rows[3:4], false)
	require.NoError(t, err)
	require.Equal(t, []string{"INVALID"}, facetOutcomes(got))
}
