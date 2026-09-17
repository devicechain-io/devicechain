// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"go/ast"
	"slices"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// upgrading puts a declaration in the cluster mid-upgrade: the version already
// recorded, the phase already saying what this run is doing.
func upgrading(t *testing.T) *dcv1beta1.Instance {
	t.Helper()
	inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
	setPhase(inst, dcv1beta1.PhaseUpgrading)
	return inst
}

// declaring puts one typed declaration into a fake dynamic client.
func declaring(t *testing.T, inst *dcv1beta1.Instance) *dynamicfake.FakeDynamicClient {
	t.Helper()
	obj, err := instanceToUnstructured(inst)
	if err != nil {
		t.Fatal(err)
	}
	return declarationClient(obj)
}

// phaseOf reads the phase back off the declaration in the cluster.
func phaseOf(t *testing.T, dyn *dynamicfake.FakeDynamicClient) string {
	t.Helper()
	inst := readBack(t, dyn, "prod")
	if inst == nil {
		t.Fatal("the declaration is gone")
	}
	return inst.Annotations[dcv1beta1.AnnotationPhase]
}

// 🔴 THE TEST THAT WOULD HAVE CAUGHT IT, AND NOTHING HERE COULD. writeInstanceCR
// stamped Bootstrapping on every write and `dcctl upgrade` writes the declaration
// too — so an instance that had ever been upgraded reported a bootstrap in
// progress for the rest of its life, to every reader of the phase, including
// `kubectl get dci`. The existing declaration tests assert the SPEC fields and
// passed throughout.
//
// 🔑 The wrong value is asserted by name rather than by "not Ready", because
// Bootstrapping is the specific lie: it names a verb that is not running, over an
// instance that is running fine.
func TestAnUpgradeDeclaresItselfUpgradingAndNotBootstrapping(t *testing.T) {
	dyn := declaring(t, atVersion(t, "ghcr.io/devicechain-io", "v1.2.0"))

	if err := recordUpgradedVersion(t.Context(), dyn, "prod",
		upgradingTo("ghcr.io/devicechain-io", "v1.3.0")); err != nil {
		t.Fatalf("recording the version this upgrade moved to: %v", err)
	}

	switch got := phaseOf(t, dyn); got {
	case dcv1beta1.PhaseUpgrading:
	case dcv1beta1.PhaseBootstrapping:
		t.Errorf("an upgrade left the declaration reading %q. Nothing is bootstrapping: this "+
			"instance is live and being moved to a new version, and every reader of the phase "+
			"— a listing, `kubectl get dci`, the next run — would report an unfinished "+
			"bootstrap over a healthy instance, permanently", got)
	default:
		t.Errorf("an upgrade left the declaration reading %q, want %q", got, dcv1beta1.PhaseUpgrading)
	}
}

