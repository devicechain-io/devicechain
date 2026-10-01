// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/controllers"
)

// cnpgChartDir is the chart the cnpg-cluster OpenTofu module applies. It is read
// from outside this module, which Go's test cache does not track: run with
// -count=1 (CI does) or an edit to the chart can be answered by a cached PASS.
var cnpgChartDir = filepath.Join("..", "..", "..", "deploy", "opentofu", "modules", "cnpg-cluster", "chart")

// 🔴 THE PRUNER MUST SELECT WHAT HELM APPLIES, NOT WHAT THE CHART RENDERS. The
// chart reaches the cluster through a Helm release, and Helm writes its own
// metadata over the template's on every object it installs or upgrades -- among
// it app.kubernetes.io/managed-by, which it sets to "Helm" whatever the template
// said. An earlier pruner selected on the rendered managed-by value; every test
// built the schedule by hand with that value and the chart check read the
// template, so all of them agreed with it, and on a real install it matched
// nothing: no snapshot was ever pruned.
//
// So this applies the REAL chart with the Helm SDK into a real API server, through
// both paths a snapshot schedule reaches a cluster by -- an install with snapshots
// on (a new instance's event store) and an upgrade that turns them on (an existing
// relational store) -- and runs the operator's own Pass over what Helm left there.
// It restates no selector: the only definition of what is selected is the operator's.
func TestSnapshotRetentionSelectsWhatHelmApplies(t *testing.T) {
	c, env := startAPIServer(t, true)
	kubeconfig := helmKubeconfig(t, env)
	day := 24 * time.Hour
	now := time.Now().Add(30 * day).Truncate(time.Second)

	mkNamespace(t, c, "dc-system", map[string]string{dcv1beta1.ComponentLabel: dcv1beta1.InfrastructureComponent})
	mkNamespace(t, c, "dci-s4", map[string]string{dcv1beta1.InstanceNamespaceLabel: "s4"})

	type store struct{ ns, name string }
	rdb, tsdb := store{"dc-system", "dc-rdb"}, store{"dci-s4", "dc-tsdb"}

	// The relational store: installed with object-store backups only, then upgraded
	// to take volume snapshots -- the path an existing install takes.
	helmApply(t, kubeconfig, rdb.ns, rdb.name, cnpgValues(rdb.name, "devicechain-rdb", ""), false)
	rel := helmApply(t, kubeconfig, rdb.ns, rdb.name, cnpgValues(rdb.name, "devicechain-rdb", "pd-snapshots"), true)
	requireHelmApplied(t, c, rdb.ns, rdb.name+"-snapshot", rel)

	// An instance's event store, installed with snapshots on.
	rel = helmApply(t, kubeconfig, tsdb.ns, tsdb.name, cnpgValues(tsdb.name, "devicechain-tsdb", "pd-snapshots"), false)
	requireHelmApplied(t, c, tsdb.ns, tsdb.name+"-snapshot", rel)

	for _, s := range []store{rdb, tsdb} {
		for suffix, age := range map[string]time.Duration{"-d1": day, "-d8": 8 * day, "-d20": 20 * day} {
			backup(t, c, s.ns, s.name+suffix, s.name+"-snapshot", s.name, "volumeSnapshot", "completed", now.Add(-age))
		}
	}

	// One pass over both, as the operator runs it: Pass is cluster-wide.
	rec := events.NewFakeRecorder(100)
	p := &controllers.SnapshotRetention{Reader: c, Writer: c, Recorder: rec, Now: func() time.Time { return now }}
	if err := p.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	evs := drain(rec)

	want := now.UTC().Format(time.RFC3339)
	for _, s := range []store{rdb, tsdb} {
		// d20 is outside the 7-day window; d8 is the newest before it, and stays.
		if got, w := strings.Join(backupNames(t, c, s.ns), ","), s.name+"-d1,"+s.name+"-d8"; got != w {
			t.Errorf("Backups left in %s: %s, want %s -- the pruner did not prune the schedule Helm applied",
				s.ns, got, w)
		}
		if got := checkedAt(t, c, s.ns, s.name+"-snapshot"); got != want {
			t.Errorf("ScheduledBackup %s/%s-snapshot records a pass at %q, want %q -- the pruner did not "+
				"select the schedule Helm applied", s.ns, s.name, got, want)
		}
		// The object-store schedule beside it carries no retention label: it is
		// CloudNativePG's to keep, and must not be stamped as if the pruner owned it.
		if got := checkedAt(t, c, s.ns, s.name+"-backup"); got != "" {
			t.Errorf("ScheduledBackup %s/%s-backup is the object-store schedule, but records a pass at %q",
				s.ns, s.name, got)
		}
		var pruned []string
		for _, e := range evs {
			if strings.Contains(e, "SnapshotBackupPruned") && strings.Contains(e, " "+s.name+"-") {
				pruned = append(pruned, e)
			}
		}
		if len(pruned) != 1 || !strings.Contains(pruned[0], "deleted Backup "+s.name+"-d20 ") {
			t.Errorf("SnapshotBackupPruned events for %s: %q, want exactly one, deleting %s-d20", s.name, pruned, s.name)
		}
	}
}

