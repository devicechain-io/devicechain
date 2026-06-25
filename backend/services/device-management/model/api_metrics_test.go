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
