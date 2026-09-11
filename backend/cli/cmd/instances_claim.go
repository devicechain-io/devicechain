// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// 🔴 BOTH COMMANDS TAKE --kube-context, AND THAT IS NOT A CONVENIENCE FLAG.
// These exist for the second operator — the one on a different machine, which is
// the entire premise of putting the declaration in the cluster. That machine has
// no local instance record, so every other verb's "look up which cluster this
// instance lives in" returns nothing. A reclaim command that could only run on
// the machine that already holds the lock would be furniture.
var (
	reclaimKubeContext string
	releaseKubeContext string
	releaseAssumeYes   bool
)

var instancesReclaimCmd = &cobra.Command{
	Use:   "reclaim",
	Short: "Take the cluster lock from an operator whose run has stopped",
	Long: `Takes the lock a dcctl run holds while it applies to a cluster.

A run holds the lock and renews it continuously. If the process is killed without
being able to give it back — a lost laptop, a dead SSH session, an OOM — the lock
stays until somebody takes it.

This command refuses until the lock has gone a full lease duration with nothing
touching it, which it verifies by watching rather than by comparing timestamps, so
that two machines whose clocks disagree cannot produce a wrong answer. That check
takes about a minute and there is no way to skip it.

🔴 It cannot tell a dead process from one that is merely stopped — a suspended VM
or a closed laptop lid looks exactly the same from here, and no amount of waiting
changes that. Confirm the other process is really gone before you take its lock.
You will be asked to type the holder's identity back, which is there to make you
read it.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReclaim(cmd.Context(), os.Stdout, os.Stdin, reclaimKubeContext)
	},
	SilenceUsage: true,
}

var instancesReleaseCmd = &cobra.Command{
	Use:   "release <instance>",
	Short: "Remove an instance declaration without destroying the instance",
	Long: `Removes the finalizer from an instance declaration and deletes it.

🔴 THIS DESTROYS NOTHING. The namespaces, the databases, the volumes and the
workloads are all still there afterwards, and dcctl will no longer have a
declaration describing them. That is the point: it is the escape hatch for a
declaration that cannot be deleted because the operator that would clear its
finalizer is gone.

If what you want is to remove the instance, use ` + "`dcctl destroy`" + `, which
clears the finalizer itself as its last step.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRelease(cmd.Context(), os.Stdout, os.Stdin, releaseKubeContext, args[0])
	},
	SilenceUsage: true,
}

func init() {
	instancesReclaimCmd.Flags().StringVar(&reclaimKubeContext, "kube-context", "",
		"kube context of the cluster whose lock to take (required off the machine that bootstrapped it)")
	instancesReleaseCmd.Flags().StringVar(&releaseKubeContext, "kube-context", "",
		"kube context of the cluster holding the declaration")
	instancesReleaseCmd.Flags().BoolVar(&releaseAssumeYes, "yes", false,
		"skip the confirmation prompt")
	instancesCmd.AddCommand(instancesReclaimCmd)
	instancesCmd.AddCommand(instancesReleaseCmd)
}

func runReclaim(ctx context.Context, out io.Writer, in io.Reader, kubeContext string) error {
	ns, typed, err := bootstrap.ClaimClients(kubeContext)
	if err != nil {
		return err
	}

	lease, err := bootstrap.PeekClaim(ctx, typed, ns)
	if err != nil {
		return fmt.Errorf("reading the cluster lock: %w", err)
	}
	if lease == nil {
		fmt.Fprintln(out, "this cluster is not claimed; there is nothing to reclaim")
		return nil
	}

	if _, err := confirmHolder(out, in, lease); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nchecking whether the holder is still renewing (this takes about %s)...\n",
		bootstrap.ClaimLeaseDuration())
	claim, err := bootstrap.Reclaim(ctx, typed, ns, kubeContext)
	if err != nil {
		return err
	}
	// Nothing else runs under this lock, so hand it straight back. The point of
	// the command is to clear a stranded lock, not to hold one.
	claim.Release(ctx)
	fmt.Fprintln(out, color.HiGreenString("the cluster lock is now free"))
	return nil
}

