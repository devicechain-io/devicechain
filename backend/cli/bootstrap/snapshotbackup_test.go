// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// Volume-snapshot base backups (`dcctl install --backup-snapshot-class`), end to end
// through dcctl: the flag, the install record every bootstrap follows, the variables
// each OpenTofu root is handed, what the chart's alerts are told, and the preflight
// that refuses a class that cannot work.

const testSnapshotClass = "pd-snapshots"

// 🔴 A SCHEMA-3 RECORD IS REFUSED. It cannot say whether the cluster takes snapshots,
// so a bootstrap from it would guess "no" -- building an event store that takes full
// copies on a cluster whose relational store takes snapshots.
func TestAnInstallRecordFromBeforeSnapshotsIsRefused(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	cm, _ := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	var rec InstallRecord
	if err := json.Unmarshal([]byte(cm.Data["install.json"]), &rec); err != nil {
		t.Fatal(err)
	}
	rec.Schema = 3
	body, _ := json.Marshal(rec)
	cm.Data["install.json"] = string(body)
	if _, err := c.CoreV1().ConfigMaps("dc-system").Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := readInstallRecord(context.Background(), c, testClusterUID)
	if !errors.Is(err, ErrInstallRecordSchema) {
		t.Fatalf("a schema-3 install record read as %v, want ErrInstallRecordSchema", err)
	}
	if !strings.Contains(err.Error(), "Re-run `dcctl install`") {
		t.Errorf("the refusal does not say how to bring the record up to date: %v", err)
	}
}

// The option reaches the State every later step reads, and through it the record.
func TestTheSnapshotClassReachesTheStateAnInstallAppliesFrom(t *testing.T) {
	st := installState(ClusterBinding{KubeContext: "kind-devicechain"}, "local",
		InstallOptions{BackupSnapshotClass: testSnapshotClass})
	if got := backupSnapshotClass(st); got != testSnapshotClass {
		t.Errorf("an install asked for class %q applies with %q", testSnapshotClass, got)
	}
}

// The class is recorded from the one predicate, and dropped with the backups it
// belongs to.
func TestTheInstallRecordCarriesTheSnapshotClass(t *testing.T) {
	st := aWritableState()
	st.BackupSnapshotClass = testSnapshotClass
	if got := installSettingsFor(st).BackupSnapshotClass; got != testSnapshotClass {
		t.Errorf("an install asking for %q recorded %q", testSnapshotClass, got)
	}

	st.Compact, st.NoTLS = true, true
	if got := installSettingsFor(st); got.BackupSnapshotClass != "" || got.DatabaseBackups {
		t.Errorf("a compact plain-HTTP install, which has no backups, recorded %+v", got)
	}
}

// What the settings promise, the apply must have delivered: an install record whose
// relational store was handed another class than the one every instance will follow
// is not written as installed.
func TestARecordWhoseApplyDidNotDeliverTheSnapshotClassIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name              string
		settings, outputs string
		backups           bool
		refused           string
	}{
		{name: "asked for, not delivered", settings: testSnapshotClass, backups: true,
			refused: `the cluster apply reports ""`},
		{name: "delivered, not asked for", outputs: testSnapshotClass, backups: true,
			refused: `the cluster apply reports "pd-snapshots"`},
		{name: "a different class delivered", settings: testSnapshotClass, outputs: "other", backups: true,
			refused: `the cluster apply reports "other"`},
		{name: "snapshots with backups off", settings: testSnapshotClass, outputs: testSnapshotClass,
			refused: "with backups off"},
		{name: "asked for and delivered", settings: testSnapshotClass, outputs: testSnapshotClass, backups: true},
		{name: "neither", backups: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := aCompleteInstall()
			if !tc.backups {
				rec = InstallRecord{ClusterUID: testClusterUID, Outputs: InstallOutputs{Rdb: aRelationalStore()}}
			}
			rec.Settings.BackupSnapshotClass = tc.settings
			rec.Outputs.BackupSnapshotClass = tc.outputs
			err := writeInstalled(context.Background(), fake.NewSimpleClientset(), rec, installClock)
			switch {
			case tc.refused == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.refused != "" && err == nil:
				t.Errorf("written as installed; want a refusal mentioning %q", tc.refused)
			case tc.refused != "" && !strings.Contains(err.Error(), tc.refused):
				t.Errorf("refused, but not for the class: %v (want %q)", err, tc.refused)
			}
		})
	}
}

