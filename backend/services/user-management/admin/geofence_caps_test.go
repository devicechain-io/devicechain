// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-user-management/iam"
)

// TestBothCapDoorsAgree is the test this slice exists to have, and the finding it was written
// from: a platform maximum enforced at ONE of two write doors is not a platform maximum.
//
// A tier's settings go through iam.ValidateTierConfig, which is called only from the two TIER
// mutations. A per-tenant override never passes through it — it reaches the database through
// GovernanceOverrides.validate() alone. So the same number has to be enforced twice, in two
// packages, against two Go types (an `any` off a JSON blob, and a typed *int column), and
// nothing about the shapes makes them agree.
//
// 🔴 WHAT THIS PINS IS AGREEMENT, NOT CORRECTNESS: it walks one table through both doors and
// requires the same accept/reject verdict from each. A maximum that was wrong in both places
// would pass — that is what TestTheGeoFenceCapValidatorsEnforceTheirMaximum is for, over in
// iam, where the constant is compared against governance's own. What this catches is the
// failure that actually happens: one door updated, the other forgotten.
func TestBothCapDoorsAgree(t *testing.T) {
	cases := []struct {
		name string
		key  string
		max  int
		set  func(*GovernanceOverrides, *int)
	}{
		{
			name: "geoFencePositionCeiling", key: iam.GeoFencePositionCeilingConfigKey,
			max: governance.MaxGeoFencePositionCeiling,
			set: func(g *GovernanceOverrides, v *int) { g.GeoFencePositionCeiling = v },
		},
		{
			name: "geoFenceCeiling", key: iam.GeoFenceCeilingConfigKey,
			max: governance.MaxGeoFenceCeiling,
			set: func(g *GovernanceOverrides, v *int) { g.GeoFenceCeiling = v },
		},
		{
			name: "geoFencePositionBudget", key: iam.GeoFencePositionBudgetConfigKey,
			max: governance.MaxTenantGeometryPositions,
			set: func(g *GovernanceOverrides, v *int) { g.GeoFencePositionBudget = v },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The values are chosen to straddle the wall exactly, and to include the ordinary
			// legal ones so "both doors reject everything" cannot pass this test.
			for _, v := range []int{1, 7, c.max - 1, c.max, c.max + 1, 2 * c.max, 0, -1} {
				tierErr := iam.ValidateTierConfig(map[string]any{c.key: v})

				var g GovernanceOverrides
				n := v
				c.set(&g, &n)
				overrideErr := g.validate()

				require.Equalf(t, tierErr == nil, overrideErr == nil,
					"the two doors disagree about %s = %d: tier says %v, per-tenant override says %v",
					c.name, v, tierErr, overrideErr)
			}

			// nil is inherit at the override door, and an ABSENT key is inherit at the tier
			// door. Both must be accepted, or clearing a cap becomes impossible.
			var g GovernanceOverrides
			c.set(&g, nil)
			require.NoError(t, g.validate(), "a nil %s override means inherit and must be accepted", c.name)
			require.NoError(t, iam.ValidateTierConfig(map[string]any{}), "an absent key means inherit")
		})
	}
}

// TestTheCapOverrideDoorRejectsRatherThanClamps pins the polarity of the override door. Clamping
// would be the tempting kindness — the operator gets a working tenant either way — but it would
// mean the number the operator typed and the number the platform enforces silently differ, on
// the one setting where the operator's intent is the whole point of the feature.
//
// The read side does clamp (governance.resolveGeoFenceCap), and that asymmetry is deliberate: a
// door is where a human is present to be told, and a wire fold is not.
func TestTheCapOverrideDoorRejectsRatherThanClamps(t *testing.T) {
	over := governance.MaxTenantGeometryPositions + 1
	g := GovernanceOverrides{GeoFencePositionBudget: &over}
	err := g.validate()
	require.Error(t, err, "an over-large budget override must be refused")
	require.Contains(t, err.Error(), "geoFencePositionBudget", "the error must name the field the caller sent")
	// 🔴 THE COMPARISON IS AGAINST A FRESH EXPRESSION, NOT AGAINST `over`, AND THAT IS THE WHOLE
	// ASSERTION. `g.GeoFencePositionBudget` POINTS AT `over`, so `require.Equal(over, *g.…)`
	// compares the variable with itself: a validate() that clamped through the pointer would
	// change both sides and the check would pass. It was written that way, and a reviewer proved
	// it — a validateBoundedOverride that clamps AND still returns the error passed the entire
	// admin package. A guard aliased to the thing it guards is decoration.
	require.NotNil(t, g.GeoFencePositionBudget)
	require.Equal(t, governance.MaxTenantGeometryPositions+1, *g.GeoFencePositionBudget,
		"validate() mutated the caller's value — refusing and clamping are different answers, and "+
			"only one of them leaves the operator's number and the enforced number the same")
}

