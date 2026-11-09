// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The agent policy and the act:* scopes, driven through the real grant, redemption, refresh and
// client_credentials paths over the real store.

const (
	agentClient  = "agent-desktop"
	agentTenant  = "acme"
	agentPerson  = "ana@example.invalid"
	actScopeAll  = "read-only act:alarms act:commands"
	openPolicyJS = `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"]}}`
)

// codesKVStub is a single-use code store: Create, Get and a Delete that consumes.
type codesKVStub struct{ refreshKVStub }

func (s *codesKVStub) Create(key string, value []byte) (uint64, error) {
	if _, ok := s.entries[key]; ok {
		return 0, nats.ErrKeyExists
	}
	return s.Put(key, value)
}

type agentEnv struct {
	*refreshEnv
	codes *codesKVStub
}

func newAgentEnv(t *testing.T, authorities ...string) *agentEnv {
	t.Helper()
	base := newRefreshEnv(t, agentClient, auth.ScopeReadOnly, auth.ScopeActAlarms, auth.ScopeActCommands)
	base.m.SetAgentActuation(true)
	codes := &codesKVStub{}
	base.m.codesKV = codes
	base.seedTenant(t, agentTenant)
	base.seedMember(t, agentPerson, agentTenant, authorities...)
	return &agentEnv{refreshEnv: base, codes: codes}
}

// publish puts doc in force directly, bypassing the step-up (the store, not the manager).
func (e *agentEnv) publish(t *testing.T, doc string) {
	t.Helper()
	d, err := agentpolicy.Parse([]byte(doc))
	require.NoError(t, err)
	b, err := d.Canonical()
	require.NoError(t, err)
	_, err = e.store.PublishAgentPolicy(context.Background(), agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return string(b), iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)
}

func (e *agentEnv) grantAct(t *testing.T, scope string) (*OAuthTokens, error) {
	t.Helper()
	return e.m.mintScopedGrant(context.Background(), agentPerson, e.epochOf(t, agentPerson), e.sidOf(t, agentPerson), agentTenant,
		scope, scope, []string{"https://mcp.example.invalid"}, agentClient)
}

func oauthCode(err error) string {
	var oe *oauthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

// With no published policy, an act:* scope is refused at the grant, whatever the person holds.
// Publishing a policy that enables actuation and lists the client is what admits it.
func TestActScopeRefusedWhenPolicyAbsent(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite), string(auth.CommandWrite))
	_, err := e.grantAct(t, actScopeAll)
	require.Error(t, err, "an act scope must be refused with no published policy")
	require.Equal(t, "invalid_grant", oauthCode(err))

	// A read-only grant is untouched by the agent policy.
	_, err = e.grantAct(t, auth.ScopeReadOnly)
	require.NoError(t, err)

	e.publish(t, openPolicyJS)
	toks, err := e.grantAct(t, actScopeAll)
	require.NoError(t, err, "control: a published policy listing the client admits the act scopes")
	c := e.claimsOf(t, toks.AccessToken)
	require.Subset(t, c.Authorities, []string{string(auth.AlarmPropose), string(auth.CommandPropose)})
	require.NotContains(t, c.Authorities, string(auth.AlarmWrite))
	require.NotContains(t, c.Authorities, string(auth.CommandWrite))
	require.True(t, auth.IsAgentOrigin(c))
}

// A published policy that does not list the client refuses it; so does a disabled policy, the
// operator's suspension, and the instance flag off.
func TestActScopeRefusedForUnlistedClient(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, `{"actuation":"enabled","clients":["someone-else"]}`)
	_, err := e.grantAct(t, "read-only act:alarms")
	require.Equal(t, "invalid_grant", oauthCode(err), "an unlisted client must be refused")

	e.publish(t, `{"actuation":"disabled","clients":["agent-desktop"]}`)
	_, err = e.grantAct(t, "read-only act:alarms")
	require.Equal(t, "invalid_grant", oauthCode(err), "a disabled policy must refuse")

	e.publish(t, openPolicyJS)
	_, err = e.grantAct(t, "read-only act:alarms")
	require.NoError(t, err, "control")

	_, err = e.store.SetAgentPolicySuspended(context.Background(), agentTenant, true, "operator")
	require.NoError(t, err)
	_, err = e.grantAct(t, "read-only act:alarms")
	require.Equal(t, "invalid_grant", oauthCode(err), "the operator's suspension must refuse")
	_, err = e.store.SetAgentPolicySuspended(context.Background(), agentTenant, false, "operator")
	require.NoError(t, err)

	e.m.SetAgentActuation(false)
	_, err = e.grantAct(t, "read-only act:alarms")
	require.Error(t, err, "the instance flag off must refuse")
}

