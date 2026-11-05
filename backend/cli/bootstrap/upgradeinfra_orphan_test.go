// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
		{"not grown: the recorded ceiling is the broker's", "16Gi", "14Gi", "16Gi", "14Gi"},
		{"nothing recorded", "16Gi", "", "36Gi", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := maxStoreAfterGrowth(state(c.recordedSize, c.recordedMax), t.TempDir(), c.live, liveMaxFileStore(state(c.recordedSize, c.recordedMax))); got != c.want {
				t.Fatalf("ceiling = %q, want %q", got, c.want)
			}
		})
	}

	// 🔴 STATED, NOT COMPARED. An operator who set nats_jetstream_max_file_store to the very value
	// the old volume derives (14Gi for 16Gi) is indistinguishable by value from nobody having set
	// it; the setting being stated in the root's tfvars is what says so.
	t.Run("hand-set to the old derived value: kept", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"),
			[]byte("nats_jetstream_max_file_store = \"14Gi\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := maxStoreAfterGrowth(state("16Gi", "14Gi"), dir, "36Gi", "14Gi"); got != "14Gi" {
			t.Fatalf("a stated ceiling was dropped: %q", got)
		}
	})
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
		{"a resize is still being applied to the filesystem", "volume expansion has not finished for dc-nats-js-dc-nats-1", func(o []runtime.Object) []runtime.Object {
			for _, x := range o {
				if c, ok := x.(*corev1.PersistentVolumeClaim); ok && c.Name == "dc-nats-js-dc-nats-1" {
					c.Status.Conditions = []corev1.PersistentVolumeClaimCondition{
						{Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue}}
				}
			}
			return o
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

// 🔴 A SERVER THAT IS GONE DOES NOT BLOCK THE UPGRADE. Nothing owns an orphaned pod, so one that was
// deleted or evicted never comes back by itself; the recreated StatefulSet is what starts it.
// Refusing on it would leave the operator unable to do the one thing that fixes it. A server that
// EXISTS and is not ready still blocks (TestAnOrphanedBrokerThatIsNotWholeIsRefused).
//
// Negative control: counting pods against claims (the earlier rule) refuses all three cases.
func TestAMissingServerDoesNotBlockTakingOverAnOrphanedBroker(t *testing.T) {
	for name, drop := range map[string][]string{
		"one of three": {"dc-nats-2"},
		"two of three": {"dc-nats-1", "dc-nats-2"},
		"every server": {"dc-nats-0", "dc-nats-1", "dc-nats-2"},
	} {
		t.Run(name, func(t *testing.T) {
			var objs []runtime.Object
			for _, x := range orphanedBrokerObjects([]string{"36Gi", "36Gi", "36Gi"}, true) {
				if p, ok := x.(*corev1.Pod); ok && slices.Contains(drop, p.Name) {
					continue
				}
				objs = append(objs, x)
			}
			infra, err := readInfraFrom(context.Background(), fake.NewSimpleClientset(objs...), storeDynamic(t), InstanceNamespace("prod"))
			if err != nil {
				t.Fatal(err)
			}
			if why := orphanedBrokerNotReady(infra.BrokerPods, infra.BrokerClaims); why != "" {
				t.Fatalf("a missing server blocked the upgrade: %s", why)
			}
		})
	}
}

// A Terminating claim is on its way out, and a claim of a server the instance no longer runs (a
// scaled-down cluster leaves them) neither sets the volume's size nor blocks; it is named in a note.
//
// Negative control: counting every claim makes the stale 8Gi claim the volume's size.
func TestOnlyTheClaimsOfTheServersThatRunCount(t *testing.T) {
	objs := orphanedBrokerObjects([]string{"36Gi", "36Gi", "36Gi"}, true)
	now := metav1.NewTime(time.Now())
	objs = append(objs,
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-js-dc-nats-3", Namespace: InstanceNamespace("prod")},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}}}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-js-dc-nats-4", Namespace: InstanceNamespace("prod"), DeletionTimestamp: &now},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Gi")}}}})
	infra, err := readInfraFrom(context.Background(), fake.NewSimpleClientset(objs...), storeDynamic(t), InstanceNamespace("prod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range infra.BrokerClaims {
		if c.Name == "dc-nats-js-dc-nats-4" {
			t.Fatal("a Terminating claim was counted")
		}
	}
	narrowed, extra := infra.forReplicas(3)
	if !reflect.DeepEqual(extra, []string{"dc-nats-js-dc-nats-3"}) {
		t.Fatalf("extra = %v", extra)
	}
	vols, err := narrowed.volumes()
	if err != nil {
		t.Fatal(err)
	}
	if vols.JetStream != "36Gi" {
		t.Fatalf("the stale claim set the volume: %q", vols.JetStream)
	}
}

// 🔴 REFUSED BEFORE ANY WRITE when nothing would recreate the StatefulSet: the claims are at the
// size the release last applied, so the plan is empty. The refusal names the claims below the
// largest, which is what a half-finished patch leaves.
//
// Negative control: without the call in prepareUpgradePlan the plan is made and the upgrade goes on
// to wait out a StatefulSet that never comes back.
func TestAnOrphanedBrokerAtTheRecordedSizeIsRefusedBeforeAnyWrite(t *testing.T) {
	nats := func(size string) *tfjson.State {
		return &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
			Resources: []*tfjson.StateResource{{Address: natsReleaseAddress, AttributeValues: valuesAttr(t,
				map[string]interface{}{"config": map[string]interface{}{"jetstream": map[string]interface{}{
					"fileStore": map[string]interface{}{"maxSize": "14Gi", "pvc": map[string]interface{}{"size": size}}}}})}}}}}
	}
	st := func(sizes ...string) *State {
		s := &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{}}
		var objs []runtime.Object
		for _, o := range orphanedBrokerObjects(sizes, true) {
			objs = append(objs, o)
		}
		infra, err := readInfraFrom(context.Background(), fake.NewSimpleClientset(objs...), storeDynamic(t), InstanceNamespace("prod"))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range infra.BrokerClaims {
			q := c.Spec.Resources.Requests[corev1.ResourceStorage]
			s.OrphanedClaims = append(s.OrphanedClaims, OrphanedClaim{Name: c.Name, Size: q.String()})
		}
		vols, err := infra.volumes()
		if err != nil {
			t.Fatal(err)
		}
		s.LiveVolumes = vols
		return s
	}

	t.Run("half-patched: the smallest is the recorded size", func(t *testing.T) {
		_, err := prepareUpgradePlan(context.Background(), st("36Gi", "16Gi", "36Gi"),
			&fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, state: nats("16Gi")}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "nothing would recreate") ||
			!strings.Contains(err.Error(), "dc-nats-js-dc-nats-1 (16Gi)") || !strings.Contains(err.Error(), "Nothing has been changed") {
			t.Fatalf("not refused as expected: %v", err)
		}
	})
	t.Run("unpatched: all at the recorded size", func(t *testing.T) {
		_, err := prepareUpgradePlan(context.Background(), st("16Gi", "16Gi", "16Gi"),
			&fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, state: nats("16Gi")}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "nothing would recreate") {
			t.Fatalf("not refused: %v", err)
		}
	})
	t.Run("grown: not refused", func(t *testing.T) {
		if err := refuseUnchangedOrphanedVolume(nats("16Gi"), st("36Gi", "36Gi", "36Gi")); err != nil {
			t.Fatalf("a grown volume was refused: %v", err)
		}
	})
}
