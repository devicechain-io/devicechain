// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/devicechain-io/dc-microservice/governance"
)

// The tenant-tier config key registry (ADR-065 decision 8, the ADR-061 FacetKey
// pattern): every key a tier may carry is declared here with a validator, and an
// unrecognized key is REJECTED AT WRITE.
//
// This is the whole reason a tier's settings are a validated blob rather than a
// free-form one. A JSON blob is runtime and unvalidated, so a typo — `shedPriorty`,
// `ingestMessagesPerSec` — would be accepted, read by nobody, and silently do
// NOTHING: the tenant quietly keeps the platform default while an operator believes
// they sold and configured a ceiling. That is fail-OPEN, which ADR-023 forbids.
// Rejecting at write turns a typo into an error the operator sees immediately,
// instead of a support ticket six months later.
//
// The keys are DERIVED from governance.AllDimensions() rather than restated, so a
// tier key is by construction the same name as the wire field the enforcing service
// reads, and a fourth dimension gets tier support the day it is declared.

// tierConfigKey is one registered key: its validator and how it is read back.
type tierConfigKey struct {
	// validate rejects a value that is not usable for this key. It is the same bar
	// the per-tenant override columns are held to (see admin.GovernanceOverrides):
	// a ceiling must be a positive number, because a zero one admits nothing.
	validate func(v any) error
}

// ShedPriorityConfigKey is the tier config key carrying the tier's ADR-063
// shed-priority default (ADR-065 S6) — the int 1–100 a tenant at this tier inherits
// unless it carries its own override. It is registered OUTSIDE the dimension loop
// below because it is a scalar preference, not a rate+burst governance dimension:
// forcing it into a Dimension would fabricate a meaningless burst field and a bogus
// rate unit. This is the shed priority the TenantTier doc comment and the
// display_order warning both reserve as "a separate field with its own meaning".
const ShedPriorityConfigKey = "shedPriority"

// HeldCommandCeilingConfigKey is the tier config key carrying the tier's default bound
// on how many commands a tenant may have parked in the HELD state — the state
// command-delivery puts a command in when the platform is DELIBERATELY withholding
// dispatch because the device is known absent (ADR-023 governance, packaged per
// ADR-065). An offline fleet's backlog accumulates there and can sit for days, so how
// much of it a tenant's packaging entitles it to is exactly a tier question.
//
// Registered OUTSIDE the dimension loop below, like ShedPriorityConfigKey and for the
// same reason: it is a standalone scalar, not a rate+burst governance dimension. A held
// backlog has no burst and no per-second unit, and forcing it into a Dimension would
// fabricate both.
//
// It differs from shedPriority in what KIND of scalar it is, which is why it does not
// share that key's validator: this is a CEILING (how much), so any positive whole
// number is meaningful and larger is simply more; shedPriority is a POINT on a fixed
// 1–100 band scale, where 101 names no band at all.
const HeldCommandCeilingConfigKey = "heldCommandCeiling"

