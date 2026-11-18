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

func TestActuationGenerationAdvancesOnEverySwitchCall(t *testing.T) {
	e := newExecEnv(t)
	ctx := context.Background()
	now := time.Now()
	claims := adminClaims(&now, auth.AmrPasswordOTP())
	p := &killStub{}
	e.m.SetKillPublisher(p)
	initial, err := e.m.AgentPolicy(ctx, agentTenant)
	require.NoError(t, err)
	generation := initial.Head.ActuationGeneration
	version := initial.Head.PublishedVersion
	for _, call := range []struct {
		name                          string
		suspend, enabled, sameVersion bool
	}{
		{name: "K1 enable already enabled", enabled: true, sameVersion: true},
		{name: "K1 disable"},
		{name: "K1 disable again", sameVersion: true},
		{name: "K1 enable", enabled: true},
		{name: "K1 enable again", enabled: true, sameVersion: true},
		{name: "K3 suspend", suspend: true, enabled: true, sameVersion: true},
		{name: "K3 suspend again", suspend: true, enabled: true, sameVersion: true},
		{name: "K3 unsuspend", suspend: true, sameVersion: true},
		{name: "K3 unsuspend again", suspend: true, sameVersion: true},
	} {
		t.Run(call.name, func(t *testing.T) {
			events := len(p.events)
			if call.suspend {
				_, err = e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, call.enabled, "operator")
			} else {
				_, err = e.m.SetTenantAgentActuation(ctx, claims, call.enabled)
			}
			require.NoError(t, err)
			generation++
			view, err := e.m.AgentPolicy(ctx, agentTenant)
			require.NoError(t, err)
			require.Equal(t, generation, view.Head.ActuationGeneration)
			if call.sameVersion {
				require.Equal(t, version, view.Head.PublishedVersion)
			}
			version = view.Head.PublishedVersion
			kills := (!call.suspend && !call.enabled) || (call.suspend && call.enabled)
			if kills {
				require.Len(t, p.events, events+1)
				require.Equal(t, generation, p.events[events].ActuationGeneration)
			} else {
				require.Len(t, p.events, events)
			}
			decision := e.decide(t, e.request("boss@example.invalid"))
			require.Equal(t, generation, decision.ActuationGeneration)
		})
	}
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
	require.Equal(t, view.Head.ActuationGeneration, p.events[0].ActuationGeneration)
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
	require.Equal(t, p.events[0].ActuationGeneration+1, p.events[1].ActuationGeneration)
	_, err = e.m.SetTenantAgentActuation(ctx, claims, true)
	require.NoError(t, err)
	require.Len(t, p.events, 2)
	head, err := e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, true, "operator")
	require.NoError(t, err)
	require.Len(t, p.events, 3)
	require.Equal(t, actiongate.KillCauseOperatorSuspended, p.events[2].Cause)
	require.Equal(t, head.ActuationGeneration, p.events[2].ActuationGeneration)
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
