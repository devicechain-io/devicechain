// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAgentTestMetrics(ms *core.Microservice) *AgentMetrics {
	return NewAgentMetrics(
		ms.NewCounterVec("agent_proposals_total", "Non-dry-run proposal calls by kind and outcome; errors count as refused.", []string{"kind", "outcome"}),
		ms.NewCounterVec("agent_approvals_total", "Approval calls by final outcome, counted once across internal retries; errors without a more specific outcome count as refused.", []string{"outcome"}),
		ms.NewCounterVec("agent_executions_total", "Final-approval executions by kind and result, counted once per execution including a fence re-enable retry; failed means the transaction could not complete (database or statement timeout, policy service or kill feed unavailable); a lock conflict is refused as a concurrent approval.", []string{"kind", "result"}),
		ms.NewHistogramVec("agent_execution_seconds", "Time spent executing final approvals, including refusals, failures and a fence re-enable retry.", nil, prometheus.DefBuckets).WithLabelValues(),
		ms.NewCounter("agent_proposals_expired_total", "Proposals moved to EXPIRED by the reaper."),
	)
}

func enableAgentTestMetrics(r *proposalRig) *AgentMetrics {
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	r.api.AgentMetrics = newAgentTestMetrics(ms)
	return r.api.AgentMetrics
}

func TestAgentProposalMetrics(t *testing.T) {
	r := newProposalRig(t)
	m := enableAgentTestMetrics(r)
	req := clearReq("metrics", "a1")
	r.propose(t, rigAgent, req)
	r.propose(t, rigAgent, req)
	r.decider.act.Verdict = actiongate.VerdictRefuse
	r.propose(t, rigAgent, clearReq("refused", "a1"))
	req.DryRun = true
	r.propose(t, rigAgent, req)
	for _, outcome := range []string{"created", "existing", "refused"} {
		require.Equal(t, float64(1), testutil.ToFloat64(m.proposals.WithLabelValues("ALARM_CLEAR", outcome)))
	}
	_, err := r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, AlarmProposalRequest{Action: "arbitrary-user-input"})
	require.Error(t, err)
	require.Equal(t, float64(1), testutil.ToFloat64(m.proposals.WithLabelValues("unknown", "refused")))
}

func TestAgentApprovalMetrics(t *testing.T) {
	for _, outcome := range []string{"recorded", "executed", "refused", "concurrent", "step_up_required", "forbidden", "fence_closed"} {
		t.Run(outcome, func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			m := enableAgentTestMetrics(r)
			switch outcome {
			case "recorded":
				r.decider.exec = execApprove(2, "boss", "carol")
			case "refused":
				r.decider.exec.Verdict = actiongate.VerdictRefuse
			case "concurrent":
				r.decider.exec = execApprove(2, "boss", "carol")
				require.NoError(t, r.db.Callback().Update().Before("gorm:update").Register("lose_approval_cas", func(db *gorm.DB) {
					if _, ok := db.Statement.Model.(*AgentProposal); ok {
						db.AddError(actiongate.ErrNotPending)
					}
				}))
			case "step_up_required":
				person = approver("boss", time.Now().Add(-time.Hour))
			case "fence_closed":
				require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, r.decider.exec.ActuationGeneration)))
			case "forbidden":
				r.decider.execErr = auth.ErrForbidden
			}
			got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			if outcome == "recorded" || outcome == "executed" || outcome == "refused" {
				require.NoError(t, err)
				require.NotNil(t, got)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, float64(1), testutil.ToFloat64(m.approvals.WithLabelValues(outcome)))
			for _, label := range []string{"recorded", "executed", "refused", "concurrent", "step_up_required", "forbidden", "fence_closed"} {
				want := float64(0)
				if label == outcome {
					want = 1
				}
				require.Equal(t, want, testutil.ToFloat64(m.approvals.WithLabelValues(label)))
			}
			if outcome == "concurrent" {
				require.Len(t, r.decider.execReqs, 2, "retry counted only once")
			}
		})
	}
}

