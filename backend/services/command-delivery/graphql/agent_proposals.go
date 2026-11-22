// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"

	"github.com/devicechain-io/dc-command-delivery/model"
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

// Targets, Approvals and Reasons answer an ERROR for a row whose stored JSON does not decode:
// an empty list would present a corrupt ledger row as one with no targets, no approvals or no
// reasons, which is a plausible value standing in for a broken one.
func (r *AgentProposalResolver) Targets() ([]string, error) {
	var out []string
	if err := json.Unmarshal(r.M.TargetSet, &out); err != nil {
		return nil, errors.New("stored proposal targets are unreadable")
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
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
func (r *AgentProposalResolver) Approvals() ([]*AgentProposalApprovalResolver, error) {
	approvals, err := actiongate.ParseApprovals(r.M.Approvals)
	if err != nil {
		return nil, errors.New("stored proposal approvals are unreadable")
	}
	out := make([]*AgentProposalApprovalResolver, 0, len(approvals))
	for _, a := range approvals {
		out = append(out, &AgentProposalApprovalResolver{a: a})
	}
	return out, nil
}
func (r *AgentProposalResolver) Reasons() ([]string, error) {
	var out []string
	if err := json.Unmarshal(r.M.Reasons, &out); err != nil {
		return nil, errors.New("stored proposal reasons are unreadable")
	}
	if out == nil {
		return []string{}, nil
	}
	return actiongate.PersistedReasons(out), nil
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

// AgentProposalOutcomeResolver resolves AgentProposalOutcome.
type AgentProposalOutcomeResolver struct{ M model.CommandProposalOutcome }

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
func (r *AgentProposalOutcomeResolver) Preview() *AgentCommandProposalPreviewResolver {
	if r.M.Preview == nil {
		return nil
	}
	return &AgentCommandProposalPreviewResolver{M: *r.M.Preview}
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

// AgentCommandProposalInput accepts only agent-authored intent.
type AgentCommandProposalInput struct {
	DeviceToken, Name                  string
	Payload                            *string
	RequestId                          string
	Rationale, ExpiresAt, DeliveryMode *string
	MaxAttempts                        *int32
	DryRun                             *bool
}

func (r *SchemaResolver) ProposeCommand(ctx context.Context, args struct{ Input AgentCommandProposalInput }) (*AgentProposalOutcomeResolver, error) {
	proposer, err := actiongate.AuthorizeProposer(ctx, auth.CommandPropose)
	if err != nil {
		return nil, proposalError(err)
	}
	in := args.Input
	out, err := r.GetApi(ctx).ProposeCommand(ctx, proposer, model.CommandProposalRequest{
		DeviceToken: in.DeviceToken, Name: in.Name, Payload: in.Payload, RequestId: in.RequestId,
		Rationale: in.Rationale, ExpiresAt: in.ExpiresAt, DeliveryMode: in.DeliveryMode,
		MaxAttempts: in.MaxAttempts, DryRun: in.DryRun != nil && *in.DryRun,
	})
	if err != nil {
		return nil, proposalError(err)
	}
	return &AgentProposalOutcomeResolver{M: *out}, nil
}

// proposalViewer decides who may read the ledger: a person holding command:read sees the tenant's
// proposals (nil viewer); an agent holding command:propose sees only its own client's proposals for
// the principal it acts for. Anyone else is refused, and so is every scoped caller.
func proposalViewer(ctx context.Context) (*actiongate.Proposer, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) {
		p, err := actiongate.AuthorizeProposer(ctx, auth.CommandPropose)
		if err != nil {
			return nil, proposalError(err)
		}
		return &p, nil
	}
	// A service token is not agent-origin by the predicate, and no service token may read the
	// ledger: no service token is allowed on any agent path.
	if claims.TokenType != auth.TokenTypeAccess {
		return nil, auth.ErrForbidden
	}
	if err := auth.Authorize(ctx, auth.CommandRead); err != nil {
		return nil, proposalError(err)
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
		return nil, proposalError(err)
	}
	found, err := r.GetApi(ctx).AgentProposals(ctx, model.AgentProposalSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		State:      args.Criteria.State, Kind: args.Criteria.Kind,
	}, viewer)
	if err != nil {
		return nil, proposalError(err)
	}
	return &AgentProposalSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// AgentProposal reads one proposal.
func (r *SchemaResolver) AgentProposal(ctx context.Context, args struct{ Token string }) (*AgentProposalResolver, error) {
	viewer, err := proposalViewer(ctx)
	if err != nil {
		return nil, proposalError(err)
	}
	p, err := r.GetApi(ctx).AgentProposalByToken(ctx, args.Token, viewer)
	if err != nil {
		return nil, proposalError(err)
	}
	return &AgentProposalResolver{M: *p}, nil
}

// ApproveAgentProposal records a person's approval.
func (r *SchemaResolver) ApproveAgentProposal(ctx context.Context, args struct {
	Token  string
	Digest string
}) (*AgentProposalResolver, error) {
	approver, err := actiongate.AuthorizeApprover(ctx, auth.CommandWrite)
	if err != nil {
		return nil, proposalError(err)
	}
	p, err := r.GetApi(ctx).ApproveAgentProposal(ctx, approver, args.Token, args.Digest)
	if err != nil {
		return nil, proposalError(err)
	}
	return &AgentProposalResolver{M: *p}, nil
}

// RejectAgentProposal records a person's rejection.
func (r *SchemaResolver) RejectAgentProposal(ctx context.Context, args struct {
	Token string
	Note  *string
}) (*AgentProposalResolver, error) {
	person, err := actiongate.AuthorizeApprover(ctx, auth.CommandWrite)
	if err != nil {
		return nil, proposalError(err)
	}
	p, err := r.GetApi(ctx).RejectAgentProposal(ctx, person, args.Token, args.Note)
	if err != nil {
		return nil, proposalError(err)
	}
	return &AgentProposalResolver{M: *p}, nil
}

// AgentCommandProposalPreviewResolver shows the immutable command snapshot and current hold.
type AgentCommandProposalPreviewResolver struct{ M model.CommandProposalPreview }

func (r *AgentCommandProposalPreviewResolver) DeviceToken() string { return r.M.DeviceToken }
func (r *AgentCommandProposalPreviewResolver) Incarnation() string {
	return strconv.FormatUint(r.M.Incarnation, 10)
}
func (r *AgentCommandProposalPreviewResolver) Command() string { return r.M.Command }
func (r *AgentCommandProposalPreviewResolver) Payload() *string {
	if !r.M.PayloadPresent {
		return nil
	}
	s := string(r.M.Payload)
	return &s
}
func (r *AgentCommandProposalPreviewResolver) PayloadPresent() bool { return r.M.PayloadPresent }
func (r *AgentCommandProposalPreviewResolver) Impact() string       { return r.M.Impact }
func (r *AgentCommandProposalPreviewResolver) Definition() string   { return r.M.Definition }
func (r *AgentCommandProposalPreviewResolver) Cutoff() string       { return rfc3339(r.M.Cutoff) }
func (r *AgentCommandProposalPreviewResolver) DispatchAfter() *string {
	if r.M.Hold == nil {
		return nil
	}
	return rfc3339Ptr(&r.M.Hold.DispatchAfter)
}
func (r *AgentCommandProposalPreviewResolver) WindowToken() *string {
	if r.M.Hold == nil {
		return nil
	}
	return &r.M.Hold.WindowToken
}

// Convenience fields are decoded from stored canonical JSON, never caller input.
type proposalCommandFields struct {
	Intent    struct{ DeviceToken, Command string }
	Effective struct{ Cutoff string }
}

func (r *AgentProposalResolver) commandFields() (proposalCommandFields, error) {
	var f proposalCommandFields
	err := json.Unmarshal(r.M.Payload, &f)
	return f, err
}
func (r *AgentProposalResolver) DeviceToken() (string, error) {
	f, err := r.commandFields()
	return f.Intent.DeviceToken, err
}
func (r *AgentProposalResolver) Command() (string, error) {
	f, err := r.commandFields()
	return f.Intent.Command, err
}
func (r *AgentProposalResolver) CommandCutoff() (string, error) {
	f, err := r.commandFields()
	return f.Effective.Cutoff, err
}
func (r *AgentProposalResolver) ResultRef() *string { return r.M.ResultRef }

// proposalError preserves the model's coded enqueue refusals on this error surface.
func proposalError(err error) error {
	var rejection *model.EnqueueRejected
	if errors.As(err, &rejection) {
		return actiongate.Refusal(string(rejection.Code), rejection.Reason, false)
	}
	return err
}
