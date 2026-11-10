// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// What only a real PostgreSQL can show about the agent-proposal ledger: the counters hold under
// concurrent proposals from two replicas' worth of writers (the advisory locks serialize them), and
// final approvals consume the proposal and effect only once. The migrations under test are the
// real chain. Run like the other integration tests beside it:
//
//	docker run -d --name dc-agentic-s4-pg -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-agentic-s4-pg 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -p 2 -tags integration -count=1 ./model/... -run 'AgentProposal|ExecuteConsumes|AgentExecution' -v
package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Two clients of ONE person race proposals at the per-principal limit: exactly the limit lands,
// however the writers interleave. Without the principal lock each client's count would be checked
// alone and both could take the last slot.
func TestAgentProposalCountersHoldUnderConcurrentProposals(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{
		TokenReference: rdb.TokenReference{Token: "a1"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: "k", MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now(),
	}).Error)
	const limit = 5
	d := &fakeDecider{act: approveOne()}
	d.act.ActionsPerHour = limit
	api.AgentDecider = d

	const racers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *ProposalOutcome, racers)
	errs := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := actiongate.Proposer{Tenant: "t", Principal: "ana@example.invalid", Client: fmt.Sprintf("client-%d", i%2), Sid: "s"}
			<-start
			out, err := api.ProposeAlarmAction(ctx, p, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: fmt.Sprintf("r%d", i)})
			if err != nil {
				errs <- err
				return
			}
			results <- out
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("a racer failed: %v", err)
	}
	pending := 0
	for out := range results {
		if out.Status == ProposalStatusPending {
			pending++
		}
	}
	require.Equal(t, limit, pending, "exactly the per-principal limit lands")
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&AgentProposal{}).Count(&n).Error)
	require.EqualValues(t, limit, n)
}

// Two approvers racing the first approval of a two-person proposal: one lands, the other decides
// again on what is there, and the second executes with each principal recorded once.
func TestAgentProposalApprovalsCompareAndSetUnderRace(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{
		TokenReference: rdb.TokenReference{Token: "a1"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: "k", MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now(),
	}).Error)
	d := &fakeDecider{act: approveOne(), exec: execApprove(2, "boss", "carol")}
	api.AgentDecider = &fixedExecutionDecider{fakeDecider: *d}
	out, err := api.ProposeAlarmAction(ctx, rigAgent, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: "r"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, who := range []string{"boss", "carol"} {
		wg.Add(1)
		go func(who string) {
			defer wg.Done()
			<-start
			_, err := api.ApproveAgentProposal(ctx, approver(who, time.Now()), out.Proposal.Token, out.Digest)
			errs <- err
		}(who)
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else {
			require.True(t, errors.Is(err, actiongate.ErrNotPending) || errors.Is(err, errConcurrentApproval), "got %v", err)
		}
	}
	require.Equal(t, 2, succeeded)
	p, err := api.AgentProposalByToken(ctx, out.Proposal.Token, nil)
	require.NoError(t, err)
	approvals, err := actiongate.ParseApprovals(p.Approvals)
	require.NoError(t, err)
	require.Len(t, approvals, 2)
	require.Len(t, actiongate.Principals(approvals), 2)
	require.Equal(t, 2, p.Revision)
	require.Equal(t, string(actiongate.StateExecuted), p.State)
}

// concurrentAlarmPublisher is safe across the eight final-approval callers.
type concurrentAlarmPublisher struct{ calls atomic.Int32 }

func (p *concurrentAlarmPublisher) PublishAlarmEvent(context.Context, *AlarmStateChangeEvent) {
	p.calls.Add(1)
}

// fixedExecutionDecider has no mutable request log, so the race tests do not race the fake.
type fixedExecutionDecider struct{ fakeDecider }

func (d *fixedExecutionDecider) Execution(context.Context, actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	return d.exec, nil
}

func TestExecuteConsumesApprovalOnce(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{TokenReference: rdb.TokenReference{Token: "execute"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1, AlarmKey: "execute", MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now()}).Error)
	api.AgentDecider = &fixedExecutionDecider{fakeDecider: fakeDecider{act: approveOne(), exec: execApprove(1, "boss")}}
	out, err := api.ProposeAlarmAction(ctx, rigAgent, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"execute"}, RequestId: "execute"})
	require.NoError(t, err)
	replica := NewApi(api.RDB)
	replica.AgentDecider = api.AgentDecider
	pubs := []*concurrentAlarmPublisher{{}, {}}
	api.AlarmPublisher = pubs[0]
	replica.AlarmPublisher = pubs[1]
	apis := []*Api{api, replica}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var executed atomic.Int32
	person := approver("boss", time.Now())
	ctx = auth.WithClaims(ctx, person.Claims)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got, err := apis[i%2].ApproveAgentProposal(ctx, person, out.Proposal.Token, out.Digest)
			if err != nil {
				require.True(t, errors.Is(err, actiongate.ErrNotPending) || errors.Is(err, errConcurrentApproval), "got %v", err)
				return
			}
			require.Equal(t, string(actiongate.StateExecuted), got.State)
			executed.Add(1)
		}(i)
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 1, executed.Load())
	require.EqualValues(t, 1, pubs[0].calls.Load()+pubs[1].calls.Load())
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&rdb.AuditEvent{}).Where("table_name = ? AND operation = ? AND proposal = ?", "alarms", "update", out.Proposal.Token).Count(&n).Error)
	require.EqualValues(t, 1, n)
}

