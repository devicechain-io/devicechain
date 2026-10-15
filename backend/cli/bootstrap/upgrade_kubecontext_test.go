// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/types"
)

// guideDeclaration is the declaration of an instance bootstrapped on dcctl's own kind
// cluster with the default filesystem object store: Managed, a ReadWriteOnce claim, and
// therefore a store the chart only accepts when it is colocated.
func guideDeclaration() *dcv1beta1.Instance {
	inst := &dcv1beta1.Instance{}
	inst.Name = "guide"
	inst.Spec = dcv1beta1.InstanceSpec{
		Provider: "local", Cluster: "devicechain", Managed: true,
		Profile: "default", Monitoring: true, CNPG: true, TLS: true,
		Host:          DefaultIngressHost,
		ImageRegistry: "ghcr.io/devicechain-io", ImageVersion: "v0.21.1",
		ObjectStore: &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreFilesystem, Size: "1Gi"},
	}
	inst.SetUID(types.UID(testUID))
	return inst
}

// upgradeValuesFor is what `dcctl upgrade local guide [--kube-context ctx]` hands the
// chart: the binding the command resolves, then the declaration applied over it, then the
// values rendered from the result. It stops short of the cluster reads, which cannot
// change what is under test.
func upgradeValuesFor(t *testing.T, flag string) (map[string]interface{}, ClusterBinding, BindingSource) {
	t.Helper()
	opts := UpgradeOptions{Options: Options{Instance: "guide", KubeContext: flag}}
	binding, source := ResolveBinding(opts.Options)
	st := &State{
		Instance: "guide", KubeContext: binding.KubeContext, Binding: binding,
		Provider: "local", Values: map[string]string{},
	}
	if err := applyUpgradeDeclaration(st, guideDeclaration(), opts); err != nil {
		t.Fatalf("applyUpgradeDeclaration: %v", err)
	}
	return helmValues(st), binding, source
}

func colocateOf(t *testing.T, vals map[string]interface{}) interface{} {
	t.Helper()
	bs, _ := vals["blobStorage"].(map[string]interface{})
	p, _ := bs["persistence"].(map[string]interface{})
	return p["colocate"]
}

// 🔴 THE REPORTED BUG, BY VALUE. `dcctl upgrade local guide --kube-context kind-devicechain`
// dropped blobStorage.persistence.colocate and the chart refused two pods on a
// ReadWriteOnce claim; the same command without the flag rendered it.
func TestUpgradeRendersTheSameInstanceWithAndWithoutKubeContext(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "guide", Provider: "local", Cluster: "devicechain",
		KubeContext: "kind-devicechain", Managed: true})

	bare, _, _ := upgradeValuesFor(t, "")
	if colocateOf(t, bare) != true {
		t.Fatalf("control: the flagless upgrade must colocate the RWO claim, got %v", colocateOf(t, bare))
	}
	flagged, binding, source := upgradeValuesFor(t, "kind-devicechain")
	if !binding.Managed {
		t.Errorf("a flag naming the recorded cluster demoted it to unmanaged: %+v (%s)", binding, source)
	}
	if got := colocateOf(t, flagged); got != true {
		t.Errorf("blobStorage.persistence.colocate with --kube-context = %v, want true", got)
	}
	if !reflect.DeepEqual(bare, flagged) {
		t.Errorf("--kube-context naming the instance's own cluster changed the rendered values:\nwithout: %v\nwith:    %v", bare, flagged)
	}
}

// The same, on a machine with no record of the instance (another laptop, or one that
// predates records): the declaration is what knows the instance is Managed.
func TestUpgradeWithoutARecordTakesManagedFromTheDeclaration(t *testing.T) {
	fakeHome(t)
	flagged, binding, source := upgradeValuesFor(t, "kind-devicechain")
	if source != BindingFromFlag {
		t.Fatalf("source = %s, want flag (no record exists)", source)
	}
	_ = binding
	if got := colocateOf(t, flagged); got != true {
		t.Errorf("with no local record, colocate = %v, want true: the declaration says Managed", got)
	}
}

// A context naming a DIFFERENT cluster than the recorded one is refused, not honoured.
func TestKubeContextNamingAnotherClusterIsRefused(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "guide", Provider: "local", Cluster: "devicechain",
		KubeContext: "kind-devicechain", Managed: true})

	b, source := ResolveBinding(Options{Instance: "guide", KubeContext: "kind-elsewhere"})
	if source != BindingConflict {
		t.Fatalf("source = %s, want conflict", source)
	}
	err := RefuseConflictingContext(source, b, "kind-elsewhere", "guide")
	if err == nil {
		t.Fatal("a conflicting --kube-context was not refused")
	}
	for _, want := range []string{"kind-elsewhere", "kind-devicechain", "guide"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
	if err := RefuseConflictingContext(BindingFromRecord, b, "kind-devicechain", "guide"); err != nil {
		t.Errorf("a matching context was refused: %v", err)
	}
}

