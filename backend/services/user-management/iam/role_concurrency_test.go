// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/stretchr/testify/require"
)

func TestRoleWriteRacingAfterRead(t *testing.T) {
	db := putest.NewSQLiteDB(t, &Role{})
	store := NewStore(&rdb.RdbManager{Database: db})
	ctx := context.Background()
	role := &Role{Scope: ScopeSystem, Token: "ops", Authorities: []string{"role:read"}}
	require.NoError(t, store.CreateRole(ctx, role))
	first, err := store.CatalogueRoleByScopeToken(ctx, ScopeSystem, "ops")
	require.NoError(t, err)
	second, err := store.CatalogueRoleByScopeToken(ctx, ScopeSystem, "ops")
	require.NoError(t, err)
	// Both writers have already loaded the same version. A read-time comparison
	// alone cannot protect the second writer from clobbering the first.
	first.Name = rdb.NullStrOf(ptrRoleName("winner"))
	require.NoError(t, store.SaveRoleIfUnmoved(ctx, first))
	second.Name = rdb.NullStrOf(ptrRoleName("loser"))
	second.Authorities = nil
	require.ErrorIs(t, store.SaveRoleIfUnmoved(ctx, second), ErrRoleConflict)
	stored, err := store.CatalogueRoleByScopeToken(ctx, ScopeSystem, "ops")
	require.NoError(t, err)
	require.Equal(t, "winner", stored.Name.String)
	require.Equal(t, []string{"role:read"}, stored.Authorities)
}

func ptrRoleName(s string) *string { return &s }

func TestUnconditionalRoleWriteAdvancesVersion(t *testing.T) {
	db := putest.NewSQLiteDB(t, &Role{})
	frozen := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	db.NowFunc = func() time.Time { return frozen }
	store := NewStore(&rdb.RdbManager{Database: db})
	ctx := context.Background()
	role := &Role{Scope: ScopeSystem, Token: "ops"}
	require.NoError(t, store.CreateRole(ctx, role))
	before := role.UpdatedAt
	require.NoError(t, store.UpdateRole(ctx, role))
	stored, err := store.CatalogueRoleByScopeToken(ctx, ScopeSystem, "ops")
	require.NoError(t, err)
	require.True(t, stored.UpdatedAt.After(before), "an unchanged clock must not preserve a version across a write")
}
