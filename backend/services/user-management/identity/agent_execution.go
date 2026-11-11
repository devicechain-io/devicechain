// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"gorm.io/gorm"
)

// The PROPOSAL-BOUND decisions (agentic actions, S3): what an owning area asks when an agent's
// proposal is stored, and again when a person approves it.
//
// 🔴 THE APPROVAL-TIME QUESTION IS NOT THE PROPOSAL-TIME QUESTION ASKED AGAIN. Under the approving
// person's console token the client id is empty, so re-running agentActuationDecision would check
// nobody's client. agentExecutionDecision is told, by the area, the ORIGINAL client and proposer
// recorded on the proposal and the approvers counted so far, and answers from this service's own
// records, read now with no cache:
//
//   - the three switches and the policy, with the original client's allowlisting (K1/K2/K3);
//   - the proposer's standing: their sign-in still open, still a member holding the write the
//     proposal was derived from, and the original client still one the tenant admits for them
//     (for an agent service account: the account still enabled, still an agent, still holding
//     the write). A proposer who has lost any of that has a dead proposal;
//   - EVERY counted approver's standing, not only the caller's: still an enabled, unscoped member
//     with an active second factor, in an approver role, holding the write. A first approval by
//     someone who has since lost the role no longer counts;
//   - how many approvals the CURRENT policy requires, and the step-up it requires.

// Reasons of the proposal-bound decision, beside agentpolicy's.
const (
	// ReasonProposerStanding is the coarse form of the PROPOSER_* reasons below, answered to a
	// caller who is neither the proposer nor an agent administrator.
	ReasonProposerStanding      = "PROPOSER_NOT_IN_STANDING"
	ReasonProposerSessionEnded  = "PROPOSER_SESSION_ENDED"
	ReasonProposerNotMember     = "PROPOSER_NOT_A_MEMBER"
	ReasonProposerScoped        = "PROPOSER_SCOPED"
	ReasonProposerLostAuthority = "PROPOSER_LOST_AUTHORITY"
	ReasonProposerClientRefused = "PROPOSER_CLIENT_REFUSED"
)

// MaxExecutionApprovers bounds the approvers one execution decision is asked about.
const MaxExecutionApprovers = 10

// kindActScope is the act:* scope a proposal of each kind is derived through.
var kindActScope = map[agentpolicy.Kind]string{
	agentpolicy.KindAlarmAck:      auth.ScopeActAlarms,
	agentpolicy.KindAlarmClear:    auth.ScopeActAlarms,
	agentpolicy.KindCommandSend:   auth.ScopeActCommands,
	agentpolicy.KindCommandBatch:  auth.ScopeActCommands,
	agentpolicy.KindRuleChangeset: auth.ScopeActRules,
	agentpolicy.KindRolloutCreate: auth.ScopeActUpdates,
	agentpolicy.KindAnnotation:    auth.ScopeActAnnotate,
}

// KindWriteAuthority is the raw write an action of kind needs: the authority its approvers must
// hold, and the only authority under which its execution decision may be asked.
func KindWriteAuthority(kind agentpolicy.Kind) (auth.Authority, bool) {
	w, ok := kindSource[kind]
	return w, ok
}

// ActuationDetail are the figures a gate needs beside the actuation verdict.
type ActuationDetail struct {
	Expiry         time.Duration
	ActionsPerHour int
	ClientAttested bool
}

