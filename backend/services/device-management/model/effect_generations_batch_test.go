// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStaticMembershipBatchAdvancesHeadsBeforeRequestOrderWrites(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	d1 := seedDeviceWithClimate(t, api, ctx, "first", "")
	d2 := seedDeviceWithClimate(t, api, ctx, "second", "")
	missing := seedDeviceWithClimate(t, api, ctx, "new", "")
	g1, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "first-group", MemberType: "device"})
	require.NoError(t, err)
	g2, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "second-group", MemberType: "device"})
	require.NoError(t, err)
	for _, key := range []staticMembershipKey{{g1.ID, "device", d1}, {g1.ID, "device", d2},
		{g1.ID, "group", g2.ID}, {g2.ID, "device", d2}} {
		require.NoError(t, api.RDB.DB(ctx).Create(&StaticMembershipGeneration{GroupId: key.groupId,
			MemberType: key.memberType, MemberId: key.memberId, Generation: 7}).Error)
	}
	member := func(token, source, targetType, target string) *EntityRelationshipCreateRequest {
		return &EntityRelationshipCreateRequest{Token: token, SourceType: "group", Source: source,
			TargetType: targetType, Target: target, RelationshipType: MembershipRelationshipType}
	}
	requests := []*EntityRelationshipCreateRequest{
		member("edge-0", g2.Token, "device", "second"),
		member("edge-1", g1.Token, "group", g2.Token),
		member("edge-2", g1.Token, "device", "second"),
		member("edge-3", g1.Token, "device", "first"),
		// One head advance, two stamps of the same logical edge.
		member("edge-4", g1.Token, "device", "first"),
		// No head yet: the first add must store 1.
		member("edge-5", g1.Token, "device", "new"),
		member("edge-6", g1.Token, "device", "new"),
	}
	_, err = api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "related"})
	require.NoError(t, err)
	plain := member("edge-7", g1.Token, "device", "first")
	plain.RelationshipType = "related"
	requests = append(requests, plain)
	var events []string
	var written []StaticMembershipGeneration
	var headSQL []string
	name := fmt.Sprintf("test:batch-head-advances-%d", stmtCounterSeq.Add(1))
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register(name+"-input", func(tx *gorm.DB) {
		if tx.Statement.Table == "static_membership_generations" {
			events = append(events, "heads")
			switch rows := tx.Statement.Dest.(type) {
			case *[]StaticMembershipGeneration:
				written = append(written, (*rows)...)
			case *StaticMembershipGeneration:
				written = append(written, *rows)
			}
		}
	}))
	require.NoError(t, api.RDB.Database.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "static_membership_generations" {
			headSQL = append(headSQL, tx.Statement.SQL.String())
		}
		if tx.Statement.Table == "entity_relationships" {
			events = append(events, tx.Statement.Dest.(*EntityRelationship).Token)
		}
	}))
	t.Cleanup(func() {
		_ = api.RDB.Database.Callback().Create().Remove(name + "-input")
		_ = api.RDB.Database.Callback().Create().Remove(name)
	})
	created, err := api.CreateEntityRelationships(ctx, requests)
	require.NoError(t, err)
	require.Equal(t, []string{"heads", "edge-0", "edge-1", "edge-2", "edge-3", "edge-4", "edge-5", "edge-6", "edge-7"}, events,
		"one multi-row upsert must precede all request-ordered edge writes")
	require.Len(t, headSQL, 1)
	require.Contains(t, headSQL[0], "ON CONFLICT")
	require.Contains(t, headSQL[0], "RETURNING")
	wantKeys := []staticMembershipKey{{g1.ID, "device", d1}, {g1.ID, "device", d2}, {g1.ID, "device", missing},
		{g1.ID, "group", g2.ID}, {g2.ID, "device", d2}}
	require.Len(t, written, len(wantKeys))
	for i, head := range written {
		require.Equal(t, wantKeys[i], staticMembershipKey{head.GroupId, head.MemberType, head.MemberId}, "head writes are sorted")
		require.Equal(t, []int64{2, 1, 2, 1, 1}[i], head.Generation, "each upsert adds the occurrence count, never zero")
	}
	for i, want := range []int64{8, 8, 8, 8, 9, 1, 2} {
		require.Equal(t, requests[i].Token, created[i].Token)
		require.True(t, created[i].MembershipGeneration.Valid)
		require.Equal(t, want, created[i].MembershipGeneration.Int64)
	}
	require.False(t, created[7].MembershipGeneration.Valid, "non-membership edges keep NULL")
	var heads []StaticMembershipGeneration
	require.NoError(t, api.RDB.DB(ctx).Order("group_id, member_type, member_id").Find(&heads).Error)
	require.Len(t, heads, len(wantKeys))
	for i, head := range heads {
		require.Equal(t, []int64{9, 8, 2, 8, 8}[i], head.Generation, "each add advances exactly once")
	}
	var stored []EntityRelationship
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&stored).Error)
	require.Len(t, stored, len(created))
	for i := range stored {
		require.Equal(t, created[i].MembershipGeneration, stored[i].MembershipGeneration, "stamps must be persisted")
	}
}

func TestStaticMembershipBatchRollsBackAdvancedHeads(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	first := seedDeviceWithClimate(t, api, ctx, "first", "")
	seedDeviceWithClimate(t, api, ctx, "second", "")
	seedDeviceWithClimate(t, api, ctx, "third", "")
	group, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "fleet", MemberType: "device"})
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Create(&StaticMembershipGeneration{GroupId: group.ID,
		MemberType: "device", MemberId: first, Generation: 7}).Error)
	var requests []*EntityRelationshipCreateRequest
	for _, token := range []string{"first", "second", "third"} {
		requests = append(requests, &EntityRelationshipCreateRequest{Token: "edge-" + token, SourceType: "group",
			Source: group.Token, TargetType: "device", Target: token, RelationshipType: MembershipRelationshipType})
	}
	requests[1].Metadata = strp("invalid JSON") // fails inside the loop, after the first edge
	var headsAtFirstEdge []StaticMembershipGeneration
	var attempted []string
	name := fmt.Sprintf("test:batch-head-rollback-%d", stmtCounterSeq.Add(1))
	require.NoError(t, api.RDB.Database.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_relationships" {
			attempted = append(attempted, tx.Statement.Dest.(*EntityRelationship).Token)
			tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Order("member_id").Find(&headsAtFirstEdge).Error)
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Create().Remove(name) })
	created, err := api.CreateEntityRelationships(ctx, requests)
	require.ErrorContains(t, err, `relationship "edge-second"`)
	require.Nil(t, created)
	require.Equal(t, []string{"edge-first"}, attempted)
	require.Len(t, headsAtFirstEdge, 3, "even heads for later requests must already be advanced")
	for i, head := range headsAtFirstEdge {
		require.Equal(t, []int64{8, 1, 1}[i], head.Generation)
	}
	var heads []StaticMembershipGeneration
	require.NoError(t, api.RDB.DB(ctx).Find(&heads).Error)
	require.Len(t, heads, 1, "new heads roll back")
	require.EqualValues(t, 7, heads[0].Generation, "existing heads roll back")
	var edges int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityRelationship{}).Count(&edges).Error)
	require.Zero(t, edges, "the earlier edge rolls back with the heads")
}
