// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func tenantActuationService(e *mintTestEnv, wired bool) *admin.Service {
	s := admin.NewService(e.store, 0, 0, nil)
	s.SetAgentActuation(true)
	if wired {
		s.SetTenantActuation(e.m)
	}
	return s
}

// Disabling commits a newer generation and publishes precisely that generation to the area fence.
func TestDisablingATenantPublishesAKill(t *testing.T) {
	e := newExecEnv(t)
	ctx := context.Background()
	head, err := e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	before := head.ActuationGeneration
	p := &killStub{}
	e.m.SetKillPublisher(p)
	s := tenantActuationService(e.mintTestEnv, true)
	_, err = s.SetTenantEnabled(ctx, agentTenant, false)
	require.NoError(t, err)
	head, err = e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	require.Greater(t, head.ActuationGeneration, before)
	require.Len(t, p.events, 1)
	require.Equal(t, agentTenant, p.events[0].Tenant)
	require.Equal(t, actiongate.KillCauseOperatorSuspended, p.events[0].Cause)
	require.Equal(t, head.ActuationGeneration, p.events[0].ActuationGeneration)
}

// Re-enabling sends no kill and advances actual execution decisions beyond the killed generation.
func TestReenablingATenantPublishesNothingAndReopensTheFence(t *testing.T) {
	e := newExecEnv(t)
	ctx := context.Background()
	p := &killStub{}
	e.m.SetKillPublisher(p)
	s := tenantActuationService(e.mintTestEnv, true)
	_, err := s.SetTenantEnabled(ctx, agentTenant, false)
	require.NoError(t, err)
	require.Len(t, p.events, 1)
	killed := p.events[0].ActuationGeneration
	_, err = s.SetTenantEnabled(ctx, agentTenant, true)
	require.NoError(t, err)
	require.Len(t, p.events, 1)
	head, err := e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	require.Greater(t, head.ActuationGeneration, killed)
	d := e.decide(t, e.request("boss@example.invalid"))
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
	require.Equal(t, head.ActuationGeneration, d.ActuationGeneration)
	require.Equal(t, actiongate.FenceReenable, actiongate.CheckFence(actiongate.FenceState{
		Present: true, Enabled: false, KilledAtGeneration: killed,
	}, d.ActuationGeneration))
	// An unchanged re-enable must bump too: this repairs an old pod's re-enable
	// during a rolling upgrade, which did not advance beyond the killed generation.
	before := head.ActuationGeneration
	_, err = s.SetTenantEnabled(ctx, agentTenant, true)
	require.NoError(t, err)
	head, err = e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	require.Greater(t, head.ActuationGeneration, before)
	require.Len(t, p.events, 1)
}

func TestDisableAndReenableInterleavingReopensTheFence(t *testing.T) {
	e := newExecEnv(t)
	ctx := context.Background()
	tenant, err := e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	p := &killStub{}
	e.m.SetKillPublisher(p)
	gDisable, err := e.store.SetTenantEnabledAndBumpGeneration(ctx, tenant, false)
	require.NoError(t, err)
	gEnable, err := e.store.SetTenantEnabledAndBumpGeneration(ctx, tenant, true)
	require.NoError(t, err)
	// Delay the disable's publish until after re-enable. With a separate bump,
	// the late kill would carry the newest generation and close the fence for
	// an enabled tenant.
	require.NoError(t, e.m.TenantDisabled(ctx, agentTenant, gDisable))
	require.Len(t, p.events, 1)
	require.Equal(t, gDisable, p.events[0].ActuationGeneration)
	require.Greater(t, gEnable, gDisable)
	tenant, err = e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.True(t, tenant.Enabled)
	require.Equal(t, actiongate.FenceReenable, actiongate.CheckFence(actiongate.FenceState{
		Present: true, Enabled: false, KilledAtGeneration: gDisable,
	}, gEnable))
}

func TestTenantWriteAndBumpAreOneTransaction(t *testing.T) {
	e := newExecEnv(t)
	ctx := context.Background()
	tenant, err := e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.True(t, tenant.Enabled)
	// Ensure the head already exists, so failure happens on its save AFTER the
	// tenant write, rather than on initial head creation before the tenant write.
	head, err := e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	require.NotNil(t, head)
	before := head.ActuationGeneration
	db := e.m.db.Database
	armed := false
	failure := errors.New("head save failed")
	failHeadSave := func(tx *gorm.DB) {
		if armed && tx.Statement.Table == (iam.AgentPolicy{}).TableName() {
			tx.AddError(failure)
		}
	}
	const callback = "test:fail_tenant_generation_save"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, failHeadSave))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callback)) })
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, failHeadSave))
	t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove(callback)) })
	armed = true
	_, err = e.store.SetTenantEnabledAndBumpGeneration(ctx, tenant, false)
	armed = false
	require.ErrorIs(t, err, failure)
	tenant, err = e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.True(t, tenant.Enabled, "the tenant write must roll back when the head save fails")
	head, err = e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	require.Equal(t, before, head.ActuationGeneration)
}

