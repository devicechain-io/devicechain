// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
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
		ms.NewCounterVec("agent_executions_total", "Final-approval transaction attempts by kind and result; rolled-back attempts count as failed.", []string{"kind", "result"}),
		ms.NewHistogramVec("agent_execution_seconds", "Time spent in final-approval transactions, including failed attempts.", nil, prometheus.DefBuckets).WithLabelValues(),
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
	for _, outcome := range []string{"recorded", "executed", "refused", "concurrent", "step_up_required", "forbidden"} {
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
			for _, label := range []string{"recorded", "executed", "refused", "concurrent", "step_up_required", "forbidden"} {
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
