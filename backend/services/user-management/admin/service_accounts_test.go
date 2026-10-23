// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// A tenant's service accounts, driven through the service with the caller taken from
// verified claims exactly as the resolvers leave it.

func saEnv(t *testing.T) (*planeEnv, *credential.DeviceSecretKey) {
	t.Helper()
	e := newPlaneEnv(t)
	k, err := credential.DeriveServiceAccountSecretKey([]byte("admin-service-account-test-key!!"))
	require.NoError(t, err)
	e.s.SetServiceAccountSecretKey(k)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "boss@globex.example", "globex", []string{iam.TenantAdminRoleToken})
	return e, k
}

func boss() context.Context { return as("boss@acme.example", "acme", "*") }

func TestCreateServiceAccountStampsTheTenantAndShowsTheSecretOnce(t *testing.T) {
	e, k := saEnv(t)
	acct, secret, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{
		Token: "ci-bot", Name: "CI", RoleTokens: []string{"operator"},
	})
	require.NoError(t, err)
	require.Equal(t, "acme", acct.TenantId, "the account must belong to the caller's tenant")
	require.True(t, strings.HasPrefix(secret, ServiceAccountSecretPrefix))
	require.True(t, strings.HasPrefix(acct.ClientId, ServiceAccountClientIdPrefix))
	require.Equal(t, "boss@acme.example", acct.CreatedBy)
	require.Len(t, acct.Secrets, 1)

	// The row holds a digest that verifies the secret, bound to the tenant — and nothing
	// that IS the secret.
	row := acct.Secrets[0]
	require.NoError(t, credential.VerifyDeviceSecret(k, "acme", row.Digest, secret))
	require.Error(t, credential.VerifyDeviceSecret(k, "globex", row.Digest, secret), "the digest is not bound to the tenant")
	require.NotContains(t, row.Digest, secret)
	require.Equal(t, secret[len(secret)-4:], row.Hint)
	require.WithinDuration(t, time.Now().Add(DefaultSecretLifetimeDays*24*time.Hour), row.ExpiresAt, time.Minute)

	// Negative control on "never readable again": no column of any stored row holds it.
	var dump []map[string]any
	require.NoError(t, e.db.Raw(`SELECT * FROM iam_service_accounts`).Scan(&dump).Error)
	var secrets []map[string]any
	require.NoError(t, e.db.Raw(`SELECT * FROM iam_service_account_secrets`).Scan(&secrets).Error)
	for _, r := range append(dump, secrets...) {
		for col, v := range r {
			if s, ok := v.(string); ok {
				require.NotContainsf(t, s, secret[len(ServiceAccountSecretPrefix):], "column %s holds the secret", col)
			}
		}
	}
	read, err := e.s.ServiceAccount(boss(), "ci-bot")
	require.NoError(t, err)
	require.Equal(t, acct.ClientId, read.ClientId)

	// Another tenant cannot see or touch it.
	_, err = e.s.ServiceAccount(as("boss@globex.example", "globex", "*"), "ci-bot")
	require.ErrorIs(t, err, ErrServiceAccountNotFound)
	deleted, err := e.s.DeleteServiceAccount(as("boss@globex.example", "globex", "*"), "ci-bot")
	require.NoError(t, err)
	require.False(t, deleted)
	_, err = e.s.ServiceAccount(boss(), "ci-bot")
	require.NoError(t, err, "another tenant's delete removed this tenant's account")

	_, _, err = e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ci-bot"})
	require.ErrorIs(t, err, ErrServiceAccountTokenTaken)
}

// No-escalation: an administrator cannot hand an account an authority it does not hold,
// nor act on an account that holds one.
func TestServiceAccountEscalationIsRefused(t *testing.T) {
	e, _ := saEnv(t)
	e.member(t, "deputy@acme.example", "acme", []string{"member-admin"})
	deputy := as("deputy@acme.example", "acme", "member:read", "member:write", "role:read")

	_, _, err := e.s.CreateServiceAccount(deputy, ServiceAccountInput{Token: "x", RoleTokens: []string{"operator"}})
	require.Equal(t, CodeEscalation, refusalCode(err), "deputy granted operator, which it does not hold: %v", err)
	_, _, err = e.s.CreateServiceAccount(deputy, ServiceAccountInput{Token: "x", RoleTokens: []string{iam.TenantAdminRoleToken}})
	require.Equal(t, CodeEscalation, refusalCode(err), "deputy granted *: %v", err)

	// The control: what it does hold, it may grant.
	_, _, err = e.s.CreateServiceAccount(deputy, ServiceAccountInput{Token: "reader", RoleTokens: []string{"member-admin"}})
	require.NoError(t, err)

	_, _, err = e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ops", RoleTokens: []string{"operator"}})
	require.NoError(t, err)
	_, _, err = e.s.RotateServiceAccountSecret(deputy, "ops", nil, nil)
	require.Equal(t, CodeEscalation, refusalCode(err), "deputy took over an account above it by rotating: %v", err)
	_, err = e.s.SetServiceAccountEnabled(deputy, "ops", false)
	require.Equal(t, CodeEscalation, refusalCode(err))
	_, err = e.s.DeleteServiceAccount(deputy, "ops")
	require.Equal(t, CodeEscalation, refusalCode(err))
	_, err = e.s.SetServiceAccountAccess(deputy, "reader", []string{"operator"}, nil)
	require.Equal(t, CodeEscalation, refusalCode(err), "deputy widened an account past itself")

	// A scoped caller is refused outright.
	e.member(t, "cust@acme.example", "acme", []string{iam.TenantAdminRoleToken}, "acme-corp")
	scoped := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "cust@acme.example",
		Authorities: []string{"*"}, ScopeAnchors: []string{"customer:acme-corp"},
	})
	_, _, err = e.s.CreateServiceAccount(scoped, ServiceAccountInput{Token: "y"})
	require.ErrorIs(t, err, auth.ErrScopeUnsupported)
}

