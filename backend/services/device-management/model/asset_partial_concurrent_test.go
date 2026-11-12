// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func assetPartialFixture(t *testing.T) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"vendor","dataType":"STRING"}]`, true)
	_, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Name: strp("Old"),
		Properties: strp(`{"vendor":"old"}`),
	})
	require.NoError(t, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func reloadPartialAsset(t *testing.T, api *Api, ctx context.Context) *Asset {
	t.Helper()
	rows, err := api.AssetsByToken(ctx, []string{"p-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}

func assetPartialWrites(statements *rdbtest.StatementCounter) []string {
	var writes []string
	for _, sql := range statements.MarkedSQL() {
		sql = strings.NewReplacer("`", "", "\"", "").Replace(sql)
		if strings.HasPrefix(sql, "UPDATE ") || strings.HasPrefix(sql, "INSERT ") || strings.HasPrefix(sql, "DELETE ") {
			writes = append(writes, sql)
		}
	}
	return writes
}

// One other writer lands after the initial asset read and its preload, before
// the update is built. A boolean makes the callback one-shot even on reloads.
func assetPartialOtherWriter(t *testing.T, api *Api, write func() error) *bool {
	t.Helper()
	fired := false
	const callback = "test:asset_partial_other_writer"
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:after_query").
		Register(callback, func(tx *gorm.DB) {
			if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "assets" {
				return
			}
			fired = true
			tx.AddError(write())
		}))
	t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove(callback) })
	return &fired
}

func TestAssetPartialConcurrentRenamePreservesProperties(t *testing.T) {
	api, ctx, statements := assetPartialFixture(t)
	before := reloadPartialAsset(t, api, ctx)
	fired := assetPartialOtherWriter(t, api, func() error {
		return api.RDB.DB(ctx).Model(&Asset{}).Where("token = ?", "p-1").
			Update("properties", `{"vendor":"new"}`).Error
	})
	updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, *fired, "the other writer never ran")
	for _, row := range []*Asset{updated, reloadPartialAsset(t, api, ctx)} {
		require.Equal(t, "Renamed", row.Name.String)
		require.NotNil(t, row.Properties)
		require.JSONEq(t, `{"vendor":"new"}`, string(*row.Properties))
		require.Equal(t, before.AssetTypeId, row.AssetTypeId)
		require.NotNil(t, row.AssetType)
		require.Equal(t, "pump", row.AssetType.Token)
	}
	nameWrites := 0
	for _, sql := range assetPartialWrites(statements) {
		require.False(t, strings.HasPrefix(sql, "INSERT INTO asset_types ") ||
			strings.HasPrefix(sql, "UPDATE asset_types "), "type association write: %s", sql)
		if !strings.HasPrefix(sql, "UPDATE assets SET ") || !strings.Contains(sql, "name=") {
			continue
		}
		nameWrites++
		for _, omitted := range []string{"properties=", "asset_type_id=", "metadata=", "description="} {
			require.NotContains(t, sql, omitted)
		}
	}
	require.Equal(t, 1, nameWrites)
}

func TestAssetPartialConcurrentClearProperties(t *testing.T) {
	for _, field := range []dcgraphql.OptionalString{dcgraphql.ClearedString(), dcgraphql.OptionalStringOf(" null ")} {
		t.Run("clear", func(t *testing.T) {
			api, ctx, _ := assetPartialFixture(t)
			before := reloadPartialAsset(t, api, ctx)
			updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{Properties: field})
			require.NoError(t, err)
			for _, row := range []*Asset{updated, reloadPartialAsset(t, api, ctx)} {
				require.Nil(t, row.Properties)
				require.Equal(t, before.Name, row.Name)
				require.Equal(t, before.AssetTypeId, row.AssetTypeId)
			}
		})
	}
}

