// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newPurgeTestService is newTestService plus the membership tables, because
// DeleteTenant counts memberships before it will cut anything.
func newPurgeTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(
		&iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{}, &iam.Membership{}))
	s := NewService(iam.NewStore(&rdb.RdbManager{Database: db}))
	seedTiers(t, s)
	return s
}

func createTenant(t *testing.T, s *Service, token string) {
	t.Helper()
	_, err := s.CreateTenant(context.Background(), TenantInput{Token: token, TierToken: iam.TierSilverToken})
	require.NoError(t, err)
}

// Deleting a tenant KEEPS THE ROW. That is the whole of ADR-077 slice 1 in one
// assertion, and the reason is not tidiness: the tenant token is the isolation key
// every other functional area writes into its rows (rdb.TenantScoped.TenantId), and
// the old hard delete freed that token for reuse — so the next tenant created at it
// inherited the deleted one's devices, dashboards, telemetry and secrets.
func TestDeleteTenantKeepsTheRowAndStampsAnEpoch(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")

	removed, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	require.True(t, removed, "the delete door was walked through")

	after, err := s.iam.TenantByToken(ctx, "acme")
	require.NoError(t, err, "the row must survive — it IS the token reservation")
	require.Equal(t, iam.PurgePurging, after.PurgeState)
	require.False(t, after.Enabled, "a purging tenant must not be enabled")
	require.NotNil(t, after.PurgeEpoch, "the epoch dates the cut; fences and the deletion record key on it")
}

// Idempotent in both directions, because a teardown that failed partway must be
// retryable: a missing tenant and an already-purging one both report "nothing changed"
// rather than erroring.
func TestDeleteTenantIsIdempotent(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")

	first, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	require.True(t, first)

	second, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err, "a second delete must not error")
	require.False(t, second, "nothing changed the second time")

	missing, err := s.DeleteTenant(ctx, "never-existed")
	require.NoError(t, err)
	require.False(t, missing)
}

// The reservation itself: once a token has been through the delete door, nothing may
// create a tenant at it again. This is the assertion that closes the disclosure path —
// with no successor tenant, there is nothing for the residue to be handed to.
func TestCreateTenantRefusesAReservedToken(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	_, err = s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierSilverToken})
	require.ErrorIs(t, err, ErrTenantTokenReserved)

	// The negative control: an unrelated token is unaffected, so the refusal is about
	// this token's history and not a service that has stopped creating tenants.
	_, err = s.CreateTenant(ctx, TenantInput{Token: "acme-2", TierToken: iam.TierSilverToken})
	require.NoError(t, err)
}

// 🔴 The refusal must not read like an "already exists".
//
// dcctl's sim flow wraps createTenant in tolerateExists (backend/cli/sim/admin.go),
// which swallows any error whose text contains "already exists", "duplicate" or
// "unique" so that re-running `sim create` is idempotent. A reservation refusal
// phrased with any of those words would be silently treated as success by the caller
// most likely to hit it — dcctl would report ✅, having minted an identity and a
// membership against a tenant nobody can enter, and the sim would then fail at login
// with nothing pointing back here. (Not a disclosure: resolveTenantGrant refuses a
// deleted tenant, so the layer below still holds. A silent, misattributed break.)
//
// The coupling is real and cross-module, so it is asserted on both sides; the dcctl
// half lives in backend/cli/sim/admin_test.go.
func TestTenantTokenReservedIsNotMistakenForAnAlreadyExistsError(t *testing.T) {
	msg := strings.ToLower(ErrTenantTokenReserved.Error())
	for _, tolerated := range []string{"already exists", "duplicate", "unique"} {
		require.NotContainsf(t, msg, tolerated,
			"the reservation refusal contains %q, which dcctl's tolerateExists swallows as success", tolerated)
	}
	// And it has to actually say something, or the check above passes over an empty
	// string and proves nothing.
	require.Contains(t, msg, "reserved")
}

// An ACTIVE tenant at the same token still collides the old way, and that is
// deliberate: callers rely on tolerating a duplicate-key error to make create
// idempotent, so the reservation must not swallow the ordinary re-create path.
func TestCreateTenantStillCollidesOnAnActiveToken(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")

	_, err := s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierSilverToken})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTenantTokenReserved,
		"an active duplicate is an already-exists, not a reservation")
}

// A deleted tenant takes no new members. Without this, DeleteTenant's zero-membership
// precondition holds only at the instant of the cut: an operator could attach a user
// afterwards, get a success toast, and hand them a tenant that answers every login
// attempt with "invalid credentials".
func TestAddMembershipRefusesADeletedTenant(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.CreateIdentity(ctx, CreateIdentityInput{Email: "someone@example.com", Password: "hunter2hunter2", Enabled: true})
	require.NoError(t, err)
	_, err = s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	_, err = s.AddMembership(ctx, "someone@example.com", "acme", nil)
	require.ErrorIs(t, err, ErrTenantDeleted)
}

// A deleted tenant must not pin its tier forever. The row keeps its NOT NULL tier_id
// (it survives to reserve the token), so counting tombstones would refuse every future
// tier deletion with "move them to another tier first" — naming tenants an operator
// can neither see nor move.
func TestDeletedTenantsDoNotBlockTierDeletion(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	removed, err := s.DeleteTenantTier(ctx, iam.TierSilverToken)
	require.NoError(t, err, "a tier whose only tenant was deleted must be removable")
	require.True(t, removed)

	// The negative control: a LIVE tenant still pins its tier, so the exclusion above
	// narrowed the count rather than disabling the guard.
	createTenant2 := func(token, tier string) {
		_, err := s.CreateTenant(ctx, TenantInput{Token: token, TierToken: tier})
		require.NoError(t, err)
	}
	createTenant2("live-one", iam.TierGoldToken)
	_, err = s.DeleteTenantTier(ctx, iam.TierGoldToken)
	require.ErrorIs(t, err, ErrTierInUse)
}
