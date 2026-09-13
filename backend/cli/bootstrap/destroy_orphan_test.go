// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The foreign-release refusal stops `dcctl destroy` from uninstalling somebody else's
// instance — and, on its own, strands the operator who met it: the local record survives
// the refusal, every re-run meets the same refusal, and no dcctl path clears it. These
// tests pin the half that makes the guard actionable, and the far more important half
// that makes it SAFE.
//
// 🔴 THE NEGATIVE CONTROLS ARE THE POINT. A version that cleared the local record
// whenever an uninstall was refused would satisfy the positive test below and would
// delete the tfstate, escrow pointer and cluster binding of a REAL instance whose release
// is mis-attributed for any other reason. Every test named "KeepsTheRecord" is that
// version failing.

// emptyCluster is a cluster with no declarations, no namespaces and no Secrets — the
// state a bootstrap that died before its fifth step leaves behind.
func emptyCluster() (*dynamicfake.FakeDynamicClient, *fake.Clientset) {
	return declarationClient(), fake.NewSimpleClientset()
}

func namespaceNamed(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"devicechain.io/instance": name},
	}}
}

func secretMintedBy(name, instance string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: infraNamespace,
		Annotations: map[string]string{
			annotationManagedBy: managedByDcctl,
			annotationOwnerName: instance,
			annotationOwnerUID:  "uid-" + instance,
		},
	}}
}

// keep runs the whole decision the way resolveForeignRelease does, so a test asserts on
// the answer the command acts on rather than on an intermediate list.
func keep(t *testing.T, dyn *dynamicfake.FakeDynamicClient, typed *fake.Clientset, instance string) error {
	t.Helper()
	found, err := instanceFootprint(t.Context(), dyn, typed, instance)
	return keepRecordReason(instance, found, err)
}

// The positive case, and the only one that ends in a deletion: a record naming an
// instance the cluster has never heard of.
func TestARecordIsClearedOnlyWhenEveryCheckSaysTheClusterHoldsNothingOfIt(t *testing.T) {
	dyn, typed := emptyCluster()
	if err := keep(t, dyn, typed, "b"); err != nil {
		t.Fatalf("a cluster holding nothing of instance %q still kept its record: %v", "b", err)
	}
}

// 🔴 THE CONTROL THE WHOLE SLICE EXISTS FOR. A namespace named for the instance is a
// real install whose release is mis-attributed; clearing its record would throw away the
// tfstate and the cluster binding of something still running.
func TestANamespaceNamedForTheInstanceKeepsTheRecord(t *testing.T) {
	dyn, _ := emptyCluster()
	typed := fake.NewSimpleClientset(namespaceNamed("b"))
	err := keep(t, dyn, typed, "b")
	if err == nil {
		t.Fatal("the record of an instance that still has its own namespace in the cluster was " +
			"cleared; that record is the only thing describing a live install")
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Errorf("the reason does not name what was found, so the operator cannot act on it: %v", err)
	}
}

// The declaration is written at step 5 of 11, before the configuration, the
// infrastructure and the release — so any run whose tfstate describes real objects wrote
// one. This is the check that makes losing tfstate unreachable.
func TestADeclarationInTheClusterKeepsTheRecord(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "b", nil))
	typed := fake.NewSimpleClientset()
	err := keep(t, dyn, typed, "b")
	if err == nil {
		t.Fatal("the record of an instance that is still declared on the cluster was cleared")
	}
	if !strings.Contains(err.Error(), "declaration") {
		t.Errorf("the reason does not name what was found: %v", err)
	}
}

// The Secrets dcctl mints live in the shared infrastructure namespace under names that
// carry no instance, so they are found by their ownership annotation.
func TestSecretsTheInstanceMintedKeepTheRecord(t *testing.T) {
	dyn, _ := emptyCluster()
	typed := fake.NewSimpleClientset(secretMintedBy("dc-rdb-app-credentials", "b"))
	err := keep(t, dyn, typed, "b")
	if err == nil {
		t.Fatal("the record of an instance that still owns credentials in the cluster was cleared; " +
			"those credentials exist nowhere else")
	}
	if !strings.Contains(err.Error(), "dc-rdb-app-credentials") {
		t.Errorf("the reason does not name the Secret it found: %v", err)
	}
}

// 🔴 THE COUNTERWEIGHT TO THE THREE ABOVE. A check that matched ANY namespace or ANY
// owned Secret would pass all of them while making the clearing path unreachable — the
// stranded record could never be removed, which is the defect this change is fixing.
// Everything here belongs to the OTHER instance, the one whose release was found.
func TestAnotherInstancesObjectsAreNotThisInstancesFootprint(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "a", nil))
	typed := fake.NewSimpleClientset(
		namespaceNamed("a"),
		secretMintedBy("dc-rdb-app-credentials", "a"),
		secretMintedBy("dc-nats-tls", "a"),
	)
	if err := keep(t, dyn, typed, "b"); err != nil {
		t.Fatalf("instance %q was read as having a footprint built entirely out of instance %q's "+
			"objects, so its stranded record could never be cleared: %v", "b", "a", err)
	}
}

