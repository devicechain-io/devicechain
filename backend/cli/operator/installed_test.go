// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	dck8s "github.com/devicechain-io/dc-k8s/config"
)

// crdClient is a cluster carrying the given CRDs, each with the given identity
// annotation ("" for a CRD carrying none).
func crdClient(t *testing.T, stamps map[string]string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		crdGVR: "CustomResourceDefinitionList",
	}
	var objs []runtime.Object
	for name, id := range stamps {
		o := &unstructured.Unstructured{}
		o.SetGroupVersionKind(crdGVR.GroupVersion().WithKind("CustomResourceDefinition"))
		o.SetName(name)
		if id != "" {
			o.SetAnnotations(map[string]string{dck8s.IdentityAnnotation: id})
		}
		objs = append(objs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
}

// theOverlay is the real rendered stream, so the CRD names under test are the ones
// dcctl actually installs rather than names invented here.
func theOverlay(t *testing.T) []byte {
	t.Helper()
	m, err := Render(ImageRef("localhost:5000", "dev"))
	if err != nil {
		t.Fatalf("rendering the operator overlay: %v", err)
	}
	return m
}

func declared(t *testing.T) []string {
	t.Helper()
	names, err := DeclaredCRDs(theOverlay(t))
	if err != nil {
		t.Fatalf("reading the declared CRDs: %v", err)
	}
	if len(names) < 2 {
		t.Fatalf("the overlay declares %v; this test's whole point is that there is more "+
			"than one, so a half-applied operator is a state that can exist", names)
	}
	return names
}

// 🔴 A CLUSTER WITH NO CRDs IS "NOT INSTALLED", AND THAT IS A DIFFERENT ANSWER FROM
// EVERY OTHER ONE. It is the answer that sends an operator to `dcctl install`.
func TestAClusterWithNoDefinitionsIsNotInstalled(t *testing.T) {
	have, err := ReadInstalled(t.Context(), crdClient(t, nil), theOverlay(t))
	if err != nil {
		t.Fatalf("reading an empty cluster: %v", err)
	}
	if have.Present {
		t.Fatal("a cluster with no definitions reported an operator present")
	}
	if len(have.Missing) == 0 {
		t.Error("the answer names nothing as missing, so the refusal cannot say what is absent")
	}
	if have.Stamped() || have.Matches("anything") {
		t.Error("an absent operator matched something")
	}
}

// 🔴 EVERY DECLARED CRD MUST BE PRESENT. An apply interrupted between two of them is
// an ordinary way to reach this state, and reporting the first one found as the whole
// install would wave a half-applied operator through.
func TestAHalfAppliedOperatorIsNotInstalled(t *testing.T) {
	names := declared(t)
	have, err := ReadInstalled(t.Context(), crdClient(t, map[string]string{names[0]: "abc"}), theOverlay(t))
	if err != nil {
		t.Fatalf("reading a half-applied cluster: %v", err)
	}
	if have.Present {
		t.Fatalf("a cluster carrying only %q reported a complete operator", names[0])
	}
	if len(have.Missing) != len(names)-1 {
		t.Errorf("the answer names %v as missing; the cluster is short %d definitions",
			have.Missing, len(names)-1)
	}
}

// 🔴 PRESENT BUT UNSTAMPED IS NOT A MISMATCH. `make deploy` pipes the kustomize CLI
// to kubectl and never passes through the stamping code, so a maintainer's
// hand-installed operator looks exactly like this — and telling them their correct
// operator is the wrong one sends them to a remedy that would overwrite a deliberate
// choice. It must be distinguishable, which is what Stamped is for.
func TestAHandInstalledOperatorIsPresentButCannotBeVouchedFor(t *testing.T) {
	stamps := map[string]string{}
	for _, n := range declared(t) {
		stamps[n] = ""
	}
	have, err := ReadInstalled(t.Context(), crdClient(t, stamps), theOverlay(t))
	if err != nil {
		t.Fatalf("reading a hand-installed cluster: %v", err)
	}
	if !have.Present {
		t.Fatal("a cluster carrying every definition reported no operator")
	}
	if have.Stamped() {
		t.Fatal("an unstamped operator reported itself as checkable")
	}
	if have.Matches("") {
		t.Fatal("an unstamped operator MATCHED an empty identity — every unstamped cluster " +
			"would then agree with every other, and the guard would pass everything")
	}
}

// The ordinary case, and its counterweight: a stamped operator matches its own
// identity and nothing else.
func TestAStampedOperatorMatchesOnlyItself(t *testing.T) {
	stamps := map[string]string{}
	for _, n := range declared(t) {
		stamps[n] = "the-identity"
	}
	have, err := ReadInstalled(t.Context(), crdClient(t, stamps), theOverlay(t))
	if err != nil {
		t.Fatalf("reading a stamped cluster: %v", err)
	}
	if !have.Stamped() || have.Identity != "the-identity" {
		t.Fatalf("the cluster's operator read as %+v", have)
	}
	if !have.Matches("the-identity") {
		t.Error("a cluster did not match the identity it carries")
	}
	if have.Matches("a-different-identity") {
		t.Error("a cluster matched an identity it does not carry, so the guard cannot refuse")
	}
}

// 🔴 DEFINITIONS THAT DISAGREE ARE THEIR OWN ANSWER AND MUST NOT BE AVERAGED. Picking
// either stamp would describe a cluster that is not in the state the answer claims.
func TestDefinitionsThatDisagreeAreRefused(t *testing.T) {
	names := declared(t)
	t.Run("one stamped, one not", func(t *testing.T) {
		stamps := map[string]string{names[0]: "abc"}
		for _, n := range names[1:] {
			stamps[n] = ""
		}
		if _, err := ReadInstalled(t.Context(), crdClient(t, stamps), theOverlay(t)); err == nil {
			t.Fatal("a partly stamped operator was accepted")
		}
	})
	t.Run("two different stamps", func(t *testing.T) {
		stamps := map[string]string{}
		for i, n := range names {
			stamps[n] = string(rune('a' + i))
		}
		_, err := ReadInstalled(t.Context(), crdClient(t, stamps), theOverlay(t))
		if err == nil {
			t.Fatal("definitions installed by two different operators were accepted")
		}
		if !strings.Contains(err.Error(), "disagree") {
			t.Errorf("the refusal does not say what is wrong: %v", err)
		}
	})
}

// 🔴 THE NEGATIVE CONTROL FOR THE ABSENCE HANDLING. "A NotFound means not installed"
// is one edit away from "any error means not installed", and a cluster that will not
// answer would then be reported as having no operator — sending an operator to run
// `dcctl install` against a cluster whose operator is in fact there.
func TestAClusterThatWillNotAnswerIsNotReadAsUninstalled(t *testing.T) {
	dyn := crdClient(t, nil)
	dyn.PrependReactor("get", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("etcd is not answering"))
		})

	have, err := ReadInstalled(t.Context(), dyn, theOverlay(t))
	if err == nil {
		t.Fatalf("a cluster that could not be read answered %+v instead of refusing", have)
	}
	if !strings.Contains(err.Error(), "is not a cluster without one") {
		t.Errorf("the refusal does not say what reading this as uninstalled would cost: %v", err)
	}
}

// A RESTMapper that has never heard of the type is the other shape of "absent", and
// it must land in the same place as a plain NotFound rather than as a hard error.
func TestATypeMissingFromDiscoveryReadsAsAbsent(t *testing.T) {
	dyn := crdClient(t, nil)
	dyn.PrependReactor("get", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, &meta.NoResourceMatchError{PartialResource: crdGVR}
		})

	have, err := ReadInstalled(t.Context(), dyn, theOverlay(t))
	if err != nil {
		t.Fatalf("a cluster whose discovery document lacks the type was an error: %v", err)
	}
	if have.Present {
		t.Fatal("it reported an operator present")
	}
}
