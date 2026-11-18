// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Exercise the production Postgres branch without a server. Only the advisory
// statement is intercepted; the admission reads and writes still run on SQLite.
type hierarchyPostgresDialect struct{ gorm.Dialector }

func (hierarchyPostgresDialect) Name() string { return "postgres" }

func observeHierarchyLock(t *testing.T, api *Api) *int {
	t.Helper()
	db := api.RDB.Database
	db.Dialector = hierarchyPostgresDialect{db.Dialector}
	locks := new(int)
	var lockedOn gorm.ConnPool
	raw := db.Callback().Raw().Get("gorm:raw")
	require.NoError(t, db.Callback().Raw().Replace("gorm:raw", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
			require.Contains(t, tx.Statement.SQL.String(), "hashtextextended(?, 0)")
			require.Equal(t, []interface{}{"acme:asset-hierarchy"}, tx.Statement.Vars, "lock key must be stable per tenant")
			_, inTx := tx.Statement.ConnPool.(*sql.Tx)
			require.True(t, inTx, "advisory lock must be transaction scoped")
			lockedOn = tx.Statement.ConnPool
			*locks++
			return
		}
		raw(tx)
	}))
	check := func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_relationships" {
			require.Positive(t, *locks, "containment read/write happened before advisory lock")
			require.Same(t, lockedOn, tx.Statement.ConnPool, "check and insert must use the lock's transaction")
		}
	}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:hierarchy-lock", check))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:hierarchy-lock", check))
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:hierarchy-lock", check))
	return locks
}

func TestContainmentDoorsLockBeforeReading(t *testing.T) {
	for _, door := range []string{"set", "single", "bulk"} {
		t.Run(door, func(t *testing.T) {
			api, ctx := hierarchyTestApi(t)
			seedAssets(t, api, ctx, "parent", "child")
			_, err := api.EnsureContainmentType(ctx)
			require.NoError(t, err)
			locks := observeHierarchyLock(t, api)
			request := &EntityRelationshipCreateRequest{Token: "edge", RelationshipType: ContainmentRelationshipType,
				SourceType: "asset", Source: "parent", TargetType: "asset", Target: "child"}
			switch door {
			case "set":
				_, err = api.SetAssetParent(ctx, "child", "parent")
			case "single":
				_, err = api.CreateEntityRelationship(ctx, request)
			case "bulk":
				_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{request})
			}
			require.NoError(t, err)
			require.Positive(t, *locks)
		})
	}
}

// A batch whose containment edge is not its first must still lock before its first
// insert: holding an earlier edge's row and index locks while waiting for the
// hierarchy lock is the reverse of the order every other door takes them in.
func TestMixedBatchLocksBeforeItsFirstInsert(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedAssets(t, api, ctx, "parent", "child")
	_, err := api.EnsureContainmentType(ctx)
	require.NoError(t, err)
	_, err = api.EnsureMembershipType(ctx)
	require.NoError(t, err)
	locks := observeHierarchyLock(t, api)
	_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{
		{Token: "member-edge", RelationshipType: MembershipRelationshipType,
			SourceType: "asset", Source: "parent", TargetType: "asset", Target: "child"},
		{Token: "contains-edge", RelationshipType: ContainmentRelationshipType,
			SourceType: "asset", Source: "parent", TargetType: "asset", Target: "child"},
	})
	require.NoError(t, err)
	require.Positive(t, *locks)
}

func TestNonContainmentDoorsDoNotLockHierarchy(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "bulk"}[bulk], func(t *testing.T) {
			api, ctx := hierarchyTestApi(t)
			seedAssets(t, api, ctx, "parent", "child")
			_, err := api.EnsureMembershipType(ctx)
			require.NoError(t, err)
			locks := observeHierarchyLock(t, api)
			require.NoError(t, api.RDB.Database.Callback().Query().Remove("test:hierarchy-lock"))
			require.NoError(t, api.RDB.Database.Callback().Row().Remove("test:hierarchy-lock"))
			require.NoError(t, api.RDB.Database.Callback().Create().Remove("test:hierarchy-lock"))
			request := &EntityRelationshipCreateRequest{Token: "edge", RelationshipType: MembershipRelationshipType,
				SourceType: "asset", Source: "parent", TargetType: "asset", Target: "child"}
			if bulk {
				_, err = api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{request})
			} else {
				_, err = api.CreateEntityRelationship(ctx, request)
			}
			require.NoError(t, err)
			require.Zero(t, *locks)
		})
	}
}

func TestHierarchyLockRequiresTenantOnPostgres(t *testing.T) {
	api, _ := hierarchyTestApi(t)
	api.RDB.Database.Dialector = hierarchyPostgresDialect{api.RDB.Database.Dialector}
	require.ErrorIs(t, lockAssetHierarchy(api.RDB.Database.WithContext(context.Background())), core.ErrNoTenant)
}
