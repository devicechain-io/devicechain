// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// beginDestroy takes the cluster lock and records what is about to happen, and it
// runs BEFORE the first deletion rather than merely early.
//
// 🔴 THE ORDERING IS THE WHOLE VALUE OF THE PHASE. A destroy is several steps, any
// of which can fail or be killed. Written first, the annotation means a destroy
// that dies halfway leaves a declaration reading Destroying — which is true, and
// which tells the next reader (a resumed destroy, another operator, `instances
// list`) that what they are looking at is a teardown in progress. Written after
// the first deletion, the same kill leaves one reading Ready over an instance that
// is already half gone, which is the failure the field exists to prevent.
//
// The lock comes first inside that ordering for the same class of reason: the
// annotation write is itself a write, so doing it before the lock makes it a race
// with whatever else might be running.
//
// Neither failure stops a destroy. The lock is a coordination courtesy and the
// annotation is a report; an operator who has decided to tear an instance down
// must not be blocked because the cluster could not be asked politely first. Both
// are LOUD when they fail, because a destroy running without the lock is
// something the operator should know about.
func beginDestroy(ctx context.Context, kubeContext, instance string) *Claim {
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
	if clusterSurvives && failed != nil && *failed == nil {
		if _, err := ReleaseInstanceDeclaration(ctx, kubeContext, instance); err != nil {
			fmt.Println(color.YellowString(
				"warning: the instance was destroyed but its declaration could not be removed (%v).\n"+
					"  Remove it with `dcctl instances release %s`, which destroys nothing.", err, instance))
		}
	} else if clusterSurvives {
		fmt.Println(color.YellowString(
			"the declaration for %q was left in place because this destroy did not finish.\n"+
				"  It still records which cluster the instance lives in, which is what a re-run needs.", instance))
	}
	if claim != nil {
		claim.Release(ctx)
	}
}
