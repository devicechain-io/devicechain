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

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dcctl/dcdir"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const wouldInstallOperator = "[dry-run] would install the operator"

// stubIdentifyCluster swaps the seam Install and its rehearsal identify the cluster
// through, and counts the reads.
func stubIdentifyCluster(t *testing.T, uid string, err error) *int {
	t.Helper()
	orig := identifyCluster
	t.Cleanup(func() { identifyCluster = orig })
	calls := 0
	identifyCluster = func(context.Context, string) (string, error) {
		calls++
		return uid, err
	}
	return &calls
}

// withLocalClusterState makes this machine hold the cluster root's state for uid, under
// a home directory of its own so the developer's ~/.devicechain is never read.
func withLocalClusterState(t *testing.T, uid string) {
	t.Helper()
	fakeHome(t)
	dir, err := dcdir.Cluster(uid)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, prereqStateSubdir, assets.ClusterRootDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "terraform.tfstate"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dryRunInstall runs the real Install as a dry run. The claim report reads through a
// dead kubeconfig (so a developer's cluster is never asked); the refusals read through
// the seams the caller stubbed.
func dryRunInstall(t *testing.T, opts InstallOptions) (string, error) {
	t.Helper()
	deadKubeconfig(t)
	// Bounded, and per call: the claim report asks the dead endpoint first and spends
	// whatever deadline it is given.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	opts.Options.DryRun = true
	opts.Options.KubeContext = "dead"
	opts.Options.ImageRegistry = "example.invalid/dc"
	opts.Options.ImageVersion = "v0.0.0-test"
	var err error
	out := captureOutput(t, func() {
		err = Install(ctx, &fakeProvider{name: "local", ensureBind: ClusterBinding{KubeContext: "dead"}}, opts)
	})
	return out, err
}

// installedCluster is a cluster serving the snapshot API and holding aCompleteInstall's
// record.
func installedCluster(t *testing.T) (*dynamicfake.FakeDynamicClient, *fake.Clientset) {
	t.Helper()
	dyn, typed := snapshotCluster(false, nil)
	if err := writeInstalled(context.Background(), typed, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	typed.ClearActions()
	return dyn, typed
}

func actionsOf(typed *fake.Clientset, dyn *dynamicfake.FakeDynamicClient) []interface{ GetVerb() string } {
	var acts []interface{ GetVerb() string }
	for _, a := range typed.Actions() {
		acts = append(acts, a)
	}
	for _, a := range dyn.Actions() {
		acts = append(acts, a)
	}
	return acts
}

// assertReadOnlyAndConnected guards against a vacuous "nothing was written": the
// rehearsal must have connected and read before its silence about writes means anything.
func assertReadOnlyAndConnected(t *testing.T, connects *int, dyn *dynamicfake.FakeDynamicClient, typed *fake.Clientset) {
	t.Helper()
	if *connects != 1 {
		t.Errorf("the dry run connected %d time(s), want 1", *connects)
	}
	if len(typed.Actions()) == 0 {
		t.Errorf("the dry run never read the install record")
	}
	if w := writesOn(actionsOf(typed, dyn)); len(w) > 0 {
		t.Errorf("the dry run wrote to the cluster: %v", w)
	}
}

func assertNoClusterRecord(t *testing.T, uid string) {
	t.Helper()
	dir, err := dcdir.Cluster(uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cluster.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dry run recorded the cluster locally (%v)", err)
	}
}

// A fixture that silently differs reads as a pass.
func requireFixtureMatchesTheInstallDefaults(t *testing.T) {
	t.Helper()
	if got, want := installSettingsFor(installState(ClusterBinding{}, "local", InstallOptions{})),
		aCompleteInstall().Settings; got != want {
		t.Fatalf("a default install's settings %+v are not aCompleteInstall's %+v; the cases below would refuse or pass for the wrong reason", got, want)
	}
}

// 🔴 A DRY RUN IS THE INSTALL MINUS THE WRITES. Over an installed cluster, the
// refusals a re-install makes were never reached by a dry run: the plan printed and
// the exit was 0 while the real run refused.
func TestADryRunInstallMakesTheReinstallRefusalsTheInstallMakes(t *testing.T) {
	requireFixtureMatchesTheInstallDefaults(t)
	const gke = "pd.csi.storage.gke.io"

	t.Run("changed settings under a running instance", func(t *testing.T) {
		withLocalClusterState(t, testClusterUID)
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := installedCluster(t)
		connects := stubInstallClients(t, dyn, typed)
		withClusterInstances(t, []string{"prod"}, nil)

		out, err := dryRunInstall(t, InstallOptions{NoMonitoring: true})
		if err == nil {
			t.Fatalf("a dry run that changes settings under a running instance returned nil:\n%s", out)
		}
		for _, want := range []string{"refusing to change cluster", "prod", "no monitoring"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q lacks %q", err, want)
			}
		}
		if strings.Contains(out, wouldInstallOperator) {
			t.Errorf("the plan was printed above a refusal:\n%s", out)
		}
		assertReadOnlyAndConnected(t, connects, dyn, typed)
		assertNoClusterRecord(t, testClusterUID)
	})

	t.Run("a lowered connection budget under a running instance", func(t *testing.T) {
		withLocalClusterState(t, testClusterUID)
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := installedCluster(t)
		connects := stubInstallClients(t, dyn, typed)
		withClusterInstances(t, []string{"prod"}, nil)

		_, err := dryRunInstall(t, InstallOptions{MaxConnections: 300})
		if err == nil || !strings.Contains(err.Error(), "connection budget 600 → 300") {
			t.Fatalf("a lowered connection budget was not refused for it: %v", err)
		}
		assertReadOnlyAndConnected(t, connects, dyn, typed)
	})

	t.Run("a database placement the instances were not built to", func(t *testing.T) {
		withLocalClusterState(t, testClusterUID)
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := installedCluster(t)
		connects := stubInstallClients(t, dyn, typed)
		withClusterInstances(t, []string{"prod"}, nil)
		stubListNodes(t, dbNodes(3, dedicated), nil)

		out, err := dryRunInstall(t, InstallOptions{DatabasePlacement: poolPlacement(t)})
		if err == nil {
			t.Fatalf("a dry run that moves the databases under a running instance returned nil:\n%s", out)
		}
		for _, want := range []string{"refusing to change cluster", "prod", "databases on"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q lacks %q", err, want)
			}
		}
		if strings.Contains(out, "would place the relational store") {
			t.Errorf("the placement was planned above a refusal:\n%s", out)
		}
		assertReadOnlyAndConnected(t, connects, dyn, typed)
	})

	t.Run("an install from another machine", func(t *testing.T) {
		fakeHome(t) // holds no state for the cluster
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := installedCluster(t)
		connects := stubInstallClients(t, dyn, typed)
		withClusterInstances(t, nil, nil)

		out, err := dryRunInstall(t, InstallOptions{})
		if err == nil || !strings.Contains(err.Error(), "another machine") ||
			!strings.Contains(err.Error(), testClusterUID) {
			t.Fatalf("a dry run from a machine without the cluster's state was not refused for it: %v", err)
		}
		if strings.Contains(out, wouldInstallOperator) {
			t.Errorf("the plan was printed above a refusal:\n%s", out)
		}
		assertReadOnlyAndConnected(t, connects, dyn, typed)
	})

	t.Run("an unusable snapshot class", func(t *testing.T) {
		fakeHome(t)
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := snapshotCluster(false, nil, storageClass("premium-rwo", gke, true))
		connects := stubInstallClients(t, dyn, typed)
		withClusterInstances(t, nil, nil)

		out, err := dryRunInstall(t, InstallOptions{BackupSnapshotClass: testSnapshotClass})
		if err == nil || !strings.Contains(err.Error(), `"pd-snapshots" does not exist`) {
			t.Fatalf("a dry run with a missing snapshot class was not refused for it: %v\n%s", err, out)
		}
		if *connects != 1 {
			t.Errorf("connected %d time(s), want 1", *connects)
		}
		if w := writesOn(actionsOf(typed, dyn)); len(w) > 0 {
			t.Errorf("the dry run wrote to the cluster: %v", w)
		}
	})

	t.Run("the refusal is the install's own text, unwrapped", func(t *testing.T) {
		withLocalClusterState(t, testClusterUID)
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := installedCluster(t)
		stubInstallClients(t, dyn, typed)
		withClusterInstances(t, []string{"prod"}, nil)

		_, err := dryRunInstall(t, InstallOptions{NoMonitoring: true})
		if err == nil || !strings.HasPrefix(err.Error(), "refusing to change cluster ") {
			t.Fatalf("the dry run's error is not the install's own text: %v", err)
		}
	})
}

