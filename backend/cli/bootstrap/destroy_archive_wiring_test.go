// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	k8stesting "k8s.io/client-go/testing"
)

// The archive every fixture below is built around: instance acme's event store,
// archiving under the path a bootstrap minted for it.
const (
	testArchiveBucket = "devicechain-tsdb"
	testArchivePath   = "dc-tsdb-acme-1a2b3c4d"
)

// testObjectStoreGVR is spelled out rather than borrowed so this file stands on its own.
var testObjectStoreGVR = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}

// archivingEventStore is instance acme's event store as the instance root renders it: a
// Cluster whose barman archiver writes under serverName, and the ObjectStore it names.
func archivingEventStore(serverName, endpoint string) []runtime.Object {
	ns := InstanceNamespace("acme")
	cl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": TsdbClusterName, "namespace": ns},
		"spec": map[string]any{"plugins": []any{map[string]any{
			"name":          "barman-cloud.cloudnative-pg.io",
			"enabled":       true,
			"isWALArchiver": true,
			"parameters":    map[string]any{"barmanObjectName": TsdbClusterName + "-backup", "serverName": serverName},
		}}},
	}}
	store := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "barmancloud.cnpg.io/v1",
		"kind":       "ObjectStore",
		"metadata":   map[string]any{"name": TsdbClusterName + "-backup", "namespace": ns},
		"spec": map[string]any{"configuration": map[string]any{
			"destinationPath": "s3://" + testArchiveBucket + "/",
			"endpointURL":     endpoint,
		}},
	}}
	return []runtime.Object{cl, store}
}

// withEventStore gives the teardown rig a dynamic client that holds, besides the
// instance declarations, acme's archiving event store — and takes that event store
// away when the rig deletes acme's namespace, as the cluster would.
func (r *teardownRig) withEventStore(t *testing.T, objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			instanceGVR:        "InstanceList",
			clusterGVR:         "ClusterList",
			testObjectStoreGVR: "ObjectStoreList",
		}, objs...)
	r.typed.PrependReactor("delete", "namespaces", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.DeleteAction).GetName() == InstanceNamespace("acme") {
			ns := InstanceNamespace("acme")
			_ = dyn.Resource(clusterGVR).Namespace(ns).Delete(context.Background(), TsdbClusterName, metav1.DeleteOptions{})
			_ = dyn.Resource(testObjectStoreGVR).Namespace(ns).Delete(context.Background(), TsdbClusterName+"-backup", metav1.DeleteOptions{})
		}
		return false, nil, nil
	})
	restore := teardownClients
	t.Cleanup(func() { teardownClients = restore })
	teardownClients = func(string) (dynamic.Interface, kubernetes.Interface, error) { return dyn, r.typed, nil }
	return dyn
}

// withInstallRecord writes the cluster's install record into the rig: a default install,
// backups on, in the cluster's own object store unless external says otherwise.
func (r *teardownRig) withInstallRecord(t *testing.T, external bool) {
	t.Helper()
	rec := aCompleteInstall()
	rec.ClusterUID = "cluster-uid" // the rig's kube-system UID
	rec.Settings.BackupsExternal = external
	if external {
		rec.Outputs.Archive.EndpointURL = "https://s3.example.com"
	}
	if err := writeInstalled(context.Background(), r.typed, rec, installClock); err != nil {
		t.Fatal(err)
	}
	stubRemoveInstanceDatabase(t, func(context.Context, string, string, ClusterRdb) error { return nil })
}

// 🔴 A DESTROY SAYS WHICH IN-CLUSTER ARCHIVE IT LEFT, AND DOES NOT CLOSE GREEN OVER IT.
//
// The whole defect, measured on a benchmark cluster: destroy removed the instance and
// left 14 GB of its WAL archive and base backups in the cluster's own object store, and
// its transcript never mentioned the archive at all. Nothing prunes a destroyed
// instance's archive, so the next instance started on a nearly full store and its
// primary ran out of disk when archiving failed.
//
// Driven through Destroy with the object store NOT reachable (the rig has no Service for
// it), so the one outcome that can be checked without a store is the one checked: the
// transcript names the exact archive, and the closing line is not the green "destroyed".
func TestDestroyNamesTheInClusterArchiveItCouldNotRemove(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
		[]string{"acme"}, "acme")
	r.withInstallRecord(t, false)
	r.withEventStore(t, archivingEventStore(testArchivePath, "http://dc-object-store.dc-system:9000")...)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	want := "s3://" + testArchiveBucket + "/" + testArchivePath + "/"
	if !strings.Contains(out, want) {
		t.Errorf("the transcript never names the archive %s:\n%s", want, out)
	}
	if !strings.Contains(out, "LEFT in the in-cluster object store") {
		t.Errorf("the closing line does not say the archive was left:\n%s", out)
	}
	if strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("a destroy that left the instance's archive closed as destroyed:\n%s", out)
	}
	if !r.stateRemoved() {
		t.Errorf("an unreachable object store failed the destroy's local cleanup:\n%s", out)
	}
}
