// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Track each armed execution transaction, including retries. Refusal and reenable
// transactions may write after rollback, and the API reads the result after commit.
type commitPointStatements struct {
	armed      bool
	lastPool   gorm.ConnPool
	armedPools []gorm.ConnPool
	statements []string
}

func (w *commitPointStatements) arm() {
	w.armedPools = append(w.armedPools, w.lastPool)
	w.armed = true
}

func watchCommitPointStatements(t *testing.T, db *gorm.DB) *commitPointStatements {
	t.Helper()
	w := &commitPointStatements{}
	for _, cb := range []struct {
		kind     string
		register func(string, func(*gorm.DB)) error
		remove   func(string) error
	}{
		{"create", db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove},
		{"query", db.Callback().Query().Before("gorm:query").Register, db.Callback().Query().Remove},
		{"update", db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove},
		{"delete", db.Callback().Delete().Before("gorm:delete").Register, db.Callback().Delete().Remove},
		{"row", db.Callback().Row().Before("gorm:row").Register, db.Callback().Row().Remove},
		{"raw", db.Callback().Raw().Before("gorm:raw").Register, db.Callback().Raw().Remove},
	} {
		kind, remove := cb.kind, cb.remove
		name := "commit_point_tail_" + kind
		require.NoError(t, cb.register(name, func(db *gorm.DB) {
			if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); !inTx {
				return
			}
			inArmedPool := slices.Contains(w.armedPools, db.Statement.ConnPool)
			if !w.armed || !inArmedPool {
				w.lastPool = db.Statement.ConnPool
			}
			if w.armed && inArmedPool {
				statement := db.Statement.Table
				if kind == "row" || kind == "raw" {
					statement = db.Statement.SQL.String()
				}
				w.statements = append(w.statements, kind+": "+statement)
			}
		}))
		t.Cleanup(func() { require.NoError(t, remove(name)) })
	}
	return w
}

func TestNoStatementBetweenDecisionAndCommit(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	w := watchCommitPointStatements(t, r.db)
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		w.arm()
		return r.decider.exec, nil
	}
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	w.armed = false
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Len(t, r.decider.execReqs, 2)
	require.NotEmpty(t, w.armedPools, "must observe the execution transaction")
	require.Len(t, w.statements, 0, "statements after decision: %v", w.statements)
}

func TestNoStatementBetweenDecisionAndCommitOnRefusal(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	w := watchCommitPointStatements(t, r.db)
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		w.arm()
		return actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse,
			Reasons: []string{"PROPOSER_NOT_IN_STANDING"}, ActuationGeneration: 3, PolicyVersion: 4}, nil
	}
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	w.armed = false
	require.NoError(t, err)
	requireCommitPointRefused(t, r, ctx, got, "PROPOSER_NOT_IN_STANDING")
	require.Len(t, r.decider.execReqs, 2)
	require.NotEmpty(t, w.armedPools, "must observe the execution transaction")
	require.Len(t, w.statements, 0, "statements after refusal in execution transaction: %v", w.statements)
}

type armingDecider struct {
	*fakeDecider
	w     *commitPointStatements
	calls int
}

func (d *armingDecider) Execution(ctx context.Context, req actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	d.calls++
	decision, err := d.fakeDecider.Execution(ctx, req)
	if d.calls >= 2 {
		d.w.arm()
	}
	return decision, err
}

func TestNoStatementBetweenDecisionAndCommitOnReenableRetry(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	v := r.decider.exec.ActuationGeneration + 1
	require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, v)))
	r.decider.exec.ActuationGeneration = v + 1
	w := watchCommitPointStatements(t, r.db)
	d := &armingDecider{fakeDecider: r.decider, w: w}
	r.api.AgentDecider = d

	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	w.armed = false
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Equal(t, 3, d.calls)
	require.Len(t, r.decider.execReqs, 3)
	require.Len(t, w.armedPools, 2, "must observe both execution transactions")
	require.Len(t, w.statements, 0, "statements after decision: %v", w.statements)
}

func afterCommitPointAlarmEffect(t *testing.T, r *proposalRig, change func()) {
	t.Helper()
	const name = "commit_point_alarm_effect"
	fired := false
	require.NoError(t, r.db.Callback().Update().After("gorm:update").Register(name, func(db *gorm.DB) {
		if !fired && db.Statement.Table == "alarms" && db.Error == nil {
			fired = true
			change()
		}
	}))
	t.Cleanup(func() { require.NoError(t, r.db.Callback().Update().Remove(name)) })
}

