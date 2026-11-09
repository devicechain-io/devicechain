// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Proposer is who asked: the canonical principal key (claims.Username, the same key the audit
// journal's Actor uses: an email for a person, the subject for a service account), the OAuth
// client the token was minted for, and the grant session it descends from.
type Proposer struct {
	Tenant    string
	Principal string
	Client    string
	// Sid is the grant session; empty when the token carried none (an agent service account's
	// token never does), which is "unknown", never a match.
	Sid string
}

// AuthorizeProposer is the FIRST step of every proposal operation: the caller must be an AGENT
// holding exactly the proposal authority the operation accepts.
//
// 🔴 IT ACCEPTS NOTHING ELSE, AND THE ORDER OF THE REFUSALS IS PART OF THE CONTRACT:
//
//   - no claims: unauthenticated;
//   - a SERVICE token: refused outright (no service token on any agent path). A pod holding the
//     shared secret can mint any authority, so a service token is never evidence of an agent,
//     and a proposal made under one would have no person behind it;
//   - anything but an ACCESS token: refused;
//   - a token that is not agent-origin (a person's console token): AGENT_ONLY. A person acts
//     through the existing mutations, under their own authority; the proposal path exists only
//     for a principal that cannot;
//   - a SCOPED grant (customer or group anchors): refused in this release. A proposal's targets
//     would have to be re-checked against the proposer's anchors at execution, and the ledger
//     does not carry that yet, so a scoped agent cannot propose (fail closed);
//   - a token without the proposal authority: forbidden. "*" never satisfies a proposal
//     authority (auth.HasAuthority), so a superuser's token cannot stand in for one.
func AuthorizeProposer(ctx context.Context, proposal auth.Authority) (Proposer, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return Proposer{}, auth.ErrUnauthenticated
	}
	if claims.TokenType == auth.TokenTypeService {
		return Proposer{}, gateErr(CodeServiceTokenRefused, auth.ErrForbidden,
			"a service token can never propose an agent action")
	}
	if claims.TokenType != auth.TokenTypeAccess {
		return Proposer{}, gateErr(CodeAgentOnly, auth.ErrForbidden, "only an agent's access token can propose an action")
	}
	if !auth.IsAgentOrigin(claims) {
		return Proposer{}, gateErr(CodeAgentOnly, auth.ErrForbidden,
			"proposals are made by AI agents; a person acts through the ordinary operations")
	}
	if len(claims.ScopeAnchors) > 0 {
		return Proposer{}, gateErr(CodeScopedNotSupported, auth.ErrForbidden,
			"an agent acting for a member scoped to part of the tenant cannot propose actions in this release")
	}
	if !auth.IsProposalAuthority(string(proposal)) || !slices.Contains(claims.Authorities, string(proposal)) {
		return Proposer{}, auth.ErrForbidden
	}
	if err := auth.Authorize(ctx, proposal); err != nil {
		return Proposer{}, err
	}
	if claims.Tenant == "" || claims.Username == "" || claims.ClientId == "" {
		// An agent token always names its tenant, its principal and its client (the OAuth mint
		// paths stamp them). One that does not cannot be attributed, so it does not propose.
		return Proposer{}, gateErr(CodeAgentOnly, auth.ErrForbidden, "the token does not name its agent client and principal")
	}
	return Proposer{Tenant: claims.Tenant, Principal: claims.Username, Client: claims.ClientId, Sid: string(claims.SessionID)}, nil
}

// Approver is who approved, as recorded on the proposal.
type Approver struct {
	Principal string
	Amr       []string
	// MfaTime is when the approver's sign-in last verified a second factor; nil when unknown.
	MfaTime *time.Time
	Claims  *auth.Claims
}

// AuthorizeApprover is the structural check on a person approving or rejecting a proposal of an
// action that needs write. It runs before anything is read, and refuses:
//
//   - no claims: unauthenticated;
//   - anything but an ACCESS token (a service, refresh or identity token);
//   - an AGENT-ORIGIN token (an agent cannot approve, whatever it holds): APPROVER_IS_AN_AGENT;
//   - a scoped grant (see AuthorizeProposer): refused in this release;
//   - a token without the raw write authority (directly or through "*").
//
// The approver-role membership, the second factor's freshness under the policy and the standing of
// every counted approver are decided after the policy is read (agentExecutionDecision and
// CheckStepUp), never from the token alone.
func AuthorizeApprover(ctx context.Context, write auth.Authority) (Approver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return Approver{}, auth.ErrUnauthenticated
	}
	if claims.TokenType == auth.TokenTypeService {
		return Approver{}, gateErr(CodeServiceTokenRefused, auth.ErrForbidden, "a service token can never approve an agent action")
	}
	if claims.TokenType != auth.TokenTypeAccess {
		return Approver{}, gateErr(CodeApproverIsAgent, auth.ErrForbidden, "only a person's own session can approve an agent action")
	}
	if auth.IsAgentOrigin(claims) {
		return Approver{}, gateErr(CodeApproverIsAgent, auth.ErrForbidden, "an agent can never approve or reject an agent action")
	}
	if len(claims.ScopeAnchors) > 0 {
		return Approver{}, gateErr(CodeScopedNotSupported, auth.ErrForbidden,
			"a member scoped to part of the tenant cannot approve agent actions in this release")
	}
	if err := auth.Authorize(ctx, write); err != nil {
		return Approver{}, err
	}
	if claims.Username == "" {
		return Approver{}, auth.ErrForbidden
	}
	a := Approver{Principal: claims.Username, Amr: append([]string{}, claims.Amr...), Claims: claims}
	if claims.MfaTime != nil {
		t := claims.MfaTime.Time.UTC()
		a.MfaTime = &t
	}
	return a, nil
}

// CheckStepUp requires the approver's second factor to be verified recently enough under su (the
// policy's stepUpMaxAge and acceptRecoveryCodes). It distinguishes a sign-in that never presented
// a second factor (SECOND_FACTOR_REQUIRED) from one that did too long ago (STEP_UP_REQUIRED),
// so the console knows whether to prompt for a code or to send the person to set one up.
//
// Freshness is mfa_time and nothing else (auth.StepUpFresh): a token's issue time never counts.
func CheckStepUp(a Approver, su auth.StepUp, now time.Time) error {
	if a.Claims == nil || !a.Claims.Mfa() {
		return gateErr(CodeMfaRequired, auth.ErrForbidden, "approving an agent action needs a sign-in with a second factor")
	}
	if !auth.StepUpFresh(a.Claims, su, now) {
		return gateErr(CodeStepUpRequired, auth.ErrForbidden,
			"approving an agent action needs a second factor verified within the last %s", su.MaxAge)
	}
	return nil
}
