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

// 🔴 THE ROOT-KEY PROPERTY, AND IT IS THE REASON THIS SEAM EXISTS.
//
// The seam is the last thing between a namespace that is not this instance's and
// writeMintedSecrets, which is one line below its only two callers. What is asserted is
// not "an error came back" but that the clientset was never asked to change ANYTHING —
// because a refusal that has already written half the credentials is the failure that was
// measured on a real cluster, not a hypothetical one: the secret-store root key, the
// broker's TLS private key and four database credentials, all in a namespace `dcctl
// destroy` then correctly refused to touch.
//
// 🔑 WHAT REACHES THIS SEAM CHANGED WHEN AN INSTANCE'S NAMESPACE GAINED A PREFIX, AND THE
// FIXTURE CHANGED WITH IT. It was built around an instance named `monitoring` on a cluster
// whose monitoring stack lives there — the case that was actually measured. That case is
// now unrepresentable, and has a test of its own below. What still reaches here is an
// earlier generation of this same instance whose destroy did not finish, because nothing
// but dcctl makes a namespace under the prefix. The property being pinned is unchanged;
// only the story that gets you to it is.
func TestTheSeamRefusesAForeignNamespaceWithoutWritingAnything(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: InstanceNamespace("dctest"),
	}})

	err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc-dctest", "default")
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
		Name:   InstanceNamespace("dctest"),
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
	c := fake.NewSimpleClientset(labelledNamespace(InstanceNamespace("dctest"), "dctest"))

	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc-dctest", "default"); err != nil {
		t.Fatalf("a namespace labelled as this instance's was refused: %v", err)
	}

	ns, err := c.CoreV1().Namespaces().Get(context.Background(), InstanceNamespace("dctest"), metav1.GetOptions{})
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
	ns := labelledNamespace(InstanceNamespace("dctest"), "dctest")
	ns.Annotations = map[string]string{"meta.helm.sh/release-name": "their-release"}
	c := fake.NewSimpleClientset(ns)

	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc-dctest", "default"); err != nil {
		t.Fatalf("a namespace labelled as this instance's was refused: %v", err)
	}

	got, err := c.CoreV1().Namespaces().Get(context.Background(), InstanceNamespace("dctest"), metav1.GetOptions{})
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
	c := stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InstanceNamespace("beta")}})

	err := precheckInstanceNamespace(context.Background(), &State{Instance: "beta"})
	var refusal *ErrNamespaceUnavailable
	if !errors.As(err, &refusal) {
		t.Fatalf("a namespace that is not this instance's was not refused as one: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("namespace %q", InstanceNamespace("beta"))) {
		t.Errorf("the refusal does not name the namespace it is about: %v", err)
	}
	if wrote := writesIn(c); len(wrote) > 0 {
		t.Errorf("the precheck wrote to the cluster: %v", wrote)
	}
}

