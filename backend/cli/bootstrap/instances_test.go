// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
// provider was actually ASKED to do, or what the operator was actually TOLD.

// fakeProvider records what it was asked to do. There was no Provider fake in this
// package before — the local one shells out to `kind` through exec with no seam, so the
// only way to test the decision logic is to substitute the whole provider.
type fakeProvider struct {
	name string
	// clusters that "exist"; ClusterExists consults it.
	present map[string]bool
	// deleted records every binding DestroyCluster was called with, in order. The point
	// of the test suite: assert the NAME, not just that something was deleted.
	deleted     []ClusterBinding
	existsErr   error
	destroyErr  error
	ensureBind  ClusterBinding
	ensureErr   error
	existsCalls int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) EnsureCluster(context.Context, Options) (ClusterBinding, error) {
	return f.ensureBind, f.ensureErr
}

func (f *fakeProvider) DestroyCluster(_ context.Context, binding ClusterBinding, _ Options) error {
	f.deleted = append(f.deleted, binding)
	return f.destroyErr
}

func (f *fakeProvider) ClusterExists(_ context.Context, binding ClusterBinding) (bool, error) {
	f.existsCalls++
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
		CreatedAt: time.Now().UTC().Truncate(time.Second), DcctlVersion: "test",
	}
	writeRecord(t, want)

	got, err := ReadInstanceRecord("harig")
	if err != nil {
		t.Fatalf("ReadInstanceRecord: %v", err)
	}
	if got.Cluster != want.Cluster || got.KubeContext != want.KubeContext || got.Managed != want.Managed {
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
	loose := filepath.Join(home, ".devicechain", "inst")
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
	src := filepath.Join(home, ".devicechain", "one", instanceRecordFile)
	dstDir := filepath.Join(home, ".devicechain", "two")
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

func TestListInstancesSkipsEscrowAndSortsByName(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "zeta", Provider: "local", Cluster: "zeta", KubeContext: "kind-zeta", Managed: true})
	writeRecord(t, InstanceRecord{Instance: "alpha", Provider: "local", Cluster: "cluster-a", KubeContext: "kind-cluster-a", Managed: false})
	// An instance directory with no record — the pre-record state D4 must keep visible.
	if err := os.MkdirAll(filepath.Join(home, ".devicechain", "legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The escrow directory is a SIBLING of the instances, not one of them.
	if err := os.MkdirAll(filepath.Join(home, ".devicechain", escrowDirName), 0o700); err != nil {
		t.Fatal(err)
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
		t.Fatalf("got %v, want %v (escrow must be skipped; order must be stable)", names, want)
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
// `Instance "harig" destroyed.` while the cluster kept running.
func TestDestroyUsesTheRecordedClusterNotTheInstanceName(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha", KubeContext: "kind-devicechain-ha", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{"devicechain-ha": true}}

	out := captureOutput(t, func() {
		if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig", AssumeYes: true}}); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})

	if len(p.deleted) != 1 {
		t.Fatalf("DestroyCluster called %d times, want 1", len(p.deleted))
	}
	if got := p.deleted[0].Cluster; got != "devicechain-ha" {
		t.Fatalf("deleted cluster %q — the instance name was used instead of the record", got)
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

	if len(p.deleted) != 0 {
		t.Errorf("deleted a cluster that does not exist: %+v", p.deleted)
	}
	if !strings.Contains(out, "already gone") {
		t.Fatalf("output must say the cluster was already gone, got:\n%s", out)
	}
	if !strings.Contains(out, "devicechain-upgrade") {
		t.Error("the output never names the cluster it expected to find")
	}
}

// 🔴 The adopted control, both halves. A destroy that silently spares the cluster is the
// same defect wearing the opposite sign, so it is not enough that the cluster survives —
// the operator has to be TOLD it survived, and told its name.
func TestDestroyLeavesAnAdoptedClusterRunningAndSaysSo(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha", KubeContext: "kind-devicechain-ha", Managed: false})
	p := &fakeProvider{name: "local", present: map[string]bool{"devicechain-ha": true}}

	// The uninstall itself reaches a real cluster and fails here, which is correct and is
	// asserted below: when an adopted cluster is genuinely reachable the instance is still
	// deployed in it, so a failed uninstall must NOT be swallowed and the local state must
	// survive to describe it. What is under test is the decision, not the helm call.
	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig", AssumeYes: true}})
	})

	if len(p.deleted) != 0 {
		t.Fatalf("deleted an adopted cluster: %+v", p.deleted)
	}
	if !strings.Contains(out, "LEFT RUNNING") || !strings.Contains(out, "devicechain-ha") {
		t.Fatalf("output must say the named cluster was left running, got:\n%s", out)
	}
	if err == nil {
		t.Fatal("an uninstall against an unreachable cluster must be reported, not swallowed")
	}
}

// 🔴 D4: no record must degrade LOUDLY. Asserting the exit code alone would pass on the
// old, silent behaviour — the sentence is the fix.
func TestDestroyWithoutARecordSaysItIsGuessing(t *testing.T) {
	fakeHome(t)
	p := &fakeProvider{name: "local", present: map[string]bool{"legacy": true}}

	out := captureOutput(t, func() {
		if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "legacy", AssumeYes: true}}); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})

	if !strings.Contains(out, "GUESSING") {
		t.Fatalf("output must say it is guessing, got:\n%s", out)
	}
	if len(p.deleted) != 1 || p.deleted[0].Cluster != "legacy" {
		t.Fatalf("the guess should still act, on the conventional name: %+v", p.deleted)
	}
}

// Whatever path a destroy takes, the instance's local state must go — otherwise the
// orphaned directories this change exists to surface simply keep accumulating.
func TestDestroyClearsLocalStateOnBothPaths(t *testing.T) {
	// The adopted case is deliberately run with the cluster ABSENT, because that is the
	// state that actually produces orphans: a rig deletes its own cluster on the way out
	// and leaves the instance's state behind. An adopted cluster that is still PRESENT is
	// covered by TestDestroyLeavesAnAdoptedClusterRunningAndSaysSo, where the uninstall
	// is a real cluster operation this test cannot perform.
	for _, tc := range []struct {
		name    string
		managed bool
		present bool
	}{{"managed", true, true}, {"adopted, cluster already gone", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: tc.managed})
			p := &fakeProvider{name: "local", present: map[string]bool{"c": tc.present}}

			captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			rec := filepath.Join(home, ".devicechain", "inst", instanceRecordFile)
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
		present bool
		want    string
		notWant string
	}{
		{"managed and present", true, true, "destroyed", ""},
		// 🔴 notWant is populated HERE, and its absence was a real hole: this is the one
		// row that can reproduce the original defect's sentence, and it was the one row
		// asserting nothing about it.
		{"managed but already gone", true, false, "already gone", "destroyed."},
		{"adopted and already gone", false, false, "was already gone", "destroyed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: tc.managed})
			p := &fakeProvider{name: "local", present: map[string]bool{"c": tc.present}}

			out := captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			if !strings.Contains(out, tc.want) {
				t.Errorf("output should contain %q, got:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(out, tc.notWant) {
				t.Errorf("output must NOT claim %q when the cluster was left alone, got:\n%s", tc.notWant, out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Regression tests for the review findings. Each of these was a real defect in the first
// cut of this change, and every one of them was a variant of the SAME failure the change
// exists to fix: a command that did something other than what it said.
// ---------------------------------------------------------------------------

// 🔴 --dry-run must destroy nothing. The first cut put the adopted branch above the
// dry-run guard, so `--dry-run` on any instance bootstrapped with --kube-context deleted
// ~/.devicechain/<instance> — tfstate and all — under a flag that promises the opposite.
func TestDryRunDestroysNothingOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		present bool
	}{
		{"managed", true, true},
		{"adopted", false, true},
		{"adopted, cluster gone", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: tc.managed})
			p := &fakeProvider{name: "local", present: map[string]bool{"c": tc.present}}

			captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{
					Options: Options{Instance: "inst", DryRun: true, AssumeYes: true},
				}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			if len(p.deleted) != 0 {
				t.Errorf("--dry-run deleted a cluster: %+v", p.deleted)
			}
			if _, err := os.Stat(filepath.Join(home, ".devicechain", "inst", instanceRecordFile)); err != nil {
				t.Fatalf("--dry-run removed the instance's local state: %v", err)
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
	captureOutput(t, func() {
		err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}})
	})

	if err == nil {
		t.Fatal("a failed existence check was treated as an answer")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".devicechain", "inst", instanceRecordFile)); statErr != nil {
		t.Fatalf("local state was removed despite not knowing whether the cluster exists: %v", statErr)
	}
	if len(p.deleted) != 0 {
		t.Errorf("deleted a cluster without knowing it was there: %+v", p.deleted)
	}
}

// 🔴 An unreadable record must REFUSE, not fall back to the guess. The guess is
// Managed:true and names kind-<instance>, so a corrupt record on `harig` would have run
// `kind delete cluster --name harig` against whatever unrelated cluster carries that name
// while devicechain-ha survived.
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
			dir := filepath.Join(home, ".devicechain", "harig")
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
			if len(p.deleted) != 0 {
				t.Fatalf("deleted a cluster on the strength of an unreadable record: %+v", p.deleted)
			}
			if _, statErr := os.Stat(filepath.Join(dir, instanceRecordFile)); statErr != nil {
				t.Errorf("removed local state despite refusing: %v", statErr)
			}
		})
	}
}

