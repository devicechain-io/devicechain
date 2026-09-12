// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Tests for the local record a refused second instance must not leave behind.
//
// 🔴 THE PHANTOM IS A MEASURED FAILURE, NOT A TIDINESS POINT. `dcctl bootstrap` writes
// ~/.devicechain/<instance>/instance.json BEFORE the pipeline runs, so a run refused
// three steps in has already recorded an instance that does not exist — and nothing can
// clear it, because `dcctl destroy` returns on its own refusal before removeInstanceState.
// It was cleared by hand the first time this was seen on a live cluster, and a hand-run
// `rm -rf` standing in for a finding is how the finding gets lost.

// aRecordFor is what the command layer writes before the pipeline starts.
func aRecordFor(instance, cluster string) InstanceRecord {
	return InstanceRecord{
		Instance:    instance,
		Provider:    "local",
		Cluster:     cluster,
		KubeContext: "kind-" + cluster,
		Managed:     false,
		CreatedAt:   time.Now().UTC(),
	}
}

// listed reports whether `dcctl instances list` would show this instance. It reads the
// same function the command does, because the acceptance criterion is about the LISTING
// and the listing enumerates DIRECTORIES — a record file removed from a directory left
// behind still prints a row.
func listed(t *testing.T, instance string) bool {
	t.Helper()
	known, err := ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range known {
		if k.Instance == instance {
			return true
		}
	}
	return false
}

// 🔴 THE ACCEPTANCE CRITERION, STATED AS THE OPERATOR EXPERIENCES IT. After the refusal,
// `dcctl instances list` shows nothing and no directory has to be removed by hand.
func TestARefusedSecondInstanceLeavesNothingForTheListingToShow(t *testing.T) {
	fakeHome(t)

	prior := CapturePriorLocalState("bravo")
	if err := WriteInstanceRecord(aRecordFor("bravo", "alpha-cluster")); err != nil {
		t.Fatal(err)
	}
	if !listed(t, "bravo") {
		t.Fatal("the fixture never produced the phantom this test is about, so a passing " +
			"result below would mean nothing")
	}

	removed, err := prior.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("the rollback did not report removing the state it removed, so the operator " +
			"is never told the record is gone")
	}
	if listed(t, "bravo") {
		t.Fatal("`dcctl instances list` still shows an instance that was refused before " +
			"anything was installed, and no dcctl path can clear it")
	}
}

// 🔴 THE NEGATIVE CONTROL, AND IT IS A REAL INSTANCE. WriteInstanceRecord REPLACES rather
// than merges, so a run naming an instance that already exists somewhere else — a
// mistyped --kube-context is the shape — has already overwritten that instance's binding
// by the time the refusal fires. A rollback that DELETED would take a live instance's
// record away and leave its cluster undestroyable; putting back exactly what was there
// leaves both describable.
func TestARollbackRestoresTheRecordOfAnInstanceThatAlreadyExisted(t *testing.T) {
	fakeHome(t)

	if err := WriteInstanceRecord(aRecordFor("bravo", "bravo-cluster")); err != nil {
		t.Fatal(err)
	}
	prior := CapturePriorLocalState("bravo")

	// The run this test is about: the same name, pointed at somebody else's cluster.
	if err := WriteInstanceRecord(aRecordFor("bravo", "alpha-cluster")); err != nil {
		t.Fatal(err)
	}
	if rec, err := ReadInstanceRecord("bravo"); err != nil || rec.Cluster != "alpha-cluster" {
		t.Fatalf("the fixture never clobbered the record it is about to restore (%+v, %v)", rec, err)
	}

	removed, err := prior.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("the rollback removed the state of an instance that already existed")
	}
	rec, err := ReadInstanceRecord("bravo")
	if err != nil {
		t.Fatalf("the restored record cannot be read, which lands `dcctl destroy` back on "+
			"the guess that deletes kind-<instance>: %v", err)
	}
	if rec.Cluster != "bravo-cluster" {
		t.Fatalf("the restored record points at %q, not the cluster the instance is actually "+
			"in", rec.Cluster)
	}
}

