// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-user-management/iam"
)

// TestTheGeoFenceCapsAreServedUnderTheirTierKeyNames is the user-management half of a
// CROSS-SERVICE wire contract. device-management selects these three fields off
// tenantGovernance before it will admit a fence create or edit, through the query in
// core/governance, and it BLOCKS on the answer — so a rename here does not degrade fence
// authoring, it stops it.
//
// 🔴 IT COMPARES THE SDL AGAINST THE TIER-KEY CONSTANTS RATHER THAN AGAINST STRING LITERALS,
// and that is the point. The wire field name, the tier config key and the per-tenant override
// field are the SAME NAME by design — an operator sets `geoFenceCeiling` on a tier and reads
// `geoFenceCeiling` off tenantGovernance — so pinning the SDL against a fresh literal would
// let the two drift apart while both tests stayed green. core/governance builds its query from
// the same three constants, which closes the loop from the reading end.
func TestTheGeoFenceCapsAreServedUnderTheirTierKeyNames(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(context.Background(),
		`{ __type(name: "TenantGovernance") { fields { name type { kind name } } } }`, "", nil)
	require.Empty(t, res.Errors)

	var out struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Type struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"type"`
			} `json:"fields"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))

	byName := make(map[string]string, len(out.Type.Fields))
	for _, f := range out.Type.Fields {
		byName[f.Name] = f.Type.Name
	}
	for _, key := range []string{
		iam.GeoFencePositionCeilingConfigKey,
		iam.GeoFenceCeilingConfigKey,
		iam.GeoFencePositionBudgetConfigKey,
	} {
		kind, ok := byName[key]
		require.Truef(t, ok, "TenantGovernance does not serve %q; device-management blocks on this "+
			"field before admitting a fence mutation, so a rename stops fence authoring outright", key)
		// NULLABLE Int, deliberately. Non-null would force this service to invent a number
		// for a tenant whose tier declares nothing — which is the enforcing service's
		// platform default to supply, not this one's, and the whole reason null means
		// "inherit" rather than "unlimited".
		require.Equalf(t, "Int", kind,
			"%s must be a NULLABLE Int: null is how 'inherit the platform default' is spelled", key)
	}
}

// TestTheGeoFenceCapResolversDoNotCrossTheirValues. Three same-typed nullable ints resolved by
// three near-identical four-line methods is the shape in which one method returns another's
// field — and every "is it set?" assertion passes while a tenant's whole-set budget is enforced
// as its per-fence ceiling. The values are far apart so a cross is a wrong number, not a
// coincidence.
func TestTheGeoFenceCapResolversDoNotCrossTheirValues(t *testing.T) {
	vertexCeiling, fenceCeiling, budget := 700, 250, 90000
	r := &TenantGovernanceResolver{t: &iam.Tenant{
		GeoFencePositionCeiling: &vertexCeiling,
		GeoFenceCeiling:         &fenceCeiling,
		GeoFencePositionBudget:  &budget,
	}}

	require.NotNil(t, r.GeoFencePositionCeiling())
	require.EqualValues(t, 700, *r.GeoFencePositionCeiling(), "geoFencePositionCeiling resolved another cap")
	require.NotNil(t, r.GeoFenceCeiling())
	require.EqualValues(t, 250, *r.GeoFenceCeiling(), "geoFenceCeiling resolved another cap")
	require.NotNil(t, r.GeoFencePositionBudget())
	require.EqualValues(t, 90000, *r.GeoFencePositionBudget(), "geoFencePositionBudget resolved another cap")
}

// TestAnUnconfiguredTenantResolvesEveryCapToNull is the counterweight to the test above, and it
// is the case every deployment is in on the day this ships. Null means "inherit"; the reader
// substitutes the platform default, which is a real bound. A zero here would be read by
// device-management's fold as "not a cap" and quietly replaced — the same answer by accident —
// but a resolver that returned 0 for an unset column would ALSO return 0 for a tenant an
// operator had genuinely misconfigured, and those must stay distinguishable.
func TestAnUnconfiguredTenantResolvesEveryCapToNull(t *testing.T) {
	r := &TenantGovernanceResolver{t: &iam.Tenant{}}
	require.Nil(t, r.GeoFencePositionCeiling(), "an unconfigured tenant must resolve to null (inherit), not 0")
	require.Nil(t, r.GeoFenceCeiling(), "an unconfigured tenant must resolve to null (inherit), not 0")
	require.Nil(t, r.GeoFencePositionBudget(), "an unconfigured tenant must resolve to null (inherit), not 0")
}

// TestTheGeoFenceCapResolversWalkTheFullCascade. The resolvers call Effective*, not the raw
// column, so a tenant with no override of its own must still be served its TIER's cap. Reading
// the column directly would compile, pass every test above, and silently un-tier every tenant
// that had not set a personal override — which is most of them.
func TestTheGeoFenceCapResolversWalkTheFullCascade(t *testing.T) {
	tiered := &iam.Tenant{Tier: &iam.TenantTier{Config: map[string]any{
		iam.GeoFencePositionCeilingConfigKey: 640,
		iam.GeoFenceCeilingConfigKey:         320,
		iam.GeoFencePositionBudgetConfigKey:  80000,
	}}}
	r := &TenantGovernanceResolver{t: tiered}
	require.NotNil(t, r.GeoFencePositionCeiling())
	require.EqualValues(t, 640, *r.GeoFencePositionCeiling(), "the resolver read the column instead of the cascade")
	require.NotNil(t, r.GeoFenceCeiling())
	require.EqualValues(t, 320, *r.GeoFenceCeiling(), "the resolver read the column instead of the cascade")
	require.NotNil(t, r.GeoFencePositionBudget())
	require.EqualValues(t, 80000, *r.GeoFencePositionBudget(), "the resolver read the column instead of the cascade")

	// And an override beats the tier, end to end through the resolver — the half that proves
	// the cascade is being walked rather than the tier being read directly.
	override := 128
	tiered.GeoFencePositionCeiling = &override
	require.EqualValues(t, 128, *r.GeoFencePositionCeiling(), "a per-tenant override must beat the tier")
	require.EqualValues(t, 320, *r.GeoFenceCeiling(), "overriding one cap must not disturb another")
}

// TestTheServedCapsNeverExceedThePlatformMaximum ties this service's output to the bound the
// enforcing service will clamp at. Both write doors reject an over-large value, so one can only
// arrive by a direct out-of-band database write — and this asserts that such a row is served as
// INHERIT rather than as a cap larger than the platform honours.
func TestTheServedCapsNeverExceedThePlatformMaximum(t *testing.T) {
	over := func(n int) *int { v := n + 1; return &v }
	r := &TenantGovernanceResolver{t: &iam.Tenant{
		GeoFencePositionCeiling: over(governance.MaxGeoFencePositionCeiling),
		GeoFenceCeiling:         over(governance.MaxGeoFenceCeiling),
		GeoFencePositionBudget:  over(governance.MaxTenantGeometryPositions),
	}}
	require.Nil(t, r.GeoFencePositionCeiling(), "an out-of-band over-large ceiling must serve as inherit")
	require.Nil(t, r.GeoFenceCeiling(), "an out-of-band over-large fence count must serve as inherit")
	require.Nil(t, r.GeoFencePositionBudget(), "an out-of-band over-large budget must serve as inherit")
}
