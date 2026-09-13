// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// Tests for the refusal `dcctl upgrade` makes when it finds no declaration.
//
// 🔴 NO TEST HERE IS SATISFIED BY "THE UPGRADE WAS REFUSED". Every path refuses; the
// whole content of this change is WHICH refusal, so each case asserts the sentence the
// operator is handed. A guard can be killed by the wrong test — a version that refused
// everything with the pre-declaration message would pass any test that only checked for
// an error, while telling an operator with a typo to destroy an instance that does not
// exist.

// heldBy is the reader's answer for a cluster whose named source attributes these ids.
func heldBy(source string, ids ...string) clusterInstances {
	return clusterInstances{IDs: ids, Source: source}
}

// TestAnInstanceThatPredatesDeclarationsIsToldToRecreate is the case the whole change
// exists for: the instance is here, the name is right, the cluster is right, and the old
// message sent the operator to re-read their command line.
func TestAnInstanceThatPredatesDeclarationsIsToldToRecreate(t *testing.T) {
	err := undeclaredInstanceRefusal("upgrig", "local", "kind-devicechain-upgrade",
		heldBy(`the "dc" Helm release`, "upgrig"), nil)
	if err == nil {
		t.Fatal("an upgrade of an instance that is present but undeclared was allowed to " +
			"proceed; it would hydrate a State from a declaration that does not exist")
	}

	var pre *ErrPreDeclarationInstance
	if !errors.As(err, &pre) {
		t.Fatalf("the refusal is not the pre-declaration one, so an operator whose instance "+
			"is right there is still being told to check the name: %v", err)
	}

	// 🔴 ASSERT THE REASON, NOT THAT THERE IS ONE. Each line below is a thing the
	// operator cannot proceed without: which instance, what said it is here, that the
	// name is NOT the problem, and the one path that works.
	for _, want := range []struct{ what, substr string }{
		{"the instance that is here", `"upgrig"`},
		{"what the answer was read from", "Helm release"},
		{"that this is not a typo", "not a mistyped name"},
		{"that this release will not upgrade onto its predecessor", "DOES NOT UPGRADE ONTO ITS PREDECESSOR"},
		{"how to destroy it", "dcctl destroy local upgrig"},
		{"how to build it again", "dcctl bootstrap local upgrig"},
		{"the cluster the recipes act on", "kind-devicechain-upgrade"},
		{"that the data goes with it", "data with it"},
	} {
		if !strings.Contains(err.Error(), want.substr) {
			t.Errorf("the refusal does not name %s (%q): %v", want.what, want.substr, err)
		}
	}

	// 🔴 AND IT MUST NOT STILL SAY THE OLD THING. A message carrying both readings is
	// the one an operator cannot act on, which is the defect being fixed.
	if strings.Contains(err.Error(), "Check the name") {
		t.Errorf("the pre-declaration refusal still tells the operator to check the name, "+
			"which is the advice that cannot be acted on here: %v", err)
	}
}

// 🔴 THE NEGATIVE CONTROL, AND THE PAIR IS THE POINT. A refusal that fired for every
// undeclared instance would pass the test above and would tell an operator who mistyped
// an instance name to destroy something that was never installed.
func TestANameThisClusterDoesNotHoldStillGetsTheOriginalAdvice(t *testing.T) {
	err := undeclaredInstanceRefusal("typo", "local", "kind-dev",
		heldBy("the instance declarations in this cluster", "production"), nil)
	if err == nil {
		t.Fatal("an upgrade of an instance nothing in this cluster names was allowed to proceed")
	}
	var pre *ErrPreDeclarationInstance
	if errors.As(err, &pre) {
		t.Fatalf("a name this cluster does not hold was told to destroy and rebuild an "+
			"instance that is not here: %v", err)
	}
	for _, want := range []string{`"typo"`, "Check the name", "kind-dev", "dcctl instances"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the original refusal no longer says %q: %v", want, err)
		}
	}
}

