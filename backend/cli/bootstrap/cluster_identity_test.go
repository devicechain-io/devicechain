// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The UUIDs here are the ones a real kind rebuild produced on 2026-09-14 — two clusters
// created in turn under ONE name, `kind-dc-uid-probe`. They are spelled out rather than
// generated because the property under test is that two clusters wearing the same name
// are different clusters, and a value the test made up cannot demonstrate that the
// cluster does.
const (
	firstClusterUID  = "163e7f17-d87c-42fe-8bc0-e672e35f5ee7"
	secondClusterUID = "446b60a1-b6f8-4cf0-9e14-ced15bc26170"
)

func kubeSystem(uid string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)},
	}
}

func TestClusterUIDReadsTheKubeSystemNamespace(t *testing.T) {
	typed := fake.NewSimpleClientset(kubeSystem(firstClusterUID))

	got, err := ClusterUID(context.Background(), typed)
	if err != nil {
		t.Fatalf("ClusterUID: %v", err)
	}
	if got != firstClusterUID {
		t.Fatalf("ClusterUID = %q, want %q", got, firstClusterUID)
	}
}

// TestTheIdentityComesFromKubeSystemAndNotSomeOtherNamespace pins WHICH namespace, which
// a test holding only kube-system cannot: a reader that fetched `default` would pass it
// by finding the one object the fake holds. Here both exist and carry different UIDs, so
// the answer names the namespace it came from.
func TestTheIdentityComesFromKubeSystemAndNotSomeOtherNamespace(t *testing.T) {
	typed := fake.NewSimpleClientset(
		kubeSystem(firstClusterUID),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: types.UID(secondClusterUID)}},
	)

	got, err := ClusterUID(context.Background(), typed)
	if err != nil {
		t.Fatalf("ClusterUID: %v", err)
	}
	if got == secondClusterUID {
		t.Fatal("the identity was read from the default namespace; an operator can delete and " +
			"recreate that one, which would silently re-identify an unchanged cluster")
	}
	if got != firstClusterUID {
		t.Fatalf("ClusterUID = %q, want kube-system's %q", got, firstClusterUID)
	}
}

// TestTwoClustersUnderOneNameAreTwoIdentities is the property the key exists for, and it
// is stated here as the rebuild that produced these values: the context name is an input
// to neither call, and the two answers differ.
func TestTwoClustersUnderOneNameAreTwoIdentities(t *testing.T) {
	before := fake.NewSimpleClientset(kubeSystem(firstClusterUID))
	after := fake.NewSimpleClientset(kubeSystem(secondClusterUID))

	a, err := ClusterUID(context.Background(), before)
	if err != nil {
		t.Fatalf("ClusterUID before the rebuild: %v", err)
	}
	b, err := ClusterUID(context.Background(), after)
	if err != nil {
		t.Fatalf("ClusterUID after the rebuild: %v", err)
	}
	if a == b {
		t.Fatal("a rebuilt cluster answered with the identity of the one it replaced")
	}
}

// TestAnUnreadableNamespaceIsAnError is the fail-closed half. Returning "" with no error
// would hand the empty string to dcdir.Cluster, and the whole point of that refusal is
// that nothing upstream should be producing one.
func TestAnUnreadableNamespaceIsAnError(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})

	got, err := ClusterUID(context.Background(), typed)
	if err == nil {
		t.Fatalf("ClusterUID = %q, want an error when the namespace cannot be read", got)
	}
	if got != "" {
		t.Fatalf("ClusterUID returned %q alongside an error; a caller reading the value first "+
			"would key state on it", got)
	}
}

// TestANamespaceWithNoUIDIsAnError covers the one shape an API server never produces and
// a fixture easily does. Without it a fake built from an under-specified namespace yields
// "" with a nil error, which is the value dcdir.Cluster refuses — the failure would then
// surface one layer away from its cause.
func TestANamespaceWithNoUIDIsAnError(t *testing.T) {
	typed := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system"},
	})

	if got, err := ClusterUID(context.Background(), typed); err == nil {
		t.Fatalf("ClusterUID = %q, want an error for a namespace carrying no UID", got)
	}
}