// The counterweights: the rehearsal must not over-refuse, and a first install is still
// planned.
func TestADryRunInstallStillPlansWhatTheInstallWouldDo(t *testing.T) {
	requireFixtureMatchesTheInstallDefaults(t)
	const gke = "pd.csi.storage.gke.io"

	t.Run("a first install", func(t *testing.T) {
		fakeHome(t)
		stubIdentifyCluster(t, testClusterUID, nil)
		dyn, typed := snapshotCluster(false,
			[]*unstructured.Unstructured{snapshotClassObject(testSnapshotClass, gke, "Delete")},
			storageClass("premium-rwo", gke, true))
		connects := stubInstallClients(t, dyn, typed)
		instances := withClusterInstances(t, []string{"never"}, nil)

		out, err := dryRunInstall(t, InstallOptions{BackupSnapshotClass: testSnapshotClass})
		if err != nil {
			t.Fatalf("a first install's dry run failed: %v", err)
		}
		if !strings.Contains(out, wouldInstallOperator) {
			t.Errorf("a first install's dry run printed no plan:\n%s", out)
		}
		if !strings.Contains(out, "checking VolumeSnapshotClass "+testSnapshotClass) {
			t.Errorf("the snapshot class was not checked on an identified cluster:\n%s", out)
		}
		if strings.Contains(out, "NOT rehearsed") {
			t.Errorf("a rehearsed check was reported as not rehearsed:\n%s", out)
		}
		if *connects != 1 || *instances != 0 {
			t.Errorf("connects=%d instance-reads=%d, want 1 and 0 (no record means nothing to hurt)", *connects, *instances)
		}
		if w := writesOn(actionsOf(typed, dyn)); len(w) > 0 {
			t.Errorf("the dry run wrote to the cluster: %v", w)
		}
	})

	for _, tc := range []struct {
		name string
		opts InstallOptions
	}{
		{"the same settings again", InstallOptions{}},
		{"a raised connection budget", InstallOptions{MaxConnections: 900}},
	} {
		t.Run(tc.name+" under a running instance", func(t *testing.T) {
			withLocalClusterState(t, testClusterUID)
			stubIdentifyCluster(t, testClusterUID, nil)
			dyn, typed := installedCluster(t)
			connects := stubInstallClients(t, dyn, typed)
			withClusterInstances(t, []string{"prod"}, nil)

			out, err := dryRunInstall(t, tc.opts)
			if err != nil {
				t.Fatalf("a harmless re-install was refused by its dry run: %v", err)
			}
			if !strings.Contains(out, wouldInstallOperator) {
				t.Errorf("no plan was printed:\n%s", out)
			}
			assertReadOnlyAndConnected(t, connects, dyn, typed)
		})
	}
}

