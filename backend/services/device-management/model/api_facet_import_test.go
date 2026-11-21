// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"hash/fnv"
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
		{MemberType: "device", Token: "d1", Key: "floors", Value: "3.0"},
		{MemberType: "device", Token: "d2", Key: "floors", Value: "bad"},
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
		{MemberType: "device", Token: "d1", Key: "climate", Value: "arid"},
		{MemberType: "device", Token: "d1", Key: "climate", Value: "cold"},
		{MemberType: "device", Token: "absent", Key: "climate", Value: "arid"},
		{MemberType: "device", Token: "d1", Key: "undeclared", Value: "arid"},
		{MemberType: "group", Token: "d1", Key: "climate", Value: "arid"},
		{MemberType: "device", Token: "d1", Key: "floors", Value: "12345678901234567890"},
		{MemberType: "device", Token: "d1", Key: "json", Value: "bad json"},
		{MemberType: "device", Token: "d2", Key: "climate", Value: strings.Repeat("x", MaxAttributeValueBytes+1)},
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
	rows := []*FacetValueImportRow{{MemberType: "device", Token: "d1", Key: "climate", Value: "arid"}}
	for _, family := range []string{"asset", "area", "customer"} {
		_, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: family, Key: "climate", ValueType: "STRING"})
		require.NoError(t, err)
		rows = append(rows, &FacetValueImportRow{MemberType: family, Token: "a1", Key: "climate", Value: "arid"})
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
		{MemberType: "device", Token: "d1", Key: "climate", Value: "arid"},
		{MemberType: "device", Token: "d1", Key: "floors", Value: "3"},
		{MemberType: "device", Token: "d2", Key: "climate", Value: "cold"},
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
	got, err := api.ImportFacetValues(ctx, []*FacetValueImportRow{{MemberType: "device", Token: "d1", Key: "floors", Value: "3"}}, false)
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
		{MemberType: "device", Token: "d1", Key: "enabled", Value: "TRUE"},
		{MemberType: "device", Token: "d1", Key: "gain", Value: "1e3"},
		{MemberType: "device", Token: "d1", Key: "object", Value: `{"hello":1}`},
		{MemberType: "device", Token: "d1", Key: "climate", Value: " arid "},
		{MemberType: "device", Token: "d2", Key: "enabled", Value: "maybe"},
		{MemberType: "device", Token: "d2", Key: "gain", Value: "NaN"},
		{MemberType: "device", Token: "d2", Key: "climate", Value: " "},
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

func TestFacetImportPreviewsConfigurationRefusals(t *testing.T) {
	api, ctx := configTestApi(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&FacetKey{}))
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "interval", ValueType: "LONG"}})
	declareFacet(t, api, ctx, "interval", "STRING")
	rows := []*FacetValueImportRow{{MemberType: "device", Token: "dev", Key: "interval", Value: "fast"}}
	before := countRevisions(t, api, ctx)
	for _, dryRun := range []bool{true, false} {
		got, err := api.ImportFacetValues(ctx, rows, dryRun)
		require.NoError(t, err)
		require.Equal(t, []string{"REFUSED"}, facetOutcomes(got))
		require.Equal(t, CodeConfigurationValueType, *got[0].Code)
		require.NotContains(t, *got[0].Message, "fast")
		require.NotContains(t, *got[0].Message, "re-run")
		require.Zero(t, countOf(t, api, ctx, &EntityAttribute{}))
		require.Equal(t, before, countRevisions(t, api, ctx))
	}
	_, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "device", Key: "interval", ValueType: "LONG"})
	require.NoError(t, err)
	rows[0].Value = "30"
	preview, err := api.ImportFacetValues(ctx, rows, true)
	require.NoError(t, err)
	require.Equal(t, []string{"SET"}, facetOutcomes(preview))
	require.Equal(t, before, countRevisions(t, api, ctx))
	actual, err := api.ImportFacetValues(ctx, rows, false)
	require.NoError(t, err)
	require.Equal(t, preview, actual)
	require.Equal(t, before+1, countRevisions(t, api, ctx))
}

