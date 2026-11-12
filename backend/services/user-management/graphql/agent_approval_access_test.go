// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

func TestAgentApprovalAccessWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    bool
		refused bool
	}{
		{"approver without admin", true, false}, {"not in role", false, false},
		{"no write", false, false}, {"no roles", false, false}, {"disabled", false, false},
		{"agent", false, true}, {"scoped", false, true}, {"no policy", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.Identity{}, &iam.Membership{}, &iam.MfaFactor{}, &iam.AgentPolicy{}, &iam.AgentPolicyVersion{})
			rdbm := &rdb.RdbManager{Database: db}
			store := iam.NewStore(rdbm)
			mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)
			mgr.SetAgentActuation(tc.name != "disabled")
			ctx := context.Background()
			authorities := []string{string(auth.AlarmWrite)}
			if tc.name == "no write" {
				authorities = []string{string(auth.AlarmRead)}
			}
			role := iam.Role{Scope: iam.ScopeTenant, Token: "approvers", Authorities: authorities}
			require.NoError(t, store.CreateRole(ctx, &role))
			id := iam.Identity{Email: "approver@example.invalid", Enabled: true, PasswordHash: "x", Memberships: []iam.Membership{{TenantId: "acme", Enabled: true, TenantRoles: []iam.Role{role}}}}
			require.NoError(t, store.CreateIdentity(ctx, &id))
			require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Create(&iam.MfaFactor{IdentityID: id.ID, Kind: iam.MfaKindTotp, State: iam.MfaStateActive}).Error)
			roles := `["approvers"]`
			if tc.name == "not in role" {
				roles = `["other"]`
			}
			if tc.name == "no roles" {
				roles = `[]`
			}
			if tc.name != "no policy" {
				require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Create(&iam.AgentPolicyVersion{TenantId: "acme", Version: 1, Document: `{"actuation":"enabled","approvers":{"roles":` + roles + `}}`}).Error)
				require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Create(&iam.AgentPolicy{TenantId: "acme", PublishedVersion: 1}).Error)
			}
			c := &auth.Claims{Tenant: "acme", Username: id.Email, TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.AlarmWrite)}}
			if tc.name == "agent" {
				c.ClientId = "agent"
			}
			if tc.name == "scoped" {
				c.ScopeAnchors = []string{"customer:one"}
			}
			ctx = context.WithValue(core.WithTenant(auth.WithClaims(ctx, c), c.Tenant), ContextIdentityKey, mgr)
			schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(ctx, `{ agentApprovalAccess { instanceEnabled canApprove } }`, "", nil)
			if tc.refused {
				require.NotEmpty(t, res.Errors)
				require.Equal(t, "FORBIDDEN", res.Errors[0].Extensions["code"])
				return
			}
			require.Empty(t, res.Errors)
			var got map[string]any
			require.NoError(t, json.Unmarshal(res.Data, &got))
			require.Equal(t, map[string]any{"agentApprovalAccess": map[string]any{"instanceEnabled": tc.name != "disabled", "canApprove": tc.want}}, got)
		})
	}
}
