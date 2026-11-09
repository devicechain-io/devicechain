// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// The AGENT POLICY and its uses by the authorization server (agentic actions, S2).
//
// 🔴 THREE SWITCHES, ALL OFF BY DEFAULT, AND ALL THREE MUST BE ON FOR AN AGENT TO RECEIVE AN act:*
// SCOPE: the instance flag (agentActuation.enabled in this service's configuration), the tenant's
// published policy (actuation: enabled, and the client listed), and the operator's per-tenant
// override (not suspended). Each is re-read at /authorize, at code redemption and at every
// refresh, so turning any one off stops new and refreshed grants; the decision query reads them
// on every call, uncached, so turning one off refuses every later decision at once.
//
// Nothing here executes an action. An act:* token carries proposal authorities, which no existing
// mutation accepts, and the proposal mutations that will accept them arrive with the first action.

// AgentPolicyError is a refusal of an agent-policy write, with a stable code the console reads.
type AgentPolicyError struct {
	code, msg string
}

func (e *AgentPolicyError) Error() string { return e.msg }

// Extensions carries the code to the GraphQL error.
func (e *AgentPolicyError) Extensions() map[string]any { return map[string]any{"code": e.code} }

// Codes of AgentPolicyError.
const (
	// AgentPolicyStepUpRequired: the change loosens the policy, and the caller's second factor is
	// not recent enough. Sign in again with a code (or step up) and retry.
	AgentPolicyStepUpRequired = "STEP_UP_REQUIRED"
	// AgentPolicyNoDraft: there is no draft to publish.
	AgentPolicyNoDraft = "NO_DRAFT"
	// AgentPolicyNoPolicy: actuation cannot be enabled before a policy has been published.
	AgentPolicyNoPolicy = "NO_PUBLISHED_POLICY"
	// AgentPolicyNoVersion: the version to roll back to does not exist.
	AgentPolicyNoVersion = "NO_SUCH_VERSION"
	// AgentPolicyInvalid: the document does not parse or breaks a rule.
	AgentPolicyInvalid = "INVALID_POLICY"
	// AgentPolicyUnreadable: the policy in force cannot be read by this release, usually because
	// a newer release wrote it during an upgrade. Only the kill switch can change it here.
	AgentPolicyUnreadable = "UNREADABLE_POLICY"
)

func agentPolicyErr(code, format string, a ...any) error {
	return &AgentPolicyError{code: code, msg: fmt.Sprintf(format, a...)}
}

// SetAgentActuation installs the instance flag. Off (the default) refuses every act:* scope at
// every mint, keeps the act:* scopes out of the advertised metadata, and makes every decision a
// refusal.
func (m *Manager) SetAgentActuation(enabled bool) { m.agentActuation = enabled }

// AgentActuationEnabled reports the instance flag.
func (m *Manager) AgentActuationEnabled() bool { return m.agentActuation }

// SupportedScopes is the scope list this AS advertises and accepts.
func (m *Manager) SupportedScopes() []string { return auth.SupportedScopesFor(m.agentActuation) }

// AgentPolicyView is a tenant's agent policy as the console reads it.
type AgentPolicyView struct {
	Head      *iam.AgentPolicy
	Published *iam.AgentPolicyVersion
}

