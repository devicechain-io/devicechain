// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/http"
	"testing"
	"time"

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

// A scoped member, a disabled identity, a disabled membership and a person with no second factor
// are not eligible approvers: a
// two-person verdict with only such people available is a refusal.
func TestIneligibleApproversAreNotCounted(t *testing.T) {
	e := newAgentEnv(t, string(auth.CommandWrite))
	ctx := context.Background()
	e.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"]},
		"tools":{"commands":{"commands":["wipe"]}}}`)
	role := &iam.Role{Scope: iam.ScopeTenant, Token: "approvers", Authorities: []string{string(auth.CommandWrite)}}
	require.NoError(t, e.store.CreateRole(ctx, role))
	add := func(email string, idEnabled, memEnabled bool, scope []string, factor bool) {
		require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{Email: email, Enabled: true, PasswordHash: "x",
			Memberships: []iam.Membership{{TenantId: agentTenant, Enabled: memEnabled, Scope: scope, TenantRoles: []iam.Role{*role}}}}))
		if factor {
			e.enrollFactor(t, email)
		}
		if !idEnabled {
			require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.Identity{}).
				Where("email = ?", email).Update("enabled", false).Error)
		}
	}
	add("ok@example.invalid", true, true, nil, true)
	add("scoped@example.invalid", true, true, []string{"customer:c1"}, true)
	add("gone@example.invalid", false, true, nil, true)
	add("off@example.invalid", true, false, nil, true)
	add("nofactor@example.invalid", true, true, nil, false)

	agent := &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Scope: "act:commands",
		Authorities: []string{string(auth.CommandPropose)}, TokenType: auth.TokenTypeAccess}
	req := agentpolicy.Request{Kind: agentpolicy.KindCommandSend, Impact: agentpolicy.ImpactDestructive, Command: "wipe", TargetCount: 1}
	d, err := e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict, "only one eligible approver: two-person must refuse")
	require.Contains(t, d.Reasons, agentpolicy.ReasonTooFewApprovers)

	add("second@example.invalid", true, true, nil, true)
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

// The kill switch never depends on reading the policy it stops: over a document this pod cannot
// read (written by a newer release, or damaged), turning actuation off still publishes a disabled
// policy without a second factor; turning it on over one is refused.
func TestKillSwitchWorksOverAnUnreadablePolicy(t *testing.T) {
	for name, doc := range map[string]string{
		"newer version": `{"version":"v9","actuation":"enabled","clients":["agent-desktop"],"futureKey":1,"serviceAccounts":{"agentFlagRequiredFor":["command:write","future:write"]}}`,
		"damaged":       `{"version":"v1alpha1","actuation":"enabled","typo":1,"serviceAccounts":{"agentFlagRequiredFor":["command:write"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newAgentEnv(t)
			ctx := context.Background()
			_, err := e.store.PublishAgentPolicy(ctx, agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
				return doc, iam.AgentPolicyOriginPublish, false, nil
			})
			require.NoError(t, err)
			noFactor := adminClaims(nil, auth.AmrPasswordOnly())
			view, err := e.m.SetTenantAgentActuation(ctx, noFactor, false)
			require.NoError(t, err)
			d, err := agentpolicy.Parse([]byte(view.Published.Document))
			require.NoError(t, err)
			require.False(t, d.ActuationOn())
			require.Equal(t, []string{"command:write"}, d.AgentFlagRequiredFor(),
				"the kill switch must carry the service-account rule forward, never drop it")

			_, err = e.store.PublishAgentPolicy(ctx, agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
				return doc, iam.AgentPolicyOriginPublish, false, nil
			})
			require.NoError(t, err)
			now := time.Now()
			_, err = e.m.SetTenantAgentActuation(ctx, adminClaims(&now, auth.AmrPasswordOTP()), true)
			require.Error(t, err, "enabling over an unreadable policy must be refused")
		})
	}
}

// The mint path fails closed on a damaged policy of a known version (the tenant's service-account
// rule must not lapse), and does not stop on a newer release's document mid-upgrade.
func TestMintFailsClosedOnADamagedPolicy(t *testing.T) {
	e := newCCEnv(t)
	ctx := context.Background()
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	publish := func(doc string) {
		_, err := e.store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
			return doc, iam.AgentPolicyOriginPublish, false, nil
		})
		require.NoError(t, err)
	}
	publish(`{"version":"v1alpha1","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"typo":1}`)
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	publish(`{"version":"v9","futureKey":1}`)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A newer release's rule is enforced on this pod too: the role grows command:write, and the
	// rule written in an unknown format still refuses the ordinary account's mint.
	sys := e.m.db.Database.WithContext(core.WithSystemContext(ctx))
	var role iam.Role
	require.NoError(t, sys.Where("token = ?", "ci-role").First(&role).Error)
	role.Authorities = []string{"device:read", "command:write"}
	require.NoError(t, sys.Save(&role).Error)
	publish(`{"version":"v9","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"futureKey":1}`)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_grant", decode(t, rec).Error)
}

// A document no pod can read at all (not even JSON): actuation is already off everywhere, and the
// kill switch publishes nothing rather than a policy that would lift the service-account rule.
func TestKillSwitchPublishesNothingOverAnUnparseableDocument(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	_, err := e.store.PublishAgentPolicy(ctx, agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return `{"version":"v9","serviceAccounts":{"agentFlagRequiredFor":"not-a-list"}}`, iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)
	view, err := e.m.SetTenantAgentActuation(ctx, adminClaims(nil, auth.AmrPasswordOnly()), false)
	require.NoError(t, err)
	require.Equal(t, 1, view.Published.Version, "nothing must be published")
}