// A record this build cannot parse is still somebody's record. It is put back verbatim
// rather than re-marshalled through InstanceRecord, which would silently drop whatever a
// newer dcctl had written into it.
func TestARollbackPutsBackARecordItCouldNotParse(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".devicechain", "bravo")
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{\"instance\":\"bravo\",\"somethingNewer\":true,\n")
	if err := os.WriteFile(filepath.Join(dir, instanceRecordFile), corrupt, stateFileMode); err != nil {
		t.Fatal(err)
	}

	prior := CapturePriorLocalState("bravo")
	if err := WriteInstanceRecord(aRecordFor("bravo", "alpha-cluster")); err != nil {
		t.Fatal(err)
	}
	if _, err := prior.Restore(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, instanceRecordFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("the record came back as %q rather than the bytes that were there", got)
	}
}

// A directory that was already there is not this run's to remove — it is an instance from
// before records existed, or what a destroy left behind. Only the record this run added
// goes.
func TestARollbackLeavesADirectoryItDidNotCreate(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".devicechain", "bravo")
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "terraform.tfstate")
	if err := os.WriteFile(stray, []byte("{}"), stateFileMode); err != nil {
		t.Fatal(err)
	}

	prior := CapturePriorLocalState("bravo")
	if err := WriteInstanceRecord(aRecordFor("bravo", "alpha-cluster")); err != nil {
		t.Fatal(err)
	}
	if removed, err := prior.Restore(); err != nil || removed {
		t.Fatalf("Restore() = %v, %v; a directory that was already there was reported as this "+
			"run's", removed, err)
	}

	if _, err := os.Stat(stray); err != nil {
		t.Errorf("the rollback removed state it did not write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, instanceRecordFile)); !os.IsNotExist(err) {
		t.Errorf("the record this run wrote is still there: %v", err)
	}
}

// 🔴 ROOT-KEY MATERIAL IS NEVER COLLATERAL. Nothing dcctl writes should put an escrow
// artifact under the instance directory — resolveEscrowPath refuses to — but an operator
// putting one where it seemed natural is exactly the case the sparing walk exists for, and
// a rollback is not the place to take a second opinion on it. Deleting a live escrow
// artifact costs every secret in a database backup that is still in object storage,
// silently.
func TestARollbackSparesRootKeyEscrowMaterialAndKeepsTheDirectoryHoldingIt(t *testing.T) {
	home := fakeHome(t)

	prior := CapturePriorLocalState("bravo")
	if err := WriteInstanceRecord(aRecordFor("bravo", "alpha-cluster")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".devicechain", "bravo")
	artifact := filepath.Join(dir, "bravo.escrow")
	if err := os.WriteFile(artifact, []byte("sealed"), stateFileMode); err != nil {
		t.Fatal(err)
	}

	removed, err := prior.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("the rollback reported removing a directory it had to keep, so the operator " +
			"is told the state is gone while it is not")
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("the rollback deleted root-key escrow material: %v", err)
	}
}

// The capture is a rollback aid, so being unable to prepare it must not fail a bootstrap —
// and a Restore built on a capture that never happened must do NOTHING rather than act on
// an unknown starting point.
func TestAnUnreadableCaptureRestoresNothing(t *testing.T) {
	home := fakeHome(t)
	if err := WriteInstanceRecord(aRecordFor("bravo", "bravo-cluster")); err != nil {
		t.Fatal(err)
	}

	removed, err := PriorLocalState{instance: "bravo"}.Restore()
	if err != nil || removed {
		t.Fatalf("Restore() = %v, %v on a state that was never captured", removed, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "bravo", instanceRecordFile)); err != nil {
		t.Errorf("a rollback with nothing to roll back to removed a live record anyway: %v", err)
	}
}