// An EMPTY cluster is the other half of the same control: nothing is here at all, which
// is the plainest "check the name" there is.
func TestAnEmptyClusterGetsTheOriginalAdvice(t *testing.T) {
	err := undeclaredInstanceRefusal("upgrig", "local", "kind-dev", clusterInstances{}, nil)
	var pre *ErrPreDeclarationInstance
	if errors.As(err, &pre) {
		t.Fatalf("an empty cluster was reported as holding a pre-declaration instance: %v", err)
	}
	if !strings.Contains(err.Error(), "Check the name") {
		t.Errorf("an empty cluster did not get the original advice: %v", err)
	}
}

// 🔴 "COULD NOT ASK" IS NOT "NOTHING THERE". A cluster that will not answer must not be
// reported as empty: that reading tells an operator their running instance does not
// exist, and sends them to check a name that is correct.
func TestAClusterThatWillNotAnswerSaysSoRatherThanChoosing(t *testing.T) {
	readErr := errors.New("connection refused")
	err := undeclaredInstanceRefusal("upgrig", "local", "kind-dev", clusterInstances{}, readErr)
	if err == nil {
		t.Fatal("an unreadable cluster let the upgrade proceed")
	}
	var pre *ErrPreDeclarationInstance
	if errors.As(err, &pre) {
		t.Fatalf("an unreadable cluster was reported as a pre-declaration instance, which "+
			"is a conclusion drawn from a question that was never answered: %v", err)
	}
	if !errors.Is(err, readErr) {
		t.Errorf("the refusal drops the reason the cluster could not be asked, so the "+
			"operator cannot tell a broken kubeconfig from a mistyped name: %v", err)
	}
	for _, want := range []string{"could not be established", "destroyed and built again", "a name or a cluster to check"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the inconclusive refusal does not say %q, so it reads as one of the "+
				"two definite answers: %v", want, err)
		}
	}
}

// A cluster holding the instance UNDER ANOTHER SOURCE is still holding it. This is what
// stops the check being keyed on the Helm release alone: the stamped Secrets answer for a
// bootstrap that died before its Helm step, and a declaration answers for one that is
// simply half-destroyed.
func TestAnyAttributingSourceIsEnough(t *testing.T) {
	for _, source := range []string{
		"the instance declarations in this cluster",
		"the credentials dcctl minted in dc-system",
		`the "dc" Helm release`,
	} {
		err := undeclaredInstanceRefusal("upgrig", "local", "kind-dev", heldBy(source, "upgrig"), nil)
		var pre *ErrPreDeclarationInstance
		if !errors.As(err, &pre) {
			t.Errorf("an instance named by %q was not recognised as present: %v", source, err)
		}
	}
}

// The reader's own answer, not a re-implementation of it. holds is what separates the
// two refusals, and a version that always said true (or always false) collapses them.
func TestHoldsSeparatesTheInstanceItIsAskedAboutFromTheOthers(t *testing.T) {
	c := clusterInstances{IDs: []string{"alpha", "bravo"}}
	if !c.holds("alpha") || !c.holds("bravo") {
		t.Error("holds did not recognise an instance the cluster reports")
	}
	if c.holds("charlie") {
		t.Error("holds claimed an instance the cluster does not report, which would tell an " +
			"operator with a typo to destroy an instance that is not there")
	}
	if (clusterInstances{}).holds("alpha") {
		t.Error("an empty cluster reports holding an instance")
	}
	// A prefix is not a match: "upgrig" and "upgrig-2" are different instances, and the
	// recipes this refusal prints would act on the wrong one.
	if (clusterInstances{IDs: []string{"upgrig-2"}}).holds("upgrig") {
		t.Error("holds matched on a prefix rather than on the whole instance name")
	}
}

