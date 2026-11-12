// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
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
		require.NotNil(t, alarms[0].AcknowledgedBy.String)
		require.Equal(t, "boss", alarms[0].AcknowledgedBy.String, "acknowledged as the approving person, not the agent")
	} else {
		require.Equal(t, string(AlarmStateCleared), alarms[0].State)
	}
	_, drainErr := r.api.DrainAgentEffects(ctx, AgentEffectsDrainPageSize)
	require.NoError(t, drainErr)
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
			require.Empty(t, effectRows(t, r.api, ctx))
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
	require.Empty(t, effectRows(t, r.api, ctx))
	alarms, readErr := r.api.AlarmsByToken(ctx, []string{"a1", "a2"})
	require.NoError(t, readErr)
	for _, alarm := range alarms {
		require.False(t, alarm.Acknowledged)
	}
	require.NoError(t, r.db.Callback().Update().Remove("fail_alarm"))
	got, err = r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	_, drainErr := r.api.DrainAgentEffects(ctx, AgentEffectsDrainPageSize)
	require.NoError(t, drainErr)
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
	require.Zero(t, pub.calls)
	row, err := r.api.AgentProposalByToken(ctx, p.Token, nil)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), row.State)
	_, err = r.api.DrainAgentEffects(ctx, AgentEffectsDrainPageSize)
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
	require.Empty(t, effectRows(t, r.api, ctx))
}

// A stored dependency set that does not match the stored targets one for one (an extra or a
// missing entry) is a change, not a pass: every named alarm still exists, so only the shape check
// can see it.
func TestDependencySetMustMatchTargetsExactly(t *testing.T) {
	for name, deps := range map[string]string{
		"extra":   `{"raisedTimes":{"a1":"%s","ghost":"2020-01-01T00:00:00Z"}}`,
		"missing": `{"raisedTimes":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			stored := r.stored(t, p.Token)
			var a Alarm
			require.NoError(t, r.db.WithContext(ctx).Where("token = ?", "a1").First(&a).Error)
			doc := deps
			if name == "extra" {
				doc = strings.Replace(deps, "%s", a.RaisedTime.UTC().Format(time.RFC3339Nano), 1)
			}
			require.NoError(t, r.db.WithContext(ctx).Model(&AgentProposal{}).Where("token = ?", stored.Token).
				Update("dependencies", datatypes.JSON(doc)).Error)
			got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			require.NoError(t, err)
			require.Equal(t, string(actiongate.StateRefused), got.State)
			require.JSONEq(t, `["DEPENDENCY_CHANGED"]`, string(got.Reasons))
			require.Empty(t, effectRows(t, r.api, ctx))
			alarms, err := r.api.AlarmsByToken(ctx, []string{"a1"})
			require.NoError(t, err)
			require.False(t, alarms[0].Acknowledged)
		})
	}
}

// The policy is asked AFTER the alarm rows are read (and, on Postgres, locked), never before: a wait
// for an alarm row must not sit between the decision and the effect.
func TestDecisionIsAskedAfterTheAlarmsAreLocked(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	var order []string
	require.NoError(t, r.db.Callback().Query().After("gorm:query").Register("order_alarm_reads", func(db *gorm.DB) {
		if db.Statement.Table == "alarms" {
			order = append(order, "alarms")
		}
	}))
	r.decider.second = func() (actiongate.ExecutionDecision, error) {
		order = append(order, "decision")
		return r.decider.exec, nil
	}
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.NotEmpty(t, order)
	require.Equal(t, "alarms", order[0])
	require.Contains(t, order, "decision")
}

func TestAgentExecutionSQLiteSkipsTimeouts(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	require.Equal(t, "sqlite", r.db.Dialector.Name())
	var rawStatements []string
	require.NoError(t, r.db.Callback().Raw().Before("gorm:raw").Register("capture_execution_raw", func(db *gorm.DB) {
		rawStatements = append(rawStatements, db.Statement.SQL.String())
	}))
	t.Cleanup(func() { r.db.Callback().Raw().Remove("capture_execution_raw") })
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Empty(t, rawStatements, "SQLite must not execute SET LOCAL")
}

func TestAgentExecutionLockErrors(t *testing.T) {
	for _, code := range []string{"55P03", "57014", "40P01"} {
		t.Run(code, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code})
			got := agentExecutionLockError(err)
			if code == "55P03" {
				require.True(t, actiongate.IsCode(got, "CONCURRENT_APPROVAL"))
			} else {
				require.Same(t, err, got)
			}
		})
	}
}

func TestAgentExecutionLocksSortedCopyAndPreservesEffectOrder(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1", "a2")
	require.NoError(t, r.db.WithContext(ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).
		Update("target_set", datatypes.JSON(`["a2","a1"]`)).Error)
	var lockTokens []any
	require.NoError(t, r.db.Callback().Query().After("gorm:query").Register("capture_alarm_lock", func(db *gorm.DB) {
		if db.Statement.Table == "alarms" {
			if _, ok := db.Statement.Clauses["FOR"]; ok {
				require.Contains(t, db.Statement.SQL.String(), "ORDER BY token")
				lockTokens = append(lockTokens, db.Statement.Vars...)
			}
		}
	}))
	t.Cleanup(func() { r.db.Callback().Query().Remove("capture_alarm_lock") })
	var effects []string
	require.NoError(t, r.db.Callback().Update().Before("gorm:update").Register("capture_alarm_effect", func(db *gorm.DB) {
		if alarm, ok := db.Statement.Model.(*Alarm); ok {
			effects = append(effects, alarm.Token)
		}
	}))
	t.Cleanup(func() { r.db.Callback().Update().Remove("capture_alarm_effect") })
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(lockTokens), 2)
	require.Equal(t, []any{"a1", "a2"}, lockTokens[:2])
	require.Equal(t, []string{"a2", "a1"}, effects)
}

func (p *committedAlarmPublisher) PublishAlarmEventDedup(ctx context.Context, ev *AlarmStateChangeEvent, dedupID string) error {
	p.PublishAlarmEvent(ctx, ev)
	return nil
}
