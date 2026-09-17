// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/dcdir"
)

// Tests for the instance record and the destroy paths it drives.
//
// 🔴 WHAT THE DEFECT WAS, because it decides the SHAPE of every test here. `dcctl destroy
// local harig` used to run `kind delete cluster --name harig` for an instance that lives
// in cluster `devicechain-ha`. kind's delete is IDEMPOTENT, so deleting a cluster that
// does not exist exits 0 — the state was removed and `Instance "harig" destroyed.` was
// printed with four containers still running. The failure was a SUCCESS MESSAGE.
//
// So no test below is satisfied by "destroy returned nil". Each one asserts what the
// provider was actually ASKED about, what was left on disk, or what the operator was
// actually TOLD.

// fakeProvider records what it was asked to do. There was no Provider fake in this
// package before — the local one shells out to `kind` through exec with no seam, so the
// only way to test the decision logic is to substitute the whole provider.
type fakeProvider struct {
	name string
	// clusters that "exist"; ClusterExists consults it.
	present map[string]bool
	// asked records every cluster name ClusterExists was called with, in order. The point
	// of the test suite: assert the NAME, not just that something was asked.
	asked       []string
	existsErr   error
	ensureBind  ClusterBinding
	ensureErr   error
	existsCalls int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) EnsureCluster(context.Context, Options) (ClusterBinding, error) {
	return f.ensureBind, f.ensureErr
}

func (f *fakeProvider) ClusterExists(_ context.Context, binding ClusterBinding) (bool, error) {
	f.existsCalls++
	f.asked = append(f.asked, binding.Cluster)
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.present[binding.Cluster], nil
}

// captureOutput runs fn with stdout redirected, returning what was printed. The messages
// ARE the contract here — a destroy that does the right thing while saying the wrong one
// is the defect this package exists to fix.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func writeRecord(t *testing.T, rec InstanceRecord) {
	t.Helper()
	if err := WriteInstanceRecord(rec); err != nil {
		t.Fatalf("WriteInstanceRecord: %v", err)
	}
}

func TestInstanceRecordRoundTrips(t *testing.T) {
	fakeHome(t)
	want := InstanceRecord{
		Instance: "harig", Provider: "local", Cluster: "devicechain-ha",
		KubeContext: "kind-devicechain-ha", Managed: false,
		ClusterUID: "163e7f17-d87c-42fe-8bc0-e672e35f5ee7",
		CreatedAt:  time.Now().UTC().Truncate(time.Second), DcctlVersion: "test",
	}
	writeRecord(t, want)

	got, err := ReadInstanceRecord("harig")
	if err != nil {
		t.Fatalf("ReadInstanceRecord: %v", err)
	}
	// 🔴 NAMED ONE BY ONE, WHICH MEANS A NEW FIELD IS INVISIBLE HERE UNTIL IT IS NAMED.
	// ClusterUID was added to the record without this line and every assertion still
	// passed — a comparison that lists what it already knew about cannot notice what the
	// change made newly true.
	if got.Cluster != want.Cluster || got.KubeContext != want.KubeContext ||
		got.Managed != want.Managed || got.ClusterUID != want.ClusterUID {
		t.Fatalf("round-trip lost the binding: got %+v want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, want.CreatedAt)
	}
}

// 🔴 The permission assertion, and the MkdirAll trap with it. os.WriteFile's mode applies
// only when it CREATES the file, and MkdirAll leaves an EXISTING directory as it found it
// — so a record written into a tree an older dcctl made at 0755 would keep 0755 unless the
// write tightens it explicitly. The tree is pre-created LOOSE here so the test fails if
// that tightening is ever dropped.
func TestInstanceRecordIsPrivateEvenInAPreExistingLooseTree(t *testing.T) {
	home := fakeHome(t)
	loose := filepath.Join(home, ".devicechain", "instances", "inst")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loose, instanceRecordFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "inst", KubeContext: "kind-inst", Managed: true})

	for path, want := range map[string]os.FileMode{
		filepath.Join(home, ".devicechain"):      0o700,
		loose:                                    0o700,
		filepath.Join(loose, instanceRecordFile): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
}