// The client's registration is checked too: an act scope the operator did not register on the
// client is refused even when the policy lists the client.
func TestActScopeRefusedWhenNotRegisteredOnClient(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, openPolicyJS)
	e.registerScopes(t, agentClient, auth.ScopeReadOnly)
	_, err := e.grantAct(t, "read-only act:alarms")
	require.Equal(t, "invalid_grant", oauthCode(err))
}

// A refresh re-reads the policy: delisting the client ends the agent's session at its next
// refresh, and the refresh token is burnt.
func TestRefreshRefusedAfterClientDelisted(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, openPolicyJS)
	toks, err := e.grantAct(t, "read-only act:alarms")
	require.NoError(t, err)

	renewed, err := e.m.RefreshOAuth(context.Background(), toks.RefreshToken, "", agentClient, nil)
	require.NoError(t, err, "control: a listed client refreshes")

	e.publish(t, `{"actuation":"enabled","clients":["someone-else"]}`)
	_, err = e.m.RefreshOAuth(context.Background(), renewed.RefreshToken, "", agentClient, nil)
	require.Equal(t, "invalid_grant", oauthCode(err), "a delisted client's refresh must be refused")
}

// An authorization code issued while the policy allowed it cannot be redeemed after the tenant
// turned agent actuation off.
func TestCodeRedemptionRefusedAfterPolicyDisabled(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, openPolicyJS)
	ctx := context.Background()
	client, err := e.m.ResolveAuthorizeClient(ctx, agentClient, "https://example.invalid/cb")
	require.NoError(t, err)
	subject := IdentitySubject{Email: agentPerson, SessionEpoch: e.epochOf(t, agentPerson), SessionID: e.sidOf(t, agentPerson)}
	params := AuthorizeParams{ResponseType: "code", ClientID: agentClient, RedirectURI: "https://example.invalid/cb",
		Scope: "read-only act:alarms", CodeChallenge: pkceChallenge("verifier-0123456789-0123456789-0123456789"), CodeChallengeMethod: "S256"}
	_, err = e.m.ValidateAuthorize(client, params)
	require.NoError(t, err, "with the flag on, act:alarms is a supported scope")

	code, err := e.m.IssueAuthorizationCode(ctx, client, params, subject, agentTenant)
	require.NoError(t, err)

	// Kill switch: no step-up needed to turn it off.
	_, err = e.m.SetTenantAgentActuation(ctx, &auth.Claims{Tenant: agentTenant, Username: "admin@example.invalid", TokenType: auth.TokenTypeAccess}, false)
	require.NoError(t, err)

	_, err = e.m.RedeemAuthorizationCode(ctx, code, agentClient, "https://example.invalid/cb", "verifier-0123456789-0123456789-0123456789", nil)
	require.Equal(t, "invalid_grant", oauthCode(err), "a code must not be redeemed after actuation was turned off")

	// And the authorize step itself now refuses before issuing a code.
	_, err = e.m.IssueAuthorizationCode(ctx, client, params, subject, agentTenant)
	var re *authorizeRedirectError
	require.True(t, errors.As(err, &re), "got %v", err)
	require.Equal(t, "invalid_scope", re.Code)
}

// With the flag off, act scopes are not even supported at /authorize.
func TestAuthorizeRefusesActScopeWhenFlagOff(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.m.SetAgentActuation(false)
	client, err := e.m.ResolveAuthorizeClient(context.Background(), agentClient, "https://example.invalid/cb")
	require.NoError(t, err)
	_, err = e.m.ValidateAuthorize(client, AuthorizeParams{ResponseType: "code", ClientID: agentClient, RedirectURI: "https://example.invalid/cb",
		Scope: "read-only act:alarms", CodeChallenge: "x", CodeChallengeMethod: "S256"})
	var re *authorizeRedirectError
	require.True(t, errors.As(err, &re))
	require.Equal(t, "invalid_scope", re.Code)
	require.NotContains(t, e.m.SupportedScopes(), auth.ScopeActAlarms)
	e.m.SetAgentActuation(true)
	require.Contains(t, e.m.SupportedScopes(), auth.ScopeActAlarms)
}

