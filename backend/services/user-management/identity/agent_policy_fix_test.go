// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/http"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// serviceAccounts.agentFlagRequiredFor is applied at EVERY mint, against the roles as they are now:
// an ordinary account whose role later gained a listed authority stops receiving tokens.
func TestAgentFlagRequirementEnforcedAtMint(t *testing.T) {
	e := newCCEnv(t)
	ctx := context.Background()
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, "control: the account mints")

	_, err := e.store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return `{"serviceAccounts":{"agentFlagRequiredFor":["command:write"]}}`, iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, "an account not holding the listed authority still mints")

	// The role is edited after the fact to grant command:write.
	sys := e.m.db.Database.WithContext(core.WithSystemContext(ctx))
	var role iam.Role
	require.NoError(t, sys.Where("token = ?", "ci-role").First(&role).Error)
	role.Authorities = []string{"device:read", "command:write"}
	require.NoError(t, sys.Save(&role).Error)

	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_grant", decode(t, rec).Error)
}

// A scoped member, a disabled identity and a disabled membership are not eligible approvers: a
// two-person verdict with only such people available is a refusal.
func TestIneligibleApproversAreNotCounted(t *testing.T) {
	e := newAgentEnv(t, string(auth.CommandWrite))
	ctx := context.Background()
	e.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"]},
		"tools":{"commands":{"commands":["wipe"]}}}`)
	role := &iam.Role{Scope: iam.ScopeTenant, Token: "approvers", Authorities: []string{string(auth.CommandWrite)}}
	require.NoError(t, e.store.CreateRole(ctx, role))
	add := func(email string, idEnabled, memEnabled bool, scope []string) {
		require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{Email: email, Enabled: true, PasswordHash: "x",
			Memberships: []iam.Membership{{TenantId: agentTenant, Enabled: memEnabled, Scope: scope, TenantRoles: []iam.Role{*role}}}}))
		if !idEnabled {
			require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.Identity{}).
				Where("email = ?", email).Update("enabled", false).Error)
		}
	}
	add("ok@example.invalid", true, true, nil)
	add("scoped@example.invalid", true, true, []string{"customer:c1"})
	add("gone@example.invalid", false, true, nil)
	add("off@example.invalid", true, false, nil)

	agent := &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, TokenType: auth.TokenTypeAccess}
	req := agentpolicy.Request{Kind: agentpolicy.KindCommandSend, Impact: agentpolicy.ImpactDestructive, Command: "wipe", TargetCount: 1}
	d, err := e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict, "only one eligible approver: two-person must refuse")
	require.Contains(t, d.Reasons, agentpolicy.ReasonTooFewApprovers)

	add("second@example.invalid", true, true, nil)
	d, err = e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "control: two eligible approvers")
	require.Equal(t, 2, d.RequiredApprovals)
}

// Each suspension and restoration is its own journal row naming the operator.
func TestSuspensionTransitionsAreJournaled(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	_, err := e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, true, "op@example.invalid")
	require.NoError(t, err)
	_, err = e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, false, "op2@example.invalid")
	require.NoError(t, err)
	var evs []rdb.AuditEvent
	require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).
		Where("operation IN ?", []string{rdb.AuditOpAgentActuationSuspended, rdb.AuditOpAgentActuationRestored}).
		Order("id").Find(&evs).Error)
	require.Len(t, evs, 2)
	require.Equal(t, rdb.AuditOpAgentActuationSuspended, evs[0].Operation)
	require.Equal(t, "op@example.invalid", evs[0].Actor)
	require.Equal(t, rdb.AuditOpAgentActuationRestored, evs[1].Operation)
	require.Equal(t, "op2@example.invalid", evs[1].Actor)
}
