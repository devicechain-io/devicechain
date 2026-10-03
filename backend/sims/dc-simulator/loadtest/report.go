// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Invariant is one asserted correctness property and its verdict. A single
// failed invariant fails the whole run (ADR-064 decision 1) — the report is a
// hard gate, not advisory.
type Invariant struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// EmitLedger is what the driver's own counters say became of every offered emit:
// accepted, refused (shed at the tenant's ceiling, or backpressured by the
// platform) or failed. Every DriveStats is built by newDriveStats, which reads it
// from one sim.Snapshot through ledgerOf — so a drive section cannot carry
// accepted without shed and backpressured beside it, the shape that once left
// every L1 report saying "shed 0" however much the ingress refused.
type EmitLedger struct {
	AchievedRatePS float64 `json:"achievedRatePerSec"`
	Accepted       int64   `json:"accepted"`
	// Shed counts emits the ingress refused at the per-tenant ceiling (HTTP 429).
	// It is distinct from Failed (real errors / indeterminate outcomes): a shed is a
	// clean non-accept. Under the L3 contention profile it is gated.
	Shed int64 `json:"shed"`
	// Backpressured counts emits refused by the platform's shared backpressure gate
	// (HTTP 503 with a Retry-After). Like a shed it never entered the pipeline, but
	// it is the platform being behind for every tenant, not the tenant's ceiling.
	Backpressured int64 `json:"backpressured"`
	Failed        int64 `json:"failed"`
	Ticks         int64 `json:"ticks"`
}

// ledgerOf is the one reader of a driver snapshot into a report's ledger.
func ledgerOf(s sim.Snapshot) EmitLedger {
	return EmitLedger{
		AchievedRatePS: s.Rate,
		Accepted:       s.Emitted,
		Shed:           s.Shed,
		Backpressured:  s.Backpressured,
		Failed:         s.Failed,
		Ticks:          s.Ticks,
	}
}

// outcome renders the ledger's counts — shared by every report's Human(), so no
// report's log line can drop a refusal its JSON carries.
func (l EmitLedger) outcome() string {
	return fmt.Sprintf("accepted %d, shed %d, backpressured %d, failed %d",
		l.Accepted, l.Shed, l.Backpressured, l.Failed)
}

// backgroundRefusals describes a background drive's clean refusals — shed at the
// tenant's ceiling AND backpressured — for the harnesses whose verdict a refusal of
// EITHER kind makes unmeasurable (presence, batch). ok is false when the ingress
// refused nothing. One reader, so the two harnesses cannot disagree about which
// refusals count.
func backgroundRefusals(snap sim.Snapshot) (desc string, ok bool) {
	if snap.Refused() == 0 {
		return "", false
	}
	return fmt.Sprintf("refused %d time(s) at the ingress (%d shed at the per-tenant ingest ceiling, %d backpressured by the platform)",
		snap.Refused(), snap.Shed, snap.Backpressured), true
}

// DriveStats is the perf-side accounting of what the driver actually applied —
// configured vs. achieved, so a run states the load it reached, not the one it
// asked for (the sim.Stats honesty, carried into the report). It is evidence,
// not a gated invariant at L1; the gated correctness lives in Invariants.
//
// The ledger is EMBEDDED: encoding/json flattens it, so .drive.accepted,
// .drive.shed, .drive.backpressured and .drive.failed sit directly under drive.
type DriveStats struct {
	Devices      int     `json:"devices"`
	TargetRatePS float64 `json:"targetRatePerSec"`
	EmitLedger
	HoldSeconds float64 `json:"holdSeconds"`
}

// newDriveStats is the only way a report's drive section is built.
func newDriveStats(devices int, target float64, snap sim.Snapshot, start, end time.Time) DriveStats {
	return DriveStats{
		Devices:      devices,
		TargetRatePS: target,
		EmitLedger:   ledgerOf(snap),
		HoldSeconds:  end.Sub(start).Seconds(),
	}
}

// Report is the machine- and human-readable result of one load-test run: the
// profile that produced it (for reproducibility), the driver's applied load, the
// oracle's quiesce observation, and the per-invariant verdicts.
type Report struct {
	Manifest      string     `json:"manifest"`
	Seed          int64      `json:"seed"`
	Tenant        string     `json:"tenant"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    time.Time  `json:"finishedAt"`
	Drive         DriveStats `json:"drive"`
	PersistedSeen int64      `json:"persistedEvents"`
	Reached       bool       `json:"reachedTarget"`
	QuiesceSecs   float64    `json:"quiesceSeconds"`
	// StateCaughtUp is whether the live device state reached every device's last accepted
	// event, and StateLagSecs how long after the drive ended the check finished; both
	// absent when the check was not run (--state-timeout 0).
	StateCaughtUp *bool   `json:"stateCaughtUp,omitempty"`
	StateLagSecs  float64 `json:"stateLagSeconds,omitempty"`
	// Identity is the identity reconciliation's evidence. Run always sets it; it is a
	// pointer only so a report without it is visibly one, and Passed refuses it.
	Identity   *IdentityReport `json:"identity,omitempty"`
	Invariants []Invariant     `json:"invariants"`
}

// Passed reports whether every invariant held. An empty invariant set is NOT a
// pass — a report with nothing asserted has proven nothing, so the gate treats
// it as a failure rather than a vacuous green. Nor is a report that carries no
// identity reconciliation: equal totals alone do not show that every accepted event
// was stored once, so a report missing the identity section or its invariant cannot
// certify a run, whichever caller built it.
func (r *Report) Passed() bool {
	if len(r.Invariants) == 0 || r.Identity == nil || invariantByName(r.Invariants, InvIdentity) == nil {
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
	fmt.Fprintf(&b, "  drive: %d devices, target %.1f ev/s, achieved %.1f ev/s over %.0fs — %s, ticks %d\n",
		r.Drive.Devices, r.Drive.TargetRatePS, r.Drive.AchievedRatePS, r.Drive.HoldSeconds,
		r.Drive.outcome(), r.Drive.Ticks)
	fmt.Fprintf(&b, "  oracle: persisted %d, reached-target %v in %.0fs\n", r.PersistedSeen, r.Reached, r.QuiesceSecs)
	if id := r.Identity; id != nil {
		fmt.Fprintf(&b, "  identity: accepted %d, stored %d; missing %d, duplicated %d, unexpected %d (refused stored %d); ambiguous %d (stored %d, absent %d); read %d devices in %.0fs, observed until %.0fs after the drive\n",
			id.Accepted, id.Persisted, id.Missing, id.DuplicateKeys, id.Unexpected, id.RefusedStored,
			id.Ambiguous, id.AmbiguousStored, id.AmbiguousAbsent, id.DevicesRead, id.ReadSeconds, id.ObservedUntilAfterDriveSecs)
	} else {
		fmt.Fprintf(&b, "  identity: NOT checked\n")
	}
	switch {
	case r.StateCaughtUp == nil:
		fmt.Fprintf(&b, "  state: not checked\n")
	case *r.StateCaughtUp:
		fmt.Fprintf(&b, "  state: caught up %.0fs after the drive\n", r.StateLagSecs)
	default:
		fmt.Fprintf(&b, "  state: NOT caught up %.0fs after the drive\n", r.StateLagSecs)
	}
	for _, inv := range r.Invariants {
		mark := "FAIL"
		if inv.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%-4s] %s — %s\n", mark, inv.Name, inv.Detail)
	}
	return b.String()
}
