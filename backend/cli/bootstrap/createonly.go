// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/fatih/color"

	"github.com/devicechain-io/dc-microservice/config"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// stepRefuseRebuild stops a bootstrap aimed at an instance that already exists.
//
// 🔴 BOOTSTRAP CREATES. It mints every credential an instance has, because none of
// them exists yet, and there is no ordering in which that is a safe thing to do to a
// running instance. It was mechanically re-runnable for as long as it took to build
// somewhere else for that job to go; `dcctl upgrade` is that place, and this is the
// step that stops the two verbs overlapping.
//
// 🔴 WHAT IT KEYS ON IS THE WHOLE DESIGN, AND THE THREE OBVIOUS CHOICES ARE ALL
// WRONG. A bootstrap builds an instance over ten steps (NewDefaultPipeline) and can
// die at any of them, so the question this step answers is not "is there anything
// here?" but "is there a LIVE INSTANCE here, whose credentials I must not mint over?":
//
//   - NOT the instance namespace. It is created in the apply step, ahead of the
//     Secrets dcctl mints into it (CloudNativePG reads the credential Secret when it
//     creates the Cluster), and again — idempotently — in the Helm step, one line
//     before the configuration document is written. A run killed anywhere between
//     leaves a namespace with no document, and a refusal keyed on the namespace makes
//     that instance permanently unrepairable.
//   - NOT the Instance declaration's existence. It lands at step 5, before a single
//     credential has been minted — so every failure in steps 6 through 8 would become
//     unrepairable. (What it RECORDS is part of the answer: see below.)
//   - NOT "any of our namespaces". The operator's namespace is created by the claim
//     step (and by `dcctl install` before that), before anything
//     instance-shaped exists, and dc-system is the cluster's — `dcctl install` created
//     it before this bootstrap was allowed to start.
//
// The configuration document is where the boundary starts: it is written at step 8
// and it is the first DURABLE copy of the broker credentials and the root key. Before
// it exists, those values live only in this machine's bootstrap record, and a re-run
// is how a half-built instance is repaired. That is the same line the whole reuse
// machinery was already drawn around, and DeployedInstanceConfig already fails closed
// on "could not tell".
//
// 🔴 BUT THE DOCUMENT ALONE DOES NOT MAKE AN INSTANCE LIVE. dcctl writes it at the
// START of the Helm step, before Helm runs (helmInstall), so a chart the API server
// rejects, a rollout that never becomes ready, or a Ctrl+C anywhere after that leaves
// the document over an instance that never served — and keyed on the document alone,
// this step refused every re-run of it and left destroy as the only way out. So the
// declaration also records whether the instance's first bootstrap has ever ended
// successfully (AnnotationBootstrapUnfinished, see bootstrapUnfinished), and a run is
// let through over the document while that record says it has not. The render step
// reads back every credential the earlier run put in the cluster — the same reuse the
// carve-outs below rely on — so finishing such an instance mints over nothing but the
// broker's TLS authority, which every run issues afresh.
//
// 🔴 ABSENCE OF THE RECORD MEANS LIVE, and that is the fail-closed direction: every
// instance built before the record existed carries none and stays exactly as
// protected as it was, and a declaration that cannot be READ stops the run rather
// than being taken for one that says "unfinished".
//
// 🔴 THE WINDOW BETWEEN STEPS 7 AND 8 IS THE ONE THIS MUST LEAVE OPEN. A live broker
// configured with credentials whose only copy is a file on this machine, live database
// Clusters whose owner passwords exist only in their Secrets, and no document. It is
// reachable, it has been observed, and re-running is the only thing that repairs it.
func stepRefuseRebuild(ctx context.Context, st *State) error {
	// A dry run applies nothing, so there is nothing to protect the instance from —
	// and rehearsing a bootstrap against a cluster that already has one is a
	// reasonable thing to want to do. It still SAYS what a real run would do, because
	// a rehearsal that hides the refusal is a rehearsal of a different run.
	if st.DryRun {
		deployed, err := lookupDeployedInstance(ctx, st.KubeContext, st.Instance)
		if err != nil || deployed == nil {
			return nil
		}
		// Best-effort, like the document read above: an unreadable declaration reads
		// as nil, which predicts the refusal — a real run would stop on it, and the
		// rehearsal still says it would not go ahead.
		decl, _ := readInstanceDeclaration(ctx, st.KubeContext, st.Instance)
		finishing := bootstrapUnfinished(decl)
		if rebuildRefusalReason(st, deployed, decl) != nil {
			wouldDo(fmt.Sprintf(
				"REFUSE: instance %q is already running here, so a real run would stop at this "+
					"step and point at `dcctl upgrade`", st.Instance))
		} else if finishing {
			wouldDo(fmt.Sprintf("finish instance %q, whose first bootstrap did not complete", st.Instance))
		}
		st.OverLiveInstance = !finishing
		return nil
	}

	doing("checking whether this instance already exists")
	deployed, err := lookupDeployedInstance(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return fail("checking for an existing instance", err)
	}
	var decl *dcv1beta1.Instance
	if deployed != nil {
		// 🔴 ASKED ONLY WHEN THE DOCUMENT IS THERE, AND AN ERROR STOPS THE RUN. "Could
		// not tell whether its first bootstrap finished" must not resolve to "it did
		// not", which is the direction that lets a run through over a live instance.
		if decl, err = readInstanceDeclaration(ctx, st.KubeContext, st.Instance); err != nil {
			return fail("checking whether this instance's first bootstrap finished", err)
		}
	}
	if err := rebuildRefusalReason(st, deployed, decl); err != nil {
		return err
	}
	finishing := deployed != nil && bootstrapUnfinished(decl)
	// Past the refusal with a document in hand and no unfinished record means a
	// carve-out let this run through over a live instance, whose superuser was seeded
	// long ago. Recorded so the credential step does not generate a seed password for
	// it (resolveCredentials), and so the declare step does not mark it unfinished.
	st.OverLiveInstance = deployed != nil && !finishing
	done()
	if finishing {
		fmt.Println(color.YellowString("  instance %q exists but its first bootstrap did not finish; "+
			"this run finishes it, reusing what the earlier run put in the cluster", st.Instance))
	}
	return nil
}

