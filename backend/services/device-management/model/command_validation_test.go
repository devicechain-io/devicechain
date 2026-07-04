// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"testing"

	"gorm.io/datatypes"
)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

// defWithSchema builds a CommandDefinition whose ParameterSchema column holds the
// JSON encoding of the given descriptors (nil for a definition with no schema).
func defWithSchema(t *testing.T, key string, params []CommandParameter) *CommandDefinition {
	t.Helper()
	def := &CommandDefinition{CommandKey: key}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		j := datatypes.JSON(raw)
		def.ParameterSchema = &j
	}
	return def
}

func TestValidateParameterSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema []CommandParameter
		ok     bool
	}{
		{"empty schema", nil, true},
		{"simple scalar", []CommandParameter{{Name: "speed", DataType: MetricInt}}, true},
		{
			"all scalar types",
			[]CommandParameter{
				{Name: "a", DataType: MetricDouble}, {Name: "b", DataType: MetricInt},
				{Name: "c", DataType: MetricBoolean}, {Name: "d", DataType: MetricString},
			}, true,
		},
		{"empty name", []CommandParameter{{DataType: MetricInt}}, false},
		{
			"duplicate name",
			[]CommandParameter{{Name: "x", DataType: MetricInt}, {Name: "x", DataType: MetricString}}, false,
		},
		{"invalid data type", []CommandParameter{{Name: "x", DataType: "WIDGET"}}, false},
		{"scalar missing data type", []CommandParameter{{Name: "x"}}, false},
		{
			"bounds on non-numeric",
			[]CommandParameter{{Name: "x", DataType: MetricString, MinValue: f64(0)}}, false,
		},
		{
			"min greater than max",
			[]CommandParameter{{Name: "x", DataType: MetricInt, MinValue: f64(10), MaxValue: f64(1)}}, false,
		},
		{
			"default violates bounds",
			[]CommandParameter{{Name: "x", DataType: MetricInt, MaxValue: f64(10), Default: str("50")}}, false,
		},
		{
			"valid default within bounds",
			[]CommandParameter{{Name: "x", DataType: MetricInt, MinValue: f64(0), MaxValue: f64(10), Default: str("5")}}, true,
		},
		{
			"object with children",
			[]CommandParameter{{Name: "pt", Kind: CommandParamObject, Parameters: []CommandParameter{
				{Name: "lat", DataType: MetricDouble}, {Name: "lon", DataType: MetricDouble},
			}}}, true,
		},
		{
			"object declaring scalar constraint",
			[]CommandParameter{{Name: "pt", Kind: CommandParamObject, DataType: MetricInt}}, false,
		},
		{
			"scalar nesting children",
			[]CommandParameter{{Name: "x", DataType: MetricInt, Parameters: []CommandParameter{{Name: "y", DataType: MetricInt}}}}, false,
		},
		{
			"nested duplicate is caught",
			[]CommandParameter{{Name: "pt", Kind: CommandParamObject, Parameters: []CommandParameter{
				{Name: "lat", DataType: MetricDouble}, {Name: "lat", DataType: MetricDouble},
			}}}, false,
		},
		{"unknown kind", []CommandParameter{{Name: "x", Kind: "ARRAY", DataType: MetricInt}}, false},
		{"int enum valid", []CommandParameter{{Name: "g", DataType: MetricInt, Enum: []string{"1", "2"}}}, true},
		{"int enum non-int entry", []CommandParameter{{Name: "g", DataType: MetricInt, Enum: []string{"1", "two"}}}, false},
		{"string enum ok", []CommandParameter{{Name: "s", DataType: MetricString, Enum: []string{"a", "b"}}}, true},
		{"enum on boolean rejected", []CommandParameter{{Name: "b", DataType: MetricBoolean, Enum: []string{"true"}}}, false},
		{
			"default outside enum rejected",
			[]CommandParameter{{Name: "g", DataType: MetricString, Enum: []string{"a", "b"}, Default: str("c")}}, false,
		},
		{
			"default in enum ok",
			[]CommandParameter{{Name: "g", DataType: MetricString, Enum: []string{"a", "b"}, Default: str("a")}}, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateParameterSchema(tc.schema)
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

func TestValidateCommandPayload(t *testing.T) {
	schema := []CommandParameter{
		{Name: "speed", DataType: MetricInt, Required: true, MinValue: f64(0), MaxValue: f64(100)},
		{Name: "gear", DataType: MetricString, Enum: []string{"low", "high"}},
		{Name: "engage", DataType: MetricBoolean},
		{Name: "target", Kind: CommandParamObject, Parameters: []CommandParameter{
			{Name: "lat", DataType: MetricDouble, Required: true},
			{Name: "lon", DataType: MetricDouble, Required: true},
		}},
	}
	def := defWithSchema(t, "drive", schema)

	cases := []struct {
		name    string
		payload string
		ok      bool
	}{
		{"valid full", `{"speed":50,"gear":"low","engage":true,"target":{"lat":1.0,"lon":2.0}}`, true},
		{"valid minimal required only", `{"speed":10}`, true},
		{"missing required", `{"gear":"low"}`, false},
		{"unknown key", `{"speed":10,"turbo":true}`, false},
		{"int accepts integral float", `{"speed":10.0}`, true},
		{"int accepts exponent", `{"speed":5e1}`, true},
		{"int given fractional", `{"speed":10.5}`, false},
		{"trailing garbage rejected", `{"speed":10}xyz`, false},
		{"below min", `{"speed":-5}`, false},
		{"above max", `{"speed":500}`, false},
		{"enum non-member", `{"speed":10,"gear":"reverse"}`, false},
		{"enum member", `{"speed":10,"gear":"high"}`, true},
		{"bool given string", `{"speed":10,"engage":"yes"}`, false},
		{"nested valid", `{"speed":10,"target":{"lat":1.5,"lon":2.5}}`, true},
		{"nested missing required child", `{"speed":10,"target":{"lat":1.5}}`, false},
		{"nested unknown child", `{"speed":10,"target":{"lat":1.5,"lon":2.5,"alt":3}}`, false},
		{"nested not an object", `{"speed":10,"target":42}`, false},
		{"optional absent ok", `{"speed":10,"gear":"low"}`, true},
		{"null optional ok", `{"speed":10,"gear":null}`, true},
		{"null required rejected", `{"speed":null}`, false},
		{"not an object", `[1,2,3]`, false},
		{"malformed json", `{"speed":`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCommandPayload(def, []byte(tc.payload))
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

// A definition with no schema accepts any well-formed JSON object (the
// backward-compatible free-form path), and an empty payload is accepted when no
// required parameters are declared.
func TestValidateCommandPayloadFreeForm(t *testing.T) {
	free := defWithSchema(t, "legacy", nil)
	if err := ValidateCommandPayload(free, []byte(`{"anything":123,"nested":{"x":"y"}}`)); err != nil {
		t.Fatalf("no-schema definition must accept free-form payload, got: %v", err)
	}
	if err := ValidateCommandPayload(free, nil); err != nil {
		t.Fatalf("no-schema definition must accept an empty payload, got: %v", err)
	}

	optional := defWithSchema(t, "ping", []CommandParameter{{Name: "note", DataType: MetricString}})
	if err := ValidateCommandPayload(optional, nil); err != nil {
		t.Fatalf("empty payload with only optional params should pass, got: %v", err)
	}

	required := defWithSchema(t, "set", []CommandParameter{{Name: "value", DataType: MetricInt, Required: true}})
	if err := ValidateCommandPayload(required, nil); err == nil {
		t.Fatalf("empty payload with a required param should be rejected")
	}

	// A JSON null payload is treated the same as an absent one.
	if err := ValidateCommandPayload(optional, []byte(`null`)); err != nil {
		t.Fatalf("null payload with only optional params should pass, got: %v", err)
	}
	if err := ValidateCommandPayload(required, []byte(`null`)); err == nil {
		t.Fatalf("null payload with a required param should be rejected")
	}
}

// Numeric enums compare by value, so 1.50 satisfies an entry "1.5" and 10 an
// entry "10".
func TestValidateCommandPayloadNumericEnum(t *testing.T) {
	def := defWithSchema(t, "tune", []CommandParameter{
		{Name: "ratio", DataType: MetricDouble, Enum: []string{"1.5", "2.0"}},
		{Name: "level", DataType: MetricInt, Enum: []string{"1", "10"}},
	})
	cases := []struct {
		payload string
		ok      bool
	}{
		{`{"ratio":1.50}`, true},
		{`{"ratio":2}`, true},
		{`{"ratio":1.7}`, false},
		{`{"level":10}`, true},
		{`{"level":5}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.payload, func(t *testing.T) {
			err := ValidateCommandPayload(def, []byte(tc.payload))
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

// The declaration-time decode is strict: an unrecognized descriptor field is
// rejected rather than silently dropped.
func TestDecodeParameterSchemaStrict(t *testing.T) {
	if _, err := decodeParameterSchemaStrict([]byte(`[{"name":"x","dataType":"INT","maximum":10}]`)); err == nil {
		t.Fatalf("expected a typo'd descriptor key to be rejected")
	}
	schema, err := decodeParameterSchemaStrict([]byte(`[{"name":"x","dataType":"INT","maxValue":10}]`))
	if err != nil {
		t.Fatalf("well-formed schema should decode, got: %v", err)
	}
	if len(schema) != 1 || schema[0].Name != "x" {
		t.Fatalf("unexpected decode result: %+v", schema)
	}
	if s, err := decodeParameterSchemaStrict(nil); err != nil || s != nil {
		t.Fatalf("empty schema should decode to nil, got %v / %v", s, err)
	}
}