// The class the cluster apply handed the relational store is read back into the
// record's outputs; null (object-store base backups) reads as empty.
func TestTheClusterApplysSnapshotClassIsRecorded(t *testing.T) {
	base := func() map[string]tfexec.OutputMeta {
		return map[string]tfexec.OutputMeta{
			"backup_endpoint_url":          {Value: []byte(`"http://e"`)},
			"backup_credentials_secret":    {Value: []byte(`"s"`)},
			"backup_access_key_id_key":     {Value: []byte(`"a"`)},
			"backup_secret_access_key_key": {Value: []byte(`"k"`)},
			"backup_bucket_tsdb":           {Value: []byte(`"b"`)},
			"namespace":                    {Value: []byte(`"dc-system"`)},
			"postgres_cluster_name":        {Value: []byte(`"dc-rdb"`)},
			"postgres_max_connections":     {Value: []byte(`600`)},
			"database_node_selector":       {Value: []byte(`{}`)},
			"database_tolerations":         {Value: []byte(`[]`)},
		}
	}
	outputs := base()
	outputs["database_backup_snapshot_class"] = tfexec.OutputMeta{Value: []byte(`"pd-snapshots"`)}
	got, err := clusterOutputs(outputs)
	if err != nil {
		t.Fatal(err)
	}
	if got.BackupSnapshotClass != testSnapshotClass {
		t.Errorf("the apply reported %q, the record holds %q", testSnapshotClass, got.BackupSnapshotClass)
	}

	outputs = base()
	outputs["database_backup_snapshot_class"] = tfexec.OutputMeta{Value: []byte(`null`)}
	if got, err := clusterOutputs(outputs); err != nil || got.BackupSnapshotClass != "" {
		t.Errorf("a null class read as %q (%v), want empty", got.BackupSnapshotClass, err)
	}
}

// Both roots declare backup_snapshot_class, so it reaches both stores; with backups off
// it reaches neither; and a bootstrap takes it from the install record, never from a
// value of its own.
func TestTheSnapshotClassReachesBothRootsOnlyWithBackups(t *testing.T) {
	want := "backup_snapshot_class=" + testSnapshotClass
	route := func(t *testing.T, st *State) (cluster, instance []string) {
		t.Helper()
		c, i, err := splitVars(infraVars(st))
		if err != nil {
			t.Fatal(err)
		}
		return c, i
	}

	st := aWritableState()
	st.BackupSnapshotClass = testSnapshotClass
	cluster, instance := route(t, st)
	if !slices.Contains(cluster, want) || !slices.Contains(instance, want) {
		t.Errorf("an install asking for snapshots handed the cluster root %v and the instance root %v; "+
			"both need %s", cluster, instance, want)
	}

	st.Compact, st.NoTLS = true, true
	cluster, instance = route(t, st)
	for _, v := range append(cluster, instance...) {
		if strings.HasPrefix(v, "backup_snapshot_class=") {
			t.Errorf("an install with no backups emitted %s", v)
		}
	}

	// A bootstrap: the record says snapshots, and the state carries nothing of its own.
	rec := aCompleteInstall()
	rec.Settings.BackupSnapshotClass = testSnapshotClass
	st = aWritableState()
	st.Install = &rec
	if _, instance := route(t, st); !slices.Contains(instance, want) {
		t.Errorf("a bootstrap on a snapshot cluster handed its event store %v, without %s", instance, want)
	}
	// ...and the record says none, whatever the state carries.
	rec = aCompleteInstall()
	st = aWritableState()
	st.Install = &rec
	st.BackupSnapshotClass = testSnapshotClass
	if _, instance := route(t, st); slices.ContainsFunc(instance, func(v string) bool {
		return strings.HasPrefix(v, "backup_snapshot_class=")
	}) {
		t.Errorf("a bootstrap on a cluster installed without snapshots emitted one: %v", instance)
	}
}

