// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

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

// installOperator puts the CRDs, RBAC and controller Deployment on the cluster,
// building the controller image first on the developer path.
//
// 🔴 IT RUNS INSIDE THE INSTALL RECORD'S applying→installed BRACKET, AND THAT
// POSITION IS LOAD-BEARING IN ONE DIRECTION. markInstallApplying is what refuses a
// cluster whose record was written by a NEWER dcctl; applying the overlay before
// that refusal would let an older binary prune a newer CRD's fields on its way to
// being told it may not touch this cluster. So: refuse first, then apply.
//
// It is applied before the OpenTofu cluster apply rather than after, because it is
// the short step and the tofu apply is the long one — a rendering or image failure
// should surface in seconds, not after ten minutes of provisioning.
func installOperator(ctx context.Context, st *State) error {
	if err := requireResolvedImages(st, "installing the operator"); err != nil {
		return err
	}
	if err := buildOperatorImageForInstall(ctx, st); err != nil {
		return err
	}
	return stepInstallCore(ctx, st)
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

// reportOperatorPlan names the operator this run will install, in the rehearsal.
//
// Printed rather than deduced: `dcctl install` did not touch the operator at all
// until this release, so an operator reading a --dry-run has every reason to
// expect it still does not, and a plan that silently gained a cluster-scoped write
// is the one kind of plan a rehearsal exists to rule out.
func reportOperatorPlan(st *State) {
	fmt.Printf("  %s %s\n", color.WhiteString("Operator:"), color.GreenString(operatorImageRef(st)))
}