// 🔴 The escrow-collision control. `destroy` spares every file whose name looks like
// escrow, so a record matching that pattern would OUTLIVE its instance — and a same-name
// rebuild would inherit a binding pointing at a cluster that is gone, or at a different
// cluster somebody has since created under that name, which destroy would then delete on
// its owner's behalf. broker_record.go documents the same trap for credentials.
func TestInstanceRecordIsNotSparedAsEscrow(t *testing.T) {
	if looksLikeEscrow(instanceRecordFile) {
		t.Fatalf("%q matches looksLikeEscrow, so destroy would spare it and a rebuild would inherit a dead binding", instanceRecordFile)
	}
	// And prove the guard it must not match is actually live, rather than a function that
	// returns false for everything.
	if !looksLikeEscrow("devicechain-rootkey.escrow") {
		t.Fatal("looksLikeEscrow no longer recognises a real escrow name — this control proves nothing")
	}
}

func TestReadInstanceRecordReportsAMissingOneAsAState(t *testing.T) {
	fakeHome(t)
	_, err := ReadInstanceRecord("never-bootstrapped")
	if !errors.Is(err, ErrNoInstanceRecord) {
		t.Fatalf("got %v, want ErrNoInstanceRecord — a missing record is a state, not a failure", err)
	}
}

// A record found in the wrong directory means a tree was copied. Acting on it would point
// destroy at another instance's cluster.
func TestReadInstanceRecordRefusesAMismatchedName(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "one", Provider: "local", Cluster: "one", KubeContext: "kind-one", Managed: true})
	src := filepath.Join(home, ".devicechain", "instances", "one", instanceRecordFile)
	dstDir := filepath.Join(home, ".devicechain", "instances", "two")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(src)
	if err := os.WriteFile(filepath.Join(dstDir, instanceRecordFile), b, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ReadInstanceRecord("two")
	if err == nil {
		t.Fatal("a record naming another instance was accepted")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error should refuse plainly, got: %v", err)
	}
}

