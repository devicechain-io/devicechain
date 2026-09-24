// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"strings"
	"testing"
	"time"
)

// DedupID is the detection's identity and nothing else: every identity field moves it, and the
// payload fields (the trigger time, severity, value) do not.
func TestDedupIDIsTheDetectionIdentity(t *testing.T) {
	base := DerivedEvent{RuleID: "acme/p@1/r1", Tenant: "acme", Kind: "threshold", Series: "d1",
		OccurredTime: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Edge: EdgeRaised}
	id := base.DedupID()
	if !strings.HasPrefix(id, "de.") || len(id) != len("de.")+64 {
		t.Fatalf("DedupID = %q, want de.<sha256 hex>", id)
	}
	moves := map[string]func(*DerivedEvent){
		"rule":     func(d *DerivedEvent) { d.RuleID = "acme/p@1/r2" },
		"tenant":   func(d *DerivedEvent) { d.Tenant = "beta" },
		"kind":     func(d *DerivedEvent) { d.Kind = "absence" },
		"series":   func(d *DerivedEvent) { d.Series = "d2" },
		"occurred": func(d *DerivedEvent) { d.OccurredTime = d.OccurredTime.Add(time.Nanosecond) },
		"edge":     func(d *DerivedEvent) { d.Edge = EdgeResolved },
	}
	for name, change := range moves {
		d := base
		change(&d)
		if d.DedupID() == id {
			t.Errorf("changing the %s did not change the dedup id", name)
		}
	}
	v := 42.0
	same := base
	same.TriggeredAt = time.Now()
	same.Severity = "major"
	same.Value = &v
	if same.DedupID() != id {
		t.Error("a payload field (trigger time, severity or value) changed the dedup id")
	}
}
