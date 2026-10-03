// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// The ledger keeps the LATEST accepted time per device, whatever order they are recorded in,
// and of any event type: the live projection reflects every type.
func TestIdentityLedgerKeepsTheLatestAcceptedTimePerDevice(t *testing.T) {
	l := NewIdentityLedger()
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	l.Record(OutcomeAccepted, "a", "Measurement", t0.Add(time.Second).Format(time.RFC3339Nano))
	l.Record(OutcomeAccepted, "a", "Measurement", t0.Format(time.RFC3339Nano))
	l.Record(OutcomeAccepted, "b", "Location", t0.Add(2*time.Second).Format(time.RFC3339Nano))
	l.Record(OutcomeAccepted, "a", "Location", t0.Add(1500*time.Millisecond).Format(time.RFC3339Nano))
	// The newest time is not the last one recorded: an older one after it must not replace it.
	l.Record(OutcomeAccepted, "a", "Measurement", t0.Add(500*time.Millisecond).Format(time.RFC3339Nano))
	// A refused or ambiguous emit never moves the live-state target.
	l.Record(OutcomeRefused, "a", "Measurement", t0.Add(9*time.Second).Format(time.RFC3339Nano))
	l.Record(OutcomeAmbiguous, "b", "Measurement", t0.Add(9*time.Second).Format(time.RFC3339Nano))
	got, unparsed := l.LatestAccepted()
	if unparsed != 0 || len(got) != 2 {
		t.Fatalf("latest = %v (unparsed %d); want two devices", got, unparsed)
	}
	if !got["a"].Equal(t0.Add(1500*time.Millisecond)) || !got["b"].Equal(t0.Add(2*time.Second)) {
		t.Errorf("latest = %v; want a at +1.5s, b at +2s", got)
	}
	l.Record(OutcomeAccepted, "c", "Measurement", "not a time")
	if _, unparsed := l.LatestAccepted(); unparsed != 1 {
		t.Errorf("an unparseable time was not counted (unparsed %d)", unparsed)
	}
	var none *IdentityLedger
	none.Record(OutcomeAccepted, "a", "Measurement", t0.Format(time.RFC3339Nano)) // nil-safe: must not panic
	if s := none.Snapshot(); len(s.Devices) != 0 || s.All != (OutcomeTotals{}) {
		t.Errorf("a nil ledger snapshots as %+v; want empty", s)
	}
}

// Only a Measurement is keyed by identity, but every type is counted, so the reconciliation
// can compare the ledger with the driver's counters, which count every type.
func TestIdentityLedgerKeysMeasurementsAndCountsEveryType(t *testing.T) {
	l := NewIdentityLedger()
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 123456000, time.UTC)
	l.Record(OutcomeAccepted, "a", "Measurement", t0.Format(time.RFC3339Nano))
	l.Record(OutcomeAccepted, "a", "Location", t0.Format(time.RFC3339Nano))
	l.Record(OutcomeRefused, "a", "Measurement", t0.Add(time.Microsecond).Format(time.RFC3339Nano))
	l.Record(OutcomeAmbiguous, "b", "Measurement", t0.Add(2*time.Microsecond).Format(time.RFC3339Nano))
	l.Record(OutcomeRefused, "b", "Location", t0.Format(time.RFC3339Nano))
	// A stamp the store cannot hold exactly is counted, so the reconciliation can refuse it.
	l.Record(OutcomeAccepted, "c", "Measurement", t0.Add(7).Format(time.RFC3339Nano))

	s := l.Snapshot()
	if want := (OutcomeTotals{Accepted: 3, Refused: 2, Ambiguous: 1}); s.All != want {
		t.Errorf("All = %+v; want %+v", s.All, want)
	}
	if want := (OutcomeTotals{Accepted: 2, Refused: 1, Ambiguous: 1}); s.Measurements() != want {
		t.Errorf("Measurements() = %+v; want %+v", s.Measurements(), want)
	}
	us := t0.UnixMicro()
	if got, want := s.Devices["a"], (DeviceIdentities{Accepted: []int64{us}, Refused: []int64{us + 1}}); !reflect.DeepEqual(got, want) {
		t.Errorf("device a = %+v; want %+v", got, want)
	}
	if got, want := s.Devices["b"], (DeviceIdentities{Ambiguous: []int64{us + 2}}); !reflect.DeepEqual(got, want) {
		t.Errorf("device b = %+v; want %+v", got, want)
	}
	if s.SubMicro != 1 {
		t.Errorf("SubMicro = %d; want 1 for the stamp with a nanosecond part", s.SubMicro)
	}
}

