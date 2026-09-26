// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func objectStoreOutputs(value string) map[string]tfexec.OutputMeta {
	return map[string]tfexec.OutputMeta{"backup_object_store_deployment": {Value: []byte(value)}}
}

const inClusterStore = `{"in_cluster":true,"namespace":"dc-system","name":"dc-object-store"}`

const noInClusterStore = `{"in_cluster":false,"namespace":"","name":""}`

// objectStoreDeployment is the object store in a given rollout state; see deployment.
func objectStoreDeployment(generation, observed int64, updated, replicas, available int32) *appsv1.Deployment {
	d := deployment(generation, observed, 1, updated, replicas, available)
	d.Name, d.Namespace = "dc-object-store", "dc-system"
	return d
}

func clientsFor(c kubernetes.Interface) func() (kubernetes.Interface, error) {
	return func() (kubernetes.Interface, error) { return c, nil }
}

// TestAStoreWhoseUpdateNeverRolledOutFailsTheInstall is the state a timed-out UPDATE
// leaves behind: the new template has been observed, its pod was created and is not
// available (an image that cannot be pulled), and the old pod is gone (the store is
// Recreate). The provider recorded the new spec anyway, so the re-run's apply plans no
// change and succeeds; this check is what keeps the install from being recorded over it.
func TestAStoreWhoseUpdateNeverRolledOutFailsTheInstall(t *testing.T) {
	client := fake.NewSimpleClientset(objectStoreDeployment(2, 2, 1, 1, 0))
	err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(inClusterStore),
		clientsFor(client), 20*time.Millisecond)
	if err == nil {
		t.Fatal("an object store whose new pod never became available was accepted")
	}
	for _, want := range []string{"dc-system/dc-object-store", "0 available", "run the same install again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%s", want, err)
		}
	}
}

// The counterweight: a check that refused everything would pass the case above.
func TestARolledOutStorePasses(t *testing.T) {
	client := fake.NewSimpleClientset(objectStoreDeployment(2, 2, 1, 1, 1))
	if err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(inClusterStore),
		clientsFor(client), 20*time.Millisecond); err != nil {
		t.Fatalf("a rolled-out object store was refused: %v", err)
	}
}

// A store the output names but the cluster does not hold is not a store that rolled out.
func TestANamedStoreThatDoesNotExistFails(t *testing.T) {
	if err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(inClusterStore),
		clientsFor(fake.NewSimpleClientset()), 20*time.Millisecond); err == nil {
		t.Fatal("an absent object store Deployment was accepted")
	}
}

// in_cluster false means this cluster runs no in-cluster store (backups off, or an
// external destination): nothing to wait for, and no reason to build kube clients.
func TestNoInClusterStoreNeedsNoCheck(t *testing.T) {
	built := false
	err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(noInClusterStore),
		func() (kubernetes.Interface, error) {
			built = true
			return nil, errors.New("no cluster in this test")
		}, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("a cluster with no in-cluster store was refused: %v", err)
	}
	if built {
		t.Error("kube clients were built for a cluster with no store to check")
	}
}

// 🔴 A MISSING OUTPUT IS NOT A NULL ONE. Read as "no store", a root that stopped
// declaring it would switch the check off on every cluster without a failure.
func TestAMissingStoreOutputFailsRatherThanSkipping(t *testing.T) {
	err := confirmObjectStoreRolledOut(context.Background(), map[string]tfexec.OutputMeta{},
		clientsFor(fake.NewSimpleClientset(objectStoreDeployment(2, 2, 1, 1, 0))), 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "backup_object_store_deployment") {
		t.Fatalf("a missing output was read as no store: %v", err)
	}
}

