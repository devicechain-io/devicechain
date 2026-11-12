// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

type pausedKillDecider struct {
	fakeDecider
	decision chan struct{}
	resume   chan struct{}
	calls    int
}

func (d *pausedKillDecider) Execution(context.Context, actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	d.calls++
	if d.calls == 2 {
		close(d.decision)
		<-d.resume
	}
	return d.exec, nil
}

func TestAgentFenceKillDuringCommitPointWaitsForTheExecution(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	now := time.Now()
	api.KillFeed = NewKillFeed(0, func() time.Time { return now })
	api.KillFeed.Beat(now)
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{TokenReference: rdb.TokenReference{Token: "kill-target"},
		OriginatorType: string(entity.TypeDevice), OriginatorId: 1, AlarmKey: "kill-target", MetricKey: "m",
		State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: now}).Error)
	d := &pausedKillDecider{fakeDecider: fakeDecider{act: approveOne(), exec: execApprove(1, "boss")}, decision: make(chan struct{}), resume: make(chan struct{})}
	api.AgentDecider = d
	proposer := rigAgent
	proposer.Tenant, _ = core.TenantFromContext(ctx)
	out, err := api.ProposeAlarmAction(ctx, proposer, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"kill-target"}, RequestId: "kill"})
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		_, err := api.ApproveAgentProposal(ctx, approver("boss", now), out.Proposal.Token, out.Digest)
		result <- err
	}()
	<-d.decision
	// A kill here either precedes execution's share lock (refused) or waits for its
	// commit. A kill committed in user-management before the commit-point decision
	// is caught by the decision itself (TestKillCommittedInUmBeforeEventConsumedRefuses).
	kill := killFor(proposer.Tenant, d.exec.ActuationGeneration)
	killed := make(chan error, 1)
	go func() { killed <- api.ApplyKillEvent(ctx, kill) }()
	// The fence share lock now precedes the decision, so the concurrent kill must wait.
	select {
	case err := <-killed:
		close(d.resume)
		t.Fatalf("kill returned before execution resumed: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(d.resume)
	require.NoError(t, <-result)
	row, err := api.AgentProposalByToken(ctx, out.Proposal.Token, nil)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), row.State)
	alarms, err := api.AlarmsByToken(ctx, []string{"kill-target"})
	require.NoError(t, err)
	require.True(t, alarms[0].Acknowledged)
	require.Len(t, effectRows(t, api, ctx), 1)
	require.NoError(t, <-killed)
	var fence AgentActuationFence
	require.NoError(t, api.RDB.DB(ctx).First(&fence).Error)
	require.False(t, fence.Enabled)
	require.Equal(t, kill.ActuationGeneration, fence.KilledAtGeneration)
}

func TestAgentEffectsPostgresSkipLockedAndPurgeClassification(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	for _, token := range []string{"held", "later"} {
		require.NoError(t, queueAgentEffect(api.RDB.DB(ctx), "proposal", &AlarmStateChangeEvent{AlarmToken: token, EventType: AlarmEventAcknowledged}))
	}
	rows := effectRows(t, api, ctx)
	require.Len(t, rows, 2)
	held := api.RDB.DB(ctx).Begin()
	require.NoError(t, held.Error)
	defer held.Rollback()
	var row AgentEffectOutbox
	require.NoError(t, held.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", rows[0].Id).First(&row).Error)
	pub := &dedupEffectPublisher{}
	api.AlarmPublisher = pub
	_, err := api.DrainAgentEffects(ctx, 1)
	require.NoError(t, err)
	_, publishedHeld := pub.seen[rows[0].Id]
	require.False(t, publishedHeld)
	_, publishedLater := pub.seen[rows[1].Id]
	require.True(t, publishedLater)
	require.NoError(t, held.Rollback().Error)
	_, err = api.DrainAgentEffects(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, effectRows(t, api, ctx))
	plan, err := tenantpurge.Classify(core.WithSystemContext(ctx), api.RDB.Database)
	require.NoError(t, err)
	found := map[string]bool{}
	for _, entry := range plan.Entries {
		if entry.Table.Schema == "device-management" && (entry.Table.Name == "agent_effect_outboxes" || entry.Table.Name == "agent_actuation_fences") {
			require.Equal(t, tenantpurge.ClassDirect, entry.Class)
			require.Equal(t, "tenant_id", entry.Column)
			found[entry.Table.Name] = true
		}
	}
	require.Len(t, found, 2)
}