// A service account cannot administer the tenant plane: its subject names no person.
func TestAServiceAccountCannotAdministerAccounts(t *testing.T) {
	e, _ := saEnv(t)
	acct, _, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "admin-bot", RoleTokens: []string{iam.TenantAdminRoleToken}})
	require.NoError(t, err)
	bot := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: auth.ServiceAccountSubject(acct.Token),
		Authorities: []string{"*"}, ClientId: acct.ClientId,
	})
	_, _, err = e.s.CreateServiceAccount(bot, ServiceAccountInput{Token: "child"})
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, _, err = e.s.RotateServiceAccountSecret(bot, "admin-bot", nil, nil)
	require.ErrorIs(t, err, auth.ErrForbidden)

	// Nor members, invitations or roles: every change on this plane resolves the caller
	// as a person, and an account's subject names none.
	_, err = e.s.SetMemberRoles(bot, "boss@acme.example", []string{"operator"})
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, _, err = e.s.InviteMember(bot, InviteInput{Email: "new@acme.example", RoleTokens: []string{"operator"}})
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, err = e.s.CreateTenantRole(bot, TenantRoleInput{Token: "bot-made", Authorities: []string{"device:read"}})
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, err = e.s.DeleteTenantRole(bot, "operator")
	require.ErrorIs(t, err, auth.ErrForbidden)
}

func TestRotationOverlapsThenEndsTheOldSecret(t *testing.T) {
	e, k := saEnv(t)
	_, first, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ci"})
	require.NoError(t, err)
	six := int32(6)
	acct, second, err := e.s.RotateServiceAccountSecret(boss(), "ci", &six, nil)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.Len(t, acct.Secrets, 2)
	newest, old := acct.Secrets[0], acct.Secrets[1]
	require.NoError(t, credential.VerifyDeviceSecret(k, "acme", newest.Digest, second))
	require.NoError(t, credential.VerifyDeviceSecret(k, "acme", old.Digest, first))
	require.WithinDuration(t, time.Now().Add(6*time.Hour), old.ExpiresAt, time.Minute, "the old secret keeps the overlap")
	require.True(t, old.Live(time.Now()))
	require.False(t, old.Live(time.Now().Add(6*time.Hour+time.Minute)), "the old secret outlived its overlap")

	// A second rotation during the overlap: the one that just took over keeps an overlap,
	// the oldest ends now — never more than two live.
	acct, _, err = e.s.RotateServiceAccountSecret(boss(), "ci", &six, nil)
	require.NoError(t, err)
	live := 0
	for _, s := range acct.Secrets {
		if s.Live(time.Now()) {
			live++
		}
	}
	require.Equal(t, 2, live)
	require.False(t, acct.Secrets[2].Live(time.Now()), "the oldest secret survived a second rotation")

	// Overlap 0 ends the replaced secret at once.
	zero := int32(0)
	acct, _, err = e.s.RotateServiceAccountSecret(boss(), "ci", &zero, nil)
	require.NoError(t, err)
	live = 0
	for _, s := range acct.Secrets {
		if s.Live(time.Now()) {
			live++
		}
	}
	require.Equal(t, 1, live)

	for _, bad := range []int32{-1, MaxRotationOverlapHours + 1} {
		bad := bad
		_, _, err = e.s.RotateServiceAccountSecret(boss(), "ci", &bad, nil)
		require.Error(t, err)
	}
	for _, bad := range []int32{0, MaxSecretLifetimeDays + 1} {
		bad := bad
		_, _, err = e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "z", ExpiresInDays: &bad})
		require.Error(t, err)
	}
}

func TestATenantRoleHeldByAServiceAccountCannotBeDeleted(t *testing.T) {
	e, _ := saEnv(t)
	_, err := e.s.CreateTenantRole(boss(), TenantRoleInput{Token: "integrator", Authorities: []string{"device:read"}})
	require.NoError(t, err)
	_, _, err = e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ci", RoleTokens: []string{"integrator"}})
	require.NoError(t, err)
	_, err = e.s.DeleteTenantRole(boss(), "integrator")
	require.ErrorIs(t, err, ErrRoleInUse)

	deleted, err := e.s.DeleteServiceAccount(boss(), "ci")
	require.NoError(t, err)
	require.True(t, deleted)
	var n int64
	require.NoError(t, e.db.Raw(`SELECT COUNT(*) FROM iam_service_account_secrets`).Scan(&n).Error)
	require.Zero(t, n, "a deleted account left its secrets behind")
	ok, err := e.s.DeleteTenantRole(boss(), "integrator")
	require.NoError(t, err)
	require.True(t, ok, "the role stayed in use after its only holder was deleted")
}

func TestAMintWithoutTheKeyFailsLoudly(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	_, _, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ci"})
	require.True(t, errors.Is(err, errNoServiceAccountKey), "got %v", err)
}

func TestServiceAccountListIsTheTenantsOwn(t *testing.T) {
	e, _ := saEnv(t)
	for _, tok := range []string{"b", "a"} {
		_, _, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: tok})
		require.NoError(t, err)
	}
	_, _, err := e.s.CreateServiceAccount(as("boss@globex.example", "globex", "*"), ServiceAccountInput{Token: "g"})
	require.NoError(t, err)
	rows, pag, err := e.s.ServiceAccounts(boss(), rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "a", rows[0].Token)
	require.EqualValues(t, 2, pag.TotalRecords)
}
