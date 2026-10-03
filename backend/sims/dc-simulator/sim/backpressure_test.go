// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A 503 carrying a Retry-After is the ingress refusing under backpressure: nothing was
// published, so it is a clean non-accept, not a failure. It is counted as BACKPRESSURED,
// apart from a shed: a shed is a 429 at the tenant's own ceiling (and, under a contention
// floor, shed priority at work), while the backpressure gate refuses every tenant alike.
// Counting it as a shed made the contention test read the platform being behind as gold
// being shed.
func TestABackpressure503IsCountedAsBackpressureNotShed(t *testing.T) {
	rt := fakeIngress(t, 10, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := EmitAll(context.Background(), rt, 4, constantMetrics); err != nil {
		t.Errorf("a tick refused only under backpressure reported failure: %v", err)
	}
	bp, shed, failed := rt.Stats.Backpressured.Load(), rt.Stats.Shed.Load(), rt.Stats.Failed.Load()
	if bp != 10 || shed != 0 || failed != 0 {
		t.Fatalf("backpressured = %d, shed = %d, failed = %d; want 10, 0 and 0", bp, shed, failed)
	}
	err := EmitMeasurement(context.Background(), rt, rt.Devices[0], "t", 1)
	if !errors.Is(err, ErrShed) || !errors.Is(err, ErrBackpressured) {
		t.Fatalf("a backpressure 503 = %v; want it to match both ErrShed and ErrBackpressured", err)
	}
}

// NEGATIVE CONTROL: a bare 503 is a failed publish, which may have been stored; it stays a
// failure, neither a shed nor a backpressure refusal.
func TestABare503IsStillAFailure(t *testing.T) {
	rt := fakeIngress(t, 10, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_ = EmitAll(context.Background(), rt, 4, constantMetrics)
	bp, shed, failed := rt.Stats.Backpressured.Load(), rt.Stats.Shed.Load(), rt.Stats.Failed.Load()
	if bp != 0 || shed != 0 || failed != 10 {
		t.Fatalf("backpressured = %d, shed = %d, failed = %d; want 0, 0 and 10", bp, shed, failed)
	}
	err := EmitMeasurement(context.Background(), rt, rt.Devices[0], "t", 1)
	if errors.Is(err, ErrShed) || errors.Is(err, ErrBackpressured) {
		t.Fatalf("a bare 503 = %v; it must not read as a shed", err)
	}
}

// One tick against an ingress answering every outcome at once: each lands in its own
// counter, and the snapshot carries all four, with Refused() the sum of the two clean
// refusals.
func TestMixedRefusalsAreCountedApart(t *testing.T) {
	rt := fakeIngress(t, 10, mixedIngress)
	if err := EmitAll(context.Background(), rt, 4, constantMetrics); err == nil {
		t.Error("the bare 503 is a real failure, so the tick must report one")
	}
	snap := rt.Stats.Snapshot(time.Now())
	if snap.Emitted != 2 || snap.Shed != 3 || snap.Backpressured != 4 || snap.Failed != 1 {
		t.Fatalf("emitted %d, shed %d, backpressured %d, failed %d; want 2, 3, 4 and 1",
			snap.Emitted, snap.Shed, snap.Backpressured, snap.Failed)
	}
	if got := snap.Refused(); got != 7 {
		t.Errorf("Refused() = %d, want 7 (3 shed + 4 backpressured)", got)
	}

	// Reset must zero the new counter too, or a second run inherits the first's refusals.
	rt.Stats.Reset(time.Now())
	if bp := rt.Stats.Backpressured.Load(); bp != 0 {
		t.Errorf("after Reset, backpressured = %d, want 0", bp)
	}
}