func TestWriteClusterRecordPutsItUnderTheClusterDirectory(t *testing.T) {
	home := fakeHome(t)

	rec := ClusterRecord{
		UID:          firstClusterUID,
		Cluster:      "dc-uid-probe",
		KubeContext:  "kind-dc-uid-probe",
		FirstSeenAt:  time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		DcctlVersion: "v0.16.0",
	}
	if err := WriteClusterRecord(rec); err != nil {
		t.Fatalf("WriteClusterRecord: %v", err)
	}

	path := filepath.Join(home, ".devicechain", "clusters", firstClusterUID, "cluster.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	// Unmarshalled independently rather than through a reader in this package: a
	// round-trip through code that shares the struct tags checks that the two halves
	// agree with each other, not that the file on disk says what it should.
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("the record is not JSON: %v", err)
	}
	if got["uid"] != firstClusterUID {
		t.Errorf("uid = %v, want %q", got["uid"], firstClusterUID)
	}
	if got["kubeContext"] != "kind-dc-uid-probe" {
		t.Errorf("kubeContext = %v, want the context name", got["kubeContext"])
	}
	if got["cluster"] != "dc-uid-probe" {
		t.Errorf("cluster = %v, want the cluster name", got["cluster"])
	}
}

// TestTheClusterTreeIsOwnerOnly matters for the same reason the instance tree's modes do,
// and it is asserted at every level rather than the leaf. What will land in here is the
// prerequisite root's OpenTofu state, and tfstate holds cleartext values; clusters/ is
// the traversable level above every one of them. Written before that state exists, on
// purpose — a mode is much harder to argue for once there is something under it.
func TestTheClusterTreeIsOwnerOnly(t *testing.T) {
	home := fakeHome(t)

	// The layout an older dcctl would leave: every level world-readable already, since
	// MkdirAll applies its mode only to directories it creates.
	loose := filepath.Join(home, ".devicechain", "clusters", firstClusterUID)
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatalf("seeding the loose layout: %v", err)
	}
	for _, p := range []string{
		filepath.Join(home, ".devicechain"),
		filepath.Join(home, ".devicechain", "clusters"),
		loose,
	} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}

	if err := WriteClusterRecord(ClusterRecord{UID: firstClusterUID, KubeContext: "kind-x"}); err != nil {
		t.Fatalf("WriteClusterRecord: %v", err)
	}

	for _, p := range []string{
		filepath.Join(home, ".devicechain"),
		filepath.Join(home, ".devicechain", "clusters"),
		loose,
	} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := fi.Mode().Perm(); got != stateDirMode {
			t.Errorf("%s has mode %04o, want %04o", p, got, stateDirMode)
		}
	}
	fi, err := os.Stat(filepath.Join(loose, "cluster.json"))
	if err != nil {
		t.Fatalf("stat the record: %v", err)
	}
	if got := fi.Mode().Perm(); got != stateFileMode {
		t.Errorf("cluster.json has mode %04o, want %04o", got, stateFileMode)
	}
}

// TestWriteClusterRecordRefusesAnEmptyIdentity is the reason dcdir.Cluster validates,
// asserted where the damage would happen. Writing into the clusters directory itself
// would put a cluster.json beside every cluster's state rather than inside one's.
func TestWriteClusterRecordRefusesAnEmptyIdentity(t *testing.T) {
	home := fakeHome(t)

	if err := WriteClusterRecord(ClusterRecord{KubeContext: "kind-x"}); err == nil {
		t.Fatal("WriteClusterRecord accepted a record with no identity")
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "clusters", "cluster.json")); err == nil {
		t.Fatal("a record was written into the clusters directory itself")
	}
}

// TestTheRecordReplacesRatherThanAccumulates pins the same property WriteInstanceRecord
// has: the names in it are the mutable half, and a cluster reached under a new context
// name must have the record corrected rather than appended to.
func TestTheRecordReplacesRatherThanAccumulates(t *testing.T) {
	home := fakeHome(t)

	for _, ctxName := range []string{"kind-old", "kind-new"} {
		if err := WriteClusterRecord(ClusterRecord{UID: firstClusterUID, KubeContext: ctxName}); err != nil {
			t.Fatalf("WriteClusterRecord(%s): %v", ctxName, err)
		}
	}

	b, err := os.ReadFile(filepath.Join(home, ".devicechain", "clusters", firstClusterUID, "cluster.json"))
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	var got ClusterRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if got.KubeContext != "kind-new" {
		t.Fatalf("kubeContext = %q, want the corrected name", got.KubeContext)
	}
}

// TestTheRecordedIdentityReachesTheBinding is the hop leg 6 will depend on: a destroy
// resolves a ClusterBinding and never opens the record itself, so an identity that
// round-trips into InstanceRecord but stops there is an identity nothing can use.
func TestTheRecordedIdentityReachesTheBinding(t *testing.T) {
	rec := InstanceRecord{
		Instance: "prod", Cluster: "kind-prod", KubeContext: "kind-prod",
		Managed: true, ClusterUID: firstClusterUID,
	}
	if got := rec.Binding().ClusterUID; got != firstClusterUID {
		t.Fatalf("Binding().ClusterUID = %q, want %q", got, firstClusterUID)
	}
}