// AgentActuationDetail answers the figures beside agentActuationDecision's verdict for the
// caller's own client, read under the same rules (an agent's token only). policyVersion is the
// version the verdict was made under: a policy published in between is an error, so the figures
// never come from a different policy than the verdict.
func (m *Manager) AgentActuationDetail(ctx context.Context, claims *auth.Claims, kind agentpolicy.Kind, policyVersion int) (ActuationDetail, error) {
	if !holdsProposalAuthority(claims) {
		return ActuationDetail{}, fmt.Errorf("%w: agentActuationDecision answers only an agent's token", auth.ErrForbidden)
	}
	suspended, off, doc, version, err := m.publishedPolicy(ctx, claims.Tenant)
	if err != nil {
		return ActuationDetail{}, err
	}
	if !m.agentActuation || suspended || off || !doc.ActuationOn() {
		return ActuationDetail{}, errors.New(errActRefused)
	}
	if version != policyVersion {
		return ActuationDetail{}, errors.New("the agent policy changed while it was being read; ask again")
	}
	attested, err := m.clientAttested(ctx, claims.ClientId)
	if err != nil {
		return ActuationDetail{}, err
	}
	return ActuationDetail{
		Expiry: doc.ExpiryFor(kind), ActionsPerHour: doc.ActionsPerHourFor(kind), ClientAttested: attested,
	}, nil
}

// ExecutionRequest is what the owning area recorded on a proposal, and who has approved it.
type ExecutionRequest struct {
	Kind              agentpolicy.Kind
	Impact            agentpolicy.Impact
	TargetCount       int
	GroupTokens       []string
	Command           string
	ProposerPrincipal string
	ProposerClient    string
	ProposerSid       string
	Approvers         []string
}

// ExecutionDecision is the proposal-bound verdict.
type ExecutionDecision struct {
	ActuationGeneration int
	agentpolicy.Decision
	// ValidApprovers are the requested approvers who are approvers now.
	ValidApprovers []string
	// StepUp is the second-factor freshness an approval needs under the current policy.
	StepUp auth.StepUp
	// AllowSelfConfirm is whether the proposer may be the single approver: only when the policy
	// allows it, the verdict needs one approval, and the impact is NORMAL.
	AllowSelfConfirm bool
}

// AgentExecutionDecision answers agentExecutionDecision. The caller must be a PERSON (never an
// agent-origin token) holding the raw write authority of the kind; the resolver enforces that
// before this is reached. It reads everything now, with no cache, and a store error is returned
// rather than answered: an error is a refusal.
func (m *Manager) AgentExecutionDecision(ctx context.Context, claims *auth.Claims, req ExecutionRequest) (ExecutionDecision, error) {
	if claims == nil || auth.IsAgentOrigin(claims) {
		return ExecutionDecision{}, fmt.Errorf("%w: agentExecutionDecision answers only a person's own session", auth.ErrForbidden)
	}
	w, ok := kindSource[req.Kind]
	if !ok {
		return ExecutionDecision{Decision: agentpolicy.Decision{Verdict: agentpolicy.VerdictRefuse, Reasons: []string{agentpolicy.ReasonUnknownKind}}}, nil
	}
	if !claims.HasAuthority(w) {
		return ExecutionDecision{}, auth.ErrForbidden
	}
	if req.ProposerPrincipal == "" || req.ProposerClient == "" {
		return ExecutionDecision{}, errors.New("the proposer's principal and client are required")
	}
	if len(req.Approvers) > MaxExecutionApprovers {
		return ExecutionDecision{}, fmt.Errorf("at most %d approvers may be named", MaxExecutionApprovers)
	}
	tenant := claims.Tenant
	suspended, off, doc, version, generation, err := m.publishedPolicyState(ctx, tenant)
	if err != nil {
		return ExecutionDecision{}, err
	}
	st := agentpolicy.State{InstanceEnabled: m.agentActuation, OperatorSuspended: suspended, TenantActuationOff: off, Policy: doc, Version: version}
	preq := agentpolicy.Request{
		Kind: req.Kind, Impact: req.Impact, TargetCount: req.TargetCount, GroupTokens: req.GroupTokens,
		Command: req.Command, Client: req.ProposerClient,
	}
	out := ExecutionDecision{ActuationGeneration: generation, ValidApprovers: []string{}}
	// The cheap checks first, as though approvers were plentiful.
	st.EligibleApprovers = 2
	if pre := agentpolicy.Decide(st, preq); pre.Verdict == agentpolicy.VerdictRefuse {
		out.Decision = pre
		return out, nil
	}
	if reason, err := m.proposerStanding(ctx, tenant, req); err != nil {
		return ExecutionDecision{}, err
	} else if reason != "" {
		// Why another principal lost standing is theirs (and an agent administrator's) to read;
		// an approver who only needs the yes/no is told the coarse reason.
		if claims.Username != req.ProposerPrincipal && !claims.HasAuthority(auth.AgentAdmin) {
			reason = ReasonProposerStanding
		}
		out.Decision = agentpolicy.Decision{Verdict: agentpolicy.VerdictRefuse, Reasons: []string{reason}, PolicyVersion: version}
		return out, nil
	}
	if preq.Attested, err = m.clientAttested(ctx, req.ProposerClient); err != nil {
		return ExecutionDecision{}, err
	}
	roles := doc.ApproverRoles()
	if st.EligibleApprovers, err = m.eligibleApprovers(ctx, tenant, roles, w); err != nil {
		return ExecutionDecision{}, err
	}
	out.Decision = agentpolicy.Decide(st, preq)
	if out.Verdict == agentpolicy.VerdictRefuse {
		return out, nil
	}
	seen := map[string]bool{}
	for _, p := range req.Approvers {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		ok, err := m.approverStanding(ctx, tenant, p, roles, w)
		if err != nil {
			return ExecutionDecision{}, err
		}
		if ok {
			out.ValidApprovers = append(out.ValidApprovers, p)
		}
	}
	impact := req.Impact
	if impact == "" {
		impact = agentpolicy.ImpactNormal
	}
	out.StepUp = auth.StepUp{MaxAge: doc.StepUpMaxAge(), AcceptRecoveryCodes: doc.AcceptRecoveryCodes()}
	out.AllowSelfConfirm = doc.SelfConfirmAllowed() && out.RequiredApprovals == 1 && impact == agentpolicy.ImpactNormal
	return out, nil
}