func TestFacetImportPreservesSafeWriteRefusal(t *testing.T) {
	api, ctx := facetImportFixture(t)
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register("test:typed-refusal", func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_attributes" {
			tx.AddError(fmt.Errorf("wrapped: %w", &ConfigurationRefusal{Code: CodeConfigurationTooLarge, Message: "private database detail"}))
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Create().Remove("test:typed-refusal") })
	got, err := api.ImportFacetValues(ctx, []*FacetValueImportRow{{MemberType: "device", Token: "d1", Key: "floors", Value: "3"}}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"REFUSED"}, facetOutcomes(got))
	require.Equal(t, CodeConfigurationTooLarge, *got[0].Code)
	require.NotContains(t, *got[0].Message, "private")
	require.NotContains(t, *got[0].Message, "re-run")
}

func TestFacetImportPreviewsConfigurationSizeLimit(t *testing.T) {
	api, ctx := configTestApi(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&FacetKey{}))
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	declareFacet(t, api, ctx, "mode", "STRING")
	rows := []*FacetValueImportRow{{MemberType: "device", Token: "dev", Key: "mode", Value: strings.Repeat("x", MaxAttributeValueBytes)}}
	before := countRevisions(t, api, ctx)
	for _, dryRun := range []bool{true, false} {
		got, err := api.ImportFacetValues(ctx, rows, dryRun)
		require.NoError(t, err)
		require.Equal(t, []string{"REFUSED"}, facetOutcomes(got))
		require.Equal(t, CodeConfigurationTooLarge, *got[0].Code)
		require.Zero(t, countOf(t, api, ctx, &EntityAttribute{}))
		require.Equal(t, before, countRevisions(t, api, ctx))
	}
}

func TestFacetDeclarationAndImportShareTransactionLock(t *testing.T) {
	for _, operation := range []string{"import", "preview", "set", "delete"} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refuse=%t", operation, refuse), func(t *testing.T) {
				api, ctx := facetImportFixture(t)
				db := api.RDB.Database
				db.Dialector = membershipPostgresDialect{db.Dialector}
				locked, reads, writes := false, 0, 0
				var pool gorm.ConnPool
				require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:facet-lock", func(tx *gorm.DB) {
					if !strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
						return
					}
					if !locked {
						// The first lock must address the same tenant/family/key on every door.
						require.Equal(t, []any{facetImportLockID("acme", "device", "floors")}, tx.Statement.Vars)
						_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
						require.True(t, inTx, "declaration lock must last until commit")
						pool = tx.Statement.ConnPool
						locked = true
						if refuse {
							tx.AddError(fmt.Errorf("lock refused"))
						}
					}
					tx.Statement.SQL.Reset()
					tx.Statement.SQL.WriteString("SELECT 1")
					tx.Statement.Vars = nil
				}))
				checkWrite := func(tx *gorm.DB) {
					if tx.Statement.Table == "facet_keys" || tx.Statement.Table == "entity_attributes" {
						writes++
						require.True(t, locked, "write before declaration lock")
						require.False(t, refuse, "write after lock refusal")
						require.Equal(t, pool, tx.Statement.ConnPool, "lock must be held through the write")
					}
				}
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:facet-write", checkWrite))
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:facet-write", checkWrite))
				require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:facet-write", checkWrite))
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:facet-read", func(tx *gorm.DB) {
					if tx.Statement.Table == "facet_keys" || tx.Statement.Table == "entity_attributes" {
						reads++
						require.True(t, locked, "read before declaration lock")
						require.False(t, refuse, "read after lock refusal")
						require.Equal(t, pool, tx.Statement.ConnPool)
					}
				}))
				switch operation {
				case "import", "preview":
					got, err := api.ImportFacetValues(ctx, []*FacetValueImportRow{{MemberType: "device", Token: "d1", Key: "floors", Value: "3"}}, operation == "preview")
					require.NoError(t, err)
					if refuse {
						require.Equal(t, []string{"REFUSED"}, facetOutcomes(got))
					} else {
						require.Equal(t, []string{"SET"}, facetOutcomes(got))
					}
				case "set":
					_, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "device", Key: "floors", ValueType: "STRING"})
					require.Equal(t, refuse, err != nil)
				case "delete":
					_, err := api.DeleteFacetKey(ctx, "device", "floors")
					require.Equal(t, refuse, err != nil)
				}
				require.True(t, locked)
				if refuse {
					require.Zero(t, reads)
				} else {
					require.Positive(t, reads)
				}
				if refuse || operation == "preview" {
					require.Zero(t, writes)
				} else {
					require.Positive(t, writes)
				}
			})
		}
	}
}

// Deliberately independent of the production lock helper.
func facetImportLockID(tenant, family, key string) int64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00facet-key\x00%s\x00%s", tenant, family, key)
	return int64(h.Sum64())
}
