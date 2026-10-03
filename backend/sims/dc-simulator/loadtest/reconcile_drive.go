// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// driveVerdict is the reconciliation of one finished L1 drive.
type driveVerdict struct {
	Quiesce QuiesceResult
	// Invariants are Reconcile's three count invariants, then ingest-identity.
	Invariants []Invariant
	Identity   IdentityReport
}

// reconcileDrive is L1's whole storage verdict, in order: wait for the windowed count to
// reach the accepted target (Oracle.Await), decide the count pre-check (Reconcile), then
// reconcile by identity (checkIdentity) and append its invariant.
//
// It is separate from Run, which needs a live cluster, so the sequence is unit-tested
// with fakes: the identity invariant is APPENDED, not merely computed, and the tenant
// total it uses is read after the device reads. Report.Passed also refuses a report with
// no ingest-identity invariant, so a caller that skipped this step fails closed.
//
// Identity is decided even when the drive had failed emits: the ambiguous ledger is what
// makes that sound, and it is the evidence a chaos run reads. The count invariants still
// fail such a run through clean-drive, so the gate's zero-failure rule does not move.
func reconcileDrive(ctx context.Context, counter eventCounter, reader identityReader, led *sim.IdentityLedger,
	devices []string, snap sim.Snapshot, w Window, driveEnd time.Time, p Profile) (driveVerdict, error) {
	oracle := &Oracle{Counter: counter, Poll: p.QuiescePoll, Timeout: p.QuiesceTimeout, Settle: p.QuiesceSettle}
	qr, err := oracle.Await(ctx, w, snap.Emitted)
	if err != nil {
		return driveVerdict{}, fmt.Errorf("oracle read-back: %w", err)
	}
	invariants := Reconcile(snap.Emitted, snap.Failed, qr.Persisted, p.MinAccepted)
	rep, inv, err := checkIdentity(ctx, counter, reader, led, devices, snap, w, driveEnd, p.QuiesceSettle)
	if err != nil {
		return driveVerdict{}, err
	}
	return driveVerdict{Quiesce: qr, Invariants: append(invariants, inv), Identity: rep}, nil
}
