// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
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

// The caps count BYTES, not runes: a value of multibyte runes that is under the cap in
// characters but over it in bytes is refused, and one that lands exactly on the cap passes.
func TestCapsCountBytesNotRunes(t *testing.T) {
	api, _, ctx := newAttrEmitTestApi(t)
	const euro = "€" // three bytes

	atCap := strings.Repeat(euro, MaxAttributeValueBytes/3) + "v" // 65535 + 1 = 65536 bytes
	require.Len(t, atCap, MaxAttributeValueBytes)
	_, err := api.SetEntityAttribute(ctx, attrRequest("k", atCap))
	require.NoError(t, err, "a value exactly at the byte cap was refused")

	over := strings.Repeat(euro, MaxAttributeValueBytes/3+1) // 21846 runes, 65538 bytes
	require.Less(t, len([]rune(over)), MaxAttributeValueBytes, "the case must be under the cap in runes")
	_, err = api.SetEntityAttribute(ctx, attrRequest("k2", over))
	requireLimitExceeded(t, err, "attribute value")

	_, err = api.SetEntityAttribute(ctx, attrRequest(strings.Repeat(euro, MaxAttributeKeyBytes/3+1), "v"))
	requireLimitExceeded(t, err, "attribute key")
}

// The update path enforces the same rule caps as create, and a refused update changes
// nothing.
func TestUpdateDetectionRuleCapsDefinitionAndGraph(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedDeviceProfile(t, api, ctx, "p1")
	_, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
		Token: "r1", DeviceProfileToken: "p1", Definition: ruleDef, Enabled: false})
	require.NoError(t, err)

	big := `{"type":"threshold","pad":"` + strings.Repeat("€", MaxDetectionRuleBytes/3) + `"}` // > cap in bytes
	var req DetectionRuleUpdateRequest
	req.Definition.Set, req.Definition.Value = true, &big
	_, err = api.UpdateDetectionRule(ctx, "r1", &req)
	requireLimitExceeded(t, err, "detection rule definition")

	graph := `{"nodes":"` + strings.Repeat("€", MaxDetectionRuleBytes/3) + `"}`
	var req2 DetectionRuleUpdateRequest
	req2.AuthoringGraph.Set, req2.AuthoringGraph.Value = true, &graph
	_, err = api.UpdateDetectionRule(ctx, "r1", &req2)
	requireLimitExceeded(t, err, "detection rule authoring graph")

	rules, err := api.DetectionRulesByToken(ctx, []string{"r1"})
	require.NoError(t, err)
	require.JSONEq(t, ruleDef, string(rules[0].Definition), "a refused update changed the stored definition")
	require.Empty(t, rules[0].AuthoringGraph, "a refused update stored a graph")
}

// A limit below 1 or an offset below 0 is refused with a typed answer, not read as the default.
func TestVersionListRefusesNonsensePaging(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedDeviceProfile(t, api, ctx, "p1")
	for name, args := range map[string]*VersionListArgs{
		"zero limit":      {Limit: i32(0)},
		"negative limit":  {Limit: i32(-3)},
		"negative offset": {Offset: i32(-1)},
	} {
		_, err := api.DeviceProfileVersions(ctx, "p1", args)
		var bad *InvalidArgumentError
		require.True(t, errors.As(err, &bad), "%s: got %v, want InvalidArgumentError", name, err)
		require.Equal(t, "INVALID_VALUE", bad.Extensions()["code"], name)
	}
}

// The asset-type history pages the same way: an offset skips the newest versions.
func TestAssetTypeVersionsOffset(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	at := seedTypeWithSchema(t, api, ctx, "pump", "", false)
	rows := make([]*AssetTypeVersion, 0, 5)
	for v := 1; v <= 5; v++ {
		rows = append(rows, &AssetTypeVersion{AssetTypeId: at.ID, Version: int32(v), PropertySchema: datatypes.JSON(`[]`)})
	}
	bulkVersions(t, api, ctx, rows)

	got, err := api.AssetTypeVersions(ctx, "pump", &VersionListArgs{Limit: i32(2), Offset: i32(1)})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.EqualValues(t, 4, got[0].Version)
	require.EqualValues(t, 3, got[1].Version)
}

// A command definition's key is the name the platform enqueues, and command-delivery
// refuses names over 128 bytes; the key grammar's own length cap must not exceed it, or a
// command could be authored that can never be enqueued.
func TestCommandKeyCapMatchesTheEnqueueCap(t *testing.T) {
	const enqueueNameCap = 128 // command-delivery's MaxCommandNameLength
	require.Equal(t, enqueueNameCap, core.MaxTokenLen)
	require.NoError(t, validateCommandKey(strings.Repeat("c", enqueueNameCap)))
	require.Error(t, validateCommandKey(strings.Repeat("c", enqueueNameCap+1)))
}
