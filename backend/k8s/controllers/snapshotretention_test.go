// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/controllers"
)

// Snapshot retention against a REAL API server: label selection, a delete
// conditional on the UID that was read, and the merge patch that records a pass are
// the API server's behaviour, and a fake client would be testing its own idea of
// them. CloudNativePG's kinds are served from minimal CRDs (testdata/cnpg-crds).

var (
	backupGVK          = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Backup"}
	scheduledBackupGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ScheduledBackup"}
)

// apiServer starts an API server with the DeviceChain CRDs, and CloudNativePG's
// kinds when withCNPG. Like withAPIServer, it fails rather than skips when the
// envtest binaries are absent.
func apiServer(t *testing.T, withCNPG bool) client.Client {
	t.Helper()
	c, _ := startAPIServer(t, withCNPG)
	return c
}

// startAPIServer is apiServer, also returning the environment, for a test that
// needs to reach the server as something other than this client (Helm does).
func startAPIServer(t *testing.T, withCNPG bool) (client.Client, *envtest.Environment) {
	t.Helper()
	dirs := []string{filepath.Join("..", "config", "crd", "bases")}
	if withCNPG {
		dirs = append(dirs, filepath.Join("testdata", "cnpg-crds"))
	}
	env := &envtest.Environment{CRDDirectoryPaths: dirs, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v (KUBEBUILDER_ASSETS=%q; see withAPIServer for how "+
			"to install the binaries)", err, os.Getenv("KUBEBUILDER_ASSETS"))
	}
	t.Cleanup(func() { _ = env.Stop() })
	if err := dcv1beta1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c, env
}

func mkNamespace(t *testing.T, c client.Client, name string, labels map[string]string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating namespace %s: %v", name, err)
	}
}

// snapshotSchedule is a ScheduledBackup with the labels the cnpg-cluster chart
// renders on its volume-snapshot schedule; labels overrides them, an empty value
// deleting one. What Helm adds when it applies the chart is
// TestSnapshotRetentionSelectsWhatHelmApplies's job, not this fixture's: a
// hand-written copy of Helm's behaviour is a model of Helm, and a model is what let
// an earlier selector on a label Helm rewrites pass every test here.
func snapshotSchedule(t *testing.T, c client.Client, ns, name, cluster, window string, labels map[string]string) {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"cluster":  map[string]any{"name": cluster},
			"schedule": "0 0 3 * * *",
			"method":   "volumeSnapshot",
		},
	}}
	u.SetGroupVersionKind(scheduledBackupGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	l := map[string]string{
		"app.kubernetes.io/component":      "database-snapshot-backup",
		controllers.SnapshotRetentionLabel: window,
	}
	for k, v := range labels {
		if v == "" {
			delete(l, k)
		} else {
			l[k] = v
		}
	}
	u.SetLabels(l)
	if err := c.Create(context.Background(), u); err != nil {
		t.Fatalf("creating ScheduledBackup %s/%s: %v", ns, name, err)
	}
}

// setScheduleMethod rewrites a ScheduledBackup's spec.method, for a schedule that
// carries the snapshot labels but does not take snapshots.
func setScheduleMethod(t *testing.T, c client.Client, ns, name, method string) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(scheduledBackupGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		t.Fatalf("reading ScheduledBackup %s/%s: %v", ns, name, err)
	}
	if err := unstructured.SetNestedField(u.Object, method, "spec", "method"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), u); err != nil {
		t.Fatalf("setting the method of ScheduledBackup %s/%s: %v", ns, name, err)
	}
}

// backup creates a CloudNativePG Backup as a ScheduledBackup would, labelled with
// its schedule (or not, when schedule is empty).
func backup(t *testing.T, c client.Client, ns, name, schedule, cluster, method, phase string, stopped time.Time) {
	t.Helper()
	status := map[string]any{"phase": phase}
	if !stopped.IsZero() {
		status["stoppedAt"] = stopped.UTC().Format(time.RFC3339)
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"cluster": map[string]any{"name": cluster},
			"method":  method,
		},
		"status": status,
	}}
	u.SetGroupVersionKind(backupGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	if schedule != "" {
		u.SetLabels(map[string]string{"cnpg.io/scheduled-backup": schedule, "cnpg.io/cluster": cluster})
	}
	if err := c.Create(context.Background(), u); err != nil {
		t.Fatalf("creating Backup %s/%s: %v", ns, name, err)
	}
}

