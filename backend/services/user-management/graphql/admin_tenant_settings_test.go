// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingFor finds one dimension's row in an effective-settings result.
func settingFor(t *testing.T, rows []*AdminTenantSettingResolver, name string) *AdminTenantSettingResolver {
	t.Helper()
	for _, row := range rows {
		if row.Dimension().Name() == name {
			return row
		}
	}
	require.FailNowf(t, "dimension missing", "no effective-settings row for %q", name)
	return nil
}

// TestEffectiveSettingsCoversEveryDimension drives the RESOLVER, not the cascade
// beneath it.
//
// This distinction is the whole lesson of the S1/S2 slices: a pure-function test of
// the fold says nothing about whether the operator-facing surface CALLS it, or calls
// it for every dimension. A screen that silently omits a dimension is exactly as
// broken as one that shows the wrong number for it — the operator cannot configure
// what they cannot see, and would have no reason to suspect it exists.
func TestEffectiveSettingsCoversEveryDimension(t *testing.T) {
	r := &AdminTenantResolver{M: iam.Tenant{Tier: goldTier()}}
	rows := r.EffectiveSettings()

	require.Len(t, rows, len(governance.AllDimensions()),
		"every declared dimension must appear on the operator's screen")
	for _, d := range governance.AllDimensions() {
		row := settingFor(t, rows, d.Name)
		// The row must be renderable, since the console draws from it directly.
		assert.Equal(t, d.Label, row.Dimension().Label())
		assert.Equal(t, d.RateUnit, row.Dimension().RateUnit())
		assert.Equal(t, d.RateField, row.Dimension().RateField())
		assert.Equal(t, d.BurstField, row.Dimension().BurstField())
	}
}

// TestEffectiveSettingsReportsTierAndDelta pins ADR-065 decision 7 at the surface
// that implements it: effective settings must resolve as tier + delta, never an
// opaque merged blob. Each level is carried SEPARATELY beside the winner, so an
// operator can see the packaging AND the exception to it — which is what makes an
// override an auditable exception rather than an invisible one.
func TestEffectiveSettingsReportsTierAndDelta(t *testing.T) {
	r := &AdminTenantResolver{M: iam.Tenant{
		Tier:                    goldTier(),
		IngestMessagesPerSecond: f64(5000),
		IngestBurst:             ip(9000),
	}}
	rows := r.EffectiveSettings()

	ingest := settingFor(t, rows, governance.Ingest.Name)
	// The winner, and the reason it won.
	assert.Equal(t, string(iam.SourceOverride), ingest.Rate().Source())
	assert.Equal(t, float64(5000), *ingest.Rate().Value())
	// The level it beat is still visible — the delta is only legible against it.
	assert.Equal(t, float64(2000), *ingest.Rate().Tier())
	assert.Equal(t, float64(5000), *ingest.Rate().Override())

	assert.Equal(t, string(iam.SourceOverride), ingest.Burst().Source())
	assert.EqualValues(t, 9000, *ingest.Burst().Value())
	assert.EqualValues(t, 4000, *ingest.Burst().Tier())
	assert.EqualValues(t, 9000, *ingest.Burst().Override())

	// An un-overridden dimension reports the tier as the winner and carries no
	// delta — nil, not zero: "no exception" and "an exception of 0" are opposite
	// facts, and 0 would render as a ceiling admitting nothing.
	outbound := settingFor(t, rows, governance.Outbound.Name)
	assert.Equal(t, string(iam.SourceTier), outbound.Rate().Source())
	assert.Equal(t, float64(200), *outbound.Rate().Value())
	assert.Equal(t, float64(200), *outbound.Rate().Tier())
	assert.Nil(t, outbound.Rate().Override())
	assert.Nil(t, outbound.Burst().Override())
}

// TestEffectiveSettingsNamesThePlatformDefaultWithoutInventingIt pins the honest
// limit of this surface.
//
// user-management CANNOT state the platform default: each enforcing service builds
// its own from its own Helm config, and the outbound dimension has two independent
// copies (event-processing at the REACT source, outbound-connectors at the sink,
// ADR-060 SD-3) which may legally differ. So a dimension nobody declares resolves to
// a NULL value with source "platform-default" — the console renders the label. A
// number here would be a third copy that goes stale the first time a chart value is
// edited, and it would be worse than no number at all, because an operator would
// believe it.
func TestEffectiveSettingsNamesThePlatformDefaultWithoutInventingIt(t *testing.T) {
	// The seeded SILVER shape: declares nothing, so every dimension inherits.
	r := &AdminTenantResolver{M: iam.Tenant{Tier: &iam.TenantTier{Token: iam.TierSilverToken}}}

	for _, row := range r.EffectiveSettings() {
		name := row.Dimension().Name()
		assert.Equal(t, string(iam.SourcePlatformDefault), row.Rate().Source(), name)
		assert.Nil(t, row.Rate().Value(), "%s: the platform default is not this service's to state", name)
		assert.Nil(t, row.Rate().Tier(), name)
		assert.Nil(t, row.Rate().Override(), name)

		assert.Equal(t, string(iam.SourcePlatformDefault), row.Burst().Source(), name)
		assert.Nil(t, row.Burst().Value(), name)
	}
}

// TestGovernanceDimensionsFailClosed holds the dimension vocabulary to the same bar
// as the tier catalog it describes: it exists to tell an operator what a tier may
// carry, so it gates on tenant:read like the tiers themselves.
func TestGovernanceDimensionsFailClosed(t *testing.T) {
	r := &AdminResolver{}

	_, err := r.GovernanceDimensions(context.Background())
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	limited := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.UserWrite)}})
	_, err = r.GovernanceDimensions(limited)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	reader := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.TenantRead)}})
	dims, err := r.GovernanceDimensions(reader)
	require.NoError(t, err)
	require.Len(t, dims, len(governance.AllDimensions()))
}