func requireCommitPointRefused(t *testing.T, r *proposalRig, ctx context.Context, got *AgentProposal, reason string) {
	t.Helper()
	require.NotNil(t, got)
	require.Equal(t, string(actiongate.StateRefused), got.State)
	var reasons []string
	require.NoError(t, json.Unmarshal(got.Reasons, &reasons))
	require.Contains(t, reasons, reason)
	alarms, err := r.api.AlarmsByToken(ctx, []string{"a1"})
	require.NoError(t, err)
	require.Len(t, alarms, 1)
	require.False(t, alarms[0].Acknowledged)
	require.Empty(t, effectRows(t, r.api, ctx))
}

func TestRevocationAfterEffectsBeforeDecisionRefuses(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	afterCommitPointAlarmEffect(t, r, func() { r.decider.exec = execApprove(1) })
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, errNotAnApprover)
	requireUnexecuted(t, r, p, ctx)
	require.Zero(t, r.stored(t, p.Token).Revision)
}

func TestProposerRevokedDuringExecutionEndsRefused(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	afterCommitPointAlarmEffect(t, r, func() {
		r.decider.exec = actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse,
			Reasons: []string{"PROPOSER_NOT_IN_STANDING"}, ActuationGeneration: 3, PolicyVersion: 4}
	})
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	requireCommitPointRefused(t, r, ctx, got, "PROPOSER_NOT_IN_STANDING")
}

func TestPolicyRepublishedDuringExecutionRefusesWhenTightened(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	afterCommitPointAlarmEffect(t, r, func() { r.decider.exec = execApprove(2, "boss", "carol") })
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, errConcurrentApproval)
	requireUnexecuted(t, r, p, ctx)
	require.Zero(t, r.stored(t, p.Token).Revision)
}

func TestKillCommittedInUmBeforeEventConsumedRefuses(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	// user-management's operator kill returns OPERATOR_SUSPENDED. Its event has
	// not reached the local fence, so the fresh decision must stop execution.
	const reason = "OPERATOR_SUSPENDED"
	afterCommitPointAlarmEffect(t, r, func() {
		r.decider.exec = actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse,
			Reasons: []string{reason}, ActuationGeneration: 4, PolicyVersion: 4}
	})
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	requireCommitPointRefused(t, r, ctx, got, reason)
}

func TestRevocationAfterCommitPointIsTheResidual(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		answer := r.decider.exec
		// The answer is already fixed when this revocation lands: this is the
		// documented residual window between the commit point and commit.
		r.decider.exec = execApprove(1)
		return answer, nil
	}
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Len(t, effectRows(t, r.api, ctx), 1)
}

// The approval count written with the EXECUTED transition is the pre-decision's figure, written
// before the commit-point decision; the commit-point decision must require the same figure, or
// the row would record a requirement that no decision judged.
func TestCommitPointRequiredApprovalsMustMatchTheFigureWritten(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.decider.exec = execApprove(2, "boss", "carol")
	_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	// carol's approval is final under the pre-decision (two required); the policy is loosened
	// to one after the effects are written and before the commit-point decision is asked.
	afterCommitPointAlarmEffect(t, r, func() { r.decider.exec = execApprove(1, "boss", "carol") })
	_, err = r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("carol", time.Now()), out.Proposal.Token, out.Digest)
	require.ErrorIs(t, err, errConcurrentApproval)
	stored := r.stored(t, out.Proposal.Token)
	require.Equal(t, string(actiongate.StateProposed), stored.State)
	require.Equal(t, 2, stored.RequiredApprovals)
	require.Empty(t, effectRows(t, r.api, tenantCtx(rigTenant)))
}

// An earlier approval stops counting when its approver loses standing after the effects were
// written: the commit-point decision's valid approvers are the ones tallied, not the
// pre-decision's. The final approver is still valid and the requirement is unchanged, so only the
// tally can stop this effect.
func TestEarlierApproverRevokedBeforeCommitPointStopsCounting(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.decider.exec = execApprove(2, "boss", "carol")
	_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	afterCommitPointAlarmEffect(t, r, func() { r.decider.exec = execApprove(2, "carol") })
	_, err = r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("carol", time.Now()), out.Proposal.Token, out.Digest)
	require.ErrorIs(t, err, errConcurrentApproval)
	require.Equal(t, string(actiongate.StateProposed), r.stored(t, out.Proposal.Token).State)
	require.Empty(t, effectRows(t, r.api, tenantCtx(rigTenant)))
}
