// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"strings"
	"testing"
	"time"

	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// durationReg wires a registry with one duration rule: temperature above 80 held for 10s.
func durationReg(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr := 80.0
	cr, err := rules0.Compile(rules0.Rule{
		ID: "acme/p@1/hot", Name: "hot", Type: rules0.TypeDuration, Severity: rules0.SeverityCritical,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
		Hold: rules0.Duration(10 * time.Second),
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

// A reading the engine refuses as too far behind its frontier is one the preview did not use, and
// a preview that stays silent about it answers "the rule would not have fired" about readings it
// never looked at. The refusal must surface as a degraded reason.
func TestPreviewReportsReadingsTheEngineRefusedAsLate(t *testing.T) {
	op := &fakeOpener{msgs: []messaging.Message{
		// d2 moves the frontier to base; d1's reading ten minutes earlier is far past a 10s hold.
		msg(t, 1, "acme", "d2", "p@1", "temperature", "20", base),
		msg(t, 2, "acme", "d1", "p@1", "temperature", "90", base.Add(-10*time.Minute)),
	}}
	res, err := Run(context.Background(), op, "resolved-events", durationReg(t), "acme", "p@1", window(), 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 0 {
		t.Fatalf("a refused reading produced firings: %+v", res.Firings)
	}
	if !strings.Contains(res.Degraded, "1 reading") || !strings.Contains(res.Degraded, "behind") {
		t.Fatalf("degraded = %q, want it to report the 1 reading refused as late", res.Degraded)
	}
}

// The counterweight: in-order readings are not reported as late, so the reason above is specific.
func TestPreviewDoesNotReportInOrderReadingsAsLate(t *testing.T) {
	op := &fakeOpener{msgs: []messaging.Message{
		msg(t, 1, "acme", "d1", "p@1", "temperature", "90", base),
		msg(t, 2, "acme", "d1", "p@1", "temperature", "90", base.Add(20*time.Second)),
	}}
	res, err := Run(context.Background(), op, "resolved-events", durationReg(t), "acme", "p@1", window(), 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 1 || !res.Firings[0].Raise || !res.Firings[0].OccurredAt.Equal(base.Add(10*time.Second)) {
		t.Fatalf("want one RAISE at base+10s, got %+v", res.Firings)
	}
	if res.Degraded != "" {
		t.Fatalf("nothing here is degraded, got %q", res.Degraded)
	}
}