// The seam is wired: refuseUndeclaredInstance must actually consult the reader. A
// version that skipped it would return the original advice always, and the test above
// would never notice because it calls the policy directly.
func TestTheRefusalActuallyAsksTheCluster(t *testing.T) {
	asked := ""
	orig := readClusterInstances
	t.Cleanup(func() { readClusterInstances = orig })
	readClusterInstances = func(_ context.Context, kubeContext string) (clusterInstances, error) {
		asked = kubeContext
		return heldBy(`the "dc" Helm release`, "upgrig"), nil
	}

	err := refuseUndeclaredInstance(context.Background(), "local", "kind-dev", "upgrig")
	var pre *ErrPreDeclarationInstance
	if !errors.As(err, &pre) {
		t.Fatalf("refuseUndeclaredInstance did not consult the reader, so a present "+
			"instance is still met with the typo advice: %v", err)
	}
	if asked != "kind-dev" {
		t.Errorf("the reader was asked about %q rather than the cluster the upgrade is "+
			"pointed at", asked)
	}
}

// 🔴 THE WIRING, WHICH IS THE ONE PIECE EVERY TEST ABOVE STRUCTURALLY CANNOT SEE. They
// all call the policy or the reader directly; a build in which hydrateUpgradeState still
// emitted the original message inline would pass every one of them, and nothing else in
// this package exercises that function — it reads a live cluster. So both of its reads
// are stubbed and the refusal it actually returns is asserted.
func TestTheHydrationReturnsTheRefusalTheClusterEarns(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatalf("resolving the local provider: %v", err)
	}
	binding := ClusterBinding{KubeContext: "kind-devicechain-upgrade", Cluster: "devicechain-upgrade"}
	opts := UpgradeOptions{Options: Options{Instance: "upgrig"}}

	prev := readInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = prev })
	// No declaration: the case both refusals start from.
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return nil, nil
	}

	stubClusterInstances(t, heldBy(`the "dc" Helm release`, "upgrig"), nil)
	_, err = hydrateUpgradeState(context.Background(), nil, provider, binding, opts)
	var pre *ErrPreDeclarationInstance
	if !errors.As(err, &pre) {
		t.Fatalf("the hydration did not return the pre-declaration refusal for an instance "+
			"this cluster holds, so the whole change is unreachable from the command: %v", err)
	}
	if pre.Provider != "local" || pre.KubeContext != "kind-devicechain-upgrade" {
		t.Errorf("the refusal was built with provider %q and context %q, so the commands it "+
			"prints would not run", pre.Provider, pre.KubeContext)
	}

	// 🔴 AND THE OTHER SIDE, FROM THE SAME ENTRY POINT: a cluster that holds nothing must
	// still get the original advice. Without this the wiring test is satisfied by a
	// hydration that returns the pre-declaration refusal unconditionally.
	stubClusterInstances(t, clusterInstances{}, nil)
	_, err = hydrateUpgradeState(context.Background(), nil, provider, binding, opts)
	if errors.As(err, &pre) {
		t.Fatalf("the hydration told an operator to destroy an instance that is not in this "+
			"cluster: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "Check the name") {
		t.Fatalf("the hydration no longer gives the original advice for a name nothing "+
			"holds: %v", err)
	}
}

// An upgrade run without a provider still has to print a recipe somebody can read. The
// placeholder is the same one ErrSecondInstance uses; a bare hole is worse.
func TestARecipeWithNoProviderReadsAsAPlaceholder(t *testing.T) {
	err := &ErrPreDeclarationInstance{Instance: "upgrig", KubeContext: "kind-dev"}
	if !strings.Contains(err.Error(), "dcctl destroy <provider> upgrig") {
		t.Errorf("a refusal with no provider printed a command with a hole in it: %v", err)
	}
	// The sentence reads "named by <source>" because every source label is a plural noun
	// phrase ("the instance declarations in this cluster", "the credentials dcctl minted
	// in dc-system", "the DeviceChain Helm releases in this cluster") and "<source> names
	// it" agreed with none of them. The fallback has to fit the same frame.
	if !strings.Contains(err.Error(), "named by what is in this cluster") {
		t.Errorf("a refusal with no source did not fall back to naming the cluster: %v", err)
	}
}
