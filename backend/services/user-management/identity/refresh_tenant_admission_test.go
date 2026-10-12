// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The edges of the refresh check on the tenant row's creation time, beyond the recreated
// tenant itself (refresh_tenant_incarnation_test.go): what it does when the row is gone,
// when the token carries no issued-at, when the row cannot be read, and that it is the
// row's CREATION it compares against and nothing a later edit moves.

// makeSuperuser creates an enabled identity holding the system superuser role.
func (e *sessionEnv) makeSuperuser(t *testing.T, email, password string) {
	t.Helper()
	ctx := context.Background()
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: email, Password: password, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, e.store.EnsureRole(ctx, iam.ScopeSystem, iam.SuperuserRoleToken, "Superuser", []string{"*"}))
	_, err = e.admin.SetSystemRoles(ctx, email, []string{iam.SuperuserRoleToken})
	require.NoError(t, err)
}

// A superuser is granted a tenant without a membership, so removing the members does not
// stop its refresh: only the tenant row does. A tenant that no longer exists is refused,
// and the refusal burns the token.
func TestARefreshForATenantThatNoLongerExistsIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	e.makeSuperuser(t, "root@example.com", "pw-root")
	_, pair := e.login(t, "root@example.com", "pw-root")
	_, jti := e.issuedAt(t, pair.RefreshToken)

	require.NoError(t, e.db().Exec("DELETE FROM iam_tenants WHERE token = ?", "acme").Error)

	_, err := e.m.Refresh(context.Background(), pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh was admitted for a tenant that no longer exists")
	e.requireBurned(t, jti)
}

// Every token this service issues carries an issued-at, so one without it cannot be placed
// against the tenant's creation and is refused — on both refresh paths — rather than
// admitted as if it were recent.
func TestARefreshTokenWithNoIssuedAtIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	epoch := e.rowEpoch(t, "pat@example.com")

	forgeNoIat := func(c auth.Claims) (string, string) {
		c.SessionID = e.sidOf(t, c.Email)
		c.ID = uuid.NewString()
		c.Subject = c.Username
		c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, &c)
		tok.Header["kid"] = auth.Thumbprint(&e.key.PublicKey)
		signed, err := tok.SignedString(e.key)
		require.NoError(t, err)
		_, err = e.m.refreshKV.Put(c.ID, []byte(c.Email))
		require.NoError(t, err)
		return signed, c.ID
	}

	plain, jti := forgeNoIat(tenantRefreshClaims("pat@example.com", epoch))
	_, err := e.m.Refresh(context.Background(), plain)
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh token with no issued-at was admitted")
	e.requireBurned(t, jti)

	scoped, jti := forgeNoIat(oauthRefreshClaims("pat@example.com", epoch))
	_, err = e.refreshOAuth(scoped)
	var oe *oauthError
	require.ErrorAs(t, err, &oe, "an OAuth refresh token with no issued-at was admitted")
	require.Equal(t, "invalid_grant", oe.Code)
	e.requireBurned(t, jti)

	// Control: the same claims WITH an issued-at refresh normally, so the refusals above
	// are about the missing claim and nothing else the forged tokens carry.
	ok := e.forge(t, tenantRefreshClaims("pat@example.com", epoch), true)
	_, err = e.m.Refresh(context.Background(), ok)
	require.NoError(t, err)
}

// A database error while reading the tenant row is not a denial: the token is left
// unconsumed and the same token refreshes once the database is back. Burning it would
// sign everyone out over an outage they could not see.
func TestARefreshThatCannotReadTheTenantDoesNotConsumeTheToken(t *testing.T) {
	e := newSessionEnv(t)
	e.makeSuperuser(t, "root@example.com", "pw-root")
	_, pair := e.login(t, "root@example.com", "pw-root")

	const name = "test:fail-tenant-reads"
	require.NoError(t, e.db().Callback().Query().Before("gorm:query").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "iam_tenants" {
			_ = db.AddError(gorm.ErrInvalidDB)
		}
	}))
	_, err := e.m.Refresh(context.Background(), pair.RefreshToken)
	require.ErrorIs(t, err, ErrRefreshUnavailable, "a store error must be reported as retryable")
	require.NoError(t, e.db().Callback().Query().Remove(name))

	_, err = e.m.Refresh(context.Background(), pair.RefreshToken)
	require.NoError(t, err, "the token was consumed by a refresh that could not read the tenant")
}

// The comparison is against when the tenant row was CREATED. An edit to the tenant after a
// sign-in moves its update time past the token's issued-at, and must not refuse the token.
func TestEditingATenantDoesNotRefuseItsRefreshTokens(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	at, _ := e.issuedAt(t, pair.RefreshToken)

	// Into a later second than the token's issued-at, which is all a token can be ordered by.
	if wait := time.Until(at.Truncate(time.Second).Add(time.Second)); wait > 0 {
		time.Sleep(wait + 20*time.Millisecond)
	}
	tenant, err := e.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	tenant.Name = sql.NullString{String: "Acme, renamed", Valid: true}
	require.NoError(t, e.store.UpdateTenant(ctx, tenant))
	edited, err := e.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.True(t, edited.UpdatedAt.Truncate(time.Second).After(at),
		"the fixture did not move the tenant's update time past the token's issued-at")

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "editing a tenant refused a refresh token issued before the edit")
}