// AgentPolicy returns the tenant's policy head and the version in force, either nil when absent.
func (m *Manager) AgentPolicy(ctx context.Context, tenant string) (*AgentPolicyView, error) {
	head, v, err := m.iam.PublishedAgentPolicy(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return &AgentPolicyView{Head: head, Published: v}, nil
}

// AgentPolicyVersions lists the tenant's published versions, newest first.
func (m *Manager) AgentPolicyVersions(ctx context.Context, tenant string, limit int) ([]iam.AgentPolicyVersion, error) {
	return m.iam.AgentPolicyVersions(ctx, tenant, limit)
}

// parsePolicyInput parses a submitted document and returns its canonical form.
func parsePolicyInput(raw string) (*agentpolicy.Document, string, error) {
	doc, err := agentpolicy.Parse([]byte(raw))
	if err != nil {
		return nil, "", agentPolicyErr(AgentPolicyInvalid, "%v", err)
	}
	b, err := doc.Canonical()
	if err != nil {
		return nil, "", err
	}
	return doc, string(b), nil
}

// SaveAgentPolicyDraft validates and stores the tenant's working draft. Saving a draft changes
// nothing in force, so it needs no step-up.
func (m *Manager) SaveAgentPolicyDraft(ctx context.Context, claims *auth.Claims, raw string) (*AgentPolicyView, error) {
	_, canonical, err := parsePolicyInput(raw)
	if err != nil {
		return nil, err
	}
	if _, err := m.iam.SaveAgentPolicyDraft(ctx, claims.Tenant, canonical, principalOf(claims)); err != nil {
		return nil, err
	}
	return m.AgentPolicy(ctx, claims.Tenant)
}

// PublishAgentPolicyDraft makes the draft the policy in force.
func (m *Manager) PublishAgentPolicyDraft(ctx context.Context, claims *auth.Claims) (*AgentPolicyView, error) {
	return m.publishAgentPolicy(ctx, claims, func(head *iam.AgentPolicy, _ *iam.AgentPolicyVersion) (string, string, bool, error) {
		if head.Draft == nil {
			return "", "", false, agentPolicyErr(AgentPolicyNoDraft, "there is no draft agent policy to publish")
		}
		return *head.Draft, iam.AgentPolicyOriginPublish, true, nil
	})
}

// RollbackAgentPolicy republishes an earlier version's document as a NEW version.
//
// The target is read before the publish transaction opens: a version is immutable once written, so
// reading it early cannot race, and the classification against the policy in force still happens
// under the head lock.
func (m *Manager) RollbackAgentPolicy(ctx context.Context, claims *auth.Claims, version int) (*AgentPolicyView, error) {
	v, err := m.iam.AgentPolicyVersionAt(ctx, claims.Tenant, version)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, agentPolicyErr(AgentPolicyNoVersion, "agent policy version %d does not exist", version)
	}
	if err != nil {
		return nil, err
	}
	return m.publishAgentPolicy(ctx, claims, func(_ *iam.AgentPolicy, _ *iam.AgentPolicyVersion) (string, string, bool, error) {
		return v.Document, iam.AgentPolicyOriginRollback, false, nil
	})
}

