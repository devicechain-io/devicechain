// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestService spins up the admin Service over an in-memory sqlite database with
// the core callbacks registered, as production does. It exercises the real store,
// so the tier invariants below are pinned end-to-end through the service rather
// than against a stub.
//
// The SQLite FK is deliberately NOT what these tests measure: the tier tests below
// pin the SERVICE-level guards, which are what turn a constraint into an error an
// operator can act on. Postgres's RESTRICT is the backstop underneath them, and it
// is exercised on a live cluster, not here.
func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&iam.TenantTier{}, &iam.Tenant{}))
	return NewService(iam.NewStore(&rdb.RdbManager{Database: db}))
}

// seedTiers installs the gold/silver/bronze vocabulary the migration seeds.
func seedTiers(t *testing.T, s *Service) {
	t.Helper()
	for _, token := range []string{iam.TierGoldToken, iam.TierSilverToken, iam.TierBronzeToken} {
		_, err := s.CreateTenantTier(context.Background(), TierInput{Token: token})
		require.NoError(t, err)
	}
}

// TestCreateTenantRequiresAKnownTier pins ADR-065 decision 3 at the API: a tenant
// cannot be created without naming a real tier. This is what makes "there is no
// unset state" true in practice — the NOT NULL FK guarantees the row can't be
// written un-tiered, and this guarantees the caller gets told why rather than a
// constraint violation naming a column.
func TestCreateTenantRequiresAKnownTier(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	seedTiers(t, s)

	// Omitted: refused. Notably NOT silently defaulted to a tier — defaulting is
	// how the unset state sneaks back in.
	_, err := s.CreateTenant(ctx, TenantInput{Token: "acme"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tierToken is required")

	// Named but unknown (a typo, a tier an operator deleted): refused, and the
	// error names the tier the caller actually asked for.
	_, err = s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: "platinum"})
	require.ErrorIs(t, err, ErrTierNotFound)
	require.Contains(t, err.Error(), "platinum")

	// Neither refusal created anything.
	tenants, err := s.ListTenants(ctx)
	require.NoError(t, err)
	require.Empty(t, tenants)

	// A real tier: created, and it reads back carrying that tier.
	created, err := s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierGoldToken})
	require.NoError(t, err)
	require.NotNil(t, created.Tier)
	require.Equal(t, iam.TierGoldToken, created.Tier.Token)
}

// TestUpdateTenantRetiersLive pins ADR-065 decision 14 at the API: re-tiering is a
// normal, live update — no drain, no phase, no downtime. It is also refused for an
// unknown tier, so a typo cannot quietly leave the tenant where it was while the
// caller believes it moved.
func TestUpdateTenantRetiersLive(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	seedTiers(t, s)

	_, err := s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierBronzeToken})
	require.NoError(t, err)

	// The upgrade path — a customer paying more money.
	up, err := s.UpdateTenant(ctx, "acme", TenantMutableInput{TierToken: iam.TierGoldToken})
	require.NoError(t, err)
	require.Equal(t, iam.TierGoldToken, up.Tier.Token)

	// An unknown tier is refused, and the tenant keeps the tier it had.
	_, err = s.UpdateTenant(ctx, "acme", TenantMutableInput{TierToken: "platinum"})
	require.ErrorIs(t, err, ErrTierNotFound)
	still, err := s.ListTenants(ctx)
	require.NoError(t, err)
	require.Equal(t, iam.TierGoldToken, still[0].Tier.Token)
}

// TestDeleteTenantTierRefusedWhileInUse pins ADR-065 decision 9 (the ADR-044
// ErrEntityInUse pattern): a tier with tenants at it cannot be deleted. Deleting it
// would strand every tenant that references it — their tier is a required FK, so
// there is no coherent state on the far side.
func TestDeleteTenantTierRefusedWhileInUse(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	seedTiers(t, s)

	_, err := s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierBronzeToken})
	require.NoError(t, err)

	// Refused while in use — and the error says how many tenants are in the way, so
	// an operator knows the size of the job rather than just that it failed.
	removed, err := s.DeleteTenantTier(ctx, iam.TierBronzeToken)
	require.ErrorIs(t, err, ErrTierInUse)
	require.False(t, removed)
	require.Contains(t, err.Error(), "1 tenant(s)")

	// The tier is still there — a refused delete is not a partial one.
	_, err = s.loadTier(ctx, iam.TierBronzeToken)
	require.NoError(t, err)

	// An unused tier deletes cleanly: the guard is about references, not about
	// protecting the seeded vocabulary (packaging is the operator's to define).
	removed, err = s.DeleteTenantTier(ctx, iam.TierSilverToken)
	require.NoError(t, err)
	require.True(t, removed)

	// Move the tenant off bronze, and bronze becomes deletable.
	_, err = s.UpdateTenant(ctx, "acme", TenantMutableInput{TierToken: iam.TierGoldToken})
	require.NoError(t, err)
	removed, err = s.DeleteTenantTier(ctx, iam.TierBronzeToken)
	require.NoError(t, err)
	require.True(t, removed)

	// Idempotent: deleting an already-gone tier is not an error.
	removed, err = s.DeleteTenantTier(ctx, iam.TierBronzeToken)
	require.NoError(t, err)
	require.False(t, removed)
}

