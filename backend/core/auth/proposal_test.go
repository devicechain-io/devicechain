// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// A proposal authority is derived only from its own source write authority. Holding a DIFFERENT
// write (or only the read) derives nothing.
func TestProposalAuthorityRequiresSourceWrite(t *testing.T) {
	cases := []struct {
		prop   Authority
		source Authority
		decoy  Authority
	}{
		{AlarmPropose, AlarmWrite, AlarmRead},
		{CommandPropose, CommandWrite, AlarmWrite},
		{RulePropose, DeviceWrite, DeviceRead},
		{RolloutPropose, ArtifactWrite, ArtifactTrust},
		{AnnotationPropose, AlarmWrite, DeviceWrite},
	}
	for _, tc := range cases {
		ceiling := []string{string(tc.prop)}
		require.Equal(t, []string{string(tc.prop)}, DeriveProposalAuthorities([]string{string(tc.source)}, ceiling, false),
			"%s must derive from %s", tc.prop, tc.source)
		require.Empty(t, DeriveProposalAuthorities([]string{string(tc.decoy)}, ceiling, false),
			"%s must NOT derive from %s", tc.prop, tc.decoy)
		require.Empty(t, DeriveProposalAuthorities(nil, ceiling, false))
	}
}

// "*" counts as holding every source write, but neither "*" nor any write authority is ever copied
// into the result: the result holds proposal authorities and nothing else.
func TestStarNeverCopiedIntoActToken(t *testing.T) {
	var all []string
	for _, s := range ActScopes {
		c, ok := ActScopeCeiling(s)
		require.True(t, ok)
		all = append(all, c...)
	}
	// A hostile ceiling: raw writes and "*" mixed in must be ignored, not passed through.
	hostile := append([]string{string(AuthorityAll), string(CommandWrite), string(AgentAdmin)}, all...)
	got := DeriveProposalAuthorities([]string{string(AuthorityAll)}, hostile, false)
	require.ElementsMatch(t, all, got)
	for _, a := range got {
		require.True(t, IsProposalAuthority(a), "%q is not a proposal authority", a)
	}
}

// A scoped grant (customer or group anchors) never derives rule:propose: a profile serves devices
// outside any scope.
func TestScopedGrantNeverDerivesRulePropose(t *testing.T) {
	held := []string{string(DeviceWrite), string(AlarmWrite)}
	ceiling := []string{string(RulePropose), string(AlarmPropose)}
	require.Equal(t, []string{string(RulePropose), string(AlarmPropose)}, DeriveProposalAuthorities(held, ceiling, false))
	require.Equal(t, []string{string(AlarmPropose)}, DeriveProposalAuthorities(held, ceiling, true))
}

// agent:admin governs the policy that bounds agents, so no act:* ceiling may ever carry it.
func TestAgentAdminNeverInActCeiling(t *testing.T) {
	for _, s := range ActScopes {
		c, ok := ActScopeCeiling(s)
		require.True(t, ok)
		require.NotContains(t, c, string(AgentAdmin), "act scope %s", s)
	}
	for s, c := range scopeAllowances {
		require.NotContains(t, c, string(AgentAdmin), "scope %s", s)
	}
	// And derivation cannot produce it whatever the ceiling says.
	require.Empty(t, DeriveProposalAuthorities([]string{string(AuthorityAll)}, []string{string(AgentAdmin)}, false))
}

// Each act ceiling is frozen per name: widening one silently widens every live grant at refresh.
// LITERAL, deliberately not derived from the table it checks.
func TestActCeilingsAreExact(t *testing.T) {
	want := map[string][]string{
		"act:alarms":   {"alarm:propose"},
		"act:commands": {"command:propose"},
		"act:rules":    {"rule:propose"},
		"act:updates":  {"rollout:propose"},
		"act:annotate": {"annotation:propose"},
	}
	require.Len(t, actScopeCeilings, len(want))
	for s, w := range want {
		require.Equal(t, w, actScopeCeilings[s], s)
	}
	require.ElementsMatch(t, []string{"act:alarms", "act:commands", "act:rules", "act:updates", "act:annotate"}, ActScopes)
}

// Proposal authorities are derived, never granted: no role and no service-token request may name
// one, and the role editor never offers one.
func TestProposalAuthoritiesAreNotGrantable(t *testing.T) {
	for p := range proposalSources {
		require.False(t, ValidAuthority(string(p)), "%s must not be grantable", p)
		require.NotContains(t, Authorities(), string(p))
		require.NotContains(t, AuthoritiesForScope(TierTenant), string(p))
		require.NotContains(t, AuthoritiesForScope(TierSystem), string(p))
		tiers, ok := TiersOf(p)
		require.True(t, ok)
		require.Equal(t, Tiers{TierTenant}, tiers)
	}
	require.True(t, ValidAuthority(string(AgentAdmin)), "agent:admin is an ordinary tenant authority a role may grant")
	tiers, ok := TiersOf(AgentAdmin)
	require.True(t, ok)
	require.Equal(t, Tiers{TierTenant}, tiers)
}

// "*" on a console token does not satisfy a proposal authority: a human acting directly is not an
// agent asking. A token that literally carries the proposal authority does.
func TestStarNeverSatisfiesProposal(t *testing.T) {
	human := &Claims{Authorities: []string{string(AuthorityAll)}, TokenType: TokenTypeAccess}
	for p := range proposalSources {
		require.False(t, human.HasAuthority(p), "%s satisfied by *", p)
	}
	require.False(t, human.HasAuthority("future:propose"), "an undefined proposal-shaped authority must not be satisfied by *")
	require.True(t, human.HasAuthority(AlarmWrite), "* still satisfies every ordinary authority")
	agent := &Claims{Authorities: []string{string(AlarmPropose)}, TokenType: TokenTypeAccess}
	require.True(t, agent.HasAuthority(AlarmPropose))
	require.False(t, agent.HasAuthority(AlarmWrite))
}