// SetTenantAgentActuation is the tenant KILL SWITCH (K1), and its re-enable: it publishes the
// policy in force as a new version with only its actuation changed. Turning actuation off is a
// tightening and always succeeds without a step-up, even with no policy (nothing is published
// then: there is nothing to turn off). Turning it back on is a loosening and needs one.
//
// A new VERSION rather than a flag beside the policy, because the version is what a decision
// reports: a decision made under the killed version is recognisably older than the kill.
func (m *Manager) SetTenantAgentActuation(ctx context.Context, claims *auth.Claims, enabled bool) (*AgentPolicyView, error) {
	if !enabled {
		head, err := m.iam.AgentPolicyHead(ctx, claims.Tenant)
		if err != nil {
			return nil, err
		}
		if head == nil || head.PublishedVersion == 0 {
			return m.AgentPolicy(ctx, claims.Tenant)
		}
	}
	view, err := m.publishAgentPolicy(ctx, claims, func(_ *iam.AgentPolicy, current *iam.AgentPolicyVersion) (string, string, bool, error) {
		if current == nil {
			return "", "", false, agentPolicyErr(AgentPolicyNoPolicy, "publish an agent policy before enabling agent actuation")
		}
		doc, err := agentpolicy.ParseStored([]byte(current.Document))
		if err != nil {
			if enabled {
				return "", "", false, fmt.Errorf("the agent policy in force cannot be read: %w", err)
			}
			// 🔴 THE KILL SWITCH NEVER DEPENDS ON READING THE POLICY IT STOPS, AND NEVER DROPS
			// THE SERVICE-ACCOUNT RULE. A document this pod cannot read (written by a newer
			// release mid-upgrade, or damaged) is replaced by a disabled policy that carries the
			// tenant's serviceAccounts.agentFlagRequiredFor forward VERBATIM (the key is frozen
			// across versions): an authority this release does not know requires nothing here,
			// but the newer release that wrote it does know it, and dropping it would lift the
			// rule there. If even that key cannot be read, no pod can read the document at all, so
			// actuation is already off everywhere; nothing is published rather than a document
			// that would silently lift the rule.
			req, ok := agentpolicy.ExtractAgentFlagRequirement([]byte(current.Document))
			if !ok {
				log.Warn().Err(err).Str("tenant", claims.Tenant).Msg("The agent policy in force cannot be read at all; agent actuation is already off, and the kill switch publishes nothing.")
				return "", "", false, errNothingToKill
			}
			doc = &agentpolicy.Document{}
			var carried []string
			for _, a := range req {
				if a != "" && !slices.Contains(carried, a) {
					carried = append(carried, a)
				}
			}
			if len(carried) > 0 {
				doc.ServiceAccounts = &agentpolicy.ServiceAccts{AgentFlagRequiredFor: carried}
			}
			log.Warn().Err(err).Str("tenant", claims.Tenant).Msg("The agent policy in force cannot be read; the kill switch publishes a disabled policy keeping its service-account rule.")
		}
		if enabled {
			doc.Actuation = agentpolicy.ActuationEnabled
		} else {
			doc.Actuation = agentpolicy.ActuationDisabled
		}
		b, err := doc.Canonical()
		if err != nil {
			return "", "", false, err
		}
		return string(b), iam.AgentPolicyOriginActuation, false, nil
	})
	if errors.Is(err, errNothingToKill) {
		return m.AgentPolicy(ctx, claims.Tenant)
	}
	return view, err
}

// errNothingToKill aborts a kill-switch publish that has nothing safe to publish (see above).
var errNothingToKill = errors.New("agent actuation is already off: the policy in force cannot be read")

// publishAgentPolicy publishes the document pick chooses, classifying it against the policy in
// force UNDER the head lock, and refusing a loosening without a fresh second factor.
//
// 🔴 THE STEP-UP IS DECIDED BY THE STRICTER OF THE TWO POLICIES. A loosening publish could
// otherwise relax its own gate: a document that raises stepUpMaxAge to an hour, or accepts
// recovery codes, must not be published under those relaxed terms.
func (m *Manager) publishAgentPolicy(ctx context.Context, claims *auth.Claims, pick func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error)) (*AgentPolicyView, error) {
	now := m.clock()
	_, err := m.iam.PublishAgentPolicy(ctx, claims.Tenant, principalOf(claims), func(head *iam.AgentPolicy, current *iam.AgentPolicyVersion) (string, string, bool, error) {
		raw, origin, clearDraft, err := pick(head, current)
		if err != nil {
			return "", "", false, err
		}
		// A draft is what a person submitted and is held to every rule. A rollback or an actuation
		// change republishes STORED content, whose carried-forward rule entries may name
		// authorities this release does not know (ParseStored).
		var next *agentpolicy.Document
		if origin == iam.AgentPolicyOriginPublish {
			next, err = agentpolicy.Parse([]byte(raw))
		} else {
			next, err = agentpolicy.ParseStored([]byte(raw))
		}
		if err != nil {
			return "", "", false, agentPolicyErr(AgentPolicyInvalid, "%v", err)
		}
		b, err := next.Canonical()
		if err != nil {
			return "", "", false, err
		}
		canonical := string(b)
		var prev *agentpolicy.Document
		if current != nil {
			if prev, err = agentpolicy.ParseStored([]byte(current.Document)); err != nil {
				// 🔴 NOTHING BUT THE KILL SWITCH CHANGES A POLICY THIS RELEASE CANNOT READ. A
				// loosening is judged against the policy in force, and an unreadable one cannot be
				// judged: classifying against "nothing" would let a publish or a rollback drop
				// rules this release cannot see without anyone presenting a second factor. A
				// newer release wrote it (or it is damaged); finish the upgrade, and change it
				// from a pod that can read it.
				if origin != iam.AgentPolicyOriginActuation || next.ActuationOn() {
					return "", "", false, agentPolicyErr(AgentPolicyUnreadable,
						"the agent policy in force cannot be read by this release (a newer release wrote it, or it is damaged); finish the upgrade first, then change it")
				}
				// The kill switch: classified against what CAN be read of the predecessor, its
				// frozen service-account rule, so dropping that rule is still a loosening.
				prev = &agentpolicy.Document{}
				if req, ok := agentpolicy.ExtractAgentFlagRequirement([]byte(current.Document)); ok && len(req) > 0 {
					prev.ServiceAccounts = &agentpolicy.ServiceAccts{AgentFlagRequiredFor: req}
				}
			}
		}
		if loosen := agentpolicy.Loosenings(prev, next); len(loosen) > 0 {
			if !auth.StepUpFresh(claims, stricterStepUp(prev, next), now) {
				return "", "", false, agentPolicyErr(AgentPolicyStepUpRequired,
					"this change loosens the agent policy (%s) and needs a second factor verified in the last %s",
					strings.Join(loosen, "; "), stricterStepUp(prev, next).MaxAge)
			}
		}
		return canonical, origin, clearDraft, nil
	})
	if err != nil {
		return nil, err
	}
	return m.AgentPolicy(ctx, claims.Tenant)
}

