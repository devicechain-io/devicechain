// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
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
}

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
		if err := checkBounds(def, f); err != nil {
			return err
		}
	case MetricInt:
		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.m.MetricKey, value, def.m.DataType)
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