// 🔴 A PHASE THAT IS ONLY EVER ENTERED IS NEVER LEFT. Writing Upgrading on the way
// in is half a mechanism: without a terminal stamp the value an instance is left
// holding is whatever the last write happened to be, which is how "the upgrade did
// not finish" became the permanent state of every upgraded instance. Both exits are
// tested because they are two different writes, and the failing one is the one a
// half-implementation forgets.
func TestAnUpgradeStampsATerminalPhaseOnBothExits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
		want   string
	}{
		{"a successful upgrade declares the instance ready", nil, dcv1beta1.PhaseReady},
		{"a failed upgrade says so", errors.New("the services never rolled over"), dcv1beta1.PhaseFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := declaring(t, upgrading(t))
			finishUpgradePhase(t.Context(), dyn, "prod", upgradingTo("ghcr.io/devicechain-io", "v1.3.0"), nil, tc.runErr)

			if got := phaseOf(t, dyn); got != tc.want {
				t.Errorf("the upgrade ended and the declaration reads %q, want %q. It is left "+
					"saying an upgrade is in flight over an instance nothing is touching", got, tc.want)
			}
		})
	}

	// A rehearsal recorded no Upgrading, so it has nothing to close out. Writing
	// here would make --dry-run modify the cluster at the one point it is easiest
	// to overlook: the cleanup.
	//
	// 🔑 THE FIXTURE IS IN Failed, NOT THE Ready EVERY OTHER ONE USES, AND THAT IS
	// THE WHOLE TEST. Rehearsing a re-run after a failed upgrade is the ordinary
	// reason to type --dry-run at all — and with a Ready fixture this assertion
	// cannot fail, because the value a stray stamp would write is the value the
	// declaration already holds. It was written that way first and a mutant walked
	// straight through it.
	t.Run("a dry run writes no phase at all", func(t *testing.T) {
		inst := atVersion(t, "ghcr.io/devicechain-io", "v1.2.0")
		setPhase(inst, dcv1beta1.PhaseFailed)
		dyn := declaring(t, inst)
		st := upgradingTo("ghcr.io/devicechain-io", "v1.3.0")
		st.DryRun = true

		finishUpgradePhase(t.Context(), dyn, "prod", st, nil, nil)

		if got := phaseOf(t, dyn); got != dcv1beta1.PhaseFailed {
			t.Errorf("a rehearsal moved the phase from %q to %q. --dry-run wrote to the "+
				"cluster, at the point it is easiest to overlook: the cleanup — and it wrote "+
				"the reassuring value over the record of a run that failed",
				dcv1beta1.PhaseFailed, got)
		}
	})

	// 🔴 A FENCED RUN WRITES NOTHING, and the case is real rather than theoretical:
	// the declaration now belongs to whoever reclaimed the lock, and a run that has
	// lost it would otherwise stamp Failed over a live run's Upgrading on its way
	// out. Deleting the Lease is how a reclaim looks from here.
	t.Run("a run that lost the cluster lock leaves the declaration alone", func(t *testing.T) {
		cs := fake.NewClientset()
		claim, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod", testKubeContext)
		if err != nil {
			t.Fatalf("taking the cluster lock: %v", err)
		}
		if err := cs.CoordinationV1().Leases(testClaimNS).Delete(
			t.Context(), claimLeaseName, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("simulating a reclaim: %v", err)
		}

		dyn := declaring(t, upgrading(t))
		finishUpgradePhase(t.Context(), dyn, "prod", upgradingTo("ghcr.io/devicechain-io", "v1.3.0"),
			claim, errors.New("fenced"))

		if got := phaseOf(t, dyn); got != dcv1beta1.PhaseUpgrading {
			t.Errorf("a fenced run wrote %q onto a declaration it no longer owns", got)
		}
	})

	// 🔑 THE COUNTERWEIGHT TO THE FENCE. A finishUpgradePhase that wrote nothing
	// whenever a claim was present would satisfy the test above and reintroduce the
	// defect for every ordinary upgrade, which is the shape that takes a lock.
	t.Run("a run still holding the lock stamps, and gives the lock back", func(t *testing.T) {
		cs := fake.NewClientset()
		claim, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod", testKubeContext)
		if err != nil {
			t.Fatalf("taking the cluster lock: %v", err)
		}

		dyn := declaring(t, upgrading(t))
		finishUpgradePhase(t.Context(), dyn, "prod", upgradingTo("ghcr.io/devicechain-io", "v1.3.0"),
			claim, nil)

		if got := phaseOf(t, dyn); got != dcv1beta1.PhaseReady {
			t.Errorf("a run holding the lock left the phase at %q, want %q", got, dcv1beta1.PhaseReady)
		}
		// The release moved INTO this function so it could be ordered after the stamp;
		// an upgrade that stopped handing the lock back would make the next operator
		// wait out a lease duration and type a holder identity to recover.
		if _, err := cs.CoordinationV1().Leases(testClaimNS).Get(
			t.Context(), claimLeaseName, metav1.GetOptions{}); err == nil {
			t.Error("the cluster lock was not given back")
		}
	})
}

// 🔴 CONSTRUCTED CORRECTLY, CONNECTED TO NOTHING is the failure the tests above
// cannot see: every one of them calls finishUpgradePhase itself. Upgrade needs a
// live cluster to reach, so what can be checked without one is the wiring — and
// the wiring has three properties, each of which was wrong at some point in
// writing this.
func TestUpgradeCannotEndWithoutRecordingHowItEnded(t *testing.T) {
	// 1. It is called at all, and AFTER the write that puts the declaration into
	//    Upgrading. Before it, the deferred stamp would answer
	//    recordUpgradedVersion's own refusals — including the one that protects a
	//    half-finished destroy — by overwriting the phase it refused on.
	want := []string{"recordUpgradedVersion", "finishUpgradePhase"}
	if got := callsWithin(t, "Upgrade", want...); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Upgrade calls %v; want %v, in that order. Without the second call an upgrade "+
			"writes a phase on the way in and none on the way out, so the instance reads "+
			"\"upgrading\" for good", got, want)
	}

	// 2. It is DEFERRED. Called in line at the bottom it would run on the success
	//    path only — leaving exactly the failures that most need a phase (a rollout
	//    that never completes, a Ctrl+C) declaring an upgrade still in flight.
	if !deferredWithin(t, "Upgrade", "finishUpgradePhase") {
		t.Error("Upgrade does not DEFER finishUpgradePhase, so every exit that is not the " +
			"happy one leaves the declaration mid-verb — which is the state this whole " +
			"mechanism exists to make impossible")
	}

	// 3. It is the ONLY release of the claim. Releasing separately puts the two in
	//    the wrong order: defers unwind backwards, Release deletes the Lease, and a
	//    deleted Lease reads as a lost claim — so the stamp would be fenced out by
	//    the run's own cleanup, silently, on every upgrade.
	//
	// 🔴 A METHOD CALL, NOT A PLAIN IDENTIFIER, and looking for the identifier is
	// how this check first missed it: callsWithin matches `f(...)` and the release
	// is spelled `upgradeClaim.Release(ctx)`. Restoring the separate defer survived
	// the gate that was supposed to forbid it — a gate keyed on the wrong node type
	// forbids nothing.
	if n := methodCallsWithin(t, "Upgrade", "Release"); n != 0 {
		t.Errorf("Upgrade calls Release %d time(s) itself. It must leave that to "+
			"finishUpgradePhase, which releases AFTER stamping: defers unwind backwards, so "+
			"a separate release defer runs FIRST, deletes the Lease, and CheckHeld then reads "+
			"this run as fenced — the phase write is skipped on every upgrade, silently", n)
	}
}