func TestAssetPartialConcurrentInvalidRequestWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request AssetUpdateRequest
	}{
		{"unknown type", AssetUpdateRequest{AssetTypeToken: dcgraphql.OptionalStringOf("missing")}},
		{"null type", AssetUpdateRequest{AssetTypeToken: dcgraphql.ClearedString()}},
		{"blank type", AssetUpdateRequest{AssetTypeToken: dcgraphql.OptionalStringOf(" ")}},
		{"invalid properties", AssetUpdateRequest{Properties: dcgraphql.OptionalStringOf(`{"unknown":1}`)}},
		{"malformed properties", AssetUpdateRequest{Properties: dcgraphql.OptionalStringOf(`{`)}},
		{"malformed metadata", AssetUpdateRequest{Metadata: dcgraphql.OptionalStringOf(`{`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, statements := assetPartialFixture(t)
			before := reloadPartialAsset(t, api, ctx)
			tc.request.Name = dcgraphql.OptionalStringOf("Refused")
			_, err := api.UpdateAsset(ctx, "p-1", &tc.request)
			require.Error(t, err)
			require.Empty(t, assetPartialWrites(statements))
			stored := reloadPartialAsset(t, api, ctx)
			require.Equal(t, before.Name, stored.Name)
			require.True(t, before.UpdatedAt.Equal(stored.UpdatedAt))
			require.NotNil(t, stored.Properties)
			require.JSONEq(t, string(*before.Properties), string(*stored.Properties))
		})
	}
}

func TestAssetPartialEmptyWritesNothing(t *testing.T) {
	api, ctx, statements := assetPartialFixture(t)
	before := reloadPartialAsset(t, api, ctx)
	updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, assetPartialWrites(statements))
	require.True(t, before.UpdatedAt.Equal(updated.UpdatedAt))
	require.True(t, before.UpdatedAt.Equal(reloadPartialAsset(t, api, ctx).UpdatedAt))
	_, err = api.UpdateAsset(ctx, "missing", &AssetUpdateRequest{})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestAssetPartialConcurrentRenameAfterRollback(t *testing.T) {
	api, ctx, statements := assetPartialFixture(t)
	_, err := api.UpdateAssetType(ctx, "pump", &AssetTypeUpdateRequest{
		PropertySchema: dcgraphql.OptionalStringOf(`[{"name":"serial","dataType":"STRING"}]`),
	})
	require.NoError(t, err)
	_, err = api.PublishAssetType(ctx, "pump", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{
		Properties: dcgraphql.OptionalStringOf(`{"serial":"SN-1"}`),
	})
	require.NoError(t, err)
	_, err = api.RollbackAssetType(ctx, "pump", 1)
	require.NoError(t, err)
	// Start recording after the setup writes.
	statements = rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)
	for _, row := range []*Asset{updated, reloadPartialAsset(t, api, ctx)} {
		require.Equal(t, "Renamed", row.Name.String)
		require.NotNil(t, row.Properties)
		require.JSONEq(t, `{"serial":"SN-1"}`, string(*row.Properties))
	}
	for _, sql := range assetPartialWrites(statements) {
		require.NotContains(t, sql, "properties=")
		require.NotContains(t, sql, "asset_type_id=")
		require.False(t, strings.HasPrefix(sql, "INSERT INTO asset_types "))
	}
	beforeWrites := len(assetPartialWrites(statements))
	_, err = api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{
		AssetTypeToken: dcgraphql.OptionalStringOf("pump"),
	})
	require.Error(t, err, "explicit retyping must validate even when it names the current type")
	require.Len(t, assetPartialWrites(statements), beforeWrites)
}