// TestListInstancesSeesOnlyWhatIsUnderInstancesAndSortsByName replaces a test that
// had to enumerate a list of names to skip. Nesting removed the list: siblings are
// peers of instances/, not of its contents, so nothing inside it needs excluding.
//
// The siblings are still CREATED here. Not because ListInstances has to skip them —
// it never sees them — but because a reader who deletes them would not notice this
// test still passing, and they are the reason the directory exists.
func TestListInstancesSeesOnlyWhatIsUnderInstancesAndSortsByName(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "zeta", Provider: "local", Cluster: "zeta", KubeContext: "kind-zeta", Managed: true})
	writeRecord(t, InstanceRecord{Instance: "alpha", Provider: "local", Cluster: "cluster-a", KubeContext: "kind-cluster-a", Managed: false})
	// An instance directory with no record — the pre-record state D4 must keep visible.
	if err := os.MkdirAll(filepath.Join(home, ".devicechain", "instances", "legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	siblings := dcdir.MemberNames()
	if len(siblings) == 0 {
		t.Fatal("the inventory is empty, so this test asserts nothing")
	}
	for _, name := range siblings {
		if err := os.MkdirAll(filepath.Join(home, ".devicechain", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ListInstances()
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	var names []string
	for _, k := range got {
		names = append(names, k.Instance)
	}
	want := []string{"alpha", "legacy", "zeta"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v (siblings %v live beside instances/, never in it; order must be stable)",
			names, want, siblings)
	}
	for _, k := range got {
		switch k.Instance {
		case "legacy":
			if k.HasRecord {
				t.Error("legacy has no record and must be reported as such")
			}
		default:
			if !k.HasRecord {
				t.Errorf("%s lost its record", k.Instance)
			}
		}
	}
}

func TestListInstancesOnAnEmptyMachineIsEmptyNotAnError(t *testing.T) {
	fakeHome(t)
	got, err := ListInstances()
	if err != nil {
		t.Fatalf("ListInstances on a machine with no ~/.devicechain: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d instances, want 0", len(got))
	}
}

func TestResolveBindingPrefersFlagThenRecordThenGuess(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha", KubeContext: "kind-devicechain-ha", Managed: false})

	// 1. The flag wins over the record: the operator is standing in front of us.
	b, src := ResolveBinding(Options{Instance: "harig", KubeContext: "kind-somewhere-else"})
	if src != BindingFromFlag || b.KubeContext != "kind-somewhere-else" {
		t.Fatalf("flag did not win: %+v %v", b, src)
	}
	if b.Managed {
		t.Error("a context named by hand is not dcctl's cluster and must not be Managed")
	}

	// 2. The record wins over the guess, and carries the DIFFERENT cluster name — this is
	// the whole defect in one assertion.
	b, src = ResolveBinding(Options{Instance: "harig"})
	if src != BindingFromRecord {
		t.Fatalf("source: got %v want record", src)
	}
	if b.Cluster != "devicechain-ha" {
		t.Fatalf("cluster: got %q want %q — the binding was re-derived from the instance name", b.Cluster, "devicechain-ha")
	}

	// 3. No record: the guess, reported as a guess.
	b, src = ResolveBinding(Options{Instance: "legacy"})
	if src != BindingGuessed {
		t.Fatalf("source: got %v want guess", src)
	}
	if b.Cluster != "legacy" || !b.Managed {
		t.Fatalf("guess should reproduce the old convention, got %+v", b)
	}
}

// 🔴 THE REGRESSION CONTROL. This is the exact input that used to print
// `Instance "harig" destroyed.` while the cluster kept running. Only devicechain-ha
// exists, so a destroy that asked about the instance NAME would be told "gone" and clear
// the state of a live instance.
func TestDestroyUsesTheRecordedClusterNotTheInstanceName(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha", KubeContext: "kind-devicechain-ha", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{"devicechain-ha": true}}

	// The uninstall reaches a real cluster and fails here; what is under test is which
	// cluster the decision was made about.
	out := captureOutput(t, func() {
		_ = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig", AssumeYes: true}})
	})

	if strings.Join(p.asked, ",") != "devicechain-ha" {
		t.Fatalf("asked about clusters %v, want [devicechain-ha] — the instance name was used instead of the record", p.asked)
	}
	if strings.Contains(out, "already gone") {
		t.Fatalf("a present cluster was reported gone:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "instances", "harig", instanceRecordFile)); err != nil {
		t.Fatalf("the state of an instance whose cluster is running was cleared: %v", err)
	}
	if !strings.Contains(out, "devicechain-ha") {
		t.Error("the output never names the cluster it acted on")
	}
}

// The other half of the same defect: a cluster that really is gone must be REPORTED as
// gone, not silently counted as a successful teardown.
func TestDestroySaysSoWhenTheClusterIsAlreadyGone(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "upgrig", Provider: "local", Cluster: "devicechain-upgrade", KubeContext: "kind-devicechain-upgrade", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{}}

	out := captureOutput(t, func() {
		if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "upgrig", AssumeYes: true}}); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})

	if !strings.Contains(out, "already gone") {
		t.Fatalf("output must say the cluster was already gone, got:\n%s", out)
	}
	if !strings.Contains(out, "devicechain-upgrade") {
		t.Error("the output never names the cluster it expected to find")
	}
}

// 🔴 DESTROY NEVER DELETES A CLUSTER, MANAGED OR ADOPTED. There is no deletion call left to
// count, so the property is asserted through everything a deletion would have to pass:
// a present cluster is never reported gone, never has its prerequisite state cleared, and
// the instance's own state survives the (here, failing) uninstall that is the only thing
// destroy does to a running cluster. A version that treated Managed as licence to take
// the "gone" path fails every one of these for the managed row.
func TestDestroyNeverTreatsARunningClusterAsItsToRemove(t *testing.T) {
	for _, managed := range []bool{true, false} {
		t.Run(fmt.Sprintf("managed=%v", managed), func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha",
				KubeContext: "kind-devicechain-ha", Managed: managed, ClusterUID: destroyedClusterUID})
			clusterState := plantClusterState(t, home, destroyedClusterUID)
			p := &fakeProvider{name: "local", present: map[string]bool{"devicechain-ha": true}}
			// Past the empty-state refusal, which asks the cluster first; the uninstall is
			// what this test is about.
			stubLiveInstanceInfrastructure(t, nil)

			// The uninstall itself reaches a real cluster and fails here, which is correct
			// and is asserted below: the instance is still deployed, so a failed uninstall
			// must NOT be swallowed and the local state must survive to describe it.
			var err error
			out := captureOutput(t, func() {
				err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig", AssumeYes: true}})
			})

			if err == nil {
				t.Fatal("an uninstall against an unreachable cluster must be reported, not swallowed")
			}
			if p.existsCalls != 1 {
				t.Errorf("ClusterExists called %d times, want 1", p.existsCalls)
			}
			if strings.Contains(out, "already gone") || strings.Contains(out, "not there any more") {
				t.Errorf("a running cluster was reported gone:\n%s", out)
			}
			if !strings.Contains(out, "uninstalling instance release") {
				t.Errorf("destroy did not go on to uninstall the instance from the running cluster:\n%s", out)
			}
			if _, statErr := os.Stat(filepath.Join(clusterState, "infra", "cluster", "terraform.tfstate")); statErr != nil {
				t.Errorf("the prerequisite state of a running cluster was removed: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(home, ".devicechain", "instances", "harig", instanceRecordFile)); statErr != nil {
				t.Errorf("the instance's state was removed although its uninstall failed: %v", statErr)
			}
		})
	}
}