// 🔴 Declining the prompt must leave everything alone. destroyInstanceOnly returned nil
// for both "aborted" and "done", so an operator answering `n` still had the instance's
// tfstate deleted, under a closing line that said it had been uninstalled.
func TestDecliningTheConfirmationChangesNothing(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "harig", Provider: "local", Cluster: "devicechain-ha", KubeContext: "kind-devicechain-ha", Managed: false})
	p := &fakeProvider{name: "local", present: map[string]bool{"devicechain-ha": true}}

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
	if _, statErr := os.Stat(filepath.Join(home, ".devicechain", "harig", instanceRecordFile)); statErr != nil {
		t.Fatalf("declining still removed the instance's local state: %v", statErr)
	}
	if strings.Contains(out, "uninstalled;") {
		t.Errorf("output claims the instance was uninstalled after the operator declined:\n%s", out)
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
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "inst", instanceRecordFile)); err != nil {
		t.Fatalf("cleared the state of an instance whose cluster it could not name: %v", err)
	}
}

// 🔴 THE SECURITY PROPERTY, ASSERTED. cmd/instances.go claims the listing opens only
// instance.json — its neighbour terraform.tfstate holds the database superuser password
// and the broker's TLS private key in cleartext, and this output is what gets pasted into
// an issue. The claim was cited against a test that did not exist; this is that test.
func TestListInstancesReadsOnlyTheRecord(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})

	// A neighbour holding something that must never be read, made UNREADABLE so that any
	// attempt to open it fails loudly rather than succeeding quietly.
	secret := filepath.Join(home, ".devicechain", "inst", "terraform.tfstate")
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
}

// The atomic-write property, asserted by its observable consequence: no partial file is
// ever visible under the record's own name.
func TestWriteInstanceRecordLeavesNoPartialFileBehind(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c2", KubeContext: "kind-c2", Managed: true})

	entries, err := os.ReadDir(filepath.Join(home, ".devicechain", "inst"))
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