func newTenantPurgeActuationEnv(t *testing.T) (*mintTestEnv, *admin.Service, *killStub) {
	t.Helper()
	e := newMintTestEnv(t)
	require.NoError(t, e.m.db.Database.AutoMigrate(&iam.TenantPurge{}))
	e.seedTenant(t, agentTenant)
	p := &killStub{}
	e.m.SetKillPublisher(p)
	return e, tenantActuationService(e, true), p
}

// Purge publishes the current generation, including zero, without creating or bumping a head.
func TestBeginningAPurgePublishesAKill(t *testing.T) {
	for _, withHead := range []bool{false, true} {
		t.Run(map[bool]string{false: "no head", true: "existing head"}[withHead], func(t *testing.T) {
			e, s, p := newTenantPurgeActuationEnv(t)
			ctx := context.Background()
			generation := 0
			if withHead {
				head, err := e.store.SetAgentPolicySuspended(ctx, agentTenant, false, "test")
				require.NoError(t, err)
				generation = head.ActuationGeneration
			}
			began, err := s.DeleteTenant(ctx, agentTenant)
			require.NoError(t, err)
			require.True(t, began)
			require.Len(t, p.events, 1)
			require.Equal(t, agentTenant, p.events[0].Tenant)
			require.Equal(t, actiongate.KillCauseOperatorSuspended, p.events[0].Cause)
			require.Equal(t, generation, p.events[0].ActuationGeneration)
			head, err := e.store.AgentPolicyHead(ctx, agentTenant)
			require.NoError(t, err)
			if withHead {
				require.NotNil(t, head)
				require.Equal(t, generation, head.ActuationGeneration)
			} else {
				require.Nil(t, head, "purge must not create policy rows")
			}
		})
	}
}

// Purging retries resend a failed kill while preserving false; missing tenants never publish.
func TestPurgeRetryRepublishesButMissingTenantDoesNot(t *testing.T) {
	e, s, p := newTenantPurgeActuationEnv(t)
	ctx := context.Background()
	p.err = errors.New("broker unavailable")
	began, err := s.DeleteTenant(ctx, agentTenant)
	var notification *KillNotificationError
	require.ErrorAs(t, err, &notification)
	require.False(t, began)
	row, err := e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.Equal(t, iam.PurgePurging, row.PurgeState)
	require.Len(t, p.events, 1)
	p.err = nil
	began, err = s.DeleteTenant(ctx, agentTenant)
	require.NoError(t, err)
	require.False(t, began)
	require.Len(t, p.events, 2)
	began, err = s.DeleteTenant(ctx, "missing")
	require.NoError(t, err)
	require.False(t, began)
	require.Len(t, p.events, 2)
}

// Notification failure surfaces after the disabling write; repeating an unchanged value retries it.
func TestKillPublishFailureIsReturned(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, agentTenant)
	ctx := context.Background()
	p := &killStub{err: errors.New("broker unavailable")}
	e.m.SetKillPublisher(p)
	s := tenantActuationService(e, true)
	_, err := s.SetTenantEnabled(ctx, agentTenant, false)
	var notification *KillNotificationError
	require.ErrorAs(t, err, &notification)
	require.ErrorIs(t, err, p.err)
	row, err := e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.False(t, row.Enabled)
	require.Len(t, p.events, 1)
	p.err = nil
	_, err = s.SetTenantEnabled(ctx, agentTenant, false)
	require.NoError(t, err)
	require.Len(t, p.events, 2)
	require.Greater(t, p.events[1].ActuationGeneration, p.events[0].ActuationGeneration)
}

// Missing wiring fails loudly only when the instance permits agent actions.
func TestUnwiredNotifierFailsLoudlyWhenActuationIsOn(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, agentTenant)
	s := tenantActuationService(e, false)
	_, err := s.SetTenantEnabled(context.Background(), agentTenant, false)
	require.EqualError(t, err, "tenant actuation notifier not wired")
	tenant, err := e.store.TenantByToken(context.Background(), agentTenant)
	require.NoError(t, err)
	require.True(t, tenant.Enabled, "an unwired service must not write the row")
	s.SetAgentActuation(false)
	_, err = s.SetTenantEnabled(context.Background(), agentTenant, false)
	require.NoError(t, err)
	head, err := e.store.AgentPolicyHead(context.Background(), agentTenant)
	require.NoError(t, err)
	require.NotNil(t, head)
	require.Positive(t, head.ActuationGeneration, "unwired actuation-off writes must still bump")
}

// Purge also refuses missing notifier wiring, BEFORE it begins: an unwired service changes nothing,
// and once wired the same request begins the purge and publishes the kill.
func TestUnwiredPurgeNotifierFailsLoudlyWhenActuationIsOn(t *testing.T) {
	e, _, p := newTenantPurgeActuationEnv(t)
	s := tenantActuationService(e, false)
	ctx := context.Background()
	began, err := s.DeleteTenant(ctx, agentTenant)
	require.False(t, began)
	require.EqualError(t, err, "tenant actuation notifier not wired")
	tenant, err := e.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.False(t, tenant.PurgeState.Deleted(), "an unwired service must not begin the purge")
	s.SetTenantActuation(e.m)
	began, err = s.DeleteTenant(ctx, agentTenant)
	require.NoError(t, err)
	require.True(t, began)
	require.Len(t, p.events, 1)
}
