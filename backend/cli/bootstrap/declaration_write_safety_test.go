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

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/operator"
)

// withField sets a value at a path in a declaration as the cluster holds it, the way a newer dcctl
// (or a newer schema) would have written a field this build does not know.
func withField(t *testing.T, obj *unstructured.Unstructured, value interface{}, path ...string) *unstructured.Unstructured {
	t.Helper()
	if err := unstructured.SetNestedField(obj.Object, value, path...); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestUnknownDeclarationFieldsFindsOnlyWhatThisBuildDoesNotKnow(t *testing.T) {
	base := declaredInstance(t, "prod", func(inst *dcv1beta1.Instance) {
		inst.Spec.ObjectStore = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreS3,
			S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "b", CredentialsSecret: &dcv1beta1.SecretReference{Namespace: "n", Name: "s"}}}
		inst.Spec.ShardGroups = &dcv1beta1.ShardGroupsSpec{Count: 2, Suffixes: []string{"inbound-events"},
			PlacementTags: map[string]dcv1beta1.ShardPlacementTags{"0": {"dc-nats-0"}}}
		inst.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: "True"}}
	})
	if got := unknownDeclarationFields(base.Object); len(got) != 0 {
		t.Fatalf("a declaration this build wrote reports unknown fields: %v", got)
	}
	// Explicit zero values a newer writer may spell out are known fields, not unknown ones.
	if got := unknownDeclarationFields(withField(t, base.DeepCopy(), false, "spec", "ha").Object); len(got) != 0 {
		t.Fatalf("an explicit false on a known field was reported unknown: %v", got)
	}
	for _, tc := range []struct {
		name string
		path []string
		want string
	}{
		{"a new spec field", []string{"spec", "futureSetting"}, "spec.futureSetting"},
		{"a new field inside a known object", []string{"spec", "objectStore", "s3", "kmsKey"}, "spec.objectStore.s3.kmsKey"},
		{"a new field deeper still", []string{"spec", "objectStore", "s3", "credentialsSecret", "key"}, "spec.objectStore.s3.credentialsSecret.key"},
		{"a new top-level field", []string{"provenance"}, "provenance"},
	} {
		got := unknownDeclarationFields(withField(t, base.DeepCopy(), "x", tc.path...).Object)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: unknown = %v, want [%s]", tc.name, got, tc.want)
		}
	}
	// Status is a subresource an Update does not write, and metadata a map the decode keeps whole.
	if got := unknownDeclarationFields(withField(t, base.DeepCopy(), "x", "status", "newObservation").Object); len(got) != 0 {
		t.Fatalf("an unknown status field was reported, but an Update of the object does not write status: %v", got)
	}
}