// Upgrade itself must stop at the mismatch, before it touches any cluster.
func TestUpgradeRefusesAConflictingKubeContextBeforeAnyClusterRead(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "guide", Provider: "local", Cluster: "devicechain",
		KubeContext: "kind-devicechain", Managed: true})
	err := Upgrade(t.Context(), &fakeProvider{name: "local"},
		UpgradeOptions{Options: Options{Instance: "guide", KubeContext: "kind-elsewhere"}})
	if err == nil || !strings.Contains(err.Error(), "different cluster") {
		t.Fatalf("Upgrade with a conflicting context: err = %v, want the binding refusal", err)
	}
}

// 🔴 THE WAY OUT OF refuseUnreadable MUST STAY OPEN. Its advice is "pass --kube-context
// to say where it is"; a resolver that answers that flag with BindingUnreadable again
// sends the operator round a loop with no exit.
func TestAnUnreadableRecordYieldsToAnExplicitKubeContext(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".devicechain", "instances", "guide")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, instanceRecordFile), []byte(`{"instance":"guide","clus`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, src := ResolveBinding(Options{Instance: "guide"}); src != BindingUnreadable {
		t.Fatalf("control: with no flag the corrupt record must be unreadable, got %s", src)
	}
	b, src := ResolveBinding(Options{Instance: "guide", KubeContext: "kind-devicechain"})
	if src != BindingFromFlag || b.KubeContext != "kind-devicechain" {
		t.Fatalf("the flag refuseUnreadable tells the operator to pass was not honoured: %+v %s", b, src)
	}
	if err := refuseUnreadable(src, "guide"); err != nil {
		t.Fatalf("following refuseUnreadable's advice was refused again: %v", err)
	}
}

// The refusal names a way out, and the way out works: removing the record it names and
// re-running with the flag it names reaches the cluster, and the instance still renders
// the profile its declaration holds.
func TestTheConflictRefusalsWayOutWorks(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "guide", Provider: "local", Cluster: "old",
		KubeContext: "kind-old", Managed: true})
	b, src := ResolveBinding(Options{Instance: "guide", KubeContext: "kind-devicechain"})
	err := RefuseConflictingContext(src, b, "kind-devicechain", "guide")
	if err == nil {
		t.Fatal("control: a conflicting context was not refused")
	}
	const path = "~/.devicechain/instances/guide/" + instanceRecordFile
	for _, want := range []string{path, "re-run with --kube-context kind-devicechain"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q:\n%v", want, err)
		}
	}
	if err := os.Remove(filepath.Join(home, ".devicechain", "instances", "guide", instanceRecordFile)); err != nil {
		t.Fatal(err)
	}
	vals, b, src := upgradeValuesFor(t, "kind-devicechain")
	if src != BindingFromFlag || b.KubeContext != "kind-devicechain" {
		t.Fatalf("after removing the record the flag did not resolve: %+v %s", b, src)
	}
	if err := RefuseConflictingContext(src, b, "kind-devicechain", "guide"); err != nil {
		t.Fatalf("still refused after following the advice: %v", err)
	}
	if got := colocateOf(t, vals); got != true {
		t.Errorf("colocate = %v after the way out, want true: the declaration says Managed", got)
	}
}

// Destroy refuses a conflicting context too, and before its dry-run guard: a rehearsal
// that answered "would destroy" for a command the real run refuses rehearses nothing.
func TestDestroyRefusesAConflictingKubeContextEvenUnderDryRun(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "guide", Provider: "local", Cluster: "devicechain",
		KubeContext: "kind-devicechain", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{"devicechain": true, "elsewhere": true}}
	var err error
	captureOutput(t, func() {
		err = Destroy(t.Context(), p, DestroyOptions{
			Options: Options{Instance: "guide", KubeContext: "kind-elsewhere", AssumeYes: true, DryRun: true}})
	})
	if err == nil || !strings.Contains(err.Error(), "different cluster") {
		t.Fatalf("destroy with a conflicting context: err = %v, want the binding refusal", err)
	}
	if p.existsCalls != 0 {
		t.Fatalf("asked about clusters %v before refusing", p.asked)
	}
}

// The declaration decides Managed in BOTH directions. With no record and no flag the
// binding is the kind-<instance> guess, which says Managed; an instance bootstrapped
// onto a cluster dcctl was pointed at declares Managed:false, and that is what renders.
func TestUpgradeTakesUnmanagedFromTheDeclarationOverTheGuess(t *testing.T) {
	fakeHome(t)
	opts := UpgradeOptions{Options: Options{Instance: "guide"}}
	binding, source := ResolveBinding(opts.Options)
	if source != BindingGuessed || !binding.Managed {
		t.Fatalf("control: with no record and no flag the guess says Managed, got %+v %s", binding, source)
	}
	inst := guideDeclaration()
	inst.Spec.Managed = false
	st := &State{Instance: "guide", KubeContext: binding.KubeContext, Binding: binding,
		Provider: "local", Values: map[string]string{}}
	if err := applyUpgradeDeclaration(st, inst, opts); err != nil {
		t.Fatalf("applyUpgradeDeclaration: %v", err)
	}
	if st.Binding.Managed {
		t.Fatal("the guess's Managed:true survived a declaration that says Managed:false")
	}
	if got := colocateOf(t, helmValues(st)); got == true {
		t.Errorf("colocate = %v for an instance declared unmanaged", got)
	}
}
