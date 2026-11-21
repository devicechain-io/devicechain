// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// Fleet operations stay within a tenant. Mirror state:demote's existing grant
// validation, and exercise the tenant data plane rather than only the catalogue.
func TestFleetOperateRoleGrantMatchesTenantOnlyAuthority(t *testing.T) {
	for _, authority := range []auth.Authority{auth.StateDemote, auth.FleetOperate} {
		t.Run(string(authority), func(t *testing.T) {
			e := newPlaneEnv(t)
			e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
			authorities := []string{string(authority)}
			role, err := e.s.CreateTenantRole(as("boss@acme.example", "acme", "*"), TenantRoleInput{
				Token: "custom-operations", Authorities: authorities,
			})
			require.NoError(t, err)
			require.Equal(t, authorities, role.Authorities)
			require.NotNil(t, role.TenantID)
			require.Equal(t, "acme", *role.TenantID)

			// A system role must not advertise power over a tenant's own devices.
			require.Error(t, validateAuthorities(iam.ScopeSystem, authorities))
			_, err = e.s.CreateRole(context.Background(), RoleInput{
				Scope: string(iam.ScopeSystem), Token: "system-operations", Authorities: authorities,
			})
			require.Error(t, err)
		})
	}
}
