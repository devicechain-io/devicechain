// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// stubNamespacePrecheck points the precheck's one cluster call at a fake holding objs,
// and hands back the fake so a test can read what the precheck did to it.
func stubNamespacePrecheck(t *testing.T, objs ...runtime.Object) *fake.Clientset {
	t.Helper()
	c := fake.NewSimpleClientset(objs...)
	orig := namespacePrecheckClient
	t.Cleanup(func() { namespacePrecheckClient = orig })
	namespacePrecheckClient = func(string) (kubernetes.Interface, error) { return c, nil }
	return c
}

// writesIn reports every action that changed something, so a test can say "nothing was
// written" about a whole clientset rather than about the one object it thought to check.
//
// 🔑 A `get` THAT RETURNS NOTFOUND IS NOT A WRITE, and neither is a `list`. Everything
// else in the fake's record is: create, update, patch and delete all mutate, and the
// point of asking this way is that a future write nobody anticipated is caught by the
// same assertion.
func writesIn(c *fake.Clientset) []string {
	var out []string
	for _, a := range c.Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			out = append(out, fmt.Sprintf("%s %s", a.GetVerb(), a.GetResource().Resource))
		}
	}
	return out
}

// labelledNamespace builds the escape hatch: a namespace an operator made and labelled
// as an instance's by hand. The two arguments are deliberately separate — name is the
// NAMESPACE, instance is the devicechain.io/instance label VALUE, and they are not the
// same string.
func labelledNamespace(name, instance string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"devicechain.io/instance": instance},
	}}
}

// 🔴 THE ROOT-KEY PROPERTY, AND IT IS THE REASON THIS WHOLE CHANGE EXISTS.
//
// The seam is the last thing between a namespace that is not this instance's and
// writeMintedSecrets, which is one line below its only two callers. What is asserted is
// not "an error came back" but that the clientset was never asked to change ANYTHING —
// because a refusal that has already written half the credentials is the failure that was
// measured on a real cluster, not a hypothetical one: the secret-store root key, the
// broker's TLS private key and four database credentials, all in a namespace `dcctl
// destroy` then correctly refused to touch.
func TestTheSeamRefusesAForeignNamespaceWithoutWritingAnything(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace("monitoring"),
	}})

	err := ensureNamespaceForRelease(context.Background(), c, "monitoring", "dc-monitoring", "default")
	if err == nil {
		t.Fatal("a namespace that is not this instance's was accepted")
	}
	if wrote := writesIn(c); len(wrote) > 0 {
		t.Errorf("the refusal wrote to the cluster anyway: %v.\n"+
			"  Anything written before this point lands in a namespace this instance does not "+
			"own, and destroy cannot remove it", wrote)
	}
}

// The other side of the same call, and the one that makes the refusal safe to ship: the
// namespace dcctl itself creates on a fresh bootstrap is the instance's, and a re-run
// over it has to keep working. Without this the guard above would be indistinguishable
// from a bootstrap that can never resume.
func TestANamespaceThisInstanceOwnsIsAccepted(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   instanceNamespace("dctest"),
		Labels: map[string]string{"devicechain.io/instance": "dctest", "app.kubernetes.io/managed-by": "Helm"},
		Annotations: map[string]string{
			"meta.helm.sh/release-name":      "dc-dctest",
			"meta.helm.sh/release-namespace": "default",
		},
	}})

	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc-dctest", "default"); err != nil {
		t.Fatalf("this instance's own namespace was refused, so no bootstrap could ever resume: %v", err)
	}
	// Nothing to stamp, so nothing should have been sent. An Update here would be a
	// write on the resume path of every single bootstrap.
	if wrote := writesIn(c); len(wrote) > 0 {
		t.Errorf("a namespace that already carries all three adoption keys was written to anyway: %v", wrote)
	}
}

