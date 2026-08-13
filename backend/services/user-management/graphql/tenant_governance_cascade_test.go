// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

// goldTier mirrors the seeded gold packaging: an explicit deviation above the
// platform default.
func goldTier() *iam.TenantTier {
	return &iam.TenantTier{Token: iam.TierGoldToken, Config: map[string]any{
		"ingestMessagesPerSecond":   float64(2000),
		"ingestBurst":               float64(4000),
		"outboundMessagesPerSecond": float64(200),
		"outboundBurst":             float64(400),
	}}
}

// TestGovernanceCascadeTierBelowOverride pins ADR-065 decision 5 — per-tenant
// override → tier → platform default — at the seam where it is assembled.
//
// Null does NOT mean unlimited at any level: a null on the wire means "neither the
// tenant nor its tier declares one", and core/governance folds that onto the
// platform default. So the three cases below are the whole cascade.
func TestGovernanceCascadeTierBelowOverride(t *testing.T) {
	t.Run("tenant override wins over its tier", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{
			Tier:                    goldTier(),
			IngestMessagesPerSecond: f64(5000),
			IngestBurst:             ip(9000),
		}}
		// The audited exception (decision 7) beats the packaging default.
		require.Equal(t, float64(5000), *r.IngestMessagesPerSecond())
		require.EqualValues(t, 9000, *r.IngestBurst())
	})

	t.Run("tier supplies the ceiling when the tenant declares none", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: goldTier()}}
		require.Equal(t, float64(2000), *r.IngestMessagesPerSecond())
		require.EqualValues(t, 4000, *r.IngestBurst())
		require.Equal(t, float64(200), *r.OutboundMessagesPerSecond())
		require.EqualValues(t, 400, *r.OutboundBurst())
	})

	t.Run("null when neither declares — the consumer applies the platform default", func(t *testing.T) {
		// The seeded SILVER shape: declares nothing, so every dimension inherits.
		// This is what keeps the standard tier tracking a platform default an
		// operator raises in Helm, rather than pinning it to a number frozen in a seed.
		r := &TenantGovernanceResolver{t: &iam.Tenant{
			Tier: &iam.TenantTier{Token: iam.TierSilverToken},
		}}
		require.Nil(t, r.IngestMessagesPerSecond())
		require.Nil(t, r.IngestBurst())
		require.Nil(t, r.OutboundMessagesPerSecond())
		require.Nil(t, r.OutboundBurst())
	})

	t.Run("the dimensions are independent", func(t *testing.T) {
		// Overriding ingest must not disturb outbound: a tenant may ingest heavily
		// yet fan out little, or the reverse.
		r := &TenantGovernanceResolver{t: &iam.Tenant{
			Tier:                    goldTier(),
			IngestMessagesPerSecond: f64(5000),
		}}
		require.Equal(t, float64(5000), *r.IngestMessagesPerSecond())
		require.EqualValues(t, 4000, *r.IngestBurst(), "burst still comes from the tier")
		require.Equal(t, float64(200), *r.OutboundMessagesPerSecond())
	})
}

// goldTierWithShedPriority is the seeded gold shape once ADR-065 S6 seeds a shed
// priority onto it (the never-shed band).
func goldTierWithShedPriority() *iam.TenantTier {
	return &iam.TenantTier{Token: iam.TierGoldToken, Config: map[string]any{"shedPriority": float64(90)}}
}

// TestShedPriorityCascadeOnTheWire pins that the data-plane tenantGovernance surface
// resolves shedPriority down the same cascade — override → tier → null — that
// event-sources reads it through. Null means the reader substitutes the fail-safe.
func TestShedPriorityCascadeOnTheWire(t *testing.T) {
	t.Run("override wins over the tier", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: goldTierWithShedPriority(), ShedPriority: ip(10)}}
		require.EqualValues(t, 10, *r.ShedPriority())
	})
	t.Run("tier supplies it when the tenant declares none", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: goldTierWithShedPriority()}}
		require.EqualValues(t, 90, *r.ShedPriority())
	})
	t.Run("null when neither declares — the reader applies the fail-safe", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: &iam.TenantTier{Token: iam.TierSilverToken}}}
		require.Nil(t, r.ShedPriority())
	})
}

// TestAdminShedPriorityIsReadable is the H1 regression: the per-tenant shedPriority
// override MUST be readable back on the admin plane. updateTenant is a full REPLACE of
// the mutable fields (applyTo writes every override, nil clearing it), so a console
// that could not read the current override would null an operator's "degrades last"
// placement on any unrelated edit. Every other override is readable; this one must be.
func TestAdminShedPriorityIsReadable(t *testing.T) {
	// A set override reads back as itself.
	r := &AdminTenantResolver{M: iam.Tenant{ShedPriority: ip(95)}}
	got := r.ShedPriority()
	require.NotNil(t, got, "the shedPriority override must be readable on AdminTenant (else an edit silently nulls it)")
	require.EqualValues(t, 95, *got)

	// An unset override reads back as null (inherit), like every other override.
	require.Nil(t, (&AdminTenantResolver{M: iam.Tenant{}}).ShedPriority())
}

// goldTierWithHeldCommandCeiling is a gold tier that packages extra headroom for an
// offline fleet's HELD-command backlog.
func goldTierWithHeldCommandCeiling() *iam.TenantTier {
	return &iam.TenantTier{Token: iam.TierGoldToken, Config: map[string]any{"heldCommandCeiling": float64(50000)}}
}