// proposalOf is the proposal authority a proposal of kind is derived as.
func proposalOf(kind agentpolicy.Kind) (string, bool) {
	scope, ok := kindActScope[kind]
	if !ok {
		return "", false
	}
	c, ok := auth.ActScopeCeiling(scope)
	if !ok || len(c) != 1 {
		return "", false
	}
	return c[0], true
}

// proposerStanding answers whether the proposer of req could still make the proposal now, with
// the reason it could not ("" when it could). A store error is returned.
func (m *Manager) proposerStanding(ctx context.Context, tenant string, req ExecutionRequest) (string, error) {
	scope := kindActScope[req.Kind]
	proposal, ok := proposalOf(req.Kind)
	if !ok {
		return agentpolicy.ReasonUnknownKind, nil
	}
	if strings.HasPrefix(req.ProposerPrincipal, auth.ServiceAccountSubjectPrefix) {
		return m.serviceAccountProposerStanding(ctx, tenant, req, scope, proposal)
	}
	sid := auth.SessionID(req.ProposerSid)
	if sid == "" {
		// A person's agent token always names its grant session; a proposal without one cannot be
		// tied to a sign-in that is still open.
		return ReasonProposerSessionEnded, nil
	}
	rec, err := m.loadSession(sid, req.ProposerPrincipal)
	if err != nil {
		if errors.Is(err, errSessionEnded) {
			return ReasonProposerSessionEnded, nil
		}
		return "", err
	}
	id, err := m.iam.IdentityByEmail(ctx, req.ProposerPrincipal)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ReasonProposerSessionEnded, nil
	case err != nil:
		return "", err
	case !id.Enabled:
		return ReasonProposerSessionEnded, nil
	}
	// 🔴 THE RECORD EXISTING IS NOT ENOUGH. A rotation of the identity's epoch (a password or MFA
	// reset, an enrolment, a disable) ends every credential minted before it, but deleting the
	// sign-in's record is best-effort, so the sign-in is live only while it was started under the
	// epoch the identity holds NOW. A record from a build before the field existed has none and
	// is refused: fail closed, and the person signs in again and the agent proposes again.
	if rec.Epoch == "" || id.SessionEpoch == "" || rec.Epoch != id.SessionEpoch {
		return ReasonProposerSessionEnded, nil
	}
	if isSuperuser(id) {
		// A superuser's grant is refused every act:* scope; an operator acts in a tenant through a
		// membership like anyone else.
		return ReasonProposerNotMember, nil
	}
	mem := findMembership(id.Memberships, tenant)
	if mem == nil || !mem.Enabled {
		return ReasonProposerNotMember, nil
	}
	_, authorities, anchors, signIn, err := m.resolveTenantGrant(ctx, sid, id, tenant, mem, false)
	if err != nil {
		if errors.Is(err, errTenantAccessDenied) || errors.Is(err, errSessionEnded) {
			return ReasonProposerNotMember, nil
		}
		return "", err
	}
	if anchors.IsScoped() {
		return ReasonProposerScoped, nil
	}
	if oerr := m.admitActScopes(ctx, tenant, req.ProposerClient, scope, false, false); oerr != nil {
		if oerr.Code == "server_error" {
			return "", errors.New(oerr.Desc)
		}
		return ReasonProposerClientRefused, nil
	}
	if oerr := m.checkActSessionCap(ctx, tenant, signIn); oerr != nil {
		if oerr.Code == "server_error" {
			return "", errors.New(oerr.Desc)
		}
		return ReasonProposerSessionEnded, nil
	}
	ceiling, _ := auth.ActScopeCeiling(scope)
	derived := auth.DeriveProposalAuthorities(effectiveAuthorities(authorities, false), ceiling, false)
	if !slices.Contains(derived, proposal) {
		return ReasonProposerLostAuthority, nil
	}
	return "", nil
}

