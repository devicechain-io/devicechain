// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// raceOnFirstUpdate makes the declaration change underneath dcctl's FIRST Update: the
// concurrent write (between) lands in the store, and the Update is answered with the
// Conflict the API server gives a stale resourceVersion. Later Updates go through.
//
// The fake enforces no resourceVersion precondition of its own (see claim_test.go), so the
// API server's answer is forced; what is asserted is what dcctl does with it -- and the
// store holds what a re-read would really see.
func raceOnFirstUpdate(t *testing.T, dyn *dynamicfake.FakeDynamicClient, between func(*dcv1beta1.Instance)) *int {
	t.Helper()
	updates := 0
	dyn.PrependReactor("update", "instances", func(a k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates > 1 {
			return false, nil, nil
		}
		// The tracker, not the client: the fake holds its lock while a reactor runs, so a
		// client Get from in here would deadlock.
		raw, err := dyn.Tracker().Get(instanceGVR, "", "prod")
		if err != nil {
			t.Fatal(err)
		}
		cur := &dcv1beta1.Instance{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.(*unstructured.Unstructured).Object, cur); err != nil {
			t.Fatal(err)
		}
		between(cur)
		obj, err := instanceToUnstructured(cur)
		if err != nil {
			t.Fatal(err)
		}
		if err := dyn.Tracker().Update(instanceGVR, obj, ""); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewConflict(instanceGVR.GroupResource(), "prod",
			errors.New("the object has been modified; please apply your changes to the latest version"))
	})
	return &updates
}

// operatorStatusWrite is the operator's first status write on a fresh instance: only
// .status moves (and, on a real server, resourceVersion).
func operatorStatusWrite(inst *dcv1beta1.Instance) {
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.Conditions = append(inst.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: "Reconciling",
		LastTransitionTime: metav1.Now(),
	})
}

// 🔴 THE LF-02 FAILURE: on a fresh instance the operator's first status write lands between
// dcctl's read and its write, and the overlay record lost the compare-and-swap to it -- in
// 2 of 3 fresh `dcctl bootstrap --values` runs. A status-only change is not a conflict.
func TestTheOverlayRecordSurvivesAnOperatorStatusWrite(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	updates := raceOnFirstUpdate(t, dyn, operatorStatusWrite)
	overlay := loadTuning(t)

	if err := recordValuesOverlay(t.Context(), dyn, "prod", overlay); err != nil {
		t.Fatalf("a status-only write between the read and the write failed the record: %v", err)
	}
	if *updates != 2 {
		t.Errorf("want one refused write and one retried write, got %d writes", *updates)
	}
	back := readBack(t, dyn, "prod")
	got, err := decodeValuesOverlay(back.Annotations)
	if err != nil {
		t.Fatal(err)
	}
	if d := diffOverlays(overlay, got); len(d) != 0 {
		t.Errorf("the recorded overlay is not the one given: %v", d)
	}
	if len(back.Status.Conditions) != 1 || back.Status.Conditions[0].Reason != "Reconciling" {
		t.Errorf("the retry must build on the fresh read; the operator's status is gone: %+v", back.Status)
	}
}

// The counterweight: a change an EDITOR made between the read and the write -- the spec, or
// another annotation -- is still a loud conflict, and dcctl writes nothing over it.
func TestTheOverlayRecordStillRefusesARealConcurrentEdit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		between func(*dcv1beta1.Instance)
		kept    func(*dcv1beta1.Instance) bool
	}{
		{"a spec change", func(i *dcv1beta1.Instance) {
			i.Generation++
			i.Spec.Host = "edited.example.com"
		}, func(i *dcv1beta1.Instance) bool { return i.Spec.Host == "edited.example.com" }},
		{"a spec change the generation alone shows", func(i *dcv1beta1.Instance) {
			i.Generation++
		}, func(i *dcv1beta1.Instance) bool { return true }},
		{"an annotation change", func(i *dcv1beta1.Instance) {
			i.Annotations["someone.example.com/note"] = "mine"
		}, func(i *dcv1beta1.Instance) bool { return i.Annotations["someone.example.com/note"] == "mine" }},
		{"a status write AND an annotation change", func(i *dcv1beta1.Instance) {
			operatorStatusWrite(i)
			i.Annotations["someone.example.com/note"] = "mine"
		}, func(i *dcv1beta1.Instance) bool { return i.Annotations["someone.example.com/note"] == "mine" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := declarationClient(declaredInstance(t, "prod", nil))
			updates := raceOnFirstUpdate(t, dyn, tc.between)

			err := recordValuesOverlay(t.Context(), dyn, "prod", loadTuning(t))
			if err == nil || !strings.Contains(err.Error(), "changed while the values overlay was being recorded") {
				t.Fatalf("a concurrent edit must be refused loudly, got %v", err)
			}
			if *updates != 1 {
				t.Errorf("a refused edit must not be retried over, got %d writes", *updates)
			}
			back := readBack(t, dyn, "prod")
			if _, has := back.Annotations[AnnotationValuesOverlay]; has {
				t.Error("the overlay was written over a concurrent edit")
			}
			if !tc.kept(back) {
				t.Error("the concurrent edit was reverted")
			}
		})
	}
}

// An operator that never stops writing status is bounded: the record gives up loudly.
func TestTheOverlayRecordGivesUpOnEndlessStatusWrites(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	updates := 0
	dyn.PrependReactor("update", "instances", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return true, nil, apierrors.NewConflict(instanceGVR.GroupResource(), "prod", errors.New("modified"))
	})
	err := recordValuesOverlay(t.Context(), dyn, "prod", loadTuning(t))
	if err == nil || !strings.Contains(err.Error(), "changed while the values overlay was being recorded") {
		t.Fatalf("want a loud conflict, got %v", err)
	}
	if updates != maxDeclarationStatusRetries+1 {
		t.Errorf("want %d bounded attempts, got %d", maxDeclarationStatusRetries+1, updates)
	}
}

// The front-end choice is the other annotation-only declaration write, and it lost the same
// race the same way.
func TestTheFrontEndRecordSurvivesAnOperatorStatusWrite(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	raceOnFirstUpdate(t, dyn, operatorStatusWrite)
	if err := recordIngressController(t.Context(), dyn, "prod", "nginx"); err != nil {
		t.Fatalf("a status-only write failed the front-end record: %v", err)
	}
	if got := readBack(t, dyn, "prod").Annotations[AnnotationIngressController]; got != "nginx" {
		t.Errorf("front-end choice = %q, want nginx", got)
	}
}
