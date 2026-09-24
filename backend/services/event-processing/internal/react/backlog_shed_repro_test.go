// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
)

// A tenant well inside its outbound ceiling must lose nothing when REACT drains a backlog.
//
// 🔴 THE DEFECT WAS THE GATE'S CLOCK. REACT is a durable consumer, so after a restart,
// rollout or failover it drains every derived event that piled up while it was down, back to
// back. The source-side gate charged each connector action at ARRIVAL, so a tenant firing
// 10 actions a second — a tenth of the platform default of 100/s — had its whole backlog
// charged at one instant: the burst (200) went out and every other action was shed, acked and
// gone, with a counter as the only record.
//
// The gate now meters on the time the triggering telemetry reached the platform, which DETECT
// stamps on the derived event (triggeredAt). The events are decoded from JSON the way REACT's
// consumer decodes them, so the stamp arrives through the wire field and not a Go literal.
// The limiter is the real core one at the configuration defaults.
func TestBacklogDrainOfACompliantTenantIsNotShedThroughTheRealLimiter(t *testing.T) {
	const n = 1000
	gate := core.NewTenantRateLimiter(core.StaticCeiling(100, 200))
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true},
		nil, nil, sink, gate, m)

	// 10 actions a second over the last 100 seconds, drained now in one go.
	start := time.Now().Add(-n * 100 * time.Millisecond)
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * 100 * time.Millisecond)
		body := fmt.Sprintf(`{"ruleId":"acme/p@1/r1","tenant":"acme","kind":"threshold","edge":"raised",`+
			`"series":"device-%d","occurredTime":%q,"triggeredAt":%q}`,
			i, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
		var ev runtime.DerivedEvent
		if err := json.Unmarshal([]byte(body), &ev); err != nil {
			t.Fatal(err)
		}
		_ = d.Dispatch(context.Background(), ev)
	}
	if len(sink.got) != n || m.connectorShed["httpCall"] != 0 {
		t.Fatalf("dispatched=%d shed=%d of %d: a tenant at a tenth of its ceiling lost actions to a backlog drain",
			len(sink.got), m.connectorShed["httpCall"], n)
	}
}
