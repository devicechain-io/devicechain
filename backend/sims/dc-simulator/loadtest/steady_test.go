// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func readMarker(t *testing.T, path string) steadyDoc {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	var d steadyDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("the marker is not JSON: %v\n%s", err, b)
	}
	return d
}

func TestSteadyMarkerWalksWarmupSteadyEnded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steady.json")
	mk := &SteadyMarker{Path: path, Phase: "R1", Warmup: time.Minute}
	start := time.Now()

	if err := mk.Begin(start, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if d := readMarker(t, path); d.State != SteadyWarmup || d.Phase != "R1" || d.Schema != SteadyMarkerSchema {
		t.Fatalf("after Begin: %+v", d)
	}

	// Inside the warm-up a tick must NOT promote: this is the half that stops a profile
	// from being taken across the ramp.
	if err := mk.Tick(start.Add(59 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if d := readMarker(t, path); d.State != SteadyWarmup {
		t.Fatalf("promoted to %s before the warm-up elapsed", d.State)
	}

	if err := mk.Tick(start.Add(61 * time.Second)); err != nil {
		t.Fatal(err)
	}
	steady := readMarker(t, path)
	if steady.State != SteadySteady {
		t.Fatalf("not steady after the warm-up: %+v", steady)
	}
	if wantEnd := start.Add(10 * time.Minute).UTC().Format(time.RFC3339Nano); steady.WindowEnds != wantEnd {
		t.Errorf("windowEnds = %s, want %s", steady.WindowEnds, wantEnd)
	}

	// A later tick must not move `since`: a collector compares it before and after.
	if err := mk.Tick(start.Add(90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if again := readMarker(t, path); again.Since != steady.Since {
		t.Errorf("since moved from %s to %s on a later tick", steady.Since, again.Since)
	}

	if err := mk.End(true); err != nil {
		t.Fatal(err)
	}
	if d := readMarker(t, path); d.State != SteadyEnded {
		t.Errorf("after a clean End: %s", d.State)
	}
}

func TestSteadyMarkerAbortedIsNotEnded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steady.json")
	mk := &SteadyMarker{Path: path, Warmup: 0}
	if err := mk.Begin(time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := mk.End(false); err != nil {
		t.Fatal(err)
	}
	if d := readMarker(t, path); d.State != SteadyAborted {
		t.Errorf("an aborted drive wrote %s", d.State)
	}
}

func TestSteadyMarkerOffIsInert(t *testing.T) {
	var nilMarker *SteadyMarker
	for _, m := range []*SteadyMarker{nilMarker, {}} {
		if err := m.Validate(time.Minute); err != nil {
			t.Error(err)
		}
		if err := m.Begin(time.Now(), time.Minute); err != nil {
			t.Error(err)
		}
		if err := m.Tick(time.Now()); err != nil {
			t.Error(err)
		}
		if err := m.End(true); err != nil {
			t.Error(err)
		}
	}
}

// A warm-up that is not shorter than the drive could never reach steady; it is refused up
// front instead of producing a marker that silently never says so.
func TestSteadyMarkerRefusesAWarmupLongerThanTheDrive(t *testing.T) {
	mk := &SteadyMarker{Path: filepath.Join(t.TempDir(), "s.json"), Warmup: 5 * time.Minute}
	if err := mk.Validate(5 * time.Minute); err == nil {
		t.Error("a warm-up equal to the drive was accepted")
	}
	if err := mk.Validate(10 * time.Minute); err != nil {
		t.Errorf("a warm-up shorter than the drive was refused: %v", err)
	}
}

// An unwritable marker path must fail the drive, not be skipped: a collector told to wait
// for a steady window would otherwise wait for ever.
func TestSteadyMarkerWriteFailureFailsTheDrive(t *testing.T) {
	mk := &SteadyMarker{Path: filepath.Join(t.TempDir(), "missing-dir", "s.json")}
	if err := mk.Begin(time.Now(), time.Minute); err == nil {
		t.Error("Begin into a missing directory reported success")
	}
}

// The drive loop itself writes the marker: warmup at the start, ended at the end.
func TestDriveMarkedWritesTheMarkerAroundTheDrive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	mk := &SteadyMarker{Path: path, Phase: "R1", Warmup: 0}
	rt := &sim.Runtime{Load: sim.Load{EmitInterval: 10 * time.Millisecond}}
	var sawSteady bool
	fs := &markerSpy{path: path, saw: &sawSteady}
	if _, _, err := driveMarked(context.Background(), rt, fs, 100*time.Millisecond, mk); err != nil {
		t.Fatal(err)
	}
	if !sawSteady {
		t.Error("the marker never read steady while the drive was ticking")
	}
	if d := readMarker(t, path); d.State != SteadyEnded {
		t.Errorf("after the drive: %s, want ended", d.State)
	}
}

// markerSpy is a sim that records whether the marker read steady during one of its ticks.
type markerSpy struct {
	fakeSim
	path string
	saw  *bool
}

func (m *markerSpy) Tick(ctx context.Context, rt *sim.Runtime) error {
	if b, err := os.ReadFile(m.path); err == nil {
		var d steadyDoc
		if json.Unmarshal(b, &d) == nil && d.State == SteadySteady {
			*m.saw = true
		}
	}
	return m.fakeSim.Tick(ctx, rt)
}