// The three geofence config keys (ADR-023 governance, packaged per ADR-065). Registered
// OUTSIDE the dimension loop below like the two scalars above, and for the same reason: a
// position count has no burst and no per-second unit.
//
// 🔴 THEY ARE THE FIRST TIER KEYS WITH A SEMANTIC MAXIMUM, and that is a deliberate break
// with every key beside them. validatePositiveBurst stops at MaxInt32 because the value
// crosses a GraphQL Int; validateHeldCommandCeiling's own comment argues AGAINST an upper
// bound, because a held backlog is tenant-local durable storage and a tier granting a huge
// one spends only that tenant's disk. A geofence set is different in kind: it is compiled and
// retained in event-processing, a process every tenant SHARES, and its manifest crosses a
// broker with a per-message ceiling. An operator who types an extra zero here does not
// overprovision one tenant, they degrade the instance. The maxima live in core/governance
// beside the defaults they bound, so the two doors that enforce them cannot disagree about
// the number.
//
// The NAMES are derived from core/governance too, rather than spelled here. Each of these
// strings is the same name in three places — the tier config key, the per-tenant override
// field, and the field device-management selects off tenantGovernance — and that sameness is
// the legibility of the whole cascade, not a coincidence. Deriving them is the pattern
// AllDimensions() already establishes for the rate and burst keys.
//
// THREE keys rather than one, because a tenant's geofence footprint has three independent
// costs: a per-fence compile, a per-set wire size, and a per-tenant share of a shared cache.
// No one number bounds all three — see the constants in core/governance for the measurement
// each was read off.
const (
	// GeoFenceVertexCeilingConfigKey bounds ONE fence's total position count across every
	// ring. It bounds a COMPILE, which is O(V²).
	GeoFenceVertexCeilingConfigKey = governance.GeoFenceVertexCeilingField
	// GeoFenceCeilingConfigKey bounds how many fences the tenant may hold. It bounds a WIRE
	// size: the fence-set manifest carries one {token, hash} pair per fence and must fit a
	// single broker message.
	GeoFenceCeilingConfigKey = governance.GeoFenceCeilingField
	// GeoFenceVertexBudgetConfigKey bounds the total position count across the tenant's
	// WHOLE current fence set. It bounds FOOTPRINT in the shared DETECT geometry cache —
	// the cost neither of the other two can express, since a ceiling on one fence bounds a
	// compile and a ceiling on the count bounds a manifest.
	GeoFenceVertexBudgetConfigKey = governance.GeoFenceVertexBudgetField
)

// tierConfigKeys is the registry, keyed by config-blob key name.
var tierConfigKeys = buildTierConfigKeys()

// buildTierConfigKeys registers, for every governance dimension, its rate key (a
// positive number) and its burst key (a positive integer) — both the dimension's own
// field names — plus the standalone non-dimension keys (ADR-063 shedPriority and the
// HELD-command ceiling), which are registered explicitly because they are scalars with
// no rate/burst pair to derive.
func buildTierConfigKeys() map[string]tierConfigKey {
	keys := make(map[string]tierConfigKey)
	for _, d := range governance.AllDimensions() {
		keys[d.RateField] = tierConfigKey{validate: validatePositiveRate}
		keys[d.BurstField] = tierConfigKey{validate: validatePositiveBurst}
	}
	keys[ShedPriorityConfigKey] = tierConfigKey{validate: validateShedPriority}
	keys[HeldCommandCeilingConfigKey] = tierConfigKey{validate: validateHeldCommandCeiling}
	keys[GeoFenceVertexCeilingConfigKey] = tierConfigKey{validate: validateGeoFenceVertexCeiling}
	keys[GeoFenceCeilingConfigKey] = tierConfigKey{validate: validateGeoFenceCeiling}
	keys[GeoFenceVertexBudgetConfigKey] = tierConfigKey{validate: validateGeoFenceVertexBudget}
	return keys
}

