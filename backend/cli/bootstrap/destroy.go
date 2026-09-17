// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/devicechain-io/dcctl/dcdir"
	"github.com/fatih/color"
)

// errDestroyAborted signals that the OPERATOR declined, as distinct from a completed
// operation. They are different outcomes and used to be the same return value, which let
// a declined uninstall fall through to deleting the instance's local state.
var errDestroyAborted = errors.New("aborted by operator")

// DestroyOptions drives an instance destroy.
type DestroyOptions struct {
	Options
	// WithoutState removes the instance without running `tofu destroy` over its
	// infrastructure state: the chart release, the database and login, and the
	// namespace. For an instance whose state is lost, or cannot be destroyed from.
	WithoutState bool
}

// Destroy removes one DeviceChain instance — the inverse of bootstrap: its Helm release,
// its own infrastructure (`tofu destroy` over the instance root), its database and login
// on the shared relational store, its namespace, and its local state (root-key escrow
// spared).
//
// 🔴 IT NEVER DELETES THE CLUSTER, OR ANYTHING ELSE THE CLUSTER'S OTHER INSTANCES SHARE.
// A cluster holds many instances and the prerequisites `dcctl install` put there serve
// all of them, so no single instance's teardown may take them down — whether or not dcctl
// created the cluster. Deleting a cluster is done with the tool that made it.
func Destroy(ctx context.Context, provider Provider, opts DestroyOptions) error {
	fmt.Println(GreenUnderline(fmt.Sprintf("\nDestroy instance %q on provider %q", opts.Instance, provider.Name())))

	binding, source := ResolveBinding(opts.Options)
	announceBinding(binding, source, opts.Instance)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}

	// 🔴 THE DRY-RUN GUARD SITS ABOVE EVERY BRANCH, AND IT MUST. An earlier version of
	// this function put the adopted branch first and the guard after it, so
	// `--dry-run` on any instance bootstrapped with --kube-context fell into the adopted
	// path and REMOVED ~/.devicechain/instances/<instance> — tfstate and all — under a flag whose
	// entire promise is "print what would happen without destroying anything". A flag
	// that destroys is worse than no flag.
	if opts.DryRun {
		infra := "run tofu destroy over the instance's infrastructure state (its broker and event store), "
		if opts.WithoutState {
			infra = "SKIP tofu destroy (--without-state), "
		}
		wouldDo(fmt.Sprintf(
			"uninstall the instance release, %sdrop its database and login from the shared relational store, "+
				"delete namespace %s and wait until it is gone, and remove ~/.devicechain/instances/%s "+
				"(root-key escrow kept), LEAVING cluster %s running",
			infra, opts.Instance, opts.Instance, binding.describe()))
		return nil
	}

	// 🔴 THE ORDER MATTERS, AND GETTING IT WRONG RE-CREATES THE ORPHAN. A cluster is very
	// often ALREADY GONE — a rig deletes its own cluster on the way out and leaves the
	// instance's state behind, which is how nine orphaned directories accumulated.
	// Uninstalling first would then fail on an unreachable cluster and abort before the
	// state was cleared, so the one command that could tidy up would refuse to, for
	// exactly the instances that need it.
	//
	// So: ask first. Cluster gone ⇒ there is nothing to uninstall and clearing the state
	// is the whole job. Cluster present ⇒ uninstall, and a failure there is REAL — the
	// instance is still deployed, so the state must survive to describe it, and the error
	// is returned rather than swallowed.
	//
	// 🔴 ONLY A NAMED CLUSTER MAY BE DECLARED GONE. For a binding carrying no cluster
	// name, ClusterExists falls back to "is this context still in kubeconfig", and a
	// kubeconfig entry's absence is not a cluster's absence — a different KUBECONFIG
	// would look exactly like a deleted cluster, and clearing the state on that reading
	// would throw away the tfstate of a live instance. So an unnamed binding skips the
	// shortcut and goes through the uninstall, which fails loudly if the cluster really
	// is unreachable.
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
			if err := removeGoneClusterState(binding); err != nil {
				return err
			}
			// 🔴 NOT "destroyed". "Destroyed" over a cluster nobody touched is precisely
			// the sentence this command was first fixed for.
			fmt.Println(color.HiGreenString("\nInstance %q removed; its cluster %s was already gone.",
				opts.Instance, binding.describe()))
			return nil
		}
	}

	opts.KubeContext = binding.KubeContext

	// 🔴 THE EMPTY-STATE REFUSAL COMES BEFORE THE FIRST CHANGE — before the prompt, the
	// lock and the uninstall — because a refusal after any of them has already removed
	// part of what it refuses to remove. See refuseStatelessLiveInstance.
	stateHasResources := false
	if opts.WithoutState {
		if n, err := instanceRootStateResources(opts.Instance); err == nil && n > 0 {
			fmt.Println(withoutStateWarning(opts.Instance, n))
		}
	} else {
		var err error
		if stateHasResources, err = refuseStatelessLiveInstance(ctx, opts.KubeContext, opts.Instance); err != nil {
			return err
		}
	}

	// 🔴 RETURNING ON AN OUTCOME IS WHAT KEEPS THE CLOSING LINE TRUE. An abort is not a
	// completed uninstall: uninstallInstance once returned nil for both, so declining the
	// confirmation still fell through to removeInstanceState — deleting the tfstate of an
	// instance the operator had just said to leave alone. And the nothing-here outcome has
	// already removed the local state and said what happened; falling through would remove
	// it a second time and then claim a destroy over a cluster nothing touched.
	leftDatabase, err := uninstallInstance(ctx, opts, stateHasResources)
	if err != nil {
		if destroyNeedsNoFurtherWork(err) {
			return nil
		}
		return err
	}
	// LAST, and only here: every failure above returns first, so a destroy that did not
	// finish keeps the state a re-run resumes from.
	if err := removeInstanceState(opts); err != nil {
		return err
	}
	fmt.Println(destroyedLine(opts.Instance, binding.describe(), leftDatabase, opts.WithoutState))
	return nil
}

