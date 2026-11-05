// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

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
				// The expansion has finished: the claim's capacity is what it asked for.
				Status: corev1.PersistentVolumeClaimStatus{Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size)}},
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

// The reservation check prefers the ceiling the release recorded, and the release records the one
// the OLD volume derived. Over a grown volume that is the check refusing the reservation the growth
// was made to hold, so the recorded ceiling is dropped when the apply is about to re-derive it, and
// kept when an operator set it.
func TestAGrownVolumeIsNotCheckedAgainstItsOldDerivedCeiling(t *testing.T) {
	state := func(size, max string) *tfjson.State {
		return &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
			Resources: []*tfjson.StateResource{{Address: natsReleaseAddress, AttributeValues: valuesAttr(t,
				map[string]interface{}{"config": map[string]interface{}{"jetstream": map[string]interface{}{
					"fileStore": map[string]interface{}{"maxSize": max, "pvc": map[string]interface{}{"size": size}}}}})}}}}}
	}
	for _, c := range []struct {
		name, recordedSize, recordedMax, live, want string
	}{
		{"grown, ceiling was derived: re-derived by the apply", "16Gi", "14Gi", "36Gi", ""},
		{"grown, ceiling hand-set: honoured", "16Gi", "15Gi", "36Gi", "15Gi"},
		{"not grown: the recorded ceiling is the broker's", "16Gi", "14Gi", "16Gi", "14Gi"},
		{"nothing recorded", "16Gi", "", "36Gi", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := maxStoreAfterGrowth(state(c.recordedSize, c.recordedMax), c.live, liveMaxFileStore(state(c.recordedSize, c.recordedMax))); got != c.want {
				t.Fatalf("ceiling = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnOrphanedBrokerThatIsNotWholeIsRefused(t *testing.T) {
	read := func(mutate func(objs []runtime.Object) []runtime.Object) liveInfra {
		typed := fake.NewSimpleClientset(mutate(orphanedBrokerObjects([]string{"36Gi", "36Gi", "36Gi"}, true))...)
		infra, err := readInfraFrom(context.Background(), typed, storeDynamic(t), InstanceNamespace("prod"))
		if err != nil {
			t.Fatal(err)
		}
		return infra
	}
	for _, c := range []struct {
		name, want string
		mutate     func([]runtime.Object) []runtime.Object
	}{
		{"a server is gone", "2 servers are running for 3 volume claims", func(o []runtime.Object) []runtime.Object {
			var out []runtime.Object
			for _, x := range o {
				if p, ok := x.(*corev1.Pod); ok && p.Name == "dc-nats-2" {
					continue
				}
				out = append(out, x)
			}
			return out
		}},
		{"a server is terminating with a stale Ready", "dc-nats-1", func(o []runtime.Object) []runtime.Object {
			for _, x := range o {
				if p, ok := x.(*corev1.Pod); ok && p.Name == "dc-nats-1" {
					now := metav1.NewTime(time.Now())
					p.DeletionTimestamp = &now
				}
			}
			return o
		}},
		{"an expansion has not finished", "volume expansion has not finished for dc-nats-js-dc-nats-0", func(o []runtime.Object) []runtime.Object {
			for _, x := range o {
				if c, ok := x.(*corev1.PersistentVolumeClaim); ok && c.Name == "dc-nats-js-dc-nats-0" {
					c.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("16Gi")}
				}
			}
			return o
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			infra := read(c.mutate)
			if why := orphanedBrokerNotReady(infra.BrokerPods, infra.BrokerClaims); !strings.Contains(why, c.want) {
				t.Fatalf("orphanedBrokerNotReady = %q, want it to say %q", why, c.want)
			}
		})
	}
}

// With --skip-infrastructure nothing recreates the StatefulSet or re-derives the broker's ceiling,
// so the services must not be checked (or upgraded) against the claims' size.
func TestSkippingInfrastructureOverAnOrphanedBrokerIsRefused(t *testing.T) {
	typed := fake.NewSimpleClientset(orphanedBrokerObjects([]string{"36Gi", "36Gi", "36Gi"}, true)...)
	infra, err := readInfraFrom(context.Background(), typed, storeDynamic(t), InstanceNamespace("prod"))
	if err != nil {
		t.Fatal(err)
	}
	stubUpgradeReads(t, clusterArchiveState{Exists: true}, infra, natsauth.DeployedHashes{})
	err = precheckJetStreamReservation(context.Background(), &State{Instance: "prod", KubeContext: "gke_p_z_c"})
	if err == nil || !strings.Contains(err.Error(), "without --skip-infrastructure") {
		t.Fatalf("services were checked against a broker nobody owns: %v", err)
	}
}
