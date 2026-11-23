// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
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
