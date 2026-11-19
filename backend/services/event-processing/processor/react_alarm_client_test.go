// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// TestWireAlarmEdgeMapping pins the runtime→wire edge translation (ADR-057): the alarmClient must map
// the dispatcher's internal edge tokens onto the SHARED wire constants both modules bind to, so a
// resolved never fails open into device-management's raise path. Asserting the mapping here (rather
// than passing the string through) makes any future spelling divergence a test failure, not a silent
// cross-module drift.
func TestWireAlarmEdgeMapping(t *testing.T) {
	if got := wireAlarmEdge(runtime.EdgeResolved); got != dmcontract.AlarmEdgeResolved {
		t.Fatalf("a resolved edge must map to the shared wire resolved constant; got %q want %q", got, dmcontract.AlarmEdgeResolved)
	}
	if got := wireAlarmEdge(runtime.EdgeRaised); got != dmcontract.AlarmEdgeRaised {
		t.Fatalf("a raised edge must map to the shared wire raised constant; got %q want %q", got, dmcontract.AlarmEdgeRaised)
	}
	// An empty/legacy edge normalizes to raised (the DerivedEvent.Edge default), never to resolved —
	// so an unstamped request can never be dropped as a resolve.
	if got := wireAlarmEdge(""); got != dmcontract.AlarmEdgeRaised {
		t.Fatalf("an empty edge must default to raised, got %q", got)
	}
}

// The alarm request is published under the request's edge token as the broker dedup id, so a
// retry's re-publish of it is stored once by raise-alarm, and scoped to the request's tenant.
func TestAlarmClientPublishesUnderTheRequestToken(t *testing.T) {
	w := &captureConnectorWriter{}
	err := NewAlarmClient(w).Dispatch(context.Background(), react.AlarmRequest{
		Tenant: "acme", Token: "tok-1", DeviceToken: "device-1", AlarmKey: "overheat",
		RuleID: "p/r1", Edge: runtime.EdgeRaised, OccurredTime: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if w.dedupID != "tok-1" {
		t.Fatalf("published under dedup id %q, want the request token %q", w.dedupID, "tok-1")
	}
	if w.tenant != "acme" {
		t.Fatalf("published under tenant %q, want acme", w.tenant)
	}
}

// The rule's position dependency reaches device-management on the wire, which is what marks the
// alarm and withholds it from a caller without location:read; a request without it says so too.
func TestAlarmClientCarriesThePositionDependency(t *testing.T) {
	for _, want := range []bool{true, false} {
		w := &captureConnectorWriter{}
		if err := NewAlarmClient(w).Dispatch(context.Background(), react.AlarmRequest{
			Tenant: "acme", Token: "tok-1", DeviceToken: "device-1", AlarmKey: "left-yard",
			RuleID: "p/r1", Edge: runtime.EdgeResolved, OccurredTime: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
			RequiresPosition: want,
		}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		got, err := dmcontract.UnmarshalRaiseAlarmRequest(w.payload)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.RequiresPosition == nil || *got.RequiresPosition != want {
			t.Fatalf("requiresPosition on the wire = %v, want %v (always sent, never absent)", got.RequiresPosition, want)
		}
	}
}
