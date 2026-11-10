// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"

	"github.com/devicechain-io/dc-device-management/model"
)

// AGENT PROPOSALS (agentic actions, preview). Every operation here starts with the actiongate
// check for its caller: an agent proposes (actiongate.AuthorizeProposer) and a person decides
// (actiongate.AuthorizeApprover). Neither check can be reached by the other kind of caller, and
// neither accepts a service token or a scoped grant.

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

// AgentProposalResolver resolves AgentProposal.
type AgentProposalResolver struct{ M model.AgentProposal }

func (r *AgentProposalResolver) Token() string   { return r.M.Token }
func (r *AgentProposalResolver) Kind() string    { return r.M.Kind }
func (r *AgentProposalResolver) State() string   { return r.M.State }
func (r *AgentProposalResolver) Payload() string { return string(r.M.Payload) }
func (r *AgentProposalResolver) Targets() []string {
	var out []string
	if err := json.Unmarshal(r.M.TargetSet, &out); err != nil || out == nil {
		return []string{}
	}
	return out
}
func (r *AgentProposalResolver) Digest() string            { return r.M.Digest }
func (r *AgentProposalResolver) Impact() string            { return r.M.Impact }
func (r *AgentProposalResolver) TargetCount() int32        { return int32(r.M.TargetCount) }
func (r *AgentProposalResolver) ProposerPrincipal() string { return r.M.ProposerSub }
func (r *AgentProposalResolver) ProposerClient() string    { return r.M.ProposerClient }
func (r *AgentProposalResolver) ClientAttested() bool      { return r.M.ClientAttested }
func (r *AgentProposalResolver) Rationale() *string        { return r.M.Rationale }
func (r *AgentProposalResolver) PolicyVersion() int32      { return int32(r.M.PolicyVersion) }
func (r *AgentProposalResolver) RequiredApprovals() int32  { return int32(r.M.RequiredApprovals) }
func (r *AgentProposalResolver) Approvals() []*AgentProposalApprovalResolver {
	approvals, err := actiongate.ParseApprovals(r.M.Approvals)
	out := make([]*AgentProposalApprovalResolver, 0, len(approvals))
	if err != nil {
		return out
	}
	for _, a := range approvals {
		out = append(out, &AgentProposalApprovalResolver{a: a})
	}
	return out
}
func (r *AgentProposalResolver) Reasons() []string {
	var out []string
	if err := json.Unmarshal(r.M.Reasons, &out); err != nil || out == nil {
		return []string{}
	}
	return out
}
func (r *AgentProposalResolver) CreatedAt() string  { return rfc3339(r.M.CreatedAt) }
func (r *AgentProposalResolver) ExpiresAt() string  { return rfc3339(r.M.ExpiresAt) }
func (r *AgentProposalResolver) DecidedAt() *string { return rfc3339Ptr(r.M.DecidedAt) }
func (r *AgentProposalResolver) DecidedBy() *string { return r.M.DecidedBy }
func (r *AgentProposalResolver) Note() *string      { return r.M.Note }

// AgentProposalApprovalResolver resolves AgentProposalApproval.
type AgentProposalApprovalResolver struct{ a actiongate.Approval }

func (r *AgentProposalApprovalResolver) Principal() string  { return r.a.Sub }
func (r *AgentProposalApprovalResolver) ApprovedAt() string { return rfc3339(r.a.At) }

// AgentAlarmProposalPreviewResolver resolves AgentAlarmProposalPreview.
type AgentAlarmProposalPreviewResolver struct{ M model.AlarmProposalPreview }

func (r *AgentAlarmProposalPreviewResolver) AlarmToken() string       { return r.M.AlarmToken }
func (r *AgentAlarmProposalPreviewResolver) State() string            { return r.M.State }
func (r *AgentAlarmProposalPreviewResolver) Acknowledged() bool       { return r.M.Acknowledged }
func (r *AgentAlarmProposalPreviewResolver) HidesLiveCondition() bool { return r.M.HidesLiveCondition }

// AgentProposalOutcomeResolver resolves AgentProposalOutcome.
type AgentProposalOutcomeResolver struct{ M model.ProposalOutcome }

func (r *AgentProposalOutcomeResolver) Status() string { return r.M.Status }
func (r *AgentProposalOutcomeResolver) Proposal() *AgentProposalResolver {
	if r.M.Proposal == nil {
		return nil
	}
	return &AgentProposalResolver{M: *r.M.Proposal}
}
func (r *AgentProposalOutcomeResolver) Verdict() string { return string(r.M.Verdict) }
func (r *AgentProposalOutcomeResolver) RequiredApprovals() int32 {
	return int32(r.M.RequiredApprovals)
}
func (r *AgentProposalOutcomeResolver) Reasons() []string {
	if r.M.Reasons == nil {
		return []string{}
	}
	return r.M.Reasons
}
func (r *AgentProposalOutcomeResolver) Digest() string     { return r.M.Digest }
func (r *AgentProposalOutcomeResolver) ExpiresAt() *string { return rfc3339Ptr(r.M.ExpiresAt) }
func (r *AgentProposalOutcomeResolver) TargetCount() int32 { return int32(r.M.TargetCount) }
func (r *AgentProposalOutcomeResolver) Preview() []*AgentAlarmProposalPreviewResolver {
	out := make([]*AgentAlarmProposalPreviewResolver, 0, len(r.M.Preview))
	for _, p := range r.M.Preview {
		out = append(out, &AgentAlarmProposalPreviewResolver{M: p})
	}
	return out
}