// 🔴 "NO STORE" MUST BE SAID, NOT IMPLIED. A root output whose value is null is not
// stored in state at all, so a root that expressed "no store" as null would reach
// dcctl as a MISSING output (the case above) on every cluster without one -- which is
// how the first cut of this check failed every install with backups off. A null that
// does arrive, or an object that does not say whether there is a store, is refused
// the same way rather than read as "no store".
func TestAStoreOutputThatDoesNotSayWhetherThereIsAStoreFails(t *testing.T) {
	for _, v := range []string{`null`, `{}`, `{"namespace":"","name":""}`, `{"namespace":"dc-system","name":"dc-object-store"}`} {
		err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(v),
			clientsFor(fake.NewSimpleClientset(objectStoreDeployment(2, 2, 1, 1, 0))), 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "does not say whether") {
			t.Errorf("output %s was not refused as not saying whether there is a store: %v", v, err)
		}
	}
	// ...and "no store" that names one anyway is a contradiction, not a skip.
	err := confirmObjectStoreRolledOut(context.Background(),
		objectStoreOutputs(`{"in_cluster":false,"namespace":"dc-system","name":"dc-object-store"}`),
		clientsFor(fake.NewSimpleClientset(objectStoreDeployment(2, 2, 1, 1, 0))), 20*time.Millisecond)
	if err == nil {
		t.Error("an output saying there is no store while naming one was accepted")
	}
}

func TestAStoreOutputNamingNoDeploymentFails(t *testing.T) {
	for _, v := range []string{`{"in_cluster":true,"namespace":"dc-system","name":""}`, `{"in_cluster":true,"namespace":"","name":"dc-object-store"}`, `"dc-object-store"`} {
		if err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(v),
			clientsFor(fake.NewSimpleClientset()), 20*time.Millisecond); err == nil {
			t.Errorf("output %s was accepted", v)
		}
	}
	// ...and an empty name is refused as what it is, before any client is built, rather
	// than surfacing as a Get of an empty name.
	for _, v := range []string{`{"in_cluster":true,"namespace":"dc-system","name":""}`, `{"in_cluster":true,"namespace":"","name":"dc-object-store"}`} {
		err := confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(v),
			func() (kubernetes.Interface, error) {
				t.Errorf("kube clients were built for output %s, which names no Deployment", v)
				return fake.NewSimpleClientset(), nil
			}, 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "names no Deployment") {
			t.Errorf("output %s was not refused as naming no Deployment: %v", v, err)
		}
	}
}

// TestLiveObjectStoreRolloutCheck is hack/tofu-rerun-rig.sh's hook into the check
// dcctl runs after every cluster apply: it drives confirmObjectStoreRolledOut, with
// dcctl's own kube clients, against a real Deployment the rig has put in a known
// state. Skipped unless the rig sets DCCTL_RIG_KUBE_CONTEXT.
//
// DCCTL_RIG_EXPECT says which answer is right, "ready" or "unready"; the rig asks
// for both, so a check that always answered one way fails one of the two runs.
func TestLiveObjectStoreRolloutCheck(t *testing.T) {
	kubeContext := os.Getenv("DCCTL_RIG_KUBE_CONTEXT")
	if kubeContext == "" {
		t.Skip("DCCTL_RIG_KUBE_CONTEXT is not set; this runs under hack/tofu-rerun-rig.sh")
	}
	ref, err := json.Marshal(map[string]any{
		"in_cluster": true,
		"namespace":  os.Getenv("DCCTL_RIG_NAMESPACE"),
		"name":       os.Getenv("DCCTL_RIG_DEPLOYMENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = confirmObjectStoreRolledOut(context.Background(), objectStoreOutputs(string(ref)),
		func() (kubernetes.Interface, error) {
			_, _, typed, err := kubeClients(kubeContext)
			return typed, err
		}, 20*time.Second)
	switch want := os.Getenv("DCCTL_RIG_EXPECT"); want {
	case "ready":
		if err != nil {
			t.Fatalf("dcctl refused a store that rolled out: %v", err)
		}
	case "unready":
		if err == nil {
			t.Fatal("dcctl accepted a store that has not rolled out")
		}
		t.Logf("refused, as it must be: %v", err)
	default:
		t.Fatalf("DCCTL_RIG_EXPECT is %q, want ready or unready", want)
	}
}
