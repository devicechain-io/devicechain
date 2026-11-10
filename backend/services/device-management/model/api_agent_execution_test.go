// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func executionSetup(t *testing.T, action AlarmProposalAction, tokens ...string) (*proposalRig, *AgentProposal, context.Context, actiongate.Approver) {
	t.Helper()
	r := newProposalRig(t)
	req := clearReq("execution", tokens...)
	req.Action = action
	p := r.propose(t, rigAgent, req).Proposal
	r.decider.exec = execApprove(1, "boss")
	person := approver("boss", time.Now())
	ctx := auth.WithBearer(auth.WithClaims(tenantCtx(rigTenant), person.Claims), "human-bearer")
	return r, p, ctx, person
}

func TestFinalApprovalExecutesAckOnce(t *testing.T)   { testFinalApproval(t, AlarmProposalAck) }
func TestFinalApprovalExecutesClearOnce(t *testing.T) { testFinalApproval(t, AlarmProposalClear) }
func testFinalApproval(t *testing.T, action AlarmProposalAction) {
	r, p, ctx, person := executionSetup(t, action, "a1")
	pub := &captureAlarmPublisher{}
	r.api.AlarmPublisher = pub
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Equal(t, p.EffectToken, *got.ResultRef)
	require.NotNil(t, got.ExecutedAt)
	approvals, err := actiongate.ParseApprovals(got.Approvals)
	require.NoError(t, err)
	require.Equal(t, []string{"boss"}, actiongate.Principals(approvals))
	alarms, err := r.api.AlarmsByToken(ctx, []string{"a1"})
	require.NoError(t, err)
	if action == AlarmProposalAck {
		require.True(t, alarms[0].Acknowledged)
	} else {
		require.Equal(t, string(AlarmStateCleared), alarms[0].State)
	}
	require.Len(t, pub.events, 1)
	_, err = r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, actiongate.ErrNotPending)
	require.Len(t, pub.events, 1)
}

func TestExecutionAuditNamesBothPrincipals(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	var rows []rdb.AuditEvent
	require.NoError(t, r.db.WithContext(ctx).Where("operation = ? AND proposal = ?", "update", p.Token).Find(&rows).Error)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Equal(t, "boss", row.Actor)
		require.Equal(t, rigAgent.Client, *row.ClientId)
		require.Equal(t, rigAgent.Principal, *row.OnBehalfOf)
		require.Equal(t, p.Token, *row.Proposal)
	}
	require.NoError(t, r.db.WithContext(ctx).Model(&Alarm{}).Where("token = ?", "a1").Update("acknowledged", false).Error)
	_, err = r.api.AcknowledgeAlarm(ctx, "a1", &person.Principal)
	require.NoError(t, err)
	var control rdb.AuditEvent
	require.NoError(t, r.db.WithContext(ctx).Order("id desc").First(&control).Error)
	require.Nil(t, control.OnBehalfOf)
	require.Nil(t, control.Proposal)
}

func TestChangedAlarmCycleRefusesExecution(t *testing.T) {
	for _, mode := range []string{"raised", "deleted", "second"} {
		t.Run(mode, func(t *testing.T) {
			targets := []string{"a1"}
			changed := "a1"
			if mode == "second" {
				targets = append(targets, "a2")
				changed = "a2"
			}
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, targets...)
			q := r.db.WithContext(ctx).Where("token = ?", changed)
			if mode == "deleted" {
				require.NoError(t, q.Delete(&Alarm{}).Error)
			} else {
				require.NoError(t, q.Model(&Alarm{}).Update("raised_time", time.Now()).Error)
			}
			got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			require.NoError(t, err)
			require.Equal(t, string(actiongate.StateRefused), got.State)
			require.JSONEq(t, `["DEPENDENCY_CHANGED"]`, string(got.Reasons))
			alarms, err := r.api.AlarmsByToken(ctx, targets)
			require.NoError(t, err)
			for _, a := range alarms {
				require.False(t, a.Acknowledged)
			}
		})
	}
}

func TestKillFlippedBetweenApprovalAndExecutionRefuses(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		return actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse, Reasons: []string{"TENANT_SUSPENDED"}}, nil
	}
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateRefused), got.State)
	require.Contains(t, string(got.Reasons), "TENANT_SUSPENDED")
	require.Equal(t, []string{"human-bearer", "human-bearer"}, r.decider.execBearers)
	alarms, readErr := r.api.AlarmsByToken(ctx, []string{"a1"})
	require.NoError(t, readErr)
	require.False(t, alarms[0].Acknowledged)

}

