// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"database/sql"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// The roll window, on the real server. A v1.9 pod writes devices and edges without knowing
// the generation columns: its rows carry NULL and its toggles and removals leave every head
// where it was. The replay below is the SQL such a pod issues for the parts that matter (a
// create that names no generation, an enable that names only disabled_at, a hard DELETE of
// an edge), applied to rows the new code created, since the old code is not in this tree.
//
// It pins three things the SQLite tests cannot: NULL and a missing head both read as 0 and
// the first aware write after them stores 1; the old pod's writes succeed against the new
// columns and table (nullable, no foreign keys); and the set-based head upsert of a cascade,
// with a logical edge stored twice, is valid PostgreSQL and advances each head once per row.
func TestIntegrationEffectGenerationsAcrossTheRoll(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	db := api.RDB.DB(ctx)
	stateOf := func(id uint) sql.NullInt64 {
		t.Helper()
		var rows []sql.NullInt64
		require.NoError(t, db.Model(&Device{}).Where("id = ?", id).Pluck("state_generation", &rows).Error)
		require.Len(t, rows, 1)
		return rows[0]
	}
	require.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, stateOf(dev.ID), "a create stores 1")

	// A v1.9 create names no generation; a v1.9 disable names only disabled_at.
	require.NoError(t, db.Exec("UPDATE devices SET state_generation = NULL WHERE id = ?", dev.ID).Error)
	require.NoError(t, db.Exec("UPDATE devices SET disabled_at = now() WHERE id = ?", dev.ID).Error)
	require.False(t, stateOf(dev.ID).Valid, "an unaware toggle leaves the generation alone")
	_, err := api.UpdateDevice(ctx, dev.Token, &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, stateOf(dev.ID), "NULL reads as 0; the first aware write stores 1")
	_, err = api.UpdateDevice(ctx, dev.Token, &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.EqualValues(t, 2, stateOf(dev.ID).Int64, "a same-value write still bumps")

	other, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	group, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "fleet", MemberType: "device"})
	require.NoError(t, err)
	member := func(token, target string) *EntityRelationshipCreateRequest {
		return &EntityRelationshipCreateRequest{Token: token, SourceType: "group", Source: "fleet",
			TargetType: "device", Target: target, RelationshipType: MembershipRelationshipType}
	}
	_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{
		member("edge-a", "dev"), member("edge-b", "dev"), member("edge-c", "other")})
	require.NoError(t, err)
	headOf := func(memberId uint) int64 {
		t.Helper()
		var heads []int64
		require.NoError(t, db.Model(&StaticMembershipGeneration{}).
			Where("group_id = ? AND member_type = ? AND member_id = ?", group.ID, "device", memberId).
			Pluck("generation", &heads).Error)
		if len(heads) == 0 {
			return 0
		}
		return heads[0]
	}
	require.EqualValues(t, 2, headOf(dev.ID), "a logical edge stored twice was written twice")
	require.EqualValues(t, 1, headOf(other.ID))

	// A v1.9 add of edge-c: no stamp and no head. A v1.9 removal of edge-b: a hard DELETE
	// that leaves the head where it was.
	require.NoError(t, db.Exec("UPDATE entity_relationships SET membership_generation = NULL WHERE token = ?", "edge-c").Error)
	require.NoError(t, db.Where("group_id = ? AND member_id = ?", group.ID, other.ID).Delete(&StaticMembershipGeneration{}).Error)
	require.NoError(t, db.Exec("DELETE FROM entity_relationships WHERE token = ?", "edge-b").Error)
	require.EqualValues(t, 2, headOf(dev.ID), "an unaware removal leaves the head alone")
	require.EqualValues(t, 0, headOf(other.ID))

	// The new pod's cascade advances an existing head and creates a missing one at 0 + 1,
	// in one multi-row upsert.
	removed, err := api.DeleteEntityGroup(ctx, "fleet")
	require.NoError(t, err)
	require.True(t, removed)
	require.EqualValues(t, 3, headOf(dev.ID))
	require.EqualValues(t, 1, headOf(other.ID), "a missing head reads as 0; the first aware write stores 1")
}
