// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The steady-state marker is how a profiling collector (hack/perf-collect.sh) knows it is
// looking at the steady window and not at a ramp, a stall or a drain.
//
// THE CONTRACT. A harness given a marker path writes ONE small JSON file, replacing it
// atomically at each state change:
//
//		{"schema":1, "phase":"R1", "state":"warmup"|"steady"|"ended"|"aborted",
//		 "since":"<RFC3339 UTC of this state>", "startedAt":"<drive start>",
//		 "windowEnds":"<RFC3339 UTC the drive is due to stop>", "pid":N}
//
//	  - "warmup": the drive has started and is inside its warm-up. Nothing may be collected.
//	  - "steady": the warm-up is over and the drive is still running. `since` is when it
//	    began; `windowEnds` is when the drive is due to stop, so a collector can refuse a
//	    collection longer than the time left.
//	  - "ended" / "aborted": the drive stopped, cleanly or not. A collection that straddles
//	    either is discarded by the collector, which re-reads the marker afterwards and
//	    requires the same steady `since`.
//
// A harness that never writes the marker leaves no file, and the collector REFUSES to
// collect: absence is not steady. The marker is the harness's claim about ITS OWN drive;
// it says nothing about whether the platform is keeping up, which is the load test's own
// verdict, not a profiling precondition.
const SteadyMarkerSchema = 1

// SteadyState is the marker's state field.
type SteadyState string

const (
	SteadyWarmup  SteadyState = "warmup"
	SteadySteady  SteadyState = "steady"
	SteadyEnded   SteadyState = "ended"
	SteadyAborted SteadyState = "aborted"
)

// SteadyMarker writes the marker for one drive. The zero value and a nil pointer are both
// off, so callers need no guard. Methods are safe for concurrent use.
type SteadyMarker struct {
	// Path is the marker file; empty is off.
	Path string
	// Phase labels the marker (for example "R1"); the collector checks it against the phase
	// it was asked to collect.
	Phase string
	// Warmup is how long into the drive the steady state begins. It must be shorter than
	// the drive, or the marker would never reach "steady".
	Warmup time.Duration

	mu         sync.Mutex
	started    time.Time
	windowEnds time.Time
	state      SteadyState
}

// Validate refuses a marker that could never say "steady".
func (m *SteadyMarker) Validate(drive time.Duration) error {
	if m == nil || m.Path == "" {
		return nil
	}
	if m.Warmup < 0 {
		return fmt.Errorf("steady warm-up %s is negative", m.Warmup)
	}
	if m.Warmup >= drive {
		return fmt.Errorf("steady warm-up %s is not shorter than the drive (%s): the marker would never reach steady", m.Warmup, drive)
	}
	return nil
}

// Begin records the drive's start and writes "warmup" (or "steady" at once for a zero
// warm-up). due is how long the drive is meant to run.
func (m *SteadyMarker) Begin(start time.Time, due time.Duration) error {
	if m == nil || m.Path == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = start
	m.windowEnds = start.Add(due)
	return m.set(SteadyWarmup, start)
}

// Tick promotes warmup to steady once the warm-up has elapsed. It is cheap enough to call
// on every tick.
func (m *SteadyMarker) Tick(now time.Time) error {
	if m == nil || m.Path == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != SteadyWarmup || now.Sub(m.started) < m.Warmup {
		return nil
	}
	return m.set(SteadySteady, now)
}

// End writes the terminal state: ended for a drive that ran to its end, aborted otherwise.
func (m *SteadyMarker) End(clean bool) error {
	if m == nil || m.Path == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := SteadyAborted
	if clean {
		st = SteadyEnded
	}
	return m.set(st, time.Now())
}

type steadyDoc struct {
	Schema     int         `json:"schema"`
	Phase      string      `json:"phase"`
	State      SteadyState `json:"state"`
	Since      string      `json:"since"`
	StartedAt  string      `json:"startedAt"`
	WindowEnds string      `json:"windowEnds"`
	PID        int         `json:"pid"`
}

// set writes the marker atomically: a collector polling the file sees the old document or
// the new one, never a torn one.
func (m *SteadyMarker) set(st SteadyState, at time.Time) error {
	m.state = st
	doc := steadyDoc{
		Schema: SteadyMarkerSchema, Phase: m.Phase, State: st,
		Since:      at.UTC().Format(time.RFC3339Nano),
		StartedAt:  m.started.UTC().Format(time.RFC3339Nano),
		WindowEnds: m.windowEnds.UTC().Format(time.RFC3339Nano),
		PID:        os.Getpid(),
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.Path), filepath.Base(m.Path)+".tmp*")
	if err != nil {
		return fmt.Errorf("steady marker: %w", err)
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("steady marker: write: %v %v", werr, cerr)
	}
	if err := os.Rename(tmp.Name(), m.Path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("steady marker: %w", err)
	}
	return nil
}
