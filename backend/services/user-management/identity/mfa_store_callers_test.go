// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// The MFA store has its own tests in iam. These go in through the FRONT DOORS that call it —
// the Manager's ClearPassword, the invitation gate, the admin Service's DeleteIdentity — because a
// store method that is tested and never called, or called with the wrong store, protects nothing.

var factorSeed = []byte("12345678901234567890")

// enrollFactor gives the identity an active factor whose seed is sealed in store.
func enrollFactor(t *testing.T, s *iam.Store, store secrets.SecretStore, identityID uint) *iam.MfaFactor {
	t.Helper()
	ctx := context.Background()
	f, err := s.BeginMfaEnrollment(ctx, store, identityID, factorSeed)
	require.NoError(t, err)
	require.NoError(t, s.ConfirmMfaEnrollment(ctx, f.ID, 7, []string{"digest-1", "digest-2"}, time.Now()))
	return f
}

func sealed(t *testing.T, store secrets.SecretStore, f *iam.MfaFactor) bool {
	t.Helper()
	ok, err := store.Exists(context.Background(), iam.MfaSeedRef(f.ID))
	require.NoError(t, err)
	return ok
}

// The second factor protects only password sign-ins, so clearing the password removes it, seed
// included, in the manager's own store.
func TestClearPasswordRemovesTheSecondFactorAndItsSealedSeed(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	_, err := e.admin.SetPassword(ctx, linkedEmail, "pw-pat")
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, linkedEmail)
	require.NoError(t, err)
	f := enrollFactor(t, e.store, e.m.secrets, id.ID)
	require.True(t, sealed(t, e.m.secrets, f))

	_, err = e.m.ClearPassword(ctx, linkedEmail)
	require.NoError(t, err)

	require.False(t, sealed(t, e.m.secrets, f), "clearPassword left the sealed seed behind")
	has, err := e.store.HasMfaFactor(ctx, id.ID)
	require.NoError(t, err)
	require.False(t, has, "clearPassword left the factor row behind")
	left, err := e.store.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	require.Zero(t, left, "clearPassword left recovery codes behind")
}

// Deleting through the admin service must reach the seed, which needs the store main wires into
// the service. Without it the delete is refused, loudly, with the identity still there.
func TestAdminDeleteIdentityRemovesTheSealedSeedAndRefusesWithoutTheStore(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	store := sealedStore(t, e)
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "pat@example.com", Password: "pw", Enabled: true})
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)
	f := enrollFactor(t, e.store, store, id.ID)

	// The service as built without SetSecretStore.
	removed, err := e.admin.DeleteIdentity(ctx, "pat@example.com")
	require.ErrorIs(t, err, iam.ErrMfaNeedsSecretStore)
	require.False(t, removed)
	_, err = e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err, "a refused delete removed the identity")
	require.True(t, sealed(t, store, f))

	// The service as main builds it.
	e.admin.SetSecretStore(store)
	removed, err = e.admin.DeleteIdentity(ctx, "pat@example.com")
	require.NoError(t, err)
	require.True(t, removed)
	require.False(t, sealed(t, store, f), "deleting the identity left its sealed seed behind")
}

func sealedStore(t *testing.T, e *sessionEnv) secrets.SecretStore {
	t.Helper()
	sys := e.db().WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(sys))
	store, err := secrets.NewFromConfig(context.Background(), rootKeyConfig(t), e.db())
	require.NoError(t, err)
	return store
}

// T19: a factor can only be enrolled from a session, so an identity holding one has been used,
// and an invitation must not set its password. The twin is the same flow without the factor.
func TestAnIdentityWithAFactorRowIsNeverTreatedAsNeverUsed(t *testing.T) {
	ctx := context.Background()

	// Twin: a new person with no factor accepts and signs in.
	e, boss := inviteEnv(t)
	_, code, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "twin@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, code, ptr("twin-pw"), nil)
	require.NoError(t, err, "the control: a person with no factor must be able to accept")

	// Same flow, with a factor row on the pending identity.
	_, code2, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "held@example.com"})
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, "held@example.com")
	require.NoError(t, err)
	store := sealedStore(t, e)
	enrollFactor(t, e.store, store, id.ID)

	_, err = e.m.AcceptInvitation(ctx, code2, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "an invitation set the password of an identity holding a second factor: %v", err)
	after, err := e.store.IdentityByEmail(ctx, "held@example.com")
	require.NoError(t, err)
	require.Empty(t, after.PasswordHash, "the password of an identity with a factor was set")
}
