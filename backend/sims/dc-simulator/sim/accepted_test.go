// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The ledger keeps the LATEST accepted time per device, whatever order they are recorded in.
func TestAcceptedLedgerKeepsTheLatestTimePerDevice(t *testing.T) {
	l := NewAcceptedLedger()
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	l.Record("a", t0.Add(time.Second).Format(time.RFC3339Nano))
	l.Record("a", t0.Format(time.RFC3339Nano))
	l.Record("b", t0.Add(2*time.Second).Format(time.RFC3339Nano))
	l.Record("a", t0.Add(1500*time.Millisecond).Format(time.RFC3339Nano))
	// The newest time is not the last one recorded: an older one after it must not replace it.
	l.Record("a", t0.Add(500*time.Millisecond).Format(time.RFC3339Nano))
	got, unparsed := l.Snapshot()
	if unparsed != 0 || len(got) != 2 {
		t.Fatalf("snapshot = %v (unparsed %d); want two devices", got, unparsed)
	}
	if !got["a"].Equal(t0.Add(1500*time.Millisecond)) || !got["b"].Equal(t0.Add(2*time.Second)) {
		t.Errorf("snapshot = %v; want a at +1.5s, b at +2s", got)
	}
	l.Record("c", "not a time")
	if _, unparsed := l.Snapshot(); unparsed != 1 {
		t.Errorf("an unparseable time was not counted (unparsed %d)", unparsed)
	}
	var none *AcceptedLedger
	none.Record("a", t0.Format(time.RFC3339Nano)) // nil-safe: must not panic
}

// Only an accepted (202) event is recorded: a shed (429) or a failure (500) never reached
// the pipeline, so the projection will never reflect it.
func TestOnlyAnAcceptedEventIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   int
	}{
		{"accepted", http.StatusAccepted, 1},
		{"shed", http.StatusTooManyRequests, 0},
		{"failed", http.StatusInternalServerError, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := fakeIngress(t, 1, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) })
			rt.Accepted = NewAcceptedLedger()
			_ = EmitMeasurement(context.Background(), rt, rt.Devices[0], "speed_kph", 1)
			got, _ := rt.Accepted.Snapshot()
			if len(got) != tc.want {
				t.Errorf("recorded %d devices; want %d", len(got), tc.want)
			}
			if tc.want == 1 && got[rt.Devices[0].Token].IsZero() {
				t.Error("the accepted event was recorded without its time")
			}
		})
	}
}
