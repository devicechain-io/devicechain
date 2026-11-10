// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/prometheus/client_golang/prometheus"
)

// AgentMetrics belongs to the service's core registry, built once at initialization
// and shared across connection restarts. Labels contain only closed vocabularies.
// A nil receiver disables observation for APIs constructed without metrics.
type AgentMetrics struct {
	proposals  *prometheus.CounterVec
	approvals  *prometheus.CounterVec
	executions *prometheus.CounterVec
	duration   prometheus.Observer
	expired    prometheus.Counter
}

// NewAgentMetrics wraps collectors registered by the service's initialize path.
func NewAgentMetrics(proposals, approvals, executions *prometheus.CounterVec, duration prometheus.Observer, expired prometheus.Counter) *AgentMetrics {
	m := &AgentMetrics{
		proposals:  proposals,
		approvals:  approvals,
		executions: executions,
		duration:   duration,
		expired:    expired,
	}
	for _, kind := range []string{string(actiongate.KindAlarmAck), string(actiongate.KindAlarmClear), "unknown"} {
		for _, outcome := range []string{"created", "existing", "refused"} {
			m.proposals.WithLabelValues(kind, outcome)
		}
		for _, result := range []string{"executed", "dependency_changed", "policy_refused", "failed"} {
			m.executions.WithLabelValues(kind, result)
		}
	}
	for _, outcome := range []string{"recorded", "executed", "refused", "concurrent", "step_up_required", "forbidden"} {
		m.approvals.WithLabelValues(outcome)
	}
	return m
}

func agentMetricKind(kind actiongate.Kind) string {
	switch kind {
	case actiongate.KindAlarmAck, actiongate.KindAlarmClear:
		return string(kind)
	default:
		return "unknown"
	}
}

func (m *AgentMetrics) proposal(kind actiongate.Kind, out *ProposalOutcome, err error) {
	if m == nil {
		return
	}
	outcome := "refused"
	if err == nil && out != nil {
		switch out.Status {
		case ProposalStatusPending:
			outcome = "created"
		case ProposalStatusExisting:
			outcome = "existing"
		}
	}
	m.proposals.WithLabelValues(agentMetricKind(kind), outcome).Inc()
}

func (m *AgentMetrics) approval(p *AgentProposal, err error) {
	if m == nil {
		return
	}
	outcome := "refused"
	switch {
	case actiongate.IsCode(err, actiongate.CodeConcurrentApproval):
		outcome = "concurrent"
	case actiongate.IsCode(err, actiongate.CodeStepUpRequired), actiongate.IsCode(err, actiongate.CodeMfaRequired):
		outcome = "step_up_required"
	case errors.Is(err, auth.ErrForbidden):
		outcome = "forbidden"
	case err == nil && p != nil:
		switch actiongate.State(p.State) {
		case actiongate.StateExecuted:
			outcome = "executed"
		case actiongate.StateProposed:
			outcome = "recorded"
		}
	}
	m.approvals.WithLabelValues(outcome).Inc()
}

func (m *AgentMetrics) execution(kind actiongate.Kind, result string, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.executions.WithLabelValues(agentMetricKind(kind), result).Inc()
	m.duration.Observe(elapsed.Seconds())
}
