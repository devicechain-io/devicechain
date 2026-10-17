// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// previewRule's draftMetrics (ADR-082 preview parity): a draft profile's formulas are checked
// as a publish checks them before anything is replayed, and a draft that passes reaches the
// replay. The recalculation itself is held by internal/preview's tests.

func strp(s string) *string { return &s }

func withDraft(in struct{ Input previewRuleInput }, metrics ...previewMetricInput) struct{ Input previewRuleInput } {
	in.Input.DraftMetrics = &metrics
	return in
}

var draftTemp = previewMetricInput{MetricKey: "temperature", DataType: "DOUBLE"}

func TestADraftFormulaProblemIsADiagnostic(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}}
	cases := map[string]previewMetricInput{
		"does not type-check":  {MetricKey: "f", DataType: "DOUBLE", Expression: strp("temperature > 1.0")},
		"reads an undeclared":  {MetricKey: "f", DataType: "DOUBLE", Expression: strp("humidity * 2.0")},
		"cannot be calculated": {MetricKey: "f", DataType: "STRING", Expression: strp("temperature")},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := r.PreviewRule(previewCtx(auth.DeviceRead),
				withDraft(previewInput(plainDraft, "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"), draftTemp, bad))
			if err != nil {
				t.Fatalf("previewRule: %v", err)
			}
			if res.Ok() || len(res.Diagnostics()) != 1 || !strings.HasPrefix(res.Diagnostics()[0].Message(), "draft profile: ") {
				t.Fatalf("a draft formula problem must fail the preview with one diagnostic, got ok=%v %v",
					res.Ok(), res.Diagnostics())
			}
		})
	}
}

// The counterweight: a draft that passes the checks is not stopped by them — execution
// continues to the window check, which rejects the inverted window.
func TestAValidDraftReachesTheReplay(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}}
	for _, draft := range [][]previewMetricInput{
		{draftTemp, {MetricKey: "temp_f", DataType: "DOUBLE", Expression: strp("temperature * 1.8 + 32.0"),
			MaxValue: func() *float64 { v := 500.0; return &v }()}},
		{}, // a draft with no metrics at all: every stored calculated value is dropped
	} {
		res, err := r.PreviewRule(previewCtx(auth.DeviceRead),
			withDraft(previewInput(plainDraft, "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z"), draft...))
		if err != nil {
			t.Fatalf("previewRule: %v", err)
		}
		if diags := res.Diagnostics(); len(diags) != 1 || !strings.Contains(diags[0].Message(), "end of the window") {
			t.Fatalf("a valid draft was stopped before the window check: %v", diags)
		}
	}
}

func TestDraftMetricsAreBounded(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}}
	many := make([]previewMetricInput, maxDraftMetrics+1)
	for i := range many {
		many[i] = previewMetricInput{MetricKey: "k" + strings.Repeat("x", i%7), DataType: "DOUBLE"}
	}
	res, err := r.PreviewRule(previewCtx(auth.DeviceRead),
		withDraft(previewInput(plainDraft, "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"), many...))
	if err != nil {
		t.Fatalf("previewRule: %v", err)
	}
	if res.Ok() || len(res.Diagnostics()) != 1 || !strings.Contains(res.Diagnostics()[0].Message(), "above the maximum") {
		t.Fatalf("an oversized draft must be refused, got %v", res.Diagnostics())
	}
}

// The served schema accepts the field and its stats, with the field names the console sends.
func TestTheSchemaCarriesDraftMetrics(t *testing.T) {
	for _, want := range []string{"draftMetrics: [PreviewMetricInput!]", "input PreviewMetricInput",
		"recalculatedSamples: Int!", "calculatedSkips: Int!"} {
		if !strings.Contains(SchemaContent, want) {
			t.Errorf("schema lacks %q", want)
		}
	}
}