// stricterStepUp is the step-up rule both documents agree on.
func stricterStepUp(prev, next *agentpolicy.Document) auth.StepUp {
	su := auth.StepUp{MaxAge: next.StepUpMaxAge(), AcceptRecoveryCodes: next.AcceptRecoveryCodes()}
	if prev != nil {
		su.MaxAge = min(su.MaxAge, prev.StepUpMaxAge())
		su.AcceptRecoveryCodes = su.AcceptRecoveryCodes && prev.AcceptRecoveryCodes()
	}
	return su
}

// SetTenantAgentActuationSuspended is the operator's per-tenant override (K3), from the admin
// plane. Suspending refuses every act:* grant and every decision in the tenant whatever its
// policy says; lifting it restores the tenant's own policy.
func (m *Manager) SetTenantAgentActuationSuspended(ctx context.Context, tenant string, suspended bool, by string) (*iam.AgentPolicy, error) {
	if _, err := m.iam.TenantByToken(ctx, tenant); err != nil {
		return nil, err
	}
	head, err := m.iam.SetAgentPolicySuspended(ctx, tenant, suspended, by)
	if err != nil {
		return nil, err
	}
	// The head keeps only the latest state; each transition is its own journal row, so the
	// history of suspensions and restorations can be read back.
	op := rdb.AuditOpAgentActuationRestored
	if suspended {
		op = rdb.AuditOpAgentActuationSuspended
	}
	m.recordAuthLabeled(ctx, op, by, tenant, tenant)
	return head, nil
}

// principalOf is the canonical principal key, the same one the journal's actor uses.
func principalOf(c *auth.Claims) string {
	if c == nil {
		return ""
	}
	return c.Username
}

// publishedPolicy reads the tenant's state for the act:* checks and decisions. A store error is
// returned; a document that cannot be parsed is logged and read as NO policy (fail closed).
func (m *Manager) publishedPolicy(ctx context.Context, tenant string) (suspended bool, doc *agentpolicy.Document, version int, err error) {
	head, v, err := m.iam.PublishedAgentPolicy(ctx, tenant)
	if err != nil {
		return false, nil, 0, err
	}
	if head != nil {
		suspended = head.OperatorSuspended
	}
	if v == nil {
		return suspended, nil, 0, nil
	}
	d, perr := agentpolicy.ParseStored([]byte(v.Document))
	if perr != nil {
		log.Error().Err(perr).Str("tenant", tenant).Int("version", v.Version).
			Msg("The published agent policy cannot be read; agent actuation is refused in this tenant.")
		return suspended, nil, 0, nil
	}
	return suspended, d, v.Version, nil
}