// TestTheCapOverridesReachTheTenantRow pins applyTo. Every field it forgets is one an operator
// can set through the API and that never lands — the write reports success and the cap does not
// change. It is a four-line function that is edited once per new override and read never.
func TestTheCapOverridesReachTheTenantRow(t *testing.T) {
	vertexCeiling, fenceCeiling, budget := 700, 250, 90000
	g := GovernanceOverrides{
		GeoFencePositionCeiling: &vertexCeiling,
		GeoFenceCeiling:         &fenceCeiling,
		GeoFencePositionBudget:  &budget,
	}
	var tenant iam.Tenant
	g.applyTo(&tenant)

	require.NotNil(t, tenant.GeoFencePositionCeiling)
	require.Equal(t, 700, *tenant.GeoFencePositionCeiling, "geoFencePositionCeiling landed on the wrong column")
	require.NotNil(t, tenant.GeoFenceCeiling)
	require.Equal(t, 250, *tenant.GeoFenceCeiling, "geoFenceCeiling landed on the wrong column")
	require.NotNil(t, tenant.GeoFencePositionBudget)
	require.Equal(t, 90000, *tenant.GeoFencePositionBudget, "geoFencePositionBudget landed on the wrong column")

	// The counterweight: applyTo is a full REPLACE, so nil must CLEAR rather than be skipped.
	// A struct-update that skipped nils would make "revert this tenant to its tier" impossible.
	tenant.GeoFencePositionCeiling, tenant.GeoFenceCeiling, tenant.GeoFencePositionBudget = &vertexCeiling, &fenceCeiling, &budget
	GovernanceOverrides{}.applyTo(&tenant)
	require.Nil(t, tenant.GeoFencePositionCeiling, "an omitted override must clear the column, not leave the old cap")
	require.Nil(t, tenant.GeoFenceCeiling, "an omitted override must clear the column, not leave the old cap")
	require.Nil(t, tenant.GeoFencePositionBudget, "an omitted override must clear the column, not leave the old cap")
}

// TestTheServiceRefusesAnOverLargeCapEndToEnd is the test that proves the DOOR is closed, not
// merely that a helper would close it.
//
// GovernanceOverrides is EMBEDDED in TenantInput and TenantMutableInput, and neither declares a
// validate() of its own — so `in.validate()` in CreateTenant and UpdateTenant is the promoted
// GovernanceOverrides.validate(). That is load-bearing and invisible: the day someone adds a
// `func (in TenantInput) validate() error` to check, say, the token, it SHADOWS the promoted
// method, every governance check silently stops running, and TestBothCapDoorsAgree above keeps
// passing because it calls the shadowed method directly.
//
// So this drives the real service, through the real store, and asserts the refusal comes out of
// the mutation an operator actually calls.
func TestTheServiceRefusesAnOverLargeCapEndToEnd(t *testing.T) {
	s := newTestService(t)
	seedTiers(t, s)
	ctx := context.Background()

	over := governance.MaxTenantGeometryPositions + 1
	_, err := s.CreateTenant(ctx, TenantInput{
		Token: "greedy", TierToken: iam.TierGoldToken,
		GovernanceOverrides: GovernanceOverrides{GeoFencePositionBudget: &over},
	})
	require.Error(t, err, "createTenant admitted a budget above the platform maximum")
	require.Contains(t, err.Error(), iam.GeoFencePositionBudgetConfigKey)

	// The counterweight: a legal cap creates fine, so the refusal above is the maximum doing
	// its job rather than the whole path being broken.
	ok := governance.MaxTenantGeometryPositions
	created, err := s.CreateTenant(ctx, TenantInput{
		Token: "bounded", TierToken: iam.TierGoldToken,
		GovernanceOverrides: GovernanceOverrides{GeoFencePositionBudget: &ok},
	})
	require.NoError(t, err, "createTenant refused a budget AT the platform maximum")
	require.NotNil(t, created.GeoFencePositionBudget)
	require.Equal(t, ok, *created.GeoFencePositionBudget)

	// And the update door, which is the one that matters more: it is a full replace, so it is
	// the mutation an operator reaches for repeatedly.
	_, err = s.UpdateTenant(ctx, "bounded", TenantMutableInput{
		TierToken:           iam.TierGoldToken,
		GovernanceOverrides: GovernanceOverrides{GeoFenceCeiling: intp(governance.MaxGeoFenceCeiling + 1)},
	})
	require.Error(t, err, "updateTenant admitted a fence count above the platform maximum")
	require.Contains(t, err.Error(), iam.GeoFenceCeilingConfigKey)

	// A refused update must not have written anything — the tenant keeps the budget it had,
	// rather than being left half-updated with its other caps cleared by the full replace.
	after, err := s.iam.TenantByToken(ctx, "bounded")
	require.NoError(t, err)
	require.NotNil(t, after.GeoFencePositionBudget,
		"a refused update cleared the tenant's existing budget — validation must run before the write")
	require.Equal(t, ok, *after.GeoFencePositionBudget)
}

func intp(v int) *int { return &v }
