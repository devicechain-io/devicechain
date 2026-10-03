// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// mixedIngress answers by device (the token travels in the posted event): dev-00001..00003
// a 429, dev-00004..00007 a 503 with a Retry-After, dev-00008 a bare 503, the rest a 202.
// The bucket sizes all differ, so any cross-wiring between counters changes a value rather
// than hiding in a tie.
func mixedIngress(w http.ResponseWriter, r *http.Request) {
	var ev struct {
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch ev.Device {
	case "dev-00001", "dev-00002", "dev-00003":
		w.WriteHeader(http.StatusTooManyRequests)
	case "dev-00004", "dev-00005", "dev-00006", "dev-00007":
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	case "dev-00008":
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// The snapshot is what GET /status serves and what every load-test report is read from,
// so the split is pinned on its JSON keys: "shed" is the 429s alone, "backpressured" the
// 503s with a Retry-After, and a bare 503 is "failed". Read through the wire shape, it
// does not depend on how the counters are declared.
func TestStatusSnapshotSeparatesShedFromBackpressure(t *testing.T) {
	rt := fakeIngress(t, 10, mixedIngress)
	_ = EmitAll(context.Background(), rt, 4, constantMetrics)
	raw, err := json.Marshal(rt.Stats.Snapshot(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"emitted": 2, "shed": 3, "backpressured": 4, "failed": 1} {
		if got, ok := status[k].(float64); !ok || got != want {
			t.Errorf("status[%q] = %v, want %v (serialized %s)", k, status[k], want, raw)
		}
	}
}
