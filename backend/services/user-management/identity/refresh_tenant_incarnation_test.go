// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// A tenant token is released when a deletion completes, and a new tenant may then be
// created with it. A refresh token names its tenant by token alone, so one issued to the
// earlier tenant must not be redeemable against the later one — whoever holds it, and
// whether or not the same person is a member of the new tenant. A refresh is admitted only
// for the tenant row that existed when the token was issued.
//
// These run on the real paths (sessionEnv: SQLite store, real RSA issuer and validator,
// refresh tokens in a real JetStream KV bucket).

// recreateTenant stands in for a completed deletion followed by a new tenant at the same
// token: the members are removed (a deletion requires that), the row is removed (as
// completion removes it), and a new row is created in a LATER SECOND than issuedAt, then
// the given members are added to it.
//
// The wait is to the next whole second because a token's issued-at is carried at
// one-second precision: a row created in the same second as a token cannot be ordered
// against it from the token alone.
func (e *sessionEnv) recreateTenant(t *testing.T, token string, issuedAt time.Time, members ...string) {
	t.Helper()
	ctx := context.Background()
	for _, email := range members {
		_, err := e.admin.RemoveMembership(ctx, email, token)
		require.NoError(t, err)
	}
	old, err := e.store.TenantByToken(ctx, token)
	require.NoError(t, err)
	require.NoError(t, e.db().Exec("DELETE FROM iam_tenants WHERE token = ?", token).Error)

	if wait := time.Until(issuedAt.Truncate(time.Second).Add(time.Second)); wait > 0 {
		time.Sleep(wait + 20*time.Millisecond)
	}
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{
		Token: token, Enabled: true, TierID: old.TierID, PurgeState: iam.PurgeActive,
	}))
	for _, email := range members {
		_, err := e.admin.AddMembership(ctx, email, token, nil)
		require.NoError(t, err)
	}
}

// issuedAt reads a refresh token's issued-at and jti back through the validator.
func (e *sessionEnv) issuedAt(t *testing.T, refreshToken string) (time.Time, string) {
	t.Helper()
	claims, err := e.validator.ValidateRefresh(refreshToken)
	require.NoError(t, err)
	require.NotNil(t, claims.IssuedAt)
	return claims.IssuedAt.Time, claims.ID
}

// requireBurned asserts the refresh token's jti is gone from the store, i.e. the refusal
// consumed it rather than leaving it to be retried.
func (e *sessionEnv) requireBurned(t *testing.T, jti string) {
	t.Helper()
	_, err := e.m.refreshKV.Get(jti)
	require.True(t, errors.Is(err, nats.ErrKeyNotFound),
		"a refused refresh must burn its token, got %v", err)
}

func TestARefreshIssuedForAnEarlierTenantAtTheSameTokenIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	at, jti := e.issuedAt(t, pair.RefreshToken)

	e.recreateTenant(t, "acme", at, "pat@example.com")

	_, err := e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken,
		"a refresh token issued for the earlier tenant was redeemed against the new one")
	e.requireBurned(t, jti)

	// Control: the same person, signed in to the new tenant, refreshes normally — so the
	// refusal above is about when the token was issued, not about the person or tenant.
	_, fresh := e.login(t, "pat@example.com", "pw-1")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err, "a refresh token issued after the tenant was created must still work")
}

// A superuser is admitted to any tenant without a membership, on a path that never reads
// the tenant row. The issued-at check must hold there too.
func TestASuperusersRefreshForAnEarlierTenantAtTheSameTokenIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{
		Email: "root@example.com", Password: "pw-root", Enabled: true})
	require.NoError(t, err)
	require.NoError(t, e.store.EnsureRole(ctx, iam.ScopeSystem, iam.SuperuserRoleToken, "Superuser", []string{"*"}))
	_, err = e.admin.SetSystemRoles(ctx, "root@example.com", []string{iam.SuperuserRoleToken})
	require.NoError(t, err)

	_, pair := e.login(t, "root@example.com", "pw-root")
	at, jti := e.issuedAt(t, pair.RefreshToken)

	e.recreateTenant(t, "acme", at)

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken,
		"a superuser's refresh token issued for the earlier tenant was redeemed against the new one")
	e.requireBurned(t, jti)

	_, fresh := e.login(t, "root@example.com", "pw-root")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err, "a superuser's refresh issued after the tenant was created must still work")
}

// The OAuth refresh grant (the one MCP clients hold) is refused the same way, as
// invalid_grant, and burns the token.
func TestAnOAuthRefreshIssuedForAnEarlierTenantAtTheSameTokenIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, _ := e.login(t, "pat@example.com", "pw-1")
	toks := e.oauthSession(t, idTok)
	at, jti := e.issuedAt(t, toks.RefreshToken)

	e.recreateTenant(t, "acme", at, "pat@example.com")

	_, err := e.refreshOAuth(toks.RefreshToken)
	var oe *oauthError
	require.True(t, errors.As(err, &oe), "want an OAuth error, got %v", err)
	require.Equal(t, "invalid_grant", oe.Code,
		"an OAuth refresh token issued for the earlier tenant was redeemed against the new one")
	e.requireBurned(t, jti)

	idTok, _ = e.login(t, "pat@example.com", "pw-1")
	fresh := e.oauthSession(t, idTok)
	_, err = e.refreshOAuth(fresh.RefreshToken)
	require.NoError(t, err, "an OAuth refresh issued after the tenant was created must still work")
}

// The rule at the boundary, without a clock: a token issued in the SAME second as the
// tenant row is admitted (a token's issued-at has one-second precision, so it cannot be
// ordered within that second, and a sign-in moments after creation is ordinary), one
// issued in an earlier second is not.
func TestIssuedBeforeTenantRoundsToTheTokensPrecision(t *testing.T) {
	created := time.Date(2026, 10, 12, 9, 30, 15, 700_000_000, time.UTC)
	require.False(t, issuedBeforeTenant(created.Truncate(time.Second), created),
		"a token issued in the creation second was refused")
	require.False(t, issuedBeforeTenant(created.Add(time.Second), created))
	require.True(t, issuedBeforeTenant(created.Truncate(time.Second).Add(-time.Nanosecond), created),
		"a token issued in the second before creation was admitted")
	require.True(t, issuedBeforeTenant(created.Add(-time.Hour), created))
}