func TestAgentExecutionChangedCycleAndFailedEffect(t *testing.T) {
	for _, mode := range []string{"cycle", "fault"} {
		t.Run(mode, func(t *testing.T) {
			api, ctx := newPostgresIntegrationApi(t)
			require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{TokenReference: rdb.TokenReference{Token: "a1"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1, AlarmKey: "k", MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now()}).Error)
			api.AgentDecider = &fakeDecider{act: approveOne(), exec: execApprove(1, "boss")}
			out, err := api.ProposeAlarmAction(ctx, rigAgent, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: "r"})
			require.NoError(t, err)
			if mode == "cycle" {
				require.NoError(t, api.RDB.DB(ctx).Model(&Alarm{}).Where("token = ?", "a1").Update("raised_time", time.Now().Add(time.Second)).Error)
			} else {
				require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register("execution_fault", func(db *gorm.DB) {
					if db.Statement.Table == "alarms" {
						db.AddError(errors.New("effect fault"))
					}
				}))
				defer api.RDB.Database.Callback().Update().Remove("execution_fault")
			}
			_, err = api.ApproveAgentProposal(ctx, approver("boss", time.Now()), out.Proposal.Token, out.Digest)
			if mode == "fault" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			p, err := api.AgentProposalByToken(ctx, out.Proposal.Token, nil)
			require.NoError(t, err)
			if mode == "fault" {
				require.Equal(t, string(actiongate.StateProposed), p.State)
				require.Zero(t, p.Revision)
				require.JSONEq(t, `[]`, string(p.Approvals))
			} else {
				require.Equal(t, string(actiongate.StateRefused), p.State)
				require.JSONEq(t, `["DEPENDENCY_CHANGED"]`, string(p.Reasons))
			}
			alarms, err := api.AlarmsByToken(ctx, []string{"a1"})
			require.NoError(t, err)
			require.False(t, alarms[0].Acknowledged)
		})
	}
}

func TestConcurrentFinalApprovalReturnsConcurrentApprovalFast(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{
		TokenReference: rdb.TokenReference{Token: "a1"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: "k", MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now(),
	}).Error)
	api.AgentDecider = &fixedExecutionDecider{fakeDecider: fakeDecider{act: approveOne(), exec: execApprove(1, "boss")}}
	out, err := api.ProposeAlarmAction(ctx, rigAgent, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: "nowait"})
	require.NoError(t, err)
	tx := api.RDB.DB(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	var locked AgentProposal
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token = ?", out.Proposal.Token).First(&locked).Error)
	start := time.Now()
	_, err = api.ApproveAgentProposal(ctx, approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	elapsed := time.Since(start)
	require.True(t, actiongate.IsCode(err, "CONCURRENT_APPROVAL"), "got %v", err)
	require.Less(t, elapsed, 2*time.Second)
	require.NoError(t, tx.Rollback().Error)
	got, err := api.ApproveAgentProposal(ctx, approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
}

func TestAgentExecutionTimeouts(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := setAgentExecutionTimeouts(tx); err != nil {
			return err
		}
		var lock, statement string
		if err := tx.Raw("SHOW lock_timeout").Scan(&lock).Error; err != nil {
			return err
		}
		if err := tx.Raw("SHOW statement_timeout").Scan(&statement).Error; err != nil {
			return err
		}
		require.Equal(t, lockTimeout, lock)
		require.Equal(t, statementTimeout, statement)
		return nil
	}))
}

func TestOverlappingTargetExecutionsDoNotDeadlock(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	for _, token := range []string{"a1", "a2"} {
		require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{
			TokenReference: rdb.TokenReference{Token: token}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
			AlarmKey: token, MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now(),
		}).Error)
	}
	api.AgentDecider = &fixedExecutionDecider{fakeDecider: fakeDecider{act: approveOne(), exec: execApprove(1, "boss")}}
	replica := NewApi(api.RDB)
	replica.AgentDecider = api.AgentDecider
	apis := []*Api{api, replica}
	for iteration := 0; iteration < 20; iteration++ {
		require.NoError(t, api.RDB.DB(ctx).Model(&Alarm{}).Where("token IN ?", []string{"a1", "a2"}).Update("acknowledged", false).Error)
		outcomes := make([]*ProposalOutcome, 2)
		for i := range outcomes {
			out, err := apis[i].ProposeAlarmAction(ctx, rigAgent, AlarmProposalRequest{
				Action: AlarmProposalAck, AlarmTokens: []string{"a1", "a2"}, RequestId: fmt.Sprintf("overlap-%d-%d", iteration, i),
			})
			require.NoError(t, err)
			require.Equal(t, ProposalStatusPending, out.Status)
			outcomes[i] = out
		}
		// Proposal creation canonicalizes targets. Force the opposite stored order for the race.
		require.NoError(t, api.RDB.DB(ctx).Model(&AgentProposal{}).Where("token = ?", outcomes[1].Proposal.Token).
			Update("target_set", datatypes.JSON(`["a2","a1"]`)).Error)
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range apis {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = apis[i].ApproveAgentProposal(ctx, approver("boss", time.Now()), outcomes[i].Proposal.Token, outcomes[i].Digest)
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			var pgerr *pgconn.PgError
			if errors.As(err, &pgerr) {
				require.NotEqual(t, "40P01", pgerr.Code, "iteration %d", iteration)
			}
			if err != nil {
				require.True(t, actiongate.IsCode(err, "CONCURRENT_APPROVAL"), "got %v", err)
				_, err = apis[i].ApproveAgentProposal(ctx, approver("boss", time.Now()), outcomes[i].Proposal.Token, outcomes[i].Digest)
			}
			require.NoError(t, err)
			stored, err := api.AgentProposalByToken(ctx, outcomes[i].Proposal.Token, nil)
			require.NoError(t, err)
			require.Equal(t, string(actiongate.StateExecuted), stored.State)
		}
	}
}