func TestDeciderErrorAtExecutionLeavesPending(t *testing.T) { testSecondDecisionFailure(t, true) }
func TestExecutionRequiresDecisionApproverStillValid(t *testing.T) {
	testSecondDecisionFailure(t, false)
}
func testSecondDecisionFailure(t *testing.T, unavailable bool) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		if unavailable {
			return actiongate.ExecutionDecision{}, errors.New("offline")
		}
		return execApprove(1, "other"), nil
	}
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	if unavailable {
		require.ErrorIs(t, err, actiongate.ErrPolicyUnavailable)
	} else {
		require.ErrorIs(t, err, errNotAnApprover)
	}
	require.Equal(t, string(actiongate.StateProposed), r.stored(t, p.Token).State)
	alarms, e := r.api.AlarmsByToken(ctx, []string{"a1"})
	require.NoError(t, e)
	require.False(t, alarms[0].Acknowledged)
}

func TestFailedEffectLeavesProposalUnexecuted(t *testing.T) { testFailedAlarmEffect(t) }

func testFailedAlarmEffect(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1", "a2")
	pub := &captureAlarmPublisher{}
	r.api.AlarmPublisher = pub
	require.NoError(t, r.db.Callback().Update().Before("gorm:update").Register("fail_alarm", func(db *gorm.DB) {
		if alarm, ok := db.Statement.Model.(*Alarm); ok && alarm.Token == "a2" {
			db.AddError(errors.New("effect failed"))
		}
	}))
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.Error(t, err)
	got := r.stored(t, p.Token)
	require.Equal(t, string(actiongate.StateProposed), got.State)
	require.Zero(t, got.Revision)
	require.JSONEq(t, `[]`, string(got.Approvals))
	require.Empty(t, pub.events)
	alarms, readErr := r.api.AlarmsByToken(ctx, []string{"a1", "a2"})
	require.NoError(t, readErr)
	for _, alarm := range alarms {
		require.False(t, alarm.Acknowledged)
	}
	require.NoError(t, r.db.Callback().Update().Remove("fail_alarm"))
	got, err = r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Len(t, pub.events, 2)
}

func TestRolledBackExecutionPublishesNothing(t *testing.T) {
	testFailedAlarmEffect(t)
}
func TestExecutedPublishesAfterCommit(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1", "a2")
	pub := &committedAlarmPublisher{t: t, api: r.api, token: p.Token}
	r.api.AlarmPublisher = pub
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, 2, pub.calls)
}

type committedAlarmPublisher struct {
	t     *testing.T
	api   *Api
	token string
	calls int
}

func (p *committedAlarmPublisher) PublishAlarmEvent(ctx context.Context, ev *AlarmStateChangeEvent) {
	row, err := p.api.AgentProposalByToken(ctx, p.token, nil)
	require.NoError(p.t, err)
	require.Equal(p.t, string(actiongate.StateExecuted), row.State)
	p.calls++
}

func TestNoAutoPathStillRefused(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		return actiongate.ExecutionDecision{Verdict: actiongate.VerdictAuto}, nil
	}
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateRefused), got.State)
	require.Contains(t, string(got.Reasons), actiongate.ReasonAutoNotAvailable)
}

func TestAgentExecutionMalformedStoredDataRollsBack(t *testing.T) {
	for _, tc := range []struct{ column, raw string }{
		{"target_set", `null`}, {"target_set", `{}`}, {"payload", `null`},
		{"payload", `{"action":"UNKNOWN"}`}, {"dependencies", `[]`}, {"dependencies", `null`},
	} {
		t.Run(tc.column+tc.raw, func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			require.NoError(t, r.db.WithContext(ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update(tc.column, tc.raw).Error)
			_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			require.Error(t, err)
			got := r.stored(t, p.Token)
			require.Equal(t, string(actiongate.StateProposed), got.State)
			require.Zero(t, got.Revision)
			alarms, err := r.api.AlarmsByToken(ctx, []string{"a1"})
			require.NoError(t, err)
			require.False(t, alarms[0].Acknowledged)
		})
	}
}

func TestAgentExecutionPolicyTightensBeforeEffect(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.decider.second = func() (actiongate.ExecutionDecision, error) { return execApprove(2, "boss", "carol"), nil }
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, errConcurrentApproval)
	require.Zero(t, r.stored(t, p.Token).Revision)
}

func TestAgentExecutionAlreadyAcknowledgedIsConsumedWithoutEvent(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	_, err := r.api.AcknowledgeAlarm(ctx, "a1", &person.Principal)
	require.NoError(t, err)
	pub := &captureAlarmPublisher{}
	r.api.AlarmPublisher = pub
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Empty(t, pub.events)
}
