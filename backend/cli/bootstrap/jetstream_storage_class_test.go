// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestJetStreamStorageClassIsOnlyDefaultedForNewGKEBrokers(t *testing.T) {
	const instance = "ssd-test"
	ns := InstanceNamespace(instance)
	for _, tc := range []struct {
		name        string
		existing    runtime.Object
		provisioner string
		diskType    string
		stated      bool
		want        string
		wantErr     string
	}{
		{name: "GKE", provisioner: "pd.csi.storage.gke.io", diskType: "pd-ssd", want: "premium-rwo"},
		{name: "kind"},
		{name: "other cloud", provisioner: "ebs.csi.aws.com"},
		{name: "existing unlabeled StatefulSet", provisioner: "pd.csi.storage.gke.io", diskType: "pd-ssd",
			existing: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: natsStatefulSetName, Namespace: ns}}},
		{name: "orphaned unlabeled claim", provisioner: "pd.csi.storage.gke.io", diskType: "pd-ssd",
			existing: &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-js-dc-nats-0", Namespace: ns}}},
		{name: "unrelated claim", provisioner: "pd.csi.storage.gke.io", diskType: "pd-ssd", want: "premium-rwo",
			existing: &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns}}},
		{name: "operator override", provisioner: "pd.csi.storage.gke.io", diskType: "pd-ssd", stated: true},
		{name: "misconfigured GKE SSD", provisioner: "pd.csi.storage.gke.io", diskType: "pd-balanced", wantErr: "must use type pd-ssd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TF_VAR_"+jetStreamStorageClassVar, "")
			dir := t.TempDir()
			if tc.stated {
				dir = writeStated(t, map[string]string{"terraform.tfvars": `nats_jetstream_storage_class = "custom"`})
			}
			var objects []runtime.Object
			if tc.provisioner != "" {
				sc := storageClass("premium-rwo", tc.provisioner, false)
				sc.Parameters = map[string]string{"type": tc.diskType}
				objects = append(objects, sc)
			}
			if tc.existing != nil {
				objects = append(objects, tc.existing)
			}
			typed := fake.NewSimpleClientset(objects...)
			base := []string{"other=value"}
			got, err := appendJetStreamStorageClass(t.Context(), typed, &State{Instance: instance}, dir, base)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"other=value"}
			if tc.want != "" {
				want = append(want, jetStreamStorageClassVar+"="+tc.want)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("vars = %v, want %v", got, want)
			}
		})
	}
}

func TestJetStreamStorageClassReadErrorsFailClosed(t *testing.T) {
	for _, resource := range []string{"statefulsets", "persistentvolumeclaims", "storageclasses"} {
		t.Run(resource, func(t *testing.T) {
			t.Setenv("TF_VAR_"+jetStreamStorageClassVar, "")
			typed := fake.NewSimpleClientset()
			typed.PrependReactor("*", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("storage probe forbidden")
			})
			_, err := appendJetStreamStorageClass(t.Context(), typed, &State{Instance: "test"}, t.TempDir(), nil)
			if err == nil || !strings.Contains(err.Error(), "storage probe forbidden") {
				t.Fatalf("read error was lost: %v", err)
			}
		})
	}
}

func TestBootstrapSelectsJetStreamClassBeforeApplyAndUpgradeLeavesItUnset(t *testing.T) {
	raw, err := os.ReadFile("tofu.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func applyInfra(")
	end := strings.Index(src[start:], "\nfunc instanceRootVars(")
	body := src[start : start+end]
	choose := strings.Index(body, "appendJetStreamStorageClass(ctx, typed, st, inst.rootdir, instanceVars)")
	apply := strings.Index(body, "return applyInstanceInfra(")
	if choose < 0 || choose > apply {
		t.Fatal("bootstrap must select the storage class before applying the instance root")
	}
	// Upgrades compose these same root vars without the new-install default.
	_, vars, err := splitVars(infraVars(&State{Instance: "test", KubeContext: managedKubeContext}))
	if err != nil {
		t.Fatal(err)
	}
	if hasVar(vars, jetStreamStorageClassVar) {
		t.Fatalf("upgrade inputs must leave the new class unset: %v", vars)
	}
}