// The read-scope intersection never emits a proposal authority, even for a "*" holder and even if
// a ceiling named one: proposals come from DeriveProposalAuthorities only.
func TestIntersectNeverEmitsProposal(t *testing.T) {
	got := IntersectAuthorities([]string{string(AuthorityAll)}, []string{string(AlarmPropose), string(AlarmRead)})
	require.Equal(t, []string{string(AlarmRead)}, got)
}

// The act scopes are advertised and accepted only when the instance flag is on; the default list is
// exactly the v1.1 one.
func TestActScopesAdvertisedOnlyWhenFlagOn(t *testing.T) {
	require.Equal(t, []string{ScopeReadOnly, ScopeLocation}, SupportedScopes)
	require.Equal(t, SupportedScopes, SupportedScopesFor(false))
	require.Equal(t, append([]string{ScopeReadOnly, ScopeLocation}, ActScopes...), SupportedScopesFor(true))
	for _, s := range ActScopes {
		require.False(t, IsSupportedScope(s), s)
		require.False(t, IsSupportedScopeFor(s, false), s)
		require.True(t, IsSupportedScopeFor(s, true), s)
		_, ok := ScopeAllowance(s)
		require.False(t, ok, "ScopeAllowance must keep refusing %s: act scopes are derived, never intersected", s)
	}
	require.False(t, ScopeSupportedFor("read-only act:alarms", false))
	require.True(t, ScopeSupportedFor("read-only act:alarms", true))
	require.True(t, HasActScope("read-only act:commands"))
	require.False(t, HasActScope("read-only location"))
}

// mfa_time rides only beside an amr that proves a second factor, at mint and at parse.
func TestMfaTimeRequiresSecondFactorAmr(t *testing.T) {
	iss, v, key := amrFixture(t)
	now := time.Now()
	_, err := iss.IssueTenantAccess("t", "a@example.com", nil, nil, false, "j", WithAmr(AmrPasswordOnly()), WithMfaTime(now))
	require.ErrorIs(t, err, ErrMalformedAmr)
	_, err = iss.IssueTenantAccess("t", "a@example.com", nil, nil, false, "j", WithMfaTime(now))
	require.ErrorIs(t, err, ErrMalformedAmr)

	ok, err := iss.IssueTenantAccess("t", "a@example.com", nil, nil, false, "j", WithAmr(AmrPasswordOTP()), WithMfaTime(now))
	require.NoError(t, err)
	c, err := v.Parse(ok.Token)
	require.NoError(t, err)
	require.NotNil(t, c.MfaTime)
	require.Equal(t, now.Unix(), c.MfaTime.Unix())

	// A forged token carrying mfa_time with a password-only amr is refused at parse.
	forged := &Claims{Tenant: "t", Username: "a@example.com", TokenType: TokenTypeAccess, Amr: AmrPasswordOnly(),
		MfaTime: jwt.NewNumericDate(now), RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "test", ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)), IssuedAt: jwt.NewNumericDate(now)}}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, forged)
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	_, err = v.Parse(signed)
	require.ErrorIs(t, err, ErrMalformedAmr)
}

// A recovery-code sign-in ({pwd,mfa}) is NOT a step-up unless the policy accepts recovery codes.
func TestRecoveryCodeAmrNotStepUp(t *testing.T) {
	now := time.Now()
	su := StepUp{MaxAge: 15 * time.Minute}
	recovery := &Claims{TokenType: TokenTypeAccess, Amr: AmrPasswordRecovery(), MfaTime: jwt.NewNumericDate(now)}
	require.False(t, StepUpFresh(recovery, su, now))
	su.AcceptRecoveryCodes = true
	require.True(t, StepUpFresh(recovery, su, now))

	otp := &Claims{TokenType: TokenTypeAccess, Amr: AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now)}
	require.True(t, StepUpFresh(otp, StepUp{MaxAge: 15 * time.Minute}, now))
}

// Freshness is mfa_time and nothing else: a token minted a second ago from a sign-in whose factor
// was verified an hour ago is NOT fresh, and a token with no mfa_time never is.
func TestStepUpUsesMfaTimeNotIat(t *testing.T) {
	now := time.Now()
	su := StepUp{MaxAge: 15 * time.Minute}
	stale := &Claims{TokenType: TokenTypeAccess, Amr: AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now.Add(-time.Hour)),
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now)}}
	require.False(t, StepUpFresh(stale, su, now))
	missing := &Claims{TokenType: TokenTypeAccess, Amr: AmrPasswordOTP(),
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now)}}
	require.False(t, StepUpFresh(missing, su, now))
	edge := &Claims{TokenType: TokenTypeAccess, Amr: AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now.Add(-14 * time.Minute))}
	require.True(t, StepUpFresh(edge, su, now))
	// Only an access token can step up.
	for _, typ := range []string{TokenTypeRefresh, TokenTypeIdentity, TokenTypeService, ""} {
		c := &Claims{TokenType: typ, Amr: AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now)}
		require.False(t, StepUpFresh(c, su, now), typ)
	}
	require.False(t, StepUpFresh(nil, su, now))
}
