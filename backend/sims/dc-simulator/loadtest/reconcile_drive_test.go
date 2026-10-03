// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// recordAll files every identity in a fresh ledger with the given outcome.
func recordAll(led *sim.IdentityLedger, o sim.EmitOutcome, dev string, times ...int64) {
	for _, us := range times {
		led.Record(o, dev, "Measurement", time.UnixMicro(us).UTC().Format(time.RFC3339Nano))
	}
}

// driveProfile is a profile whose quiesce runs in milliseconds.
func driveProfile(minAccepted int64) Profile {
	return Profile{MinAccepted: minAccepted, QuiescePoll: time.Millisecond, QuiesceTimeout: 30 * time.Millisecond}
}

// invariantNames lists the names, in order.
func invariantNames(invs []Invariant) []string {
	var out []string
	for _, inv := range invs {
		out = append(out, inv.Name)
	}
	return out
}

// 🔴 The caller test: the issue's A,A,C,D store, driven through the sequence Run uses.
// The count invariants pass on it and the identity invariant must be APPENDED and
// failed — computing it and dropping it would leave the run green.
func TestReconcileDriveAppendsIdentity(t *testing.T) {
	led := sim.NewIdentityLedger()
	recordAll(led, sim.OutcomeAccepted, "d1", idA, idB, idC, idD)
	store := &storeFake{rows: map[string][]int64{"d1": {idA, idA, idC, idD}}}

	v, err := reconcileDrive(context.Background(), store, store, led, []string{"d1"},
		sim.Snapshot{Emitted: 4}, Window{}, time.Now(), driveProfile(4))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(invariantNames(v.Invariants), ","), "load-applied,clean-drive,ingest-completeness,ingest-identity"; got != want {
		t.Fatalf("invariants = %s; want %s", got, want)
	}
	if !invByName(t, v.Invariants, InvCompleteness).Passed {
		t.Error("the count pre-check should pass equal totals")
	}
	if id := invByName(t, v.Invariants, InvIdentity); id.Passed {
		t.Errorf("ingest-identity passed a loss offset by a duplicate: %s", id.Detail)
	}
	if v.Identity.Missing != 1 || v.Identity.DuplicateKeys != 1 || v.Quiesce.Persisted != 4 || !v.Quiesce.Reached {
		t.Errorf("identity missing %d duplicated %d, quiesce %+v", v.Identity.Missing, v.Identity.DuplicateKeys, v.Quiesce)
	}
	report := &Report{Identity: &v.Identity, Invariants: v.Invariants}
	if report.Passed() {
		t.Error("the L1 report passed a loss offset by a duplicate")
	}

	// The counterweight: the intact store passes the whole sequence.
	store = &storeFake{rows: map[string][]int64{"d1": {idA, idB, idC, idD}}}
	v, err = reconcileDrive(context.Background(), store, store, led, []string{"d1"},
		sim.Snapshot{Emitted: 4}, Window{}, time.Now(), driveProfile(4))
	if err != nil {
		t.Fatal(err)
	}
	if !(&Report{Identity: &v.Identity, Invariants: v.Invariants}).Passed() {
		t.Errorf("an intact store failed: %v", v.Invariants)
	}
}

// lateRowStore counts one row more once the device reads have run: a row that arrived
// during the identity read.
type lateRowStore struct {
	*storeFake
	mu   sync.Mutex
	read bool
}

func (l *lateRowStore) DeviceIdentities(ctx context.Context, dev string, w Window) ([]int64, int, error) {
	l.mu.Lock()
	l.read = true
	l.mu.Unlock()
	return l.storeFake.DeviceIdentities(ctx, dev, w)
}

func (l *lateRowStore) Count(ctx context.Context, w Window) (int64, error) {
	n, err := l.storeFake.Count(ctx, w)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.read {
		n++
	}
	return n, err
}

// The tenant total is read AFTER the device reads, so a row that lands during the read
// shows as unattributed and fails the run. Read before, it would match the rows read and
// the late row would be invisible.
func TestReconcileDriveReadsTenantTotalAfterDevices(t *testing.T) {
	led := sim.NewIdentityLedger()
	recordAll(led, sim.OutcomeAccepted, "d1", idA, idB)
	store := &lateRowStore{storeFake: &storeFake{rows: map[string][]int64{"d1": {idA, idB}}}}
	v, err := reconcileDrive(context.Background(), store, store, led, []string{"d1"},
		sim.Snapshot{Emitted: 2}, Window{}, time.Now(), driveProfile(2))
	if err != nil {
		t.Fatal(err)
	}
	if v.Identity.TenantTotal != 3 || v.Identity.Unattributed != 1 || v.Identity.Unexpected != 1 {
		t.Errorf("tenant total %d, unattributed %d, unexpected %d; want 3, 1, 1",
			v.Identity.TenantTotal, v.Identity.Unattributed, v.Identity.Unexpected)
	}
	if invByName(t, v.Invariants, InvIdentity).Passed {
		t.Error("ingest-identity passed with a row the device reads did not see")
	}
}

