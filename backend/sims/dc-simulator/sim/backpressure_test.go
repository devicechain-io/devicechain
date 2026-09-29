// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A 503 carrying a Retry-After is the ingress refusing under backpressure: nothing was
// published, so it is a clean non-accept and is counted as a shed, like a 429. Before, it
// was counted as a failure, which made a load run that reached the platform's backpressure
// read as a correctness regression.
func TestABackpressure503IsAShed(t *testing.T) {
	rt := fakeIngress(t, 10, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := EmitAll(context.Background(), rt, 4, constantMetrics); err != nil {
		t.Errorf("a tick refused only under backpressure reported failure: %v", err)
	}
	if shed, failed := rt.Stats.Shed.Load(), rt.Stats.Failed.Load(); shed != 10 || failed != 0 {
		t.Fatalf("shed = %d, failed = %d; want 10 and 0", shed, failed)
	}
	err := EmitMeasurement(context.Background(), rt, rt.Devices[0], "t", 1)
	if !errors.Is(err, ErrShed) || !errors.Is(err, ErrBackpressured) {
		t.Fatalf("a backpressure 503 = %v; want it to match both ErrShed and ErrBackpressured", err)
	}
}

// NEGATIVE CONTROL: a bare 503 is a failed publish, which may have been stored; it stays a
// failure, not a shed.
func TestABare503IsStillAFailure(t *testing.T) {
	rt := fakeIngress(t, 10, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_ = EmitAll(context.Background(), rt, 4, constantMetrics)
	if shed, failed := rt.Stats.Shed.Load(), rt.Stats.Failed.Load(); shed != 0 || failed != 10 {
		t.Fatalf("shed = %d, failed = %d; want 0 and 10", shed, failed)
	}
	err := EmitMeasurement(context.Background(), rt, rt.Devices[0], "t", 1)
	if errors.Is(err, ErrShed) || errors.Is(err, ErrBackpressured) {
		t.Fatalf("a bare 503 = %v; it must not read as a shed", err)
	}
}