// A superuser's grant is refused every act:* scope, even with a membership and a policy listing
// the client: an operator acts in a tenant through a membership, never through sudo.
func TestSudoGrantRefusedActScopes(t *testing.T) {
	e := newAgentEnv(t)
	e.publish(t, openPolicyJS)
	e.seedSuperuser(t, "root@example.invalid")
	_, err := e.m.mintScopedGrant(context.Background(), "root@example.invalid", e.epochOf(t, "root@example.invalid"),
		e.sidOf(t, "root@example.invalid"), agentTenant, "read-only act:alarms", "read-only act:alarms", nil, agentClient)
	require.Equal(t, "invalid_grant", oauthCode(err))
	// Control: the same superuser's read-only grant works.
	_, err = e.m.mintScopedGrant(context.Background(), "root@example.invalid", e.epochOf(t, "root@example.invalid"),
		e.sidOf(t, "root@example.invalid"), agentTenant, auth.ScopeReadOnly, auth.ScopeReadOnly, nil, agentClient)
	require.NoError(t, err)
}

// A tenant administrator holding "*" grants exactly the proposal authorities of the act scopes:
// never "*", never a write.
func TestStarNeverCopiedIntoActTokenAtGrant(t *testing.T) {
	e := newAgentEnv(t, string(auth.AuthorityAll))
	e.publish(t, openPolicyJS)
	toks, err := e.grantAct(t, "act:alarms act:commands")
	require.NoError(t, err)
	c := e.claimsOf(t, toks.AccessToken)
	require.ElementsMatch(t, []string{string(auth.AlarmPropose), string(auth.CommandPropose)}, c.Authorities)
}

// Proposal authorities are re-derived at every refresh: a person who loses alarm:write loses
// alarm:propose at the agent's next refresh.
func TestRefreshDropsProposalWhenWriteRevoked(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite), string(auth.CommandWrite))
	e.publish(t, openPolicyJS)
	toks, err := e.grantAct(t, actScopeAll)
	require.NoError(t, err)
	require.Contains(t, e.claimsOf(t, toks.AccessToken).Authorities, string(auth.AlarmPropose))

	// Revoke alarm:write from the person's role.
	ctx := context.Background()
	id, err := e.store.IdentityByEmail(ctx, agentPerson)
	require.NoError(t, err)
	role := id.Memberships[0].TenantRoles[0]
	role.Authorities = []string{string(auth.CommandWrite)}
	require.NoError(t, e.m.db.Database.Save(&role).Error)

	renewed, err := e.m.RefreshOAuth(ctx, toks.RefreshToken, "", agentClient, nil)
	require.NoError(t, err)
	c := e.claimsOf(t, renewed.AccessToken)
	require.NotContains(t, c.Authorities, string(auth.AlarmPropose), "alarm:propose must not survive the loss of alarm:write")
	require.Contains(t, c.Authorities, string(auth.CommandPropose))
}

