// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// ResolvedMetric is the part of a published metric definition that event resolution
// reads: the key a measurement names, the classifier id stamped onto it, and what
// validation and the stored value's self-description need. It is what the per-device-type
// resolution cache holds, one per declared metric.
//
// 🔴 IT IS A PROJECTION BECAUSE THE WHOLE DEFINITION IS DECODED ON EVERY EVENT. The cache
// entry is read once per resolved event of every type, location and alert included, and a
// full MetricDefinition carries timestamps, soft-delete state, names, descriptions and
// metadata that resolution never reads but a JSON decode still pays for — several
// microseconds per definition, so a profile declaring a couple of hundred metrics would add
// over a millisecond to every event. The fields here are exactly the ones read below and in
// the resolver's classifier stamp; a new one is added by reading it through Resolved().
//
// 🔴 IT IS READ-ONLY, because the cached ProfileResolution that holds it is shared by every
// event of a device type: its one field is unexported, and the accessors return values. A
// new accessor must not hand out the Enum slice or a pointer into the definition.
type ResolvedMetric struct {
	m resolvedMetricWire
	// inputSlot is one more than this metric's position among the resolution's formula
	// inputs (calcPlan), or 0 when no formula of the version reads it — every metric of a
	// version with no formulas, and the zero value. Set once, by freeze; never cached.
	inputSlot int
}

// resolvedMetricWire is a resolved metric's fields, in the form the cache stores them.
type resolvedMetricWire struct {
	ID        uint     `json:"id"`
	MetricKey string   `json:"key"`
	DataType  string   `json:"type"`
	Unit      *string  `json:"unit,omitempty"`
	MinValue  *float64 `json:"min,omitempty"`
	MaxValue  *float64 `json:"max,omitempty"`
	// Enum is the allow-list, already parsed; nil when there is none, and also when the
	// stored one is malformed or empty (see checkEnum).
	Enum []string `json:"enum,omitempty"`
	// Expression is a calculated metric's formula source (ADR-082), nil for a measured one.
	// Only the source is cached: the compiled program is rebuilt by freeze on every decode,
	// so a cache entry never carries anything but text.
	Expression *string `json:"expr,omitempty"`
}

// Calculated reports whether the metric is computed from a formula (ADR-082) rather than
// reported by devices. A device-sent value under its key is dropped at resolve.
func (r *ResolvedMetric) Calculated() bool { return r.m.Expression != nil }

// ID is the definition's id, stamped onto a measurement as its classifier.
func (r *ResolvedMetric) ID() uint { return r.m.ID }

// Key is the metric key a measurement names.
func (r *ResolvedMetric) Key() string { return r.m.MetricKey }

// DataType is the declared data type.
func (r *ResolvedMetric) DataType() string { return r.m.DataType }

// Unit is the declared unit, and whether one is declared.
func (r *ResolvedMetric) Unit() (string, bool) {
	if r.m.Unit == nil {
		return "", false
	}
	return *r.m.Unit, true
}

// MinValue is the declared lower bound, and whether one is declared.
func (r *ResolvedMetric) MinValue() (float64, bool) {
	if r.m.MinValue == nil {
		return 0, false
	}
	return *r.m.MinValue, true
}

// MaxValue is the declared upper bound, and whether one is declared.
func (r *ResolvedMetric) MaxValue() (float64, bool) {
	if r.m.MaxValue == nil {
		return 0, false
	}
	return *r.m.MaxValue, true
}

// AllowedValueCount is the length of the allow-list ingest checks a value against: 0 when
// there is none.
func (r *ResolvedMetric) AllowedValueCount() int { return len(r.m.Enum) }

// AllowedValue is the i-th member of the allow-list (0 <= i < AllowedValueCount). Indexed
// rather than handed out as a slice, because the resolution holding this metric is shared.
func (r *ResolvedMetric) AllowedValue(i int) string { return r.m.Enum[i] }

