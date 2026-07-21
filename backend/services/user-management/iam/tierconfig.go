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

// tierConfigKeys is the registry, keyed by config-blob key name.
var tierConfigKeys = buildTierConfigKeys()

// buildTierConfigKeys registers, for every governance dimension, its rate key (a
// positive number) and its burst key (a positive integer) — both the dimension's own
// field names — plus the standalone ADR-063 shedPriority key.
func buildTierConfigKeys() map[string]tierConfigKey {
	keys := make(map[string]tierConfigKey)
	for _, d := range governance.AllDimensions() {
		keys[d.RateField] = tierConfigKey{validate: validatePositiveRate}
		keys[d.BurstField] = tierConfigKey{validate: validatePositiveBurst}
	}
	keys[ShedPriorityConfigKey] = tierConfigKey{validate: validateShedPriority}
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

// ShedPriority returns the tier's ADR-063 shed-priority default (1–100), or nil if
// the tier declares none (inherit the platform fail-safe). Nil for a nil tier, like
// RateFor/BurstFor, so a caller need not special-case an unloaded association. Read
// defensively through the same validator the write path uses — an out-of-band DB
// write parking a junk value here inherits rather than banding to a wrong class.
func (t *TenantTier) ShedPriority() *int {
	f := t.positiveNumber(ShedPriorityConfigKey, validateShedPriority)
	if f == nil {
		return nil
	}
	i := int(*f)
	return &i
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
	f := t.positiveNumber(dim.BurstField, validatePositiveBurst)
	if f == nil {
		return nil
	}
	i := int(*f)
	return &i
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