// The act session cap: an act grant whose sign-in is older than the policy's actRefreshMaxAge is
// refused at refresh.
func TestActRefreshRefusedPastSessionCap(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"session":{"actRefreshMaxAge":"1h"}}`)
	toks, err := e.grantAct(t, "read-only act:alarms")
	require.NoError(t, err)
	e.m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	_, err = e.m.RefreshOAuth(context.Background(), toks.RefreshToken, "", agentClient, nil)
	require.Equal(t, "invalid_grant", oauthCode(err))
}

// mfa_time is the time the sign-in VERIFIED its factor, copied unchanged through every refresh:
// a refresh never advances it, so a token minted now from an old verification is not fresh.
func TestMfaTimeNotAdvancedByRefresh(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	ctx := context.Background()
	sid, err := e.m.startSession(agentPerson, auth.AmrPasswordOTP())
	require.NoError(t, err)
	// Age the verification: rewrite the record as though the factor was presented an hour ago.
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	raw := e.kvr.entries[sessionKey(sid)]
	var rec sessionRecord
	require.NoError(t, json.Unmarshal(raw, &rec))
	require.NotNil(t, rec.MfaAt, "an OTP sign-in must record when the factor was verified")
	rec.MfaAt = &past
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	e.kvr.entries[sessionKey(sid)] = b

	// The console path: the tenant pair a sign-in mints, then a refresh of it.
	rec2, err := e.m.loadSession(sid, agentPerson)
	require.NoError(t, err)
	pair, err := e.m.issueTenantTokens(agentTenant, agentPerson, e.epochOf(t, agentPerson), sid, nil, nil, auth.Scope{}, false, rec2.signIn())
	require.NoError(t, err)
	first := e.claimsOf(t, pair.AccessToken)
	require.NotNil(t, first.MfaTime)
	require.Equal(t, past.Unix(), first.MfaTime.Unix())

	renewed, err := e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
	second := e.claimsOf(t, renewed.AccessToken)
	require.NotNil(t, second.MfaTime)
	require.Equal(t, past.Unix(), second.MfaTime.Unix(), "a refresh must not advance mfa_time")
	require.False(t, auth.StepUpFresh(second, auth.StepUp{MaxAge: 15 * time.Minute}, time.Now()),
		"a freshly minted token from an hour-old verification is not a step-up")

	// A password-only sign-in carries no mfa_time at all.
	plainSid := e.sidOf(t, agentPerson)
	rec3, err := e.m.loadSession(plainSid, agentPerson)
	require.NoError(t, err)
	plainPair, err := e.m.issueTenantTokens(agentTenant, agentPerson, e.epochOf(t, agentPerson), plainSid, nil, nil, auth.Scope{}, false, rec3.signIn())
	require.NoError(t, err)
	require.Nil(t, e.claimsOf(t, plainPair.AccessToken).MfaTime)
}

// 🔴 An OAuth (third-party client) token never carries mfa_time, even from a sign-in that verified
// a factor a second ago: when a person last presented a factor is not the client's business, and
// a step-up never accepts an agent-origin token anyway.
func TestOAuthTokenCarriesNoMfaTime(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	sid, err := e.m.startSession(agentPerson, auth.AmrPasswordOTP())
	require.NoError(t, err)
	toks := mustGrant(t, e, sid)
	access := e.claimsOf(t, toks.AccessToken)
	require.Nil(t, access.MfaTime)
	require.Equal(t, auth.AmrPasswordOTP(), access.Amr, "the amr itself still rides the token")
	refresh, err := e.validator.ValidateRefresh(toks.RefreshToken)
	require.NoError(t, err)
	require.Nil(t, refresh.MfaTime)
	renewed, err := e.m.RefreshOAuth(context.Background(), toks.RefreshToken, "", agentClient, nil)
	require.NoError(t, err)
	require.Nil(t, e.claimsOf(t, renewed.AccessToken).MfaTime)
}

func mustGrant(t *testing.T, e *agentEnv, sid auth.SessionID) *OAuthTokens {
	t.Helper()
	toks, err := e.m.mintScopedGrant(context.Background(), agentPerson, e.epochOf(t, agentPerson), sid, agentTenant,
		auth.ScopeReadOnly, auth.ScopeReadOnly, nil, agentClient)
	require.NoError(t, err)
	return toks
}

func adminClaims(mfa *time.Time, amr []string) *auth.Claims {
	c := &auth.Claims{Tenant: agentTenant, Username: "admin@example.invalid", TokenType: auth.TokenTypeAccess, Amr: amr}
	if mfa != nil {
		c.MfaTime = jwt.NewNumericDate(*mfa)
	}
	return c
}

func policyErrCode(err error) string {
	var pe *AgentPolicyError
	if errors.As(err, &pe) {
		return pe.code
	}
	return ""
}

// Publishing a LOOSER policy needs a second factor verified within the step-up age; a tighter one,
// and the kill switch, do not. A recovery-code sign-in is not a step-up unless the policy accepts
// recovery codes.
func TestLoosenRequiresStepUp(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	now := time.Now()
	stale := now.Add(-time.Hour)
	noFactor := adminClaims(nil, auth.AmrPasswordOnly())
	staleFactor := adminClaims(&stale, auth.AmrPasswordOTP())
	recovery := adminClaims(&now, auth.AmrPasswordRecovery())
	fresh := adminClaims(&now, auth.AmrPasswordOTP())

	_, err := e.m.SaveAgentPolicyDraft(ctx, noFactor, openPolicyJS)
	require.NoError(t, err, "saving a draft changes nothing in force and needs no step-up")
	for name, c := range map[string]*auth.Claims{"no factor": noFactor, "stale factor": staleFactor, "recovery code": recovery} {
		_, err = e.m.PublishAgentPolicyDraft(ctx, c)
		require.Equal(t, AgentPolicyStepUpRequired, policyErrCode(err), name)
	}
	view, err := e.m.PublishAgentPolicyDraft(ctx, fresh)
	require.NoError(t, err)
	require.Equal(t, 1, view.Published.Version)

	// A tightening needs no factor.
	_, err = e.m.SaveAgentPolicyDraft(ctx, noFactor, `{"actuation":"enabled","clients":[],"approvers":{"roles":["approvers"]}}`)
	require.NoError(t, err)
	_, err = e.m.PublishAgentPolicyDraft(ctx, noFactor)
	require.NoError(t, err, "removing a client is a tightening")

	// The kill switch needs none; turning it back on does.
	_, err = e.m.SetTenantAgentActuation(ctx, noFactor, false)
	require.NoError(t, err)
	_, err = e.m.SetTenantAgentActuation(ctx, noFactor, true)
	require.Equal(t, AgentPolicyStepUpRequired, policyErrCode(err))
	_, err = e.m.SetTenantAgentActuation(ctx, fresh, true)
	require.NoError(t, err)

	// A rollback to the looser v1 is a loosening too.
	_, err = e.m.RollbackAgentPolicy(ctx, noFactor, 1)
	require.Equal(t, AgentPolicyStepUpRequired, policyErrCode(err))

	// A loosening cannot relax its own gate: a draft accepting recovery codes is still published
	// under the policy in force, which does not.
	_, err = e.m.SaveAgentPolicyDraft(ctx, noFactor, `{"actuation":"enabled","clients":["agent-desktop","x"],"stepUp":{"acceptRecoveryCodes":true}}`)
	require.NoError(t, err)
	_, err = e.m.PublishAgentPolicyDraft(ctx, recovery)
	require.Equal(t, AgentPolicyStepUpRequired, policyErrCode(err))

	vs, err := e.m.AgentPolicyVersions(ctx, agentTenant, 10)
	require.NoError(t, err)
	require.Len(t, vs, 4, "publish, tighten, kill, re-enable")
	require.Equal(t, iam.AgentPolicyOriginActuation, vs[0].Origin)
}

// Every decision with no policy, an unreadable one, the instance flag off, or the operator's
// suspension is a refusal; the client is the token's.
func TestDecisionFailsClosedOnMissingPolicy(t *testing.T) {
	e := newAgentEnv(t, string(auth.AlarmWrite))
	ctx := context.Background()
	agent := &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Scope: "act:alarms",
		Authorities: []string{string(auth.AlarmPropose)}, TokenType: auth.TokenTypeAccess}
	req := agentpolicy.Request{Kind: agentpolicy.KindAlarmAck, TargetCount: 1, Client: "forged-client-argument"}

	d, err := e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	require.Contains(t, d.Reasons, agentpolicy.ReasonNoPolicy)

	// Approvers: one member in the approver role holding alarm:write.
	e.seedApprover(t, "boss@example.invalid", "approvers", string(auth.AlarmWrite))
	e.publish(t, openPolicyJS)
	d, err = e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "control: a listed client is decided by the policy")
	require.Equal(t, 1, d.RequiredApprovals)
	require.Equal(t, 1, d.PolicyVersion)

	// Only an agent may ask: a console token, even a tenant administrator's with "*", and an
	// agent-origin token without a proposal authority are refused, whatever client is passed.
	for name, c := range map[string]*auth.Claims{
		"console":   {Tenant: agentTenant, Username: agentPerson, Authorities: []string{"*"}, TokenType: auth.TokenTypeAccess},
		"read-only": {Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Scope: "read-only", Authorities: []string{"alarm:read"}, TokenType: auth.TokenTypeAccess},
		"no origin": {Tenant: agentTenant, Username: agentPerson, Authorities: []string{"alarm:write"}, TokenType: auth.TokenTypeAccess},
	} {
		_, err = e.m.AgentActuationDecision(ctx, c, agentpolicy.Request{Kind: agentpolicy.KindAlarmAck, TargetCount: 1, Client: agentClient})
		require.ErrorIs(t, err, auth.ErrForbidden, name)
	}

	_, err = e.store.SetAgentPolicySuspended(ctx, agentTenant, true, "operator")
	require.NoError(t, err)
	d, err = e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	_, err = e.store.SetAgentPolicySuspended(ctx, agentTenant, false, "operator")
	require.NoError(t, err)

	e.m.SetAgentActuation(false)
	d, err = e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	e.m.SetAgentActuation(true)

	// A stored document that does not parse reads as NO policy.
	require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.AgentPolicyVersion{}).Where("tenant_id = ?", agentTenant).
		Update("document", `{"actuation":"enabled","clients":["agent-desktop"],"unknown":1}`).Error)
	d, err = e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
}

// seedApprover adds a member holding a role with the given token and authorities.
func (e *agentEnv) seedApprover(t *testing.T, email, roleToken string, authorities ...string) {
	t.Helper()
	ctx := context.Background()
	role := &iam.Role{Scope: iam.ScopeTenant, Token: roleToken, Authorities: authorities}
	require.NoError(t, e.store.CreateRole(ctx, role))
	require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{
		Email: email, Enabled: true, PasswordHash: "x",
		Memberships: []iam.Membership{{TenantId: agentTenant, Enabled: true, TenantRoles: []iam.Role{*role}}},
	}))
	e.enrollFactor(t, email)
}

// An AGENT service account's token never carries a raw write: reads, plus the proposal authorities
// derived from its roles' writes when an admitted act scope is asked for. An ordinary account may
// not ask for an act scope at all.
func TestAgentFlaggedServiceAccountMintsProposalsOnly(t *testing.T) {
	e := newCCEnv(t)
	e.m.SetAgentActuation(true)
	ctx := context.Background()
	agentID, agentSecret := e.account(t, "acme", "bot", nil, "device:read", "alarm:read", "alarm:write", "command:write")
	plainID, plainSecret := e.account(t, "acme", "ci", nil, "device:read", "command:write")
	require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.ServiceAccount{}).Where("client_id = ?", agentID).Update("agent", true).Error)

	publish := func(doc string) {
		d, err := agentpolicy.Parse([]byte(doc))
		require.NoError(t, err)
		b, _ := d.Canonical()
		_, err = e.store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
			return string(b), iam.AgentPolicyOriginPublish, false, nil
		})
		require.NoError(t, err)
	}

	// No scope: the agent account's token carries its reads and NO write.
	rec := e.grant(t, agentID, agentSecret)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	c, err := e.validator.Validate(decode(t, rec).AccessToken)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"device:read", "alarm:read"}, c.Authorities)

	// An act scope before the policy lists the account: refused.
	rec = e.grant(t, agentID, agentSecret, "scope", "read-only act:alarms act:commands")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_scope", decode(t, rec).Error)

	publish(`{"actuation":"enabled","clients":["` + agentID + `"]}`)
	rec = e.grant(t, agentID, agentSecret, "scope", "read-only act:alarms act:commands")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	c, err = e.validator.Validate(decode(t, rec).AccessToken)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"device:read", "alarm:read", "alarm:propose", "command:propose"}, c.Authorities)
	for _, a := range c.Authorities {
		require.False(t, strings.HasSuffix(a, ":write"), "an agent account's token carried %s", a)
	}

	// An ordinary account may not ask for an act scope, even when listed.
	publish(`{"actuation":"enabled","clients":["` + agentID + `","` + plainID + `"]}`)
	rec = e.grant(t, plainID, plainSecret, "scope", "read-only act:commands")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	// ...and its ordinary token is unchanged.
	rec = e.grant(t, plainID, plainSecret)
	require.Equal(t, http.StatusOK, rec.Code)
	c, err = e.validator.Validate(decode(t, rec).AccessToken)
	require.NoError(t, err)
	require.True(t, slices.Contains(c.Authorities, "command:write"))
}

// enrollFactor gives email an ACTIVE TOTP factor, which an eligible approver needs.
func (e *mintTestEnv) enrollFactor(t *testing.T, email string) {
	t.Helper()
	ctx := context.Background()
	id, err := e.store.IdentityByEmail(ctx, email)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).Create(&iam.MfaFactor{
		IdentityID: id.ID, Kind: iam.MfaKindTotp, State: iam.MfaStateActive, SecretName: "test-" + email, CreatedAt: now, ConfirmedAt: &now,
	}).Error)
}