// requireHelmApplied is the harness's own positive control: this test is worth
// something only while Helm really rewrites the label the template rendered. It
// checks that the RENDERED snapshot schedule does not say managed-by "Helm" and the
// APPLIED one does -- so the value can only have come from Helm's apply -- and that
// what was applied is a volume-snapshot schedule carrying the module's window.
func requireHelmApplied(t *testing.T, c client.Client, ns, name string, rel *release.Release) {
	t.Helper()
	var rendered map[string]any
	for _, doc := range strings.Split(rel.Manifest, "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("release %s: parsing a rendered document: %v", rel.Name, err)
		}
		u := unstructured.Unstructured{Object: obj}
		if obj != nil && u.GetKind() == "ScheduledBackup" && u.GetName() == name {
			rendered = obj
		}
	}
	if rendered == nil {
		t.Fatalf("release %s rendered no ScheduledBackup %s; the chart no longer renders a snapshot "+
			"schedule for these values", rel.Name, name)
	}
	if got := (&unstructured.Unstructured{Object: rendered}).GetLabels()["app.kubernetes.io/managed-by"]; got == "Helm" {
		t.Fatalf("the chart RENDERS managed-by=%q on %s, so this test cannot tell Helm's apply-time "+
			"rewrite from the template", got, name)
	}

	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(scheduledBackupGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, sb); err != nil {
		t.Fatalf("reading the ScheduledBackup Helm applied, %s/%s: %v", ns, name, err)
	}
	if got := sb.GetLabels()["app.kubernetes.io/managed-by"]; got != "Helm" {
		t.Fatalf("Helm applied managed-by=%q on %s/%s; this test exists to run the pruner against what "+
			"Helm applies, and this harness no longer reproduces it", got, ns, name)
	}
	if got, _, _ := unstructured.NestedString(sb.Object, "spec", "method"); got != "volumeSnapshot" {
		t.Fatalf("%s/%s has method %q, want volumeSnapshot", ns, name, got)
	}
	if got := sb.GetLabels()[controllers.SnapshotRetentionLabel]; got != "7d" {
		t.Fatalf("%s/%s carries %s=%q, want the module's 7d", ns, name, controllers.SnapshotRetentionLabel, got)
	}
}

// helmKubeconfig writes a kubeconfig for an administrator of the test API server,
// which is how Helm reaches a cluster.
func helmKubeconfig(t *testing.T, env *envtest.Environment) string {
	t.Helper()
	u, err := env.AddUser(envtest.User{Name: "helm", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatalf("adding a Helm user to the test API server: %v", err)
	}
	b, err := u.KubeConfig()
	if err != nil {
		t.Fatalf("building Helm's kubeconfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// helmApply installs, or upgrades, a release of the cnpg-cluster chart with the
// Helm SDK, storing releases in Secrets as the module's provider does.
func helmApply(t *testing.T, kubeconfig, namespace, name string, values map[string]any, upgrade bool) *release.Release {
	t.Helper()
	flags := genericclioptions.NewConfigFlags(false)
	// A discovery cache per test: envtest's port changes every run.
	cache := t.TempDir()
	flags.KubeConfig, flags.Namespace, flags.CacheDir = &kubeconfig, &namespace, &cache
	cfg := new(action.Configuration)
	if err := cfg.Init(flags, namespace, "secret", t.Logf); err != nil {
		t.Fatalf("initialising Helm: %v", err)
	}
	chrt, err := loader.Load(cnpgChartDir)
	if err != nil {
		t.Fatalf("loading the cnpg-cluster chart from %s: %v", cnpgChartDir, err)
	}
	ctx := context.Background()
	var rel *release.Release
	if upgrade {
		a := action.NewUpgrade(cfg)
		a.Namespace = namespace
		rel, err = a.RunWithContext(ctx, name, chrt, values)
	} else {
		a := action.NewInstall(cfg)
		a.ReleaseName, a.Namespace = name, namespace
		rel, err = a.RunWithContext(ctx, chrt, values)
	}
	if err != nil {
		t.Fatalf("helm (upgrade=%t) %s/%s: %v", upgrade, namespace, name, err)
	}
	return rel
}

// cnpgValues is a values set the cnpg-cluster module could pass: a store with
// object-store backups on a seven-day window, taking its daily base backup as a
// volume snapshot when snapshotClass is set.
func cnpgValues(name, bucket, snapshotClass string) map[string]any {
	b := map[string]any{
		"enabled":            true,
		"bucket":             bucket,
		"endpointURL":        "http://dc-object-store.dc-system:9000",
		"credentialsSecret":  "dc-object-store-credentials",
		"accessKeyIdKey":     "MINIO_ROOT_USER",
		"secretAccessKeyKey": "MINIO_ROOT_PASSWORD",
		"retentionPolicy":    "7d",
	}
	if snapshotClass != "" {
		b["snapshotClass"] = snapshotClass
	}
	return map[string]any{
		"name":             name,
		"imageName":        "ghcr.io/example/postgres:17",
		"instances":        1,
		"aliasServiceName": "dc-postgresql",
		"storage":          map[string]any{"size": "8Gi"},
		"bootstrap": map[string]any{
			"database":   "dc",
			"owner":      "devicechain",
			"secretName": name + "-app-credentials",
		},
		"backup": b,
	}
}