// TestAGuessedBindingCarriesNoIdentity is the other half, and it is the distinction the
// record type's own comment refuses to let anyone collapse. A guess is derived from the
// instance NAME, which is exactly what cannot identify a cluster — so it must answer
// "nothing recorded" rather than anything a caller could compare against.
func TestAGuessedBindingCarriesNoIdentity(t *testing.T) {
	if got := GuessBinding("prod").ClusterUID; got != "" {
		t.Fatalf("GuessBinding carries ClusterUID %q; a guess knows no identity", got)
	}
}

// TestAPreIdentityRecordStillReadsAsABinding keeps the field from becoming a requirement.
// Every instance bootstrapped before this existed has a record with no clusterUid, and
// those instances have to stay destroyable — the whole ClusterBinding type exists because
// an unactionable live instance is worse than a degraded one.
func TestAPreIdentityRecordStillReadsAsABinding(t *testing.T) {
	fakeHome(t)

	old := []byte(`{"instance":"legacy","provider":"local","cluster":"kind-legacy",` +
		`"kubeContext":"kind-legacy","managed":true,"createdAt":"2026-01-01T00:00:00Z"}` + "\n")
	dir, err := instanceStateDir("legacy", "")
	if err != nil {
		t.Fatalf("instanceStateDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), old, 0o600); err != nil {
		t.Fatalf("seeding the pre-identity record: %v", err)
	}

	got, err := ReadInstanceRecord("legacy")
	if err != nil {
		t.Fatalf("ReadInstanceRecord on a pre-identity record: %v", err)
	}
	if got.ClusterUID != "" {
		t.Errorf("ClusterUID = %q, want empty for a record written before it existed", got.ClusterUID)
	}
	if b := got.Binding(); b.KubeContext != "kind-legacy" || !b.Managed {
		t.Fatalf("the rest of the binding did not survive: %+v", b)
	}
}

// 🔴 THE SUBDIRECTORY IS THE POINT, and the mutation round found it untested: ignoring
// `sub` entirely SURVIVED, because every existing caller passed "".
//
// 🔑 WHAT THE MUTANT WOULD COST. The prerequisite root's working directory and state
// would land in the cluster's own directory rather than under it — on top of
// cluster.json, beside it — so `tofu init` would write its provider cache and its state
// into the same directory the identity record lives in. The record still reads, the
// apply still runs, and the only symptom is a record written to be read sharing a
// directory with a terraform.tfstate.
//
// (An earlier version of this comment said that state holds the database superuser
// password. Measured on a live round-trip, it does not: since dcctl began minting the
// credentials before the apply, the prerequisite state carries only Secret NAMES and key
// names. The directory stays owner-only anyway — see the next test.)
func TestClusterStateDirPutsTheRootUnderTheClusterNotBesideIt(t *testing.T) {
	home := fakeHome(t)

	dir, err := clusterStateDir(firstClusterUID, "infra")
	if err != nil {
		t.Fatalf("clusterStateDir: %v", err)
	}
	want := filepath.Join(home, ".devicechain", "clusters", firstClusterUID, "infra")
	if dir != want {
		t.Errorf("clusterStateDir(uid, %q) = %q, want %q", "infra", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("the subdirectory was not created: %v", err)
	}

	// The counterweight, and it is what stops this passing against a function that
	// appends a fixed string: an empty sub must still mean the cluster's own directory,
	// which is where cluster.json goes.
	own, err := clusterStateDir(firstClusterUID, "")
	if err != nil {
		t.Fatalf("clusterStateDir with no sub: %v", err)
	}
	if own != filepath.Join(home, ".devicechain", "clusters", firstClusterUID) {
		t.Errorf("clusterStateDir(uid, \"\") = %q, want the cluster's own directory", own)
	}
}

// ...and the subdirectory must be owner-only too. The tree test above this one asserts
// every level down to the cluster; the state the prerequisite root writes lands one
// level deeper, and that is the level holding the tfstate.
func TestTheClusterStateSubdirectoryIsOwnerOnly(t *testing.T) {
	home := fakeHome(t)

	// The layout an older dcctl would leave: world-readable already, since MkdirAll
	// applies its mode only to directories it creates.
	loose := filepath.Join(home, ".devicechain", "clusters", firstClusterUID, "infra")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}

	dir, err := clusterStateDir(firstClusterUID, "infra")
	if err != nil {
		t.Fatalf("clusterStateDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != stateDirMode {
		t.Errorf("the prerequisite state directory is %#o, want %#o — it holds a tfstate, "+
			"which describes the cluster's shared infrastructure and is one provider change "+
			"away from holding a value that must not be world-readable", got, stateDirMode)
	}
}
