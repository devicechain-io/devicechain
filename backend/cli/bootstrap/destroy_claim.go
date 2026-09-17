// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fatih/color"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// beginDestroy records what is about to happen — on this machine and in the
// cluster — and takes the cluster lock, and it runs BEFORE the first deletion
// rather than merely early.
//
// 🔴 THE ORDERING IS THE WHOLE VALUE OF BOTH RECORDS. A destroy is several steps,
// any of which can fail or be killed. Written first, they mean a destroy that dies
// halfway leaves evidence that a teardown is in progress — which is true, and
// which is what the next reader needs. The readers are named because a record
// nothing acts on is the thing this slice criticises elsewhere: the local marker
// is what RefuseUnfinishedDestroy refuses `dcctl bootstrap` and `dcctl upgrade`
// on; the phase annotation is what writeInstanceCR refuses a rebuild on and what
// hydrateUpgradeState refuses an upgrade on. `dcctl instances list` reads BOTH and
// prints either as PART-WAY DESTROYED — it became a reader in the same change that
// added the marker, having been named as one here for a while before that while
// reading neither. Written after the first deletion, the same kill leaves a
// declaration reading Ready over an instance that is already half gone, which is
// the failure they exist to prevent.
//
// 🔴 AND THE LOCAL MARKER COMES FIRST OF ALL, BECAUSE THE TWO STEPS AFTER IT BOTH
// RETURN. ClaimClients fails on a cluster that cannot be reached and AcquireClaim
// fails on a lock this run could not take; both warn and return, so neither
// reaches the phase write below. Every destroy that proceeds past one of those
// writes NO phase anywhere, and a marker placed beside the phase would be skipped
// in exactly the case it exists for. The marker is also the only half that
// outlives a cluster deleted out from under the instance.
// TestTheMarkerIsWrittenBeforeTheFirstDeletionAndWithoutReachingTheCluster drives
// that case end to end, against a context that does not resolve.
//
// The lock comes before the PHASE for a different reason: the annotation write is
// itself a cluster write, so doing it before the lock makes it a race with
// whatever else might be running. The marker is local and races with nothing.
//
// No failure here stops a destroy. An operator who has decided to tear an instance
// down must not be blocked because a file could not be written or the cluster
// could not be asked politely first. All three are LOUD when they fail, because a
// destroy running without the lock — or leaving no evidence that it started — is
// something the operator should know about.
func beginDestroy(ctx context.Context, kubeContext, instance string) *Claim {
	if err := writeDestroyMarker(instance); err != nil {
		fmt.Println(color.YellowString(
			"warning: could not record on this machine that instance %q is being destroyed (%v);\n"+
				"  continuing — but if this run dies part-way, nothing local will say a teardown started",
			instance, err))
	}
	ns, typed, err := ClaimClients(kubeContext)
	if err != nil {
		fmt.Println(color.YellowString(
			"warning: could not reach the cluster to take the lock before destroying (%v); continuing", err))
		return nil
	}
	claim, err := AcquireClaim(ctx, typed, ns, instance, kubeContext)
	if err != nil {
		fmt.Println(color.YellowString("warning: %v", err))
		fmt.Println(color.YellowString("  continuing with the destroy anyway — but if that run is live, this will fight it"))
		return nil
	}
	if err := SetInstancePhase(ctx, kubeContext, instance, dcv1beta1.PhaseDestroying); err != nil {
		fmt.Println(color.YellowString("warning: could not record that this instance is being destroyed (%v)", err))
	}
	return claim
}

// endDestroy clears the declaration's finalizer and gives the lock back.
//
// 🔴 CLEARING THE FINALIZER IS NOT OPTIONAL TIDYING — IT IS WHAT KEEPS THE
// FINALIZER FROM BEING A TRAP. Declarations carry one so that a hand-deleted CR
// cannot orphan an instance and reset the immutability baseline with it. The price
// of that is an object which only dcctl can finish deleting, and destroy is the
// one path that is supposed to. A destroy that removed everything else and left
// the declaration behind would leave every cluster it ran on holding an object
// nobody could remove, which is worse than the problem the finalizer solves.
//
// It runs only when the cluster survives the destroy. When the cluster itself is
// being deleted the declaration goes with it, and reaching into a cluster that is
// mid-teardown to tidy one object is a way to fail at the last step for no gain.
func endDestroy(ctx context.Context, claim *Claim, kubeContext, instance string, clusterSurvives bool, failed *error) {
	// 🔴 A FAILED DESTROY MUST NOT DELETE THE DECLARATION, and the first version of
	// this function did exactly that. It ran as an unconditional defer, so a destroy
	// that died at the Helm uninstall stripped the finalizer and removed the CR
	// while every workload was still running — manufacturing the orphaned instance
	// with no declaration that the finalizer was added to prevent, from inside the
	// change that added it.
	//
	// The declaration is only removed when the destroy actually finished. Otherwise
	// it stays, still reading Destroying, which is true and is what a resumed
	// destroy needs to find.
	// Detached for the same reason Claim.Release is: an interrupted destroy still
	// has to clear the finalizer it set, and the caller's context is dead by then.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	// 🔴 A NIL OUTCOME POINTER MEANS "NOT TOLD", AND NOT-TOLD MUST NOT MEAN SUCCESS.
	// The first version read `failed == nil || *failed == nil`, so a caller that
	// passed nothing got the DELETING branch — the exact direction this function
	// exists to prevent, reachable by omission rather than by decision. Requiring
	// the pointer makes the safe answer the default.
	switch {
	// 🔴 A THIRD OUTCOME, AND BOTH MESSAGES BELOW ARE FALSE OF IT. When a destroy meets
	// the foreign-release refusal and establishes that the instance it named has NOTHING
	// in this cluster, there is no declaration to release — that absence is the very
	// thing that was established — and nothing was left behind either. The first branch
	// would warn that the declaration "could not be removed", naming a command that
	// would also fail; the second would report a destroy that did not finish, when it
	// finished. Saying nothing is the only true answer.
	case failed != nil && errors.Is(*failed, errInstanceNotInCluster):
	case clusterSurvives && failed != nil && *failed == nil:
		if _, err := ReleaseInstanceDeclaration(ctx, kubeContext, instance); err != nil {
			fmt.Println(color.YellowString(
				"warning: the instance was destroyed but its declaration could not be removed (%v).\n"+
					"  Remove it with `dcctl instances release %s`, which destroys nothing.", err, instance))
		}
	case clusterSurvives:
		fmt.Println(color.YellowString(
			"the declaration for %q was left in place because this destroy did not finish.\n"+
				"  It still records which cluster the instance lives in, which is what a re-run needs.", instance))
	}
	if claim != nil {
		claim.Release(ctx)
	}
}
