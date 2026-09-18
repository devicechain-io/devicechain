// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dcctl/operator"
	"github.com/fatih/color"
)

// The operator is a CLUSTER's, not an instance's, and `dcctl install` is the verb
// that prepares a cluster.
//
// 🔴 WHY THIS MOVED, BECAUSE THE EARLIER DECISION WAS THE OPPOSITE ONE. The CRDs
// and the controller were installed by `dcctl bootstrap` (step 5) and re-applied
// by `dcctl upgrade`, on the recorded ground that they were "versioned with the
// instance". They are not. They are cluster-scoped objects with one copy per
// cluster, shared by every instance on it — so bootstrapping instance B moved a
// structural schema that instance A was already running against, and upgrading
// instance B could move it BACKWARDS, silently, because neither verb compared
// versions at all. The lifecycle the CRDs actually follow is the cluster's:
// install prepares it, and a future `dcctl uninstall` removes it.
//
// 🔑 WHAT THIS DOES NOT BUY, stated here so nobody claims more for it later: it
// does not remove the cluster-wide move, it RELOCATES it. Installing a newer
// release still moves one CRD for every instance on the cluster. What changes is
// that the move now happens under a cluster-scoped verb, run deliberately, rather
// than as a side effect of building or upgrading one instance.
//
// 🔴 THE LIFETIME IS ONE-SHOT-AND-FOREVER. Nothing here reference-counts
// instances, adopts an existing operator, or removes anything when the last
// instance leaves. Removal belongs to `dcctl uninstall`, which does not exist yet.

// claimForInstall takes the cluster lock for an install, with this command's own
// progress framing.
//
// The lock itself is ClaimCluster's, shared with the bootstrap pipeline. What is
// here is only what differs: how an install announces it, and that a rehearsal
// takes nothing while still reporting the claim it would have met.
func claimForInstall(ctx context.Context, st *State) error {
	if st.DryRun {
		ns, err := operator.Namespace()
		if err != nil {
			return fail("reading the operator namespace", err)
		}
		st.OperatorNamespace = ns
		wouldDo("take the cluster lock in namespace " + ns)
		return reportExistingClaim(ctx, st)
	}

	doing("claiming the cluster")
	claim, ns, err := ClaimCluster(ctx, st.KubeContext, st.Instance)
	// Recorded even when the claim was refused: the namespace is resolved before
	// the lock is contended, and a refusal still wants to say where it was looking.
	if ns != "" {
		st.OperatorNamespace = ns
	}
	if err != nil {
		return err
	}
	st.Claim = claim
	done()
	return nil
}

// stillHoldsTheCluster is Install's fence, and it is the counterpart of the check
// Pipeline.Run performs at every step boundary.
//
// 🔴 A LOCK NOBODY RE-ASKS ABOUT IS A LOCK THAT STOPS WORKING THE MOMENT IT IS
// TAKEN AWAY. `AcquireClaim` decides who may start; it cannot decide who may
// CONTINUE. A run whose machine slept through a full lease duration can be
// reclaimed by a second operator — deliberately, with a typed confirmation — and
// without this it would wake up and carry on applying to the same cluster, which
// is the two-appliers-one-cluster outcome the whole mechanism exists to prevent.
// The bootstrap pipeline gets this for free at each step; Install is a straight
// line of function calls with no boundaries, so it has to ask by name.
//
// 🔑 THE HONEST BOUND IS THE REMAINDER OF THE CALL IN FLIGHT, not the gap between
// checks — the cluster apply is ten minutes long, and nothing here cancels it
// mid-flight. What this buys is that the run stops before the NEXT irreversible
// write rather than finishing and stamping its own record over the reclaimer's.
// Pipeline.Run documents the same limitation for the same reason.
func stillHoldsTheCluster(ctx context.Context, st *State, before string) error {
	if st.Claim == nil {
		return nil
	}
	if err := st.Claim.CheckHeld(ctx); err != nil {
		return fmt.Errorf("stopping before %s: %w", before, err)
	}
	return nil
}

// installOperator puts the CRDs, RBAC and controller Deployment on the cluster,
// building the controller image first on the developer path.
//
// 🔴 IT RUNS INSIDE THE INSTALL RECORD'S applying→installed BRACKET, after the last
// refusal that bracket performs — see the call site for what that refusal does and,
// more importantly, does not cover.
//
// 🔴 THE BUILD IS DELIBERATELY NOT PART OF IT and happens before the bracket opens.
// A ko build is minutes long and the preflight treats `ko` as optional, so a
// developer without it would otherwise fail here — leaving a cluster recorded
// `applying`, which every later bootstrap refuses, over a step that never touched
// the cluster at all.
//
// The apply goes before the OpenTofu cluster apply rather than after, because this
// is the short step and that one is the long one: a rendering failure or a bad image
// reference should surface in seconds, not after ten minutes of provisioning.
func installOperator(ctx context.Context, st *State) error {
	if err := stepInstallCore(ctx, st); err != nil {
		return err
	}
	return waitForOperatorRollout(ctx, st)
}

