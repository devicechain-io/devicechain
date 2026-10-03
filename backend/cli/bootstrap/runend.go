// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	"k8s.io/client-go/dynamic"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// FinishBootstrapPhase records how a bootstrap run ENDED on its declaration. The
// command layer calls it before giving the cluster lock back (finishClaim).
//
// It returns an error only for the one case where the stamp is correctness rather
// than courtesy: see recordRunEnded.
func FinishBootstrapPhase(ctx context.Context, st *State, runErr error) error {
	return finishBootstrapPhase(ctx, st, runErr, func() (dynamic.Interface, error) {
		dyn, _, _, err := kubeClients(st.KubeContext)
		return dyn, err
	})
}

// finishBootstrapPhase is FinishBootstrapPhase with the connection supplied.
//
// 🔴 SPLIT BECAUSE THIS IS NOW WHAT PROTECTS A LIVE INSTANCE. Bootstrap's Ready stamp is
// the only bootstrap write that removes the first-bootstrap record, so a version of this
// that returned without stamping — or stamped without the run's claim, over a reclaimer's
// declaration — would leave every successful bootstrap open to the next plain one. Built
// from a kubeconfig inside, none of it was reachable by a test. The connection is a
// function rather than a client so a dry run still connects to nothing.
func finishBootstrapPhase(ctx context.Context, st *State, runErr error, connect func() (dynamic.Interface, error)) error {
	if st.DryRun {
		return nil
	}
	dyn, err := connect()
	if err != nil {
		return runEndUnrecorded("bootstrap", st, runErr,
			fmt.Errorf("connecting to the cluster to record the instance phase: %w", err))
	}
	return recordRunEnded(ctx, dyn, "bootstrap", st.Instance, st, st.Claim, runErr)
}

// recordRunEnded stamps Ready or Failed on the declaration at the end of a bootstrap
// or an upgrade — the one implementation both verbs' terminal stamps go through, so
// the rules below cannot be honoured by one verb and forgotten by the other.
//
// 🔴 A FENCED RUN WRITES NOTHING. Once the claim is lost the declaration belongs to
// whoever reclaimed it, and stamping Failed would overwrite the phase of a run that is
// live and doing well. CheckHeld is what establishes that — asked of the API server
// here, not read from the renewal loop's cached flag, which can be a full interval out
// of date at exactly this moment ("Wait for readiness" runs without a fence). A run
// that never HELD the lock (claim == nil) DOES stamp: beginUpgradeClaim warns and
// continues when it cannot take one, so such a run has already rewritten the
// declaration unfenced, and refusing it the terminal phase buys no safety.
//
// 🔴 AND NOT OVER A TEARDOWN. Destroying is the phase value another command ACTS on —
// writeInstanceCR refuses a rebuild over it and hydrateUpgradeState refuses an upgrade
// over it — so stamping it out would remove the only cluster-side evidence that the
// cluster holds half an instance. The refusals upstream should mean this never fires;
// the guarantee is made local to the function that would do the damage.
//
// 🔴 THE STAMP IS A COURTESY — EXCEPT AFTER A RUN THAT ENDED WELL OVER AN INSTANCE
// WHOSE FIRST BOOTSTRAP HAD NOT FINISHED. Everywhere else a phase that did not land is
// a warning: a run that worked must not be reported as failed because an annotation
// did not. But that run's Ready write is what removes the first-bootstrap record
// (setInstancePhase), and a record left standing over the instance the run just made
// live is a later bootstrap let through over a running instance. So in that one case —
// fenced, unreadable, or a write that failed — the run is reported as failed, with the
// remedy: run the same command again, which reuses everything and records it.
func recordRunEnded(ctx context.Context, dyn dynamic.Interface, verb, instance string, st *State, claim *Claim, runErr error) error {
	// Detached from the caller's cancellation: Ctrl+C is when the terminal phase
	// matters most, and it is also when the run's context is already dead.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if claim != nil {
		if err := claim.CheckHeld(cleanup); err != nil {
			return runEndUnrecorded(verb, st, runErr,
				fmt.Errorf("the cluster lock was taken over before the outcome could be recorded: %w", err))
		}
	}
	phase := dcv1beta1.PhaseReady
	if runErr != nil {
		phase = dcv1beta1.PhaseFailed
	}
	// 🔑 A READ THAT FAILS NEEDS NO ARM OF ITS OWN: setInstancePhase reads the same
	// declaration through the same client before it patches, so a read this could not
	// make is a write that cannot happen either — and that reports below.
	current, readErr := readInstanceCR(cleanup, dyn, instance)
	if readErr == nil && current != nil &&
		current.Annotations[dcv1beta1.AnnotationPhase] == dcv1beta1.PhaseDestroying {
		fmt.Println(color.YellowString(
			"warning: instance %q is part-way through being DESTROYED, so this %s left the "+
				"declaration saying so rather than recording itself as %s.\n"+
				"  Finish the teardown with `dcctl destroy %s`, which is resumable, and build it "+
				"again with `dcctl bootstrap` afterwards.", instance, verb, phase, instance))
		return nil
	}
	if err := setInstancePhase(cleanup, dyn, instance, phase); err != nil {
		return runEndUnrecorded(verb, st, runErr, err)
	}
	return nil
}

// runEndUnrecorded is what a terminal stamp that could not be made amounts to. After a
// FAILED run, or over an instance whose first bootstrap had already finished, it is a
// warning and nil, which is what it always was. After a SUCCESSFUL run over an
// unfinished one it is the run's error: see recordRunEnded.
func runEndUnrecorded(verb string, st *State, runErr, err error) error {
	if runErr != nil || !st.UnfinishedBootstrap {
		fmt.Println(color.YellowString("warning: could not record the instance phase (%v)", err))
		return nil
	}
	return fmt.Errorf("instance %q is running, but dcctl could not record that its %s finished (%w). "+
		"Until it does, its declaration still says its first bootstrap never finished, so a later "+
		"`dcctl bootstrap` would run over it. Run `dcctl %s %s %s` again with the same flags: it reuses "+
		"everything the instance holds, and records it",
		st.Instance, verb, err, verb, st.Provider, st.Instance)
}