// 🔴 THE HAND-LABELLED NAMESPACE IS THE ESCAPE HATCH, AND WITHOUT THE STAMP IT IS A TRAP.
//
// The refusal above tells an operator to label the namespace; a namespace they made
// themselves has none of Helm's adoption metadata, so labelling it gets them past the
// seam and into exactly the strand the seam was added to close — Helm refusing at the
// install, after the credentials have landed. Accepting it therefore has to include
// giving it the three keys.
func TestALabelledNamespaceIsGivenTheMetadataHelmAdoptsItWith(t *testing.T) {
	c := fake.NewSimpleClientset(labelledNamespace(instanceNamespace("dctest"), "dctest"))

	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc-dctest", "default"); err != nil {
		t.Fatalf("a namespace labelled as this instance's was refused: %v", err)
	}

	ns, err := c.CoreV1().Namespaces().Get(context.Background(), instanceNamespace("dctest"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Literals, for the reason TestTheNamespaceIsCreatedInAShapeHelmCanAdopt states: these
	// are Helm's names, not ours, and reading them from our constants would follow a
	// rename straight past the only thing being checked.
	if got := ns.Labels["app.kubernetes.io/managed-by"]; got != "Helm" {
		t.Errorf("app.kubernetes.io/managed-by is %q, so the install still refuses this namespace "+
			"— after the root key has been written into it", got)
	}
	if got := ns.Annotations["meta.helm.sh/release-name"]; got != "dc-dctest" {
		t.Errorf("meta.helm.sh/release-name is %q, not the release this instance installs under", got)
	}
	if got := ns.Annotations["meta.helm.sh/release-namespace"]; got != "default" {
		t.Errorf("meta.helm.sh/release-namespace is %q, not where the release record lives", got)
	}
	if ns.Labels["devicechain.io/instance"] != "dctest" {
		t.Error("the stamp dropped the label that made the namespace this instance's in the first place")
	}
}

// 🔴 THE STAMP FILLS GAPS; IT DOES NOT OVERWRITE A CLAIM. A meta.helm.sh value that says
// another release is that release's claim on the namespace, and replacing it would make
// Helm's ownership check pass for a release that does not own it — a loud refusal turned
// into a silent adoption. Left alone, Helm refuses and names both values.
func TestTheStampDoesNotOverwriteAnotherReleasesClaim(t *testing.T) {
	ns := labelledNamespace(instanceNamespace("dctest"), "dctest")
	ns.Annotations = map[string]string{"meta.helm.sh/release-name": "their-release"}
	c := fake.NewSimpleClientset(ns)

	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc-dctest", "default"); err != nil {
		t.Fatalf("a namespace labelled as this instance's was refused: %v", err)
	}

	got, err := c.CoreV1().Namespaces().Get(context.Background(), instanceNamespace("dctest"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Annotations["meta.helm.sh/release-name"] != "their-release" {
		t.Errorf("meta.helm.sh/release-name was rewritten to %q: dcctl would be taking a namespace "+
			"another release holds instead of letting Helm say so",
			got.Annotations["meta.helm.sh/release-name"])
	}
}

// 🔴 THE PRECHECK IS THE SAME VERDICT FOUR STEPS EARLIER, AND IT MUST BE A PURE READ.
//
// It runs inside stepCheckClusterSingletons, before the operator install and before the
// Instance declaration. Anything it wrote would be written by a step whose entire value is
// that it writes nothing — and would be left behind by the refusal it is about to raise,
// because no unwind covers it.
func TestThePrecheckRefusesAForeignNamespaceWithoutWritingAnything(t *testing.T) {
	c := stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace("beta")}})

	err := precheckInstanceNamespace(context.Background(), &State{Instance: "beta"})
	var refusal *ErrNamespaceUnavailable
	if !errors.As(err, &refusal) {
		t.Fatalf("a namespace that is not this instance's was not refused as one: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("namespace %q", instanceNamespace("beta"))) {
		t.Errorf("the refusal does not name the namespace it is about: %v", err)
	}
	if wrote := writesIn(c); len(wrote) > 0 {
		t.Errorf("the precheck wrote to the cluster: %v", wrote)
	}
}

// A namespace this instance owns is not refused here either — the precheck must let a
// resumed bootstrap through, or it refuses every re-run of every instance.
func TestThePrecheckAcceptsThisInstancesOwnNamespace(t *testing.T) {
	stubNamespacePrecheck(t, labelledNamespace(instanceNamespace("beta"), "beta"))
	if err := precheckInstanceNamespace(context.Background(), &State{Instance: "beta"}); err != nil {
		t.Fatalf("this instance's own namespace was refused before the run started: %v", err)
	}
}

// And an absent namespace is the ordinary case: nothing exists, nothing objects.
func TestThePrecheckAcceptsAnAbsentNamespace(t *testing.T) {
	stubNamespacePrecheck(t)
	if err := precheckInstanceNamespace(context.Background(), &State{Instance: "beta"}); err != nil {
		t.Fatalf("a fresh bootstrap was refused: %v", err)
	}
}

// 🔴 "COULD NOT TELL" IS NOT "FREE", AND IT IS NOT A REFUSAL EITHER. A read that failed
// has to stop the run — carrying on would write the root key into a namespace nobody
// checked — but it must NOT come back typed, because the typed refusals are what the
// command layer unwinds the local record on, and a cluster that would not answer may be
// one this run has already built half an instance on.
func TestAnUnreadableNamespaceStopsTheRunWithoutBeingARefusal(t *testing.T) {
	c := stubNamespacePrecheck(t)
	c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})

	err := precheckInstanceNamespace(context.Background(), &State{Instance: "beta"})
	if err == nil {
		t.Fatal("a cluster that would not say whether the namespace is free was read as free")
	}
	var refusal *ErrNamespaceUnavailable
	if errors.As(err, &refusal) {
		t.Error("a failed read came back as a refusal, so the command layer would delete the " +
			"local record of a run that may have built something")
	}
}

