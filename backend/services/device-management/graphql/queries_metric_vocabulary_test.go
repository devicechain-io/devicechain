// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"reflect"
	"testing"

	"gorm.io/datatypes"

	"github.com/devicechain-io/dc-device-management/model"
)

// Each field is what the console's connectivity helper picks an example value from, so a
// bound or allow-list lost here becomes a command the platform then refuses.
func TestPublishedMetricResolverExposesWhatIngestChecks(t *testing.T) {
	enum := datatypes.JSON(`["1","2"]`)
	res := model.NewProfileResolution(model.ProfileScope{}, []*model.MetricDefinition{
		{MetricKey: "full", DataType: string(model.MetricInt),
			Unit:     sql.NullString{String: "Cel", Valid: true},
			MinValue: sql.NullFloat64{Float64: -5, Valid: true},
			MaxValue: sql.NullFloat64{Float64: 0, Valid: true},
			Enum:     &enum},
		{MetricKey: "bare", DataType: string(model.MetricBoolean)},
	})
	metrics := []model.ResolvedMetric{res.MetricAt(0), res.MetricAt(1)}
	got := (&DeviceMetricVocabularyResolver{M: model.MetricVocabulary{DeviceExists: true, Metrics: metrics}}).Metrics()
	if len(got) != 2 {
		t.Fatalf("got %d metrics", len(got))
	}

	full, bare := got[0], got[1]
	if full.MetricKey() != "full" || full.DataType() != "INT" {
		t.Errorf("full = %s/%s", full.MetricKey(), full.DataType())
	}
	if u := full.Unit(); u == nil || *u != "Cel" {
		t.Errorf("unit = %v", u)
	}
	// A bound of 0 is a real bound: the zero value must not read as "none".
	if v := full.MinValue(); v == nil || *v != -5 {
		t.Errorf("min = %v", v)
	}
	if v := full.MaxValue(); v == nil || *v != 0 {
		t.Errorf("max = %v", v)
	}
	if v := full.AllowedValues(); v == nil || !reflect.DeepEqual(*v, []string{"1", "2"}) {
		t.Errorf("allowed = %v", v)
	}
	if bare.Unit() != nil || bare.MinValue() != nil || bare.MaxValue() != nil || bare.AllowedValues() != nil {
		t.Errorf("an undeclared constraint must be null, got unit=%v min=%v max=%v allowed=%v",
			bare.Unit(), bare.MinValue(), bare.MaxValue(), bare.AllowedValues())
	}
}

// A calculated metric is computed by the platform, so the vocabulary must say so: a
// picker marks it, and a device-facing example leaves it out (a device-sent value under
// its key is dropped at resolve).
func TestPublishedMetricResolverReportsCalculated(t *testing.T) {
	res := model.NewProfileResolution(model.ProfileScope{}, []*model.MetricDefinition{
		{MetricKey: "volts", DataType: string(model.MetricDouble)},
		{MetricKey: "double_volts", DataType: string(model.MetricDouble),
			Expression: sql.NullString{String: "volts * 2.0", Valid: true}},
	})
	metrics := []model.ResolvedMetric{res.MetricAt(0), res.MetricAt(1)}
	got := (&DeviceMetricVocabularyResolver{M: model.MetricVocabulary{DeviceExists: true, Metrics: metrics}}).Metrics()
	byKey := map[string]bool{}
	for _, m := range got {
		byKey[m.MetricKey()] = m.Calculated()
	}
	if byKey["volts"] || !byKey["double_volts"] {
		t.Fatalf("calculated flags = %v, want volts=false double_volts=true", byKey)
	}
}