func TestAgentExecutionMetrics(t *testing.T) {
	for _, result := range []string{"executed", "dependency_changed", "policy_refused", "failed"} {
		t.Run(result, func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			m := enableAgentTestMetrics(r)
			switch result {
			case "dependency_changed":
				require.NoError(t, r.db.WithContext(ctx).Model(&Alarm{}).Where("token = ?", "a1").Update("raised_time", time.Now()).Error)
			case "policy_refused":
				r.decider.second = func() (actiongate.ExecutionDecision, error) {
					return actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse}, nil
				}
			case "failed":
				require.NoError(t, r.db.Callback().Update().Before("gorm:update").Register("fail_effect", func(db *gorm.DB) {
					if _, ok := db.Statement.Model.(*Alarm); ok {
						db.AddError(errors.New("effect failed"))
					}
				}))
			}
			_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			if result == "failed" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			for _, label := range []string{"executed", "dependency_changed", "policy_refused", "failed"} {
				want := float64(0)
				if label == result {
					want = 1
				}
				require.Equal(t, want, testutil.ToFloat64(m.executions.WithLabelValues("ALARM_ACK", label)))
			}
			metric := &dto.Metric{}
			require.NoError(t, m.duration.(prometheus.Metric).Write(metric))
			require.Equal(t, uint64(1), metric.GetHistogram().GetSampleCount())
			require.GreaterOrEqual(t, metric.GetHistogram().GetSampleSum(), float64(0))
		})
	}
}

func TestAgentExecutionCountedOnceWhenRetriedAfterNotPending(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	m := enableAgentTestMetrics(r)
	bumped := false
	r.decider.beforeExec = func() {
		if bumped {
			return
		}
		// The first pre-decision follows pendingProposal and precedes the execution lock.
		require.Empty(t, r.decider.execReqs, "bump before the first pre-decision, not at the commit point")
		update := r.db.WithContext(ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).
			Update("revision", gorm.Expr("revision + 1"))
		require.NoError(t, update.Error)
		require.EqualValues(t, 1, update.RowsAffected)
		bumped = true
	}

	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.True(t, bumped)
	require.Len(t, r.decider.execReqs, 3, "NOT_PENDING retries the pre-decision before the successful transaction decision; plain execution needs two calls")
	for _, result := range []string{"executed", "dependency_changed", "policy_refused", "fence_closed", "refused", "failed"} {
		want := float64(0)
		if result == "executed" {
			want = 1
		}
		require.Equal(t, want, testutil.ToFloat64(m.executions.WithLabelValues("ALARM_ACK", result)), result)
	}
	metric := &dto.Metric{}
	require.NoError(t, m.duration.(prometheus.Metric).Write(metric))
	require.Equal(t, uint64(1), metric.GetHistogram().GetSampleCount())
	require.Equal(t, float64(1), testutil.ToFloat64(m.approvals.WithLabelValues("executed")))
}

func TestAgentExecutionCountsCommittedOutcomeWhenReadBackFails(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	m := enableAgentTestMetrics(r)
	var failReadBack atomic.Bool
	readBackErr := errors.New("post-commit read-back failed")
	queries := r.api.RDB.Database.Callback().Query()
	require.NoError(t, queries.Before("gorm:query").Register("fail_execution_read_back", func(db *gorm.DB) {
		if failReadBack.Load() {
			db.AddError(readBackErr)
		}
	}))
	t.Cleanup(func() {
		failReadBack.Store(false)
		r.api.afterDecisionForTest = nil
		require.NoError(t, queries.Remove("fail_execution_read_back"))
	})
	// The decision is followed only by COMMIT, so the next query is the read-back.
	r.api.afterDecisionForTest = func(*gorm.DB) error {
		failReadBack.Store(true)
		return nil
	}

	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, readBackErr)

	failReadBack.Store(false)
	stored, err := r.api.AgentProposalByToken(ctx, p.Token, nil)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), stored.State)
	require.Equal(t, float64(1), testutil.ToFloat64(m.executions.WithLabelValues("ALARM_ACK", "executed")))
	require.Equal(t, float64(0), testutil.ToFloat64(m.executions.WithLabelValues("ALARM_ACK", "failed")))
}

func TestAgentExpiryMetricsCountRowsOnce(t *testing.T) {
	r := newProposalRig(t)
	m := enableAgentTestMetrics(r)
	for _, id := range []string{"expire-one", "expire-two"} {
		p := r.propose(t, rigAgent, clearReq(id, "a1")).Proposal
		require.NoError(t, r.db.WithContext(tenantCtx(rigTenant)).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	}
	r.propose(t, rigAgent, clearReq("still-live", "a1"))
	for _, want := range []int64{2, 0} {
		n, err := r.api.ExpireAgentProposals(tenantCtx(rigTenant))
		require.NoError(t, err)
		require.Equal(t, want, n)
		require.Equal(t, float64(2), testutil.ToFloat64(m.expired))
	}
}

func TestAgentMetricNamesAndLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(reg)
	newAgentTestMetrics(ms)
	families, err := reg.Gather()
	require.NoError(t, err)
	expected := map[string][]string{
		"devicechain_devicemanagement_agent_proposals_total":         {"kind", "outcome"},
		"devicechain_devicemanagement_agent_approvals_total":         {"outcome"},
		"devicechain_devicemanagement_agent_executions_total":        {"kind", "result"},
		"devicechain_devicemanagement_agent_execution_seconds":       {},
		"devicechain_devicemanagement_agent_proposals_expired_total": {},
	}
	require.Len(t, families, len(expected))
	for _, family := range families {
		labels, ok := expected[family.GetName()]
		require.True(t, ok, family.GetName())
		for _, metric := range family.Metric {
			var names []string
			for _, label := range metric.Label {
				names = append(names, label.GetName())
			}
			require.ElementsMatch(t, labels, names)
		}
	}
}

