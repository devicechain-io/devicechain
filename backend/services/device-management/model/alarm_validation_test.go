// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func i32(v int32) *int32 { return &v }

// The alarm vocabulary enums accept only their known members, and severity ranks
// from CRITICAL (0, most severe) to INDETERMINATE.
func TestAlarmVocabularyValid(t *testing.T) {
	for _, c := range []AlarmConditionType{AlarmConditionSimple, AlarmConditionDuration, AlarmConditionRepeating} {
		if !c.Valid() {
			t.Errorf("expected condition type %q valid", c)
		}
	}
	for _, bad := range []AlarmConditionType{"", "simple", "ONCE"} {
		if bad.Valid() {
			t.Errorf("expected condition type %q invalid", bad)
		}
	}
	for _, o := range []AlarmOperator{AlarmOpGreater, AlarmOpGreaterEqual, AlarmOpLess, AlarmOpLessEqual, AlarmOpEqual, AlarmOpNotEqual} {
		if !o.Valid() {
			t.Errorf("expected operator %q valid", o)
		}
	}
	for _, bad := range []AlarmOperator{"", ">", "GREATER"} {
		if bad.Valid() {
			t.Errorf("expected operator %q invalid", bad)
		}
	}
	// Severity order: CRITICAL most severe (rank 0) → INDETERMINATE least.
	if AlarmSeverityCritical.Rank() >= AlarmSeverityMajor.Rank() ||
		AlarmSeverityMajor.Rank() >= AlarmSeverityMinor.Rank() ||
		AlarmSeverityMinor.Rank() >= AlarmSeverityWarning.Rank() ||
		AlarmSeverityWarning.Rank() >= AlarmSeverityIndeterminate.Rank() {
		t.Errorf("severity ranks are not strictly ordered most→least severe")
	}
	if AlarmSeverity("BOGUS").Valid() || AlarmSeverity("BOGUS").Rank() != -1 {
		t.Errorf("expected unknown severity invalid with rank -1")
	}
}

func TestValidateAlarmDefinition(t *testing.T) {
	base := func() *AlarmDefinitionCreateRequest {
		return &AlarmDefinitionCreateRequest{
			AlarmKey:      "over-temp",
			MetricKey:     "temp",
			ConditionType: "SIMPLE",
			Operator:      "GT",
			Severity:      "MAJOR",
			Threshold:     f64(100),
		}
	}
	cases := []struct {
		name string
		mut  func(*AlarmDefinitionCreateRequest)
		ok   bool
	}{
		{"simple static ok", func(r *AlarmDefinitionCreateRequest) {}, true},
		{"dynamic threshold ok", func(r *AlarmDefinitionCreateRequest) { r.Threshold = nil; r.ThresholdAttr = str("maxTemp") }, true},
		{"invalid condition", func(r *AlarmDefinitionCreateRequest) { r.ConditionType = "ONCE" }, false},
		{"invalid operator", func(r *AlarmDefinitionCreateRequest) { r.Operator = "GREATER" }, false},
		{"invalid severity", func(r *AlarmDefinitionCreateRequest) { r.Severity = "SEVERE" }, false},
		{"missing metric", func(r *AlarmDefinitionCreateRequest) { r.MetricKey = "" }, false},
		{"no threshold source", func(r *AlarmDefinitionCreateRequest) { r.Threshold = nil }, false},
		{"both threshold sources", func(r *AlarmDefinitionCreateRequest) { r.ThresholdAttr = str("maxTemp") }, false},
		{"whitespace attr is no source", func(r *AlarmDefinitionCreateRequest) { r.Threshold = nil; r.ThresholdAttr = str("  ") }, false},
		{"simple with duration rejected", func(r *AlarmDefinitionCreateRequest) { r.DurationSeconds = i32(30) }, false},
		{
			"duration ok",
			func(r *AlarmDefinitionCreateRequest) { r.ConditionType = "DURATION"; r.DurationSeconds = i32(30) }, true,
		},
		{"duration missing seconds", func(r *AlarmDefinitionCreateRequest) { r.ConditionType = "DURATION" }, false},
		{
			"duration non-positive",
			func(r *AlarmDefinitionCreateRequest) { r.ConditionType = "DURATION"; r.DurationSeconds = i32(0) }, false,
		},
		{
			"repeating ok",
			func(r *AlarmDefinitionCreateRequest) {
				r.ConditionType = "REPEATING"
				r.RepeatCount = i32(3)
				r.RepeatWindowSeconds = i32(60)
			}, true,
		},
		{
			"repeating missing window",
			func(r *AlarmDefinitionCreateRequest) { r.ConditionType = "REPEATING"; r.RepeatCount = i32(3) }, false,
		},
		{
			"repeating with duration rejected",
			func(r *AlarmDefinitionCreateRequest) {
				r.ConditionType = "REPEATING"
				r.RepeatCount = i32(3)
				r.RepeatWindowSeconds = i32(60)
				r.DurationSeconds = i32(30)
			}, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mut(r)
			err := ValidateAlarmDefinition(r)
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}