// bootstrapUnfinished reports whether a declaration records that its instance's first
// bootstrap never ended successfully.
//
// 🔴 EXACTLY THE ONE VALUE, AND NEVER BESIDE READY. Anything else — no declaration, no
// record, another value — reads as finished, the direction that keeps a live instance
// protected. The phase conjunct is for the one writer this release cannot reach: a dcctl
// from before the record stamps Ready without removing it, and an instance that reached
// Ready is live whoever wrote it. This release never leaves the two together — its Ready
// write removes the record in the same patch (setInstancePhase) — so the conjunct costs
// nothing here.
func bootstrapUnfinished(decl *dcv1beta1.Instance) bool {
	return decl != nil &&
		decl.Annotations[dcv1beta1.AnnotationBootstrapUnfinished] == dcv1beta1.BootstrapUnfinished &&
		decl.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseReady
}

// rebuildRefusalReason returns the refusal, or nil when this run may proceed.
//
// Separated from the step so the policy can be exercised without a cluster — the
// carve-outs below are each a documented path, and a guard whose exceptions were
// never constructed is a guard that removes them on its next edit.
func rebuildRefusalReason(st *State, deployed *config.InstanceConfiguration, decl *dcv1beta1.Instance) error {
	if deployed == nil {
		return nil
	}

	// 🔴 A FIRST BOOTSTRAP THAT NEVER FINISHED IS NOT A LIVE INSTANCE, whatever the
	// document says: see stepRefuseRebuild for why the document can exist over one.
	if bootstrapUnfinished(decl) {
		return nil
	}

	// 🔴 A RESTORE OVER A LIVE INSTANCE IS A SUPPORTED RETRY, NOT A REBUILD. Recovery
	// is the situation in which a run is most likely to be interrupted and most
	// likely to need running again, and the guard that makes it safe is already
	// there and sharper than this one: refuseRestoreOverADifferentKey allows it only
	// when the escrow artifact carries the key the instance is already running. A
	// blanket refusal here would take away the retry precisely when it matters.
	//
	// 🔴 Active(), NOT RestoresEventStore(), and the breadth is the point: the carve-out
	// widens for ANY archive a run names, so a restore field added later reaches it
	// without anyone remembering to widen it. TestEveryWayOfNamingARestoreReachesTheCarveOut
	// reads the struct rather than a list for the same reason.
	if st.Restore.Active() || st.Escrow.RestoringRootKey() {
		return nil
	}

	// The documented path for an instance carrying databases from before the managed
	// ones: dump, then re-run with this flag so the old resources are removed. It is
	// a re-run against a live instance by construction, and it is what the flag is
	// for — there is nothing else it could mean.
	if st.AllowLegacyDbRemoval {
		return nil
	}

	return fmt.Errorf(
		"instance %q is already running in this cluster, and `dcctl bootstrap` builds an "+
			"instance rather than changing one. It mints every credential an instance has — the "+
			"database passwords, the broker's authority and logins, the secret-store root key — "+
			"and there is no ordering in which handing a running instance new ones is safe.\n\n"+
			"  To move it onto a new version:  dcctl upgrade %s %s --version <tag>\n"+
			"  To rebuild it from nothing:     dcctl destroy %s %s, then bootstrap again\n\n"+
			"The second takes its data with it",
		st.Instance, st.Provider, st.Instance, st.Provider, st.Instance)
}