// 🔴 THE WRITE THAT WOULD ERASE A FIELD IS REFUSED, AND NOTHING IS WRITTEN. A declaration a newer
// dcctl wrote carries a field this one cannot decode; the bootstrap/upgrade write and the metadata
// CAS both replace the declaration whole, so both must refuse before sending anything.
func TestAWholeDeclarationWriteRefusesFieldsThisBuildDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(ctx context.Context, dyn *dynamicfake.FakeDynamicClient) error
	}{
		{"the declaration write", func(ctx context.Context, dyn *dynamicfake.FakeDynamicClient) error {
			return writeInstanceDecl(ctx, dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseUpgrading, markCarried)
		}},
		{"a metadata compare-and-swap", func(ctx context.Context, dyn *dynamicfake.FakeDynamicClient) error {
			return updateInstanceMetadata(ctx, dyn, "prod", "a test annotation", func(inst *dcv1beta1.Instance) (bool, error) {
				inst.Annotations["core.devicechain.io/test"] = "1"
				return true, nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := declarationClient(withField(t, declaredInstance(t, "prod", nil), "kept", "spec", "futureSetting"))
			err := tc.write(t.Context(), dyn)
			if err == nil || !strings.Contains(err.Error(), "spec.futureSetting") {
				t.Fatalf("the write went ahead over a field it would erase (err %v)", err)
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "update" || a.GetVerb() == "patch" || a.GetVerb() == "create" {
					t.Fatalf("something was written despite the refusal: %s", a.GetVerb())
				}
			}
			// The ordinary read still works: destroy and status lose nothing by ignoring a field.
			if inst := readBack(t, dyn, "prod"); inst == nil {
				t.Fatal("the declaration could not be read at all")
			}
		})
	}
}

// 🔴 RELEASE TOUCHES THE FINALIZERS AND NOTHING ELSE. It is the escape hatch for a cluster whose
// operator is gone or moved, which is when a declaration is likeliest to carry a field this dcctl
// does not know; a whole-object Update would erase it. It is a merge patch of the finalizer list,
// guarded by the resourceVersion it read, and the delete is held to the UID it read.
func TestReleasePatchesOnlyTheFinalizers(t *testing.T) {
	obj := withField(t, declaredInstance(t, "prod", func(inst *dcv1beta1.Instance) {
		inst.Finalizers = append(inst.Finalizers, "example.com/someone-else")
		inst.UID = "uid-1"
		inst.ResourceVersion = "7"
	}), "kept", "spec", "futureSetting")
	dyn := declarationClient(obj)

	released, err := releaseInstanceDeclaration(t.Context(), dyn, "prod")
	if err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	var patched, deleted bool
	for _, a := range dyn.Actions() {
		switch act := a.(type) {
		case clienttesting.UpdateAction:
			t.Fatalf("release replaced the whole declaration (an Update); it must patch the finalizers only")
		case clienttesting.PatchAction:
			patched = true
			var body map[string]map[string]interface{}
			if err := json.Unmarshal(act.GetPatch(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["metadata"] == nil {
				t.Fatalf("the patch touches more than metadata: %s", act.GetPatch())
			}
			meta := body["metadata"]
			if meta["resourceVersion"] != "7" {
				t.Fatalf("the patch carries no resourceVersion precondition: %s", act.GetPatch())
			}
			fins, _ := meta["finalizers"].([]interface{})
			if len(meta) != 2 || len(fins) != 1 || fins[0] != "example.com/someone-else" {
				t.Fatalf("the patch must leave exactly the other finalizers: %s", act.GetPatch())
			}
		case clienttesting.DeleteAction:
			deleted = true
			if p := act.GetDeleteOptions().Preconditions; p == nil || p.UID == nil || *p.UID != "uid-1" {
				t.Fatalf("the delete is not held to the UID that was read: %+v", act.GetDeleteOptions())
			}
		}
	}
	if !patched || !deleted {
		t.Fatalf("patched=%v deleted=%v; want both", patched, deleted)
	}
}

// A release refuses an operator that does not match, and lets a MISSING one through: that is the
// case the command exists for.
func TestReleaseRefusesAMismatchedOperatorButNotAMissingOne(t *testing.T) {
	if note, err := judgeOperatorUnlessAbsent(operator.Installed{Present: false, Missing: []string{"instances.core.devicechain.io"}},
		"want", "release", "dcctl install"); err != nil || note == "" {
		t.Fatalf("a missing operator: note %q, err %v; want a note and no refusal", note, err)
	}
	mismatch := operator.Installed{Present: true, Identity: "other"}
	if _, err := judgeOperatorUnlessAbsent(mismatch, "want", "release", "dcctl install"); err == nil {
		t.Fatal("an operator that does not match this dcctl was not refused")
	}
	match := operator.Installed{Present: true, Identity: "want"}
	if note, err := judgeOperatorUnlessAbsent(match, "want", "release", "dcctl install"); err != nil || note != "" {
		t.Fatalf("the matching operator: note %q, err %v; want silence", note, err)
	}
}

// 🔴 THE UPGRADE CLAIM IS REFUSED, NOT ANNOUNCED, AND THE STAMP IS ASKED AGAIN UNDER IT.
func TestTheUpgradeClaimRefusesAndRechecksUnderTheLock(t *testing.T) {
	cs := fake.NewClientset()
	origClients, origAcquire := claimClientsFor, acquireClaimFor
	t.Cleanup(func() { claimClientsFor, acquireClaimFor = origClients, origAcquire })
	claimClientsFor = func(string) (string, kubernetes.Interface, error) { return testClaimNS, cs, nil }

	// Held by another run: refused, and the refusal is still the typed held error.
	other, err := AcquireClaim(t.Context(), cs, testClaimNS, "other", testKubeContext)
	if err != nil {
		t.Fatal(err)
	}
	rechecked := false
	_, err = takeUpgradeClaim(t.Context(), testKubeContext, "prod", func(context.Context) error { rechecked = true; return nil })
	var held *ClaimHeldError
	if err == nil || !errors.As(err, &held) {
		t.Fatalf("an upgrade went ahead under a lock another run holds (err %v)", err)
	}
	if rechecked {
		t.Fatal("the stamp was rechecked without the lock")
	}
	other.Release(t.Context())

	// The recheck refuses: the lock is given back and the refusal returned.
	stale := errors.New("this cluster's DeviceChain operator is not the one this dcctl expects")
	if _, err := takeUpgradeClaim(t.Context(), testKubeContext, "prod", func(context.Context) error { return stale }); !errors.Is(err, stale) {
		t.Fatalf("a failed recheck was not returned: %v", err)
	}
	if _, err := cs.CoordinationV1().Leases(testClaimNS).Get(t.Context(), claimLeaseName, metav1.GetOptions{}); err == nil {
		t.Fatal("the lock was kept after the recheck refused")
	}

	// Free lock, matching stamp: the claim is returned, held, with the recheck asked under it.
	var leaseAtRecheck *coordinationv1.Lease
	claim, err := takeUpgradeClaim(t.Context(), testKubeContext, "prod", func(ctx context.Context) error {
		leaseAtRecheck, _ = cs.CoordinationV1().Leases(testClaimNS).Get(ctx, claimLeaseName, metav1.GetOptions{})
		return nil
	})
	if err != nil || claim == nil {
		t.Fatalf("a free lock was not taken: %v", err)
	}
	t.Cleanup(func() { claim.Release(context.Background()) })
	if leaseAtRecheck == nil {
		t.Fatal("the stamp was rechecked before the lock was held")
	}
}

// 🔴 TESTING THE HELPER IS NOT TESTING ITS CALLER. Upgrade must take the claim BEFORE its first
// declaration write; this reads the order in the source, so moving the call below the write (or
// back to the old warning) fails here.
func TestUpgradeTakesTheClaimBeforeItsFirstDeclarationWrite(t *testing.T) {
	src, err := os.ReadFile("upgrade.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "func Upgrade(")
	claim := strings.Index(s[start:], "takeUpgradeClaim(ctx, kubeContext")
	write := strings.Index(s[start:], "recordUpgradedVersion(ctx, dyn, opts.Instance, st)")
	if claim < 0 || write < 0 || claim > write {
		t.Fatalf("Upgrade does not take the claim before it records the upgraded version (claim at %d, write at %d)", claim, write)
	}
	if strings.Contains(s, "continuing with the upgrade anyway") {
		t.Fatal("the upgrade still announces a lock it could not take and carries on")
	}
}