// 🔴 AND NOTHING THAT COULD DELETE A CLUSTER IS REACHABLE FROM HERE. Destroy is handed a
// Provider, so the Provider's method set is the whole of what it can ask a cluster to do.
func TestTheProviderCannotBeAskedToDeleteACluster(t *testing.T) {
	typ := reflect.TypeOf((*Provider)(nil)).Elem()
	sawExists := false
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		sawExists = sawExists || name == "ClusterExists"
		if strings.Contains(name, "Destroy") || strings.Contains(name, "Delete") {
			t.Errorf("Provider has %s; destroy removes an instance and must have no way to remove its cluster", name)
		}
	}
	// The reach control: a reflection that saw no methods would pass the loop above.
	if !sawExists {
		t.Fatal("reflection did not find ClusterExists on Provider, so this test inspected nothing")
	}
}

// 🔴 D4: no record must degrade LOUDLY. Asserting the exit code alone would pass on the
// old, silent behaviour — the sentence is the fix.
func TestDestroyWithoutARecordSaysItIsGuessing(t *testing.T) {
	fakeHome(t)
	p := &fakeProvider{name: "local", present: map[string]bool{}}

	out := captureOutput(t, func() {
		if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "legacy", AssumeYes: true}}); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})

	if !strings.Contains(out, "GUESSING") {
		t.Fatalf("output must say it is guessing, got:\n%s", out)
	}
	if strings.Join(p.asked, ",") != "legacy" {
		t.Fatalf("the guess should still act, on the conventional name: asked %v", p.asked)
	}
}

// Whatever kind of binding it is, a destroy that finishes must take the instance's local
// state — otherwise the orphaned directories this change exists to surface simply keep
// accumulating. Run with the cluster ABSENT, because that is the state that actually
// produces orphans: a rig deletes its own cluster on the way out and leaves the
// instance's state behind. A present cluster is covered by
// TestDestroyNeverTreatsARunningClusterAsItsToRemove, where the uninstall is a real
// cluster operation this test cannot perform.
func TestDestroyClearsLocalStateOnBothPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
	}{{"managed, cluster already gone", true}, {"adopted, cluster already gone", false}} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: tc.managed})
			p := &fakeProvider{name: "local", present: map[string]bool{"c": false}}

			captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			rec := filepath.Join(home, ".devicechain", "instances", "inst", instanceRecordFile)
			if _, err := os.Stat(rec); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the record survived a destroy (%v) — a rebuild would inherit it", err)
			}
		})
	}
}

// 🔴 The closing line is part of the contract, not decoration. "Instance destroyed." over
// a cluster nobody touched is the exact sentence that made the original defect invisible,
// so each outcome must end with a DIFFERENT and accurate one.
func TestDestroyClosingMessageMatchesWhatActuallyHappened(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
	}{{"managed but already gone", true}, {"adopted and already gone", false}} {
		t.Run(tc.name, func(t *testing.T) {
			fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: tc.managed})
			p := &fakeProvider{name: "local", present: map[string]bool{}}

			out := captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			if !strings.Contains(out, "was already gone") {
				t.Errorf("output should say the cluster was already gone, got:\n%s", out)
			}
			if strings.Contains(out, "destroyed") {
				t.Errorf("output must NOT claim a destroy when the cluster was already gone, got:\n%s", out)
			}
		})
	}

	// The uninstalling path's line cannot be reached without a cluster, so it is asserted
	// directly: both variants say the cluster was left running, and the one that left a
	// database behind never says the instance was destroyed.
	full := destroyedLine("inst", "c", "", false)
	if !strings.Contains(full, `Instance "inst" destroyed`) || !strings.Contains(full, "cluster c left running") {
		t.Errorf("a complete destroy should say so and name the cluster left running, got %q", full)
	}
	partial := destroyedLine("inst", "c", "the store could not be reached", false)
	if strings.Contains(partial, "destroyed") {
		t.Errorf("a destroy that left the database behind claims the instance was destroyed: %q", partial)
	}
	if !strings.Contains(partial, "LEFT on the shared relational store: the store could not be reached") ||
		!strings.Contains(partial, "cluster c left running") {
		t.Errorf("a destroy that left the database behind must say so, and why, got %q", partial)
	}
}