// errActRefused is the one description every act:* admission refusal carries to the client: it
// does not say which switch was off.
const errActRefused = "agent actions are not available for this client in this tenant"

// admitActScopes is the ONE check that decides whether a grant may carry act:* scopes, run at
// /authorize (when the tenant is chosen), at code redemption, at every refresh, and at the
// client_credentials mint of an agent service account. It is nil when scope names no act:* scope.
//
// Every one of these must hold, re-read now:
//   - the instance flag is on;
//   - the grant is not a superuser's (an operator acts in a tenant through a membership);
//   - for an OAuth client: the client exists, is enabled, and the operator registered every
//     requested scope on it (serviceAccount skips this: an account has no registration);
//   - the operator has not suspended agent actuation for the tenant;
//   - the tenant's published policy enables actuation and lists this client id.
//
// A refusal is invalid_scope; the caller at refresh turns it into invalid_grant. A store error is a
// server_error and decides nothing.
func (m *Manager) admitActScopes(ctx context.Context, tenant, clientID, scope string, su, serviceAccount bool) *oauthError {
	if !auth.HasActScope(scope) {
		return nil
	}
	if !m.agentActuation || su || clientID == "" {
		return errInvalidScope(errActRefused)
	}
	if !serviceAccount {
		client, err := m.iam.OAuthClientByClientId(ctx, clientID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errInvalidScope(errActRefused)
		}
		if err != nil {
			log.Error().Err(err).Msg("Could not load the OAuth client to admit an act scope.")
			return errServer(errGrantUnavailable)
		}
		if !client.Enabled || !scopesRegistered(client.Scopes, scope) {
			return errInvalidScope(errActRefused)
		}
	}
	suspended, doc, _, err := m.publishedPolicy(ctx, tenant)
	if err != nil {
		log.Error().Err(err).Msg("Could not load the agent policy to admit an act scope.")
		return errServer(errGrantUnavailable)
	}
	if suspended || !doc.ActuationOn() || !doc.ClientAllowed(clientID) {
		return errInvalidScope(errActRefused)
	}
	return nil
}

// splitScope separates a scope string into its read part and its act part.
func splitScope(scope string) (read, act []string) {
	for _, s := range auth.ParseScope(scope) {
		if auth.IsActScope(s) {
			act = append(act, s)
		} else {
			read = append(read, s)
		}
	}
	return read, act
}