// 🔴 "WE COULD NOT TELL" MUST NEVER RESOLVE TO THE DESTRUCTIVE ANSWER. Each subtest
// breaks exactly one of the three reads; every one of them must keep the record, because
// a record is removed on a positive finding of absence and on nothing else.
func TestAClusterThatCannotBeReadKeepsTheRecord(t *testing.T) {
	boom := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	}

	t.Run("the declaration cannot be read", func(t *testing.T) {
		dyn := declarationClient()
		dyn.PrependReactor("get", "instances", boom)
		if err := keep(t, dyn, fake.NewSimpleClientset(), "b"); err == nil {
			t.Fatal("a declaration read that failed was taken as proof there is no declaration")
		}
	})

	t.Run("the namespace cannot be read", func(t *testing.T) {
		dyn, _ := emptyCluster()
		typed := fake.NewSimpleClientset()
		typed.PrependReactor("get", "namespaces", boom)
		if err := keep(t, dyn, typed, "b"); err == nil {
			t.Fatal("a namespace read that failed was taken as proof there is no namespace")
		}
	})

	t.Run("the infrastructure Secrets cannot be listed", func(t *testing.T) {
		dyn, _ := emptyCluster()
		typed := fake.NewSimpleClientset()
		typed.PrependReactor("list", "secrets", boom)
		if err := keep(t, dyn, typed, "b"); err == nil {
			t.Fatal("a Secret list that failed was taken as proof the instance minted none")
		}
	})
}

// 🔴 THE CALLER MATCHES ON TYPE, NOT ON WORDS. destroyInstanceOnly answers the
// foreign-release refusal and no other failure; recognising it by its message would start
// clearing local state the day the wording changed, or the day an unrelated error
// happened to contain the same words.
func TestTheForeignReleaseRefusalIsRecognisableByTypeAndCarriesBothNames(t *testing.T) {
	err := uninstallRefusalReason("a", "b")
	var foreign *foreignReleaseError
	if !errors.As(err, &foreign) {
		t.Fatalf("the refusal is not recognisable to its caller (%T), so `dcctl destroy` cannot "+
			"tell it from a failed uninstall and the stranded record stays forever", err)
	}
	if foreign.Owner != "a" || foreign.Instance != "b" {
		t.Fatalf("got owner %q, instance %q; want \"a\", \"b\"", foreign.Owner, foreign.Instance)
	}
}

// 🔴 THE REFUSAL HAS TO BE CONNECTED TO THE THING THAT ANSWERS IT. Written inline in
// destroyInstanceOnly this branch sits behind a real Helm uninstall against a real
// cluster, so a version that dropped it would break no test at all: the guard would still
// refuse, correctly, and the operator would still be stranded with a record nothing can
// clear — which is the entire defect. This asserts the call HAPPENS.
func TestTheForeignReleaseRefusalIsHandedToTheThingThatResolvesIt(t *testing.T) {
	resolved := false
	out := uninstallOutcome(uninstallRefusalReason("a", "b"), func() error {
		resolved = true
		return errInstanceNotInCluster
	})
	if !resolved {
		t.Fatal("the foreign-release refusal was never handed to resolveForeignRelease, so the " +
			"stranded local record it leaves behind can never be cleared")
	}
	if !errors.Is(out, errInstanceNotInCluster) {
		t.Fatalf("the resolver's answer was not returned to the caller: %v", out)
	}
}

// Only the foreign-release refusal is answerable. A Helm failure, a dead API server, a
// timeout — none of them say the instance is absent, and treating one as if it did would
// delete local state on a transient error.
func TestNoOtherUninstallFailureIsReadAsAForeignRelease(t *testing.T) {
	resolved := false
	err := uninstallOutcome(errors.New("timed out waiting for the release to be deleted"), func() error {
		resolved = true
		return nil
	})
	if resolved {
		t.Fatal("an unrelated uninstall failure was routed to the path that removes local state")
	}
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a failed uninstall did not come back as a failure: %v", err)
	}
}

// 🔴 THE TWO CALLERS OF destroyInstanceOnly READ ITS RESULT FOR DIFFERENT PURPOSES, AND
// THE SET THEY READ IT AGAINST HAS TO BE THE SAME ONE. A failure that leaked into this
// set would be reported as a completed destroy; an outcome that fell out of it would make
// `dcctl destroy --keep-cluster` exit non-zero on a job it finished, and would send the
// adopted-cluster branch on to remove the local state a second time and close with
// "uninstalled" over a cluster nothing touched.
func TestOnlyAnOutcomeCountsAsNoFurtherWorkAndAFailureNeverDoes(t *testing.T) {
	for name, err := range map[string]error{
		"the operator declined":     errDestroyAborted,
		"there was nothing here":    errInstanceNotInCluster,
		"and still so once wrapped": fmt.Errorf("uninstalling release: %w", errInstanceNotInCluster),
		"and still so once wrapped twice": fmt.Errorf("destroy: %w",
			fmt.Errorf("uninstalling release: %w", errDestroyAborted)),
	} {
		t.Run(name, func(t *testing.T) {
			if !destroyNeedsNoFurtherWork(err) {
				t.Fatalf("a completed outcome was read as a failure: %v", err)
			}
		})
	}
	for name, err := range map[string]error{
		"a real uninstall failure":          errors.New("timed out waiting for the release to be deleted"),
		"a foreign release nobody resolved": uninstallRefusalReason("a", "b"),
	} {
		t.Run(name, func(t *testing.T) {
			if destroyNeedsNoFurtherWork(err) {
				t.Fatalf("a failure was read as a completed destroy: %v", err)
			}
		})
	}
}

// The line printed before the record is removed names the instance that is actually
// installed, because that is what the operator has to go and look at.
func TestTheOwnerIsReadBackOutOfTheRefusalForTheMessage(t *testing.T) {
	if got := foreignOwner(uninstallRefusalReason("production", "staging"), "fallback"); got != "production" {
		t.Fatalf("got %q, want %q", got, "production")
	}
	if got := foreignOwner(errors.New("something else"), "fallback"); got != "fallback" {
		t.Fatalf("an error that names no owner produced %q rather than the fallback", got)
	}
}
