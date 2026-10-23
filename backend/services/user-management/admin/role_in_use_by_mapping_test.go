// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A role an identity provider maps a group to cannot be deleted from under that mapping,
// and the refusal says why in a sentence an administrator can act on (REFERENCE_VIOLATION
// on the wire, like every other "this record is still referred to").
func TestDeleteRoleRefusesARoleAnIdpMappingNames(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&iam.Role{}, &iam.Identity{}, &iam.Membership{}, &iam.IdpConnection{}, &iam.IdpRoleMapping{},
		&iam.ServiceAccount{}, &iam.ServiceAccountSecret{}))
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	s := NewService(iam.NewStore(&rdb.RdbManager{Database: db}), testSettle, testTokenHold, nil)
	ctx := context.Background()

	for _, token := range []string{"mapped", "free"} {
		_, err := s.CreateRole(ctx, RoleInput{Scope: "tenant", Token: token, Authorities: []string{}})
		require.NoError(t, err)
	}
	mapped, err := s.iam.CatalogueRoleByScopeToken(ctx, iam.ScopeTenant, "mapped")
	require.NoError(t, err)
	require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Create(&iam.IdpRoleMapping{ConnectionID: 1, TenantId: "acme", GroupValue: "ops", RoleID: mapped.ID}).Error)

	deleted, err := s.DeleteRole(ctx, "tenant", "mapped")
	require.ErrorIs(t, err, ErrRoleInUseByMapping)
	require.False(t, deleted)
	_, err = s.iam.CatalogueRoleByScopeToken(ctx, iam.ScopeTenant, "mapped")
	require.NoError(t, err, "the mapped role must still exist")

	// The control: an unmapped role deletes as before, so the refusal is the mapping.
	deleted, err = s.DeleteRole(ctx, "tenant", "free")
	require.NoError(t, err)
	require.True(t, deleted)
}
