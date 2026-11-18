// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

func TestAgentApprovalAccess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    bool
		refused bool
	}{
		{"approver without admin", true, false}, {"not in role", false, false},
		{"no write", false, false}, {"no roles", false, false}, {"disabled", false, false},
		{"agent", false, true}, {"scoped", false, true}, {"no policy", false, false},
		{"no second factor", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newExecEnv(t)
			ctx := context.Background()
			c := personClaims("boss@example.invalid", string(auth.AlarmWrite))
			switch tc.name {
			case "not in role":
				x.publish(t, `{"actuation":"enabled","approvers":{"roles":["other"]}}`)
			case "no write":
				x.role.Authorities = []string{string(auth.AlarmRead)}
				require.NoError(t, x.m.db.Database.Save(x.role).Error)
			case "no roles":
				x.publish(t, `{"actuation":"enabled"}`)
			case "disabled":
				x.m.SetAgentActuation(false)
			case "agent":
				c.ClientId = "agent"
			case "scoped":
				c.ScopeAnchors = []string{"customer:one"}
			case "no policy":
				c.Tenant = "no-policy"
			case "no second factor":
				require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Where("1 = 1").Delete(&iam.MfaFactor{}).Error)
			}
			got, err := x.m.AgentApprovalAccess(ctx, c)
			if tc.refused {
				require.ErrorIs(t, err, auth.ErrForbidden)
				require.Equal(t, ApprovalAccess{}, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, ApprovalAccess{InstanceEnabled: tc.name != "disabled", CanApprove: tc.want}, got)
		})
	}
}

func TestAgentApprovalAccessReturnsStoreError(t *testing.T) {
	x := newExecEnv(t)
	require.NoError(t, x.m.db.Database.Migrator().DropTable(&iam.AgentPolicy{}))
	_, err := x.m.AgentApprovalAccess(context.Background(), personClaims("boss@example.invalid"))
	require.Error(t, err)
}
