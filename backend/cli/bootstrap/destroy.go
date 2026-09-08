// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fatih/color"
)

// errDestroyAborted signals that the OPERATOR declined, as distinct from a completed
// operation. They are different outcomes and used to be the same return value, which let
// a declined uninstall fall through to deleting the instance's local state.
var errDestroyAborted = errors.New("aborted by operator")

// DestroyOptions drives a teardown. KeepCluster switches from the default full
// teardown (delete the whole cluster) to an instance-only uninstall; PurgeRegistry
// additionally removes the shared local image registry container.
type DestroyOptions struct {
	Options
	KeepCluster   bool
	PurgeRegistry bool
}

// Destroy tears down a DeviceChain instance — the inverse of bootstrap.
//
// Default (full): delete the whole cluster the instance lives in and remove the
// instance's persisted local state. For the local provider this is a `kind
// delete cluster`, which removes the operator, CRDs, infra and all data in one
// shot — fast and total.
//
// --keep-cluster: uninstall only the instance (helm release + its namespace),
// leaving the cluster, infra and operator in place so a re-bootstrap is quick.
func Destroy(ctx context.Context, provider Provider, opts DestroyOptions) error {
	if opts.KeepCluster {
		// An operator declining a prompt is not a failed command — swallow the sentinel
		// here so `--keep-cluster` still exits 0 when the answer is no, as it always has.
		if err := destroyInstanceOnly(ctx, opts); err != nil && !errors.Is(err, errDestroyAborted) {
			return err
		}
		return nil
	}
	return destroyEverything(ctx, provider, opts)
}

// announceBinding prints where the cluster name came from, and is the reason this whole
// change is not just a bug fix.
//
// 🔴 THE ORIGINAL DEFECT WAS NOT A WRONG ANSWER, IT WAS A CONFIDENT ONE. `destroy` derived
// `kind delete cluster --name <instance>`, kind's delete is idempotent so a cluster that
// does not exist exits 0, and the command printed `Instance "harig" destroyed.` while four
// containers kept running. Nothing in that output distinguished a real teardown from a
// no-op. So every path below names the cluster it is about to act on and says how it knows
// — and the guess says out loud that it is a guess.
func announceBinding(binding ClusterBinding, source BindingSource, instance string) {
	switch source {
	case BindingFromRecord:
		fmt.Println(color.WhiteString("Instance %s is recorded in cluster %s (context %s).",
			color.GreenString(instance), color.GreenString(binding.describe()), binding.KubeContext))
	case BindingFromFlag:
		fmt.Println(color.WhiteString("Targeting context %s, as given.", color.GreenString(binding.KubeContext)))
	case BindingUnreadable:
		// Handled by refuseUnreadable before anything acts; announced here so the reason
		// appears in the same place as every other source.
		fmt.Println(color.RedString(
			"Instance %q HAS a cluster record and it could not be read.", instance))
	case BindingGuessed:
		fmt.Println(color.YellowString(
			"No record of which cluster instance %q lives in — GUESSING cluster %q (context %s) from its name.\n"+
				"  This instance predates dcctl recording that, or its record was removed. If it was bootstrapped\n"+
				"  with --kube-context, this guess is WRONG and the cluster below will be left running.",
			instance, binding.Cluster, binding.KubeContext))
	}
}

// destroyInstanceOnly removes just the instance's Helm release, leaving the
// cluster and platform (infra + operator) warm for a fast re-bootstrap.
func destroyInstanceOnly(ctx context.Context, opts DestroyOptions) error {
	binding, source := ResolveBinding(opts.Options)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}
	kubeContext := binding.KubeContext

	fmt.Println(GreenUnderline(fmt.Sprintf("\nUninstall instance %q (keeping cluster %s)", opts.Instance, binding.describe())))
	announceBinding(binding, source, opts.Instance)
	if opts.DryRun {
		wouldDo("helm uninstall the instance release and delete namespace " + opts.Instance)
		return nil
	}
	if !opts.AssumeYes && !confirm(fmt.Sprintf(
		"Uninstall instance %q? The cluster, infrastructure and operator stay in place", opts.Instance)) {
		fmt.Println(color.YellowString("Aborted."))
		return errDestroyAborted
	}

	doing("uninstalling instance release (Helm)")
	if err := helmUninstall(ctx, kubeContext); err != nil {
		return fail("uninstalling release", err)
	}
	done()

	fmt.Println(color.HiGreenString("\nInstance %q uninstalled; cluster %s left running.", opts.Instance, kubeContext))
	return nil
}

