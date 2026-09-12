// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// Tests for the ONE reader that answers "which instances does this cluster hold?".
//
// 🔴 THE ANSWER DECIDES WHETHER A RUN MAY WRITE TO A CLUSTER SOMEBODY ELSE'S INSTANCE IS
// RUNNING IN, so the direction of every error matters more than its text: an absence
// must read as empty, and everything else must refuse. A reader that resolved "I could
// not tell" to "there is nothing here" would hand a second bootstrap a green light at
// exactly the moment it is most wrong.

// listFails makes the declaration client answer a List with err.
func listFails(dyn *dynamicfake.FakeDynamicClient, err error) {
	dyn.PrependReactor("list", "instances", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

// 🔴 THE VIRGIN CLUSTER, AND IT IS THE CASE THAT WOULD BREAK EVERY FIRST BOOTSTRAP. This
// reader runs BEFORE stepInstallCore puts the Instance CRD in, so on a cluster nobody has
// ever installed anything into, the declaration source must answer "nothing" rather than
// "I cannot look".
func TestAClusterWithNoInstanceCRDHoldsNoInstances(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		// The shape the API server produces when it answers a request for an unserved
		// collection with a Status. A LIST names no object, so this cannot be confused
		// with "that instance is not declared" the way a GET's 404 can.
		{"the API server 404s the collection", apierrors.NewNotFound(
			schema.GroupResource{Group: instanceGVR.Group, Resource: instanceGVR.Resource}, "")},
		// The shape a RESTMapper produces when it has never heard of the type.
		{"the type is not in the discovery document", &meta.NoResourceMatchError{
			PartialResource: instanceGVR.GroupVersion().WithResource(instanceGVR.Resource)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := declarationClient()
			listFails(dyn, tc.err)

			ids, err := declaredInstances(t.Context(), dyn)
			if err != nil {
				t.Fatalf("a cluster with no Instance CRD was reported as unreadable, which "+
					"refuses the first bootstrap of every new cluster: %v", err)
			}
			if len(ids) != 0 {
				t.Fatalf("a cluster with no Instance CRD named instances %v", ids)
			}
		})
	}
}

// 🔴 THE NEGATIVE CONTROL FOR THE PAIR ABOVE. "Tolerate a missing CRD" is one edit away
// from "tolerate everything", and a reader that swallowed every error would read a live
// instance's cluster as empty — the exact direction this whole check exists to stop.
func TestAClusterThatWillNotSayWhatItHoldsIsNotReadAsEmpty(t *testing.T) {
	dyn := declarationClient()
	listFails(dyn, apierrors.NewInternalError(errors.New("etcd is not answering")))

	ids, err := declaredInstances(t.Context(), dyn)
	if err == nil {
		t.Fatalf("a cluster that could not be read answered %v instead of refusing", ids)
	}
	if !strings.Contains(err.Error(), "over a live one") {
		t.Errorf("the refusal does not say what reading this as empty would cost: %v", err)
	}
}

// The declarations are the FIRST source for a reason: they are written at step 5, two
// steps before the credentials and three before the release. This is the half-built
// cluster a release-keyed reader would have called empty.
func TestEveryDeclaredInstanceIsNamed(t *testing.T) {
	dyn := declarationClient(
		declaredInstance(t, "zulu", nil),
		declaredInstance(t, "alpha", nil),
	)

	ids, err := declaredInstances(t.Context(), dyn)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"alpha", "zulu"}) {
		t.Fatalf("the declarations read as %v, want a sorted [alpha zulu]", ids)
	}
}

// A destroy that started and did not finish leaves a declaration behind and leaves part
// of the instance in the cluster. Bootstrapping a DIFFERENT instance into the remains is
// the case this refuses, so the declaration still counts.
func TestADeclarationBeingDestroyedStillCounts(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "alpha", func(i *dcv1beta1.Instance) {
		setPhase(i, dcv1beta1.PhaseDestroying)
	}))

	ids, err := declaredInstances(t.Context(), dyn)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"alpha"}) {
		t.Fatalf("an instance part-way through being destroyed read as %v", ids)
	}
}

// stampedSecret builds a Secret as writeOwnedSecret would have left it.
func stampedSecret(name, instance string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: infraNamespace}}
	setAnnotations(s, instance, "a-uid", "2026-01-01T00:00:00Z")
	return s
}

// 🔴 dc-system IS FULL OF SECRETS NOBODY HERE WROTE — CloudNativePG's certificates,
// Helm's own release records, whatever an operator put there. None of them says anything
// about an instance, and a reader that counted them would attribute a cluster to whatever
// the first one happened to be named after.
func TestOnlyTheSecretsDcctlMintedAttributeACluster(t *testing.T) {
	typed := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dc-rdb-ca", Namespace: infraNamespace}},
		stampedSecret("dc-rdb-app-credentials", "alpha"),
		stampedSecret("dc-tsdb-app-credentials", "alpha"),
	)

	ids, err := ownedSecretInstances(t.Context(), typed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"alpha"}) {
		t.Fatalf("the minted credentials read as %v, want a de-duplicated [alpha]", ids)
	}
}

// 🔴 "COULD NOT TELL" NEVER RESOLVES TO "NOTHING HERE". writeOwnedSecret stamps the
// managed-by marker and the instance name together or not at all, so a Secret carrying
// one and not the other has been edited by hand — which is precisely when guessing is
// worst.
func TestASecretDcctlMintedWithNoInstanceNameFailsClosed(t *testing.T) {
	s := stampedSecret("dc-rdb-app-credentials", "alpha")
	delete(s.Annotations, annotationOwnerName)
	typed := fake.NewSimpleClientset(s)

	ids, err := ownedSecretInstances(t.Context(), typed)
	if err == nil {
		t.Fatalf("a Secret dcctl minted for nobody read as %v instead of refusing", ids)
	}
	if !strings.Contains(err.Error(), "dc-rdb-app-credentials") {
		t.Errorf("the refusal does not name the Secret to look at: %v", err)
	}
}

