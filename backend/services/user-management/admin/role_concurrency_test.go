// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

func TestRoleRefusesCallersStaleVersion(t *testing.T) {
	s := newPartialUpdateService(t, &iam.Role{})
	ctx := context.Background()
	r, err := s.CreateRole(ctx, RoleInput{Scope: "system", Token: "ops", Name: "original"})
	require.NoError(t, err)
	old := r.UpdatedAt.Format(time.RFC3339Nano)
	r, err = s.UpdateRole(ctx, "system", "ops", &RoleUpdateRequest{
		Name: dcgraphql.OptionalStringOf("winner"),
	}, nil)
	require.NoError(t, err)
	require.NotEqual(t, old, r.UpdatedAt.Format(time.RFC3339Nano))
	_, err = s.UpdateRole(ctx, "system", "ops", &RoleUpdateRequest{
		Name: dcgraphql.OptionalStringOf("loser"),
	}, &old)
	require.ErrorIs(t, err, iam.ErrRoleConflict)
	stored, err := s.iam.CatalogueRoleByScopeToken(ctx, iam.ScopeSystem, "ops")
	require.NoError(t, err)
	require.Equal(t, "winner", stored.Name.String)
}
