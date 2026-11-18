// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestActuationDetailTenantLiveness(t *testing.T) {
	for _, state := range []string{"live", "disabled", "purging", "missing"} {
		t.Run(state, func(t *testing.T) {
			x := newExecEnv(t)
			ctx := context.Background()
			claims := tenantProposalClaims()
			x.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"]},"expiry":{"default":"30m"},"tools":{"alarms":{"ratePerHour":5}}}`)
			d, err := x.m.AgentActuationDecision(ctx, claims, agentpolicy.Request{Kind: agentpolicy.KindAlarmClear, TargetCount: 1})
			require.NoError(t, err)
			require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
			want := ActuationDetail{Expiry: 30 * time.Minute, ActionsPerHour: 5, ClientAttested: false}
			detail, err := x.m.AgentActuationDetail(ctx, claims, agentpolicy.KindAlarmClear, d.PolicyVersion)
			require.NoError(t, err)
			require.Equal(t, want, detail)

			switch state {
			case "disabled":
				disableExecutionTenant(t, x)
			case "purging":
				purgeExecutionTenant(t, x)
			case "missing":
				require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Unscoped().Where("token = ?", agentTenant).Delete(&iam.Tenant{}).Error)
			}
			assertDecisionTransaction(t, x.m.db.Database, func() {
				detail, err = x.m.AgentActuationDetail(ctx, claims, agentpolicy.KindAlarmClear, d.PolicyVersion)
			})
			if state == "live" {
				require.NoError(t, err)
				require.Equal(t, want, detail)
			} else {
				require.EqualError(t, err, errActRefused)
				require.Equal(t, ActuationDetail{}, detail)
			}
		})
	}
}

func TestActuationDetailTenantStoreErrorIsAnError(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	claims := tenantProposalClaims()
	detail, err := x.m.AgentActuationDetail(ctx, claims, agentpolicy.KindAlarmClear, 1)
	require.NoError(t, err)
	require.Equal(t, ActuationDetail{Expiry: agentpolicy.DefaultExpiry}, detail)

	fault := errors.New("tenant read failed")
	armed := false
	const name = "test:actuation-detail-tenant-read-error"
	require.NoError(t, x.m.db.Database.Callback().Query().After("gorm:query").Register(name, func(db *gorm.DB) {
		if armed && db.Statement.Table == (iam.Tenant{}).TableName() {
			db.AddError(fault)
		}
	}))
	t.Cleanup(func() {
		armed = false
		require.NoError(t, x.m.db.Database.Callback().Query().Remove(name))
	})
	armed = true
	detail, err = x.m.AgentActuationDetail(ctx, claims, agentpolicy.KindAlarmClear, 1)
	require.ErrorIs(t, err, fault)
	require.Equal(t, ActuationDetail{}, detail)
}