// A dry run that cannot reach the cluster says what it did not check, and plans.
func TestADryRunThatCannotIdentifyTheClusterSaysWhatItDidNotCheck(t *testing.T) {
	fakeHome(t)
	identifies := stubIdentifyCluster(t, "", errors.New("no such context"))
	dyn, typed := snapshotCluster(false, nil)
	connects := stubInstallClients(t, dyn, typed)
	instances := withClusterInstances(t, []string{"prod"}, nil)

	out, err := dryRunInstall(t, InstallOptions{BackupSnapshotClass: testSnapshotClass})
	if err != nil {
		t.Fatalf("a dry run aimed at a cluster that cannot be identified failed: %v", err)
	}
	for _, want := range []string{
		"were NOT rehearsed",
		"NOT rehearsed, because the cluster could not be identified",
		wouldInstallOperator,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if *identifies != 1 || *connects != 0 || *instances != 0 {
		t.Errorf("identifies=%d connects=%d instance-reads=%d, want 1, 0, 0", *identifies, *connects, *instances)
	}
}

// What the cluster ANSWERS is fatal on the real run, so it is fatal on the rehearsal;
// only failing to reach it is softened.
func TestADryRunTakesAnIdentityTheClusterRefusedAsTheRealRunDoes(t *testing.T) {
	fakeHome(t)
	stubIdentifyCluster(t, "", apierrors.NewForbidden(
		schema.GroupResource{Resource: "namespaces"}, "kube-system", errors.New("rbac")))

	out, err := dryRunInstall(t, InstallOptions{})
	if err == nil || !apierrors.IsForbidden(err) {
		t.Fatalf("an identity read the cluster refused was softened: %v\n%s", err, out)
	}
	if strings.Contains(out, wouldInstallOperator) {
		t.Errorf("a plan was printed above an identity refusal:\n%s", out)
	}
}

func TestACancelledRehearsalIsAnErrorNotAPlan(t *testing.T) {
	fakeHome(t)
	stubIdentifyCluster(t, "", errors.New("context canceled"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	st := &State{KubeContext: "x", Values: map[string]string{}}
	rehearsed, err := rehearseInstallRefusals(ctx, st, InstallSettings{}, stateHere())
	if err == nil || rehearsed {
		t.Fatalf("a cancelled rehearsal returned (%v, %v), want an error", rehearsed, err)
	}
}
