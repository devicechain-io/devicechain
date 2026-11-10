// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// What only a real PostgreSQL can show about the agent-proposal ledger: the counters hold under
// concurrent proposals from two replicas' worth of writers (the advisory locks serialize them), and
// two racing approvals of the same proposal cannot both land. The migrations under test are the
// real chain. Run like the other integration tests beside it:
//
//	docker run -d --name dc-agentic-pg -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-agentic-pg 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'AgentProposal' -v
package model

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
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
// again on what is there, and the approvals hold each principal once.
func TestAgentProposalApprovalsCompareAndSetUnderRace(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&Alarm{
		TokenReference: rdb.TokenReference{Token: "a1"}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: "k", MetricKey: "m", State: string(AlarmStateActive), Severity: string(AlarmSeverityMajor), RaisedTime: time.Now(),
	}).Error)
	d := &fakeDecider{act: approveOne(), exec: execApprove(2, "boss", "carol")}
	api.AgentDecider = d
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
			require.True(t, errors.Is(err, actiongate.ErrExecutionNotAvailable), "got %v", err)
		}
	}
	require.Equal(t, 1, succeeded)
	p, err := api.AgentProposalByToken(ctx, out.Proposal.Token, nil)
	require.NoError(t, err)
	approvals, err := actiongate.ParseApprovals(p.Approvals)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	require.Len(t, actiongate.Principals(approvals), 1)
	require.Equal(t, 1, p.Revision)
	require.Equal(t, string(actiongate.StateProposed), p.State)
}