// actCeiling is the union of the proposal ceilings of act scopes.
func actCeiling(act []string) ([]string, error) {
	var out []string
	for _, s := range act {
		c, ok := auth.ActScopeCeiling(s)
		if !ok {
			return nil, fmt.Errorf("unknown scope %q", s)
		}
		for _, a := range c {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

// grantAuthorities is derive-or-intersect, the ONE place an OAuth grant's authorities are
// computed (grant and refresh both reach it through resolveScopedGrant): the read scopes intersect
// the subject's effective authorities exactly as before, the act scopes DERIVE proposal
// authorities from the subject's write authorities, and a mixed request gets the union.
//
// 🔴 AN act:* SCOPE NEVER CARRIES A RAW WRITE. The derivation emits proposal authorities only, and
// the read intersection never emits one: holding "*" counts as holding every write, but neither
// "*" nor any write reaches the token.
func grantAuthorities(authorities []string, su, scoped bool, scope string) ([]string, error) {
	read, act := splitScope(scope)
	var out []string
	if len(read) > 0 {
		allow, err := scopeAllowance(strings.Join(read, " "))
		if err != nil {
			return nil, err
		}
		out = capToScope(authorities, su, allow)
	}
	if len(act) > 0 {
		ceiling, err := actCeiling(act)
		if err != nil {
			return nil, err
		}
		out = unionStrings(out, auth.DeriveProposalAuthorities(effectiveAuthorities(authorities, su), ceiling, scoped))
	}
	if len(read) == 0 && len(act) == 0 {
		return nil, fmt.Errorf("no scope requested")
	}
	return out, nil
}

// eligibleApprovers counts the members of tenant who could approve: an enabled membership of an
// enabled identity WITH AN ACTIVE SECOND FACTOR (approval needs a step-up, so a person who cannot
// present one cannot approve), UNSCOPED (a customer- or group-scoped member covers only part of the tenant, and
// a count cannot know the targets, so it is not counted), holding one of roles and the raw write
// authority w (directly or through "*"). It may undercount, never overcount: an approval verdict
// is refused when it needs more approvers than this.
func (m *Manager) eligibleApprovers(ctx context.Context, tenant string, roles []string, w auth.Authority) (int, error) {
	if len(roles) == 0 {
		return 0, nil
	}
	ms, err := m.iam.TenantMemberships(ctx, tenant)
	if err != nil {
		return 0, err
	}
	n := 0
	var candidates []uint
	for i := range ms {
		mem := &ms[i]
		if !mem.Enabled || mem.IsScoped() {
			continue
		}
		inRole := false
		for _, r := range mem.TenantRoles {
			if slices.Contains(roles, r.Token) {
				inRole = true
				break
			}
		}
		if !inRole {
			continue
		}
		held := mem.TenantAuthorities()
		if !slices.Contains(held, string(w)) && !slices.Contains(held, string(auth.AuthorityAll)) {
			continue
		}
		candidates = append(candidates, mem.IdentityID)
	}
	ok, err := m.iam.ApproverCandidates(ctx, candidates)
	if err != nil {
		return 0, err
	}
	for _, id := range candidates {
		if ok[id] {
			n++
		}
	}
	return n, nil
}

// kindSource is the raw write authority an action of each kind needs: the authority its approvers
// must hold.
var kindSource = map[agentpolicy.Kind]auth.Authority{
	agentpolicy.KindAlarmAck:      auth.AlarmWrite,
	agentpolicy.KindAlarmClear:    auth.AlarmWrite,
	agentpolicy.KindCommandSend:   auth.CommandWrite,
	agentpolicy.KindCommandBatch:  auth.CommandWrite,
	agentpolicy.KindRuleChangeset: auth.DeviceWrite,
	agentpolicy.KindRolloutCreate: auth.ArtifactWrite,
	agentpolicy.KindAnnotation:    auth.AlarmWrite,
}

// clientAttested reports whether a client id names an authenticated principal: a confidential
// OAuth client, or a service account (which always authenticates with a secret). A public
// client's id is a label anyone can claim.
func (m *Manager) clientAttested(ctx context.Context, clientID string) (bool, error) {
	c, err := m.iam.OAuthClientByClientId(ctx, clientID)
	switch {
	case err == nil:
		return c.Enabled && c.IsConfidential(), nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return false, err
	}
	a, err := m.iam.ServiceAccountByClientId(ctx, clientID)
	switch {
	case err == nil:
		return a.Enabled && a.Agent, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return false, nil
	}
	return false, err
}

// AgentActuationDecision answers agentActuationDecision: the tenant's verdict on a prospective
// agent action, for the caller's OWN tenant and the client id in the caller's OWN token (never an
// argument). It reads everything now, with no cache, and a store error is returned rather than
// answered: a caller must treat an error as a refusal.
//
// Only an AGENT may ask: an agent-origin token that carries at least one proposal authority. A
// person's console token, or an integration's, is refused with ErrForbidden rather than answered,
// so the query is not an oracle over the tenant's policy and approvers for anyone else.
//
// It is cheap first: the switches, the client and the kind are decided before anything is counted,
// and the approver count (the one query over the tenant's members) runs only for a verdict that
// would need one.
func (m *Manager) AgentActuationDecision(ctx context.Context, claims *auth.Claims, req agentpolicy.Request) (agentpolicy.Decision, error) {
	if !holdsProposalAuthority(claims) {
		return agentpolicy.Decision{}, fmt.Errorf("%w: agentActuationDecision answers only an agent's token", auth.ErrForbidden)
	}
	req.Client = claims.ClientId
	req.Attested = false
	st := agentpolicy.State{InstanceEnabled: m.agentActuation}
	suspended, doc, version, err := m.publishedPolicy(ctx, claims.Tenant)
	if err != nil {
		return agentpolicy.Decision{}, err
	}
	st.OperatorSuspended, st.Policy, st.Version = suspended, doc, version
	// Decide as though approvers were plentiful: a refusal for any other reason needs no count.
	st.EligibleApprovers = 2
	if pre := agentpolicy.Decide(st, req); pre.Verdict == agentpolicy.VerdictRefuse {
		return pre, nil
	}
	if req.Attested, err = m.clientAttested(ctx, req.Client); err != nil {
		return agentpolicy.Decision{}, err
	}
	st.EligibleApprovers = 0
	if w, ok := kindSource[req.Kind]; ok && doc.Approvers != nil {
		if st.EligibleApprovers, err = m.eligibleApprovers(ctx, claims.Tenant, doc.Approvers.Roles, w); err != nil {
			return agentpolicy.Decision{}, err
		}
	}
	return agentpolicy.Decide(st, req), nil
}

// holdsProposalAuthority reports whether claims are an agent's: agent-origin, and literally
// carrying at least one proposal authority (which only the mint-time derivation puts there).
func holdsProposalAuthority(c *auth.Claims) bool {
	if c == nil || !auth.IsAgentOrigin(c) {
		return false
	}
	for _, a := range c.Authorities {
		if auth.IsProposalAuthority(a) {
			return true
		}
	}
	return false
}

// requiredAgentFlags reads the tenant's serviceAccounts.agentFlagRequiredFor for the service
// account paths (create, access change, mint).
//
// 🔴 THE RULE NEVER LAPSES. A damaged document of this release's format is an error, and the
// caller refuses (fail closed). A document written by a NEWER release during a rolling upgrade has
// its one frozen key extracted (agentpolicy.AgentFlagRequirement), so an older pod enforces the
// same rule as the new ones instead of minting the raw writes they would refuse, without stopping
// every integration on the old pods until the rollout finishes.
func requiredAgentFlags(ctx context.Context, store *iam.Store, tenant string) ([]string, error) {
	_, v, err := store.PublishedAgentPolicy(ctx, tenant)
	if err != nil || v == nil {
		return nil, err
	}
	required, perr := agentpolicy.AgentFlagRequirement([]byte(v.Document))
	if perr != nil {
		return nil, fmt.Errorf("the published agent policy of %s cannot be read: %w", tenant, perr)
	}
	return required, nil
}

// checkActSessionCap enforces the policy's absolute cap on an act:* grant
// (session.actRefreshMaxAge), counted from the moment the sign-in was accepted and never extended
// by a refresh. A grant past it must be authorized again.
func (m *Manager) checkActSessionCap(ctx context.Context, tenant string, in *signIn) *oauthError {
	if in == nil || in.anchor.IsZero() {
		// A sign-in with no recorded anchor has no age to bound: refused rather than unbounded.
		return errInvalidGrant(errActRefused)
	}
	_, doc, _, err := m.publishedPolicy(ctx, tenant)
	if err != nil {
		log.Error().Err(err).Msg("Could not load the agent policy to bound an act grant.")
		return errServer(errGrantUnavailable)
	}
	if m.clock().Sub(in.anchor) > doc.ActRefreshMaxAge() {
		return errInvalidGrant("the agent session has reached its maximum age; authorize again")
	}
	return nil
}
