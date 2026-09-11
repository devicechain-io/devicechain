// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// declaredInstance builds the declaration as it would already be sitting in the
// cluster. The conversion goes through instanceToUnstructured — the same helper
// the write path uses — because what is under test here is which declarations are
// REFUSED, not how a typed object becomes JSON.
func declaredInstance(t *testing.T, id string, mutate func(*dcv1beta1.Instance)) *unstructured.Unstructured {
	t.Helper()
	inst := &dcv1beta1.Instance{}
	inst.Name = id
	inst.Spec = dcv1beta1.InstanceSpec{
		Provider: "local", Cluster: "kind-devicechain", Managed: true,
		Profile: "default", Monitoring: true, CNPG: true, TLS: true,
		Host: DefaultIngressHost,
	}
	setPhase(inst, dcv1beta1.PhaseReady)
	addFinalizer(inst)
	if mutate != nil {
		mutate(inst)
	}
	obj, err := instanceToUnstructured(inst)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

// declarationClient is a dynamic client holding whatever declarations a test puts
// in it. The list kind has to be named explicitly because the fake has no scheme
// to derive it from — an empty scheme plus this map is the supported way to serve
// a CRD type.
func declarationClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{instanceGVR: "InstanceList"},
		objs...)
}

// desiredSpec is what a re-run of the same bootstrap would write.
func desiredSpec() dcv1beta1.InstanceSpec {
	return dcv1beta1.InstanceSpec{
		Provider: "local", Cluster: "kind-devicechain", Managed: true,
		Profile: "default", Monitoring: true, CNPG: true, TLS: true,
		Host: DefaultIngressHost,
	}
}

// readBack returns the declaration as the cluster now holds it.
func readBack(t *testing.T, dyn *dynamicfake.FakeDynamicClient, id string) *dcv1beta1.Instance {
	t.Helper()
	inst, err := readInstanceCR(t.Context(), dyn, id)
	if err != nil {
		t.Fatalf("reading the declaration back: %v", err)
	}
	return inst
}

// 🔴 THIS IS THE PHASE ANNOTATION'S ONLY READER. dcctl writes it before the first
// destructive act of a destroy, and if nothing ever consults it the annotation is
// the thing this slice criticises everywhere else: recorded correctly, connected
// to nothing — and deletable without a single test noticing.
//
// What it guards is a real cluster state: a destroy that started and did not
// finish leaves some of the instance running and some of it gone. Bootstrapping
// over that produces a half-old, half-new instance whose failures are attributed
// to the new run.
func TestADeclarationLeftMidDestroyIsRefused(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "prod", func(inst *dcv1beta1.Instance) {
		setPhase(inst, dcv1beta1.PhaseDestroying)
	}))

	err := writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v0.17.0")
	if err == nil {
		t.Fatal("a bootstrap ran over an instance whose destroy did not finish")
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("the refusal does not say what state the instance is in: %v", err)
	}
	// Both ways out have to be named. One finishes the teardown, the other drops a
	// declaration for an instance the operator knows is already gone; an operator
	// told only "refused" has neither.
	for _, route := range []string{"dcctl destroy", "dcctl instances release"} {
		if !strings.Contains(err.Error(), route) {
			t.Errorf("the refusal does not offer %q as a way forward: %v", route, err)
		}
	}

	// And it refused before writing: a refusal that had already stamped
	// Bootstrapping over Destroying would erase the evidence it just acted on.
	inst := readBack(t, dyn, "prod")
	if inst == nil {
		t.Fatal("the refused write deleted the declaration")
	}
	if got := inst.Annotations[dcv1beta1.AnnotationPhase]; got != dcv1beta1.PhaseDestroying {
		t.Errorf("the phase is now %q; the refusal overwrote the state it was refusing on", got)
	}
}

// 🔴 A TERMINATING DECLARATION IS NOT ADOPTABLE. Kubernetes deletion is one-way:
// once deletionTimestamp is set there is no API to clear it, so an "adopt" here
// would write a spec into an object that vanishes the moment its finalizer clears
// — a bootstrap reporting success over a declaration with a delete already
// committed against it.
func TestATerminatingDeclarationIsRefused(t *testing.T) {
	deleting := metav1.NewTime(time.Now())

	t.Run("a declaration marked for deletion is not adopted", func(t *testing.T) {
		dyn := declarationClient(declaredInstance(t, "prod", func(inst *dcv1beta1.Instance) {
			inst.DeletionTimestamp = &deleting
		}))

		err := writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v0.17.0")
		if err == nil {
			t.Fatal("a bootstrap adopted a declaration that is being deleted")
		}
		if !strings.Contains(err.Error(), "marked for deletion") {
			t.Errorf("the refusal does not say why the declaration cannot be adopted: %v", err)
		}
		for _, route := range []string{"dcctl destroy", "dcctl instances release"} {
			if !strings.Contains(err.Error(), route) {
				t.Errorf("the refusal does not offer %q as a way forward: %v", route, err)
			}
		}
	})

	// 🔴 THE ORDERING IS DELIBERATE AND IS THEREFORE ASSERTED. An object can be
	// both — a destroy that set the phase and then had its declaration deleted by
	// hand — and the Destroying message is the more useful of the two, because it
	// describes the CLUSTER (part of the instance is still running) rather than the
	// object. Leaving which message wins to the order two ifs happen to sit in is
	// how a reader learns the less useful fact.
	t.Run("a declaration that is both reports the unfinished destroy", func(t *testing.T) {
		dyn := declarationClient(declaredInstance(t, "prod", func(inst *dcv1beta1.Instance) {
			setPhase(inst, dcv1beta1.PhaseDestroying)
			inst.DeletionTimestamp = &deleting
		}))

		err := writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v0.17.0")
		if err == nil {
			t.Fatal("a declaration that is both terminating and mid-destroy was adopted")
		}
		if !strings.Contains(err.Error(), "did not finish") {
			t.Errorf("the refusal describes the object rather than the cluster: %v", err)
		}
		if strings.Contains(err.Error(), "marked for deletion") {
			t.Errorf("the terminating message won over the unfinished-destroy one: %v", err)
		}
	})
}

