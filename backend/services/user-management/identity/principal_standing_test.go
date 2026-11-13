// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

func TestPrincipalStanding(t *testing.T) {
	for _, name := range []string{
		"both authorities", "missing authority", "wildcard", "scoped", "disabled membership",
		"disabled identity", "unknown principal", "another tenant", "duplicates",
	} {
		t.Run(name, func(t *testing.T) {
			db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.Identity{}, &iam.Membership{})
			rdbm := &rdb.RdbManager{Database: db}
			store := iam.NewStore(rdbm)
			mgr := NewManager(nil, rdbm, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
			ctx := context.Background()
			role := iam.Role{Scope: iam.ScopeTenant, Token: "reviewers", Authorities: []string{string(auth.DeviceWrite), string(auth.AlarmWrite)}}
			if name == "missing authority" {
				role.Authorities = []string{string(auth.DeviceWrite)}
			}
			if name == "wildcard" {
				role.Authorities = []string{string(auth.AuthorityAll)}
			}
			require.NoError(t, store.CreateRole(ctx, &role))
			mem := iam.Membership{TenantId: "acme", Enabled: name != "disabled membership", TenantRoles: []iam.Role{role}}
			if name == "scoped" {
				mem.Scope = []string{"customer:one"}
			}
			if name == "another tenant" {
				mem.TenantId = "other"
			}
			// No approver policy or second factor is needed for principal standing.
			id := iam.Identity{Email: "person@example.invalid", Enabled: name != "disabled identity", PasswordHash: "x", Memberships: []iam.Membership{mem}}
			require.NoError(t, store.CreateIdentity(ctx, &id))
			principals := []string{id.Email}
			want := []PrincipalStanding{{Principal: id.Email, Qualifies: name == "both authorities" || name == "wildcard" || name == "duplicates"}}
			if name == "unknown principal" {
				principals = []string{"unknown@example.invalid"}
				want = []PrincipalStanding{{Principal: principals[0]}}
			}
			if name == "duplicates" {
				principals = []string{id.Email, "unknown@example.invalid", id.Email, "unknown@example.invalid"}
				want = append(want, PrincipalStanding{Principal: "unknown@example.invalid"})
			}
			got, err := mgr.PrincipalStanding(ctx, "acme", principals, []auth.Authority{auth.DeviceWrite, auth.AlarmWrite})
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestPrincipalStandingReturnsStoreError(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	mgr := NewManager(nil, rdbm, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	require.NoError(t, db.Migrator().DropTable(&iam.Identity{}))
	got, err := mgr.PrincipalStanding(context.Background(), "acme", []string{"person@example.invalid"}, []auth.Authority{auth.DeviceWrite})
	require.Error(t, err)
	require.Nil(t, got)
}