// Changing the class under running instances is refused like every other setting: each
// instance's event store was built to the one it found.
func TestAReinstallThatChangesTheSnapshotClassUnderInstancesIsRefused(t *testing.T) {
	withClusterInstances(t, []string{"prod"}, nil)
	st, settings := reinstallState()
	settings.BackupSnapshotClass = testSnapshotClass
	err := refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings, stateHere())
	if err == nil {
		t.Fatal("turning volume-snapshot base backups on under a running instance was not refused")
	}
	if !strings.Contains(err.Error(), "volume-snapshot base backups (pd-snapshots)") {
		t.Errorf("the refusal does not name the change: %v", err)
	}

	withClusterInstances(t, nil, nil)
	if err := refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings, stateHere()); err != nil {
		t.Errorf("with no instances running the change was refused: %v", err)
	}
}

// What the apply reported reaches the chart's alerts, and absence is off.
func TestTheChartIsToldWhetherBaseBackupsAreSnapshots(t *testing.T) {
	st := &State{Instance: "prod", Values: map[string]string{databaseBackupSnapshotsKey: "true"}}
	if got := metricsBlock(t, st)["databaseBackupSnapshots"]; got != true {
		t.Errorf("metrics.databaseBackupSnapshots = %#v, want true", got)
	}
	st = &State{Instance: "prod", Values: map[string]string{}}
	if got := metricsBlock(t, st)["databaseBackupSnapshots"]; got != false {
		t.Errorf("metrics.databaseBackupSnapshots = %#v with nothing reported, want false", got)
	}
}

// An upgrade runs no apply, so it carries the value from the release it replaces --
// and never invents it.
func TestAnUpgradeKeepsTheSnapshotAlertsTheInstanceHad(t *testing.T) {
	prev := aPreviousRelease()
	prev["metrics"].(map[string]interface{})["databaseBackupSnapshots"] = true
	st := &State{Values: map[string]string{}}
	carryForwardFromRelease(st, prev)
	if got := st.Values[databaseBackupSnapshotsKey]; got != "true" {
		t.Errorf("databaseBackupSnapshots came across as %q; the upgrade would put the base-backup alert "+
			"back on a daily threshold under a weekly schedule", got)
	}

	st = &State{Values: map[string]string{}}
	carryForwardFromRelease(st, aPreviousRelease())
	if got, ok := st.Values[databaseBackupSnapshotsKey]; ok {
		t.Errorf("databaseBackupSnapshots was invented as %q for a release that records none", got)
	}
}

func TestTheSnapshotClassFlagIsCheckedFromArgv(t *testing.T) {
	if err := ValidateBackupSnapshotClass("", false); err != nil {
		t.Errorf("no class refused: %v", err)
	}
	if err := ValidateBackupSnapshotClass(testSnapshotClass, true); err != nil {
		t.Errorf("a valid class with backups on refused: %v", err)
	}
	for _, bad := range []string{"PD-Snapshots", "pd_snapshots", "-pd", "pd snapshots"} {
		if err := ValidateBackupSnapshotClass(bad, true); err == nil || !strings.Contains(err.Error(), "not a Kubernetes object name") {
			t.Errorf("class %q: %v, want a refusal of the name", bad, err)
		}
	}
	if err := ValidateBackupSnapshotClass(testSnapshotClass, false); err == nil ||
		!strings.Contains(err.Error(), "no database backups") {
		t.Errorf("a class with backups off: %v, want a refusal", err)
	}
}

// --- the preflight -----------------------------------------------------------------

func snapshotClassObject(name, driver, policy string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotClass",
		"driver": driver, "deletionPolicy": policy,
	}}
	u.SetName(name)
	return u
}

func storageClass(name, provisioner string, isDefault bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: provisioner}
	if isDefault {
		sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return sc
}