// A namespace this instance owns is not refused here either — the precheck must let a
// resumed bootstrap through, or it refuses every re-run of every instance.
func TestThePrecheckAcceptsThisInstancesOwnNamespace(t *testing.T) {
	stubNamespacePrecheck(t, labelledNamespace(InstanceNamespace("beta"), "beta"))
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
	stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InstanceNamespace("beta")}})

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
	stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InstanceNamespace("beta")}})
	foreign := rehearse(t)
	if !strings.Contains(foreign, "REFUSE") ||
		!strings.Contains(foreign, fmt.Sprintf("namespace %q already exists", InstanceNamespace("beta"))) {
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
	if !strings.Contains(unread, fmt.Sprintf("namespace %q", InstanceNamespace("beta"))) ||
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

// 🔴🔴 THE COLLISION THIS PREFIX EXISTS TO RETIRE, ASSERTED AS IMPOSSIBLE RATHER THAN
// REFUSED.
//
// Measured on a real cluster: `dcctl bootstrap local monitoring`, on a cluster whose
// monitoring stack lives in the `monitoring` namespace, wrote the instance's secret-store
// root key, the broker's TLS private key and four database credentials INTO that namespace
// and installed the broker and the event store there before Helm's ownership check refused
// it. `dcctl destroy` then correctly declined to delete a namespace not labelled as the
// instance's, so it reported success and left the credentials behind, and the next
// bootstrap found them and advised running destroy.
//
// Seven namespaces `dcctl install` creates were valid instance names. Under the prefix an
// instance called `monitoring` is built in `dci-monitoring`, so the two names are in
// different sets and the collision cannot be expressed — which is a stronger claim than
// "it is refused", and is the whole reason a reserved-name list was not built instead.
//
// 🔑 THIS ASSERTS ACCEPTANCE, WHICH IS THE ONLY WAY TO SEE THE DIFFERENCE. A test that the
// collision is refused would pass just as well under a list of reserved names, and would
// also pass if the prefix quietly stopped being applied. Requiring the build to PROCEED,
// with the cluster's own namespace untouched beside it, fails under both.
func TestAnInstanceNamedAfterAClusterNamespaceIsBuiltBesideItNotIntoIt(t *testing.T) {
	for _, owned := range []string{
		"monitoring", "dc-system", "cert-manager", "cnpg-system", "ingress-nginx", "default",
	} {
		t.Run(owned, func(t *testing.T) {
			// The cluster's own namespace, exactly as `dcctl install` leaves it: present,
			// and carrying no devicechain.io/instance label.
			c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   owned,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "dcctl"},
			}})

			if err := ensureNamespaceForRelease(
				context.Background(), c, owned, "dc-"+owned, "default"); err != nil {
				t.Fatalf("an instance named %q was refused because the cluster owns a namespace by "+
					"that name: %v.\n  The prefix exists so that name is not the namespace — if this "+
					"refuses, the two sets have met again", owned, err)
			}

			// And it was built beside that namespace rather than into it.
			built, err := c.CoreV1().Namespaces().Get(
				context.Background(), InstanceNamespace(owned), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("namespace %q was not created: %v", InstanceNamespace(owned), err)
			}
			if built.Labels[instanceNamespaceLabel] != owned {
				t.Errorf("namespace %q carries %s=%q, want %q", built.Name, instanceNamespaceLabel,
					built.Labels[instanceNamespaceLabel], owned)
			}
			// 🔴 The cluster's namespace must be untouched. This is the assertion that
			// would have failed against the measured defect.
			cluster, err := c.CoreV1().Namespaces().Get(context.Background(), owned, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("the cluster's own namespace %q is gone: %v", owned, err)
			}
			if _, claimed := cluster.Labels[instanceNamespaceLabel]; claimed {
				t.Errorf("the cluster's own namespace %q was claimed as instance %q's: an instance "+
					"named after a cluster component took its namespace over", owned, owned)
			}
		})
	}
}

// 🔴🔴 AN INSTANCE BUILT BEFORE THE PREFIX IS STILL THERE, AND THIS IS WHAT EVERY OTHER
// COMMAND ASKS. Reading only the prefixed namespace answers "nothing here" about a live
// instance, and the callers act on that answer: `dcctl upgrade` reports the instance was
// never finished, and stepRefuseRebuild stops refusing — so a re-run mints a fresh root key
// and re-sets the running instance's database login. A machine holding the instance's
// OpenTofu state has a later fence; a machine without it has nothing.
func TestAnInstanceBuiltBeforeThePrefixIsStillFound(t *testing.T) {
	c := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "legacy",
			Labels: map[string]string{"devicechain.io/instance": "legacy"},
		}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "dci-legacy-config", Namespace: "legacy"},
			Data:       map[string][]byte{"instance": []byte(`{"id":"legacy"}`)},
		},
	)

	got := instanceNamespaceCandidates(context.Background(), c, "legacy")
	want := []string{InstanceNamespace("legacy"), "legacy"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("candidates = %v, want %v: an instance built before the prefix is invisible, "+
			"and every caller reads that as a free name", got, want)
	}
}

// 🔴 AND THE COUNTERWEIGHT: the bare id is the shape of a namespace somebody else owns, so
// it is a candidate only while it carries this instance's label. Without this, `monitoring`
// — the collision the prefix exists to retire — would be read as instance `monitoring`'s
// own namespace all over again.
func TestAnUnlabelledNamespaceSharingTheNameIsNotACandidate(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "monitoring",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "dcctl"},
	}})

	got := instanceNamespaceCandidates(context.Background(), c, "monitoring")
	if len(got) != 1 || got[0] != InstanceNamespace("monitoring") {
		t.Fatalf("candidates = %v, want only %q: the monitoring stack's namespace was taken "+
			"for an instance's", got, InstanceNamespace("monitoring"))
	}
}

// A cluster that will not answer the extra namespace read drops the candidate rather than
// failing the call: the caller's read of the instance's OWN namespace is the authoritative
// one and reports its own errors.
func TestACandidateLookupThatCannotBeAnsweredFallsBackToTheInstancesOwnNamespace(t *testing.T) {
	c := fake.NewSimpleClientset()
	c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server is not answering")
	})

	got := instanceNamespaceCandidates(context.Background(), c, "acme")
	if len(got) != 1 || got[0] != InstanceNamespace("acme") {
		t.Fatalf("candidates = %v, want only %q", got, InstanceNamespace("acme"))
	}
}