// destroyEverything deletes the whole cluster and clears the instance's local
// state (and, with PurgeRegistry, the shared local registry container).
func destroyEverything(ctx context.Context, provider Provider, opts DestroyOptions) error {
	fmt.Println(GreenUnderline(fmt.Sprintf("\nDestroy instance %q on provider %q", opts.Instance, provider.Name())))

	binding, source := ResolveBinding(opts.Options)
	announceBinding(binding, source, opts.Instance)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}

	// 🔴 THE DRY-RUN GUARD SITS ABOVE EVERY BRANCH, AND IT MUST. An earlier version of
	// this function put the adopted branch first and the guard after it, so
	// `--dry-run` on any instance bootstrapped with --kube-context fell into the adopted
	// path and REMOVED ~/.devicechain/<instance> — tfstate and all — under a flag whose
	// entire promise is "print what would happen without destroying anything". A flag
	// that destroys is worse than no flag.
	if opts.DryRun {
		switch {
		case !binding.Managed:
			wouldDo(fmt.Sprintf(
				"uninstall instance %q and remove ~/.devicechain/%s, LEAVING cluster %q running",
				opts.Instance, opts.Instance, binding.describe()))
		default:
			wouldDo(fmt.Sprintf("delete cluster %q and remove ~/.devicechain/%s", binding.describe(), opts.Instance))
		}
		if opts.PurgeRegistry {
			wouldDo("remove the shared local image registry container")
		}
		return nil
	}

	// 🔴 THE ADOPTED CASE, AND THE REASON THIS IS NOT JUST A NAME LOOKUP. The operator
	// pointed dcctl at a cluster somebody else made — which is what both validation rigs
	// do — so the cluster is not ours to delete. The old code expressed that as a hard
	// REFUSAL inside DestroyCluster, which meant `destroy` could not clean up a rig
	// instance at all; the operator's only route was `--keep-cluster`, which they had to
	// know to reach for. Now the uninstall still happens and the cluster is named as
	// left running, so the outcome is complete AND the boundary is visible.
	if !binding.Managed {
		fmt.Println(color.YellowString(
			"Cluster %s was not created or named by dcctl, so it will be LEFT RUNNING.\n"+
				"  Uninstalling the instance from it instead; delete the cluster with whatever made it.",
			binding.describe()))

		// 🔴 THE ORDER MATTERS, AND GETTING IT WRONG RE-CREATES THE ORPHAN. An adopted
		// cluster is very often ALREADY GONE — a rig deletes its own cluster on the way
		// out and leaves the instance's state behind, which is how nine orphaned
		// directories accumulated. Uninstalling first would then fail on an unreachable
		// cluster and abort before the state was cleared, so the one command that could
		// tidy up would refuse to, for exactly the instances that need it.
		//
		// So: ask first. Cluster gone ⇒ there is nothing to uninstall and clearing the
		// state is the whole job. Cluster present ⇒ uninstall, and a failure there is
		// REAL — the instance is still deployed, so the state must survive to describe
		// it, and the error is returned rather than swallowed.
		// 🔴 ONLY A NAMED CLUSTER MAY BE DECLARED GONE. For a binding carrying no cluster
		// name, ClusterExists falls back to "is this context still in kubeconfig", and a
		// kubeconfig entry's absence is not a cluster's absence — a different KUBECONFIG
		// would look exactly like a deleted cluster, and clearing the state on that
		// reading would throw away the tfstate of a live instance. So an unnamed binding
		// skips the shortcut and goes through the uninstall, which fails loudly if the
		// cluster really is unreachable.
		if binding.Cluster != "" {
			exists, err := provider.ClusterExists(ctx, binding)
			if err != nil {
				return fail("checking whether the cluster exists", err)
			}
			if !exists {
				fmt.Println(color.YellowString(
					"Cluster %s is not there any more — nothing to uninstall. Clearing local state only.", binding.describe()))
				if err := removeInstanceState(opts); err != nil {
					return err
				}
				// 🔴 NOT "destroyed". Every closing line on this path says what actually
				// happened, because "destroyed" over a cluster nobody touched is
				// precisely the sentence this whole change exists to stop printing.
				fmt.Println(color.HiGreenString("\nInstance %q removed; its cluster %s was already gone.",
					opts.Instance, binding.describe()))
				return nil
			}
		}
		keepOpts := opts
		keepOpts.KeepCluster = true
		keepOpts.KubeContext = binding.KubeContext
		// 🔴 An ABORT is not a completed uninstall. destroyInstanceOnly used to return nil
		// for both, so declining the confirmation still fell through to removeInstanceState
		// — deleting the tfstate of an instance the operator had just said to leave alone,
		// and closing with a line that said it was uninstalled.
		if err := destroyInstanceOnly(ctx, keepOpts); err != nil {
			if errors.Is(err, errDestroyAborted) {
				return nil
			}
			return err
		}
		if err := removeInstanceState(opts); err != nil {
			return err
		}
		fmt.Println(color.HiGreenString("\nInstance %q uninstalled; cluster %s was NOT created by dcctl and is still running.",
			opts.Instance, binding.describe()))
		return nil
	}

	if !opts.AssumeYes && !confirm(fmt.Sprintf(
		"Permanently destroy instance %q AND cluster %q? This deletes ALL of its data", opts.Instance, binding.describe())) {
		fmt.Println(color.YellowString("Aborted."))
		return nil
	}

	// 🔴 ASKED, NOT ASSUMED. `kind delete cluster` on a cluster that does not exist exits
	// 0 — which is exactly how the old command turned "there was nothing here" into
	// "destroyed". Checking first is what lets the two outcomes be REPORTED differently;
	// the delete itself stays idempotent, so a cluster that vanishes between the check
	// and the call is still handled.
	exists, err := provider.ClusterExists(ctx, binding)
	if err != nil {
		return fail("checking whether the cluster exists", err)
	}
	clusterWasDeleted := exists
	if !exists {
		fmt.Println(color.YellowString(
			"Cluster %s is already gone — nothing to delete. Clearing local state only.", binding.describe()))
	} else {
		doing(fmt.Sprintf("deleting cluster %q", binding.describe()))
		if err := provider.DestroyCluster(ctx, binding, opts.Options); err != nil {
			return fail("deleting cluster", err)
		}
		done()
	}

	if err := removeInstanceState(opts); err != nil {
		return err
	}

	if opts.PurgeRegistry {
		doing("removing local image registry container")
		_ = removeLocalRegistry(ctx) // best-effort: a missing container is fine
		done()
	}

	// 🔴 The closing line differs by what actually happened. `Instance %q destroyed.` over
	// a cluster that was already gone is word-for-word the sentence the original defect
	// printed — so the one case that could reproduce it gets its own wording, and
	// TestDestroyClosingMessageMatchesWhatActuallyHappened asserts the phrase is absent.
	if clusterWasDeleted {
		fmt.Println(color.HiGreenString("\nInstance %q destroyed.", opts.Instance))
	} else {
		fmt.Println(color.HiGreenString("\nInstance %q removed; its cluster %s was already gone.",
			opts.Instance, binding.describe()))
	}
	return nil
}

