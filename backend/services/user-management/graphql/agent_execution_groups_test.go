// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/identity"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"testing"
)

type executionGroupsRoot struct {
	decision *AgentExecutionDecisionResolver
}

func (r *executionGroupsRoot) Decision() *AgentExecutionDecisionResolver { return r.decision }

func TestAgentExecutionDecisionGroupRestrictionThroughResolver(t *testing.T) {
	for _, groups := range [][]string{nil, {"g1", "g2"}} {
		t.Run(map[bool]string{true: "restricted", false: "unrestricted"}[groups != nil], func(t *testing.T) {
			commands := map[string]any{"commands": []string{"reboot"}, "mode": "approve"}
			if groups != nil {
				commands["groups"] = groups
			}
			raw, err := json.Marshal(map[string]any{"actuation": "enabled", "clients": []string{"agent"}, "tools": map[string]any{"commands": commands}})
			require.NoError(t, err)
			policy, err := agentpolicy.Parse(raw)
			require.NoError(t, err)
			decision := agentpolicy.Decide(agentpolicy.State{InstanceEnabled: true, Policy: policy, Version: 3, EligibleApprovers: 2}, agentpolicy.Request{Kind: agentpolicy.KindCommandSend, Impact: agentpolicy.ImpactNormal, TargetCount: 1, Command: "reboot", Client: "agent", GroupTokens: groups})
			require.Equal(t, agentpolicy.VerdictApprove, decision.Verdict)
			r := &AgentExecutionDecisionResolver{d: identity.ExecutionDecision{Decision: decision}}
			schema, err := gql.ParseSchema(`type Query { decision: AgentExecutionDecision! } type AgentExecutionDecision { groupRestriction: [String!] }`, &executionGroupsRoot{decision: r})
			require.NoError(t, err)
			res := schema.Exec(context.Background(), `{ decision { groupRestriction } }`, "", nil)
			require.Empty(t, res.Errors)
			want := `{"decision":{"groupRestriction":null}}`
			if groups != nil {
				want = `{"decision":{"groupRestriction":["g1","g2"]}}`
			}
			require.JSONEq(t, want, string(res.Data))
		})
	}
}