func backupNames(t *testing.T, c client.Client, ns string) []string {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "BackupList"})
	if err := c.List(context.Background(), list, client.InNamespace(ns)); err != nil {
		t.Fatalf("listing Backups in %s: %v", ns, err)
	}
	var names []string
	for _, b := range list.Items {
		names = append(names, b.GetName())
	}
	sort.Strings(names)
	return names
}

func checkedAt(t *testing.T, c client.Client, ns, name string) string {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(scheduledBackupGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		t.Fatalf("reading ScheduledBackup %s/%s: %v", ns, name, err)
	}
	return u.GetAnnotations()[controllers.SnapshotRetentionCheckedAnnotation]
}

func drain(r *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-r.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestSnapshotRetentionPrunesOnlyItsOwnSnapshotsToTheWindow(t *testing.T) {
	c := apiServer(t, true)
	day := 24 * time.Hour
	// The pass runs a month from now, so the Backups the API server stamps as
	// created now are a month old to it -- which is what lets the failed-and-old
	// case be written against a server that will not take a creationTimestamp.
	now := time.Now().Add(30 * day).Truncate(time.Second)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	mkNamespace(t, c, "dc-system", map[string]string{dcv1beta1.ComponentLabel: dcv1beta1.InfrastructureComponent})
	mkNamespace(t, c, "dci-acme", map[string]string{dcv1beta1.InstanceNamespaceLabel: "acme"})
	mkNamespace(t, c, "someone-else", nil)

	// The relational store's schedule, 7 days -- carrying the managed-by label as Helm
	// applies it, while the event store's below carries none, as the chart renders
	// it. Which schedules are pruned depends on neither.
	snapshotSchedule(t, c, "dc-system", "dc-rdb-snapshot", "dc-rdb", "7d",
		map[string]string{"app.kubernetes.io/managed-by": "Helm"})
	for name, stopped := range map[string]time.Duration{"rdb-d1": day, "rdb-d6": 6 * day, "rdb-d8": 8 * day,
		"rdb-d9": 9 * day, "rdb-d20": 20 * day} {
		backup(t, c, "dc-system", name, "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed", ago(stopped))
	}
	backup(t, c, "dc-system", "rdb-failed-old", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "failed", time.Time{})
	backup(t, c, "dc-system", "rdb-running-old", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "running", time.Time{})
	// Old, and each not this schedule's snapshot in one way: an object-store backup
	// carrying the schedule's label, another Cluster's snapshot carrying it, and a
	// snapshot of this Cluster from no schedule at all.
	backup(t, c, "dc-system", "rdb-plugin-old", "dc-rdb-snapshot", "dc-rdb", "plugin", "completed", ago(20*day))
	backup(t, c, "dc-system", "other-cluster-old", "dc-rdb-snapshot", "dc-other", "volumeSnapshot", "completed", ago(20*day))
	backup(t, c, "dc-system", "by-hand-old", "", "dc-rdb", "volumeSnapshot", "completed", ago(20*day))

	// An instance's event store, two weeks.
	snapshotSchedule(t, c, "dci-acme", "dc-tsdb-snapshot", "dc-tsdb", "2w", nil)
	for name, stopped := range map[string]time.Duration{"tsdb-d13": 13 * day, "tsdb-d15": 15 * day,
		"tsdb-d16": 16 * day} {
		backup(t, c, "dci-acme", name, "dc-tsdb-snapshot", "dc-tsdb", "volumeSnapshot", "completed", ago(stopped))
	}

	// 🔴 A CloudNativePG user's own schedule, with every label copied, in a namespace
	// DeviceChain does not own. The operator's ClusterRole reaches it; the operator
	// must not.
	snapshotSchedule(t, c, "someone-else", "their-snapshot", "theirs", "1d", nil)
	backup(t, c, "someone-else", "theirs-old", "their-snapshot", "theirs", "volumeSnapshot", "completed", ago(20*day))
	backup(t, c, "someone-else", "theirs-older", "their-snapshot", "theirs", "volumeSnapshot", "completed", ago(21*day))

	// A schedule carrying the retention label but not the chart's component label.
	snapshotSchedule(t, c, "dci-acme", "not-the-charts", "dc-tsdb", "1d",
		map[string]string{"app.kubernetes.io/component": ""})
	backup(t, c, "dci-acme", "not-the-charts-old", "not-the-charts", "dc-tsdb", "volumeSnapshot", "completed", ago(20*day))
	backup(t, c, "dci-acme", "not-the-charts-older", "not-the-charts", "dc-tsdb", "volumeSnapshot", "completed", ago(21*day))

	// The chart's component label but no retention label: not a schedule the pruner
	// was told a window for, so it is not one it has finished. Read as an empty
	// window it would delete nothing, but its stamp would silence the alert.
	snapshotSchedule(t, c, "dci-acme", "no-window", "dc-tsdb", "",
		map[string]string{controllers.SnapshotRetentionLabel: ""})

	// Both labels, but a schedule of object-store backups: the window means nothing
	// to it, so it is reported and never stamped -- a stamp would say a pass pruned
	// it. Its old Backup is of its own method, and stays.
	snapshotSchedule(t, c, "dci-acme", "plugin-schedule", "dc-tsdb", "1d", nil)
	setScheduleMethod(t, c, "dci-acme", "plugin-schedule", "plugin")
	backup(t, c, "dci-acme", "plugin-schedule-old", "plugin-schedule", "dc-tsdb", "plugin", "completed", ago(20*day))

	rec := events.NewFakeRecorder(100)
	p := &controllers.SnapshotRetention{Reader: c, Writer: c, Recorder: rec, Now: func() time.Time { return now }}
	if err := p.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	for _, tc := range []struct {
		ns   string
		want []string
	}{
		// rdb-d8 is the newest before the window, and stays; the failed one is a
		// month old to the pass; the running one is never touched.
		{"dc-system", []string{"by-hand-old", "other-cluster-old", "rdb-d1", "rdb-d6", "rdb-d8",
			"rdb-plugin-old", "rdb-running-old"}},
		{"dci-acme", []string{"not-the-charts-old", "not-the-charts-older", "plugin-schedule-old", "tsdb-d13",
			"tsdb-d15"}},
		{"someone-else", []string{"theirs-old", "theirs-older"}},
	} {
		if got := backupNames(t, c, tc.ns); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("Backups left in %s: %v, want %v", tc.ns, got, tc.want)
		}
	}

	// The pass is recorded on the schedules it finished, with the pass's own time,
	// and on nothing else.
	want := now.UTC().Format(time.RFC3339)
	for _, sb := range [][2]string{{"dc-system", "dc-rdb-snapshot"}, {"dci-acme", "dc-tsdb-snapshot"}} {
		if got := checkedAt(t, c, sb[0], sb[1]); got != want {
			t.Errorf("ScheduledBackup %s/%s records a pass at %q, want %q", sb[0], sb[1], got, want)
		}
	}
	for _, sb := range [][2]string{{"someone-else", "their-snapshot"}, {"dci-acme", "not-the-charts"},
		{"dci-acme", "no-window"}, {"dci-acme", "plugin-schedule"}} {
		if got := checkedAt(t, c, sb[0], sb[1]); got != "" {
			t.Errorf("ScheduledBackup %s/%s is not a snapshot schedule the pass pruned, but records a pass at %q",
				sb[0], sb[1], got)
		}
	}

	var pruned, ignored []string
	for _, e := range drain(rec) {
		switch {
		case strings.Contains(e, "SnapshotBackupPruned"):
			pruned = append(pruned, e)
		case strings.Contains(e, "SnapshotRetentionIgnored"):
			ignored = append(ignored, e)
		}
	}
	if len(pruned) != 4 {
		t.Errorf("%d SnapshotBackupPruned events, want 4 (rdb-d9, rdb-d20, rdb-failed-old, tsdb-d16): %v",
			len(pruned), pruned)
	}
	if len(ignored) != 1 || !strings.Contains(ignored[0], `"plugin"`) {
		t.Errorf("SnapshotRetentionIgnored events %v, want exactly one, for plugin-schedule's method", ignored)
	}

	// A second pass over what is left deletes nothing more.
	if err := p.Pass(context.Background()); err != nil {
		t.Fatalf("second Pass: %v", err)
	}
	if got := backupNames(t, c, "dc-system"); len(got) != 7 {
		t.Errorf("a second pass changed dc-system: %v", got)
	}
}

