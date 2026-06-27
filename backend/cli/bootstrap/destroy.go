// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"

	"github.com/fatih/color"
)

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
		return destroyInstanceOnly(ctx, opts)
	}
	return destroyEverything(ctx, provider, opts)
}

// destroyInstanceOnly removes just the instance's Helm release, leaving the
// cluster and platform (infra + operator) warm for a fast re-bootstrap.
func destroyInstanceOnly(ctx context.Context, opts DestroyOptions) error {
	kubeContext := destroyContext(opts.Options)

	fmt.Println(GreenUnderline(fmt.Sprintf("\nUninstall instance %q (keeping cluster %s)", opts.Instance, kubeContext)))
	if opts.DryRun {
		wouldDo("helm uninstall the instance release and delete namespace " + opts.Instance)
		return nil
	}
	if !opts.AssumeYes && !confirm(fmt.Sprintf(
		"Uninstall instance %q? The cluster, infrastructure and operator stay in place", opts.Instance)) {
		fmt.Println(color.YellowString("Aborted."))
		return nil
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
	if opts.DryRun {
		wouldDo(fmt.Sprintf("delete the cluster for instance %q and remove ~/.devicechain/%s", opts.Instance, opts.Instance))
		if opts.PurgeRegistry {
			wouldDo("remove the shared local image registry container")
		}
		return nil
	}
	if !opts.AssumeYes && !confirm(fmt.Sprintf(
		"Permanently destroy instance %q AND its cluster? This deletes ALL of its data", opts.Instance)) {
		fmt.Println(color.YellowString("Aborted."))
		return nil
	}

	doing(fmt.Sprintf("deleting cluster for instance %q", opts.Instance))
	if err := provider.DestroyCluster(ctx, opts.Options); err != nil {
		return fail("deleting cluster", err)
	}
	done()

	// The cluster is gone, so its persisted tofu state now points at nothing —
	// remove the whole per-instance state directory.
	doing(fmt.Sprintf("removing local state (~/.devicechain/%s)", opts.Instance))
	if dir, err := instanceRoot(opts.Instance); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			return fail("removing local state", err)
		}
	}
	done()

	if opts.PurgeRegistry {
		doing("removing local image registry container")
		_ = removeLocalRegistry(ctx) // best-effort: a missing container is fine
		done()
	}

	fmt.Println(color.HiGreenString("\nInstance %q destroyed.", opts.Instance))
	return nil
}

// destroyContext resolves the kube-context to act on for an instance-only
// teardown. It mirrors localProvider.EnsureCluster's kind-<instance> convention
// (an explicit --kube-context wins) but never creates anything.
func destroyContext(opts Options) string {
	if opts.KubeContext != "" {
		return opts.KubeContext
	}
	return "kind-" + opts.Instance
}