// The infrastructure namespace does not exist until the apply creates it, so on a cluster
// this reader is asked about before that step it is simply not there.
func TestAMissingInfrastructureNamespaceHoldsNoInstances(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, infraNamespace)
	})

	ids, err := ownedSecretInstances(t.Context(), typed)
	if err != nil {
		t.Fatalf("a cluster whose %s namespace does not exist yet was unreadable: %v", infraNamespace, err)
	}
	if len(ids) != 0 {
		t.Fatalf("an absent namespace named instances %v", ids)
	}
}

// The negative control for the one above: every other failure to read that namespace
// still refuses.
func TestAnUnreadableInfrastructureNamespaceIsNotReadAsEmpty(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "secrets"}, "", errors.New("no"))
	})

	if ids, err := ownedSecretInstances(t.Context(), typed); err == nil {
		t.Fatalf("a namespace this run may not read answered %v instead of refusing", ids)
	}
}

// source is a test source that records whether it was consulted.
func source(what string, ids []string, err error, consulted *bool) instanceSource {
	return instanceSource{what: what, read: func() ([]string, error) {
		*consulted = true
		return ids, err
	}}
}

// 🔴 THE HALF-BUILT CLUSTER, AND IT IS WHY THIS READER IS COMPOSITE RATHER THAN THE HELM
// RELEASE. A bootstrap writes its declaration at step 5 and its release at step 8, so a
// run that dies between them leaves a cluster holding a declaration and NO release. Keyed
// on the release alone that cluster reads as empty, and a differently-named instance
// walks in.
func TestTheEarliestArtifactAnswersEvenWhenTheLaterOnesAreMissing(t *testing.T) {
	var declaredSeen, releaseSeen, secretsSeen bool
	held, err := firstAnsweringSource([]instanceSource{
		source("the declarations", []string{"alpha"}, nil, &declaredSeen),
		source("the release", nil, nil, &releaseSeen),
		source("the credentials", nil, nil, &secretsSeen),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(held.IDs, []string{"alpha"}) {
		t.Fatalf("a cluster holding a declaration and no release read as %v", held.IDs)
	}
	if held.Source != "the declarations" {
		t.Errorf("the answer does not say what it was read from: %q", held.Source)
	}
	if releaseSeen || secretsSeen {
		t.Errorf("a source that had already answered was followed by the later ones anyway "+
			"(release consulted: %v, credentials consulted: %v)", releaseSeen, secretsSeen)
	}
}

// 🔴 AN EMPTY ANSWER IS NOT AN ANSWER, and getting this backwards is the quiet way to
// break the whole reader: stopping at the first source that returns without error makes
// a virgin cluster's silent CRD the final word for every cluster.
func TestASourceWithNothingInItDoesNotStopTheWalk(t *testing.T) {
	var a, b, c bool
	held, err := firstAnsweringSource([]instanceSource{
		source("the declarations", nil, nil, &a),
		source("the release", nil, nil, &b),
		source("the credentials", []string{"alpha"}, nil, &c),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(held.IDs, []string{"alpha"}) {
		t.Fatalf("a cluster whose only surviving artifact is its minted credentials read as %v", held.IDs)
	}
	if !a || !b || !c {
		t.Errorf("the walk stopped early (declarations: %v, release: %v, credentials: %v)", a, b, c)
	}
}

// A source that cannot answer stops everything. Falling through to the next one would
// make an unattributable Helm release — the case releaseInstance refuses to guess at —
// silently become "the cluster is empty" whenever the later sources happened to be bare.
func TestASourceThatCannotAnswerStopsTheWalk(t *testing.T) {
	var a, b, c bool
	held, err := firstAnsweringSource([]instanceSource{
		source("the declarations", nil, nil, &a),
		source("the release", nil, errors.New("it does not say whose it is"), &b),
		source("the credentials", []string{"alpha"}, nil, &c),
	})
	if err == nil {
		t.Fatalf("an unattributable artifact answered %v instead of refusing", held.IDs)
	}
	if c {
		t.Error("the walk continued past a source that could not answer, so a later source " +
			"can overrule a refusal")
	}
}

// The truly empty cluster, which is every first bootstrap and must proceed.
func TestAClusterWithNothingInItAnswersEmpty(t *testing.T) {
	var a, b, c bool
	held, err := firstAnsweringSource([]instanceSource{
		source("the declarations", nil, nil, &a),
		source("the release", nil, nil, &b),
		source("the credentials", nil, nil, &c),
	})
	if err != nil {
		t.Fatalf("a virgin cluster was refused: %v", err)
	}
	if len(held.IDs) != 0 {
		t.Fatalf("a virgin cluster named instances %v", held.IDs)
	}
}

func TestOthersThanExcludesOnlyTheInstanceNamed(t *testing.T) {
	held := clusterInstances{IDs: []string{"alpha", "bravo"}}
	if got := held.othersThan("alpha"); !reflect.DeepEqual(got, []string{"bravo"}) {
		t.Fatalf("othersThan(alpha) = %v", got)
	}
	if got := held.othersThan("charlie"); !reflect.DeepEqual(got, []string{"alpha", "bravo"}) {
		t.Fatalf("othersThan(charlie) = %v", got)
	}
}