// A window the pruner cannot read deletes nothing and records no pass -- the
// missing record is what raises the alert.
func TestSnapshotRetentionRefusesAWindowItCannotRead(t *testing.T) {
	c := apiServer(t, true)
	now := time.Now().Add(30 * 24 * time.Hour)
	mkNamespace(t, c, "dc-system", map[string]string{dcv1beta1.ComponentLabel: dcv1beta1.InfrastructureComponent})
	snapshotSchedule(t, c, "dc-system", "dc-rdb-snapshot", "dc-rdb", "seven", nil)
	for _, name := range []string{"a", "b", "c"} {
		backup(t, c, "dc-system", name, "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed",
			now.Add(-20*24*time.Hour))
	}

	rec := events.NewFakeRecorder(10)
	p := &controllers.SnapshotRetention{Reader: c, Writer: c, Recorder: rec, Now: func() time.Time { return now }}
	if err := p.Pass(context.Background()); err == nil {
		t.Error("a pass over an unreadable window reported success")
	}
	if got := backupNames(t, c, "dc-system"); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("an unreadable window deleted Backups: %v left, want a,b,c", got)
	}
	if got := checkedAt(t, c, "dc-system", "dc-rdb-snapshot"); got != "" {
		t.Errorf("an unreadable window recorded a pass at %q", got)
	}
	if ev := strings.Join(drain(rec), "\n"); !strings.Contains(ev, "SnapshotRetentionInvalid") {
		t.Errorf("no SnapshotRetentionInvalid event: %q", ev)
	}
}

