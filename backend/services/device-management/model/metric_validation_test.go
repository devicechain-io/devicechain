// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"

	"gorm.io/datatypes"
)

func enumJSON(s string) *datatypes.JSON {
	j := datatypes.JSON([]byte(s))
	return &j
}

func TestValidateMetricValue(t *testing.T) {
	cases := []struct {
		name string
		def  *MetricDefinition
		val  string
		ok   bool
	}{
		{"double ok", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "21.5", true},
		{"double non-numeric", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "warm", false},
		{"int ok", &MetricDefinition{MetricKey: "rpm", DataType: "INT"}, "1200", true},
		{"int rejects float", &MetricDefinition{MetricKey: "rpm", DataType: "INT"}, "12.5", false},
		{"bool ok", &MetricDefinition{MetricKey: "on", DataType: "BOOLEAN"}, "true", true},
		{"bool bad", &MetricDefinition{MetricKey: "on", DataType: "BOOLEAN"}, "yes", false},
		{"string ok", &MetricDefinition{MetricKey: "mode", DataType: "STRING"}, "anything", true},
		{
			"below min",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", MinValue: sql.NullFloat64{Float64: 0, Valid: true}},
			"-5", false,
		},
		{
			"above max",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", MaxValue: sql.NullFloat64{Float64: 100, Valid: true}},
			"120", false,
		},
		{
			"within bounds",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE",
				MinValue: sql.NullFloat64{Float64: 0, Valid: true}, MaxValue: sql.NullFloat64{Float64: 100, Valid: true}},
			"42", true,
		},
		{
			"enum member",
			&MetricDefinition{MetricKey: "mode", DataType: "STRING", Enum: enumJSON(`["eco","sport"]`)},
			"eco", true,
		},
		{
			"enum non-member",
			&MetricDefinition{MetricKey: "mode", DataType: "STRING", Enum: enumJSON(`["eco","sport"]`)},
			"turbo", false,
		},
		{
			"malformed enum imposes no constraint",
			&MetricDefinition{MetricKey: "mode", DataType: "STRING", Enum: enumJSON(`not-json`)},
			"anything", true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMetricValue(tc.def, tc.val)
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

// ValidateMeasurement is lenient: an undeclared key passes; a declared key is
// validated against its definition.
func TestValidateMeasurementLenient(t *testing.T) {
	defs := map[string]*MetricDefinition{
		"temp": {MetricKey: "temp", DataType: "DOUBLE", MaxValue: sql.NullFloat64{Float64: 100, Valid: true}},
	}
	if err := ValidateMeasurement(defs, "humidity", "not-a-number"); err != nil {
		t.Fatalf("undeclared key must pass through unvalidated, got: %v", err)
	}
	if err := ValidateMeasurement(defs, "temp", "42"); err != nil {
		t.Fatalf("declared conforming value should pass, got: %v", err)
	}
	if err := ValidateMeasurement(defs, "temp", "150"); err == nil {
		t.Fatalf("declared out-of-bounds value should be rejected")
	}
}
