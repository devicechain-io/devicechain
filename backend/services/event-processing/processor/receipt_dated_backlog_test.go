// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
)

// A consequence of event-sources dating a timestampless reading at the moment the platform
// RECEIVED it (the capture stream's append time) rather than when it was decoded, pinned here
// so that it is a decision and not a surprise.
//
// After an event-sources outage, the capture stream drains readings received up to the whole
// outage ago. Traffic that does not pass through event-sources (LwM2M, Sparkplug) kept the
// engine-wide watermark at now, so those readings are LATE to a sliding-window rule: they are
// not folded, and detect_late_samples_total counts them. That is exactly what already happened
// to a backlog of readings that carry their own timestamps; before, a timestampless one was
// dated at decode (now) and folded as if it had just happened, which was the wrong date.
func TestAReceiptDatedBacklogIsLateToASlidingWindow(t *testing.T) {
	now := testBase.Add(2 * time.Hour)
	received := now.Add(-time.Hour) // received an hour ago, drained now

	run := func(t *testing.T, backlogAt time.Time) (late float64, derived int) {
		t.Helper()
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		metrics := NewDetectMetrics(ms)
		reg := repeatingReg(t)
		out := &captureWriter{}
		ctx := context.Background()
		rp := &ResolvedEventsProcessor{
			Store: newTestStore(t),
			cfg: Config{
				PartitionId:        "singleton",
				CheckpointEvents:   100,
				CheckpointInterval: time.Hour,
				TickInterval:       time.Hour,
				Clock:              detectcore.RealClock{},
			},
			registry:  reg,
			metrics:   metrics,
			publisher: runtime.NewPublisher(out, reg, metrics),
			clock:     detectcore.RealClock{},
			procCtx:   ctx,
		}
		if err := rp.restore(ctx); err != nil {
			t.Fatalf("restore: %v", err)
		}
		// Another transport's live traffic has moved the watermark to now.
		rp.handle(measuredMsgAt(t, 1, now, "acme", "lwm2m-dev", "p@1", "temperature", "20", &fakeAck{}))
		// The drained backlog: three readings over the threshold, each dated backlogAt.
		for i := 0; i < 3; i++ {
			at := backlogAt.Add(time.Duration(i) * time.Second)
			rp.handle(measuredMsgAt(t, uint64(2+i), at, "acme", "d1", "p@1", "temperature", "90", &fakeAck{}))
		}
		rp.checkpoint(ctx) // detections publish at the checkpoint
		return testutil.ToFloat64(metrics.lateSamplesTotal), out.writes
	}

	t.Run("dated at receipt", func(t *testing.T) {
		late, derived := run(t, received)
		if late != 3 {
			t.Errorf("detect_late_samples_total = %v, want 3: a reading received an hour ago is late to a 10s window", late)
		}
		if derived != 0 {
			t.Errorf("the late readings fired the rule %d times; late samples are not folded", derived)
		}
	})

	// The counterweight: the same readings dated at now (as decode-dating used to date them) are
	// folded and fire the rule, so the case above measures the dating, not a rule that never fires.
	t.Run("dated now", func(t *testing.T) {
		late, derived := run(t, now.Add(time.Second))
		if late != 0 {
			t.Errorf("detect_late_samples_total = %v, want 0 for readings at the watermark", late)
		}
		if derived == 0 {
			t.Error("three readings over the threshold inside the window did not fire the rule")
		}
	})
}
