// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
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
			name: "geoFenceVertexCeiling", key: iam.GeoFenceVertexCeilingConfigKey,
			max: governance.MaxGeoFenceVertexCeiling,
			set: func(g *GovernanceOverrides, v *int) { g.GeoFenceVertexCeiling = v },
		},
		{
			name: "geoFenceCeiling", key: iam.GeoFenceCeilingConfigKey,
			max: governance.MaxGeoFenceCeiling,
			set: func(g *GovernanceOverrides, v *int) { g.GeoFenceCeiling = v },
		},
		{
			name: "geoFenceVertexBudget", key: iam.GeoFenceVertexBudgetConfigKey,
			max: governance.MaxTenantGeometryVertices,
			set: func(g *GovernanceOverrides, v *int) { g.GeoFenceVertexBudget = v },
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
	over := governance.MaxTenantGeometryVertices + 1
	g := GovernanceOverrides{GeoFenceVertexBudget: &over}
	err := g.validate()
	require.Error(t, err, "an over-large budget override must be refused")
	require.Contains(t, err.Error(), "geoFenceVertexBudget", "the error must name the field the caller sent")
	require.NotNil(t, g.GeoFenceVertexBudget)
	require.Equal(t, over, *g.GeoFenceVertexBudget,
		"validate() must not mutate the caller's value — refusing and clamping are different answers")
}

// TestTheCapOverridesReachTheTenantRow pins applyTo. Every field it forgets is one an operator
// can set through the API and that never lands — the write reports success and the cap does not
// change. It is a four-line function that is edited once per new override and read never.
func TestTheCapOverridesReachTheTenantRow(t *testing.T) {
	vertexCeiling, fenceCeiling, budget := 700, 250, 90000
	g := GovernanceOverrides{
		GeoFenceVertexCeiling: &vertexCeiling,
		GeoFenceCeiling:       &fenceCeiling,
		GeoFenceVertexBudget:  &budget,
	}
	var tenant iam.Tenant
	g.applyTo(&tenant)

	require.NotNil(t, tenant.GeoFenceVertexCeiling)
	require.Equal(t, 700, *tenant.GeoFenceVertexCeiling, "geoFenceVertexCeiling landed on the wrong column")
	require.NotNil(t, tenant.GeoFenceCeiling)
	require.Equal(t, 250, *tenant.GeoFenceCeiling, "geoFenceCeiling landed on the wrong column")
	require.NotNil(t, tenant.GeoFenceVertexBudget)
	require.Equal(t, 90000, *tenant.GeoFenceVertexBudget, "geoFenceVertexBudget landed on the wrong column")

	// The counterweight: applyTo is a full REPLACE, so nil must CLEAR rather than be skipped.
	// A struct-update that skipped nils would make "revert this tenant to its tier" impossible.
	tenant.GeoFenceVertexCeiling, tenant.GeoFenceCeiling, tenant.GeoFenceVertexBudget = &vertexCeiling, &fenceCeiling, &budget
	GovernanceOverrides{}.applyTo(&tenant)
	require.Nil(t, tenant.GeoFenceVertexCeiling, "an omitted override must clear the column, not leave the old cap")
	require.Nil(t, tenant.GeoFenceCeiling, "an omitted override must clear the column, not leave the old cap")
	require.Nil(t, tenant.GeoFenceVertexBudget, "an omitted override must clear the column, not leave the old cap")
}