// Identity is decided on a drive with failed emits, not skipped: the ambiguous ledger
// makes it sound. clean-drive still fails the run, so the gate's rule does not move.
func TestReconcileDriveRunsIdentityOnDirtyDrive(t *testing.T) {
	m1, m2 := idBase+8000, idBase+9000
	led := sim.NewIdentityLedger()
	recordAll(led, sim.OutcomeAccepted, "d1", idA, idB)
	recordAll(led, sim.OutcomeAmbiguous, "d1", m1, m2)
	store := &storeFake{rows: map[string][]int64{"d1": {idA, idB, m1}}}
	v, err := reconcileDrive(context.Background(), store, store, led, []string{"d1"},
		sim.Snapshot{Emitted: 2, Failed: 2}, Window{}, time.Now(), driveProfile(2))
	if err != nil {
		t.Fatal(err)
	}
	if invByName(t, v.Invariants, InvCleanDrive).Passed {
		t.Error("clean-drive passed a drive with 2 failed emits")
	}
	id := invByName(t, v.Invariants, InvIdentity)
	if !id.Passed || v.Identity.AmbiguousStored != 1 || v.Identity.AmbiguousAbsent != 1 {
		t.Errorf("identity passed=%v stored=%d absent=%d; want pass, 1, 1 (%s)",
			id.Passed, v.Identity.AmbiguousStored, v.Identity.AmbiguousAbsent, id.Detail)
	}
}

// A stored set that will not hold still under the read is an INCONCLUSIVE verdict, not
// an error: the run still produces its report, with the count invariants in it, which is
// the evidence a lagging or chaos run exists to keep.
func TestReconcileDriveUnstableReadStillReports(t *testing.T) {
	led := sim.NewIdentityLedger()
	recordAll(led, sim.OutcomeAccepted, "d1", idA)
	recordAll(led, sim.OutcomeAccepted, "d2", idB)
	store := &storeFake{rows: map[string][]int64{"d1": {idA}, "d2": {idB}}}
	reader := &scriptedReader{rows: store.rows, moves: map[string]int{"d2": -1}}
	v, err := reconcileDrive(context.Background(), store, reader, led, []string{"d1", "d2"},
		sim.Snapshot{Emitted: 2}, Window{}, time.Now(), driveProfile(2))
	if err != nil {
		t.Fatalf("an unstable read must not lose the report: %v", err)
	}
	if !invByName(t, v.Invariants, InvCompleteness).Passed {
		t.Error("the count invariants should still be decided")
	}
	id := invByName(t, v.Invariants, InvIdentity)
	if id.Passed || !strings.Contains(id.Detail, "inconclusive") || v.Identity.UnstableDevices != 1 {
		t.Errorf("identity passed=%v unstable=%d detail %q; want an inconclusive failure naming 1 unstable device",
			id.Passed, v.Identity.UnstableDevices, id.Detail)
	}
}

// The report states its observation horizon: the settle, when the read started and when
// the final total was read, each after the end of the drive.
func TestReconcileDriveRecordsTheObservationHorizon(t *testing.T) {
	led := sim.NewIdentityLedger()
	recordAll(led, sim.OutcomeAccepted, "d1", idA)
	store := &storeFake{rows: map[string][]int64{"d1": {idA}}}
	p := driveProfile(1)
	p.QuiesceSettle = 3 * time.Millisecond
	driveEnd := time.Now().Add(-2 * time.Second)
	v, err := reconcileDrive(context.Background(), store, store, led, []string{"d1"},
		sim.Snapshot{Emitted: 1}, Window{}, driveEnd, p)
	if err != nil {
		t.Fatal(err)
	}
	id := v.Identity
	if id.SettleSeconds != 0.003 {
		t.Errorf("settleSeconds = %v; want 0.003", id.SettleSeconds)
	}
	if id.ReadStartedAfterDriveSecs < 2 || id.ObservedUntilAfterDriveSecs < id.ReadStartedAfterDriveSecs {
		t.Errorf("read started %.3fs and observed until %.3fs after the drive; want >= 2 and in that order",
			id.ReadStartedAfterDriveSecs, id.ObservedUntilAfterDriveSecs)
	}
}

// A read-back that fails outright does not lose the run's report: the count invariants
// are still decided, and ingest-identity fails as inconclusive, naming the error.
func TestReconcileDriveFailedReadStillReports(t *testing.T) {
	led := sim.NewIdentityLedger()
	recordAll(led, sim.OutcomeAccepted, "d1", idA)
	recordAll(led, sim.OutcomeAccepted, "d2", idB)
	store := &storeFake{rows: map[string][]int64{"d1": {idA}, "d2": {idB}}}
	reader := &scriptedReader{rows: store.rows, hard: map[string]error{"d2": errors.New("connection reset by peer")}}
	v, err := reconcileDrive(context.Background(), store, reader, led, []string{"d1", "d2"},
		sim.Snapshot{Emitted: 2}, Window{}, time.Now(), driveProfile(2))
	if err != nil {
		t.Fatalf("a failed identity read must not lose the report: %v", err)
	}
	if got, want := strings.Join(invariantNames(v.Invariants), ","), "load-applied,clean-drive,ingest-completeness,ingest-identity"; got != want {
		t.Fatalf("invariants = %s; want %s", got, want)
	}
	if !invByName(t, v.Invariants, InvCompleteness).Passed {
		t.Error("the count invariants should still be decided")
	}
	id := invByName(t, v.Invariants, InvIdentity)
	if id.Passed || v.Identity.Reconciled || !strings.Contains(id.Detail, "connection reset by peer") ||
		len(v.Identity.Inconclusive) != 1 || !strings.Contains(v.Identity.Inconclusive[0], "connection reset by peer") {
		t.Errorf("identity passed=%v reconciled=%v detail %q inconclusive %q; want an inconclusive failure recording the error",
			id.Passed, v.Identity.Reconciled, id.Detail, v.Identity.Inconclusive)
	}
	if v.Identity.Accepted != 2 {
		t.Errorf("accepted = %d; want the ledger's 2 kept in the failed report", v.Identity.Accepted)
	}
}