// TestHeldCommandCeilingCascadeOnTheWire pins that the data-plane tenantGovernance
// surface resolves heldCommandCeiling down the same cascade — override → tier → null —
// that command-delivery will read it through. Null means the reader substitutes its own
// configured default, which is a real bound; no level can express "unlimited".
func TestHeldCommandCeilingCascadeOnTheWire(t *testing.T) {
	t.Run("override wins over the tier", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: goldTierWithHeldCommandCeiling(), HeldCommandCeiling: ip(200)}}
		require.EqualValues(t, 200, *r.HeldCommandCeiling())
	})
	t.Run("tier supplies it when the tenant declares none", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: goldTierWithHeldCommandCeiling()}}
		require.EqualValues(t, 50000, *r.HeldCommandCeiling())
	})
	t.Run("null when neither declares — the reader applies its own default", func(t *testing.T) {
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: &iam.TenantTier{Token: iam.TierSilverToken}}}
		require.Nil(t, r.HeldCommandCeiling())
	})
	t.Run("a junk override falls through to the tier, not past it", func(t *testing.T) {
		// Same asymmetry as the rate ceilings: the consumer floors an unusable value to
		// its OWN default, so passing one through here would skip the tier entirely and
		// hand a gold tenant the service default instead of its packaged headroom.
		r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: goldTierWithHeldCommandCeiling(), HeldCommandCeiling: ip(0)}}
		require.EqualValues(t, 50000, *r.HeldCommandCeiling())
	})
}

// TestAdminHeldCommandCeilingIsReadable is the same regression TestAdminShedPriorityIsReadable
// pins, for the new override. updateTenant is a full REPLACE of the mutable fields, so an
// override the admin plane cannot read back is one that any unrelated edit silently nulls —
// here, quietly unbounding a tenant's HELD backlog down to whatever its tier or the service
// default happens to allow, with nothing in the API to show it ever had a bound.
func TestAdminHeldCommandCeilingIsReadable(t *testing.T) {
	r := &AdminTenantResolver{M: iam.Tenant{HeldCommandCeiling: ip(2500)}}
	got := r.HeldCommandCeiling()
	require.NotNil(t, got, "the heldCommandCeiling override must be readable on AdminTenant (else an edit silently nulls it)")
	require.EqualValues(t, 2500, *got)

	// An unset override reads back as null (inherit), like every other override.
	require.Nil(t, (&AdminTenantResolver{M: iam.Tenant{}}).HeldCommandCeiling())
}

// TestUnusableOverrideFallsThroughToTheTierNotPastIt pins the one case where "the
// consumer already folds onto the platform default, so the composition is
// equivalent" is NOT equivalent to ADR-065 D5's cascade.
//
// An unusable override (non-positive) can only arrive via a direct out-of-band DB
// write — the API rejects it. If this seam passed it through, core/governance would
// floor it to the PLATFORM DEFAULT, skipping the tier entirely: a gold tenant
// carrying a junk -5 would meter at 1000/s rather than its tier's 2000/s. The
// cascade says override → tier → default, so an override that means nothing must
// fall through to the TIER, not past it.
func TestUnusableOverrideFallsThroughToTheTierNotPastIt(t *testing.T) {
	junk := &TenantGovernanceResolver{t: &iam.Tenant{
		Tier:                    goldTier(),
		IngestMessagesPerSecond: f64(-5),
		IngestBurst:             ip(0),
	}}
	require.Equal(t, float64(2000), *junk.IngestMessagesPerSecond(),
		"a junk override must fall through to the tier, not past it to the platform default")
	require.EqualValues(t, 4000, *junk.IngestBurst())

	// With no tier setting either, it still degrades to null (inherit the platform
	// default) — never to the junk value itself.
	noTier := &TenantGovernanceResolver{t: &iam.Tenant{
		Tier:                      &iam.TenantTier{Token: iam.TierSilverToken},
		OutboundMessagesPerSecond: f64(0),
	}}
	require.Nil(t, noTier.OutboundMessagesPerSecond())
}

// TestGovernanceCascadeFailsSafeWithoutTier pins the direction a missing tier fails
// in. The tier is a required FK and both read paths preload it, so a nil Tier means
// a caller loaded the tenant some other way — a bug. It must degrade to the
// pre-ADR-065 behavior (override, else platform default), costing a tier's tuning
// and never a ceiling: a nil-deref here would take down the query every enforcing
// service refreshes against, and a zero would admit nothing.
func TestGovernanceCascadeFailsSafeWithoutTier(t *testing.T) {
	r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: nil}}
	require.Nil(t, r.IngestMessagesPerSecond())
	require.Nil(t, r.IngestBurst())
	require.Nil(t, r.AiInferenceRequestsPerMinute())
	require.Nil(t, r.AiInferenceBurst())

	withOverride := &TenantGovernanceResolver{t: &iam.Tenant{
		Tier:                    nil,
		IngestMessagesPerSecond: f64(750),
	}}
	require.Equal(t, float64(750), *withOverride.IngestMessagesPerSecond())
}

// TestAiInferenceCascade covers the per-MINUTE dimension. The unit conversion is
// core/governance's job (Dimension.PerSecondScale) — this seam must hand back the
// DECLARED unit unchanged, exactly as a raw override always did, or a tier setting
// and a tenant override would disagree about what "30" means.
func TestAiInferenceCascade(t *testing.T) {
	tier := &iam.TenantTier{Config: map[string]any{
		"aiInferenceRequestsPerMinute": float64(60),
		"aiInferenceBurst":             float64(30),
	}}
	r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: tier}}
	require.Equal(t, float64(60), *r.AiInferenceRequestsPerMinute(), "declared per minute, unconverted")
	require.EqualValues(t, 30, *r.AiInferenceBurst())

	// Consent is NOT part of the cascade: it is a boolean gate with no default to
	// inherit, so a tier can never grant it.
	require.False(t, r.AiExternalEnabled())
}
