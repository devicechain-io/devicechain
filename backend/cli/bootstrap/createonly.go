// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
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
// WRONG. A bootstrap builds an instance over ten steps and can die at any of them, so
// the question this step answers is not "is there anything here?" but "is there a
// LIVE INSTANCE here, whose credentials I must not mint over?":
//
//   - NOT the instance namespace. It is created inside the Helm step, one line before
//     the configuration document is written. A run killed between them leaves a
//     namespace with no document, and a refusal keyed on the namespace makes that
//     instance permanently unrepairable.
//   - NOT the Instance declaration. It lands at step 4, before a single credential has
//     been minted — so every failure in steps 5 through 7 would become unrepairable.
//   - NOT "any of our namespaces". The operator's lands at step 2 and the
//     infrastructure's at step 6, both before anything instance-shaped exists.
//
// The configuration document is the boundary: it is written at step 7 and it is the
// first DURABLE copy of the broker credentials and the root key. Before it exists,
// those values live only in this machine's bootstrap record, and a re-run is how a
// half-built instance is repaired. After it exists, a re-run is how a working one is
// destroyed. That is the same line the whole reuse machinery was already drawn
// around, and DeployedInstanceConfig already fails closed on "could not tell".
//
// 🔴 THE WINDOW BETWEEN STEPS 6 AND 7 IS THE ONE THIS MUST LEAVE OPEN. A live broker
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
		if err == nil && deployed != nil && rebuildRefusalReason(st, deployed) != nil {
			wouldDo(fmt.Sprintf(
				"REFUSE: instance %q is already running here, so a real run would stop at this "+
					"step and point at `dcctl upgrade`", st.Instance))
		}
		return nil
	}

	doing("checking whether this instance already exists")
	deployed, err := lookupDeployedInstance(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return fail("checking for an existing instance", err)
	}
	if err := rebuildRefusalReason(st, deployed); err != nil {
		return err
	}
	done()
	return nil
}

// rebuildRefusalReason returns the refusal, or nil when this run may proceed.
//
// Separated from the step so the policy can be exercised without a cluster — the
// carve-outs below are each a documented path, and a guard whose exceptions were
// never constructed is a guard that removes them on its next edit.
func rebuildRefusalReason(st *State, deployed *config.InstanceConfiguration) error {
	if deployed == nil {
		return nil
	}

	// 🔴 A RESTORE OVER A LIVE INSTANCE IS A SUPPORTED RETRY, NOT A REBUILD. Recovery
	// is the situation in which a run is most likely to be interrupted and most
	// likely to need running again, and the guard that makes it safe is already
	// there and sharper than this one: refuseRestoreOverADifferentKey allows it only
	// when the escrow artifact carries the key the instance is already running. A
	// blanket refusal here would take away the retry precisely when it matters.
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