// ---------------------------------------------------------------------------
// Regression tests for the review findings. Each of these was a real defect in the first
// cut of this change, and every one of them was a variant of the SAME failure the change
// exists to fix: a command that did something other than what it said.
// ---------------------------------------------------------------------------

// 🔴 --dry-run must destroy nothing. The first cut put the adopted branch above the
// dry-run guard, so `--dry-run` on any instance bootstrapped with --kube-context deleted
// ~/.devicechain/instances/<instance> — tfstate and all — under a flag that promises the opposite.
func TestDryRunDestroysNothingOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		present bool
	}{
		{"managed", true, true},
		{"managed, cluster gone", true, false},
		{"adopted", false, true},
		{"adopted, cluster gone", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c",
				Managed: tc.managed, ClusterUID: destroyedClusterUID})
			clusterState := plantClusterState(t, home, destroyedClusterUID)
			p := &fakeProvider{name: "local", present: map[string]bool{"c": tc.present}}

			out := captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{
					Options: Options{Instance: "inst", DryRun: true, AssumeYes: true},
				}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			if _, err := os.Stat(filepath.Join(home, ".devicechain", "instances", "inst", instanceRecordFile)); err != nil {
				t.Fatalf("--dry-run removed the instance's local state: %v", err)
			}
			if _, err := os.Stat(filepath.Join(clusterState, "cluster.json")); err != nil {
				t.Fatalf("--dry-run removed the cluster's local state: %v", err)
			}
			if !strings.Contains(out, "LEAVING cluster c running") {
				t.Errorf("--dry-run should say the cluster is left running, got:\n%s", out)
			}
		})
	}
}

// 🔴 A failure to ASK is not an answer of "no". ClusterExists returning an error meant
// Docker was unreachable; treating that as "the cluster is gone" deletes the state of
// every live instance and prints "destroyed" over all of them.
func TestDestroyStopsWhenItCannotTellWhetherTheClusterExists(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	p := &fakeProvider{name: "local", existsErr: errors.New("docker daemon unreachable")}

	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}})
	})

	if err == nil {
		t.Fatal("a failed existence check was treated as an answer")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".devicechain", "instances", "inst", instanceRecordFile)); statErr != nil {
		t.Fatalf("local state was removed despite not knowing whether the cluster exists: %v", statErr)
	}
	if strings.Contains(out, "uninstalling instance release") {
		t.Errorf("went on to uninstall without knowing whether the cluster was there:\n%s", out)
	}
}