// waitForOperatorRollout blocks until the controller this install applied is
// actually running.
//
// 🔴 AN APPLY IS NOT AN INSTALL, AND WITHOUT THIS THE COMMAND LIES. A server-side
// apply succeeds the moment the API server accepts the objects; it says nothing
// about whether the image exists. `dcctl install --registry <typo>` would accept
// the Deployment, apply the prerequisites, record the cluster `installed` and
// print success, leaving a controller in ImagePullBackOff forever — the exact
// "an ImagePullBackOff on a controller nobody is watching" failure the image
// resolver's own comments are written against, produced by the command that now
// owns the operator.
//
// It sits here rather than inside stepInstallCore because that function is the
// APPLY and this is the confirmation; keeping them separate is what let bootstrap
// run the apply without the wait while it still did, and it is where a future
// `dcctl uninstall` or repair path would reuse one without the other.
//
// The overlay is rendered a second time rather than threaded through State. It is
// a pure, in-process render of embedded manifests — no cluster, no I/O — and the
// alternative is a field that exists only to carry bytes between two adjacent
// calls, which is the kind of state that outlives its reason.
func waitForOperatorRollout(ctx context.Context, st *State) error {
	if st.DryRun {
		return nil
	}
	manifests, err := operator.Render(operatorImageRef(st))
	if err != nil {
		return fail("rendering the operator manifests to find what to wait for", err)
	}
	targets, err := operatorDeployments(manifests)
	if err != nil {
		return fail("reading the rendered operator manifests", err)
	}
	if len(targets) == 0 {
		// Not a warning to print and continue past — the same refusal `dcctl
		// upgrade` makes, for the same reason. A stream with no Deployment in it
		// applies cleanly and installs no controller, so waiting for nothing and
		// reporting success would be false.
		return fmt.Errorf("the operator overlay rendered no Deployment, so no controller was " +
			"installed and reporting success would be false; the overlay at backend/k8s/config was changed")
	}

	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fail("building kube clients", err)
	}
	doing("waiting for the operator to roll out")
	if err := waitForRollout(ctx, typed, targets, 5*time.Minute); err != nil {
		return fail("waiting for the operator to roll out", err)
	}
	done()
	return nil
}

// buildOperatorImageForInstall ensures the local registry exists and builds the
// controller into it, on `--build` only.
//
// 🔴 IT BUILDS THE OPERATOR AND NOTHING ELSE, which is the difference between this
// and the bootstrap path's stepLocalRegistry. The service images belong to an
// instance and are deployed by the chart; building them here would make preparing
// a cluster wait on images that preparing a cluster never applies.
//
// The REGISTRY, by contrast, is genuinely cluster-scoped — a container on the kind
// network, advertised to the cluster through a ConfigMap in kube-public — so
// ensuring it is install's business, and a later `bootstrap --build` finds it
// already there and says so.
func buildOperatorImageForInstall(ctx context.Context, st *State) error {
	if !st.BuildImages {
		return nil
	}
	if st.DryRun {
		wouldDo(fmt.Sprintf("provision a local registry at %s and ko-build+push the operator image at tag %q",
			st.ImageRegistry, st.ImageVersion))
		return nil
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}

	doing("ensuring local image registry")
	if err := ensureLocalRegistry(ctx, st); err != nil {
		return fail("provisioning local registry", err)
	}
	done()

	doing("building + pushing the operator image from source (ko)")
	if err := buildOperatorImage(ctx, root, st); err != nil {
		return fail("building the operator image", err)
	}
	done()
	return nil
}

// reportOperatorPlan names the operator this run will install, on every run and
// in the rehearsal alike.
//
// Printed rather than left to be deduced: `dcctl install` did not touch the
// operator at all until this release, so whoever reads this output has every
// reason to expect it still does not. A cluster-scoped write that appears in no
// heading is the one an operator finds out about afterwards.
func reportOperatorPlan(st *State) {
	fmt.Printf("  %s %s\n", color.WhiteString("Operator:"), color.GreenString(operatorImageRef(st)))
}
