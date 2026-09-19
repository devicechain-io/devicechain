// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dcctl/operator"
	"github.com/fatih/color"
)

// RequireOperator refuses a cluster whose operator is missing, or is not the one
// this dcctl was built to work with.
//
// 🔴 THIS IS THE FLIP. `dcctl bootstrap` used to INSTALL the operator as its fifth
// step and `dcctl upgrade` used to re-apply it; both now check and refuse instead.
// The reason is that the operator is CLUSTER-scoped — one copy shared by every
// instance — so a per-instance verb moving it moved it for instances nobody was
// touching, in either direction, with no version comparison anywhere. `dcctl
// install` owns it now, and owning it means these two have to stop.
//
// 🔑 WHAT AN OPERATOR ACTUALLY LOSES, said plainly: the one-command upgrade. It is
// two commands now — `dcctl install` to move the cluster, then `dcctl upgrade` per
// instance. That is a real cost and it buys a real thing: the cluster-wide move
// happens when somebody asks for a cluster-wide move.
//
// 🔴 THE THREE ANSWERS GET THREE DIFFERENT SENTENCES, because they have three
// different remedies. See operator.Installed — an unstamped operator is a
// maintainer's `make deploy`, which is not a mismatch and is not something to
// refuse; it is something dcctl cannot vouch for and says so once.
func RequireOperator(ctx context.Context, kubeContext, verb, installCommand string) error {
	manifests, err := operator.Render(operatorless)
	if err != nil {
		return fmt.Errorf("rendering the operator overlay to check this cluster against it: %w", err)
	}
	want, err := operator.Identity(manifests)
	if err != nil {
		return fmt.Errorf("reading the identity of the operator this dcctl carries: %w", err)
	}

	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to check which operator it has: %w", err)
	}
	have, err := operator.ReadInstalled(ctx, dyn, manifests)
	if err != nil {
		return err
	}
	note, err := judgeOperator(have, want, verb, installCommand)
	if note != "" {
		fmt.Println(color.YellowString(note))
	}
	return err
}

// judgeOperator turns what a cluster has into what to say about it.
//
// 🔴 SPLIT FROM THE READ SO THE THREE SENTENCES CAN BE EXERCISED AT ALL. Reading
// needs a cluster; CHOOSING between "not installed", "cannot vouch for" and "wrong
// one" needs nothing, and it is the half where a mistake costs an operator a wrong
// remedy — being told to run `dcctl install` over an operator they installed
// deliberately, or being waved through onto a schema their instance was not built
// for. A decision reachable only with a live cluster is a decision nothing checks.
//
// The note is RETURNED rather than printed, for the same reason: a side effect is
// not an assertion.
func judgeOperator(have operator.Installed, want, verb, installCommand string) (note string, err error) {
	switch {
	case !have.Present:
		return "", fmt.Errorf(
			"this cluster has no DeviceChain operator, so there is nothing for %s to build on "+
				"(missing: %v).\n"+
				"  The operator and its definitions belong to the CLUSTER — one copy, shared by every "+
				"instance on it — so they are installed once, by:\n\n    %s\n\n"+
				"  then build any number of instances on it with `dcctl bootstrap`",
			verb, have.Missing, installCommand)

	case !have.Stamped():
		// NOT a refusal. `make deploy` renders through the kustomize CLI and pipes
		// it to kubectl, so it never passes through the code that stamps — which
		// means a maintainer who installed the operator deliberately, by the
		// documented maintainer path, lands exactly here. Refusing them would be
		// dcctl overruling a choice it has no better information about. Saying
		// nothing would be worse in the other direction: the check below is the
		// one thing protecting an instance from a schema it was not built for, and
		// an operator should know when it did not run.
		return fmt.Sprintf(
			"  note: this cluster's operator carries no identity, so dcctl cannot check it against "+
				"the one this build expects, and %s is going ahead unchecked.\n"+
				"  That is what a `make deploy` install looks like, and it is fine. It is ALSO what "+
				"a dcctl from before this check ran against this cluster looks like — and that one "+
				"moved the definitions to whatever version it carried. If you did not install this "+
				"operator by hand, run `%s` to put a known one back.",
			verb, installCommand), nil

	case !have.Matches(want):
		return "", fmt.Errorf(
			"this cluster's DeviceChain operator is not the one this dcctl expects, so %s would write "+
				"against definitions it was not built for.\n"+
				"  cluster:  %s\n  this build: %s\n"+
				"  The operator belongs to the CLUSTER and is shared by every instance on it, so moving "+
				"it is a cluster-wide act and has its own command:\n\n    %s\n\n"+
				"  Run that first, then come back to this.",
			verb, short(have.Identity), short(want), installCommand)
	}
	return "", nil
}

// requireOperatorFor is the seam hydrateUpgradeState reads through, so that the
// refusals AROUND this check — a missing declaration, an unfinished teardown, an
// uninstalled cluster — stay reachable without a live cluster to check against.
//
// A var rather than a parameter because Upgrade's signature is already the command
// layer's contract; the tests that need it are in this package.
var requireOperatorFor = RequireOperator

// operatorless renders the overlay with no image.
//
// 🔴 THE IDENTITY DOES NOT MOVE WITH THE IMAGE — it digests the CRDs only — so the
// stream this comparison is built from deliberately names no image at all. Passing
// one would suggest the answer depends on it, and the next person to read this
// would reasonably wire the run's image in and then wonder why two clusters at the
// same release disagree about nothing.
const operatorless = ""

// short trims an identity to something a human can compare across two lines
// without reading 64 hex characters. The full value is never what an operator acts
// on — the remedy is the same whatever it says.
func short(id string) string {
	if id == "" {
		return "(none)"
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
