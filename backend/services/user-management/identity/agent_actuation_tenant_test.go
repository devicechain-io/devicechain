// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func tenantProposalClaims() *auth.Claims {
	return &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient,
		Scope: auth.ScopeActAlarms, Authorities: []string{string(auth.AlarmPropose)}, TokenType: auth.TokenTypeAccess}
}

// Disabling the tenant refuses proposals from an agent token that previously received approval.
func TestProposalDecisionRefusesDisabledTenant(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	claims := tenantProposalClaims()
	req := agentpolicy.Request{Kind: agentpolicy.KindAlarmClear, TargetCount: 1}
	d, err := x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)

	disableExecutionTenant(t, x)
	d, err = x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	require.Equal(t, []string{agentpolicy.ReasonOperatorSuspended}, d.Reasons)
}

// Purging the tenant refuses proposals even while the tenant remains enabled.
func TestProposalDecisionRefusesPurgingTenant(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	claims := tenantProposalClaims()
	req := agentpolicy.Request{Kind: agentpolicy.KindAlarmClear, TargetCount: 1}
	d, err := x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)

	purgeExecutionTenant(t, x)
	d, err = x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	require.Equal(t, []string{agentpolicy.ReasonOperatorSuspended}, d.Reasons)
}

// Removing the tenant row refuses proposals from an otherwise admitted agent token.
func TestProposalDecisionRefusesMissingTenantRow(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	claims := tenantProposalClaims()
	req := agentpolicy.Request{Kind: agentpolicy.KindAlarmClear, TargetCount: 1}
	d, err := x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)

	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Unscoped().Where("token = ?", agentTenant).Delete(&iam.Tenant{}).Error)
	d, err = x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	require.Equal(t, []string{agentpolicy.ReasonOperatorSuspended}, d.Reasons)
}

// A tenant read failure returns an error instead of a proposal verdict.
func TestProposalDecisionTenantStoreErrorIsAnError(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	claims := tenantProposalClaims()
	req := agentpolicy.Request{Kind: agentpolicy.KindAlarmClear, TargetCount: 1}
	d, err := x.m.AgentActuationDecision(ctx, claims, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)

	armed := false
	const name = "test:proposal-tenant-read-error"
	require.NoError(t, x.m.db.Database.Callback().Query().After("gorm:query").Register(name, func(db *gorm.DB) {
		if armed && db.Statement.Table == (iam.Tenant{}).TableName() {
			db.AddError(errors.New("boom"))
		}
	}))
	t.Cleanup(func() {
		armed = false
		require.NoError(t, x.m.db.Database.Callback().Query().Remove(name))
	})
	armed = true
	d, err = x.m.AgentActuationDecision(ctx, claims, req)
	require.ErrorContains(t, err, "boom")
	require.Empty(t, d.Verdict)
}
