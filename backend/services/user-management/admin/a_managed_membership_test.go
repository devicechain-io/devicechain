// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOperatorManagedMembershipEdits(t *testing.T) {
	for _, state := range []string{"served", "unmanaged", "connection-gone", "tenant-unserved"} {
		for _, operation := range []string{"disable", "remove", "roles"} {
			t.Run(state+"/"+operation, func(t *testing.T) {
				s, db := newPurgeTestServiceDB(t)
				ctx := context.Background()
				sys := db.WithContext(core.WithSystemContext(ctx))
				require.NoError(t, db.AutoMigrate(&iam.IdpConnection{}, &iam.IdpConnectionTenant{}))
				createTenant(t, s, "acme")
				_, err := s.CreateRole(ctx, RoleInput{Scope: "tenant", Token: "operator", Authorities: []string{}})
				require.NoError(t, err)
				id, err := s.CreateIdentity(ctx, CreateIdentityInput{Email: "member@example.com", Password: "hunter2hunter2", Enabled: true})
				require.NoError(t, err)
				_, err = s.AddMembership(ctx, id.Email, "acme", []string{"operator"})
				require.NoError(t, err)
				mem, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, "acme")
				require.NoError(t, err)
				if state != "unmanaged" {
					conn := &iam.IdpConnection{Token: "corporate", Kind: iam.IdpKindTenant}
					require.NoError(t, s.iam.CreateIdpConnection(ctx, conn))
					require.NoError(t, sys.Model(mem).Update("managed_by", conn.ID).Error)
					if state == "tenant-unserved" {
						require.NoError(t, sys.Create(&iam.IdpConnectionTenant{ConnectionID: conn.ID, TenantId: "other"}).Error)
					} else {
						require.NoError(t, sys.Create(&iam.IdpConnectionTenant{ConnectionID: conn.ID, TenantId: "acme"}).Error)
					}
					if state == "connection-gone" {
						require.NoError(t, sys.Delete(conn).Error)
					}
				}
				before, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, "acme")
				require.NoError(t, err)
				switch operation {
				case "disable":
					_, err = s.SetMembershipEnabled(ctx, id.Email, "acme", false)
				case "remove":
					_, err = s.RemoveMembership(ctx, id.Email, "acme")
				case "roles":
					_, err = s.SetMembershipRoles(ctx, id.Email, "acme", nil)
				}
				if state == "served" {
					require.Error(t, err)
					var refusal interface{ Extensions() map[string]any }
					require.ErrorAs(t, err, &refusal)
					require.Equal(t, CodeManagedByIdp, refusal.Extensions()["code"])
					for _, remedy := range []string{"disable the identity", "provider's groups", "stop the connection serving the tenant"} {
						require.Contains(t, err.Error(), remedy)
					}
					after, loadErr := s.iam.MembershipByIdentityTenant(ctx, id.ID, "acme")
					require.NoError(t, loadErr)
					require.Equal(t, before, after, "a refused mutation must leave the row and roles unchanged")
					return
				}
				require.NoError(t, err)
				after, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, "acme")
				if operation == "remove" {
					require.ErrorIs(t, err, gorm.ErrRecordNotFound)
					return
				}
				require.NoError(t, err)
				if operation == "disable" {
					require.False(t, after.Enabled)
				} else {
					require.Empty(t, after.TenantRoles)
				}
			})
		}
	}
}
