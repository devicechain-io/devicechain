// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
)

// The tenant's AGENT POLICY on its own data plane (agentic actions, S2).
//
// 🔴 THE GOVERNANCE PLANE IS NEVER AGENT-AUTHORISED. Every write here requires agent:admin, which
// no act:* ceiling can carry, AND refuses any agent-origin token outright (auth.IsAgentOrigin: an
// OAuth client's token, a service account's, anything with a scope or an audience). A person edits
// the policy that bounds agents from their own console session; nothing an agent holds can.

// errAgentOriginRefused is the refusal of a policy write from an agent-origin token.
var errAgentOriginRefused = fmt.Errorf("%w: the agent policy can be changed only from a person's own session, never by an agent or a service account", auth.ErrForbidden)

// authorizePolicyWrite gates every policy write and returns the caller's claims.
func authorizePolicyWrite(ctx context.Context) (*auth.Claims, error) {
	if err := auth.Authorize(ctx, auth.AgentAdmin); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) {
		return nil, errAgentOriginRefused
	}
	return claims, nil
}

// AgentPolicyVersionResolver resolves AgentPolicyVersion.
type AgentPolicyVersionResolver struct{ M iam.AgentPolicyVersion }

func (r *AgentPolicyVersionResolver) Version() int32      { return int32(r.M.Version) }
func (r *AgentPolicyVersionResolver) Document() string    { return r.M.Document }
func (r *AgentPolicyVersionResolver) PublishedBy() string { return r.M.PublishedBy }
func (r *AgentPolicyVersionResolver) PublishedAt() string {
	return r.M.CreatedAt.UTC().Format(time.RFC3339)
}
func (r *AgentPolicyVersionResolver) Origin() string { return strings.ToUpper(r.M.Origin) }

// AgentPolicyResolver resolves AgentPolicy.
type AgentPolicyResolver struct {
	view     *identity.AgentPolicyView
	instance bool
}

func (r *AgentPolicyResolver) Published() *AgentPolicyVersionResolver {
	if r.view == nil || r.view.Published == nil {
		return nil
	}
	return &AgentPolicyVersionResolver{M: *r.view.Published}
}
func (r *AgentPolicyResolver) Draft() *string {
	if r.view == nil || r.view.Head == nil {
		return nil
	}
	return r.view.Head.Draft
}
func (r *AgentPolicyResolver) DraftUpdatedAt() *string {
	if r.view == nil || r.view.Head == nil {
		return nil
	}
	return rfc3339(r.view.Head.DraftUpdatedAt)
}
func (r *AgentPolicyResolver) DraftUpdatedBy() *string {
	if r.view == nil || r.view.Head == nil {
		return nil
	}
	return r.view.Head.DraftUpdatedBy
}
func (r *AgentPolicyResolver) OperatorSuspended() bool {
	return r.view != nil && r.view.Head != nil && r.view.Head.OperatorSuspended
}
func (r *AgentPolicyResolver) InstanceEnabled() bool { return r.instance }

// ActuationInEffect is all three switches, read the same way a decision reads them: an
// unreadable published document is NOT in effect.
func (r *AgentPolicyResolver) ActuationInEffect() bool {
	if !r.instance || r.OperatorSuspended() || r.view == nil || r.view.Published == nil {
		return false
	}
	doc, err := agentpolicy.ParseStored([]byte(r.view.Published.Document))
	return err == nil && doc.ActuationOn()
}

func (r *SchemaResolver) agentPolicyResolver(ctx context.Context, view *identity.AgentPolicyView) *AgentPolicyResolver {
	return &AgentPolicyResolver{view: view, instance: r.getIdentityManager(ctx).AgentActuationEnabled()}
}

// AgentPolicy reads this tenant's agent policy (requires agent:admin).
func (r *SchemaResolver) AgentPolicy(ctx context.Context) (*AgentPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.AgentAdmin); err != nil {
		return nil, err
	}
	claims, _ := auth.ClaimsFromContext(ctx)
	view, err := r.getIdentityManager(ctx).AgentPolicy(ctx, claims.Tenant)
	if err != nil {
		return nil, err
	}
	return r.agentPolicyResolver(ctx, view), nil
}

// AgentPolicyVersions lists the published versions, newest first (requires agent:admin).
func (r *SchemaResolver) AgentPolicyVersions(ctx context.Context, args struct{ Limit *int32 }) ([]*AgentPolicyVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.AgentAdmin); err != nil {
		return nil, err
	}
	limit := 20
	if args.Limit != nil {
		if *args.Limit < 1 || *args.Limit > 100 {
			return nil, fmt.Errorf("limit must be between 1 and 100")
		}
		limit = int(*args.Limit)
	}
	claims, _ := auth.ClaimsFromContext(ctx)
	vs, err := r.getIdentityManager(ctx).AgentPolicyVersions(ctx, claims.Tenant, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*AgentPolicyVersionResolver, 0, len(vs))
	for i := range vs {
		out = append(out, &AgentPolicyVersionResolver{M: vs[i]})
	}
	return out, nil
}

