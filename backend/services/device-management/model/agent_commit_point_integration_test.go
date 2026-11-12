// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func commitPointSetup(t *testing.T) (api *Api, ctx context.Context, proposal *AgentProposal, digest string, person actiongate.Approver) {
	t.Helper()
	api, ctx = newPostgresIntegrationApi(t)
	api.KillFeed = NewKillFeed(0, nil)
	api.KillFeed.Beat(api.KillFeed.Now())
	now := time.Now()
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{
		TokenReference: rdb.TokenReference{Token: "a1"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: "commit-point", MetricKey: "m", State: string(AlarmStateActive),
		Severity: string(AlarmSeverityMajor), RaisedTime: now,
	}).Error)
	api.AgentDecider = &fixedExecutionDecider{fakeDecider: fakeDecider{act: approveOne(), exec: execApprove(1, "boss")}}
	proposer := rigAgent
	var ok bool
	proposer.Tenant, ok = core.TenantFromContext(ctx)
	require.True(t, ok)
	out, err := api.ProposeAlarmAction(ctx, proposer, AlarmProposalRequest{
		Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: "commit-point",
	})
	require.NoError(t, err)
	require.NotNil(t, out.Proposal)
	return api, ctx, out.Proposal, out.Digest, approver("boss", time.Now())
}

func requirePGUnexecuted(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	p, err := api.AgentProposalByToken(ctx, token, nil)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateProposed), p.State)
	alarms, err := api.AlarmsByToken(ctx, []string{"a1"})
	require.NoError(t, err)
	require.Len(t, alarms, 1)
	require.False(t, alarms[0].Acknowledged)
	require.Empty(t, effectRows(t, api, ctx))
}

type commitPointDecider struct {
	fakeDecider
	calls       atomic.Int32
	commitPoint func(context.Context) (actiongate.ExecutionDecision, error)
}

func (d *commitPointDecider) Execution(ctx context.Context, _ actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	if d.calls.Add(1) == 2 && d.commitPoint != nil {
		return d.commitPoint(ctx)
	}
	return execApprove(1, "boss"), nil
}

func TestStallingDecisionHitsTheRPCDeadline(t *testing.T) {
	api, ctx, p, digest, person := commitPointSetup(t)
	api.AgentDecider = &commitPointDecider{commitPoint: func(ctx context.Context) (actiongate.ExecutionDecision, error) {
		select {
		case <-ctx.Done():
			return actiongate.ExecutionDecision{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return execApprove(1, "boss"), nil
		}
	}}
	start := time.Now()
	_, err := api.ApproveAgentProposal(ctx, person, p.Token, digest)
	elapsed := time.Since(start)
	require.True(t, errors.Is(err, actiongate.ErrPolicyUnavailable), "got %v", err)
	require.Less(t, elapsed, 5*time.Second)
	requirePGUnexecuted(t, api, ctx, p.Token)
}

func TestStallAfterCommitPointCannotCommitLate(t *testing.T) {
	api, ctx, p, digest, person := commitPointSetup(t)
	api.AgentDecider = &commitPointDecider{commitPoint: func(context.Context) (actiongate.ExecutionDecision, error) {
		// Ignore the RPC deadline. Postgres terminates the session after 5 s
		// idle in transaction, so COMMIT cannot land even with a late answer.
		time.Sleep(6 * time.Second)
		return execApprove(1, "boss"), nil
	}}
	_, err := api.ApproveAgentProposal(ctx, person, p.Token, digest)
	require.ErrorIs(t, err, actiongate.ErrPolicyUnavailable)
	// The API's normal read path obtains a fresh handle after termination.
	requirePGUnexecuted(t, api, ctx, p.Token)
}

func TestLateAnswerAfterTheDeadlineIsDiscarded(t *testing.T) {
	api, ctx, p, digest, person := commitPointSetup(t)
	api.AgentDecider = &commitPointDecider{commitPoint: func(ctx context.Context) (actiongate.ExecutionDecision, error) {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		return execApprove(1, "boss"), nil
	}}
	_, err := api.ApproveAgentProposal(ctx, person, p.Token, digest)
	require.ErrorIs(t, err, actiongate.ErrPolicyUnavailable)
	requirePGUnexecuted(t, api, ctx, p.Token)
}

func TestCumulativeTailCannotExceedTheBound(t *testing.T) {
	api, ctx, p, digest, person := commitPointSetup(t)
	api.AgentDecider = &commitPointDecider{}
	// Negative control: each statement resets the idle timer. The idle bound
	// alone admits this seven-second tail; the zero-statement rule forbids it.
	api.afterDecisionForTest = func(tx *gorm.DB) error {
		time.Sleep(3500 * time.Millisecond)
		if err := tx.Exec("SELECT 1").Error; err != nil {
			return err
		}
		time.Sleep(3500 * time.Millisecond)
		return tx.Exec("SELECT 1").Error
	}
	start := time.Now()
	got, err := api.ApproveAgentProposal(ctx, person, p.Token, digest)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Len(t, effectRows(t, api, ctx), 1)
	require.GreaterOrEqual(t, elapsed, 7*time.Second)
}

func TestSingleIdleTailOverTheBoundIsTerminated(t *testing.T) {
	api, ctx, p, digest, person := commitPointSetup(t)
	api.AgentDecider = &commitPointDecider{}
	api.afterDecisionForTest = func(*gorm.DB) error {
		time.Sleep(6 * time.Second)
		return nil
	}
	_, err := api.ApproveAgentProposal(ctx, person, p.Token, digest)
	require.ErrorIs(t, err, actiongate.ErrPolicyUnavailable)
	requirePGUnexecuted(t, api, ctx, p.Token)
}

func TestRefusalUnwindKeepsTheErasureFence(t *testing.T) {
	api, ctx, p, digest, person := commitPointSetup(t)
	systemCtx := core.WithSystemContext(context.Background())
	plan, err := tenantpurge.Classify(systemCtx, api.RDB.Database)
	require.NoError(t, err)
	tenant, ok := core.TenantFromContext(ctx)
	require.True(t, ok)
	t.Cleanup(func() {
		require.NoError(t, tenantpurge.LiftFence(systemCtx, api.RDB.Database, plan, tenant, time.Now(), nil))
	})
	api.AgentDecider = &commitPointDecider{commitPoint: func(context.Context) (actiongate.ExecutionDecision, error) {
		now := time.Now()
		// Use the database pool, not the execution transaction's connection.
		if _, err := tenantpurge.PlantFence(systemCtx, api.RDB.Database, plan, tenant, now, now, nil); err != nil {
			return actiongate.ExecutionDecision{}, err
		}
		return actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse,
			Reasons: []string{"PROPOSER_NOT_IN_STANDING"}, ActuationGeneration: 3, PolicyVersion: 4}, nil
	}}
	// The REFUSED write either precedes purge or is refused ErrTenantPurged.
	// Execution already memoized a clear fence when writing effects; reusing
	// that transaction for refusal after unwind would admit a stale answer.
	_, err = api.ApproveAgentProposal(ctx, person, p.Token, digest)
	require.True(t, errors.Is(err, rdb.ErrTenantPurged), "got %v", err)
	requirePGUnexecuted(t, api, ctx, p.Token)
}