// removeInstanceState removes the instance's persisted local state, sparing root-key
// escrow, and reports what it spared.
//
// Extracted so the ADOPTED path can reach it too. That path uninstalls the instance and
// leaves somebody else's cluster running — but the OpenTofu state, the instance record
// and everything else under ~/.devicechain/<instance> describe an instance that no
// longer exists, and leaving them behind is how nine orphaned state directories
// accumulated on one machine with nothing able to report them.
func removeInstanceState(opts DestroyOptions) error {
	doing(fmt.Sprintf("removing local state (~/.devicechain/%s)", opts.Instance))
	var keptEscrow []string
	var removeErr error
	if dir, err := instanceRoot(opts.Instance); err == nil {
		keptEscrow, removeErr = removeStatePreservingEscrow(dir)
	}
	// Anything spared is named BEFORE the error is returned. A partially removed tree
	// is exactly when an operator needs to know what survived in it, and reporting
	// only on the success path meant the failure case said nothing.
	for _, p := range keptEscrow {
		fmt.Println(color.YellowString("  kept root-key escrow %s — the cluster is gone but this still opens its database backups", p))
	}
	if removeErr != nil {
		return fail("removing local state", removeErr)
	}
	done()

	// The artifact dcctl actually wrote lives OUTSIDE this tree by design, so the
	// walk above never sees it. Naming it here is the point: destroy is the one
	// command that knows the instance is gone, and an operator who is told nothing
	// will hit the "already exists" refusal on their next bootstrap of the same name
	// with no idea where the file came from.
	if p, err := DefaultEscrowPath(opts.Instance); err == nil {
		if _, err := os.Stat(p); err == nil {
			fmt.Println(color.YellowString(
				"  the root-key escrow for %q is still at %s — it is not part of the cluster and was not removed.\n"+
					"  Keep it for as long as you keep any backup of this instance's databases; delete it only when those are gone.",
				opts.Instance, p))
		}
	}
	return nil
}

// refuseUnreadable stops a command that has a record it cannot trust.
//
// 🔴 REFUSING IS THE POINT. The alternative — falling back to the kind-<instance> guess —
// is what made a corrupt record dangerous: the guess is Managed:true, so destroy would
// delete a cluster named after the instance, which for a rig instance is either nothing
// at all or somebody else's cluster. There is a safe manual route out (name the context,
// or delete the record and accept the guess knowingly), and the error says both.
func refuseUnreadable(source BindingSource, instance string) error {
	if source != BindingUnreadable {
		return nil
	}
	return fmt.Errorf(
		"instance %q has a cluster record that cannot be read, so which cluster it lives in is unknown.\n"+
			"  Refusing to fall back to guessing %q from the instance name — that guess would delete whatever cluster\n"+
			"  happens to carry that name. Either pass --kube-context to say where it is, or remove\n"+
			"  ~/.devicechain/%s/%s to accept the guess deliberately",
		instance, instance, instance, instanceRecordFile)
}