// 🔴 THE PHASE BECAME A PARAMETER, SO SOMETHING HAS TO WATCH WHAT EACH CALLER
// PASSES. The value used to be a constant inside writeInstanceCR, where it could
// not be got wrong; now it is an argument, and the bootstrap entry point
// WriteInstanceCR builds its own client from a kubeconfig — so no unit test
// reaches the one call that must still say Bootstrapping. Handing an upgrade's
// word to bootstrap would be invisible everywhere else: the write succeeds, the
// spec is right, and only the phase is wrong.
func TestEachVerbNamesItsOwnPhase(t *testing.T) {
	for fn, want := range map[string][]string{
		"WriteInstanceCR":       {"PhaseBootstrapping"},
		"recordUpgradedVersion": {"PhaseUpgrading"},
		// 🔴 PhaseDestroying IS IN THIS LIST AS A READ, NOT AS A WORD THIS VERB CLAIMS.
		// finishUpgradePhase compares the declaration's current phase against it and
		// DECLINES to stamp when they match, because Destroying is the one value another
		// command acts on. This check counts MENTIONS, so it cannot tell a comparison from
		// a write — TestFinishUpgradePhaseNeverWritesADestroyingPhase is what keeps the
		// distinction, and without it this entry is a hole an actual mis-stamp fits through.
		"finishUpgradePhase": {"PhaseDestroying", "PhaseFailed", "PhaseReady"},
	} {
		got := phaseConstantsNamedBy(t, fn)
		if !slices.Equal(got, want) {
			t.Errorf("%s names the phase constants %v; want %v. Each verb declares what IT is "+
				"doing, and a verb reaching for another's word writes a declaration that names "+
				"the wrong command — the defect this whole change exists to remove", fn, got, want)
		}
	}
}

// phaseConstantsNamedBy returns the Phase* constants a function mentions, sorted
// and deduplicated.
func phaseConstantsNamedBy(t *testing.T, fn string) []string {
	t.Helper()
	ids, ok := identifiersByFunction(t)[fn]
	if !ok {
		// Not a soft failure: a renamed function would otherwise make the check above
		// pass by examining nothing.
		t.Fatalf("there is no function %q in this package, so this check examined nothing", fn)
	}
	var out []string
	for _, id := range ids {
		if strings.HasPrefix(id, "Phase") && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// methodCallsWithin counts calls to a METHOD of that name in fn's body —
// `x.Name(...)` — which is the node callsWithin cannot see.
func methodCallsWithin(t *testing.T, fn, method string) int {
	t.Helper()
	_, files := packageFiles(t)
	count := 0
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
					count++
				}
				return true
			})
		}
	}
	return count
}

// deferredWithin reports whether fn defers a call to target, directly or through a
// func literal — the two spellings, since the closure form is what a defer needs
// when it reads the run's named error.
func deferredWithin(t *testing.T, fn, target string) bool {
	t.Helper()
	_, files := packageFiles(t)
	found := false
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				d, ok := n.(*ast.DeferStmt)
				if !ok {
					return true
				}
				ast.Inspect(d, func(inner ast.Node) bool {
					call, ok := inner.(*ast.CallExpr)
					if !ok {
						return true
					}
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == target {
						found = true
					}
					return true
				})
				return true
			})
		}
	}
	return found
}