// The snapshot is a copy: recording after it must not change what the reader holds.
func TestIdentityLedgerSnapshotIsACopy(t *testing.T) {
	l := NewIdentityLedger()
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	l.Record(OutcomeAccepted, "a", "Measurement", t0.Format(time.RFC3339Nano))
	s := l.Snapshot()
	l.Record(OutcomeAccepted, "a", "Measurement", t0.Add(time.Second).Format(time.RFC3339Nano))
	l.Record(OutcomeRefused, "a", "Measurement", t0.Add(2*time.Second).Format(time.RFC3339Nano))
	if got := s.Devices["a"]; len(got.Accepted) != 1 || len(got.Refused) != 0 {
		t.Errorf("snapshot changed after a later record: %+v", got)
	}
	if s.All.Accepted != 1 {
		t.Errorf("snapshot totals changed after a later record: %+v", s.All)
	}
}

// identityIngress answers by device token: each device gets one response class, so each
// identity's ledger bucket can be asserted by value.
func identityIngress(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			Device string `json:"device"`
		}
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		switch ev.Device {
		case "dev-00001":
			w.WriteHeader(http.StatusAccepted)
		case "dev-00002":
			w.WriteHeader(http.StatusTooManyRequests)
		case "dev-00003":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
		case "dev-00004":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "dev-00005":
			w.WriteHeader(http.StatusBadRequest)
		case "dev-00006":
			w.WriteHeader(http.StatusInternalServerError)
		case "dev-00007":
			// The connection drops before any response: the request may have been processed.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("the test server cannot hijack a connection")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		default:
			t.Errorf("unexpected device %q", ev.Device)
		}
	}
}

// postEvent files every emit that reached the wire by what the response says about the
// store. The dangerous misfilings are a bare 503 or a dropped connection filed REFUSED (a
// stored copy would then read as a false refusal and fail a correct run) and a 429 or a
// backpressure 503 filed ambiguous (a stored refusal would then be allowed).
func TestPostEventFilesIdentityByOutcome(t *testing.T) {
	rt := fakeIngress(t, 7, identityIngress(t))
	rt.Identity = NewIdentityLedger()
	_ = EmitAll(context.Background(), rt, 3, constantMetrics)

	want := map[string]string{
		"dev-00001": "accepted",  // 202
		"dev-00002": "refused",   // 429
		"dev-00003": "refused",   // 503 with a Retry-After
		"dev-00004": "ambiguous", // bare 503
		"dev-00005": "refused",   // other 4xx
		"dev-00006": "ambiguous", // other 5xx
		"dev-00007": "ambiguous", // connection dropped
	}
	s := rt.Identity.Snapshot()
	for dev, bucket := range want {
		d := s.Devices[dev]
		got := map[string]int{"accepted": len(d.Accepted), "refused": len(d.Refused), "ambiguous": len(d.Ambiguous)}
		for b, n := range got {
			wantN := 0
			if b == bucket {
				wantN = 1
			}
			if n != wantN {
				t.Errorf("%s: %d in %s; want %d (expected bucket %s)", dev, n, b, wantN, bucket)
			}
		}
	}
	if want := (OutcomeTotals{Accepted: 1, Refused: 3, Ambiguous: 3}); s.All != want {
		t.Errorf("All = %+v; want %+v", s.All, want)
	}
	// The driver's own counters are unchanged by the ledger: a non-429 4xx is still a
	// failure there, and only the two clean refusals are shed or backpressured.
	snap := rt.Stats.Snapshot(time.Now())
	if snap.Emitted != 1 || snap.Shed != 1 || snap.Backpressured != 1 || snap.Failed != 4 {
		t.Errorf("stats = emitted %d shed %d backpressured %d failed %d; want 1/1/1/4",
			snap.Emitted, snap.Shed, snap.Backpressured, snap.Failed)
	}
	// The identity recorded is the stamp that was sent, in microseconds.
	if a := s.Devices["dev-00001"].Accepted; len(a) == 1 {
		latest, _ := rt.Identity.LatestAccepted()
		if latest["dev-00001"].UnixMicro() != a[0] {
			t.Errorf("accepted identity %d does not match the latest accepted time %v", a[0], latest["dev-00001"])
		}
	}
}

// A Location emit is counted but not keyed: the reconciliation reads Measurements only.
// A runtime without a ledger posts as before.
func TestPostEventKeysOnlyMeasurements(t *testing.T) {
	rt := fakeIngress(t, 1, accepted)
	rt.Identity = NewIdentityLedger()
	if err := EmitLocation(context.Background(), rt, rt.Devices[0], Fix{Latitude: 1, Longitude: 2}); err != nil {
		t.Fatal(err)
	}
	s := rt.Identity.Snapshot()
	if len(s.Devices) != 0 || s.All.Accepted != 1 {
		t.Errorf("after one Location: devices %v, totals %+v; want no identities and 1 accepted", s.Devices, s.All)
	}
	if latest, _ := rt.Identity.LatestAccepted(); latest[rt.Devices[0].Token].IsZero() {
		t.Error("an accepted Location must still move the device's latest accepted time")
	}

	bare := fakeIngress(t, 1, accepted)
	if err := EmitMeasurement(context.Background(), bare, bare.Devices[0], "k", 1); err != nil {
		t.Fatalf("a runtime with no ledger must still emit: %v", err)
	}
}