// AgentProposalSearchResultsResolver resolves AgentProposalSearchResults.
type AgentProposalSearchResultsResolver struct {
	M model.AgentProposalSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AgentProposalSearchResultsResolver) Results() []*AgentProposalResolver {
	out := make([]*AgentProposalResolver, 0, len(r.M.Results))
	for _, p := range r.M.Results {
		out = append(out, &AgentProposalResolver{M: p})
	}
	return out
}

func (r *AgentProposalSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// AgentAlarmProposalInput is the GraphQL input of proposeAlarmAction.
type AgentAlarmProposalInput struct {
	Action      string
	AlarmTokens []string
	RequestId   string
	Rationale   *string
	DryRun      *bool
}

// ProposeAlarmAction is an agent's request to acknowledge or clear alarms. Only an agent's token
// holding alarm:propose reaches the model.
func (r *SchemaResolver) ProposeAlarmAction(ctx context.Context, args struct {
	Input AgentAlarmProposalInput
}) (*AgentProposalOutcomeResolver, error) {
	proposer, err := actiongate.AuthorizeProposer(ctx, auth.AlarmPropose)
	if err != nil {
		return nil, err
	}
	in := args.Input
	out, err := r.GetApi(ctx).ProposeAlarmAction(ctx, proposer, model.AlarmProposalRequest{
		Action: model.AlarmProposalAction(in.Action), AlarmTokens: in.AlarmTokens, RequestId: in.RequestId,
		Rationale: in.Rationale, DryRun: in.DryRun != nil && *in.DryRun,
	})
	if err != nil {
		return nil, err
	}
	return &AgentProposalOutcomeResolver{M: *out}, nil
}

// proposalViewer decides who may read the ledger: a person holding alarm:read sees the tenant's
// proposals (nil viewer); an agent holding alarm:propose sees only its own client's proposals for
// the principal it acts for. Anyone else is refused, and so is every scoped caller.
func proposalViewer(ctx context.Context) (*actiongate.Proposer, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) {
		p, err := actiongate.AuthorizeProposer(ctx, auth.AlarmPropose)
		if err != nil {
			return nil, err
		}
		return &p, nil
	}
	// A service token is not agent-origin by the predicate, and no service token may read the
	// ledger: no service token is allowed on any agent path.
	if claims.TokenType != auth.TokenTypeAccess {
		return nil, auth.ErrForbidden
	}
	if err := auth.Authorize(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	return nil, nil
}

// AgentProposals pages the ledger.
func (r *SchemaResolver) AgentProposals(ctx context.Context, args struct {
	Criteria struct {
		PageNumber int32
		PageSize   int32
		State      *string
		Kind       *string
	}
}) (*AgentProposalSearchResultsResolver, error) {
	viewer, err := proposalViewer(ctx)
	if err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).AgentProposals(ctx, model.AgentProposalSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		State:      args.Criteria.State, Kind: args.Criteria.Kind,
	}, viewer)
	if err != nil {
		return nil, err
	}
	return &AgentProposalSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// AgentProposal reads one proposal.
func (r *SchemaResolver) AgentProposal(ctx context.Context, args struct{ Token string }) (*AgentProposalResolver, error) {
	viewer, err := proposalViewer(ctx)
	if err != nil {
		return nil, err
	}
	p, err := r.GetApi(ctx).AgentProposalByToken(ctx, args.Token, viewer)
	if err != nil {
		return nil, err
	}
	return &AgentProposalResolver{M: *p}, nil
}

// authorizeDecision is the structural check on a person deciding the proposal token: the raw
// write its kind needs, from a person's own access token.
func (r *SchemaResolver) authorizeDecision(ctx context.Context, token string) (actiongate.Approver, error) {
	// The write a kind needs is the same for every kind this release has; it is asserted before
	// the proposal is read so that a caller without it learns nothing about the proposal.
	approver, err := actiongate.AuthorizeApprover(ctx, auth.AlarmWrite)
	if err != nil {
		return actiongate.Approver{}, err
	}
	p, err := r.GetApi(ctx).AgentProposalByToken(ctx, token, nil)
	if err != nil {
		return actiongate.Approver{}, err
	}
	w, ok := model.ProposalWriteAuthority(p.Kind)
	if !ok {
		return actiongate.Approver{}, auth.ErrForbidden
	}
	if err := auth.Authorize(ctx, w); err != nil {
		return actiongate.Approver{}, err
	}
	return approver, nil
}

// ApproveAgentProposal records a person's approval.
func (r *SchemaResolver) ApproveAgentProposal(ctx context.Context, args struct {
	Token  string
	Digest string
}) (*AgentProposalResolver, error) {
	approver, err := r.authorizeDecision(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	p, err := r.GetApi(ctx).ApproveAgentProposal(ctx, approver, args.Token, args.Digest)
	if err != nil {
		return nil, err
	}
	return &AgentProposalResolver{M: *p}, nil
}

// RejectAgentProposal records a person's rejection.
func (r *SchemaResolver) RejectAgentProposal(ctx context.Context, args struct {
	Token string
	Note  *string
}) (*AgentProposalResolver, error) {
	person, err := r.authorizeDecision(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	p, err := r.GetApi(ctx).RejectAgentProposal(ctx, person, args.Token, args.Note)
	if err != nil {
		return nil, err
	}
	return &AgentProposalResolver{M: *p}, nil
}
