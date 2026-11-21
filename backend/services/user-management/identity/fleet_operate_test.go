// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"slices"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// Every enabled member gets the viewer baseline, but fleet-wide changes require
// an explicit role decision. The generic :read suffix test catches this spelling
// today; the absence is policy, independent of the authority's suffix.
func TestFleetOperateIsNotInTheViewerBaseline(t *testing.T) {
	if slices.Contains(viewerAuthorities, string(auth.FleetOperate)) {
		t.Fatal("the viewer baseline must not grant fleet:operate")
	}
	if slices.Contains(effectiveAuthorities(nil, false), string(auth.FleetOperate)) {
		t.Fatal("a roleless member must not receive fleet:operate")
	}
}

// Shipping vocabulary must not silently opt any built-in role into fleet changes.
// Inspect the actual startup seed and its replay; wildcard administrator roles
// already satisfy the authority without explicitly seeding its name.
func TestFleetOperateIsNotSeededIntoAnyBuiltInRole(t *testing.T) {
	f := newSeedFixture(t)
	ctx := context.Background()
	for _, phase := range []string{"first startup", "restart"} {
		t.Run(phase, func(t *testing.T) {
			require.NoError(t, f.manager("seed-password").Initialize(ctx, &stubRefreshKV{}, nil))
			roles, err := iam.NewStore(f.rdbm).ListCatalogueRoles(ctx, nil)
			require.NoError(t, err)
			require.Len(t, roles, 3, "inspect the superuser, tenant-admin and viewer seeds")
			for _, role := range roles {
				require.NotContains(t, role.Authorities, string(auth.FleetOperate),
					"built-in role %s must not explicitly seed fleet:operate", role.Token)
			}
		})
	}
}
