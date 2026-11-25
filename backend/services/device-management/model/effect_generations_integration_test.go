// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

type membershipBatchLockRole struct{}

// Both retained heads and missing heads contend when two callers add the same members.
// Park the first batch after its first edge, then observe the second waiting in
// PostgreSQL before releasing it. With request-order upserts (even with a SELECT
// pre-lock of existing heads), the missing-head case owns the opposite head at
// this point, so resuming the first creates a 40P01 cycle.
func TestIntegrationStaticMembershipOppositeBatchOrder(t *testing.T) {
	t.Run("existing-heads", func(t *testing.T) {
		testStaticMembershipOppositeBatchOrder(t, 7)
	})
	t.Run("missing-heads", func(t *testing.T) {
		testStaticMembershipOppositeBatchOrder(t, 0)
	})
}

func testStaticMembershipOppositeBatchOrder(t *testing.T, initialGeneration int64) {
	t.Helper()
	api, ctx, first := attributeLockFixture(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	other, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	group, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "fleet", MemberType: "device"})
	require.NoError(t, err)
	_, err = api.EnsureMembershipType(ctx)
	require.NoError(t, err)
	db := api.RDB.DB(ctx)
	if initialGeneration > 0 {
		for _, id := range []uint{first.ID, other.ID} {
			require.NoError(t, db.Create(&StaticMembershipGeneration{GroupId: group.ID,
				MemberType: "device", MemberId: id, Generation: initialGeneration}).Error)
		}
	} else {
		var count int64
		require.NoError(t, db.Model(&StaticMembershipGeneration{}).Where("group_id = ?", group.ID).Count(&count).Error)
		require.Zero(t, count, "neither member has a head before the concurrent adds")
	}
	member := func(token, target string) *EntityRelationshipCreateRequest {
		return &EntityRelationshipCreateRequest{Token: token, SourceType: "group", Source: group.Token,
			TargetType: "device", Target: target, RelationshipType: MembershipRelationshipType}
	}
	firstWritten, release := make(chan struct{}), make(chan struct{})
	secondPID := make(chan int, 1)
	var releaseOnce, pidOnce sync.Once
	var workers sync.WaitGroup
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	name := "test:membership-batch-order"
	// Capture the second transaction's PID before its first head upsert.
	beforeHead := func(tx *gorm.DB) {
		if tx.Statement.Table != "static_membership_generations" ||
			tx.Statement.Context.Value(membershipBatchLockRole{}) != "second" {
			return
		}
		pidOnce.Do(func() {
			var pid int
			err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error
			if err != nil {
				tx.AddError(err)
				return
			}
			secondPID <- pid
		})
	}
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register(name+"-pid", beforeHead))
	require.NoError(t, api.RDB.Database.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Table != "entity_relationships" ||
			tx.Statement.Context.Value(membershipBatchLockRole{}) != "first" {
			return
		}
		if tx.Statement.Dest.(*EntityRelationship).Token != "first-a" {
			return
		}
		close(firstWritten)
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}))
	t.Cleanup(func() {
		resume()
		cancel()
		workers.Wait()
		_ = api.RDB.Database.Callback().Create().Remove(name + "-pid")
		_ = api.RDB.Database.Callback().Create().Remove(name)
	})
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := api.CreateEntityRelationships(context.WithValue(ctx, membershipBatchLockRole{}, "first"),
			[]*EntityRelationshipCreateRequest{member("first-a", first.Token), member("first-b", other.Token)})
		firstDone <- err
	}()
	attributeAwait(t, firstWritten)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := api.CreateEntityRelationships(context.WithValue(ctx, membershipBatchLockRole{}, "second"),
			[]*EntityRelationshipCreateRequest{member("second-b", other.Token), member("second-a", first.Token)})
		secondDone <- err
	}()
	attributeWaitPID(t, db, attributeAwait(t, secondPID), "transactionid")
	resume()
	firstErr, secondErr := attributeAwait(t, firstDone), attributeAwait(t, secondDone)
	require.NoError(t, firstErr, "first batch must succeed without 40P01")
	require.NoError(t, secondErr, "opposite-order batch must succeed without 40P01")
	var heads []StaticMembershipGeneration
	require.NoError(t, db.Where("group_id = ?", group.ID).Find(&heads).Error)
	require.Len(t, heads, 2)
	for _, head := range heads {
		require.Equal(t, initialGeneration+2, head.Generation, "each of the two adds advances its head exactly once")
	}
	edges, err := api.EntityRelationshipsByToken(ctx, []string{"first-a", "first-b", "second-a", "second-b"})
	require.NoError(t, err)
	require.Len(t, edges, 4)
	for _, edge := range edges {
		require.True(t, edge.MembershipGeneration.Valid)
		want := initialGeneration + 1
		if edge.Token == "second-a" || edge.Token == "second-b" {
			want++
		}
		require.Equal(t, want, edge.MembershipGeneration.Int64)
	}
}