// TierConfigKeys returns the registered key names, sorted — for error messages and
// for an operator-facing "what may a tier carry?" listing.
func TierConfigKeys() []string {
	out := make([]string, 0, len(tierConfigKeys))
	for k := range tierConfigKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UsableRate reports whether a per-tenant rate override is a live ceiling, by the
// same rule a tier setting is held to. It exists so the cascade can tell an override
// that MEANS something from one that does not: an unusable override must fall
// through to the tier (ADR-065 D5's next level), not past it to the platform
// default. Only an out-of-band DB write can produce one — the API rejects it.
func UsableRate(v float64) bool { return validatePositiveRate(v) == nil }

// UsableBurst is UsableRate for a burst.
func UsableBurst(v int) bool { return validatePositiveBurst(v) == nil }

// ValidateTierConfig rejects a tier config carrying an unknown key or an unusable
// value. A nil/empty config is valid: a tier that declares nothing simply inherits
// the platform default for every dimension, which is exactly what the standard tier
// does.
//
// Unknown keys are rejected rather than ignored — see the package comment above.
// The error names the registered keys, because the overwhelmingly likely cause is a
// typo and the operator needs to see the right spelling, not just "invalid".
func ValidateTierConfig(cfg map[string]any) error {
	for k, v := range cfg {
		key, ok := tierConfigKeys[k]
		if !ok {
			return fmt.Errorf("unknown tier setting %q (known settings: %v)", k, TierConfigKeys())
		}
		if err := key.validate(v); err != nil {
			return fmt.Errorf("tier setting %q: %w", k, err)
		}
	}
	return nil
}

// toFloat coerces any numeric config value to a float64.
//
// It judges a NUMBER, not a float64, and both the validators and the readers below
// go through it so the two can never disagree about what counts as one. That
// matters because the same key arrives as different Go types depending on the door:
// the GraphQL write path and the gorm json round-trip both decode to float64
// (encoding/json's default), but a seed is whatever literal its author typed — and
// `"ingestBurst": 4000` is an int. Insisting on float64 would fail a migration at
// boot over a type nobody thinks about while reading a table of numbers.
//
// json.Number is accepted for the same reason: it costs nothing here, and it is what
// a decoder configured with UseNumber would hand us.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// validatePositiveRate accepts a finite positive number. A non-positive rate would
// hand core.TenantRateLimiter a bucket that admits NOTHING — an outage for every
// tenant at the tier — so inherit-the-platform-default is the only safe reading of
// one, and it must be spelled by omitting the key rather than zeroing it.
func validatePositiveRate(v any) error {
	f, ok := toFloat(v)
	if !ok {
		return fmt.Errorf("must be a number (got %T)", v)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("must be a finite number (got %v)", f)
	}
	if f <= 0 {
		return fmt.Errorf("must be positive (got %v); omit it to inherit the platform default", f)
	}
	return nil
}

// validatePositiveBurst accepts a positive whole number. A fractional burst is not
// an integer count and must not silently truncate into a live ceiling.
func validatePositiveBurst(v any) error {
	f, ok := toFloat(v)
	if !ok {
		return fmt.Errorf("must be a number (got %T)", v)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("must be a finite number (got %v)", f)
	}
	if f != math.Trunc(f) {
		return fmt.Errorf("must be a whole number (got %v)", f)
	}
	if f <= 0 || f > math.MaxInt32 {
		return fmt.Errorf("must be positive (got %v); omit it to inherit the platform default", f)
	}
	return nil
}

// validateShedPriority accepts a whole number in [1, 100] — the ADR-063 shed-priority
// band. Unlike a rate/burst ceiling, zero is not "inherit the default" here (an
// omitted key is); a shed priority is a point on a fixed 1–100 scale, and a value
// outside it names no band. Held to the same "reject at write" bar as the ceilings so
// a typo is an error the operator sees, not a silent inherit.
func validateShedPriority(v any) error {
	f, ok := toFloat(v)
	if !ok {
		return fmt.Errorf("must be a number (got %T)", v)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("must be a finite number (got %v)", f)
	}
	if f != math.Trunc(f) {
		return fmt.Errorf("must be a whole number (got %v)", f)
	}
	if f < 1 || f > 100 {
		return fmt.Errorf("must be between 1 and 100 (got %v); omit it to inherit the platform default", f)
	}
	return nil
}

// validateHeldCommandCeiling accepts a positive whole number: a bound on how many
// commands a tenant may hold, so it is a COUNT and a fractional one is not a count.
//
// It is deliberately the validatePositiveBurst rule and not the validateShedPriority
// one, and it delegates rather than restating so the two cannot drift. Shaping it on
// the band validator would be the tempting mistake — both are "an integer on a tier" —
// but a ceiling has no upper bound in its meaning, only in its representation, and
// capping one at 100 would silently make every realistic fleet backlog unconfigurable.
// The MaxInt32 bound validatePositiveBurst applies is the representational one: the
// value round-trips through a GraphQL Int, so a larger number would be truncated at the
// wire rather than rejected at the door.
//
// Zero is not "inherit" here — an omitted key is. A zero ceiling would mean the tenant
// may hold NOTHING, refusing every command to an absent device across the whole tier,
// so it is rejected at write like every other ceiling (ADR-065 decision 8) rather than
// quietly reinterpreted.
func validateHeldCommandCeiling(v any) error { return validatePositiveBurst(v) }

// validateBoundedCount accepts a positive whole number no larger than max — the burst rule
// plus a semantic upper bound.
//
// It DELEGATES to validatePositiveBurst rather than restating it, so the two cannot drift on
// what counts as a number, what counts as whole, and where the representational MaxInt32 wall
// is. What it adds is the part validatePositiveBurst deliberately does not have, and the
// error says why the bound exists rather than only what it is: an operator who has just been
// refused needs to know whether to type a smaller number or to raise a platform constant.
func validateBoundedCount(v any, max int, unit, because string) error {
	if err := validatePositiveBurst(v); err != nil {
		return err
	}
	f, _ := toFloat(v) // validatePositiveBurst already proved this coerces
	if int(f) > max {
		return fmt.Errorf("must be at most %d %s (got %v); %s", max, unit, v, because)
	}
	return nil
}

// validateGeoFenceVertexCeiling accepts a positive whole number of positions up to
// governance.MaxGeoFenceVertexCeiling.
//
// The maximum is the one this codebase is most likely to be asked to raise, so the error
// names the cost rather than the rule: compiling a fence is quadratic in its position count,
// and the binding consumer is event-processing filling its geometry cache after an eviction,
// where the compile stalls that tenant's containment.
func validateGeoFenceVertexCeiling(v any) error {
	return validateBoundedCount(v, governance.MaxGeoFenceVertexCeiling, "positions per fence",
		"compiling one fence is quadratic in its position count, and above this the compile stalls "+
			"containment for the tenant every time event-processing refills its geometry cache")
}

// validateGeoFenceCeiling accepts a positive whole number of fences up to
// governance.MaxGeoFenceCeiling.
func validateGeoFenceCeiling(v any) error {
	return validateBoundedCount(v, governance.MaxGeoFenceCeiling, "fences",
		"a fence-set manifest carries one entry per fence and must fit inside one broker message")
}

// validateGeoFenceVertexBudget accepts a positive whole number of positions up to
// governance.MaxTenantGeometryVertices.
//
// This is the maximum whose error most needs to explain itself, because the resource it
// protects belongs to everyone: above it, one tenant may hold more than half the shared DETECT
// geometry cache and its refills can evict every other tenant's geometry.
func validateGeoFenceVertexBudget(v any) error {
	return validateBoundedCount(v, governance.MaxTenantGeometryVertices, "positions across the tenant's fence set",
		"the DETECT geometry cache is shared by every tenant on the instance, and above this one "+
			"tenant's fence set can evict every other tenant's geometry")
}

// HeldCommandCeiling returns the tier's default bound on a tenant's HELD-command
// backlog, or nil if the tier declares none (inherit the enforcing service's platform
// default — which is a real ceiling, never unlimited). Nil for a nil tier, like
// RateFor/BurstFor/ShedPriority, so a caller need not special-case an unloaded
// association. Read defensively through the same validator the write path uses: an
// out-of-band DB write parking a junk value here inherits rather than turning into a
// ceiling of zero, which would refuse every command to an absent device.
func (t *TenantTier) HeldCommandCeiling() *int {
	return t.positiveInt(HeldCommandCeilingConfigKey, validateHeldCommandCeiling)
}

// GeoFenceVertexCeiling returns the tier's default bound on ONE fence's position count, or nil
// if the tier declares none (inherit the platform default). Nil for a nil tier, like every
// accessor beside it, so a caller need not special-case an unloaded association.
//
// Read defensively through the same validator the write path uses, which here CLAMPS NOTHING
// and returns nil instead: an out-of-band DB write parking a value above the maximum inherits
// the platform default rather than becoming a cap larger than the platform will honour. The
// read-side clamp lives one layer out, in governance.resolveGeoFenceCap, where the wire value
// arrives; this one refuses.
func (t *TenantTier) GeoFenceVertexCeiling() *int {
	return t.positiveInt(GeoFenceVertexCeilingConfigKey, validateGeoFenceVertexCeiling)
}

// GeoFenceCeiling returns the tier's default bound on how many fences a tenant may hold, or
// nil to inherit.
func (t *TenantTier) GeoFenceCeiling() *int {
	return t.positiveInt(GeoFenceCeilingConfigKey, validateGeoFenceCeiling)
}

// GeoFenceVertexBudget returns the tier's default bound on the position count across a
// tenant's whole fence set, or nil to inherit.
func (t *TenantTier) GeoFenceVertexBudget() *int {
	return t.positiveInt(GeoFenceVertexBudgetConfigKey, validateGeoFenceVertexBudget)
}

// ShedPriority returns the tier's ADR-063 shed-priority default (1–100), or nil if
// the tier declares none (inherit the platform fail-safe). Nil for a nil tier, like
// RateFor/BurstFor, so a caller need not special-case an unloaded association. Read
// defensively through the same validator the write path uses — an out-of-band DB
// write parking a junk value here inherits rather than banding to a wrong class.
func (t *TenantTier) ShedPriority() *int {
	return t.positiveInt(ShedPriorityConfigKey, validateShedPriority)
}

// RateFor returns the tier's rate ceiling for a dimension, or nil if the tier
// declares none (inherit the platform default). Nil for a nil tier, so a caller
// need not special-case an unloaded association.
//
// Values are read defensively: the write path validates, but a direct out-of-band DB
// write could still park an unusable value here, and inheriting the platform default
// is the safe reading of one — never a live ceiling of zero, which would admit
// nothing (see governance.parseRate for the same reasoning on the wire).
func (t *TenantTier) RateFor(dim governance.Dimension) *float64 {
	return t.positiveNumber(dim.RateField, validatePositiveRate)
}

// BurstFor returns the tier's burst ceiling for a dimension, or nil to inherit.
func (t *TenantTier) BurstFor(dim governance.Dimension) *int {
	return t.positiveInt(dim.BurstField, validatePositiveBurst)
}

// positiveNumber reads a numeric config key, returning nil unless it is present and
// passes validate.
//
// It coerces through the same toFloat the validators use, deliberately: a validator
// that accepts a type the reader then drops would be worse than one that rejects it
// outright — the write would succeed, the setting would be visible in the API, and
// the tenant would silently keep the platform default.
func (t *TenantTier) positiveNumber(key string, validate func(any) error) *float64 {
	if t == nil || t.Config == nil {
		return nil
	}
	v, ok := t.Config[key]
	if !ok {
		return nil
	}
	if err := validate(v); err != nil {
		return nil
	}
	f, ok := toFloat(v)
	if !ok {
		return nil
	}
	return &f
}

// positiveInt is positiveNumber for the whole-number settings — every tier accessor
// except RateFor, which legitimately returns a float.
//
// The truncation to int is safe because each of these keys is validated as a whole
// number on both the write path and (via validate) here, so there is no fraction to
// lose. It exists because the same four-line tail was written out at each call site,
// and a tail repeated per setting is one that can be got subtly wrong on the next one.
func (t *TenantTier) positiveInt(key string, validate func(any) error) *int {
	f := t.positiveNumber(key, validate)
	if f == nil {
		return nil
	}
	i := int(*f)
	return &i
}