// snapshotCluster is a cluster serving the snapshot API (unless noAPI), holding these
// classes and these objects.
func snapshotCluster(noAPI bool, classes []*unstructured.Unstructured, objects ...runtime.Object) (*dynamicfake.FakeDynamicClient, *fake.Clientset) {
	var objs []runtime.Object
	for _, c := range classes {
		objs = append(objs, c)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{volumeSnapshotClassGVR: "VolumeSnapshotClassList"}, objs...)
	typed := fake.NewSimpleClientset(objects...)
	if !noAPI {
		typed.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{{
			GroupVersion: "snapshot.storage.k8s.io/v1",
			APIResources: []metav1.APIResource{{Name: "volumesnapshotclasses", Kind: "VolumeSnapshotClass"}},
		}}
	}
	return dyn, typed
}

func boundClaim(ns, name, cluster, volume string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"cnpg.io/cluster": cluster}},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: volume},
	}
}

func csiVolume(name, driver string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{Driver: driver, VolumeHandle: name}}}}
}

func TestTheSnapshotClassPreflight(t *testing.T) {
	const gke = "pd.csi.storage.gke.io"
	good := snapshotClassObject(testSnapshotClass, gke, "Delete")
	for _, tc := range []struct {
		name    string
		noAPI   bool
		classes []*unstructured.Unstructured
		objects []runtime.Object
		refused []string
	}{{
		name:    "no snapshot API",
		noAPI:   true,
		refused: []string{"serves no VolumeSnapshotClass API", "on EKS install the snapshot controller"},
	}, {
		name:    "no such class, and the ones there are named",
		classes: []*unstructured.Unstructured{snapshotClassObject("csi-hostpath", "hostpath.csi.k8s.io", "Delete")},
		objects: []runtime.Object{storageClass("premium-rwo", gke, true)},
		refused: []string{`"pd-snapshots" does not exist`, "csi-hostpath (driver hostpath.csi.k8s.io)"},
	}, {
		name:    "a class that keeps the provider's snapshot",
		classes: []*unstructured.Unstructured{snapshotClassObject(testSnapshotClass, gke, "Retain")},
		objects: []runtime.Object{storageClass("premium-rwo", gke, true)},
		refused: []string{`deletionPolicy "Retain"`},
	}, {
		name:    "a class for another driver than the store's volumes",
		classes: []*unstructured.Unstructured{good},
		objects: []runtime.Object{
			storageClass("premium-rwo", gke, true),
			boundClaim("dc-system", "dc-rdb-1", "dc-rdb", "pv-1"),
			csiVolume("pv-1", "ebs.csi.aws.com"),
		},
		refused: []string{`is for driver "pd.csi.storage.gke.io"`, `provisioned by "ebs.csi.aws.com"`},
	}, {
		name:    "a new store on a cluster with no default StorageClass",
		classes: []*unstructured.Unstructured{good},
		objects: []runtime.Object{storageClass("premium-rwo", gke, false)},
		refused: []string{"no default StorageClass"},
	}, {
		name:    "a new store on a cluster with two defaults",
		classes: []*unstructured.Unstructured{good},
		objects: []runtime.Object{storageClass("premium-rwo", gke, true), storageClass("standard", "rancher.io/local-path", true)},
		refused: []string{"marks 2 StorageClasses as default"},
	}, {
		name:    "a new store on the default StorageClass of the class's driver",
		classes: []*unstructured.Unstructured{good},
		objects: []runtime.Object{storageClass("premium-rwo", gke, true)},
	}, {
		name:    "an existing store whose volumes the class's driver provisions",
		classes: []*unstructured.Unstructured{good},
		objects: []runtime.Object{
			storageClass("standard", "rancher.io/local-path", true),
			boundClaim("dc-system", "dc-rdb-1", "dc-rdb", "pv-1"),
			csiVolume("pv-1", gke),
		},
	}, {
		// CSI migration: an in-tree name on the class, the CSI driver underneath.
		name:    "a migrated in-tree default StorageClass",
		classes: []*unstructured.Unstructured{good},
		objects: []runtime.Object{storageClass("standard", "kubernetes.io/gce-pd", true)},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, typed := snapshotCluster(tc.noAPI, tc.classes, tc.objects...)
			err := checkVolumeSnapshotClass(context.Background(), dyn, typed, testSnapshotClass, "dc-system", "dc-rdb")
			if len(tc.refused) == 0 {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want a refusal mentioning %q", tc.refused)
			}
			for _, want := range tc.refused {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// stubSnapshotClassClients points the bootstrap preflight at these clients and counts
// the connections.
func stubSnapshotClassClients(t *testing.T, dyn *dynamicfake.FakeDynamicClient, typed *fake.Clientset) *int {
	t.Helper()
	orig := snapshotClassClients
	t.Cleanup(func() { snapshotClassClients = orig })
	calls := 0
	snapshotClassClients = func(string) (dynamic.Interface, kubernetes.Interface, error) {
		calls++
		return dyn, typed, nil
	}
	return &calls
}

// 🔴 THE STEP, NOT JUST THE CHECK. A bootstrap on a snapshot cluster whose class has
// gone is refused in the step TestTheSingletonStepRunsBeforeAnythingIsWritten holds
// ahead of the Instance declaration -- found at the apply, it would leave a declared
// instance that was never built, which an upgrade then refuses. The store is not even
// asked.
func TestABootstrapRefusesAMissingSnapshotClassBeforeDeclaringTheInstance(t *testing.T) {
	stubNamespacePrecheck(t)
	stubSingletons(t, clusterSingletons{}, nil)
	storeAsks := stubSharedStore(t, instanceStore{}, errors.New("the store must not be reached"))
	dyn, typed := snapshotCluster(false, nil, storageClass("premium-rwo", "pd.csi.storage.gke.io", true))
	connects := stubSnapshotClassClients(t, dyn, typed)

	rec := installed()
	rec.Settings.BackupSnapshotClass = testSnapshotClass
	rec.Outputs.BackupSnapshotClass = testSnapshotClass
	st := &State{Instance: "beta", IngressHost: "beta.localhost", Install: rec, Values: map[string]string{}}
	err := stepCheckClusterSingletons(context.Background(), st)
	if err == nil || !strings.Contains(err.Error(), `"pd-snapshots" does not exist`) {
		t.Fatalf("a bootstrap on a cluster whose snapshot class is gone was not refused for it: %v", err)
	}
	if *connects != 1 || *storeAsks != 0 {
		t.Errorf("the class was checked %d time(s) and the store asked %d time(s); want 1 and 0",
			*connects, *storeAsks)
	}

	// A cluster installed without snapshots does not look.
	connects = stubSnapshotClassClients(t, dyn, typed)
	st = &State{Instance: "beta", IngressHost: "beta.localhost", Install: installed(), Values: map[string]string{}}
	_ = stepCheckClusterSingletons(context.Background(), st)
	if *connects != 0 {
		t.Errorf("a cluster without snapshots had its snapshot class checked %d time(s)", *connects)
	}
}

// stubInstallClients points Install's connection at these clients and counts the
// connections.
func stubInstallClients(t *testing.T, dyn *dynamicfake.FakeDynamicClient, typed *fake.Clientset) *int {
	t.Helper()
	orig := installClients
	t.Cleanup(func() { installClients = orig })
	calls := 0
	installClients = func(string) (dynamic.Interface, kubernetes.Interface, error) {
		calls++
		return dyn, typed, nil
	}
	return &calls
}

// writesOn names every action on a fake client that is not a read.
func writesOn(actions []interface{ GetVerb() string }) []string {
	var writes []string
	for _, a := range actions {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			writes = append(writes, a.GetVerb())
		}
	}
	return writes
}

// 🔴 THE INSTALL, NOT JUST THE CHECK. `dcctl install --backup-snapshot-class` on a
// cluster without that class is refused where Install connects -- before the clients
// every write goes through are handed back -- so nothing is written and the install
// record is never marked applying. The OpenTofu precondition catches a missing class
// only after both, and never catches one for another driver.
func TestAnInstallRefusesABadSnapshotClassBeforeAnyWrite(t *testing.T) {
	const gke = "pd.csi.storage.gke.io"
	for _, tc := range []struct {
		name    string
		classes []*unstructured.Unstructured
		refused string
	}{
		{"a class the cluster does not have", nil, `"pd-snapshots" does not exist`},
		{"a class that keeps the provider's snapshot",
			[]*unstructured.Unstructured{snapshotClassObject(testSnapshotClass, gke, "Retain")}, `deletionPolicy "Retain"`},
		{"a class for another driver",
			[]*unstructured.Unstructured{snapshotClassObject(testSnapshotClass, "ebs.csi.aws.com", "Delete")}, `is for driver "ebs.csi.aws.com"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, typed := snapshotCluster(false, tc.classes, storageClass("premium-rwo", gke, true))
			connects := stubInstallClients(t, dyn, typed)
			st := aWritableState()
			st.BackupSnapshotClass = testSnapshotClass

			gotDyn, gotTyped, err := connectAndRefuse(context.Background(), st, installSettingsFor(st),
				func(string) (bool, error) { return true, nil })
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("an install with %s was not refused for it: %v", tc.name, err)
			}
			if gotDyn != nil || gotTyped != nil {
				t.Errorf("a refused install was still handed the clients its writes go through")
			}
			if *connects != 1 {
				t.Errorf("connected %d time(s), want 1", *connects)
			}
			var acts []interface{ GetVerb() string }
			for _, a := range typed.Actions() {
				acts = append(acts, a)
			}
			for _, a := range dyn.Actions() {
				acts = append(acts, a)
			}
			if w := writesOn(acts); len(w) > 0 {
				t.Errorf("a refused install wrote to the cluster first: %v", w)
			}
		})
	}

	// The accept path hands the clients back; an install without the flag never
	// looks for a class at all.
	for _, class := range []string{testSnapshotClass, ""} {
		dyn, typed := snapshotCluster(false,
			[]*unstructured.Unstructured{snapshotClassObject(testSnapshotClass, gke, "Delete")},
			storageClass("premium-rwo", gke, true))
		stubInstallClients(t, dyn, typed)
		st := aWritableState()
		st.BackupSnapshotClass = class
		gotDyn, gotTyped, err := connectAndRefuse(context.Background(), st, installSettingsFor(st),
			func(string) (bool, error) { return true, nil })
		if err != nil || gotDyn == nil || gotTyped == nil {
			t.Errorf("class %q: an install with nothing to refuse got (%v, %v, %v)", class, gotDyn, gotTyped, err)
		}
		if looked := len(dyn.Actions()) > 0; looked != (class != "") {
			t.Errorf("class %q: the VolumeSnapshotClass was read: %v", class, looked)
		}
	}
}

// What the chart's snapshot alerts are told, from what the instance apply reports. The
// output exists on every instance with backups -- null when snapshots are off -- so its
// PRESENCE must not read as "on".
func TestSnapshotAlertsFollowTheReportedClassNotTheOutputsPresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs map[string]tfexec.OutputMeta
		want    string
	}{
		{"a class", map[string]tfexec.OutputMeta{"database_backup_snapshot_class": {Value: json.RawMessage(`"pd-snapshots"`)}}, "true"},
		{"null (backups on, snapshots off)", map[string]tfexec.OutputMeta{"database_backup_snapshot_class": {Value: json.RawMessage(`null`)}}, "false"},
		{"an empty string", map[string]tfexec.OutputMeta{"database_backup_snapshot_class": {Value: json.RawMessage(`""`)}}, "false"},
		{"no such output", map[string]tfexec.OutputMeta{"database_backups_enabled": {Value: json.RawMessage(`true`)}}, "false"},
		{"an unreadable one", map[string]tfexec.OutputMeta{"database_backup_snapshot_class": {Value: json.RawMessage(`true`)}}, "false"},
	} {
		if got := backupSnapshotsFromOutputs(tc.outputs); got != tc.want {
			t.Errorf("%s: databaseBackupSnapshots = %q, want %q", tc.name, got, tc.want)
		}
	}
}