// 🔴 An unreadable record must REFUSE, not fall back to the guess. The guess names
// kind-<instance>, so a corrupt record on `harig` would have acted on whatever unrelated
// cluster carries that name — once, `kind delete cluster --name harig` — while
// devicechain-ha survived.
func TestAnUnreadableRecordRefusesRatherThanGuessing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"truncated", `{"instance":"harig","cluster":"devicech`},
		{"names another instance", `{"instance":"somebody-else","provider":"local","cluster":"x","kubeContext":"kind-x","managed":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			dir := filepath.Join(home, ".devicechain", "instances", "harig")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, instanceRecordFile), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}

			binding, source := ResolveBinding(Options{Instance: "harig"})
			if source != BindingUnreadable {
				t.Fatalf("source %q — an unreadable record was silently downgraded to a guess (binding %+v)", source, binding)
			}

			p := &fakeProvider{name: "local", present: map[string]bool{"harig": true}}
			var err error
			captureOutput(t, func() {
				err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig", AssumeYes: true}})
			})
			if err == nil {
				t.Fatal("destroy proceeded with a record it could not read")
			}
			if p.existsCalls != 0 {
				t.Fatalf("acted on a cluster on the strength of an unreadable record: asked %v", p.asked)
			}
			if _, statErr := os.Stat(filepath.Join(dir, instanceRecordFile)); statErr != nil {
				t.Errorf("removed local state despite refusing: %v", statErr)
			}
		})
	}
}

// 🔴 Declining the prompt must leave everything alone. The uninstall once returned nil
// for both "aborted" and "done", so an operator answering `n` still had the instance's
// tfstate deleted, under a closing line that said it had been uninstalled.
func TestDecliningTheConfirmationChangesNothing(t *testing.T) {
	for _, managed := range []bool{true, false} {
		t.Run(fmt.Sprintf("managed=%v", managed), func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha", KubeContext: "kind-devicechain-ha", Managed: managed})
			p := &fakeProvider{name: "local", present: map[string]bool{"devicechain-ha": true}}
			stubLiveInstanceInfrastructure(t, nil)

			// confirm() reads stdin; an empty stdin is a decline, which is the default anyway.
			stdin, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			orig := os.Stdin
			os.Stdin = stdin
			defer func() { os.Stdin = orig }()

			var destroyErr error
			out := captureOutput(t, func() {
				destroyErr = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig"}})
			})

			if destroyErr != nil {
				t.Errorf("declining is not a command failure: %v", destroyErr)
			}
			if _, statErr := os.Stat(filepath.Join(home, ".devicechain", "instances", "harig", instanceRecordFile)); statErr != nil {
				t.Fatalf("declining still removed the instance's local state: %v", statErr)
			}
			if !strings.Contains(out, "Aborted.") {
				t.Errorf("declining was never acknowledged:\n%s", out)
			}
			if !strings.Contains(out, "ALL ITS DATA") || !strings.Contains(out, "shared prerequisites stay") {
				t.Errorf("the prompt must say the data goes and the cluster stays:\n%s", out)
			}
			for _, claim := range []string{"uninstalled;", "destroyed;", "uninstalling instance release"} {
				if strings.Contains(out, claim) {
					t.Errorf("output contains %q after the operator declined:\n%s", claim, out)
				}
			}
		})
	}
}

// 🔴 An unnamed adopted binding may not be declared "gone" from kubeconfig alone. A
// different KUBECONFIG looks exactly like a deleted cluster, and clearing state on that
// reading throws away the tfstate of a live instance.
func TestAnUnnamedAdoptedBindingIsNeverDeclaredGone(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "", KubeContext: "prod-eu-west", Managed: false})
	// present is empty, so ClusterExists would answer "no" if it were consulted.
	p := &fakeProvider{name: "local", present: map[string]bool{}}

	captureOutput(t, func() {
		_ = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}})
	})

	if p.existsCalls != 0 {
		t.Errorf("consulted ClusterExists for a binding with no cluster name (%d calls)", p.existsCalls)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "instances", "inst", instanceRecordFile)); err != nil {
		t.Fatalf("cleared the state of an instance whose cluster it could not name: %v", err)
	}
}

// 🔴 THE SECURITY PROPERTY, ASSERTED. cmd/instances.go claims the listing opens only
// instance.json and the destroy marker — their neighbour terraform.tfstate holds the
// database superuser password and the broker's TLS private key in cleartext, and this
// output is what gets pasted into an issue. The claim was cited against a test that did
// not exist; this is that test.
//
// 🔑 THE MARKER IS READ FROM IN HERE FOR THIS TEST'S SAKE. Reading it in the command
// layer instead would have been a second, unwatched open of a file in that directory —
// and this test, which drives ListInstances, would have gone on passing while the surface
// it pins grew. The marker is opened by STAT, so it is not even read; keeping it inside
// this function is what keeps every local open of an instance directory in one place with
// one test over it.
func TestListInstancesReadsTheRecordAndTheDestroyMarkerAndNothingElse(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})

	// A neighbour holding something that must never be read, made UNREADABLE so that any
	// attempt to open it fails loudly rather than succeeding quietly.
	secret := filepath.Join(home, ".devicechain", "instances", "inst", "terraform.tfstate")
	if err := os.WriteFile(secret, []byte(`{"password":"hunter2"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })

	// 🔴 The reach control. If the neighbour turns out to be READABLE — running as root,
	// an exotic filesystem — then "ListInstances succeeded" says nothing at all about
	// whether it opened the file, and this test would pass while the property rotted.
	if _, err := os.ReadFile(secret); err == nil {
		t.Fatal("the planted neighbour is readable, so this test cannot detect the listing opening it")
	}

	got, err := ListInstances()
	if err != nil {
		t.Fatalf("ListInstances opened something it should not have: %v", err)
	}
	if len(got) != 1 || !got[0].HasRecord || got[0].Record.Cluster != "c" {
		t.Fatalf("listing did not read the record it was supposed to: %+v", got)
	}
	if got[0].Destroying || got[0].DestroyingErr != nil {
		t.Fatalf("an instance with no destroy marker was reported as part-way destroyed: %+v", got[0])
	}
}

