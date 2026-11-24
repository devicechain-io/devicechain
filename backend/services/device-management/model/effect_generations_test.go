// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

func TestStateGenerationOrdinaryAPI(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	read := func() int64 {
		var generation int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&Device{}).Where("token = ?", "dev").
			Select("COALESCE(state_generation, 0)").Scan(&generation).Error)
		return generation
	}
	before := read()
	for i, enabled := range []bool{true, false, true} {
		_, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(enabled)})
		require.NoError(t, err)
		require.Equal(t, before+int64(i)+1, read())
	}
	// ABA must remain visible even though the final enabled value equals the first.
	require.NotEqual(t, before, read(), "a pre-ABA reversal has CHANGED_SINCE evidence")
	_, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err)
	require.Equal(t, before+3, read())
}

func TestStaticMembershipGenerations(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	d := seedDeviceWithClimate(t, api, ctx, "d1", "")
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "fleet", MemberType: "device"})
	require.NoError(t, err)
	request := &EntityRelationshipCreateRequest{Token: "edge", SourceType: "group", Source: "fleet",
		TargetType: "device", Target: "d1", RelationshipType: MembershipRelationshipType}
	read := func(want int64) {
		var generation int64
		require.NoError(t, api.RDB.DB(ctx).Model(&StaticMembershipGeneration{}).
			Where("tenant_id = ? AND group_id = ? AND member_type = ? AND member_id = ?", "acme", g.ID, "device", d).
			Pluck("generation", &generation).Error)
		require.Equal(t, want, generation)
	}
	_, err = api.CreateEntityRelationship(ctx, request)
	require.NoError(t, err)
	read(1)
	assertEdge := func(want int64) {
		edges, err := api.EntityRelationshipsByToken(ctx, []string{"edge"})
		require.NoError(t, err)
		require.Len(t, edges, 1)
		require.True(t, edges[0].MembershipGeneration.Valid)
		require.Equal(t, want, edges[0].MembershipGeneration.Int64)
	}
	assertEdge(1)
	var stateBefore int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("id = ?", d).
		Select("COALESCE(state_generation, 0)").Scan(&stateBefore).Error)
	_, err = api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "d1",
		Scope: string(AttributeScopeServer), AttrKey: "note", ValueType: string(AttributeValueString), Value: strp("unrelated")})
	require.NoError(t, err)
	var note string
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityAttribute{}).
		Where("entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?", "device", d, string(AttributeScopeServer), "note").
		Pluck("value", &note).Error)
	require.Equal(t, "unrelated", note)
	read(1)
	var stateAfter int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("id = ?", d).
		Select("COALESCE(state_generation, 0)").Scan(&stateAfter).Error)
	require.Equal(t, stateBefore, stateAfter)
	ok, err := api.RemoveEntityRelationship(ctx, "edge")
	require.NoError(t, err)
	require.True(t, ok)
	read(2)
	_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{request})
	require.NoError(t, err)
	read(3)
	assertEdge(3)
	ok, err = api.RemoveEntityRelationships(ctx, []string{"edge"})
	require.NoError(t, err)
	require.True(t, ok)
	read(4)
	_, err = api.RemoveEntityRelationship(ctx, "edge")
	require.NoError(t, err)
	read(4)
	bad := *request
	bad.Token, bad.Target = "bad-edge", "does-not-exist"
	_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{request, &bad})
	require.Error(t, err)
	read(4) // the first edge and its head rolled back with the refused second edge
}

func TestStateGenerationCreateWriters(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	check := func(token string) {
		var generation int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&Device{}).Where("token = ?", token).
			Select("COALESCE(state_generation, 0)").Scan(&generation).Error)
		require.EqualValues(t, 1, generation, token)
	}
	for _, enabled := range []bool{true, false} {
		token := "single-on"
		if !enabled {
			token = "single-off"
		}
		_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "dt", Enabled: boolp(enabled)})
		require.NoError(t, err)
		check(token)
	}
	_, err := f.api.CreateDevices(f.ctx, []*DeviceCreateRequest{
		{Token: "batch-on", DeviceTypeToken: "dt", Enabled: boolp(true)},
		{Token: "batch-off", DeviceTypeToken: "dt", Enabled: boolp(false)},
	})
	require.NoError(t, err)
	check("batch-on")
	check("batch-off")
	_, err = f.api.CreateDevicesFromTemplate(f.ctx, &DeviceBulkCreateRequest{
		DeviceTypeToken: "dt", Count: 1, TokenTemplate: "template-{n}",
	})
	require.NoError(t, err)
	check("template-1")
}

func TestStateGenerationImportAndRegistration(t *testing.T) {
	t.Run("import", func(t *testing.T) {
		api, ctx := newImportApi(t)
		for i, mode := range []string{DeviceImportModeNone, DeviceImportModeCredentials} {
			row := importRows(i + 1)[i]
			if mode == DeviceImportModeCredentials {
				row.CredentialType = strp(string(CredentialAccessToken))
				row.CredentialId = strp(importSecret)
			}
			result := mustImport(t, api, ctx, []*DeviceImportRow{row}, mode, false)
			require.EqualValues(t, 1, result.Created, "%+v", result.Rows)
			var generation int64
			require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", row.Token).
				Select("COALESCE(state_generation, 0)").Scan(&generation).Error)
			require.EqualValues(t, 1, generation)
		}
	})
	t.Run("registration", func(t *testing.T) {
		f := newProvFixture(t)
		f.profile("pp", ProvisionAllowNew)
		_, err := f.register("pp", "generation-device", material(1))
		require.NoError(t, err)
		var generation int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&Device{}).Where("external_id = ?", "generation-device").
			Select("COALESCE(state_generation, 0)").Scan(&generation).Error)
		require.EqualValues(t, 1, generation)
	})
}

func TestStaticMembershipGenerationCascade(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	d := seedDeviceWithClimate(t, api, ctx, "cascade-device", "")
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "cascade-group", MemberType: "device"})
	require.NoError(t, err)
	_, err = api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: "cascade-edge",
		SourceType: "group", Source: g.Token, TargetType: "device", Target: "cascade-device", RelationshipType: MembershipRelationshipType})
	require.NoError(t, err)
	removed, err := api.DeleteEntityGroup(ctx, g.Token)
	require.NoError(t, err)
	require.True(t, removed)
	var generation int64
	require.NoError(t, api.RDB.DB(ctx).Model(&StaticMembershipGeneration{}).
		Where("tenant_id = ? AND group_id = ? AND member_type = ? AND member_id = ?", "acme", g.ID, "device", d).
		Pluck("generation", &generation).Error)
	require.EqualValues(t, 2, generation, "cascade deletion preserves the removed edge's head")
}
