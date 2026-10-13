// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/datatypes"
)

// renderWotSnapshot renders a hand-built snapshot through ThingModelOf, the function the
// resolver calls, so a refusal is proven at the caller and not only in a helper.
func renderWotSnapshot(t *testing.T, snap *ProfileSnapshot) ([]byte, error) {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	profile := &DeviceProfile{}
	profile.Token = "p"
	return ThingModelOf(profile, &DeviceProfileVersion{Version: 1, Snapshot: datatypes.JSON(raw)})
}

func wotJSON(s string) *datatypes.JSON { j := datatypes.JSON(s); return &j }

func metricSnapshot(m *MetricDefinition) *ProfileSnapshot {
	m.MetricKey = "m"
	return &ProfileSnapshot{Metrics: []*MetricDefinition{m}}
}

// Every way a metric's allowed values can fail to be a list of distinct TEXT values is
// refused: not a list, a repeat, or a member that is not text. Ingest enforces allowed
// values only as text, so a non-text member is a constraint the platform does not apply,
// and advertising it would be a lie the model tells.
func TestThingModelRefusesAMetricWhoseAllowedValuesCannotBeExpressed(t *testing.T) {
	for _, tc := range []struct{ dataType, enum string }{
		{"STRING", `{"a":1}`},
		{"STRING", `"x"`},
		{"STRING", `["a","a"]`},
		{"STRING", `[1,"1"]`},
		{"INT", `[1.5,true]`},
		{"INT", `[{"a":1}]`},
	} {
		out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: tc.dataType, Enum: wotJSON(tc.enum)}))
		if err == nil {
			t.Errorf("%s enum %s rendered instead of refused: %s", tc.dataType, tc.enum, out)
		}
	}
}

// "1" and "01" are distinct as text but both the integer 1, on a metric and on a command
// parameter alike; a Thing Model's enum must hold distinct values.
func TestThingModelRefusesAllowedValuesThatRepeatOnceTyped(t *testing.T) {
	out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: "INT", Enum: wotJSON(`["1","01"]`)}))
	if err == nil || !strings.Contains(err.Error(), "repeat") {
		t.Fatalf("metric: got %s, %v; want a refusal naming the repeat", out, err)
	}
	for _, params := range []string{
		`[{"name":"p","dataType":"STRING","enum":["a","a"]}]`,
		`[{"name":"p","dataType":"INT","enum":["1","01"]}]`,
		`[{"name":"p","dataType":"DOUBLE","enum":["1","1.0"]}]`,
	} {
		out, err := renderWotSnapshot(t, &ProfileSnapshot{Commands: []*CommandDefinition{
			{CommandKey: "c", ParameterSchema: wotJSON(params)}}})
		if err == nil || !strings.Contains(err.Error(), "repeat") {
			t.Errorf("parameter %s: got %s, %v; want a refusal naming the repeat", params, out, err)
		}
	}
}

// NaN and infinities parse as numbers but have no JSON form: they are refused with an
// error, never a panic.
func TestThingModelRefusesANonFiniteAllowedValue(t *testing.T) {
	for _, v := range []string{"NaN", "Inf", "-Inf"} {
		out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: "DOUBLE", Enum: wotJSON(`["` + v + `"]`)}))
		if err == nil {
			t.Errorf("allowed value %s rendered: %s", v, out)
		}
	}
}

// Numeric bounds on a property that is not a number cannot be carried.
func TestThingModelRefusesBoundsOnANonNumericMetric(t *testing.T) {
	for _, dt := range []string{"BOOLEAN", "STRING"} {
		out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: dt,
			MinValue: sql.NullFloat64{Float64: 1, Valid: true}}))
		if err == nil {
			t.Errorf("%s metric with a minimum rendered: %s", dt, out)
		}
	}
	// The counterweight: bounds on a numeric metric are carried.
	out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: "INT",
		MinValue: sql.NullFloat64{Float64: 1, Valid: true}, MaxValue: sql.NullFloat64{Float64: 9, Valid: true}}))
	if err != nil || !strings.Contains(string(out), `"minimum":1`) || !strings.Contains(string(out), `"maximum":9`) {
		t.Fatalf("INT bounds: %s, %v", out, err)
	}
}

// An unknown data type is refused rather than guessed, on a metric and on a parameter.
func TestThingModelRefusesAnUnknownDataType(t *testing.T) {
	out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: "FLOAT"}))
	if err == nil {
		t.Fatalf("unknown data type rendered: %s", out)
	}
	out, err = renderWotSnapshot(t, &ProfileSnapshot{Commands: []*CommandDefinition{
		{CommandKey: "c", ParameterSchema: wotJSON(`[{"name":"p","dataType":"FLOAT"}]`)}}})
	if err == nil {
		t.Fatalf("unknown parameter data type rendered: %s", out)
	}
}

// Allowed values declared as text on a numeric metric are emitted as values of its type, in
// the order declared.
func TestThingModelTypesAMetricsAllowedValues(t *testing.T) {
	out, err := renderWotSnapshot(t, metricSnapshot(&MetricDefinition{DataType: "INT", Enum: wotJSON(`["3","1","2"]`)}))
	if err != nil || !strings.Contains(string(out), `"enum":[3,1,2]`) {
		t.Fatalf("got %s, %v; want the integers 3, 1, 2", out, err)
	}
}