func TestExecutionResult(t *testing.T) {
	executed := &AgentProposal{Record: actiongate.Record{State: string(actiongate.StateExecuted)}}
	refused := &AgentProposal{Record: actiongate.Record{State: string(actiongate.StateRefused), Reasons: []byte(`[]`)}}
	dependency := &AgentProposal{Record: actiongate.Record{State: string(actiongate.StateRefused), Reasons: []byte(`["OTHER","DEPENDENCY_CHANGED"]`)}}
	for _, tc := range []struct {
		name     string
		proposal *AgentProposal
		err      error
		want     string
	}{
		{"executed", executed, nil, "executed"},
		{"dependency changed", dependency, nil, "dependency_changed"},
		{"policy refused", refused, nil, "policy_refused"},
		{"fence closed", nil, actiongate.ErrFenceClosed, "fence_closed"},
		{"wrapped fence closed", nil, fmt.Errorf("wrapped: %w", actiongate.ErrFenceClosed), "fence_closed"},
		{"forbidden fence closed", nil, actiongate.Refusal(actiongate.CodeFenceClosed, "closed", true), "fence_closed"},
		{"policy unavailable", nil, fmt.Errorf("wrapped: %w", actiongate.ErrPolicyUnavailable), "failed"},
		{"fence unavailable", nil, fmt.Errorf("wrapped: %w", actiongate.ErrFenceUnavailable), "failed"},
		{"database", nil, errors.New("database unavailable"), "failed"},
		{"unrecognised error", nil, errors.New("lock timeout"), "failed"},
		{"forbidden", nil, auth.ErrForbidden, "refused"},
		{"wrapped forbidden", nil, fmt.Errorf("wrapped: %w", auth.ErrForbidden), "refused"},
		{"not approver", nil, errNotAnApprover, "refused"},
		{"self approval", nil, errSelfApproval, "refused"},
		{"concurrent", nil, errConcurrentApproval, "refused"},
		{"not pending", nil, actiongate.ErrNotPending, "refused"},
		{"expired", nil, errProposalExpired, "refused"},
		{"step up", nil, actiongate.Refusal(actiongate.CodeStepUpRequired, "step up", true), "refused"},
		{"second factor", nil, actiongate.Refusal(actiongate.CodeMfaRequired, "second factor", true), "refused"},
		{"wrapped refusal", nil, fmt.Errorf("wrapped: %w", errNotAnApprover), "refused"},
		{"error overrides state", executed, errors.New("read failed"), "failed"},
		{"missing result", nil, nil, "failed"},
		{"unexpected state", &AgentProposal{}, nil, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, executionResult(tc.proposal, tc.err)) })
	}
}

func TestAgentExecutionFenceMetrics(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("reenable_retry=%t", retry), func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			m := enableAgentTestMetrics(r)
			generation := r.decider.exec.ActuationGeneration + 1
			require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, generation)))
			want := "fence_closed"
			if retry {
				r.decider.exec.ActuationGeneration = generation + 1
				want = "executed"
			}
			got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			if retry {
				require.NoError(t, err)
				require.Equal(t, string(actiongate.StateExecuted), got.State)
				require.Len(t, r.decider.execReqs, 3, "pre-decision plus both transaction decisions")
			} else {
				require.True(t, actiongate.IsCode(err, actiongate.CodeFenceClosed))
				requireUnexecuted(t, r, p, ctx)
			}
			for _, label := range []string{"executed", "dependency_changed", "policy_refused", "fence_closed", "refused", "failed"} {
				value := float64(0)
				if label == want {
					value = 1
				}
				require.Equal(t, value, testutil.ToFloat64(m.executions.WithLabelValues("ALARM_ACK", label)), label)
			}
			require.Equal(t, float64(1), testutil.ToFloat64(m.approvals.WithLabelValues(want)))
			metric := &dto.Metric{}
			require.NoError(t, m.duration.(prometheus.Metric).Write(metric))
			require.Equal(t, uint64(1), metric.GetHistogram().GetSampleCount())
		})
	}
}
