// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// refusingSnapshot is a drive in which the ingress answered every way at once. The
// counts all differ, so a counter read into the wrong field changes a value.
func refusingSnapshot() sim.Snapshot {
	return sim.Snapshot{Emitted: 2, Shed: 3, Backpressured: 4, Failed: 1, Ticks: 5, Rate: 0.5}
}

// 🔴 The L1, detection, command and monitored reports built their drive section without
// the shed count, so every one said "shed 0" however much the ingress refused, and a
// release gate's refusals had to be recomputed by hand as offered minus accepted. The
// drive section is now built in one place; this pins what it serializes, by the JSON keys
// the release gate's jq reads.
func TestDriveStatsCarriesEveryRefusal(t *testing.T) {
	start := time.Unix(1000, 0)
	d := newDriveStats(10, 20, refusingSnapshot(), start, start.Add(4*time.Second))
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var drive map[string]any
	if err := json.Unmarshal(raw, &drive); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"devices": 10, "targetRatePerSec": 20, "achievedRatePerSec": 0.5,
		"accepted": 2, "shed": 3, "backpressured": 4, "failed": 1, "ticks": 5, "holdSeconds": 4,
	}
	for k, v := range want {
		if got, ok := drive[k].(float64); !ok || got != v {
			t.Errorf("drive[%q] = %v, want %v (serialized %s)", k, drive[k], v, raw)
		}
	}
	if len(drive) != len(want) {
		t.Errorf("drive has %d keys, want %d: %s", len(drive), len(want), raw)
	}
}

// NEGATIVE CONTROL: a drive the ingress accepted in full reports no refusal — the ledger
// must not invent one.
func TestDriveStatsWithNoRefusals(t *testing.T) {
	d := newDriveStats(10, 20, sim.Snapshot{Emitted: 50, Ticks: 5}, time.Time{}, time.Time{})
	if d.Accepted != 50 || d.Shed != 0 || d.Backpressured != 0 || d.Failed != 0 {
		t.Errorf("ledger = %+v, want accepted 50 and nothing refused or failed", d.EmitLedger)
	}
}

// Every report's operator-readable line carries both refusal counts, not only its JSON:
// a log that drops them reads as "nothing was refused", the same defect in prose.
func TestEveryReportsHumanLineCarriesTheRefusals(t *testing.T) {
	d := newDriveStats(10, 20, refusingSnapshot(), time.Time{}, time.Time{})
	const want = "accepted 2, shed 3, backpressured 4, failed 1"
	for name, human := range map[string]func() string{
		"L1":        (&Report{Drive: d}).Human,
		"command":   (&CommandReport{Drive: d}).Human,
		"detection": (&DetectionReport{Drive: d}).Human,
		"monitored": (&MonitorReport{Drive: d}).Human,
		"presence":  (&PresenceReport{Drive: d}).Human,
		"batch":     (&BatchReport{Drive: d}).Human,
	} {
		if got := human(); !strings.Contains(got, want) {
			t.Errorf("%s report's Human() does not carry %q:\n%s", name, want, got)
		}
	}
}

// Presence and batch cannot measure a run whose background fleet was refused at the
// ingress for EITHER reason; the one description they share counts both, and names each.
func TestBackgroundRefusalsCountsBothKinds(t *testing.T) {
	if desc, ok := backgroundRefusals(sim.Snapshot{Emitted: 100}); ok {
		t.Errorf("no refusals, but backgroundRefusals reported %q", desc)
	}
	desc, ok := backgroundRefusals(sim.Snapshot{Emitted: 100, Backpressured: 4})
	if !ok {
		t.Fatal("the fleet was backpressured 4 times, but backgroundRefusals reported nothing")
	}
	if want := "refused 4 time(s) at the ingress (0 shed at the per-tenant ingest ceiling, 4 backpressured by the platform)"; desc != want {
		t.Errorf("backgroundRefusals = %q, want %q", desc, want)
	}
	desc, _ = backgroundRefusals(refusingSnapshot())
	if want := "refused 7 time(s) at the ingress (3 shed at the per-tenant ingest ceiling, 4 backpressured by the platform)"; desc != want {
		t.Errorf("backgroundRefusals = %q, want %q", desc, want)
	}
}