// 🔴 THE MARKER IS READ HERE, AND ITS FAILURE IS NOT FOLDED INTO ITS ABSENCE. The listing
// is the one place an operator finds out that a teardown started and stopped; a row that
// could not answer must say so rather than fall through to the healthy cell.
func TestListInstancesReportsTheDestroyMarkerAndWhenItCouldNotBeChecked(t *testing.T) {
	t.Run("a marked instance", func(t *testing.T) {
		fakeHome(t)
		writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
		if err := writeDestroyMarker("inst"); err != nil {
			t.Fatal(err)
		}
		got, err := ListInstances()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !got[0].Destroying {
			t.Fatalf("the listing did not notice the destroy marker: %+v", got)
		}
		if got[0].DestroyingErr != nil {
			t.Errorf("a marker that was found was also reported as unreadable: %v", got[0].DestroyingErr)
		}
	})

	t.Run("a marker that could not be checked", func(t *testing.T) {
		home := fakeHome(t)
		writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
		unstattableMarker(t, home, "inst")

		got, err := ListInstances()
		if err != nil {
			t.Fatalf("one unreadable marker failed the whole listing: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("listing: %+v", got)
		}
		if got[0].DestroyingErr == nil {
			t.Fatal("a marker that could not be statted was reported as a definite answer, " +
				"which prints as a healthy row over an instance nobody can say anything about")
		}
		if got[0].Destroying {
			t.Error("could-not-tell was reported as present")
		}
	})
}

// The atomic-write property, asserted by its observable consequence: no partial file is
// ever visible under the record's own name.
func TestWriteInstanceRecordLeavesNoPartialFileBehind(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c2", KubeContext: "kind-c2", Managed: true})

	entries, err := os.ReadDir(filepath.Join(home, ".devicechain", "instances", "inst"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != instanceRecordFile {
			t.Errorf("left a stray file behind: %s", e.Name())
		}
	}
	rec, err := ReadInstanceRecord("inst")
	if err != nil {
		t.Fatalf("rewrite corrupted the record: %v", err)
	}
	if rec.Cluster != "c2" {
		t.Errorf("rewrite did not take effect: %+v", rec)
	}
}

// TestAnInstanceMayBeNamedAfterASibling is the inverse of the test it replaces, and
// the inversion is the point of nesting.
//
// While instances sat directly under ~/.devicechain, a name matching a sibling had
// to be REFUSED — in two hand-written lists that had to agree and did not, which is
// how the simulator record directory came to be enumerated as an instance. An
// instance now lives under instances/, so the two namespaces never meet and there is
// nothing left to refuse. Keeping the refusal would be a guard whose reason is gone.
func TestAnInstanceMayBeNamedAfterASibling(t *testing.T) {
	fakeHome(t)

	names := dcdir.MemberNames()
	if len(names) == 0 {
		t.Fatal("the inventory is empty, so this test asserts nothing")
	}
	for _, name := range names {
		if err := ValidateInstanceName(name); err != nil {
			t.Errorf("ValidateInstanceName(%q) still refuses a sibling name: %v", name, err)
		}
		root, err := instanceRoot(name)
		if err != nil {
			t.Fatalf("instanceRoot(%q): %v", name, err)
		}
		sibling, err := dcdir.Sibling(name)
		if err != nil {
			t.Fatalf("Sibling(%q): %v", name, err)
		}
		if root == sibling {
			t.Errorf("instance %q and the sibling of the same name are both %q", name, root)
		}
	}
}