// The step, not just the precheck: a namespace that is not this instance's stops the
// bootstrap at step 4, and stops it TYPED — which is what the command layer takes the
// local record back on. The precheck tests above prove the verdict; this proves the step
// carries it out rather than reading it and going on.
func TestTheSingletonStepRefusesANamespaceThisInstanceDoesNotOwn(t *testing.T) {
	stubSingletons(t, clusterSingletons{}, nil)
	stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace("beta")}})

	err := stepCheckClusterSingletons(context.Background(), &State{
		Instance: "beta", IngressHost: "beta.localhost", Values: map[string]string{}})
	var refusal *ErrNamespaceUnavailable
	if !errors.As(err, &refusal) {
		t.Fatalf("step 4 let a bootstrap through into a namespace that is not this instance's, so "+
			"the refusal lands at the infrastructure apply instead — after the root key: %v", err)
	}
}

// 🔴 THE REHEARSAL IS WHERE AN OPERATOR FINDS OUT WHETHER THEIR ARGUMENTS ARE USABLE, SO
// IT HAS TO SAY THE NAMESPACE REFUSAL IS COMING. Under --dry-run the step returns nil
// whatever it found, so its OUTPUT is the whole of what it tells anybody — and a step
// that stopped printing this would leave a rehearsal that reads clean in front of a real
// run that stops dead at step 4. Nothing else can see it: the return value is nil in
// every case below, which is why this asserts on what was printed.
func TestTheDryRunRehearsesTheNamespaceVerdict(t *testing.T) {
	// The host half is stubbed to "nothing held, read fine" throughout, so every line
	// these cases see is the namespace half's.
	stubSingletons(t, clusterSingletons{}, nil)
	rehearse := func(t *testing.T) string {
		t.Helper()
		var err error
		out := captureOutput(t, func() {
			err = stepCheckClusterSingletons(context.Background(), &State{
				Instance: "beta", IngressHost: "beta.localhost", DryRun: true, Values: map[string]string{}})
		})
		if err != nil {
			t.Fatalf("a rehearsal returned an error instead of rehearsing one: %v", err)
		}
		return out
	}

	// A namespace that is not this instance's: the rehearsal does not refuse, it says what
	// a real run would refuse over, in the refusal's own words.
	stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace("beta")}})
	foreign := rehearse(t)
	if !strings.Contains(foreign, "REFUSE") ||
		!strings.Contains(foreign, fmt.Sprintf("namespace %q already exists", instanceNamespace("beta"))) {
		t.Errorf("a rehearsal against a namespace this instance does not own printed:\n%s"+
			"  It has to name the refusal a real run raises. A rehearsal that says nothing about "+
			"the namespace sends an operator into a bootstrap that stops at step 4", foreign)
	}

	// 🔴 AND A READ THAT FAILED IS THE OTHER ARM, WHICH MUST NOT READ LIKE EITHER OF THE
	// OTHER TWO. The cluster did not say the namespace is somebody else's — it said
	// nothing — so calling it a refusal would be a claim nobody made, and saying nothing
	// would make an unanswered question look like an answered one.
	c := stubNamespacePrecheck(t)
	c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	unread := rehearse(t)
	if strings.Contains(unread, "REFUSE") {
		t.Errorf("a namespace read that failed was rehearsed as a refusal:\n%s"+
			"  Nothing on the cluster said this namespace is taken", unread)
	}
	if !strings.Contains(unread, fmt.Sprintf("namespace %q", instanceNamespace("beta"))) ||
		!strings.Contains(unread, "forbidden") {
		t.Errorf("a rehearsal whose namespace read failed printed:\n%s"+
			"  It has to say which read failed and why. The line missing from the rehearsal is "+
			"the one that would have explained the real run's stop", unread)
	}

	// 🔴 THE CONTROL FOR BOTH. Each assertion above is on a line being PRESENT, and a step
	// that printed one unconditionally would satisfy them while rehearsing nothing. A
	// namespace that is free is the ordinary case and has nothing to say.
	stubNamespacePrecheck(t)
	if free := rehearse(t); strings.Contains(free, "namespace") {
		t.Errorf("a rehearsal against a free namespace talked about it anyway:\n%s"+
			"  Then the two assertions above pass whatever the step actually decided", free)
	}
}