// serviceAccountProposerStanding is proposerStanding for an agent service account.
func (m *Manager) serviceAccountProposerStanding(ctx context.Context, tenant string, req ExecutionRequest, scope, proposal string) (string, error) {
	acct, err := m.iam.ServiceAccountByClientId(ctx, req.ProposerClient)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ReasonProposerNotMember, nil
	case err != nil:
		return "", err
	}
	if acct.TenantId != tenant || !acct.Enabled || !acct.Agent ||
		auth.ServiceAccountSubject(acct.Token) != req.ProposerPrincipal {
		return ReasonProposerNotMember, nil
	}
	if acct.IsScoped() {
		return ReasonProposerScoped, nil
	}
	derived, oerr := m.serviceAccountAuthorities(ctx, acct, false, []string{}, []string{scope})
	if oerr != nil {
		if oerr.Code == "server_error" {
			return "", errors.New(oerr.Desc)
		}
		return ReasonProposerClientRefused, nil
	}
	if !slices.Contains(derived, proposal) {
		return ReasonProposerLostAuthority, nil
	}
	return "", nil
}

// approverStanding answers whether principal is an approver of tenant NOW: an enabled identity
// with an active second factor, an enabled and unscoped membership in one of roles, holding the
// raw write w (directly or through "*"). A superuser counts only through such a membership.
func (m *Manager) approverStanding(ctx context.Context, tenant, principal string, roles []string, w auth.Authority) (bool, error) {
	if len(roles) == 0 {
		return false, nil
	}
	id, err := m.iam.IdentityByEmail(ctx, principal)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return false, nil
	case err != nil:
		return false, err
	case !id.Enabled:
		return false, nil
	}
	mem := findMembership(id.Memberships, tenant)
	if mem == nil || !mem.Enabled || mem.IsScoped() {
		return false, nil
	}
	inRole := false
	for _, r := range mem.TenantRoles {
		if slices.Contains(roles, r.Token) {
			inRole = true
			break
		}
	}
	if !inRole {
		return false, nil
	}
	held := mem.TenantAuthorities()
	if !slices.Contains(held, string(w)) && !slices.Contains(held, string(auth.AuthorityAll)) {
		return false, nil
	}
	ok, err := m.iam.ApproverCandidates(ctx, []uint{id.ID})
	if err != nil {
		return false, err
	}
	return ok[id.ID], nil
}
