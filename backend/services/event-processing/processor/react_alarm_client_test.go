// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// TestWireAlarmEdgeMapping pins the runtime→wire edge translation (ADR-057): the alarmClient must map
// the dispatcher's internal edge tokens onto the SHARED wire constants both modules bind to, so a
// resolved never fails open into device-management's raise path. Asserting the mapping here (rather
// than passing the string through) makes any future spelling divergence a test failure, not a silent
// cross-module drift.
func TestWireAlarmEdgeMapping(t *testing.T) {
	if got := wireAlarmEdge(runtime.EdgeResolved); got != dmmodel.AlarmEdgeResolved {
		t.Fatalf("a resolved edge must map to the shared wire resolved constant; got %q want %q", got, dmmodel.AlarmEdgeResolved)
	}
	if got := wireAlarmEdge(runtime.EdgeRaised); got != dmmodel.AlarmEdgeRaised {
		t.Fatalf("a raised edge must map to the shared wire raised constant; got %q want %q", got, dmmodel.AlarmEdgeRaised)
	}
	// An empty/legacy edge normalizes to raised (the DerivedEvent.Edge default), never to resolved —
	// so an unstamped request can never be dropped as a resolve.
	if got := wireAlarmEdge(""); got != dmmodel.AlarmEdgeRaised {
		t.Fatalf("an empty edge must default to raised, got %q", got)
	}
}