func runRelease(ctx context.Context, out io.Writer, in io.Reader, kubeContext, instance string) error {
	if kubeContext == "" {
		rec, err := bootstrap.ReadInstanceRecord(instance)
		if err == nil && rec.KubeContext != "" {
			kubeContext = rec.KubeContext
		}
	}
	if err := bootstrap.ValidateInstanceName(instance); err != nil {
		return err
	}

	fmt.Fprintln(out, color.HiYellowString(
		"This removes the declaration for instance %q and nothing else.", instance))
	fmt.Fprintln(out, "Its namespaces, databases, volumes and workloads stay exactly where they are,")
	fmt.Fprintln(out, "and dcctl will no longer have a record of what they belong to.")
	fmt.Fprintln(out, "To remove the instance itself, run `dcctl destroy` instead.")

	if !releaseAssumeYes {
		fmt.Fprintf(out, "\nType the instance name to confirm:\n> ")
		answer, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && answer == "" {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if strings.TrimSpace(answer) != instance {
			return fmt.Errorf("that does not match %q; the declaration was left alone", instance)
		}
	}

	released, err := bootstrap.ReleaseInstanceDeclaration(ctx, kubeContext, instance)
	if err != nil {
		return err
	}
	if !released {
		fmt.Fprintf(out, "the declaration for %q carried no finalizer; nothing to do\n", instance)
		return nil
	}
	fmt.Fprintln(out, color.HiGreenString("the declaration was removed; the instance itself is untouched"))
	return nil
}

// confirmHolder prints who holds the lock and requires their identity to be typed
// back, returning it on success.
//
// Extracted from runReclaim so it can be tested: everything around it needs a live
// cluster, and this is the part with the decisions in it. The refusal of an empty
// holder is one of them, and an untestable refusal is one that gets deleted.
func confirmHolder(out io.Writer, in io.Reader, lease *coordinationv1.Lease) (string, error) {
	holder := ""
	if lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}
	if holder == "" {
		// 🔴 An empty holder would make the typed confirmation a bare Enter, which
		// turns the one deliberate piece of friction in this command into none at
		// all. A Lease with no holder is not something dcctl writes, so refuse
		// rather than invent a ceremony around it.
		return "", fmt.Errorf("the cluster lock names no holder, so there is nothing to confirm against; " +
			"inspect it with `kubectl get lease -n dc-k8s-system dcctl -o yaml` before removing it by hand")
	}

	fmt.Fprintf(out, "  held by:   %s\n", color.HiYellowString(holder))
	if inst := lease.Annotations["core.devicechain.io/instance"]; inst != "" {
		fmt.Fprintf(out, "  instance:  %s\n", inst)
	}
	if lease.Spec.RenewTime != nil {
		fmt.Fprintf(out, "  renewed:   %s ago\n", time.Since(lease.Spec.RenewTime.Time).Round(time.Second))
	}

	// 🔴 THE HOLDER IS TYPED BACK, NOT CONFIRMED WITH A KEYSTROKE, and the two are
	// not interchangeable. A y/N prompt adds ceremony and no information: the answer
	// is the same whether or not the operator read the line above it. This command's
	// one real risk is taking a lock from a process that is alive, and the only
	// defence against that is making them look at whose it is.
	//
	// There is deliberately no --yes. An unattended reclaim is exactly the thing
	// that must not exist, because the judgement it needs — "I know that process is
	// gone" — is not one a flag can carry.
	fmt.Fprintf(out, "\nType the holder identity above to take the lock, or anything else to abort:\n> ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return "", fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(answer) != holder {
		return "", fmt.Errorf("that does not match the holder identity; the lock was left alone")
	}
	return holder, nil
}