// SaveAgentPolicyDraft stores the working draft (requires agent:admin, never an agent).
func (r *SchemaResolver) SaveAgentPolicyDraft(ctx context.Context, args struct{ Document string }) (*AgentPolicyResolver, error) {
	claims, err := authorizePolicyWrite(ctx)
	if err != nil {
		return nil, err
	}
	view, err := r.getIdentityManager(ctx).SaveAgentPolicyDraft(ctx, claims, args.Document)
	if err != nil {
		return nil, err
	}
	return r.agentPolicyResolver(ctx, view), nil
}

// PublishAgentPolicy publishes the draft (requires agent:admin, never an agent; step-up to loosen).
func (r *SchemaResolver) PublishAgentPolicy(ctx context.Context) (*AgentPolicyResolver, error) {
	claims, err := authorizePolicyWrite(ctx)
	if err != nil {
		return nil, err
	}
	view, err := r.getIdentityManager(ctx).PublishAgentPolicyDraft(ctx, claims)
	if err != nil {
		return nil, err
	}
	return r.agentPolicyResolver(ctx, view), nil
}

// RollbackAgentPolicy republishes an earlier version (requires agent:admin, never an agent).
func (r *SchemaResolver) RollbackAgentPolicy(ctx context.Context, args struct{ Version int32 }) (*AgentPolicyResolver, error) {
	claims, err := authorizePolicyWrite(ctx)
	if err != nil {
		return nil, err
	}
	view, err := r.getIdentityManager(ctx).RollbackAgentPolicy(ctx, claims, int(args.Version))
	if err != nil {
		return nil, err
	}
	return r.agentPolicyResolver(ctx, view), nil
}

// SetAgentActuation is the tenant kill switch (requires agent:admin, never an agent).
func (r *SchemaResolver) SetAgentActuation(ctx context.Context, args struct{ Enabled bool }) (*AgentPolicyResolver, error) {
	claims, err := authorizePolicyWrite(ctx)
	if err != nil {
		return nil, err
	}
	view, err := r.getIdentityManager(ctx).SetTenantAgentActuation(ctx, claims, args.Enabled)
	if err != nil {
		return nil, err
	}
	return r.agentPolicyResolver(ctx, view), nil
}

// AgentActuationDecisionResolver resolves AgentActuationDecision.
type AgentActuationDecisionResolver struct{ d agentpolicy.Decision }

func (r *AgentActuationDecisionResolver) Verdict() string { return string(r.d.Verdict) }
func (r *AgentActuationDecisionResolver) RequiredApprovals() int32 {
	return int32(r.d.RequiredApprovals)
}
func (r *AgentActuationDecisionResolver) Reasons() []string {
	if r.d.Reasons == nil {
		return []string{}
	}
	return r.d.Reasons
}
func (r *AgentActuationDecisionResolver) PolicyVersion() int32 { return int32(r.d.PolicyVersion) }
func (r *AgentActuationDecisionResolver) GroupRestriction() *[]string {
	if r.d.GroupRestriction == nil {
		return nil
	}
	g := r.d.GroupRestriction
	return &g
}

// maxDecisionGroups bounds the group list of a decision request.
const maxDecisionGroups = 100

// AgentActuationDecision is the tenant's verdict for the caller's own client. Any signed-in token
// of the tenant may ask; the client comes from the token. It is read now, uncached.
func (r *SchemaResolver) AgentActuationDecision(ctx context.Context, args struct {
	Kind        string
	Impact      *string
	TargetCount int32
	GroupTokens *[]string
	Command     *string
}) (*AgentActuationDecisionResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Tenant == "" {
		return nil, auth.ErrUnauthenticated
	}
	req := agentpolicy.Request{Kind: agentpolicy.Kind(args.Kind), TargetCount: int(args.TargetCount)}
	if args.Impact != nil {
		req.Impact = agentpolicy.Impact(*args.Impact)
	}
	if args.GroupTokens != nil {
		if len(*args.GroupTokens) > maxDecisionGroups {
			return nil, fmt.Errorf("groupTokens may list at most %d groups", maxDecisionGroups)
		}
		req.GroupTokens = *args.GroupTokens
	}
	if args.Command != nil {
		req.Command = *args.Command
	}
	d, err := r.getIdentityManager(ctx).AgentActuationDecision(ctx, claims, req)
	if err != nil {
		return nil, err
	}
	return &AgentActuationDecisionResolver{d: d}, nil
}
