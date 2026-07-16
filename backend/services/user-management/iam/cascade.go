// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"github.com/devicechain-io/dc-microservice/governance"
)

// The ADR-065 decision 5 cascade — tenant override → tier → platform default —
// resolved ONCE, here, for every reader.
//
// Two surfaces need it and they must never disagree: the data-plane
// tenantGovernance query an enforcing service reads its ceiling from, and the admin
// plane's effective-settings view an operator reads to answer "what is this tenant
// actually metered at, and why?". If the second re-implemented the cascade, the
// console would eventually tell an operator something the platform does not do —
// the worst possible failure for a screen whose entire job is to be the truth about
// packaging.
//
// The platform default is deliberately NOT a value here, only a SOURCE. It is not
// user-management's to know: each enforcing service constructs its own default from
// its own Helm config (event-sources for ingest, ai-inference for AI drafting), and
// the outbound dimension has TWO independent copies — event-processing charges at
// the REACT source, outbound-connectors at the sink (ADR-060 SD-3) — which may
// legally differ. So the cascade resolves to "nil, and the reason is
// SourcePlatformDefault"; the consumer folds that onto its own number, and the admin
// plane says "Platform default" rather than inventing one. A copy of it here would
// be a third statement of a number this service does not own, drifting silently the
// first time an operator edits a chart value.

// SettingSource names which level of the cascade produced an effective setting. It
// is the provenance half of decision 7: effective settings must stay inspectable as
// tier + delta, never an opaque merged blob, so a value never travels without the
// reason it won.
type SettingSource string

const (
	// SourceOverride: the tenant carries a usable per-tenant override, which is an
	// audited exception to its tier rather than part of what the tier sells.
	SourceOverride SettingSource = "override"
	// SourceTier: no override, so the tenant's packaging decides.
	SourceTier SettingSource = "tier"
	// SourcePlatformDefault: neither level declares one. The effective value is the
	// enforcing service's own default, which is a real ceiling and never unlimited
	// (ADR-023), but is not a number this service can state.
	SourcePlatformDefault SettingSource = "platform-default"
)

// overridesFor maps a dimension to the tenant's override columns, reporting false
// for a dimension this tenant row has no columns for.
//
// This mapping is hand-written and cannot be derived — the overrides are typed
// struct fields, so something must connect "the ingest dimension" to
// IngestMessagesPerSecond. What matters is that it exists in exactly ONE place and
// FAILS LOUD rather than silent: a dimension with no columns returns false, and
// TestEveryDimensionHasOverrideColumns turns that into a build-time failure the day
// a fourth dimension is declared without them. The alternative — returning nil, nil
// and letting it look like "no override declared" — would ship a dimension whose
// per-tenant overrides silently did nothing.
func (t *Tenant) overridesFor(dim governance.Dimension) (rate *float64, burst *int, ok bool) {
	switch dim.Name {
	case governance.Ingest.Name:
		return t.IngestMessagesPerSecond, t.IngestBurst, true
	case governance.Outbound.Name:
		return t.OutboundMessagesPerSecond, t.OutboundBurst, true
	case governance.AIInference.Name:
		return t.AiInferenceRequestsPerMinute, t.AiInferenceBurst, true
	}
	return nil, nil, false
}

// OverrideRate returns the tenant's own rate override for a dimension, or nil when
// it declares none (or declares an unusable one — see EffectiveRate). Exposed so the
// admin plane can show the delta beside the tier's value.
func (t *Tenant) OverrideRate(dim governance.Dimension) *float64 {
	rate, _, ok := t.overridesFor(dim)
	if !ok || rate == nil || !UsableRate(*rate) {
		return nil
	}
	return rate
}

// OverrideBurst is OverrideRate for a burst.
func (t *Tenant) OverrideBurst(dim governance.Dimension) *int {
	_, burst, ok := t.overridesFor(dim)
	if !ok || burst == nil || !UsableBurst(*burst) {
		return nil
	}
	return burst
}

// EffectiveRate resolves one dimension's rate down the cascade, returning the value
// and the level that produced it. A nil value means SourcePlatformDefault.
//
// An override only wins if it is USABLE. An unusable one (non-positive) is
// unreachable through the API — the write path rejects it — so it can only arrive by
// a direct DB write; but passing it through would send it to the consumer, which
// floors an unusable value to the PLATFORM DEFAULT and would thereby skip the tier
// entirely. That is the one case where "the consumer already folds onto the default"
// is not equivalent to decision 5's cascade: a gold tenant carrying a junk -5
// override would meter at 1000/s instead of its tier's 2000/s. Judging it here keeps
// the levels in the order the ADR specifies.
//
// A tenant whose tier is not preloaded degrades to override-or-platform-default —
// the fail-safe direction, since a missed preload then costs a tier's tuning rather
// than a ceiling. The tier is a required FK and every read path preloads it.
func (t *Tenant) EffectiveRate(dim governance.Dimension) (*float64, SettingSource) {
	if v := t.OverrideRate(dim); v != nil {
		return v, SourceOverride
	}
	if v := t.Tier.RateFor(dim); v != nil {
		return v, SourceTier
	}
	return nil, SourcePlatformDefault
}

// EffectiveBurst resolves one dimension's burst the same way.
func (t *Tenant) EffectiveBurst(dim governance.Dimension) (*int, SettingSource) {
	if v := t.OverrideBurst(dim); v != nil {
		return v, SourceOverride
	}
	if v := t.Tier.BurstFor(dim); v != nil {
		return v, SourceTier
	}
	return nil, SourcePlatformDefault
}
