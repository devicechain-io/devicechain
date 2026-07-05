// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ValidateMetricValue checks a measurement value against the metric definition it
// is declared under (ADR-016 ingest validation). It enforces, in order: the value
// parses as the declared DataType; for numeric types it falls within the optional
// MinValue/MaxValue bounds; and, when an Enum allow-list is declared, the value is
// one of its members. A nil error means the value conforms. The metric key is
// included in the error so a dead-lettered event is self-describing.
//
// Validation is value-shaped, not unit-converting: Unit is metadata exposed
// through the API, not coerced here (normalization is a later step).
func ValidateMetricValue(def *MetricDefinition, value string) error {
	switch MetricDataType(def.DataType) {
	case MetricDouble:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.MetricKey, value, def.DataType)
		}
		if err := checkBounds(def, f); err != nil {
			return err
		}
	case MetricInt:
		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.MetricKey, value, def.DataType)
		}
		if err := checkBounds(def, float64(i)); err != nil {
			return err
		}
	case MetricBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("metric %q: %q is not a valid %s", def.MetricKey, value, def.DataType)
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
func checkBounds(def *MetricDefinition, v float64) error {
	if def.MinValue.Valid && v < def.MinValue.Float64 {
		return fmt.Errorf("metric %q: %v is below the minimum %v", def.MetricKey, v, def.MinValue.Float64)
	}
	if def.MaxValue.Valid && v > def.MaxValue.Float64 {
		return fmt.Errorf("metric %q: %v is above the maximum %v", def.MetricKey, v, def.MaxValue.Float64)
	}
	return nil
}

// checkEnum enforces the optional Enum allow-list (a JSON array of permitted
// values, compared as strings against the raw value). An absent or empty Enum
// imposes no constraint; a malformed Enum is treated as no constraint (a profile
// fault must not reject device data).
func checkEnum(def *MetricDefinition, value string) error {
	if def.Enum == nil {
		return nil
	}
	var allowed []string
	if err := json.Unmarshal(*def.Enum, &allowed); err != nil || len(allowed) == 0 {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("metric %q: %q is not one of the allowed values %v", def.MetricKey, value, allowed)
}

// ValidateMeasurement looks up the definition for a measurement by its key in defs
// and validates the value against it. Lenient by design (ADR-016): a measurement
// whose key is not declared on the profile passes through unvalidated, so the
// metric model is an additive typing layer rather than a strict allow-list. defs
// is the device's resolved metric definitions (device → type → profile, ADR-045),
// keyed by MetricKey.
func ValidateMeasurement(defs map[string]*MetricDefinition, name string, value string) error {
	def, declared := defs[name]
	if !declared {
		return nil
	}
	return ValidateMetricValue(def, value)
}
