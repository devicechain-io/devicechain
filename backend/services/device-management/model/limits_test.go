// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func requireLimitExceeded(t *testing.T, err error, what string) {
	t.Helper()
	var limit *LimitExceededError
	require.True(t, errors.As(err, &limit), "got %v, want LimitExceededError for %s", err, what)
	require.Equal(t, "LIMIT_EXCEEDED", limit.Extensions()["code"])
	require.Contains(t, limit.Error(), what)
}

func attrRequest(key, value string) *EntityAttributeSetRequest {
	return &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: "d1", Scope: string(AttributeScopeShared),
		AttrKey: key, ValueType: string(AttributeValueString), Value: &value,
	}
}

// An attribute key over 128 bytes and a value over 64 KiB are refused with the typed
// answer, and nothing is stored; the largest accepted size still goes through.
func TestSetEntityAttributeCapsKeyAndValue(t *testing.T) {
	api, _, ctx := newAttrEmitTestApi(t)

	_, err := api.SetEntityAttribute(ctx, attrRequest(strings.Repeat("k", MaxAttributeKeyBytes+1), "v"))
	requireLimitExceeded(t, err, "attribute key")
	_, err = api.SetEntityAttribute(ctx, attrRequest("big", strings.Repeat("v", MaxAttributeValueBytes+1)))
	requireLimitExceeded(t, err, "attribute value")

	_, err = api.SetEntityAttribute(ctx, attrRequest(strings.Repeat("k", MaxAttributeKeyBytes), strings.Repeat("v", MaxAttributeValueBytes)))
	require.NoError(t, err, "an input exactly at the cap was refused")

	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityAttribute{}).Count(&n).Error)
	require.EqualValues(t, 1, n, "a refused write left a row behind")
}

// A rule definition and its authoring graph are each capped at 256 KiB.
func TestDetectionRuleDefinitionAndGraphAreCapped(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedDeviceProfile(t, api, ctx, "p1")

	pad := strings.Repeat("x", MaxDetectionRuleBytes)
	oversized := `{"type":"threshold","pad":"` + pad + `"}`
	_, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
		Token: "r-big", DeviceProfileToken: "p1", Definition: oversized, Enabled: false})
	requireLimitExceeded(t, err, "detection rule definition")

	graph := `{"nodes":"` + pad + `"}`
	_, err = api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
		Token: "r-graph", DeviceProfileToken: "p1", Definition: ruleDef, Enabled: false, AuthoringGraph: &graph})
	requireLimitExceeded(t, err, "detection rule authoring graph")

	_, err = api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
		Token: "r-ok", DeviceProfileToken: "p1", Definition: ruleDef, Enabled: false})
	require.NoError(t, err)
}

func bulkVersions[T any](t *testing.T, api *Api, ctx context.Context, rows []T) {
	t.Helper()
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(rows, 200).Error)
}

func i32(v int32) *int32 { return &v }

// A version history is clamped to rdb.MaxPageSize, newest first, and the optional limit
// and offset page through the rest. Seeded past the cap: 1001 rows read back as 1000.
func TestVersionListsAreClamped(t *testing.T) {
	const total = rdb.MaxPageSize + 1

	t.Run("device profile", func(t *testing.T) {
		api, ctx := newRuleScopeTestApi(t)
		seedDeviceProfile(t, api, ctx, "p1")
		p, err := api.deviceProfileByToken(ctx, "p1")
		require.NoError(t, err)
		rows := make([]*DeviceProfileVersion, 0, total)
		for v := 1; v <= total; v++ {
			rows = append(rows, &DeviceProfileVersion{DeviceProfileId: p.ID, Version: int32(v), Snapshot: datatypes.JSON(`{}`)})
		}
		bulkVersions(t, api, ctx, rows)

		got, err := api.DeviceProfileVersions(ctx, "p1", nil)
		require.NoError(t, err)
		require.Len(t, got, rdb.MaxPageSize)
		require.EqualValues(t, total, got[0].Version, "not newest first")

		got, err = api.DeviceProfileVersions(ctx, "p1", &VersionListArgs{Limit: i32(5000)})
		require.NoError(t, err)
		require.Len(t, got, rdb.MaxPageSize, "a limit above the cap was honoured")

		got, err = api.DeviceProfileVersions(ctx, "p1", &VersionListArgs{Limit: i32(10), Offset: i32(rdb.MaxPageSize)})
		require.NoError(t, err)
		require.Len(t, got, 1, "the offset did not reach the oldest version")
		require.EqualValues(t, 1, got[0].Version)
	})

	t.Run("entity group", func(t *testing.T) {
		api, ctx := newGroupVersionTestApi(t)
		g := createDynamicGroup(t, api, ctx, "g1", `attr["climate"] == "arid"`)
		rows := make([]*EntityGroupVersion, 0, total)
		for v := 1; v <= total; v++ {
			rows = append(rows, &EntityGroupVersion{EntityGroupId: g.ID, Version: int32(v), Selector: "true", MemberType: "device"})
		}
		bulkVersions(t, api, ctx, rows)

		got, err := api.EntityGroupVersions(ctx, "g1", nil)
		require.NoError(t, err)
		require.Len(t, got, rdb.MaxPageSize)
		require.EqualValues(t, total, got[0].Version)
	})

	t.Run("asset type", func(t *testing.T) {
		api, ctx := assetPropertyTestApi(t)
		at := seedTypeWithSchema(t, api, ctx, "pump", "", false)
		rows := make([]*AssetTypeVersion, 0, total)
		for v := 1; v <= total; v++ {
			rows = append(rows, &AssetTypeVersion{AssetTypeId: at.ID, Version: int32(v), PropertySchema: datatypes.JSON(`[]`)})
		}
		bulkVersions(t, api, ctx, rows)

		got, err := api.AssetTypeVersions(ctx, "pump", nil)
		require.NoError(t, err)
		require.Len(t, got, rdb.MaxPageSize)
		require.EqualValues(t, total, got[0].Version)
	})
}
