// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/natsauth"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// The documented way to grow the JetStream volume (observability.md, "Sizing the volume") is to
// delete the broker's StatefulSet with --cascade=orphan, patch its claims, and run `dcctl upgrade`
// again. The upgrade used to read the StatefulSet first and refuse when it was gone, so the
// recipe could not be completed. These tests start from exactly the state the recipe leaves.

func orphanedBrokerObjects(claimSizes []string, ready bool) []runtime.Object {
	var objs []runtime.Object
	ns := InstanceNamespace("prod")
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionFalse
	}
	for i, size := range claimSizes {
		n := string(rune('0' + i))
		objs = append(objs,
			&corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-js-dc-nats-" + n, Namespace: ns},
				Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}}},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-" + n, Namespace: ns},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: status}}},
			})
	}
	// An unrelated claim must not be taken for the broker's.
	objs = append(objs, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-blob", Namespace: ns},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}})
	return objs
}

func storeDynamic(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusterGVR: "ClusterList"},
		func() runtime.Object {
			es := anEventStore(3, "8Gi", "tsdb:1")
			es.SetNamespace(InstanceNamespace("prod"))
			return es
		}())
}

func TestReadInfraFromSurvivesAnOrphanedStatefulSet(t *testing.T) {
	typed := fake.NewSimpleClientset(orphanedBrokerObjects([]string{"36Gi", "36Gi", "36Gi"}, true)...)
	infra, err := readInfraFrom(context.Background(), typed, storeDynamic(t), InstanceNamespace("prod"))
	if err != nil {
		t.Fatalf("the state the documented recipe leaves (StatefulSet deleted, claims patched) is refused: %v", err)
	}
	if infra.Broker != nil || len(infra.BrokerClaims) != 3 || len(infra.BrokerPods) != 3 {
		t.Fatalf("read %v claims, %d pods, broker %v", len(infra.BrokerClaims), len(infra.BrokerPods), infra.Broker)
	}
	vols, err := infra.volumes()
	if err != nil {
		t.Fatal(err)
	}
	if vols.JetStream != "36Gi" || vols.EventStore != "8Gi" {
		t.Fatalf("volumes = %+v, want JetStream 36Gi from the claims", vols)
	}
}

func TestAHalfPatchedBrokerReadsAtItsSmallestClaim(t *testing.T) {
	typed := fake.NewSimpleClientset(orphanedBrokerObjects([]string{"36Gi", "16Gi", "36Gi"}, true)...)
	infra, err := readInfraFrom(context.Background(), typed, storeDynamic(t), InstanceNamespace("prod"))
	if err != nil {
		t.Fatal(err)
	}
	vols, err := infra.volumes()
	if err != nil {
		t.Fatal(err)
	}
	if vols.JetStream != "16Gi" {
		t.Fatalf("JetStream = %q: a claim not yet grown bounds the volume", vols.JetStream)
	}
}

// With neither the StatefulSet nor its claims there is no broker, and that stays an error naming the
// StatefulSet: treating it as "nothing to keep" would hand the apply the roots' defaults.
func TestNoStatefulSetAndNoClaimsIsStillNoBroker(t *testing.T) {
	typed := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-blob", Namespace: InstanceNamespace("prod")}})
	_, err := readInfraFrom(context.Background(), typed, storeDynamic(t), InstanceNamespace("prod"))
	if err == nil || !strings.Contains(err.Error(), "StatefulSet") {
		t.Fatalf("an absent broker was not refused: %v", err)
	}
}

func TestTheUpgradeAppliesOverAnOrphanedBrokerAtTheGrownSize(t *testing.T) {
	creds, err := existingCredentials()
	if err != nil {
		t.Fatal(err)
	}
	read := func(ready bool) liveInfra {
		typed := fake.NewSimpleClientset(orphanedBrokerObjects([]string{"36Gi", "36Gi", "36Gi"}, ready)...)
		infra, err := readInfraFrom(context.Background(), typed, storeDynamic(t), InstanceNamespace("prod"))
		if err != nil {
			t.Fatal(err)
		}
		infra.StoreImages = imagesOf(3, "tsdb:1")
		return infra
	}
	st := func() *State {
		return &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{
			"natsCA": "ca", "natsCalloutIssuerSeed": creds.IssuerSeed, "natsServicePassword": creds.ServicePassword}}
	}

	t.Run("ready servers: the apply is handed the grown size", func(t *testing.T) {
		stubUpgradeReads(t, clusterArchiveState{Exists: true}, read(true), natsauth.DeployedHashes{Service: creds.ServicePasswordBcrypt})
		s := st()
		if err := settleUpgradeInfraInputs(context.Background(), s); err != nil {
			t.Fatalf("settle over an orphaned broker: %v", err)
		}
		var got bool
		for _, v := range infraVars(s) {
			got = got || v == "nats_jetstream_storage=36Gi"
		}
		if !got {
			t.Errorf("the apply would recreate the StatefulSet at the default size, not the claims': %v", infraVars(s))
		}
		if len(s.InfraNotes) == 0 || !strings.Contains(strings.Join(s.InfraNotes, " "), "--cascade=orphan") {
			t.Errorf("the operator is not told the StatefulSet is being recreated: %v", s.InfraNotes)
		}
	})

	t.Run("servers not ready: refused before anything is written", func(t *testing.T) {
		stubUpgradeReads(t, clusterArchiveState{Exists: true}, read(false), natsauth.DeployedHashes{Service: creds.ServicePasswordBcrypt})
		err := settleUpgradeInfraInputs(context.Background(), st())
		if err == nil || !strings.Contains(err.Error(), "dc-nats-0 Running") || !strings.Contains(err.Error(), "Nothing has been changed") {
			t.Fatalf("an orphaned broker with unready servers was not refused: %v", err)
		}
	})
}
