// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// tenantServiceAccountRequest derives alarm:propose from an unscoped agent account's alarm
// write role through act:alarms, admitted by a policy listing the account's client id.
func tenantServiceAccountRequest(t *testing.T, x *execEnv) ExecutionRequest {
	t.Helper()
	ctx := context.Background()
	role := &iam.Role{Scope: iam.ScopeTenant, Token: "agent-alarm-writer", Authorities: []string{string(auth.AlarmWrite)}}
	require.NoError(t, x.store.CreateRole(ctx, role))
	acct := &iam.ServiceAccount{
		TenantId: agentTenant, Token: "alarm-agent", ClientId: "dcsa_alarm_agent",
		Enabled: true, Agent: true, Roles: []iam.Role{*role},
	}
	require.NoError(t, x.store.CreateServiceAccount(ctx, acct))
	x.publish(t, `{"actuation":"enabled","clients":["agent-desktop","dcsa_alarm_agent"],"approvers":{"roles":["approvers"]}}`)
	req := ExecutionRequest{
		Kind: agentpolicy.KindAlarmClear, TargetCount: 1,
		ProposerPrincipal: auth.ServiceAccountSubject(acct.Token), ProposerClient: acct.ClientId,
		Approvers:        []string{"boss@example.invalid"},
		ApproverSessions: []ApproverSession{{Principal: "boss@example.invalid", Sid: x.approverSids["boss@example.invalid"]}},
	}
	reason, err := x.m.serviceAccountProposerStanding(ctx, agentTenant, req, auth.ScopeActAlarms, string(auth.AlarmPropose))
	require.NoError(t, err)
	require.Equal(t, "", reason)
	return req
}

func disableExecutionTenant(t *testing.T, x *execEnv) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.Tenant{}).Where("token = ?", agentTenant).Update("enabled", false).Error)
	tenant, err := x.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.Equal(t, false, tenant.Enabled)
	require.Equal(t, iam.PurgeActive, tenant.PurgeState)
}

func purgeExecutionTenant(t *testing.T, x *execEnv) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.Tenant{}).Where("token = ?", agentTenant).Update("purge_state", iam.PurgePurging).Error)
	tenant, err := x.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.Equal(t, true, tenant.Enabled, "purge must be checked independently of enabled")
	require.Equal(t, iam.PurgePurging, tenant.PurgeState)
}

func requireLiveTenantApproval(t *testing.T, x *execEnv, req ExecutionRequest) {
	t.Helper()
	d := x.decide(t, req)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
	require.Equal(t, []string(nil), d.Reasons)
	require.Equal(t, req.Approvers, d.ValidApprovers)
}

func requireTenantSuspension(t *testing.T, x *execEnv, req ExecutionRequest) {
	t.Helper()
	d := x.decide(t, req)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	require.Equal(t, []string{agentpolicy.ReasonOperatorSuspended}, d.Reasons)
	require.Equal(t, []string{}, d.ValidApprovers)
}

func TestEnabledTenantStillExecutes(t *testing.T) {
	x := newExecEnv(t)
	requireLiveTenantApproval(t, x, tenantServiceAccountRequest(t, x))
	x.addApprover(t, "second@example.invalid")
	requireLiveTenantApproval(t, x, x.request("boss@example.invalid", "second@example.invalid"))
}

func TestDisabledTenantRefusesExecutionForServiceAccountProposer(t *testing.T) {
	x := newExecEnv(t)
	req := tenantServiceAccountRequest(t, x)
	requireLiveTenantApproval(t, x, req)
	disableExecutionTenant(t, x)
	requireTenantSuspension(t, x, req)
}

func TestDisabledTenantApproverDoesNotCount(t *testing.T) {
	x := newExecEnv(t)
	x.addApprover(t, "second@example.invalid")
	req := x.request("boss@example.invalid", "second@example.invalid")
	requireLiveTenantApproval(t, x, req)
	disableExecutionTenant(t, x)
	requireTenantSuspension(t, x, req)
}

func TestPurgingTenantRefusesExecution(t *testing.T) {
	t.Run("service account proposer", func(t *testing.T) {
		x := newExecEnv(t)
		req := tenantServiceAccountRequest(t, x)
		requireLiveTenantApproval(t, x, req)
		purgeExecutionTenant(t, x)
		requireTenantSuspension(t, x, req)
	})
	t.Run("person proposer with approvers", func(t *testing.T) {
		x := newExecEnv(t)
		x.addApprover(t, "second@example.invalid")
		req := x.request("boss@example.invalid", "second@example.invalid")
		requireLiveTenantApproval(t, x, req)
		purgeExecutionTenant(t, x)
		requireTenantSuspension(t, x, req)
	})
}

func TestMissingTenantRowRefusesExecution(t *testing.T) {
	x := newExecEnv(t)
	req := tenantServiceAccountRequest(t, x)
	requireLiveTenantApproval(t, x, req)
	ctx := context.Background()
	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Unscoped().Where("token = ?", agentTenant).Delete(&iam.Tenant{}).Error)
	requireTenantSuspension(t, x, req)
}
