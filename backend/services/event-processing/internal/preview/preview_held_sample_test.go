// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"testing"
	"time"

	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// TestPreviewSettlesASampleAtTheWindowEnd: the window is [Start, End] — a reading AT End is in it —
// and a rate or count-window rule decides a reading only once the frontier is strictly past it. The
// closing advance must therefore go past End, or the last reading of every preview whose end lands
// on a reading (a device reporting on whole seconds, an author picking that second) is replayed and
// then never decided: the preview answers "would not have fired" about a reading it read.
func TestPreviewSettlesASampleAtTheWindowEnd(t *testing.T) {
	thr := 50.0
	cr, err := rules0.Compile(rules0.Rule{ID: "acme/p@1/spike", Name: "spike", Type: rules0.TypeDeltaRate,
		Metric: "temperature", Op: rules0.OpGt, Threshold: &thr}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
	end := base.Add(time.Minute)
	op := &fakeOpener{msgs: []messaging.Message{
		msg(t, 1, "acme", "d1", "p@1", "temperature", "0", base),
		msg(t, 2, "acme", "d1", "p@1", "temperature", "100", end), // +100 > 50, exactly at End
	}}
	res, err := Run(context.Background(), op, "resolved-events", reg, "acme", "p@1",
		TimeRange{Start: base.Add(-time.Hour), End: end}, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 1 || !res.Firings[0].Raise || !res.Firings[0].OccurredAt.Equal(end) || res.Firings[0].Value != 100 {
		t.Fatalf("the +100 reading at the window's end must raise, stamped at End; got %+v", res.Firings)
	}
}