// An empty window keeps every snapshot, as an empty retentionPolicy keeps every
// object-store backup, and still records the pass.
func TestSnapshotRetentionEmptyWindowKeepsEverything(t *testing.T) {
	c := apiServer(t, true)
	now := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	mkNamespace(t, c, "dc-system", map[string]string{dcv1beta1.ComponentLabel: dcv1beta1.InfrastructureComponent})
	snapshotSchedule(t, c, "dc-system", "dc-rdb-snapshot", "dc-rdb", "", nil)
	backup(t, c, "dc-system", "old", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed", now.Add(-90*24*time.Hour))
	backup(t, c, "dc-system", "older", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed", now.Add(-91*24*time.Hour))

	p := &controllers.SnapshotRetention{Reader: c, Writer: c, Recorder: events.NewFakeRecorder(10),
		Now: func() time.Time { return now }}
	if err := p.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if got := backupNames(t, c, "dc-system"); strings.Join(got, ",") != "old,older" {
		t.Errorf("an empty window deleted Backups: %v left", got)
	}
	if got, want := checkedAt(t, c, "dc-system", "dc-rdb-snapshot"), now.UTC().Format(time.RFC3339); got != want {
		t.Errorf("recorded a pass at %q, want %q", got, want)
	}
}

// A cluster installed without CloudNativePG has nothing to prune, and that is not
// an error: the operator runs on such clusters too.
func TestSnapshotRetentionWithoutCloudNativePG(t *testing.T) {
	c := apiServer(t, false)
	p := &controllers.SnapshotRetention{Reader: c, Writer: c, Recorder: events.NewFakeRecorder(1), Now: time.Now}
	if err := p.Pass(context.Background()); err != nil {
		t.Errorf("a pass on a cluster without CloudNativePG failed: %v", err)
	}
}

// replacingWriter stands in for a second replica, an operator or CloudNativePG acting
// between the pruner's list and its delete: before forwarding the delete of `name`, it
// deletes that Backup itself and creates a new one under the same name.
type replacingWriter struct {
	client.Client
	t        *testing.T
	name     string
	replaced bool
}

func (w *replacingWriter) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if obj.GetName() == w.name && !w.replaced {
		w.replaced = true
		old := &unstructured.Unstructured{}
		old.SetGroupVersionKind(backupGVK)
		if err := w.Client.Get(ctx, client.ObjectKeyFromObject(obj), old); err != nil {
			w.t.Fatalf("reading %s before replacing it: %v", w.name, err)
		}
		if err := w.Client.Delete(ctx, old); err != nil {
			w.t.Fatalf("deleting %s to replace it: %v", w.name, err)
		}
		fresh := &unstructured.Unstructured{Object: map[string]any{
			"spec":   old.Object["spec"],
			"status": map[string]any{"phase": "running"},
		}}
		fresh.SetGroupVersionKind(backupGVK)
		fresh.SetNamespace(old.GetNamespace())
		fresh.SetName(old.GetName())
		fresh.SetLabels(old.GetLabels())
		if err := w.Client.Create(ctx, fresh); err != nil {
			w.t.Fatalf("recreating %s: %v", w.name, err)
		}
	}
	return w.Client.Delete(ctx, obj, opts...)
}

// 🔴 A DELETE IS OF THE BACKUP THAT WAS READ, NOT OF WHATEVER NOW HAS ITS NAME. Between
// the pruner's list and its delete, the old Backup goes and a new one takes its name.
// The delete must leave the new one alone -- it is not the snapshot the pass judged
// old -- and must count as done, so the pass still finishes and records itself.
func TestSnapshotRetentionDeletesOnlyTheBackupItRead(t *testing.T) {
	c := apiServer(t, true)
	day := 24 * time.Hour
	now := time.Now().Add(30 * day).Truncate(time.Second)
	mkNamespace(t, c, "dc-system", map[string]string{dcv1beta1.ComponentLabel: dcv1beta1.InfrastructureComponent})
	snapshotSchedule(t, c, "dc-system", "dc-rdb-snapshot", "dc-rdb", "7d", nil)
	backup(t, c, "dc-system", "rdb-d1", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed", now.Add(-day))
	backup(t, c, "dc-system", "rdb-d8", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed", now.Add(-8*day))
	backup(t, c, "dc-system", "rdb-d9", "dc-rdb-snapshot", "dc-rdb", "volumeSnapshot", "completed", now.Add(-9*day))

	w := &replacingWriter{Client: c, t: t, name: "rdb-d9"}
	p := &controllers.SnapshotRetention{Reader: c, Writer: w, Recorder: events.NewFakeRecorder(10),
		Now: func() time.Time { return now }}
	if err := p.Pass(context.Background()); err != nil {
		t.Fatalf("a pass whose Backup was replaced under it failed: %v", err)
	}
	if !w.replaced {
		t.Fatal("the pass never deleted rdb-d9, so this test replaced nothing")
	}
	if got := backupNames(t, c, "dc-system"); strings.Join(got, ",") != "rdb-d1,rdb-d8,rdb-d9" {
		t.Errorf("Backups left: %v, want rdb-d1,rdb-d8,rdb-d9 -- the new rdb-d9 is not the one the pass read", got)
	}
	if got, want := checkedAt(t, c, "dc-system", "dc-rdb-snapshot"), now.UTC().Format(time.RFC3339); got != want {
		t.Errorf("the pass recorded itself at %q, want %q", got, want)
	}
}
