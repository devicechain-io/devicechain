// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Invariant is one asserted correctness property and its verdict. A single
// failed invariant fails the whole run (ADR-064 decision 1) — the report is a
// hard gate, not advisory.
type Invariant struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// DriveStats is the perf-side accounting of what the driver actually applied —
// configured vs. achieved, so a run states the load it reached, not the one it
// asked for (the sim.Stats honesty, carried into the report). It is evidence,
// not a gated invariant at L1; the gated correctness lives in Invariants.
type DriveStats struct {
	Devices        int     `json:"devices"`
	TargetRatePS   float64 `json:"targetRatePerSec"`
	AchievedRatePS float64 `json:"achievedRatePerSec"`
	Accepted       int64   `json:"accepted"`
	Failed         int64   `json:"failed"`
	Ticks          int64   `json:"ticks"`
	HoldSeconds    float64 `json:"holdSeconds"`
}

// Report is the machine- and human-readable result of one load-test run: the
// profile that produced it (for reproducibility), the driver's applied load, the
// oracle's quiesce observation, and the per-invariant verdicts.
type Report struct {
	Manifest      string      `json:"manifest"`
	Seed          int64       `json:"seed"`
	Tenant        string      `json:"tenant"`
	StartedAt     time.Time   `json:"startedAt"`
	FinishedAt    time.Time   `json:"finishedAt"`
	Drive         DriveStats  `json:"drive"`
	PersistedSeen int64       `json:"persistedEvents"`
	Quiesced      bool        `json:"quiesced"`
	QuiesceSecs   float64     `json:"quiesceSeconds"`
	Invariants    []Invariant `json:"invariants"`
}

// Passed reports whether every invariant held. An empty invariant set is NOT a
// pass — a report with nothing asserted has proven nothing, so the gate treats
// it as a failure rather than a vacuous green.
func (r *Report) Passed() bool {
	if len(r.Invariants) == 0 {
		return false
	}
	for _, inv := range r.Invariants {
		if !inv.Passed {
			return false
		}
	}
	return true
}

// JSON renders the report as indented JSON for the CI artifact.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Human renders a terse operator-readable summary: the verdict headline, the
// applied-load line, and one line per invariant.
func (r *Report) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Passed() {
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "load-test %s — %s (seed %d, tenant %s)\n", verdict, r.Manifest, r.Seed, r.Tenant)
	fmt.Fprintf(&b, "  drive: %d devices, target %.1f ev/s, achieved %.1f ev/s over %.0fs — accepted %d, failed %d, ticks %d\n",
		r.Drive.Devices, r.Drive.TargetRatePS, r.Drive.AchievedRatePS, r.Drive.HoldSeconds,
		r.Drive.Accepted, r.Drive.Failed, r.Drive.Ticks)
	fmt.Fprintf(&b, "  oracle: persisted %d, quiesced %v in %.0fs\n", r.PersistedSeen, r.Quiesced, r.QuiesceSecs)
	for _, inv := range r.Invariants {
		mark := "FAIL"
		if inv.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%-4s] %s — %s\n", mark, inv.Name, inv.Detail)
	}
	return b.String()
}