// destroyedLine is the closing line of a destroy that uninstalled the instance.
func destroyedLine(instance, cluster, leftDatabase string, withoutState bool) string {
	if withoutState {
		// 🔴 NOT GREEN EITHER. Nothing checked that the instance's infrastructure was
		// all in its namespace; the operator asked for that, and the line says so.
		return color.YellowString("\nInstance %q removed WITHOUT tofu destroy (--without-state); cluster %s left running. "+
			"Anything of its infrastructure outside namespace %s was not removed.%s",
			instance, cluster, instanceNamespace(instance), leftDatabaseNote(leftDatabase))
	}
	if leftDatabase != "" {
		// 🔴 NOT GREEN. Something of this instance is still on the shared store, and a
		// closing line that says it is gone is the sentence this command keeps being
		// fixed for.
		return color.YellowString("\nInstance %q uninstalled; cluster %s left running. Its database, "+
			"if it has one, was LEFT on the shared relational store: %s", instance, cluster, leftDatabase)
	}
	return color.HiGreenString("\nInstance %q destroyed; cluster %s left running.", instance, cluster)
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
				"  with --kube-context, this guess targets the WRONG cluster: pass --kube-context to name the right one.",
			instance, binding.Cluster, binding.KubeContext))
	}
}

// leftDatabaseNote is the database half of a closing line, empty when it was dropped.
func leftDatabaseNote(leftDatabase string) string {
	if leftDatabase == "" {
		return ""
	}
	return " Its database, if it has one, was LEFT on the shared relational store: " + leftDatabase
}

// uninstallInstance removes the instance from its cluster, in this order: the chart
// release, the instance root's infrastructure (`tofu destroy`, unless there is nothing in
// state or the operator said --without-state), its database and login on the shared
// relational store, and its namespace — waited on until it is gone. It returns a
// non-empty reason when the database was left on the store.
func uninstallInstance(ctx context.Context, opts DestroyOptions, stateHasResources bool) (leftDatabase string, err error) {
	kubeContext := opts.KubeContext
	if !opts.AssumeYes && !confirm(fmt.Sprintf(
		"Destroy instance %q? This deletes the instance and ALL ITS DATA; the cluster and its shared prerequisites stay",
		opts.Instance)) {
		fmt.Println(color.YellowString("Aborted."))
		return "", errDestroyAborted
	}

	// Lock and intent BEFORE the first deletion. See beginDestroy.
	claim := beginDestroy(ctx, kubeContext, opts.Instance)
	defer func() { endDestroy(ctx, claim, kubeContext, opts.Instance, true, &err) }()

	doing("uninstalling instance release (Helm)")
	if err := helmUninstall(ctx, kubeContext, opts.Instance); err != nil {
		return "", uninstallOutcome(err, func() error {
			return resolveForeignRelease(ctx, kubeContext, opts, err)
		})
	}
	done()

	// 🔴 THE INSTANCE'S OWN INFRASTRUCTURE, AFTER THE CHART AND BEFORE THE DATABASE. The
	// services are gone, so nothing is publishing to the broker or writing to the event
	// store while they are destroyed; and the namespace delete below stays as the
	// backstop for anything in it that tofu never managed.
	switch {
	case opts.WithoutState:
		fmt.Println(color.YellowString("  tofu destroy SKIPPED (--without-state): the instance's infrastructure is removed only with its namespace"))
	case !stateHasResources:
		fmt.Println(color.WhiteString("  the instance's infrastructure state lists nothing — no tofu destroy to run"))
	default:
		fmt.Println(color.WhiteString("destroying the instance's infrastructure (tofu destroy)..."))
		if err := destroyInstanceRoot(ctx, kubeContext, opts.Instance); err != nil {
			return "", fmt.Errorf("destroying the instance's infrastructure: %w", err)
		}
	}
	// 🔴 AND THE NAMESPACE, WHICH THE UNINSTALL DOES NOT ALWAYS REACH. dcctl writes the
	// instance configuration Secret — the root key with it — before Helm installs
	// anything, so a run that died in between leaves a namespace with no release to
	// uninstall, and the uninstall above reports that as success. See
	// removeInstanceNamespace for why the leftover makes this very command the remedy
	// that does not work.
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return "", fmt.Errorf("connecting to the cluster to remove the instance namespace: %w", err)
	}
	// 🔴 AND ITS DATABASE AND LOGIN ON THE SHARED STORE, which nothing above reaches: the
	// store is the cluster's, so uninstalling the instance leaves both behind — and a
	// later instance by the same name would be refused over a database it did not create.
	// After the uninstall, which ends the services' sessions on it.
	doing("dropping the instance's database and login")
	leftDatabase, err = removeInstanceRelationalLogin(ctx, typed, kubeContext, opts.Instance)
	if err != nil {
		return "", fail("removing the instance's database and login", err)
	}
	done()
	doing(fmt.Sprintf("deleting namespace %s and waiting until it is gone", instanceNamespace(opts.Instance)))
	deleted, err := removeInstanceNamespace(ctx, typed, opts.Instance)
	if err != nil {
		return "", fail("removing the instance namespace", err)
	}
	// 🔑 WAITED ON ONLY WHEN THIS RUN DELETED IT. A namespace left alone because it is not
	// labelled as this instance's is not going anywhere, and was already reported.
	if deleted {
		if err := waitForNamespaceGone(ctx, typed, instanceNamespace(opts.Instance), namespaceGoneTimeout, namespaceGonePollEach); err != nil {
			return "", fail("waiting for the instance namespace to be deleted", err)
		}
	}
	done()
	return leftDatabase, nil
}

