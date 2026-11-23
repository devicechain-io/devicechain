// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"
)

// MetricDataType.Valid accepts only the known metric data-type vocabulary (ADR-016).
func TestMetricDataTypeValid(t *testing.T) {
	valid := []MetricDataType{MetricDouble, MetricInt, MetricBoolean, MetricString}
	for _, dt := range valid {
		if !dt.Valid() {
			t.Errorf("expected metric data type %q to be valid", dt)
		}
	}

	invalid := []MetricDataType{"", "double", "FLOAT", "LONG", "JSON", "DOUBLE "}
	for _, dt := range invalid {
		if dt.Valid() {
			t.Errorf("expected metric data type %q to be invalid", dt)
		}
	}
}

// StorableAsMetric accepts the numeric types (DOUBLE/INT/BOOLEAN, stored in the numeric column)
// and STRING (stored as text); anything else is not a reading type.
func TestMetricDataTypeStorableAsMetric(t *testing.T) {
	storable := []MetricDataType{MetricDouble, MetricInt, MetricBoolean, MetricString}
	for _, dt := range storable {
		if !dt.StorableAsMetric() {
			t.Errorf("expected metric data type %q to be storable", dt)
		}
	}
	notStorable := []MetricDataType{"", "OBJECT", "string"}
	for _, dt := range notStorable {
		if dt.StorableAsMetric() {
			t.Errorf("expected metric data type %q to not be storable", dt)
		}
	}
}

func TestStorableStringValue(t *testing.T) {
	for _, ok := range []string{"", "heating", "déjà vu 中", strings.Repeat("a", MaxStringMetricBytes)} {
		if !StorableStringValue(ok) {
			t.Errorf("expected %q to be storable", ok)
		}
	}
	for name, bad := range map[string]string{
		"over the bound": strings.Repeat("a", MaxStringMetricBytes+1),
		"a NUL byte":     "a\x00b",
		"invalid UTF-8":  "a\xffb",
	} {
		if StorableStringValue(bad) {
			t.Errorf("expected a value with %s to be refused", name)
		}
	}
}
