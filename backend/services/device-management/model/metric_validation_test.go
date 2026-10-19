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
		// The store holds decimal(20,8): NaN passes every comparison and +/-Inf and 1e12 or more
		// can never be written, so each is refused here rather than at the INSERT.
		{"double NaN", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "NaN", false},
		{"double +Inf", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "+Inf", false},
		{"double -Inf", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "-Inf", false},
		{"double NaN under bounds", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE",
			MaxValue: sql.NullFloat64{Float64: 100, Valid: true}}, "NaN", false},
		{"double 1e12", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "1e12", false},
		{"double -1.25e+21", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "-1.25e+21", false},
		{"double 999999999999", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "999999999999", true},
		{"double 1e-12", &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}, "1e-12", true},
		{"int 1e12", &MetricDefinition{MetricKey: "rpm", DataType: "INT"}, "1000000000000", false},
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
		// The bounds are inclusive, and nothing past them is let through.
		{
			"at max",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", MaxValue: sql.NullFloat64{Float64: 100, Valid: true}},
			"100", true,
		},
		{
			"just above max",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", MaxValue: sql.NullFloat64{Float64: 100, Valid: true}},
			"100.5", false,
		},
		{
			"at min",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", MinValue: sql.NullFloat64{Float64: 0, Valid: true}},
			"0", true,
		},
		{
			"just below min",
			&MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", MinValue: sql.NullFloat64{Float64: 0, Valid: true}},
			"-0.5", false,
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
			// Through the projection resolution caches, so a field it dropped fails here.
			resolved := tc.def.Resolved()
			err := ValidateMetricValue(&resolved, tc.val)
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
	defs := NewProfileResolution(ProfileScope{}, []*MetricDefinition{{MetricKey: "temp", DataType: "DOUBLE",
		MaxValue: sql.NullFloat64{Float64: 100, Valid: true}}})
	if err := defs.ValidateMeasurement("humidity", "not-a-number"); err != nil {
		t.Fatalf("undeclared key must pass through unvalidated, got: %v", err)
	}
	if err := defs.ValidateMeasurement("temp", "42"); err != nil {
		t.Fatalf("declared conforming value should pass, got: %v", err)
	}
	if err := defs.ValidateMeasurement("temp", "150"); err == nil {
		t.Fatalf("declared out-of-bounds value should be rejected")
	}
}