func TestAssetPartialConcurrentRetypeValidatesCurrentProperties(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "accepted"}[compatible], func(t *testing.T) {
			api, ctx, _ := assetPartialFixture(t)
			schema := `[{"name":"vendor","dataType":"STRING","enum":["old"]}]`
			if compatible {
				schema = `[{"name":"vendor","dataType":"STRING"}]`
			}
			destination := seedTypeWithSchema(t, api, ctx, "valve", schema, true)
			statements := rdbtest.NewStatementCounter(" ")
			statements.Record(true)
			api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
			fired := assetPartialOtherWriter(t, api, func() error {
				return api.RDB.DB(ctx).Model(&Asset{}).Where("token = ?", "p-1").Update("properties", `{"vendor":"new"}`).Error
			})
			updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{AssetTypeToken: dcgraphql.OptionalStringOf("valve")})
			require.True(t, *fired)
			stored := reloadPartialAsset(t, api, ctx)
			require.NotNil(t, stored.Properties)
			require.JSONEq(t, `{"vendor":"new"}`, string(*stored.Properties))
			if !compatible {
				require.Error(t, err)
				require.Equal(t, "pump", stored.AssetType.Token)
			} else {
				require.NoError(t, err)
				require.Equal(t, destination.ID, stored.AssetTypeId)
				require.Equal(t, destination.ID, updated.AssetType.ID)
				require.NotNil(t, updated.Properties)
				require.JSONEq(t, `{"vendor":"new"}`, string(*updated.Properties))
			}
			typeWrites := 0
			for _, sql := range assetPartialWrites(statements) {
				require.False(t, strings.HasPrefix(sql, "INSERT INTO asset_types ") || strings.HasPrefix(sql, "UPDATE asset_types "))
				if strings.HasPrefix(sql, "UPDATE assets SET ") && strings.Contains(sql, "asset_type_id=") {
					typeWrites++
					require.NotContains(t, sql, "properties=")
				}
			}
			if compatible {
				require.Equal(t, 1, typeWrites)
			} else {
				require.Zero(t, typeWrites)
			}
		})
	}
}

func TestAssetPartialConcurrentPropertiesValidateCurrentType(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "accepted"}[compatible], func(t *testing.T) {
			api, ctx, _ := assetPartialFixture(t)
			destination := seedTypeWithSchema(t, api, ctx, "valve", `[{"name":"serial","dataType":"STRING"}]`, true)
			statements := rdbtest.NewStatementCounter(" ")
			statements.Record(true)
			api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
			fired := assetPartialOtherWriter(t, api, func() error {
				return api.RDB.DB(ctx).Model(&Asset{}).Where("token = ?", "p-1").Updates(map[string]any{
					"asset_type_id": destination.ID, "properties": `{"serial":"SN-1"}`,
				}).Error
			})
			document := `{"vendor":"new"}`
			if compatible {
				document = `{"serial":"SN-2"}`
			}
			updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{Properties: dcgraphql.OptionalStringOf(document)})
			require.True(t, *fired)
			stored := reloadPartialAsset(t, api, ctx)
			require.Equal(t, destination.ID, stored.AssetTypeId)
			require.NotNil(t, stored.Properties)
			if compatible {
				require.NoError(t, err)
				require.NotNil(t, updated.AssetType)
				require.Equal(t, destination.ID, updated.AssetType.ID)
				require.NotNil(t, updated.Properties)
				require.JSONEq(t, document, string(*updated.Properties))
				require.JSONEq(t, document, string(*stored.Properties))
			} else {
				require.Error(t, err, "must validate against the current type, not the initial preload")
				require.JSONEq(t, `{"serial":"SN-1"}`, string(*stored.Properties))
			}
			propertyOnlyWrites := 0
			for _, sql := range assetPartialWrites(statements) {
				require.False(t, strings.HasPrefix(sql, "INSERT INTO asset_types ") || strings.HasPrefix(sql, "UPDATE asset_types "))
				if strings.HasPrefix(sql, "UPDATE assets SET ") && !strings.Contains(sql, "asset_type_id=") {
					propertyOnlyWrites++
					require.Contains(t, sql, "properties=")
				}
			}
			if compatible {
				require.Equal(t, 1, propertyOnlyWrites)
			} else {
				require.Zero(t, propertyOnlyWrites)
			}
		})
	}
}