// Resolved projects a metric definition onto what event resolution reads. It is the one
// place the projection is defined.
func (def *MetricDefinition) Resolved() ResolvedMetric {
	r := resolvedMetricWire{ID: def.ID, MetricKey: def.MetricKey, DataType: def.DataType}
	if def.Unit.Valid {
		unit := def.Unit.String
		r.Unit = &unit
	}
	if def.MinValue.Valid {
		min := def.MinValue.Float64
		r.MinValue = &min
	}
	if def.MaxValue.Valid {
		max := def.MaxValue.Float64
		r.MaxValue = &max
	}
	if def.Enum != nil {
		// Parsed here, once per cache fill, rather than on every validated value. A
		// malformed or empty list leaves Enum nil, which checkEnum reads as no constraint
		// — the same outcome parsing it per value always gave.
		var allowed []string
		if err := json.Unmarshal(*def.Enum, &allowed); err == nil && len(allowed) > 0 {
			r.Enum = allowed
		}
	}
	if def.Calculated() {
		expr := def.Expression.String
		r.Expression = &expr
	}
	return ResolvedMetric{m: r}
}

// ValidateMetricValue checks a measurement value against the metric definition it
// is declared under (ADR-016 ingest validation). It enforces, in order: the value
// parses as the declared DataType; for numeric types it falls within the optional
// MinValue/MaxValue bounds; and, when an Enum allow-list is declared, the value is
// one of its members. A nil error means the value conforms. The metric key is
// included in the error so a dead-lettered event is self-describing.
//
// Validation is value-shaped, not unit-converting: Unit is metadata exposed
// through the API, not coerced here (normalization is a later step).
func ValidateMetricValue(def *ResolvedMetric, value string) error {
	switch MetricDataType(def.m.DataType) {
	case MetricDouble:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.m.MetricKey, value, def.m.DataType)
		}
		if err := CheckStorableMeasurement(f); err != nil {
			return fmt.Errorf("metric %q: %q: %w", def.m.MetricKey, value, err)
		}
		if err := checkBounds(def, f); err != nil {
			return err
		}
	case MetricInt:
		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.m.MetricKey, value, def.m.DataType)
		}
		if err := CheckStorableMeasurement(float64(i)); err != nil {
			return fmt.Errorf("metric %q: %q: %w", def.m.MetricKey, value, err)
		}
		if err := checkBounds(def, float64(i)); err != nil {
			return err
		}
	case MetricBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.m.MetricKey, value, def.m.DataType)
		}
	case MetricString:
		// Any string parses; only the optional Enum constrains it (checked below).
	default:
		// An unknown declared DataType is a profile misconfiguration, not a device
		// fault; do not reject the device's data on its behalf.
		return nil
	}
	return checkEnum(def, value)
}

// CheckStorableMeasurement refuses a numeric measurement value the event store cannot hold:
// NaN or +/-Inf, and a magnitude of calcfield.StoredMagnitude or more (the column is
// decimal(20,8)). ParseFloat accepts "NaN" and "Inf", and NaN fails every comparison, so
// without this a declared Min/Max would pass it. Refusing here is the same outcome the
// store would reach later, but for one event instead of after three extra transactions,
// and NaN would not be refused by the store at all: the column takes it, and it then
// poisons every average and rollup over its bucket.
func CheckStorableMeasurement(f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("is not a finite number")
	}
	if math.Abs(f) >= storedMeasurementMagnitude {
		return fmt.Errorf("is beyond the storable range (magnitude must be below %g)", storedMeasurementMagnitude)
	}
	return nil
}

// checkBounds enforces the optional MinValue/MaxValue numeric bounds.
func checkBounds(def *ResolvedMetric, v float64) error {
	if def.m.MinValue != nil && v < *def.m.MinValue {
		return fmt.Errorf("metric %q: %v is below the minimum %v", def.m.MetricKey, v, *def.m.MinValue)
	}
	if def.m.MaxValue != nil && v > *def.m.MaxValue {
		return fmt.Errorf("metric %q: %v is above the maximum %v", def.m.MetricKey, v, *def.m.MaxValue)
	}
	return nil
}

// checkEnum enforces the optional Enum allow-list (the definition's JSON array of
// permitted values, compared as strings against the raw value). An absent or empty Enum
// imposes no constraint; a malformed Enum is treated as no constraint (a profile fault
// must not reject device data) — Resolved() leaves both nil.
func checkEnum(def *ResolvedMetric, value string) error {
	if len(def.m.Enum) == 0 {
		return nil
	}
	for _, a := range def.m.Enum {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("metric %q: %q is not one of the allowed values %v", def.m.MetricKey, value, def.m.Enum)
}
