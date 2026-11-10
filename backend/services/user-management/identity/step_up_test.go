// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestStepUpBindsOnlyHumanAccessToConsoleRefresh(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	cases := map[string]func(*auth.Claims){
		"client":          func(c *auth.Claims) { c.ClientId = "agent" },
		"scope":           func(c *auth.Claims) { c.Scope = "read" },
		"audience":        func(c *auth.Claims) { c.Audience = jwt.ClaimStrings{"mcp"} },
		"proposal":        func(c *auth.Claims) { c.Authorities = []string{"command:propose"} },
		"service":         func(c *auth.Claims) { c.TokenType = auth.TokenTypeService },
		"service account": func(c *auth.Claims) { c.Username = auth.ServiceAccountSubjectPrefix + "robot" },
		"refresh":         func(c *auth.Claims) { c.TokenType = auth.TokenTypeRefresh },
		"identity":        func(c *auth.Claims) { c.TokenType = auth.TokenTypeIdentity },
		"username":        func(c *auth.Claims) { c.Username = "other@example.com" },
		"tenant":          func(c *auth.Claims) { c.Tenant = "other" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := *access
			change(&c)
			_, err := e.m.StepUp(context.Background(), &c, pair.RefreshToken, "123456", "203.0.113.5")
			require.ErrorIs(t, err, auth.ErrForbidden)
			_, err = e.m.refreshKV.Get(refresh.ID)
			require.NoError(t, err)
		})
	}
	_, err = e.m.StepUp(context.Background(), nil, pair.RefreshToken, "123456", "203.0.113.5")
	require.ErrorIs(t, err, auth.ErrForbidden)
	// A genuinely signed OAuth refresh is rejected before its grant can be widened.
	c := *refresh
	c.Scope = auth.ScopeReadOnly
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(e.key)
	require.NoError(t, err)
	_, err = e.m.StepUp(context.Background(), access, raw, "123456", "203.0.113.5")
	require.ErrorIs(t, err, ErrInvalidToken)
	// The unchanged console pair reaches the distinct no-factor refusal.
	_, err = e.m.StepUp(context.Background(), access, pair.RefreshToken, "123456", "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaNotEnabled)
}

func TestStepUpRefusesEndedEpochAndSsoSessions(t *testing.T) {
	for _, kind := range []string{"ended", "epoch", "sso"} {
		t.Run(kind, func(t *testing.T) {
			e := newMfaEnv(t)
			e.createMember(t, "pat@example.com", "pw-1")
			_, pair := e.login(t, "pat@example.com", "pw-1")
			access, err := e.validator.Validate(pair.AccessToken)
			require.NoError(t, err)
			refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
			require.NoError(t, err)
			expected := ErrInvalidToken
			switch kind {
			case "ended":
				require.NoError(t, e.m.refreshKV.Delete(sessionKey(refresh.SessionID)))
			case "epoch":
				require.NoError(t, e.db().Model(&iam.Identity{}).Where("email = ?", "pat@example.com").Update("session_epoch", "rotated").Error)
			case "sso":
				rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
				require.NoError(t, err)
				rec.Method = SessionMethodSso
				b, err := json.Marshal(rec)
				require.NoError(t, err)
				_, err = e.m.refreshKV.Put(sessionKey(refresh.SessionID), b)
				require.NoError(t, err)
				expected = ErrStepUpSso
			}
			_, err = e.m.StepUp(context.Background(), access, pair.RefreshToken, "123456", "203.0.113.5")
			require.ErrorIs(t, err, expected)
		})
	}
}

func TestStepUpNeverRedeemsRecoveryAndSpendsRequestBudget(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, codes := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ia, err := e.m.VerifyMfa(ctx, e.challenge(t, "pat@example.com", "pw-1").Challenge, codes[0], "203.0.113.5")
	require.NoError(t, err)
	pair, err := e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)
	before, err := e.store.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	budget := credential.WithRequestBudget(ctx, 1)
	_, err = e.m.StepUp(budget, access, pair.RefreshToken, codes[1], "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	after, err := e.store.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = e.m.StepUp(budget, access, pair.RefreshToken, "123456", "203.0.113.5")
	require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "a refused code must leave the refresh token usable")
}

func TestStepUpRequiresStoredValidRefresh(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	for _, token := range []string{"garbage", pair.AccessToken} {
		_, err = e.m.StepUp(context.Background(), access, token, "123456", "203.0.113.5")
		require.ErrorIs(t, err, ErrInvalidToken)
	}
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	require.NoError(t, e.m.refreshKV.Delete(refresh.ID))
	_, err = e.m.StepUp(context.Background(), access, pair.RefreshToken, "123456", "203.0.113.5")
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestStepUpIsRationedPerSourceAndPreservesRefresh(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	e.m.mfaLimiter = newMfaVerifyLimiter()
	ctx := context.Background()
	var limited int
	for i := 0; i < 20; i++ {
		_, err := e.m.VerifyMfa(ctx, "no-such-challenge", "123456", "198.51.100.7")
		if errors.Is(err, ErrMfaRateLimited) {
			limited++
		} else {
			require.True(t, isCode(err, CodeMfaChallengeExpired), "got %v", err)
		}
	}
	require.Positive(t, limited)
	fresh, err := e.m.StepUp(ctx, access, pair.RefreshToken, "123456", "198.51.100.7")
	require.ErrorIs(t, err, ErrMfaRateLimited)
	require.Nil(t, fresh)
	_, err = e.m.refreshKV.Get(refresh.ID)
	require.NoError(t, err, "a rate-limited step-up must not consume the refresh token")
	_, err = e.m.StepUp(ctx, access, pair.RefreshToken, "123456", "198.51.100.8")
	require.ErrorIs(t, err, ErrMfaNotEnabled, "another source reaches the factor check")
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "a rate-limited step-up must leave the refresh token usable")
}

// The fake KV retains an old session, as a previous step-up TTL reset would.
func TestStepUpAbsoluteLifetimeBeforeEvaluatingCode(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "just under ceiling", true: "past ceiling"}[expired], func(t *testing.T) {
			e := newMfaEnv(t)
			e.createMember(t, "pat@example.com", "pw-1")
			_, pair := e.login(t, "pat@example.com", "pw-1")
			access, err := e.validator.Validate(pair.AccessToken)
			require.NoError(t, err)
			refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
			require.NoError(t, err)
			rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
			require.NoError(t, err)
			e.m.refreshTTL = time.Hour
			now := rec.Anchor.Add(e.m.refreshTTL).Add(-time.Nanosecond)
			if expired {
				now = now.Add(2 * time.Nanosecond)
			}
			e.m.now = func() time.Time { return now }
			data, err := json.Marshal(rec)
			require.NoError(t, err)
			kv := &refreshKVStub{entries: map[string][]byte{
				sessionKey(refresh.SessionID): data,
				refresh.ID:                    []byte(refresh.Username),
			}}
			e.m.refreshKV = kv
			_, err = e.m.StepUp(context.Background(), access, pair.RefreshToken, "123456", "203.0.113.5")
			if expired {
				require.ErrorIs(t, err, ErrInvalidToken)
			} else {
				require.ErrorIs(t, err, ErrMfaNotEnabled, "live session reaches factor lookup")
			}
			require.Equal(t, data, kv.entries[sessionKey(refresh.SessionID)])
			require.Len(t, kv.entries, 2)
			require.Empty(t, kv.deleted)
			require.Empty(t, e.metrics.verifications, "no code was evaluated")
		})
	}
}