// TestTierConfigIsValidatedAtTheDoor pins ADR-065 D8's actual invariant: unknown
// keys are rejected AT WRITE. The registry's unit tests only exercise
// ValidateTierConfig as a pure function, which says nothing about whether anything
// CALLS it — delete both call sites and those tests stay green while the blob is
// fail-open again. This drives the real doors instead.
//
// Fail-open is the whole point of the registry: an unknown key is accepted, read by
// nobody, and does nothing, while an operator believes they sold and configured a
// ceiling.
func TestTierConfigIsValidatedAtTheDoor(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	// Create: a plausible typo of a real key must not reach the database.
	_, err := s.CreateTenantTier(ctx, TierInput{
		Token:  "platinum",
		Config: map[string]any{"ingestMessagesPerSec": float64(500)},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tier setting")

	// ...and the refusal is total: no tier was created.
	tiers, err := s.ListTenantTiers(ctx)
	require.NoError(t, err)
	require.Empty(t, tiers)

	// Create: an unusable value on a KNOWN key is refused too — a zero ceiling
	// admits nothing, which is an outage for every tenant at the tier.
	_, err = s.CreateTenantTier(ctx, TierInput{
		Token:  "platinum",
		Config: map[string]any{"ingestMessagesPerSecond": float64(0)},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be positive")

	// Update: the same bar applies to the edit door.
	seedTiers(t, s)
	_, err = s.UpdateTenantTier(ctx, iam.TierGoldToken, TierMutableInput{
		Name:   "Gold",
		Config: &map[string]any{"ingestBurst": float64(0)},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be positive")

	_, err = s.UpdateTenantTier(ctx, iam.TierGoldToken, TierMutableInput{
		Name:   "Gold",
		Config: &map[string]any{"shedPriority": float64(80)},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tier setting")

	// A valid config passes both doors and lands.
	got, err := s.UpdateTenantTier(ctx, iam.TierGoldToken, TierMutableInput{
		Name:   "Gold",
		Config: &map[string]any{"ingestMessagesPerSecond": float64(2000)},
	})
	require.NoError(t, err)
	require.Equal(t, float64(2000), got.Config["ingestMessagesPerSecond"])
}

// TestRenamingATierDoesNotWipeItsPackaging is the guard for a silent, tier-wide
// re-pricing.
//
// If config were a full replace like name/description, then
// `updateTenantTier(token:"gold", request:{name:"Gold Plus"})` — a mutation that
// states ONLY a rename — would NULL gold's settings. Every gold tenant would drop
// from 2000/s to the platform default within core/governance's 60s TTL: no error, no
// log, live, and total loss of the thing tiers exist to carry. The direction is
// fail-safe (the default, never unlimited), which is the only reason it would be
// survivable rather than an outage.
//
// So config is a patch: nil leaves it alone, and an explicit empty map clears it.
func TestRenamingATierDoesNotWipeItsPackaging(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.CreateTenantTier(ctx, TierInput{
		Token:  iam.TierGoldToken,
		Name:   "Gold",
		Config: map[string]any{"ingestMessagesPerSecond": float64(2000)},
	})
	require.NoError(t, err)

	// A rename that says nothing about config must leave the packaging intact.
	renamed, err := s.UpdateTenantTier(ctx, iam.TierGoldToken, TierMutableInput{Name: "Gold Plus"})
	require.NoError(t, err)
	require.Equal(t, "Gold Plus", renamed.Name.String)
	require.Equal(t, float64(2000), renamed.Config["ingestMessagesPerSecond"],
		"a name-only update must not clear the tier's ceilings")

	// It survives the round-trip too, not just the returned struct.
	reloaded, err := s.loadTier(ctx, iam.TierGoldToken)
	require.NoError(t, err)
	require.Equal(t, float64(2000), reloaded.Config["ingestMessagesPerSecond"])

	// Clearing stays possible — it just has to be said out loud.
	cleared, err := s.UpdateTenantTier(ctx, iam.TierGoldToken, TierMutableInput{
		Name:   "Gold Plus",
		Config: &map[string]any{},
	})
	require.NoError(t, err)
	require.Empty(t, cleared.Config, "an explicit empty config clears the tier's settings")
}

// TestUpdateTenantTierEditsPackaging pins decision 4: the seeded vocabulary is
// operator-editable — what "bronze includes" is a product decision, so it must be
// changeable without a deploy, for every tenant at that tier at once.
func TestUpdateTenantTierEditsPackaging(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	seedTiers(t, s)

	updated, err := s.UpdateTenantTier(ctx, iam.TierBronzeToken, TierMutableInput{
		Name:        "Starter",
		Description: "Renamed packaging",
	})
	require.NoError(t, err)
	require.Equal(t, "Starter", updated.Name.String)
	require.Equal(t, iam.TierBronzeToken, updated.Token, "the token is the tier's identity and is fixed")

	// Editing an unknown tier is a legible not-found, not a silent create.
	_, err = s.UpdateTenantTier(ctx, "platinum", TierMutableInput{Name: "Platinum"})
	require.ErrorIs(t, err, ErrTierNotFound)
}
