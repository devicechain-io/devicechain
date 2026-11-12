// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/identity"
)

// AgentExecutionDecisionResolver resolves AgentExecutionDecision.
type AgentExecutionDecisionResolver struct{ d identity.ExecutionDecision }

func (r *AgentExecutionDecisionResolver) Verdict() string { return string(r.d.Verdict) }
func (r *AgentExecutionDecisionResolver) RequiredApprovals() int32 {
	return int32(r.d.RequiredApprovals)
}
func (r *AgentExecutionDecisionResolver) ValidApprovers() []string {
	if r.d.ValidApprovers == nil {
		return []string{}
	}
	return r.d.ValidApprovers
}
func (r *AgentExecutionDecisionResolver) Reasons() []string {
	if r.d.Reasons == nil {
		return []string{}
	}
	return r.d.Reasons
}
func (r *AgentExecutionDecisionResolver) ActuationGeneration() int32 {
	return int32(r.d.ActuationGeneration)
}
func (r *AgentExecutionDecisionResolver) PolicyVersion() int32 { return int32(r.d.PolicyVersion) }
func (r *AgentExecutionDecisionResolver) StepUpMaxAgeSeconds() int32 {
	return int32(r.d.StepUp.MaxAge / time.Second)
}
func (r *AgentExecutionDecisionResolver) AcceptRecoveryCodes() bool {
	return r.d.StepUp.AcceptRecoveryCodes
}
func (r *AgentExecutionDecisionResolver) AllowSelfConfirm() bool { return r.d.AllowSelfConfirm }

// AgentExecutionDecisionInput is the GraphQL input.
type AgentExecutionDecisionInput struct {
	ProposalToken     string
	Kind              string
	Impact            *string
	TargetCount       int32
	GroupTokens       *[]string
	Command           *string
	ProposerPrincipal string
	ProposerClient    string
	ProposerSid       *string
	Approvers         []string
}

// AgentExecutionDecision is the proposal-bound re-check an owning area asks when a person
// approves an agent's proposal (agentic actions, S3).
//
// 🔴 ONLY A PERSON'S OWN SESSION HOLDING THE KIND'S RAW WRITE MAY ASK. An agent-origin token is
// refused before anything is read (an agent never approves, and the query is not an oracle over the
// tenant's members for one), and so is a token without the write: the question is asked on behalf
// of someone about to exercise it. auth.Authorize also refuses a scoped caller.
func (r *SchemaResolver) AgentExecutionDecision(ctx context.Context, args struct {
	Input AgentExecutionDecisionInput
}) (*AgentExecutionDecisionResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Tenant == "" {
		return nil, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) || claims.TokenType != auth.TokenTypeAccess {
		return nil, fmt.Errorf("%w: agentExecutionDecision answers only a person's own session", auth.ErrForbidden)
	}
	in := args.Input
	kind := agentpolicy.Kind(in.Kind)
	w, known := identity.KindWriteAuthority(kind)
	if !known {
		return nil, fmt.Errorf("unknown kind %q", in.Kind)
	}
	if err := auth.Authorize(ctx, w); err != nil {
		return nil, err
	}
	if len(in.Approvers) > identity.MaxExecutionApprovers {
		return nil, fmt.Errorf("approvers may name at most %d principals", identity.MaxExecutionApprovers)
	}
	req := identity.ExecutionRequest{
		Kind: kind, TargetCount: int(in.TargetCount), ProposerPrincipal: in.ProposerPrincipal,
		ProposerClient: in.ProposerClient, Approvers: in.Approvers,
	}
	if in.Impact != nil {
		req.Impact = agentpolicy.Impact(*in.Impact)
	}
	if in.GroupTokens != nil {
		if len(*in.GroupTokens) > maxDecisionGroups {
			return nil, fmt.Errorf("groupTokens may list at most %d groups", maxDecisionGroups)
		}
		req.GroupTokens = *in.GroupTokens
	}
	if in.Command != nil {
		req.Command = *in.Command
	}
	if in.ProposerSid != nil {
		req.ProposerSid = *in.ProposerSid
	}
	d, err := r.getIdentityManager(ctx).AgentExecutionDecision(ctx, claims, req)
	if err != nil {
		return nil, err
	}
	return &AgentExecutionDecisionResolver{d: d}, nil
}