// removeInstanceState removes the instance's persisted local state, sparing root-key
// escrow, and reports what it spared.
//
// Every path that finishes a destroy reaches it, including the ones that leave a cluster
// running: the OpenTofu state, the instance record and everything else under
// ~/.devicechain/instances/<instance> describe an instance that no longer exists, and
// leaving them behind is how nine orphaned state directories accumulated on one machine
// with nothing able to report them.
func removeInstanceState(opts DestroyOptions) error {
	doing(fmt.Sprintf("removing local state (~/.devicechain/instances/%s)", opts.Instance))
	var keptEscrow []string
	var removeErr error
	if dir, err := instanceRoot(opts.Instance); err == nil {
		keptEscrow, removeErr = removeStatePreservingEscrow(dir)
	}
	// Anything spared is named BEFORE the error is returned. A partially removed tree
	// is exactly when an operator needs to know what survived in it, and reporting
	// only on the success path meant the failure case said nothing.
	for _, p := range keptEscrow {
		fmt.Println(color.YellowString("  kept root-key escrow %s — the instance is gone but this still opens its database backups", p))
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

// removeGoneClusterState removes ~/.devicechain/clusters/<uid> for a cluster that no
// longer exists. Call it ONLY once the cluster is gone.
//
// 🔴 THE SPLIT MADE THIS NECESSARY. While the prerequisites were applied from the
// instance's own root, removeInstanceState took all of the infrastructure state with it.
// Once they moved to a root keyed on the cluster, that half outlived the cluster, and
// each kind rebuild — a new cluster, so a new UID — left one more behind. Found on the
// first live round-trip; no test was looking.
//
// 🔑 KEYED ON WHETHER THE CLUSTER IS GONE, NOT ON WHICH COMMAND RAN. State filed under a
// dead cluster's UID describes nothing and the UID cannot recur. A cluster that is still
// running still has its prerequisites installed, and this state is the only description
// of them — so a destroy that finds the cluster present never calls this.
//
// No UID recorded ⇒ nothing is removed. The instance predates the identity, so no
// directory was ever filed under it; and an empty key must never reach dcdir.Cluster's
// caller as a path, because it joins to the clusters directory itself.
func removeGoneClusterState(binding ClusterBinding) error {
	if binding.ClusterUID == "" {
		return nil
	}
	dir, err := dcdir.Cluster(binding.ClusterUID)
	if err != nil {
		return fail("resolving the cluster's local state", err)
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	doing(fmt.Sprintf("removing the gone cluster's local state (~/.devicechain/clusters/%s)", binding.ClusterUID))
	if err := os.RemoveAll(dir); err != nil {
		return fail("removing the cluster's local state", err)
	}
	done()
	return nil
}

// refuseUnreadable stops a command that has a record it cannot trust.
//
// 🔴 REFUSING IS THE POINT. The alternative — falling back to the kind-<instance> guess —
// is what made a corrupt record dangerous: destroy would act on a cluster named after the
// instance, which for a rig instance is either nothing at all — so its state is cleared
// as "already gone" over a live instance — or somebody else's cluster. There is a safe manual route out (name the context,
// or delete the record and accept the guess knowingly), and the error says both.
func refuseUnreadable(source BindingSource, instance string) error {
	if source != BindingUnreadable {
		return nil
	}
	return fmt.Errorf(
		"instance %q has a cluster record that cannot be read, so which cluster it lives in is unknown.\n"+
			"  Refusing to fall back to guessing %q from the instance name — that guess would act on whatever cluster\n"+
			"  happens to carry that name. Either pass --kube-context to say where it is, or remove\n"+
			"  ~/.devicechain/instances/%s/%s to accept the guess deliberately",
		instance, instance, instance, instanceRecordFile)
}