// 🔴 THE COUNTERWEIGHT, and without it every refusal above is satisfied by a
// function that refuses everything — which would break the supported path
// entirely while looking careful.
func TestAnOrdinaryDeclarationIsWrittenAndRewritten(t *testing.T) {
	t.Run("a fresh install creates it", func(t *testing.T) {
		dyn := declarationClient()

		if err := writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v0.17.0"); err != nil {
			t.Fatalf("a first bootstrap could not declare its instance: %v", err)
		}

		inst := readBack(t, dyn, "prod")
		if inst == nil {
			t.Fatal("the declaration is not in the cluster after a successful write")
		}
		if inst.Spec.Provider != "local" || inst.Spec.Cluster != "kind-devicechain" {
			t.Errorf("the declaration records the wrong binding: %+v", inst.Spec)
		}
		// The phase says what this run is TRYING to do, and it is written before the
		// run does any of it — a process killed later leaves a declaration that is true.
		if got := inst.Annotations[dcv1beta1.AnnotationPhase]; got != dcv1beta1.PhaseBootstrapping {
			t.Errorf("phase %q, want %q", got, dcv1beta1.PhaseBootstrapping)
		}
		if inst.Annotations[dcv1beta1.AnnotationLastAppliedBy] != "v0.17.0" {
			t.Errorf("the declaration does not record which dcctl wrote it: %v", inst.Annotations)
		}
		// The finalizer is what keeps a hand-deleted declaration readable until
		// destroy has run.
		found := false
		for _, f := range inst.Finalizers {
			if f == dcv1beta1.FinalizerInstance {
				found = true
			}
		}
		if !found {
			t.Errorf("the declaration carries no finalizer: %v", inst.Finalizers)
		}
	})

	t.Run("a re-run updates it", func(t *testing.T) {
		dyn := declarationClient(declaredInstance(t, "prod", nil))

		desired := desiredSpec()
		desired.Profile = "full"
		desired.HA = true
		if err := writeInstanceCR(t.Context(), dyn, "prod", desired, "v0.18.0"); err != nil {
			t.Fatalf("an ordinary re-run was refused: %v", err)
		}

		inst := readBack(t, dyn, "prod")
		if inst.Spec.Profile != "full" || !inst.Spec.HA {
			t.Errorf("the re-run's values did not reach the declaration: %+v", inst.Spec)
		}
		if inst.Annotations[dcv1beta1.AnnotationLastAppliedBy] != "v0.18.0" {
			t.Errorf("the provenance was not updated: %v", inst.Annotations)
		}
	})

	// 🔴 THE RESTORE FACT SURVIVES A FLAGLESS RE-RUN. A restore only happens when
	// the cluster is created, so every re-run after it is flagless — and a wholesale
	// spec replacement would write restored=false over an instance whose databases
	// genuinely did come from an archive, quietly, on a green run.
	t.Run("a re-run cannot un-restore a restored instance", func(t *testing.T) {
		restoredAt := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
		dyn := declarationClient(declaredInstance(t, "prod", func(inst *dcv1beta1.Instance) {
			inst.Spec.Restored = true
			inst.Spec.RestoredAt = &restoredAt
		}))

		if err := writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v0.17.0"); err != nil {
			t.Fatalf("a flagless re-run over a restored instance was refused: %v", err)
		}

		inst := readBack(t, dyn, "prod")
		if !inst.Spec.Restored {
			t.Error("a re-run recorded a restored instance as never restored")
		}
		if inst.Spec.RestoredAt == nil || !inst.Spec.RestoredAt.Time.Equal(restoredAt.Time) {
			t.Errorf("the restore timestamp did not survive the re-run: %v", inst.Spec.RestoredAt)
		}
	})

	// Validation moved inside the seam, so the inner path is not a way around it:
	// a declaration nothing could later act on must be refused before it is stored.
	t.Run("an unusable declaration is refused before anything is written", func(t *testing.T) {
		dyn := declarationClient()
		spec := desiredSpec()
		spec.Provider = ""

		if err := writeInstanceCR(t.Context(), dyn, "prod", spec, "v0.17.0"); err == nil {
			t.Fatal("a declaration naming no provider was written")
		}
		if inst := readBack(t, dyn, "prod"); inst != nil {
			t.Error("the refused declaration was stored anyway")
		}
	})
}
