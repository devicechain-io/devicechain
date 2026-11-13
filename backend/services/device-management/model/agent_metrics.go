// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"errors"
	"slices"
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
		for _, result := range []string{"executed", "dependency_changed", "policy_refused", "fence_closed", "refused", "failed"} {
			m.executions.WithLabelValues(kind, result)
		}
	}
	for _, outcome := range []string{"recorded", "executed", "refused", "concurrent", "step_up_required", "forbidden", "fence_closed"} {
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
	case actiongate.IsCode(err, actiongate.CodeFenceClosed):
		outcome = "fence_closed"
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

// executionResult classifies the final result after any fence re-enable retry.
func executionResult(p *AgentProposal, err error) string {
	if err == nil && p != nil {
		switch actiongate.State(p.State) {
		case actiongate.StateExecuted:
			return "executed"
		case actiongate.StateRefused:
			var reasons []string
			if json.Unmarshal(p.Reasons, &reasons) == nil && slices.Contains(reasons, actiongate.ReasonDependencyChanged) {
				return "dependency_changed"
			}
			return "policy_refused"
		}
	}
	if actiongate.IsCode(err, actiongate.CodeFenceClosed) {
		return "fence_closed"
	}
	if actiongate.IsCode(err, actiongate.CodePolicyUnavailable) || actiongate.IsCode(err, actiongate.CodeFenceUnavailable) {
		return "failed"
	}
	// errNotAnApprover, errSelfApproval, errConcurrentApproval, errProposalExpired and
	// actiongate.ErrNotPending are all *actiongate.Error. A decision refused for the caller's
	// standing (auth.ErrForbidden) is a refusal too, not a failure to complete.
	var refusal *actiongate.Error
	if errors.As(err, &refusal) || errors.Is(err, auth.ErrForbidden) {
		return "refused"
	}
	return "failed"
}
