// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedTier creates a tier and returns it — the test-side stand-in for the seeds the
// ADR-065 migration installs.
func seedTier(t *testing.T, s *Store, token string) *TenantTier {
	t.Helper()
	tier := &TenantTier{Token: token}
	require.NoError(t, s.CreateTenantTier(context.Background(), tier))
	return tier
}

// TestUpdateTenantRoundTripsTier pins the write-side of a live tier change (ADR-065
// decision 14): re-tiering a tenant through UpdateTenant must actually persist.
//
// UpdateTenant writes through a column ALLOWLIST rather than a full-row Save, so a
// TierID missing from that Select would leave the update silently dropped — the
// caller sees success, the tenant keeps its old packaging, and it stays on ceilings
// and entitlements someone believes they changed. That is the same fail-open shape
// the AI-consent revoke hit; this is its guard for the tier.
func TestUpdateTenantRoundTripsTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	bronze := seedTier(t, s, TierBronzeToken)
	gold := seedTier(t, s, TierGoldToken)

	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme", TierID: bronze.ID}))

	reload := func() *Tenant {
		got, err := s.TenantByToken(ctx, "acme")
		require.NoError(t, err)
		return got
	}

	// The tier the tenant was created at is the tier it reads back at, preloaded.
	created := reload()
	require.Equal(t, bronze.ID, created.TierID)
	require.NotNil(t, created.Tier, "TenantByToken must preload the tier")
	require.Equal(t, TierBronzeToken, created.Tier.Token)

	// Upgrade to gold — the paying-customer case, and the one that must not
	// silently no-op.
	up := reload()
	up.TierID = gold.ID
	require.NoError(t, s.UpdateTenant(ctx, up))
	require.Equal(t, gold.ID, reload().TierID, "re-tiering must persist through UpdateTenant's Select allowlist")
	require.Equal(t, TierGoldToken, reload().Tier.Token)

	// And back down again: the change is not one-way.
	down := reload()
	down.TierID = bronze.ID
	require.NoError(t, s.UpdateTenant(ctx, down))
	require.Equal(t, bronze.ID, reload().TierID)
}

// TestUpdateTenantPreservesTierWhenOtherFieldsChange guards the other half of the
// allowlist: an update that does NOT intend to re-tier must leave the tier alone.
// A tier is a commercial fact — editing a tenant's rate limit must never move it.
func TestUpdateTenantPreservesTierWhenOtherFieldsChange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gold := seedTier(t, s, TierGoldToken)
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme", TierID: gold.ID}))

	rate := 500.0
	got, err := s.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	got.IngestMessagesPerSecond = &rate
	require.NoError(t, s.UpdateTenant(ctx, got))

	after, err := s.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.Equal(t, gold.ID, after.TierID)
	require.NotNil(t, after.IngestMessagesPerSecond)
}

// TestCountTenantsAtTier backs the deletion guard (ADR-065 decision 9): the count
// must see tenants at THIS tier and no others, or a tier deletion would be refused
// for the wrong reason — or, worse, allowed while in use.
func TestCountTenantsAtTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gold := seedTier(t, s, TierGoldToken)
	bronze := seedTier(t, s, TierBronzeToken)

	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme", TierID: gold.ID}))
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "globex", TierID: gold.ID}))
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "initech", TierID: bronze.ID}))

	n, err := s.CountTenantsAtTier(ctx, gold.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)

	n, err = s.CountTenantsAtTier(ctx, bronze.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// An unreferenced tier counts zero — the case that makes deletion legal.
	empty := seedTier(t, s, TierSilverToken)
	n, err = s.CountTenantsAtTier(ctx, empty.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, n)
}

// TestDeleteTenantTierIsHard pins that a deleted tier frees its token. Every iam
// entity embeds gorm.Model, so a default (soft) delete would leave the row
// occupying the unique token index and block re-creating a tier by the same name —
// the tombstone trap this repo has hit before.
func TestDeleteTenantTierIsHard(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tier := seedTier(t, s, TierBronzeToken)
	require.NoError(t, s.DeleteTenantTier(ctx, tier))

	_, err := s.TenantTierByToken(ctx, TierBronzeToken)
	require.Error(t, err)

	require.NoError(t, s.CreateTenantTier(ctx, &TenantTier{Token: TierBronzeToken}),
		"a deleted tier must free its token for re-creation")
}

// TestTenantByTokenPreloadsTheTier pins the association itself, not a fixture that
// supplies it.
//
// This looks redundant next to the round-trip above, and it is not. The graphql layer's
// TenantGovernanceResolver.TierToken() reads t.Tier.Token and returns "" when Tier is
// nil, which is only correct because THIS query preloads it. Every test on that
// resolver hand-builds &Tenant{Tier: &TenantTier{...}} — so each one derives its
// expectation from a fixture that guarantees the very thing production has to supply,
// and dropping the Preload here would leave all of them green.
//
// The failure that would ship is silent and total: every tenant resolves tierToken:"",
// ai-inference joins that against its grant tables, finds nothing, and reports the NL
// authoring door "unavailable" for everyone — which is indistinguishable from the
// correct fail-closed answer for an unknown tier. Nothing logs, nothing errors.
func TestTenantByTokenPreloadsTheTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gold := seedTier(t, s, TierGoldToken)
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme", TierID: gold.ID}))

	got, err := s.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.NotNil(t, got.Tier,
		"TenantByToken must preload Tier: the governance wire reads Tier.Token and degrades to \"\" — silently — without it")
	require.Equal(t, TierGoldToken, got.Tier.Token)
}

// TestListTenantsPreloadsTheTier pins the same association on the admin list path,
// which renders the tier column and the effective-settings cascade.
func TestListTenantsPreloadsTheTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gold := seedTier(t, s, TierGoldToken)
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme", TierID: gold.ID}))

	tenants, err := s.ListTenants(ctx)
	require.NoError(t, err)
	require.Len(t, tenants, 1)
	require.NotNil(t, tenants[0].Tier, "the admin list resolves each tenant's tier + effective settings")
	require.Equal(t, TierGoldToken, tenants[0].Tier.Token)
}
