// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type killStub struct {
	events []actiongate.KillEvent
	err    error
}

func (p *killStub) PublishKill(_ context.Context, e actiongate.KillEvent) error {
	p.events = append(p.events, e)
	return p.err
}
func TestKillNotifications(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, openPolicyJS)
	ctx := context.Background()
	now := time.Now()
	claims := adminClaims(&now, auth.AmrPasswordOTP())
	p := &killStub{}
	e.m.SetKillPublisher(p)
	view, err := e.m.SetTenantAgentActuation(ctx, claims, false)
	require.NoError(t, err)
	require.Len(t, p.events, 1)
	require.Equal(t, view.Head.PublishedVersion, p.events[0].PolicyVersion)
	require.Equal(t, actiongate.KillCauseTenantDisabled, p.events[0].Cause)
	require.Equal(t, agentTenant, p.events[0].Tenant)
	require.NoError(t, p.events[0].Validate())
	id, err := hex.DecodeString(p.events[0].EventID)
	require.NoError(t, err)
	require.Len(t, id, 16)
	_, err = e.m.SetTenantAgentActuation(ctx, claims, false)
	require.NoError(t, err)
	require.Len(t, p.events, 2)
	require.NotEqual(t, p.events[0].EventID, p.events[1].EventID)
	require.Equal(t, p.events[0].PolicyVersion, p.events[1].PolicyVersion)
	_, err = e.m.SetTenantAgentActuation(ctx, claims, true)
	require.NoError(t, err)
	require.Len(t, p.events, 2)
	head, err := e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, true, "operator")
	require.NoError(t, err)
	require.Len(t, p.events, 3)
	require.Equal(t, actiongate.KillCauseOperatorSuspended, p.events[2].Cause)
	require.Equal(t, head.PublishedVersion, p.events[2].PolicyVersion)
	_, err = e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, false, "operator")
	require.NoError(t, err)
	require.Len(t, p.events, 3)
	for _, suspend := range []bool{false, true} {
		t.Run(map[bool]string{false: "disable", true: "suspend"}[suspend], func(t *testing.T) {
			p.err = errors.New("broker down")
			for i := 0; i < 2; i++ {
				n := len(p.events)
				if suspend {
					_, err = e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, true, "operator")
				} else {
					_, err = e.m.SetTenantAgentActuation(ctx, claims, false)
				}
				var notify *KillNotificationError
				require.ErrorAs(t, err, &notify)
				require.ErrorIs(t, err, p.err)
				require.Contains(t, err.Error(), "kill is in force")
				require.Contains(t, err.Error(), "repeat the action")
				require.Len(t, p.events, n+1)
				stored, err := e.m.AgentPolicy(ctx, agentTenant)
				require.NoError(t, err)
				if suspend {
					require.True(t, stored.Head.OperatorSuspended)
				} else {
					require.True(t, stored.Head.TenantActuationOff)
				}
			}
			p.err = nil
		})
	}
	e.m.SetKillPublisher(nil)
	_, err = e.m.SetTenantAgentActuation(ctx, claims, false)
	require.NoError(t, err)
	_, err = e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, true, "operator")
	require.NoError(t, err)
}
